// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/FreshLabDev/tg"

	catalogfiles "github.com/FreshLabDev/framy/catalog"
	"github.com/FreshLabDev/framy/internal/bot"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/config"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/health"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/metrics"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/providers/openrouter"
	"github.com/FreshLabDev/framy/migrations"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// shutdownTimeout bounds a stop. Docker sends SIGKILL ten seconds after
// SIGTERM by default; the stack sets stop_grace_period above this.
const shutdownTimeout = 20 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(os.Getenv("LOG_LEVEL"))}))
	if err := run(log); err != nil {
		log.Error("exit", "error", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// A broken catalog file stops the start, not a sync an hour later.
	catalogFiles, err := catalog.LoadFiles(catalogfiles.FS)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.AutoMigrate {
		if err := store.Migrate(ctx, migrations.FS, jobs.MigrateRiver); err != nil {
			return err
		}
	}

	client := tg.New(cfg.TelegramBotToken,
		tg.WithAPIBase(cfg.TelegramAPIBase),
		// my_chat_member tells core which groups framy is in; Telegram leaves
		// it out of the default set, so it has to be asked for.
		tg.WithAllowedUpdates("message", "callback_query", "my_chat_member"),
		tg.WithLogger(log),
		tg.WithObserver(func(e tg.Event) {
			switch {
			// A capability probe is answered with a parameter error on
			// purpose; counting it would add a phantom incident per probed
			// method to every start.
			case e.Probe, e.Err == nil:
			case e.Status == http.StatusTooManyRequests:
				metrics.TelegramLimited.Inc(e.Method)
			default:
				metrics.TelegramErrors.Inc(e.Method)
			}
		}),
	)
	// In a group the panel is an ephemeral reply; a server without ephemeral
	// messages would leave /start there unanswered.
	me, err := client.Preflight(ctx, tg.Needs{
		Methods: []string{"editEphemeralMessageText", "deleteEphemeralMessage"},
	})
	if err != nil {
		return err
	}
	log.Info("telegram preflight passed", "username", me.Username, "bot_api", tg.BotAPI)
	b := bot.New(store, client, log, me.Username, version)

	userAgent := "framy/" + version + " (+https://github.com/FreshLabDev/framy)"
	// Until results have storage (#15), a job ends as storage before
	// anything is paid; OpenAI and Gemini have no adapter yet (#10, #11).
	queue, err := jobs.NewQueue(store.Pool(), jobs.Config{
		Providers: map[string]jobs.Provider{
			catalog.OpenRouter: openrouter.New(openrouter.Config{
				Base:      cfg.OpenRouterAPIBase,
				UserAgent: userAgent,
				Referer:   "https://github.com/FreshLabDev/framy",
				Title:     "framy",
				Key:       openRouterKey(cfg.OpenRouterKey),
			}),
		},
		Log:       log.With("component", "jobs"),
		StopGrace: cfg.JobsStopGrace,
	})
	if err != nil {
		return err
	}
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start the job queue: %w", err)
	}

	started := time.Now()
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.New(store, b.LastPoll, b.Initialized, started, health.Build{
		Version: version, Commit: commit, Date: date,
	}, log))
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	servers := []*http.Server{srv}
	if cfg.MetricsAddr != "" {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("GET /metrics", metrics.Handler())
		servers = append(servers, &http.Server{
			Addr:              cfg.MetricsAddr,
			Handler:           metricsMux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
		})
	}

	errCh := make(chan error, len(servers)+1)
	var workers sync.WaitGroup
	for _, s := range servers {
		workers.Go(func() {
			log.Info("http listening", "addr", s.Addr, "version", version)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		})
	}
	if cfg.CatalogEvery > 0 {
		syncer := &catalog.Syncer{
			Source: &catalog.OpenRouterSource{
				Base:      cfg.OpenRouterAPIBase,
				HTTP:      &http.Client{Timeout: 30 * time.Second},
				UserAgent: userAgent,
				Parallel:  4,
			},
			Files: catalogFiles,
			Store: store,
			Log:   log.With("component", "catalog"),
			Every: cfg.CatalogEvery,
			Retry: 10 * time.Minute,
			Check: price.Check,
		}
		workers.Go(func() { syncer.Run(ctx) })
	}
	workers.Go(func() {
		log.Info("telegram polling starting")
		if err := b.Run(ctx); err != nil {
			errCh <- err
		}
	})

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("stopping")
	case runErr = <-errCh:
		stop()
	}
	// The poller ends on the canceled context; the server stops accepting and
	// lets requests in flight finish. Both get the same deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = err
		}
	}
	// Runs in progress get longer than the rest: cutting one short can cost
	// the person a paid run.
	queueStopped := make(chan error, 1)
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), cfg.JobsStopGrace+10*time.Second)
		defer cancel()
		queueStopped <- queue.Stop(stopCtx)
	}()
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		if runErr == nil {
			runErr = errors.New("shutdown timed out")
		}
	}
	if err := <-queueStopped; err != nil && runErr == nil {
		runErr = fmt.Errorf("stop the job queue: %w", err)
	}
	return runErr
}

// connect retries the first PostgreSQL connection. Container DNS inside the
// shared core_net is not always resolvable the instant the process starts, and
// exiting there turns a two-second blip into a restart loop.
func connect(ctx context.Context, url string, log *slog.Logger) (*db.Store, error) {
	const maxAttempts = 8
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		store, err := db.Connect(ctx, url)
		if err == nil {
			return store, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		delay := time.Duration(1<<min(attempt-1, 4)) * time.Second
		log.Warn("database connect failed; retrying", "attempt", attempt, "retry_in", delay, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// openRouterKey pays for OpenRouter runs: framy's own key for the free
// allowance. A person's connection pays for the rest once it exists (#13).
func openRouterKey(own string) func(context.Context, jobs.Job) (string, error) {
	return func(_ context.Context, j jobs.Job) (string, error) {
		switch {
		case !j.Free:
			return "", &jobs.Error{Class: jobs.Key, Err: errors.New("the person has no OpenRouter connection")}
		case own == "":
			return "", &jobs.Error{Class: jobs.Key, Err: errors.New("OPENROUTER_API_KEY is not set, so the free allowance cannot run")}
		}
		return own, nil
	}
}
