// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/FreshLabDev/framy/internal/catalog"
)

type generateArgs struct {
	JobID int64 `json:"job_id"`
}

func (generateArgs) Kind() string { return "framy_generate" }

type generateWorker struct {
	river.WorkerDefaults[generateArgs]
	q *Queue
}

func (w *generateWorker) Work(ctx context.Context, rj *river.Job[generateArgs]) error {
	err := w.q.work(ctx, rj.Args.JobID)
	var snooze *rivertype.JobSnoozeError
	var cancel *rivertype.JobCancelError
	if err == nil || errors.As(err, &snooze) || errors.As(err, &cancel) {
		return err
	}
	if rj.Attempt >= rj.MaxAttempts {
		// River gives up after this attempt, so the job ends here rather than
		// stay queued or running for nobody.
		ctx, done := detach(ctx)
		defer done()
		if endErr := w.q.end(ctx, rj.Args.JobID, ending{status: Failed, class: Internal, msg: err.Error()}); endErr != nil {
			return endErr
		}
		return river.JobCancel(err)
	}
	return err
}

// work moves job id as far as it can go in one attempt.
func (q *Queue) work(ctx context.Context, id int64) error {
	j, err := load(ctx, q.pool, id)
	if err != nil {
		return err
	}
	switch j.Status {
	case Draft:
		return river.JobCancel(fmt.Errorf("job %d is a draft", id))
	case Queued:
		claimed, err := q.claim(ctx, j)
		if err != nil {
			return err
		}
		if !claimed {
			// Over the person's limit, behind an older job of theirs, or
			// waiting out a refusal.
			return river.JobSnooze(q.waitFor(j))
		}
		j.Status = Running
	case Running:
	default:
		// Ended: cancelled while it waited, or worked already.
		return nil
	}
	p := q.cfg.Providers[j.Provider]
	switch {
	case p == nil:
		return q.end(ctx, j.ID, ending{status: Failed, class: NoAdapter, msg: "framy has no adapter for " + j.Provider})
	case q.cfg.Sink == nil:
		return q.end(ctx, j.ID, ending{status: Failed, class: Storage, msg: "no storage for results"})
	case j.ProviderJobID != "":
		return q.poll(ctx, p, j)
	case j.SubmittedAt != nil:
		// The paid request went out and its answer never came back: the
		// worker stopped mid-run. Sending it again could pay twice.
		return q.end(ctx, j.ID, ending{status: Failed, class: Interrupted, msg: "the worker stopped while the paid request was out; it is not sent again"})
	}
	if q.cfg.Inputs != nil {
		inputs, err := q.cfg.Inputs.Inputs(ctx, j)
		var pe *Error
		switch {
		case errors.As(err, &pe):
			return q.end(ctx, j.ID, ending{status: Failed, class: pe.Class, msg: pe.Error()})
		case err != nil:
			return fmt.Errorf("inputs of job %d: %w", j.ID, err)
		}
		j.Inputs = inputs
	}
	return q.submit(ctx, p, j)
}

// waitFor is how long a job that could not start sleeps: until its retry
// time, or WaitSlot. A job that ends wakes the next one sooner.
func (q *Queue) waitFor(j Job) time.Duration {
	if j.RetryAt != nil {
		if d := j.RetryAt.Sub(q.now()); d > 0 {
			return d
		}
	}
	return q.cfg.WaitSlot
}

// claim starts a queued job if the person has a free slot and no older job
// waiting for one. The person's claims take turns under a lock, so two
// workers cannot both take the last slot.
func (q *Queue) claim(ctx context.Context, j Job) (bool, error) {
	var claimed bool
	err := pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('framy.job.person:' || $1::bigint::text, 0))`, j.UserID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE job SET status='running', retry_at=NULL, started_at=COALESCE(started_at, now()), updated_at=now()
			WHERE id=$1 AND status='queued' AND (retry_at IS NULL OR retry_at <= now())
			  AND (SELECT count(*) FROM job WHERE user_id=$2 AND status='running') < $3
			  AND NOT EXISTS (
			    SELECT 1 FROM job o WHERE o.user_id=$2 AND o.status='queued' AND o.id < $1
			      AND (o.retry_at IS NULL OR o.retry_at <= now()))`,
			j.ID, j.UserID, q.cfg.MaxRunning)
		claimed = tag.RowsAffected() == 1
		return err
	})
	return claimed, err
}

// submit sends the paid request.
func (q *Queue) submit(ctx context.Context, p Provider, j Job) error {
	// The mark goes in before the request does.
	tag, err := q.pool.Exec(ctx, `
		UPDATE job SET submitted_at=now(), submits=submits+1, error_class=NULL, error=NULL, updated_at=now()
		WHERE id=$1 AND status='running' AND submitted_at IS NULL`, j.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("job %d changed under its worker", j.ID)
	}
	j.Submits++
	now := q.now()
	j.SubmittedAt = &now

	limit := imageCall
	if j.Kind == catalog.Video {
		limit = videoCall
	}
	call, cancel := context.WithTimeout(ctx, limit)
	sub, err := p.Submit(call, j)
	var pe *Error
	if err != nil {
		pe = classify(call, err)
	}
	cancel()

	// What came back is written down even when a stop has begun.
	ctx, done := detach(ctx)
	defer done()
	if pe != nil {
		if pe.retryable() && j.Submits < maxSubmits {
			return q.requeue(ctx, j, pe)
		}
		return q.end(ctx, j.ID, ending{status: Failed, class: pe.Class, msg: pe.Error()})
	}
	if sub.ProviderJobID != "" {
		if _, err := q.pool.Exec(ctx, `UPDATE job SET provider_job_id=$2, updated_at=now() WHERE id=$1`, j.ID, sub.ProviderJobID); err != nil {
			// Without the id a retry cannot ask about the run; the log keeps
			// it for a person to look up.
			q.log.Error("job provider id not saved", "job", j.ID, "provider_job_id", sub.ProviderJobID, "error", err)
			return err
		}
		j.ProviderJobID = sub.ProviderJobID
	}
	switch {
	case sub.Outcome != nil && !sub.Outcome.Running:
		return q.settle(ctx, j, *sub.Outcome)
	case j.ProviderJobID == "":
		return q.end(ctx, j.ID, ending{status: Failed, class: ProviderFailed, msg: "the provider returned neither a result nor a job id"})
	}
	return river.JobSnooze(q.cfg.PollEvery)
}

// requeue puts back in line a job the provider refused before the run
// started, after the provider's wait or a growing one. The person's other
// jobs may go ahead of it meanwhile.
func (q *Queue) requeue(ctx context.Context, j Job, pe *Error) error {
	wait := max(pe.RetryAfter, time.Duration(j.Submits*j.Submits)*5*time.Second)
	err := pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE job SET status='queued', submitted_at=NULL, retry_at=now() + make_interval(secs => $2),
			       error_class=$3, error=$4, updated_at=now()
			WHERE id=$1 AND status='running'`, j.ID, wait.Seconds(), pe.Class, clip(pe.Error()))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return q.wake(ctx, tx, j.UserID)
	})
	if err != nil {
		return err
	}
	q.log.Info("job refused before the run; retrying", "job", j.ID, "class", pe.Class, "submits", j.Submits, "retry_in", wait.String())
	return river.JobSnooze(wait)
}

// poll asks about a run in the background. Asking costs nothing, so any
// trouble short of a definite answer means asking again later.
func (q *Queue) poll(ctx context.Context, p Provider, j Job) error {
	call, cancel := context.WithTimeout(ctx, videoCall)
	out, err := p.Poll(call, j)
	var pe *Error
	if err != nil {
		pe = classify(call, err)
	}
	cancel()

	ctx, done := detach(ctx)
	defer done()
	late := j.SubmittedAt != nil && q.now().Sub(*j.SubmittedAt) > runDeadline
	switch {
	case pe != nil && (pe.retryable() || pe.Class == ProviderFailed || pe.Class == Timeout || pe.Class == Interrupted):
		if late {
			return q.end(ctx, j.ID, ending{status: Expired, class: pe.Class, msg: "the provider did not finish within an hour: " + pe.Error()})
		}
		return river.JobSnooze(max(q.cfg.PollEvery, pe.RetryAfter))
	case pe != nil:
		return q.end(ctx, j.ID, ending{status: Failed, class: pe.Class, msg: pe.Error()})
	case out.Running:
		if late {
			return q.end(ctx, j.ID, ending{status: Expired, class: Timeout, msg: "the provider did not finish within an hour"})
		}
		return river.JobSnooze(q.cfg.PollEvery)
	}
	return q.settle(ctx, j, out)
}

// settle ends a run the provider finished.
func (q *Queue) settle(ctx context.Context, j Job, out Outcome) error {
	e := ending{cost: out.CostUSD}
	switch {
	case out.Err != nil:
		e.status, e.class, e.msg = Failed, out.Err.Class, out.Err.Error()
	case len(out.Results) == 0:
		e.status, e.class, e.msg = Failed, ProviderFailed, "the run ended with no result"
	default:
		if err := q.cfg.Sink.Keep(ctx, j, out.Results); err != nil {
			if j.ProviderJobID != "" {
				// The provider still holds the result: ask for it again.
				return fmt.Errorf("keep the results of job %d: %w", j.ID, err)
			}
			e.status, e.class, e.msg = Failed, Storage, err.Error()
			break
		}
		e.status, e.results = Done, len(out.Results)
	}
	return q.end(ctx, j.ID, e)
}

// ending is how a job ends.
type ending struct {
	status  Status
	class   Class
	msg     string
	cost    *float64
	results int
}

// end finishes a job that is still queued or running and wakes the person's
// next job. A job that ended already keeps its ending: the first one wins.
func (q *Queue) end(ctx context.Context, id int64, e ending) error {
	var userID int64
	var estimate, actual *float64
	ended := false
	err := pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE job SET status=$2, error_class=NULLIF($3, ''), error=NULLIF($4, ''),
			       actual_usd=COALESCE($5, actual_usd), results=$6, retry_at=NULL, finished_at=now(), updated_at=now()
			WHERE id=$1 AND status IN ('queued', 'running')
			RETURNING user_id, estimate_usd::float8, actual_usd::float8`,
			id, e.status, e.class, clip(e.msg), e.cost, e.results).Scan(&userID, &estimate, &actual)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ended = true
		return q.wake(ctx, tx, userID)
	})
	if err != nil || !ended {
		return err
	}
	attrs := []any{"job", id, "status", e.status}
	if e.class != "" {
		attrs = append(attrs, "class", e.class)
	}
	if estimate != nil {
		attrs = append(attrs, "estimate_usd", *estimate)
	}
	if actual != nil {
		attrs = append(attrs, "actual_usd", *actual)
	}
	if e.status == Done {
		q.log.Info("job done", attrs...)
	} else {
		q.log.Warn("job ended without a result", append(attrs, "error", e.msg)...)
	}
	// A price that changed shows up as a run that cost more, or less, than
	// framy said it would.
	if estimate != nil && actual != nil && *estimate > 0 && math.Abs(*actual-*estimate) > 0.2**estimate {
		q.log.Warn("job cost differs from its estimate by more than 20%", attrs...)
	}
	return nil
}

// wake makes the person's next waiting job look for a slot now rather than
// at its next WaitSlot. A job waiting out a refusal is left to sleep.
func (q *Queue) wake(ctx context.Context, tx pgx.Tx, userID int64) error {
	var riverID int64
	err := tx.QueryRow(ctx, `
		SELECT river_job_id FROM job
		WHERE user_id=$1 AND status='queued' AND river_job_id IS NOT NULL AND (retry_at IS NULL OR retry_at <= now())
		ORDER BY id LIMIT 1`, userID).Scan(&riverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = q.river.JobRetryTx(ctx, tx, riverID)
	return err
}

// detach keeps ctx's values without its cancellation, with a short limit of
// its own, for the writes that must follow a paid call.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

// clip keeps an error message to a size a row can hold.
func clip(s string) string {
	const limit = 2000
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
