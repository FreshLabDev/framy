// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
)

// ErrNotFound: no such job, or not the person's. The two are the same
// answer, so nobody can learn which job ids exist.
var ErrNotFound = errors.New("no such job")

// State is a job as one read of its row shows it. Seq is the row's
// event_seq: it grows with every change a person can see, so it is the id of
// the event that reports this state.
type State struct {
	ID     int64
	Seq    int64
	Status jobs.Status
	Kind   catalog.Kind
	Offer  string
	Free   bool

	ErrorClass jobs.Class
	// Error is what the provider said, in its own words, or framy's line
	// when it said nothing of its own.
	Error   string
	RetryAt *time.Time

	EstimateUSD *float64
	// Estimate is the estimate with its lines, as saved with the job.
	Estimate  *price.Estimate
	ActualUSD *float64
	// Currency, PerUSD and FXDate are the currency the estimate was shown in
	// and its rate; Currency is empty for USD alone.
	Currency string
	PerUSD   float64
	FXDate   *time.Time

	// Model, Prompt and Settings are what was asked for: the model's id, the
	// person's words and the settings the run went with, defaults included.
	// Surface is where it was asked: site, mini_app, group or dm.
	Model    string
	Prompt   string
	Settings map[string]any
	Surface  string

	CreatedAt  time.Time
	FinishedAt *time.Time
	// Inputs are the files the job took that are still kept, in order.
	Inputs []Result
	// Results are the files of a finished job, in order; empty before.
	Results []Result
	// Grounding is what a search-grounded run brought with its results.
	Grounding *jobs.Grounding
}

// Ended reports whether the job is over and nothing will change it again.
func (s State) Ended() bool {
	switch s.Status {
	case jobs.Done, jobs.Failed, jobs.Cancelled, jobs.Expired:
		return true
	}
	return false
}

// Filter is a page of the history.
type Filter struct {
	// Kind is image or video, empty for both.
	Kind catalog.Kind
	// Query is a part of the prompt, any case; empty for every job.
	Query string
	// Before is the id of the last job of the previous page, 0 for the
	// first.
	Before int64
	Limit  int
}

// Result is a file a run made, without its link.
type Result struct {
	ID          int64
	ContentType string
	Bytes       int64
	Width       int
	Height      int
	// Seconds is a clip's length, 0 for a picture and where it is not known.
	Seconds float64
}

// InputAsset is a file of the person's that a new job would take.
type InputAsset struct {
	ID          int64
	ContentType string
	Width       int
	Height      int
	Bytes       int64
	// Seconds is a clip's length as ffprobe read it, 0 for a picture and
	// where it is not known.
	Seconds float64
}

// ErrActive: the job is queued or running, or, for the account, some job is.
// It has to end, or be stopped, first.
var ErrActive = errors.New("a job is still queued or running")

// data is what the API reads from the job and asset tables, and the person's
// own deletions. A job is made by the queue.
type data interface {
	// State is the job, for its owner. ErrNotFound for anyone else, and for
	// a job the person deleted.
	State(ctx context.Context, userID, id int64) (State, error)
	// List is a page of the person's history: the jobs that were queued and
	// not deleted, newest first.
	List(ctx context.Context, userID int64, f Filter) ([]State, error)
	// ByKey is the job a dedupe key started, if any.
	ByKey(ctx context.Context, key string) (id int64, ok bool, err error)
	// Inputs are the person's files among ids, in the order of ids; fewer
	// than asked when some are not theirs or do not exist.
	Inputs(ctx context.Context, userID int64, ids []int64) ([]InputAsset, error)
	// Continuation is a job of the person's that a run would continue:
	// ErrNotFound for anyone else's, and for one the person deleted.
	Continuation(ctx context.Context, userID, id int64) (Continuation, error)
	// Remove clears a job of the person's that ended, once its files are
	// gone: ErrNotFound, or ErrActive for one that has not ended.
	Remove(ctx context.Context, userID, id int64) error
	// Active reports whether the person has a job queued or running.
	Active(ctx context.Context, userID int64) (bool, error)
	// Forget removes what framy keeps of the person, once their files are
	// gone: ErrActive while a job is queued or running.
	Forget(ctx context.Context, userID int64) error
}

type pgData struct{ pool *pgxpool.Pool }

// stateColumns are what State and List read of a job row, in the order of
// scanState.
const stateColumns = `id, event_seq, status, kind, offer_id, free, error_class, COALESCE(provider_message, error), retry_at,
	estimate_usd::float8, estimate, actual_usd::float8,
	currency, fx_rate::float8, fx_date, created_at, finished_at, grounding,
	model_id, prompt, settings, surface`

func scanState(row pgx.Row) (State, error) {
	var (
		s        State
		estim    []byte
		errCls   *string
		errText  *string
		cur      *string
		rate     *float64
		ground   []byte
		settings []byte
	)
	err := row.Scan(
		&s.ID, &s.Seq, &s.Status, &s.Kind, &s.Offer, &s.Free, &errCls, &errText, &s.RetryAt,
		&s.EstimateUSD, &estim, &s.ActualUSD,
		&cur, &rate, &s.FXDate, &s.CreatedAt, &s.FinishedAt, &ground,
		&s.Model, &s.Prompt, &settings, &s.Surface)
	if err != nil {
		return State{}, err
	}
	if errCls != nil {
		s.ErrorClass = jobs.Class(*errCls)
	}
	if errText != nil {
		s.Error = *errText
	}
	if cur != nil && rate != nil {
		s.Currency, s.PerUSD = *cur, *rate
	}
	if len(estim) > 0 {
		var e price.Estimate
		if err := json.Unmarshal(estim, &e); err == nil {
			s.Estimate = &e
		}
	}
	if len(ground) > 0 {
		var g jobs.Grounding
		if err := json.Unmarshal(ground, &g); err == nil && !g.Empty() {
			s.Grounding = &g
		}
	}
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &s.Settings)
	}
	return s, nil
}

func (d pgData) State(ctx context.Context, userID, id int64) (State, error) {
	s, err := scanState(d.pool.QueryRow(ctx, `
		SELECT `+stateColumns+` FROM job WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("read job %d: %w", id, err)
	}
	states := []State{s}
	if err := d.files(ctx, userID, states); err != nil {
		return State{}, err
	}
	return states[0], nil
}

func (d pgData) List(ctx context.Context, userID int64, f Filter) ([]State, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT `+stateColumns+` FROM job
		WHERE user_id = $1 AND deleted_at IS NULL AND queued_at IS NOT NULL
		  AND ($2 = '' OR kind = $2)
		  AND ($3 = '' OR prompt ILIKE '%' || $3 || '%' ESCAPE '\')
		  AND ($4 = 0 OR id < $4)
		ORDER BY id DESC
		LIMIT $5`, userID, string(f.Kind), likeEscape(f.Query), f.Before, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("read the history: %w", err)
	}
	states, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (State, error) { return scanState(r) })
	if err != nil {
		return nil, fmt.Errorf("read the history: %w", err)
	}
	if err := d.files(ctx, userID, states); err != nil {
		return nil, err
	}
	return states, nil
}

// likeEscape makes text match itself in a LIKE pattern with ESCAPE '\'.
func likeEscape(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}

// files fills the inputs of every state and the results of the done ones,
// in two reads. The results are written before a job is marked done, so a
// done job shows them all.
func (d pgData) files(ctx context.Context, userID int64, states []State) error {
	if len(states) == 0 {
		return nil
	}
	ids := make([]int64, len(states))
	at := make(map[int64]int, len(states))
	for i, s := range states {
		ids[i], at[s.ID] = s.ID, i
	}
	scan := func(r pgx.CollectableRow) (struct {
		job int64
		Result
	}, error) {
		var x struct {
			job int64
			Result
		}
		return x, r.Scan(&x.job, &x.ID, &x.ContentType, &x.Bytes, &x.Width, &x.Height, &x.Seconds)
	}
	rows, err := d.pool.Query(ctx, `
		SELECT i.job_id, a.id, a.content_type, a.bytes, COALESCE(a.width, 0), COALESCE(a.height, 0), COALESCE(a.seconds, 0)::float8
		FROM job_input i JOIN asset a ON a.id = i.asset_id AND a.user_id = $2
		WHERE i.job_id = ANY($1)
		ORDER BY i.job_id, i.position`, ids, userID)
	if err != nil {
		return fmt.Errorf("read the inputs of jobs: %w", err)
	}
	inputs, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return fmt.Errorf("read the inputs of jobs: %w", err)
	}
	for _, x := range inputs {
		s := &states[at[x.job]]
		s.Inputs = append(s.Inputs, x.Result)
	}
	rows, err = d.pool.Query(ctx, `
		SELECT job_id, id, content_type, bytes, COALESCE(width, 0), COALESCE(height, 0), COALESCE(seconds, 0)::float8
		FROM asset WHERE job_id = ANY($1) AND user_id = $2 AND kind = 'result'
		ORDER BY job_id, id`, ids, userID)
	if err != nil {
		return fmt.Errorf("read the results of jobs: %w", err)
	}
	results, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return fmt.Errorf("read the results of jobs: %w", err)
	}
	for _, x := range results {
		if s := &states[at[x.job]]; s.Status == jobs.Done {
			s.Results = append(s.Results, x.Result)
		}
	}
	return nil
}

func (d pgData) ByKey(ctx context.Context, key string) (int64, bool, error) {
	var id int64
	err := d.pool.QueryRow(ctx, `SELECT id FROM job WHERE dedupe_key = $1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the job of a dedupe key: %w", err)
	}
	return id, true, nil
}

func (d pgData) Inputs(ctx context.Context, userID int64, ids []int64) ([]InputAsset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `
		SELECT u.id, a.content_type, COALESCE(a.width, 0), COALESCE(a.height, 0), a.bytes, COALESCE(a.seconds, 0)::float8
		FROM unnest($2::bigint[]) WITH ORDINALITY AS u(id, ord)
		JOIN asset a ON a.id = u.id AND a.user_id = $1
		ORDER BY u.ord`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("read inputs: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (InputAsset, error) {
		var x InputAsset
		return x, r.Scan(&x.ID, &x.ContentType, &x.Width, &x.Height, &x.Bytes, &x.Seconds)
	})
}

func (d pgData) Continuation(ctx context.Context, userID, id int64) (Continuation, error) {
	var c Continuation
	var videoID *int64
	var v InputAsset
	err := d.pool.QueryRow(ctx, `
		SELECT j.model_id, j.provider, COALESCE(j.provider_job_id, ''), j.status, j.free,
		       r.id, COALESCE(r.content_type, ''), COALESCE(r.width, 0), COALESCE(r.height, 0), COALESCE(r.bytes, 0),
		       COALESCE(r.seconds, 0)::float8
		FROM job j
		LEFT JOIN LATERAL (
		  SELECT a.id, a.content_type, a.width, a.height, a.bytes, a.seconds FROM asset a
		  WHERE a.job_id = j.id AND a.kind = 'result' AND a.content_type LIKE 'video/%'
		  ORDER BY a.id LIMIT 1) r ON true
		WHERE j.id = $1 AND j.user_id = $2 AND j.deleted_at IS NULL`, id, userID).Scan(
		&c.ModelID, &c.Provider, &c.ProviderJobID, &c.Status, &c.Free,
		&videoID, &v.ContentType, &v.Width, &v.Height, &v.Bytes, &v.Seconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return Continuation{}, ErrNotFound
	}
	if err != nil {
		return Continuation{}, fmt.Errorf("read the job to continue: %w", err)
	}
	if videoID != nil {
		v.ID = *videoID
		c.Video = &v
	}
	return c, nil
}

// cleared is what a deleted job keeps: its kind, offer, payer, status,
// costs and times, which the free allowance, its budget and the per-person
// limits count. Everything the person wrote or got goes.
const cleared = `deleted_at = now(), prompt = '', settings = '{}', error = NULL, grounding = NULL,
	chat_id = NULL, message_id = NULL, updated_at = now()`

func (d pgData) Remove(ctx context.Context, userID, id int64) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE job SET `+cleared+`
			WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL AND finished_at IS NOT NULL`, id, userID)
		if err != nil {
			return fmt.Errorf("delete job %d: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			var ended bool
			err := tx.QueryRow(ctx, `SELECT finished_at IS NOT NULL FROM job WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID).Scan(&ended)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return ErrNotFound
			case err != nil:
				return fmt.Errorf("delete job %d: %w", id, err)
			case !ended:
				return ErrActive
			}
			return ErrNotFound
		}
		for _, stmt := range []string{`DELETE FROM job_input WHERE job_id = $1`, `DELETE FROM chat_run WHERE job_id = $1`} {
			if _, err := tx.Exec(ctx, stmt, id); err != nil {
				return fmt.Errorf("delete job %d: %w", id, err)
			}
		}
		return nil
	})
}

func (d pgData) Active(ctx context.Context, userID int64) (bool, error) {
	var active bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job WHERE user_id = $1 AND status IN ('queued', 'running'))`, userID).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("active jobs of %d: %w", userID, err)
	}
	return active, nil
}

// Forget is the account's rows, in one transaction under the lock a job
// takes to start, so a run the queue is about to claim is either seen here
// and refused or starts after: drafts are cancelled, every job that ended is
// cleared as Remove clears one, and the keys, the pending connections to
// OpenRouter, the profile (currency, name and photo) and the photos of
// albums framy saw the person send in a chat are deleted. The sessions go
// through Config.Revoke, which also drops what internal/auth remembers of
// them. The person stays in core: that row is the family's, not framy's.
func (d pgData) Forget(ctx context.Context, userID int64) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('framy.job.person:' || $1::bigint::text, 0))`, userID); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job WHERE user_id = $1 AND status IN ('queued', 'running'))`, userID).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrActive
		}
		for _, stmt := range []string{
			`UPDATE job SET status = 'cancelled', finished_at = now(), updated_at = now() WHERE user_id = $1 AND status = 'draft'`,
			`DELETE FROM chat_run WHERE job_id IN (SELECT id FROM job WHERE user_id = $1)`,
			`DELETE FROM job_input WHERE job_id IN (SELECT id FROM job WHERE user_id = $1)`,
			`UPDATE job SET ` + cleared + ` WHERE user_id = $1 AND deleted_at IS NULL AND finished_at IS NOT NULL`,
			`DELETE FROM connection WHERE user_id = $1`,
			`DELETE FROM oauth_state WHERE user_id = $1`,
			`DELETE FROM profile WHERE user_id = $1`,
			`DELETE FROM album_item WHERE user_id = $1`,
		} {
			if _, err := tx.Exec(ctx, stmt, userID); err != nil {
				return fmt.Errorf("forget %d: %w", userID, err)
			}
		}
		return nil
	})
}
