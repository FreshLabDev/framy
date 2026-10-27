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
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/health"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/metrics"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/providers/gemini"
	"github.com/FreshLabDev/framy/internal/providers/openai"
	"github.com/FreshLabDev/framy/internal/providers/openrouter"
	"github.com/FreshLabDev/framy/internal/r2"
	"github.com/FreshLabDev/framy/internal/safehttp"
	"github.com/FreshLabDev/framy/internal/storage"
	"github.com/FreshLabDev/framy/internal/web"
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
	files, work, err := openStorage(ctx, cfg, store, client, log)
	if err != nil {
		return err
	}
	jobsCfg := jobs.Config{
		Providers: map[string]jobs.Provider{
			catalog.OpenRouter: openrouter.New(openrouter.Config{
				Base:      cfg.OpenRouterAPIBase,
				UserAgent: userAgent,
				Referer:   "https://github.com/FreshLabDev/framy",
				Title:     "framy",
				Key:       openRouterKey(cfg.OpenRouterKey),
			}),
			catalog.OpenAI: openai.New(openai.Config{
				UserAgent: userAgent,
				Key:       openAIKey(),
			}),
			catalog.Gemini: gemini.New(gemini.Config{
				UserAgent: userAgent,
				Key:       geminiKey,
			}),
		},
		Log:       log.With("component", "jobs"),
		StopGrace: cfg.JobsStopGrace,
	}
	// Without a bucket a job ends as storage before anything is paid.
	if files != nil {
		jobsCfg.Sink, jobsCfg.Inputs = files, files
	}
	queue, err := jobs.NewQueue(store.Pool(), jobsCfg)
	if err != nil {
		return err
	}
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start the job queue: %w", err)
	}

	started := time.Now()
	// Every route of the public listener is registered on site: it adds the
	// security headers, the CSRF check, the cache rules and the rate limits.
	site, err := web.New(web.Config{
		Log:            log.With("component", "web"),
		R2Endpoint:     cfg.R2.Endpoint,
		TrustedProxies: cfg.Web.TrustedProxies,
	})
	if err != nil {
		return err
	}
	site.Handle("GET /{$}", web.Placeholder())
	site.Handle("/healthz", health.New(store, b.LastPoll, b.Initialized, started, health.Build{
		Version: version, Commit: commit, Date: date,
	}, log))

	servers := []*http.Server{site.HTTPServer(cfg.HTTPAddr)}
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
	if cfg.FXSync {
		rates := &fx.Syncer{
			Source: &fx.Client{HTTP: &http.Client{Timeout: 20 * time.Second}, UserAgent: userAgent},
			Store:  store,
			Log:    log.With("component", "fx"),
		}
		workers.Go(func() { rates.Run(ctx) })
	}
	workers.Go(func() { sweepWork(ctx, work, log) })
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

// openStorage connects the bucket and checks ffmpeg. Without an R2 token it
// returns no store, and the service runs without one.
func openStorage(ctx context.Context, cfg config.Config, store *db.Store, client *tg.Client, log *slog.Logger) (*storage.Store, media.Work, error) {
	work := media.Work{Root: cfg.WorkDir}
	if !cfg.R2.Enabled() {
		log.Warn("no R2 token: results cannot be kept, so every job ends as storage before anything is paid")
		return nil, work, nil
	}
	bucket, err := r2.New(r2.Config{
		Endpoint: cfg.R2.Endpoint, Bucket: cfg.R2.Bucket,
		AccessKeyID: cfg.R2.AccessKeyID, SecretAccessKey: cfg.R2.SecretAccessKey,
	})
	if err != nil {
		return nil, work, err
	}
	conv := &media.Converter{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe}
	if err := conv.Check(ctx); err != nil {
		return nil, work, fmt.Errorf("ffmpeg cannot normalize photos: %w", err)
	}
	var notify func(context.Context, string) error
	if cfg.OperatorChatID != 0 {
		notify = func(ctx context.Context, text string) error {
			_, err := client.SendPlainText(ctx, cfg.OperatorChatID, text)
			return err
		}
	}
	files, err := storage.New(storage.Config{
		Pool:   store.Pool(),
		Bucket: bucket,
		Media:  conv,
		Work:   work,
		Fetch:  safehttp.Client(storage.DefaultFetchTimeout),
		Notify: notify,
		Log:    log.With("component", "storage"),
	})
	if err != nil {
		return nil, work, err
	}
	log.Info("storage ready", "bucket", cfg.R2.Bucket, "work_dir", cfg.WorkDir)
	return files, work, nil
}

// sweepWork clears working files a crash left behind: at start and every
// hour, whatever is older than an hour. Nothing framy does takes that long.
func sweepWork(ctx context.Context, work media.Work, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := work.Sweep(time.Hour); err != nil {
			log.Error("sweep working files", "error", err)
		} else if n > 0 {
			log.Warn("swept working files a crash left", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
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

// openAIKey pays for OpenAI runs. Only a person's own key does, never
// framy's, and the store of people's keys does not exist yet (#13): until it
// does, a run fails as key before anything is paid.
func openAIKey() func(context.Context, jobs.Job) (string, error) {
	return func(context.Context, jobs.Job) (string, error) {
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no OpenAI key connected yet")}
	}
}

// geminiKey pays for Gemini runs: only a person's own key does, and the store
// of keys (#13) does not exist yet, so a run fails as key before anything is
// paid. The free allowance never runs on Gemini.
func geminiKey(context.Context, jobs.Job) (string, error) {
	return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no Gemini key connected yet")}
}
