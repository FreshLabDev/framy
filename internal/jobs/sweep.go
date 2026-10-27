// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type sweepArgs struct{}

func (sweepArgs) Kind() string { return "framy_sweep" }

// sweepWorker runs every minute on River's leader.
type sweepWorker struct {
	river.WorkerDefaults[sweepArgs]
	q *Queue
}

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[sweepArgs]) error {
	_, err := w.q.sweep(ctx)
	return err
}

// sweep ends what nobody else will:
//   - drafts not confirmed within 10 minutes;
//   - jobs River gave up on without working them, as it does with a job
//     whose worker died on its last attempt.
func (q *Queue) sweep(ctx context.Context) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE job SET status='cancelled', error_class='draft_expired', finished_at=now(), updated_at=now()
			WHERE status='draft' AND created_at <= now() - make_interval(secs => $1)`, draftTTL.Seconds())
		if err != nil {
			return err
		}
		n += tag.RowsAffected()
		rows, err := tx.Query(ctx, `
			UPDATE job j SET status='failed', finished_at=now(), updated_at=now(),
			       error_class=CASE WHEN j.submitted_at IS NULL THEN 'internal' ELSE 'interrupted' END,
			       error='River gave up on the job'
			FROM river_job r
			WHERE r.id = j.river_job_id AND j.status IN ('queued', 'running')
			  AND r.state IN ('completed', 'cancelled', 'discarded')
			RETURNING j.id, j.user_id`)
		if err != nil {
			return err
		}
		people := map[int64]bool{}
		for rows.Next() {
			var id, user int64
			if err := rows.Scan(&id, &user); err != nil {
				rows.Close()
				return err
			}
			q.log.Warn("job ended by the sweep; River gave up on it", "job", id)
			people[user] = true
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for user := range people {
			if err := q.wake(ctx, tx, user); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}
