// SPDX-License-Identifier: Apache-2.0

// Package free is the free allowance (docs/design.md, Free tier): 5 images
// in any 24 hours and 1 video in any 7 days per Telegram user id across every
// surface, on framy's own OpenRouter key, with fixed settings. It is counted
// from the job table, not stored apart, and two fuses close it for everyone:
// a global budget for each UTC day, and the limit of framy's OpenRouter key.
//
// The jobs queue asks the allowance (Allowance.Admit) inside the transaction
// that creates or queues a free job, with the person's lock and then the
// budget's held, so the check and the insert are one step: two requests at
// once cannot both take the last slot or pass the last cents. A refusal is a
// typed error the surfaces word: *LimitError, *ClosedError or *NotFreeError.
//
// What counts, in one place:
//
//   - A slot of the person's is taken by a run that is queued, running or
//     done, and by one the provider refused for its content (the moderation
//     class), so probing costs something. A run that failed otherwise, was
//     cancelled or expired takes nothing. A draft waits for "Generate" and
//     takes nothing until then.
//   - The budget counts what a run costs framy: the estimate saved with the
//     job while it is queued or running, the actual cost once it ended, and
//     the estimate for a run that ended without a cost on the books but may
//     have been paid (done with no reported cost, expired, or failed as a
//     timeout, an interruption or an unreadable answer). A run that failed
//     before anything went out costs nothing. The day is the UTC day of the
//     moment the run was queued.
package free

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/metrics"
)

// MoneyPause is how long the free tier stays closed after a free run failed
// for lack of money: framy's key is out of credit or over its limit, and the
// next run would only fail the same way.
const MoneyPause = 15 * time.Minute

// DefaultDailyBudgetUSD is the owner's budget (27 October): about 50 free
// images a day.
const DefaultDailyBudgetUSD = 1.0

const announceTimeout = 15 * time.Second

// Gauge is the reading of framy's OpenRouter key: *KeyWatch.
type Gauge interface {
	State() (KeyState, bool)
}

// Config is the allowance's setup.
type Config struct {
	// Pool's search_path is framy.
	Pool *pgxpool.Pool
	// DailyBudgetUSD is the most free runs cost framy in a UTC day. Zero
	// closes the free tier.
	DailyBudgetUSD float64
	// Key is the reading of framy's OpenRouter key; nil goes by the budget
	// and by runs that failed for lack of money.
	Key Gauge
	// Notify tells the operator chat the first time each fuse closes the
	// tier on a day. Nil only logs it, once a day in this process, and
	// leaves the day in runtime_state to production (docs/design.md,
	// Stacks), which has someone to tell.
	Notify func(ctx context.Context, text string) error
	Log    *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Allowance enforces the free allowance. It implements jobs.Admitter.
type Allowance struct {
	pool   *pgxpool.Pool
	budget int64 // micro-dollars
	key    Gauge
	notify func(ctx context.Context, text string) error
	log    *slog.Logger
	now    func() time.Time
	wg     sync.WaitGroup
	// told is the UTC day, by fuse, this process has told the operator chat
	// about, or is telling: a flood of refusals costs one query a day, not
	// one each.
	mu   sync.Mutex
	told map[Fuse]time.Time
	// pause runs after a request has passed every check, still under the
	// locks: tests widen the window in which a missing lock would show.
	pause func()
}

// New builds the allowance.
func New(cfg Config) (*Allowance, error) {
	switch {
	case cfg.Pool == nil:
		return nil, errors.New("free: the pool is needed")
	case math.IsNaN(cfg.DailyBudgetUSD) || math.IsInf(cfg.DailyBudgetUSD, 0) || cfg.DailyBudgetUSD < 0:
		return nil, errors.New("free: the daily budget must be zero or more")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Allowance{
		pool: cfg.Pool, budget: micro(cfg.DailyBudgetUSD), key: cfg.Key,
		notify: cfg.Notify, log: cfg.Log, now: cfg.Now, told: map[Fuse]time.Time{},
	}, nil
}

// Wait waits for the notices to the operator chat in flight. Call it when
// nothing asks the allowance any more.
func (a *Allowance) Wait() { a.wg.Wait() }

// Admit decides whether a free job may be created or queued: jobs.Admitter.
// It runs in the job's own transaction and takes the person's lock, then the
// budget's, which end with the transaction. A refusal is *LimitError,
// *ClosedError or *NotFreeError.
func (a *Allowance) Admit(ctx context.Context, tx pgx.Tx, adm jobs.Admission) error {
	err := a.decide(ctx, tx, adm, true)
	a.refused(err)
	return err
}

// Check answers what Admit would answer now, without a lock and without
// taking a slot: for a surface that shows the Generate button or the price
// ("free · 4 of 5 left"). Only Admit decides.
func (a *Allowance) Check(ctx context.Context, adm jobs.Admission) error {
	return a.decide(ctx, a.pool, adm, false)
}

// conn is a transaction or the pool.
type conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (a *Allowance) decide(ctx context.Context, c conn, adm jobs.Admission, lock bool) error {
	plan, ok := planOf(adm.Offer)
	if !ok {
		return &NotFreeError{Reason: adm.Offer + " is not a model of the free allowance"}
	}
	if err := plan.check(adm); err != nil {
		return err
	}
	if lock {
		if err := advisory(ctx, c, "framy.free.person:"+strconv.FormatInt(adm.UserID, 10)); err != nil {
			return err
		}
		// A request that started a job already is the same request, not a
		// second run: it creates nothing, so it takes no slot.
		if adm.DedupeKey != "" {
			var seen bool
			if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job WHERE dedupe_key = $1)`, adm.DedupeKey).Scan(&seen); err != nil {
				return err
			}
			if seen {
				return nil
			}
		}
	}
	now := a.now()
	lim := limitOf(plan.Kind)
	taken, err := a.taken(ctx, c, adm.UserID, plan.Kind, now)
	if err != nil {
		return err
	}
	if len(taken) >= lim.runs {
		// At the limit it is the oldest run that leaves the window first;
		// over it, the one whose leaving brings the count under the limit.
		return &LimitError{Kind: plan.Kind, Limit: lim.runs, Window: lim.window, FreesAt: taken[len(taken)-lim.runs].Add(lim.window)}
	}
	if lock {
		if err := advisory(ctx, c, "framy.free.budget"); err != nil {
			return err
		}
	}
	if err := a.fuses(ctx, c, adm, now); err != nil {
		return err
	}
	if lock && a.pause != nil {
		a.pause()
	}
	return nil
}

// advisory takes a lock that ends with the transaction.
func advisory(ctx context.Context, c conn, name string) error {
	_, err := c.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name)
	return err
}

// counts are the runs that take a slot of the person's.
const counts = `(status IN ('queued', 'running', 'done') OR (status = 'failed' AND error_class = 'moderation'))`

// taken lists, oldest first, when the person's runs of a kind that count
// were queued, within the kind's window.
func (a *Allowance) taken(ctx context.Context, c conn, userID int64, kind catalog.Kind, now time.Time) ([]time.Time, error) {
	rows, err := c.Query(ctx, `
		SELECT queued_at FROM job
		WHERE free AND user_id = $1 AND kind = $2 AND queued_at > $3 AND `+counts+`
		ORDER BY queued_at`, userID, kind, now.Add(-limitOf(kind).window))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[time.Time])
}

// cost is what a free job has cost framy, or will: see the package comment.
const cost = `CASE
	WHEN status IN ('queued', 'running') THEN COALESCE(estimate_usd, 0)
	WHEN actual_usd IS NOT NULL THEN actual_usd
	WHEN status IN ('done', 'expired') OR error_class IN ('timeout', 'interrupted', 'provider') THEN COALESCE(estimate_usd, 0)
	ELSE 0
END`

// spend sums the day's free runs in micro-dollars: all of them, and the ones
// still in flight.
func (a *Allowance) spend(ctx context.Context, c conn, day time.Time) (spent, inFlight int64, err error) {
	err = c.QueryRow(ctx, `
		SELECT COALESCE(sum(`+cost+`) * 1000000, 0)::bigint,
		       COALESCE(sum(CASE WHEN status IN ('queued', 'running') THEN COALESCE(estimate_usd, 0) END) * 1000000, 0)::bigint
		FROM job
		WHERE free AND queued_at >= $1 AND queued_at < $2`, day, day.Add(24*time.Hour)).Scan(&spent, &inFlight)
	return spent, inFlight, err
}

// fuses closes the tier when the day's budget cannot pay for the run, or
// framy's key cannot.
func (a *Allowance) fuses(ctx context.Context, c conn, adm jobs.Admission, now time.Time) error {
	est := micro(adm.EstimateUSD)
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	spent, inFlight, err := a.spend(ctx, c, day)
	if err != nil {
		return err
	}
	if spent+est > a.budget {
		return &ClosedError{Fuse: FuseBudget, Until: day.Add(24 * time.Hour), Why: fmt.Sprintf(
			"the day's budget of $%.2f has $%.4f left and the run needs $%.4f", dollars(a.budget), dollars(max(a.budget-spent, 0)), dollars(est))}
	}
	if a.key != nil {
		// What OpenRouter reports leaves out the runs still on their way, and
		// each of them will take its price from the limit.
		if st, ok := a.key.State(); ok && st.Limited && micro(st.RemainingUSD)-inFlight < est {
			var until time.Time
			if st.Daily {
				until = day.Add(24 * time.Hour)
			}
			return &ClosedError{Fuse: FuseKey, Until: until, Why: fmt.Sprintf(
				"framy's OpenRouter key has $%.4f left of its limit, $%.4f of it is for runs on their way, and the run needs $%.4f",
				st.RemainingUSD, dollars(inFlight), dollars(est))}
		}
	}
	// A free run that failed for lack of money says the key cannot pay,
	// whatever the reading says: no credit left, or a limit the reading
	// has not caught up with yet.
	var failedAt *time.Time
	if err := c.QueryRow(ctx, `
		SELECT max(finished_at) FROM job
		WHERE free AND error_class = 'money' AND finished_at > $1`, now.Add(-MoneyPause)).Scan(&failedAt); err != nil {
		return err
	}
	if failedAt != nil {
		return &ClosedError{Fuse: FuseKey, Until: failedAt.Add(MoneyPause), Why: "a free run failed for lack of money, so framy's OpenRouter key cannot pay"}
	}
	return nil
}

// Meter is the person's use of the allowance of a kind of run.
type Meter struct {
	Used, Limit int
	Window      time.Duration
	// FreesAt is when one more run is allowed; zero while a slot is free.
	FreesAt time.Time
}

// Left is how many runs the person still has.
func (m Meter) Left() int { return max(m.Limit-m.Used, 0) }

// Usage is what the person has used.
type Usage struct {
	Images, Videos Meter
}

// Usage reads the person's meters, for "free · 4 of 5 left".
func (a *Allowance) Usage(ctx context.Context, userID int64) (Usage, error) {
	now := a.now()
	meter := func(kind catalog.Kind) (Meter, error) {
		taken, err := a.taken(ctx, a.pool, userID, kind, now)
		if err != nil {
			return Meter{}, err
		}
		lim := limitOf(kind)
		m := Meter{Used: len(taken), Limit: lim.runs, Window: lim.window}
		if len(taken) >= lim.runs {
			m.FreesAt = taken[len(taken)-lim.runs].Add(lim.window)
		}
		return m, nil
	}
	var u Usage
	var err error
	if u.Images, err = meter(catalog.Image); err != nil {
		return Usage{}, err
	}
	if u.Videos, err = meter(catalog.Video); err != nil {
		return Usage{}, err
	}
	return u, nil
}

// refused counts a refusal, and tells the operator chat when it is the
// first of the day to say a fuse has closed the tier.
func (a *Allowance) refused(err error) {
	var limit *LimitError
	var closed *ClosedError
	var locked *NotFreeError
	switch {
	case errors.As(err, &limit):
		metrics.FreeRefused.Inc(string(limit.Kind) + "_limit")
	case errors.As(err, &closed):
		metrics.FreeRefused.Inc(string(closed.Fuse))
		// The transaction is about to end, and it holds the locks: the notice
		// goes on its own, after.
		if a.claimNotice(closed.Fuse) {
			a.wg.Go(func() { a.announce(closed) })
		}
	case errors.As(err, &locked):
		metrics.FreeRefused.Inc("not_free")
	}
}

// alertDay is the column that remembers the last UTC day the operator chat
// was told each fuse had closed the tier.
var alertDay = map[Fuse]string{FuseBudget: "free_budget_alert_day", FuseKey: "free_key_alert_day"}

// today is the start of the UTC day the allowance's clock is in.
func (a *Allowance) today() time.Time {
	now := a.now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// claimNotice says whether this process still has to look into telling the
// chat that the fuse closed the tier today.
func (a *Allowance) claimNotice(f Fuse) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	day := a.today()
	if a.told[f].Equal(day) {
		return false
	}
	a.told[f] = day
	return true
}

// releaseNotice lets the next refusal try again.
func (a *Allowance) releaseNotice(f Fuse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.told, f)
}

// announce tells the operator chat once a day per fuse, across restarts and
// across processes: the day is swapped in runtime_state, not set, so two
// refusals at once tell it once. A notice that failed is tried again by the
// next refusal. A process with nobody to tell only logs.
func (a *Allowance) announce(c *ClosedError) {
	if a.notify == nil {
		metrics.FreeClosed.Inc(string(c.Fuse))
		a.log.Warn("the free tier is closed", "fuse", c.Fuse, "until", c.Until, "why", c.Why)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), announceTimeout)
	defer cancel()
	day := a.today()
	col := alertDay[c.Fuse]
	tag, err := a.pool.Exec(ctx, `UPDATE runtime_state SET `+col+` = $1::date, updated_at = now() WHERE `+col+` IS DISTINCT FROM $1::date`, day)
	if err != nil {
		a.log.Error("note the closing of the free tier", "fuse", c.Fuse, "error", err)
		a.releaseNotice(c.Fuse)
		return
	}
	if tag.RowsAffected() != 1 {
		return
	}
	metrics.FreeClosed.Inc(string(c.Fuse))
	a.log.Warn("the free tier is closed", "fuse", c.Fuse, "until", c.Until, "why", c.Why)
	if err := a.notify(ctx, "framy: "+c.Error()); err != nil {
		a.log.Error("tell the operator chat", "error", err)
		_, _ = a.pool.Exec(ctx, `UPDATE runtime_state SET `+col+` = NULL WHERE `+col+` = $1::date`, day)
		a.releaseNotice(c.Fuse)
	}
}

// micro writes dollars in millionths, the precision of the job table, so
// sums are exact and "exactly the budget" is a number.
func micro(usd float64) int64 { return int64(math.Round(usd * 1e6)) }

func dollars(m int64) float64 { return float64(m) / 1e6 }
