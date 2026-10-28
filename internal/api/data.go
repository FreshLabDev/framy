// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Error      string
	RetryAt    *time.Time

	EstimateUSD *float64
	// Estimate is the estimate with its lines, as saved with the job.
	Estimate  *price.Estimate
	ActualUSD *float64
	// Currency, PerUSD and FXDate are the currency the estimate was shown in
	// and its rate; Currency is empty for USD alone.
	Currency string
	PerUSD   float64
	FXDate   *time.Time

	CreatedAt  time.Time
	FinishedAt *time.Time
	// Results are the files of a finished job, in order; empty before.
	Results []Result
	// Grounding is what a search-grounded run brought with its results.
	Grounding *jobs.Grounding
}

// Result is a file a run made, without its link.
type Result struct {
	ID          int64
	ContentType string
	Bytes       int64
	Width       int
	Height      int
}

// InputAsset is a file of the person's that a new job would take.
type InputAsset struct {
	ID          int64
	ContentType string
	Width       int
	Height      int
}

// data is what the API reads from the job and asset tables. Reads only: a
// job is made by the queue.
type data interface {
	// State is the job, for its owner. ErrNotFound for anyone else.
	State(ctx context.Context, userID, id int64) (State, error)
	// ByKey is the job a dedupe key started, if any.
	ByKey(ctx context.Context, key string) (id int64, ok bool, err error)
	// Inputs are the person's files among ids, in the order of ids; fewer
	// than asked when some are not theirs or do not exist.
	Inputs(ctx context.Context, userID int64, ids []int64) ([]InputAsset, error)
}

type pgData struct{ pool *pgxpool.Pool }

func (d pgData) State(ctx context.Context, userID, id int64) (State, error) {
	var (
		s       State
		estim   []byte
		errCls  *string
		errText *string
		cur     *string
		rate    *float64
		ground  []byte
	)
	err := d.pool.QueryRow(ctx, `
		SELECT id, event_seq, status, kind, offer_id, free, error_class, error, retry_at,
		       estimate_usd::float8, estimate, actual_usd::float8,
		       currency, fx_rate::float8, fx_date, created_at, finished_at, grounding
		FROM job WHERE id = $1 AND user_id = $2`, id, userID).Scan(
		&s.ID, &s.Seq, &s.Status, &s.Kind, &s.Offer, &s.Free, &errCls, &errText, &s.RetryAt,
		&s.EstimateUSD, &estim, &s.ActualUSD,
		&cur, &rate, &s.FXDate, &s.CreatedAt, &s.FinishedAt, &ground)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("read job %d: %w", id, err)
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
	if s.Status != jobs.Done {
		return s, nil
	}
	// The results are written before the job is marked done, so a done job
	// shows them all.
	rows, err := d.pool.Query(ctx, `
		SELECT id, content_type, bytes, COALESCE(width, 0), COALESCE(height, 0)
		FROM asset WHERE job_id = $1 AND user_id = $2 AND kind = 'result' ORDER BY id`, id, userID)
	if err != nil {
		return State{}, fmt.Errorf("read results of job %d: %w", id, err)
	}
	s.Results, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Result, error) {
		var x Result
		return x, r.Scan(&x.ID, &x.ContentType, &x.Bytes, &x.Width, &x.Height)
	})
	if err != nil {
		return State{}, fmt.Errorf("read results of job %d: %w", id, err)
	}
	return s, nil
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
		SELECT u.id, a.content_type, COALESCE(a.width, 0), COALESCE(a.height, 0)
		FROM unnest($2::bigint[]) WITH ORDINALITY AS u(id, ord)
		JOIN asset a ON a.id = u.id AND a.user_id = $1
		ORDER BY u.ord`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("read inputs: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (InputAsset, error) {
		var x InputAsset
		return x, r.Scan(&x.ID, &x.ContentType, &x.Width, &x.Height)
	})
}
