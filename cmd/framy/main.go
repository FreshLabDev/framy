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
	"github.com/FreshLabDev/framy/internal/anim"
	"github.com/FreshLabDev/framy/internal/api"
	"github.com/FreshLabDev/framy/internal/auth"
	"github.com/FreshLabDev/framy/internal/bot"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/chat"
	"github.com/FreshLabDev/framy/internal/config"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/health"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/metrics"
	"github.com/FreshLabDev/framy/internal/oauth"
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
// SIGTERM by default; the stack sets stop_grace_period above this. It is
// longer than bot.UpdateGrace, which the commands already taken get.
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
		// it out of the default set, so it has to be asked for. So does
		// edited_message, which can swap the file of an album's item.
		tg.WithAllowedUpdates("message", "edited_message", "callback_query", "my_chat_member"),
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
		// deleteMessage removes a key a person pasted into a direct chat.
		Methods: []string{"editEphemeralMessageText", "deleteEphemeralMessage", "deleteMessage"},
	})
	if err != nil {
		return err
	}
	log.Info("telegram preflight passed", "username", me.Username, "bot_api", tg.BotAPI)

	userAgent := "framy/" + version + " (+https://github.com/FreshLabDev/framy)"
	files, work, err := openStorage(ctx, cfg, store, client, log)
	if err != nil {
		return err
	}
	// People's own keys (#13). Without a KEK file there are none, and a run
	// that needs one fails as key. Track writes what each run shows about its
	// key as the connection's status.
	people, err := openKeys(ctx, cfg, store, userAgent, log)
	if err != nil {
		return err
	}
	jobsCfg := jobs.Config{
		Providers: map[string]jobs.Provider{
			catalog.OpenRouter: people.Track(openrouter.New(openrouter.Config{
				Base:      cfg.OpenRouterAPIBase,
				UserAgent: userAgent,
				Referer:   "https://github.com/FreshLabDev/framy",
				Title:     "framy",
				Key:       openRouterKey(cfg.OpenRouterKey, people.JobKey),
				// A finished video may be redirected to a link OpenRouter
				// signed: the client refuses private addresses.
				Fetch:     safehttp.Client(storage.DefaultFetchTimeout),
				Callbacks: openRouterCallbacks(cfg.Hooks),
			})),
			// Only a person's own key pays at OpenAI and Gemini, never
			// framy's: JobKey refuses a free run.
			catalog.OpenAI: people.Track(openai.New(openai.Config{
				UserAgent: userAgent,
				Key:       people.JobKey,
			})),
			catalog.Gemini: people.Track(gemini.New(gemini.Config{
				UserAgent: userAgent,
				Key:       people.JobKey,
			})),
		},
		Log:       log.With("component", "jobs"),
		StopGrace: cfg.JobsStopGrace,
	}
	// Without a bucket a job ends as storage before anything is paid.
	if files != nil {
		jobsCfg.Sink, jobsCfg.Inputs = files, files
	}
	// Every free job asks the allowance before it is created or queued.
	allowance, keyWatch, err := openAllowance(cfg, store, client, userAgent, log)
	if err != nil {
		return err
	}
	defer allowance.Wait()
	jobsCfg.Admit = allowance
	queue, err := jobs.NewQueue(store.Pool(), jobsCfg)
	if err != nil {
		return err
	}
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start the job queue: %w", err)
	}
	// /img and /edit in chats (#24) make jobs on the queue and keep their
	// photos in the bucket.
	flow, err := openFlow(ctx, cfg, client, store, queue, files, work, allowance, people, log)
	if err != nil {
		return err
	}
	// The bot comes after the allowance, the keys and the flow: the home
	// panel of a direct chat shows what is left of the one and what is
	// connected of the other, and the flow answers /img.
	opts := []bot.Option{bot.WithAllowance(allowance), bot.WithConnections(people), bot.WithSiteURL(cfg.SiteURL)}
	if flow != nil {
		opts = append(opts, bot.WithFlow(flow))
	}
	b := bot.New(store, client, log, me.Username, me.ID, version, opts...)
	if flow != nil {
		flow.SetAlbums(b)
	}

	// Sign-in (#18): the site through Telegram Login, the Mini App through its
	// initData. Without the Login client the site's routes answer 503 and the
	// rest runs.
	signIn, err := auth.New(auth.Config{
		ClientID:     cfg.TelegramLogin.ClientID,
		ClientSecret: cfg.TelegramLogin.ClientSecret,
		SiteURL:      cfg.SiteURL,
		BotToken:     cfg.TelegramBotToken,
		Sessions:     auth.NewPGSessions(store.Pool()),
		People:       store,
		Log:          log.With("component", "auth"),
	})
	if err != nil {
		return err
	}
	if !signIn.SiteEnabled() {
		log.Warn("no Telegram Login client: people cannot sign in on the site (TELEGRAM_LOGIN_CLIENT_ID, TELEGRAM_LOGIN_CLIENT_SECRET, SITE_URL)")
	}

	started := time.Now()
	// Every route of the public listener is registered on site: it adds the
	// security headers, the CSRF check, the cache rules and the rate limits.
	site, err := web.New(web.Config{
		Log:            log.With("component", "web"),
		R2Endpoint:     cfg.R2.Endpoint,
		TrustedProxies: cfg.Web.TrustedProxies,
		Person:         signIn.PersonKey,
	})
	if err != nil {
		return err
	}
	site.Handle("GET /{$}", web.Placeholder())
	signIn.Mount(site)
	site.Handle("/healthz", health.New(store, b.LastPoll, b.Initialized, started, health.Build{
		Version: version, Commit: commit, Date: date,
	}, log))
	// Connect OpenRouter with OAuth PKCE (#12). The routes exist when the
	// callback URL is known (SITE_URL) and the key they end with can be kept
	// (a KEK file).
	if cfg.SiteURL != "" && people != nil {
		connect, err := oauth.New(oauth.Config{
			Store: oauth.NewPGStore(store.Pool()),
			Keys:  people,
			// Nobody signed in is sent to /auth/login and comes back here.
			Person:    signIn.Person,
			SiteURL:   cfg.SiteURL,
			APIBase:   cfg.OpenRouterAPIBase,
			UserAgent: userAgent,
			Log:       log.With("component", "oauth"),
		})
		if err != nil {
			return err
		}
		site.HandleFunc("GET "+oauth.StartPath, connect.Start)
		// The callback exchanges the code and checks the key, two calls to
		// OpenRouter: more than the server's ten seconds to answer.
		site.HandleFunc("GET "+oauth.CallbackPath, connect.Callback, web.Deadlines(10*time.Second, 45*time.Second))
	} else {
		log.Warn("connecting OpenRouter is off: it needs SITE_URL and FRAMY_KEK_FILE")
	}

	// The job API: uploads, starting a job, reading it, and following it over
	// SSE (#16), for whoever sign-in names.
	var uploads api.Uploads
	var links api.Links
	if files != nil {
		uploads, links = files, files
	}
	jobAPI, err := api.New(api.Config{
		Pool: store.Pool(), Queue: queue, Store: store, Uploads: uploads, Links: links, Keys: people,
		Person: signIn.Person, Log: log.With("component", "api"),
	})
	if err != nil {
		return err
	}
	jobAPI.Register(site)
	// The listener ends with ctx, and ends every open stream with it, so that
	// a stop is not held up by a connection that has no end of its own.
	go jobAPI.Run(ctx)

	// OpenRouter's video webhooks (#32).
	mountOpenRouterHook(site, cfg.Hooks, store.Pool(), queue, log)

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
	if files != nil {
		workers.Go(func() { sweepUploads(ctx, files, log) })
	}
	if keyWatch != nil {
		workers.Go(func() { keyWatch.Run(ctx) })
	}
	if flow != nil {
		workers.Go(func() {
			flow.Run(ctx)
			flow.Wait()
		})
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
	// The poller stops taking updates on the canceled context and works the
	// ones it took for up to bot.UpdateGrace; the server stops accepting and
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
		Prefix: cfg.R2.Prefix,
		Media:  conv,
		Work:   work,
		Fetch:  safehttp.Client(storage.DefaultFetchTimeout),
		Notify: notify,
		Log:    log.With("component", "storage"),
	})
	if err != nil {
		return nil, work, err
	}
	log.Info("storage ready", "bucket", cfg.R2.Bucket, "prefix", cfg.R2.Prefix, "work_dir", cfg.WorkDir)
	return files, work, nil
}

// openFlow sets up /img and /edit. Without the bucket, or with an ffmpeg that
// cannot draw the loops, they are off and the rest runs.
func openFlow(ctx context.Context, cfg config.Config, client *tg.Client, store *db.Store, queue *jobs.Queue,
	files *storage.Store, work media.Work, allowance *free.Allowance, people *keys.Service, log *slog.Logger) (*chat.Flow, error) {
	if files == nil {
		log.Warn("no bucket, so /img is off: the photos a run takes and its results are kept there")
		return nil, nil
	}
	renderer := &anim.Renderer{FFmpeg: cfg.FFmpeg, Cache: cfg.AnimCache}
	if err := renderer.Check(ctx); err != nil {
		log.Error("the loops cannot be drawn, so /img is off", "error", err)
		return nil, nil
	}
	return chat.New(chat.Config{
		Telegram: client, Store: store, Queue: queue, Files: files, Allowance: allowance, Keys: people,
		Renderer: renderer, Work: work, Log: log.With("component", "chat"),
	})
}

// sweepUploads deletes the uploads no job took within storage.UploadTTL:
// at start and every hour.
func sweepUploads(ctx context.Context, files *storage.Store, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := files.SweepUploads(ctx, storage.UploadTTL); err != nil {
			log.Error("sweep uploads no job took", "swept", n, "error", err)
		} else if n > 0 {
			log.Info("swept uploads no job took", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
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
// allowance, the person's connection for the rest. A run never falls back
// from one to the other.
func openRouterKey(own string, people func(context.Context, jobs.Job) (string, error)) func(context.Context, jobs.Job) (string, error) {
	return func(ctx context.Context, j jobs.Job) (string, error) {
		switch {
		case !j.Free:
			return people(ctx, j)
		case own == "":
			return "", &jobs.Error{Class: jobs.Key, Err: errors.New("OPENROUTER_API_KEY is not set, so the free allowance cannot run")}
		}
		return own, nil
	}
}

// openAllowance builds the free allowance. With framy's own OpenRouter key it
// also builds the watch that reads the key's limit, which the caller runs.
func openAllowance(cfg config.Config, store *db.Store, client *tg.Client, userAgent string, log *slog.Logger) (*free.Allowance, *free.KeyWatch, error) {
	log = log.With("component", "free")
	fc := free.Config{Pool: store.Pool(), DailyBudgetUSD: cfg.FreeDailyBudgetUSD, Log: log}
	var watch *free.KeyWatch
	if cfg.OpenRouterKey != "" {
		reader := openrouter.New(openrouter.Config{Base: cfg.OpenRouterAPIBase, UserAgent: userAgent})
		watch = free.NewKeyWatch(func(ctx context.Context) (free.KeyState, error) {
			info, err := reader.KeyInfo(ctx, cfg.OpenRouterKey)
			if err != nil || info.Remaining == nil {
				// A key without a limit has none to run out of.
				return free.KeyState{}, err
			}
			return free.KeyState{Limited: true, RemainingUSD: *info.Remaining, Daily: info.Reset == "daily"}, nil
		}, free.DefaultKeyEvery, log)
		fc.Key = watch
	}
	if cfg.OperatorChatID != 0 {
		fc.Notify = func(ctx context.Context, text string) error {
			_, err := client.SendPlainText(ctx, cfg.OperatorChatID, text)
			return err
		}
	}
	allowance, err := free.New(fc)
	if err != nil {
		return nil, nil, err
	}
	return allowance, watch, nil
}

// openKeys loads the KEK file and rotates what is wrapped under an older
// version of it. Without a file it returns no service, which is a framy
// that keeps no keys. A KEK file that cannot be read stops the start: it
// is the operator's mistake, and running without it would look like every
// stored key being gone.
func openKeys(ctx context.Context, cfg config.Config, store *db.Store, userAgent string, log *slog.Logger) (*keys.Service, error) {
	if cfg.KEKFile == "" {
		log.Warn("no FRAMY_KEK_FILE: people cannot connect keys of their own, and a run on one fails as key")
		return nil, nil
	}
	kek, err := keys.LoadKEKFile(cfg.KEKFile)
	if err != nil {
		return nil, err
	}
	if kek.Loose() {
		log.Warn("the KEK file can be read by others; keep it at mode 0400", "path", cfg.KEKFile)
	}
	log = log.With("component", "keys")
	svc, err := keys.New(keys.Config{
		Pool:           store.Pool(),
		KEK:            kek,
		UserAgent:      userAgent,
		OpenRouterBase: cfg.OpenRouterAPIBase,
		Log:            log,
	})
	if err != nil {
		return nil, err
	}
	// A failed rotation leaves every row readable under the version it had,
	// so the service starts either way and says what is wrong.
	res, err := svc.Rotate(ctx)
	switch {
	case err != nil:
		log.Error("KEK rotation failed", "error", err, "kek_versions", kek.Versions())
	case res.Failed > 0 || res.Unreadable > 0:
		log.Error("stored keys cannot be read with this KEK file: people have to enter them again, or the lines for their versions have to come back",
			"unreadable", res.Unreadable, "rewrap_failed", res.Failed, "rewrapped", res.Rewrapped, "kek_versions", kek.Versions())
	case res.Rewrapped > 0:
		log.Info("data keys rewrapped under the current KEK; the older lines of the file can go", "rewrapped", res.Rewrapped, "kek_version", res.Current)
	default:
		log.Info("keys ready", "kek_versions", kek.Versions(), "kek_version", res.Current)
	}
	return svc, nil
}
