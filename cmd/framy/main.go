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
	"path/filepath"
	"strings"
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
	"github.com/FreshLabDev/framy/internal/watch"
	"github.com/FreshLabDev/framy/internal/web"
	"github.com/FreshLabDev/framy/migrations"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// shutdownTimeout bounds the stop of the servers and the poller. Docker
// sends SIGKILL ten seconds after SIGTERM by default; the stack sets
// stop_grace_period above everything a stop takes. It is longer than
// bot.UpdateGrace, which the commands already taken get.
const shutdownTimeout = 20 * time.Second

// queueHardStop is what the runs in progress get, after JOBS_STOP_GRACE,
// to return once they are cut short. The queue stops beside the servers,
// so a stop takes JOBS_STOP_GRACE and this at most: 3m40s by default,
// under the stacks' stop_grace_period of 4m.
const queueHardStop = 10 * time.Second

func main() {
	// Info until the configuration names the level, so a configuration that
	// cannot be read is still logged.
	level := new(slog.LevelVar)
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if err := run(log, level); err != nil {
		log.Error("exit", "error", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

func run(log *slog.Logger, level *slog.LevelVar) error {
	if limit := limitMemoryHere(); limit > 0 {
		log.Info("memory limit", "go_mib", limit>>20)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	level.Set(cfg.LogLevel)
	// A broken catalog file stops the start, not a sync an hour later.
	catalogFiles, err := catalog.LoadFiles(catalogfiles.FS)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !cfg.Primary() {
		log.Info("stack", "name", cfg.Stack, "upkeep", "production's")
	}
	store, err := connect(ctx, cfg.DatabaseURL, cfg.Stack, log)
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.AutoMigrate {
		if err := store.Migrate(ctx, migrations.FS, jobs.MigrateRiver); err != nil {
			return err
		}
	}

	// What the calls to Telegram show, for /healthz: the local Bot API keeps
	// answering getUpdates when it has lost Telegram.
	tgCalls := new(health.Calls)
	tgOpts := []tg.Option{
		tg.WithAPIBase(cfg.TelegramAPIBase),
		// my_chat_member tells core which groups framy is in; Telegram leaves
		// it out of the default set, so it has to be asked for. So does
		// edited_message, which can swap the file of an album's item.
		tg.WithAllowedUpdates("message", "edited_message", "callback_query", "my_chat_member", "inline_query", "guest_message"),
		tg.WithLogger(log),
		tg.WithObserver(observeTelegram(metrics.TelegramErrors, metrics.TelegramLimited, tgCalls)),
	}
	// A --local server hands files over by path on its own disk.
	if cfg.BotAPIFilesDir != "" {
		tgOpts = append(tgOpts, tg.WithLocalFiles(cfg.BotAPIFilesDir))
	}
	client := tg.New(cfg.TelegramBotToken, tgOpts...)
	if cfg.BotAPIBotDir != "" {
		if err := linkBotDir(cfg.BotAPIFilesDir, cfg.BotAPIBotDir, cfg.TelegramBotToken); err != nil {
			return err
		}
	}
	if cfg.BotAPIFilesDir != "" {
		// Found now rather than on the first photo: the bot's directory is
		// mounted, and writable, since a file read is removed after.
		if err := client.VerifyLocalFiles(); err != nil {
			return err
		}
	}
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
	operator := operatorNotify(cfg, func(ctx context.Context, chat int64, text string) error {
		_, err := client.SendPlainText(ctx, chat, text)
		return err
	}, log)
	files, work, err := openStorage(ctx, cfg, store, operator, log)
	if err != nil {
		return err
	}
	// People's own keys (#13). Without a KEK file there are none, and a run
	// that needs one fails as key. Track writes what each run shows about its
	// key as the connection's status.
	// What a key check shows of OpenAI's list of models goes to the same
	// operator chat as the rest, from production alone.
	models := &modelWatch{files: catalogFiles, log: log.With("component", "models"), notify: operator}
	people, err := openKeys(ctx, cfg, store, userAgent, models.listed, log)
	if err != nil {
		return err
	}
	jobsCfg := jobs.Config{
		Stack: cfg.Stack,
		Providers: map[string]jobs.Provider{
			catalog.OpenRouter: people.Track(openrouter.New(openrouter.Config{
				Base:       cfg.OpenRouterAPIBase,
				UserAgent:  userAgent,
				Referer:    openrouter.AppURL,
				Title:      openrouter.AppTitle,
				Categories: openrouter.AppCategories,
				Key:        openRouterKey(cfg.OpenRouterKey, people.JobKey),
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
				// Results taken as inputs are made upload-sized when a
				// run's files would not fit inline.
				Shrink: shrinker(files),
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
	allowance, keyWatch, err := openAllowance(cfg, store, operator, userAgent, log)
	if err != nil {
		return err
	}
	defer allowance.Wait()
	jobsCfg.Admit = allowance
	queue, err := jobs.NewQueue(store.Pool(), jobsCfg)
	if err != nil {
		return err
	}
	// /img and /edit in chats (#24) make jobs on the queue and keep their
	// photos in the bucket.
	outbox := outboxOf(cfg, client)
	flow, err := openFlow(ctx, cfg, client, store, queue, files, work, outbox, allowance, people, me.Username, log)
	if err != nil {
		return err
	}
	// The bot comes after the allowance, the keys and the flow: the home
	// panel of a direct chat shows what is left of the one and what is
	// connected of the other, and the flow answers /img.
	opts := []bot.Option{bot.WithAllowance(allowance), bot.WithConnections(people), bot.WithSiteURL(cfg.SiteURL)}
	if cfg.Primary() {
		opts = append(opts, bot.WithAlbumSweep())
	}
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
		Sessions:     auth.NewPGSessions(store.Pool(), cfg.Stack),
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
	// The site and the Mini App: the page and its assets, built by go generate.
	app, err := web.NewApp()
	if err != nil {
		return err
	}
	// Every route of the public listener is registered on site: it adds the
	// security headers, the CSRF check, the cache rules and the rate limits.
	site, err := web.New(web.Config{
		Log:            log.With("component", "web"),
		R2Endpoint:     cfg.R2.Endpoint,
		CSPExtra:       app.CSP(),
		TrustedProxies: cfg.Web.TrustedProxies,
		Person:         signIn.PersonKey,
	})
	if err != nil {
		return err
	}
	app.Mount(site)
	signIn.Mount(site)
	// Whether framy works: the public /healthz says only that, and the
	// metrics listener gives each part with the version and the commit.
	checks := health.New(health.Config{
		Store: store, LastPoll: b.LastPoll, LastUpdate: b.LastUpdate, Initialized: b.Initialized,
		Telegram: tgCalls, StartedAt: started,
		Build: health.Build{Version: version, Commit: commit, Date: date},
		Log:   log,
	})
	site.Handle("/healthz", http.HandlerFunc(checks.Public))
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
		// Both are navigations: over the rate limit the browser goes back
		// to the site, which says to try again in a few minutes.
		busy := web.OnLimit(connect.ResultURL(oauth.Unavailable))
		site.HandleFunc("GET "+oauth.StartPath, connect.Start, busy)
		// The callback exchanges the code and checks the key, two calls to
		// OpenRouter: more than the server's ten seconds to answer.
		site.HandleFunc("GET "+oauth.CallbackPath, connect.Callback, busy, web.Deadlines(10*time.Second, 45*time.Second))
		// The Mini App's ticket for the start in the browser (#489): a fetch
		// of its own script, so over the limit it gets a plain 429.
		site.HandleFunc("POST "+oauth.TicketPath, connect.Ticket)
	} else {
		log.Warn("connecting OpenRouter is off: it needs SITE_URL and FRAMY_KEK_FILE")
	}

	// The job API: the catalog and quotes for anyone; uploads, starting a
	// job, reading it, and following it over SSE (#16), for whoever sign-in
	// names.
	var uploads api.Uploads
	var links api.Links
	var kept api.Files
	var clips api.Videos
	if files != nil {
		uploads, links, kept, clips = files, files, files, files
	}
	// Send to Telegram (#37): the flow prepares a result for shareMessage and
	// serves Telegram the files of the ones it never sent. The wait for a
	// slot and a picture made a JPEG take more than ten seconds to answer.
	var sharing api.Sharing
	if flow != nil {
		sharing = flow
		site.HandleFunc("GET "+chat.SharePath+"{token}", flow.ServeShare, web.Deadlines(10*time.Second, time.Minute))
	}
	jobAPI, err := api.New(api.Config{
		Pool: store.Pool(), Queue: queue, Store: store, Uploads: uploads, Links: links, Files: kept, Videos: clips, Keys: people,
		Allowance: allowance, Person: signIn.Identify, Sharing: sharing, Log: log.With("component", "api"),
		Revoke: func(ctx context.Context, userID int64) error {
			_, err := signIn.RevokeAll(ctx, userID)
			return err
		},
		SessionsEnded: signIn.ForgetAll,
		Renew:         signIn.Renew,
		// The operator chat hears once a day when photos or clips had to
		// wait for room everyone shares.
		Notify: operator,
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

	// The queue starts once nothing of the start can fail. A run it takes is
	// paid, and River's listener holds a connection of the pool from its
	// start: a start that failed after it would wait in store.Close for
	// ever, with nothing in the log and SIGTERM still caught. From here the
	// queue stops the moment the process does, whichever way run ends, and
	// beside the servers rather than after them, so a slow request does not
	// eat into the time runs in progress have before Docker's SIGKILL.
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start the job queue: %w", err)
	}
	stopQueue := stopWhenDone(ctx, queue.Stop, cfg.JobsStopGrace+queueHardStop)
	defer func() {
		stop()
		stopQueue()
	}()

	servers := []*http.Server{site.HTTPServer(cfg.HTTPAddr)}
	if cfg.MetricsAddr != "" {
		metricsMux := http.NewServeMux()
		metrics.JobsWaiting.Set(queue.Waiting)
		metricsMux.Handle("GET /metrics", metrics.Handler())
		metricsMux.HandleFunc("GET /healthz", checks.Detail)
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
	// The tables both stacks share have one keeper, production.
	keep := upkeepOf(cfg, files != nil)
	if keep.catalog {
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
	if keep.rates {
		rates := &fx.Syncer{
			Source: &fx.Client{HTTP: &http.Client{Timeout: 20 * time.Second}, UserAgent: userAgent},
			Store:  store,
			Log:    log.With("component", "fx"),
		}
		workers.Go(func() { rates.Run(ctx) })
	}
	workers.Go(func() { sweepWork(ctx, work, "", log) })
	if outbox != nil {
		workers.Go(func() { sweepWork(ctx, *outbox, cfg.TelegramBotToken, log) })
	}
	if keep.unused {
		workers.Go(func() { sweepUnused(ctx, queue, files, log) })
	}
	if keep.sessions {
		workers.Go(func() { sweepSessions(ctx, signIn, log) })
	}
	if keep.forget {
		workers.Go(func() { forgetJobs(ctx, store, log) })
	}
	// What /healthz does not show: the disk filling up, and jobs that could
	// start and nothing takes. Each stack watches its own queue; only
	// production has an operator chat to tell.
	watcher := watch.New(watch.Config{Queue: queue, Dir: cfg.WorkDir, Notify: operator, Log: log.With("component", "watch")})
	workers.Go(func() { watcher.Run(ctx) })
	if keep.thumbs {
		workers.Go(func() { fillThumbs(ctx, files, log) })
		workers.Go(func() { catchUpBucket(ctx, files) })
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
	// lets requests in flight finish. Both get the same deadline. The queue
	// began its stop when the context ended: it takes no more jobs, and the
	// runs in progress have JOBS_STOP_GRACE.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = err
		}
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		if runErr == nil {
			runErr = errors.New("shutdown timed out")
		}
	}
	if err := stopQueue(); err != nil && runErr == nil {
		runErr = fmt.Errorf("stop the job queue: %w", err)
	}
	return runErr
}

// stopWhenDone calls stop as soon as ctx ends, with limit to finish, and
// returns the wait for it. stop runs once: the wait starts it when ctx has
// not ended yet, and every call of the wait waits for it and returns its
// error.
func stopWhenDone(ctx context.Context, stop func(context.Context) error, limit time.Duration) (wait func() error) {
	wait = sync.OnceValue(func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		return stop(stopCtx)
	})
	go func() {
		<-ctx.Done()
		wait()
	}()
	return wait
}

// observeTelegram counts what went wrong with Telegram, by method: the 429s
// apart from the rest. It also tells calls which calls Telegram answered and
// which got no answer.
func observeTelegram(failed, limited interface{ Inc(string) }, calls telegramCalls) tg.Observer {
	return func(e tg.Event) {
		// A capability probe is answered with a parameter error on purpose;
		// counting it would add a phantom incident per probed method to
		// every start.
		if e.Probe {
			return
		}
		// "Message is not modified" answers a press that draws the panel it
		// already shows, and tg and framy take it for success.
		answered := e.Err == nil || tg.IsMessageNotModified(e.Err)
		switch {
		case answered:
		case e.Status == http.StatusTooManyRequests:
			limited.Inc(e.Method)
		default:
			failed.Inc(e.Method)
		}
		// getUpdates has its own check, and a file read from the local
		// server's disk is no call. A 4xx or a 429 neither clears a run of
		// failures nor adds to it: the local server can give some of those
		// from what it knows, without Telegram.
		switch {
		case e.Method == "getUpdates" || e.Method == "readLocalFile":
		case answered:
			calls.Answered(time.Now())
		case e.Retryable && e.Status != http.StatusTooManyRequests:
			calls.Failed(time.Now())
		}
	}
}

// telegramCalls is what /healthz learns of the calls (health.Calls).
type telegramCalls interface {
	Answered(time.Time)
	Failed(time.Time)
}

// upkeep is what this process keeps of the tables every stack on the schema
// shares: the catalog, the currency rates, the drafts that never ran and the
// uploads no job took, the sessions and tokens that expired, whose a deleted
// account's jobs were, and the thumbnails results lack.
type upkeep struct{ catalog, rates, unused, sessions, forget, thumbs bool }

// upkeepOf: production keeps them, so two releases never write their own
// versions over each other (a catalog of dev's overlay over production's
// every hour); the test bot keeps none. bucket says the process has one.
func upkeepOf(cfg config.Config, bucket bool) upkeep {
	if !cfg.Primary() {
		return upkeep{}
	}
	return upkeep{catalog: cfg.CatalogEvery > 0, rates: cfg.FXSync, unused: true, sessions: true, forget: true, thumbs: bucket}
}

// connect retries the first PostgreSQL connection. Container DNS inside the
// shared core_net is not always resolvable the instant the process starts, and
// exiting there turns a two-second blip into a restart loop.
func connect(ctx context.Context, url, stack string, log *slog.Logger) (*db.Store, error) {
	const maxAttempts = 8
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		store, err := db.Connect(ctx, url, db.WithStack(stack))
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

// shrinker is the bucket's way of making a picture upload-sized, for an
// adapter that carries files inline; none without a bucket, when every job
// ends as storage before it is sent anyway.
func shrinker(files *storage.Store) func(context.Context, []byte) ([]byte, string, error) {
	if files == nil {
		return nil
	}
	return files.Shrink
}

// openStorage connects the bucket and checks ffmpeg. Without an R2 token it
// returns no store, and the service runs without one.
func openStorage(ctx context.Context, cfg config.Config, store *db.Store, notify func(context.Context, string) error, log *slog.Logger) (*storage.Store, media.Work, error) {
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
	// The site's clips go into a bucket of their own when one is set, with
	// a token that reaches nothing else (docs/design.md, Storage).
	var incoming storage.Bucket
	clips := cfg.R2.Bucket
	if in := cfg.R2.Incoming; in.Bucket != "" {
		if incoming, err = r2.New(r2.Config{
			Endpoint: cfg.R2.Endpoint, Bucket: in.Bucket,
			AccessKeyID: in.AccessKeyID, SecretAccessKey: in.SecretAccessKey,
		}); err != nil {
			return nil, work, err
		}
		clips = in.Bucket
	}
	conv := &media.Converter{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe}
	if err := conv.Check(ctx); err != nil {
		return nil, work, fmt.Errorf("ffmpeg cannot normalize photos: %w", err)
	}
	if err := conv.CheckVideo(ctx); err != nil {
		return nil, work, fmt.Errorf("ffmpeg cannot encode clips: %w", err)
	}
	files, err := storage.New(storage.Config{
		Pool:     store.Pool(),
		Bucket:   bucket,
		Incoming: incoming,
		Prefix:   cfg.R2.Prefix,
		Media:    conv,
		Work:     work,
		Fetch:    safehttp.Client(storage.DefaultFetchTimeout),
		Notify:   notify,
		Log:      log.With("component", "storage"),
	})
	if err != nil {
		return nil, work, err
	}
	log.Info("storage ready", "bucket", cfg.R2.Bucket, "clips", clips, "prefix", cfg.R2.Prefix, "work_dir", cfg.WorkDir)
	return files, work, nil
}

// openFlow sets up /img and /edit. Without the bucket, or with an ffmpeg that
// cannot draw the loops, they are off and the rest runs.
func openFlow(ctx context.Context, cfg config.Config, client *tg.Client, store *db.Store, queue *jobs.Queue,
	files *storage.Store, work media.Work, outbox *media.Work, allowance *free.Allowance, people *keys.Service, username string, log *slog.Logger) (*chat.Flow, error) {
	if files == nil {
		log.Warn("no bucket, so /img is off: the photos a run takes and its results are kept there")
		return nil, nil
	}
	renderer := &anim.Renderer{FFmpeg: cfg.FFmpeg, Cache: cfg.AnimCache}
	if err := renderer.Check(ctx); err != nil {
		log.Error("the loops cannot be drawn, so /img is off", "error", err)
		return nil, nil
	}
	// The same ffmpeg storage checked for clips: /edit reads the clip it
	// takes and readies the result for Telegram.
	clips := &media.Converter{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe}
	chatCfg := chat.Config{
		Telegram: client, Store: store, Queue: queue, Files: files, Allowance: allowance, Keys: people,
		Renderer: renderer, Clips: clips, Work: work, Log: log.With("component", "chat"), AppURL: cfg.SiteURL,
		Stack: cfg.Stack, Username: username,
	}
	if outbox != nil {
		chatCfg.Outbox = *outbox
	}
	return chat.New(chatCfg)
}

// outboxOf is where results go to a local Bot API server by their path: a
// directory of the bot's own media directory, which framy and the server
// see at the same path, so tg does not hold a video in memory to upload it.
// Nil with Telegram's own server, which reads no disk.
func outboxOf(cfg config.Config, client *tg.Client) *media.Work {
	if cfg.BotAPIFilesDir == "" || !client.IsLocalServer() {
		return nil
	}
	return &media.Work{Root: filepath.Join(cfg.BotAPIFilesDir, cfg.TelegramBotToken, "framy-send")}
}

// sweepUnused clears the drafts that ended without running and then
// deletes the uploads no job takes within storage.UploadTTL, theirs among
// them: at start and every hour. files is nil without R2.
func sweepUnused(ctx context.Context, queue *jobs.Queue, files *storage.Store, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := queue.ClearDrafts(ctx); err != nil {
			log.Error("clear drafts that never ran", "error", err)
		} else if n > 0 {
			log.Info("cleared drafts that never ran", "count", n)
		}
		if files != nil {
			if n, err := files.SweepUploads(ctx, storage.UploadTTL); err != nil {
				log.Error("sweep uploads no job took", "swept", n, "error", err)
			} else if n > 0 {
				log.Info("swept uploads no job took", "count", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// sweepSessions removes the sessions and Mini App tokens that expired a day
// ago: at start and every hour. An expired one is never accepted; this keeps
// the table, which every stack on the schema shares, from growing.
func sweepSessions(ctx context.Context, signIn *auth.Auth, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := signIn.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.Warn("sweep expired sessions", "error", err)
		} else if n > 0 {
			log.Info("swept expired sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// forgetJobs takes the person's id off the jobs of deleted accounts once the
// free allowance no longer counts them: at start and every hour.
func forgetJobs(ctx context.Context, store *db.Store, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := store.ForgetJobs(ctx); err != nil && ctx.Err() == nil {
			log.Error("forget the jobs of deleted accounts", "error", err)
		} else if n > 0 {
			log.Info("forgot whose jobs of deleted accounts were", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// fillThumbs makes the thumbnails results lack, kept before framy made them
// or whose thumbnail failed: at start and every minute, a few at a time.
func fillThumbs(ctx context.Context, files *storage.Store, log *slog.Logger) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if n, err := files.FillThumbs(ctx, 20); err != nil && ctx.Err() == nil {
			log.Error("fill thumbnails", "made", n, "error", err)
		} else if n > 0 {
			log.Info("made thumbnails results lacked", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// catchUpBucket sums the bucket every minute when a write came too soon
// after the last sum to be summed, so the operator hears of a step the last
// write of a busy minute crossed without waiting for the next write.
func catchUpBucket(ctx context.Context, files *storage.Store) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			files.CatchUpBucket(ctx)
		}
	}
}

// sweepWork clears working files a crash left behind: at start and every
// hour, whatever is older than an hour. Nothing framy does takes that long.
// An error says secret, a part of the path that must not reach a log, as
// ***: a local Bot API server names the bot's directory after its token.
func sweepWork(ctx context.Context, work media.Work, secret string, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := work.Sweep(time.Hour); err != nil {
			msg := err.Error()
			if secret != "" {
				msg = strings.ReplaceAll(msg, secret, "***")
			}
			log.Error("sweep working files", "error", msg)
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
func openAllowance(cfg config.Config, store *db.Store, notify func(context.Context, string) error, userAgent string, log *slog.Logger) (*free.Allowance, *free.KeyWatch, error) {
	log = log.With("component", "free")
	fc := free.Config{Pool: store.Pool(), DailyBudgetUSD: cfg.FreeDailyBudgetUSD, Notify: notify, Log: log}
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
	allowance, err := free.New(fc)
	if err != nil {
		return nil, nil, err
	}
	return allowance, watch, nil
}

// operatorNotify is how framy tells its operator what needs a person: the
// free tier closed by a fuse, the bucket past another 10 GB, room everyone
// shares full for the day, the watch's findings (internal/watch). Only
// production tells, since those steps are shared and kept in the database
// (docs/design.md, Stacks); another stack gets nil and leaves them alone.
// Production without OPERATOR_CHAT_ID still takes each step, so it is
// logged once, at Warn, by the part that took it, and the start says where
// the notices went.
func operatorNotify(cfg config.Config, send func(ctx context.Context, chat int64, text string) error, log *slog.Logger) func(context.Context, string) error {
	if !cfg.Primary() {
		return nil
	}
	if cfg.OperatorChatID == 0 {
		log.Warn("no OPERATOR_CHAT_ID: the notices for the operator (the free tier closing, the bucket's size, shared room full, the disk, jobs nothing takes) are only in this log")
		return func(context.Context, string) error { return nil }
	}
	return func(ctx context.Context, text string) error {
		return send(ctx, cfg.OperatorChatID, text)
	}
}

// openKeys loads the KEK file and, on production, rotates what is wrapped
// under an older version of it. Without a file it returns no service, which
// is a framy that keeps no keys. A KEK file that cannot be read stops the
// start: it is the operator's mistake, and running without it would look
// like every stored key being gone.
func openKeys(ctx context.Context, cfg config.Config, store *db.Store, userAgent string, listed func(string, []catalog.Listed), log *slog.Logger) (*keys.Service, error) {
	if cfg.KEKFile == "" {
		log.Warn("no FRAMY_KEK_FILE: people cannot connect keys of their own, and a run on one fails as key")
		return nil, nil
	}
	kek, err := keys.LoadKEKFile(cfg.KEKFile)
	if err != nil {
		return nil, err
	}
	if err := checkKEKMode(kek, cfg, log); err != nil {
		return nil, err
	}
	log = log.With("component", "keys")
	svc, err := keys.New(keys.Config{
		Pool:           store.Pool(),
		KEK:            kek,
		UserAgent:      userAgent,
		OpenRouterBase: cfg.OpenRouterAPIBase,
		Listed:         listed,
		Log:            log,
	})
	if err != nil {
		return nil, err
	}
	checkKEK(ctx, svc, cfg.Primary(), kek.Versions(), log)
	return svc, nil
}

// checkKEKMode stops the start when group or others can read the KEK file.
// A wrong mode is a deploy mistake (a volume made 0644, a file an editor
// recreated), and with the file readable the dump of the database and its
// KEK are one theft apart. It holds for the test bot too, whose file is a
// copy of production's. FRAMY_KEK_ALLOW_LOOSE is the way out on a laptop and
// is logged when used.
func checkKEKMode(kek *keys.KEK, cfg config.Config, log *slog.Logger) error {
	if !kek.Loose() {
		return nil
	}
	if cfg.KEKAllowLoose {
		log.Warn("the KEK file can be read by others and FRAMY_KEK_ALLOW_LOOSE lets it start; never set that on a server", "path", cfg.KEKFile)
		return nil
	}
	return fmt.Errorf("the KEK file %s can be read by others: chmod 0400 it (FRAMY_KEK_ALLOW_LOOSE=true starts anyway, for a laptop only)", cfg.KEKFile)
}

// kekChecker is what checkKEK asks of keys.Service.
type kekChecker interface {
	Rotate(context.Context) (keys.RotateResult, error)
	Inspect(context.Context) (keys.KEKState, error)
}

// checkKEK rewraps the data keys still under an older KEK version, on
// production only: the connection table is one for both stacks, and the
// test bot's copy of the KEK file rotating it would move every key to a
// version production may not hold, so that no run on a person's key could
// open it. Another stack only counts and says what is wrong. Either way the
// service starts: a failed rotation leaves every row readable under the
// version it had.
func checkKEK(ctx context.Context, svc kekChecker, primary bool, versions []int, log *slog.Logger) {
	if !primary {
		st, err := svc.Inspect(ctx)
		switch {
		case err != nil:
			log.Error("read the KEK versions of the stored keys", "error", err, "kek_versions", versions)
		case st.Unreadable > 0:
			log.Error("stored keys are wrapped under a KEK version this file lacks: this stack's KEK file has to be production's",
				"unreadable", st.Unreadable, "kek_versions", versions)
		case st.Older > 0:
			log.Warn("stored keys are on an older KEK version than this file's newest line: keys saved here are wrapped under that line, which production cannot open until it restarts with it, so a new line goes into production's file first",
				"older", st.Older, "kek_version", st.Current)
		default:
			log.Info("keys ready", "kek_versions", versions, "kek_version", st.Current, "rotation", "production's")
		}
		return
	}
	res, err := svc.Rotate(ctx)
	switch {
	case err != nil:
		log.Error("KEK rotation failed", "error", err, "kek_versions", versions)
	case res.Failed > 0 || res.Unreadable > 0:
		log.Error("stored keys cannot be read with this KEK file: people have to enter them again, or the lines for their versions have to come back",
			"unreadable", res.Unreadable, "rewrap_failed", res.Failed, "rewrapped", res.Rewrapped, "kek_versions", versions)
	case res.Rewrapped > 0:
		log.Info("data keys rewrapped under the current KEK; copy this KEK file to the other stacks and restart them, and the older lines can go from every file once no start finds keys on them", "rewrapped", res.Rewrapped, "kek_version", res.Current)
	default:
		log.Info("keys ready", "kek_versions", versions, "kek_version", res.Current)
	}
}
