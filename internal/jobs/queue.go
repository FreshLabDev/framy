// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// Schema is where framy's and River's tables live.
const Schema = "framy"

// Queue names: one per provider and kind, each with its own workers.
const (
	QueueOpenAI          = "openai"
	QueueGemini          = "gemini"
	QueueOpenRouterImage = "openrouter_image"
	QueueOpenRouterVideo = "openrouter_video"
)

// QueueFor is the queue of a provider's runs of a kind.
func QueueFor(provider string, kind catalog.Kind) string {
	switch {
	case provider == "openrouter" && kind == catalog.Video:
		return QueueOpenRouterVideo
	case provider == "openrouter":
		return QueueOpenRouterImage
	case provider == "gemini":
		return QueueGemini
	}
	return QueueOpenAI
}

// DefaultWorkers is how many jobs of each queue one process works at once. A
// video job waits for the provider snoozed, not holding a worker.
var DefaultWorkers = map[string]int{
	QueueOpenAI:          4,
	QueueGemini:          4,
	QueueOpenRouterImage: 8,
	QueueOpenRouterVideo: 4,
}

// Timings. The image call's limit is the design's; River rescues a job whose
// worker died after RescueAfter, which has to outlast the longest attempt.
const (
	imageCall    = 180 * time.Second
	videoCall    = 60 * time.Second
	attemptLimit = 4 * time.Minute
	rescueAfter  = 5 * time.Minute
	// maxSubmits caps the paid requests refused before the run started.
	maxSubmits = 5
	// maxAttempts caps River's own retries, for errors that are framy's.
	maxAttempts = 5
	draftTTL    = 10 * time.Minute
	// runDeadline: a run the provider has not finished by then expires.
	runDeadline = time.Hour
)

// Config is the queue's setup. Providers are keyed by the catalog's provider
// name: openai, gemini, openrouter.
type Config struct {
	Providers map[string]Provider
	Sink      Sink
	Log       *slog.Logger
	// MaxRunning is how many jobs one person runs at once; 2 by default.
	MaxRunning int
	// Workers per queue; DefaultWorkers by default.
	Workers map[string]int
	// StopGrace is how long a stop waits for runs in progress before it
	// cuts them short, which costs the person a run: longer than an image
	// call by default.
	StopGrace time.Duration
	// WaitSlot is how often a job over the person's limit looks again, in
	// case the wake-up from the job before it was missed. PollEvery is how
	// often a background run is asked about.
	WaitSlot  time.Duration
	PollEvery time.Duration
	// FetchPoll is how often River looks for jobs without a notification;
	// River's default when zero. Tests make it short.
	FetchPoll time.Duration
}

// Queue creates jobs and works them.
type Queue struct {
	pool   *pgxpool.Pool
	river  *river.Client[pgx.Tx]
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	cancel context.CancelFunc
}

// NewQueue builds the queue on pool, whose search_path is framy.
func NewQueue(pool *pgxpool.Pool, cfg Config) (*Queue, error) {
	cfg.MaxRunning = cmp.Or(cfg.MaxRunning, 2)
	cfg.StopGrace = cmp.Or(cfg.StopGrace, imageCall+30*time.Second)
	cfg.WaitSlot = cmp.Or(cfg.WaitSlot, 5*time.Second)
	cfg.PollEvery = cmp.Or(cfg.PollEvery, 30*time.Second)
	if cfg.Workers == nil {
		cfg.Workers = DefaultWorkers
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	q := &Queue{pool: pool, cfg: cfg, log: cfg.Log, now: time.Now}

	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &generateWorker{q: q}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(workers, &sweepWorker{q: q}); err != nil {
		return nil, err
	}
	queues := map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}}
	for name, n := range cfg.Workers {
		queues[name] = river.QueueConfig{MaxWorkers: max(n, 1)}
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema:               Schema,
		Queues:               queues,
		Workers:              workers,
		JobTimeout:           attemptLimit,
		RescueStuckJobsAfter: rescueAfter,
		MaxAttempts:          maxAttempts,
		SoftStopTimeout:      cfg.StopGrace,
		FetchPollInterval:    cfg.FetchPoll,
		FetchCooldown:        min(cmp.Or(cfg.FetchPoll, 100*time.Millisecond), 100*time.Millisecond),
		Logger:               cfg.Log,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return sweepArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("river client: %w", err)
	}
	q.river = client
	return q, nil
}

// MigrateRiver brings River's tables in the framy schema up to date, in tx.
func MigrateRiver(ctx context.Context, tx pgx.Tx) error {
	m, err := rivermigrate.New(riverpgxv5.New(nil), &rivermigrate.Config{Schema: Schema})
	if err != nil {
		return err
	}
	if _, err := m.MigrateTx(ctx, tx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	return nil
}

// Start starts working jobs. It returns at once; Stop ends the work.
func (q *Queue) Start(ctx context.Context) error {
	// The work outlives the signal that starts a stop: Stop decides how long
	// runs in progress get.
	ctx, q.cancel = context.WithCancel(context.WithoutCancel(ctx))
	return q.river.Start(ctx)
}

// Stop stops fetching jobs and waits for the ones in progress, up to
// StopGrace, then cuts them short.
func (q *Queue) Stop(ctx context.Context) error {
	err := q.river.Stop(ctx)
	if q.cancel != nil {
		q.cancel()
	}
	return err
}

// Enqueue creates a queued job and its River job in one transaction. A job
// with the same dedupe key is returned instead, and nothing starts.
func (q *Queue) Enqueue(ctx context.Context, n New) (id int64, created bool, err error) {
	err = pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		id, created, err = q.insert(ctx, tx, n, Queued)
		if err != nil || !created {
			return err
		}
		return q.schedule(ctx, tx, id, n.Offer.Provider, n.Offer.Kind)
	})
	return id, created, err
}

// Draft creates a job that waits for "Generate", as a chat preview does.
func (q *Queue) Draft(ctx context.Context, n New) (id int64, created bool, err error) {
	err = pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		id, created, err = q.insert(ctx, tx, n, Draft)
		return err
	})
	return id, created, err
}

// Generate queues a draft. A draft older than 10 minutes, already started or
// cancelled gives ErrNotDraft, so a repeated press starts nothing.
func (q *Queue) Generate(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		var provider, kind string
		err := tx.QueryRow(ctx, `
			UPDATE job SET status='queued', queued_at=now(), updated_at=now()
			WHERE id=$1 AND status='draft' AND created_at > now() - make_interval(secs => $2)
			RETURNING provider, kind`, id, draftTTL.Seconds()).Scan(&provider, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotDraft
		}
		if err != nil {
			return err
		}
		return q.schedule(ctx, tx, id, provider, catalog.Kind(kind))
	})
}

// Cancel ends a draft or a queued job. A running one gives ErrRunning; one
// already ended is left as it is.
func (q *Queue) Cancel(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		var riverID *int64
		err := tx.QueryRow(ctx, `
			UPDATE job SET status='cancelled', finished_at=now(), updated_at=now()
			WHERE id=$1 AND status IN ('draft','queued')
			RETURNING river_job_id`, id).Scan(&riverID)
		if errors.Is(err, pgx.ErrNoRows) {
			var status Status
			if err := tx.QueryRow(ctx, `SELECT status FROM job WHERE id=$1`, id).Scan(&status); err != nil {
				return err
			}
			if status == Running {
				return ErrRunning
			}
			return nil
		}
		if err != nil || riverID == nil {
			return err
		}
		_, err = q.river.JobCancelTx(ctx, tx, *riverID)
		return err
	})
}

// Get reads a job.
func (q *Queue) Get(ctx context.Context, id int64) (Job, error) {
	return load(ctx, q.pool, id)
}

func (q *Queue) insert(ctx context.Context, tx pgx.Tx, n New, status Status) (int64, bool, error) {
	var estimateUSD *float64
	if n.Estimate != nil {
		estimateUSD = &n.Estimate.USD
	}
	settings := n.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO job (user_id, surface, chat_id, message_id, dedupe_key, offer_id, model_id, provider, kind,
		                 prompt, settings, free, status, estimate_usd, estimate, queued_at)
		VALUES ($1, $2, NULLIF($3, 0), NULLIF($4, 0), NULLIF($5, ''), $6, $7, $8, $9,
		        $10, $11, $12, $13, $14, $15, CASE WHEN $13 = 'queued' THEN now() END)
		ON CONFLICT (dedupe_key) DO NOTHING
		RETURNING id`,
		n.UserID, n.Surface, n.ChatID, n.MessageID, n.DedupeKey, n.Offer.ID, n.Offer.ModelID, n.Offer.Provider, n.Offer.Kind,
		n.Prompt, settings, n.Free, status, estimateUSD, n.Estimate).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM job WHERE dedupe_key=$1`, n.DedupeKey).Scan(&id)
		return id, false, err
	}
	if err != nil {
		return 0, false, fmt.Errorf("insert job: %w", err)
	}
	return id, true, nil
}

// schedule inserts the River job that works job id, in the same
// transaction.
func (q *Queue) schedule(ctx context.Context, tx pgx.Tx, id int64, provider string, kind catalog.Kind) error {
	res, err := q.river.InsertTx(ctx, tx, generateArgs{JobID: id}, &river.InsertOpts{
		Queue:      QueueFor(provider, kind),
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	})
	if err != nil {
		return fmt.Errorf("insert river job: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE job SET river_job_id=$2 WHERE id=$1`, id, res.Job.ID)
	return err
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func load(ctx context.Context, db querier, id int64) (Job, error) {
	var j Job
	var chatID, messageID *int64
	var dedupe, providerJobID, errClass, errText *string
	err := db.QueryRow(ctx, `
		SELECT id, user_id, surface, chat_id, message_id, dedupe_key, offer_id, model_id, provider, kind,
		       prompt, settings, free, status, idempotency_key::text, submits, submitted_at, retry_at, provider_job_id,
		       estimate_usd::float8, actual_usd::float8, error_class, error, created_at
		FROM job WHERE id=$1`, id).Scan(
		&j.ID, &j.UserID, &j.Surface, &chatID, &messageID, &dedupe, &j.OfferID, &j.ModelID, &j.Provider, &j.Kind,
		&j.Prompt, &j.Settings, &j.Free, &j.Status, &j.IdempotencyKey, &j.Submits, &j.SubmittedAt, &j.RetryAt, &providerJobID,
		&j.EstimateUSD, &j.ActualUSD, &errClass, &errText, &j.CreatedAt)
	if err != nil {
		return Job{}, fmt.Errorf("load job %d: %w", id, err)
	}
	j.ChatID, j.MessageID = deref(chatID), deref(messageID)
	j.ProviderJobID, j.Error = deref(providerJobID), deref(errText)
	j.ErrorClass = Class(deref(errClass))
	return j, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
