// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// PollNowTx makes the next poll of a run in the background happen now instead
// of at its next turn, 30 s away. A provider's webhook calls it: the run
// changed, so ask. The poll itself is the queue's usual one, with the same
// key, so nothing the webhook carried is believed.
//
// It reports whether a poll was brought forward. It is not when the job has
// ended, has no provider job id yet, or is being worked at this moment (River
// does not touch a running job); the last asks again by itself, at most one
// turn later. The wake is part of tx, so a webhook that is recorded and a
// poll that is brought forward are one step.
func (q *Queue) PollNowTx(ctx context.Context, tx pgx.Tx, id int64) (bool, error) {
	var riverID *int64
	err := tx.QueryRow(ctx, `
		SELECT river_job_id FROM job
		WHERE id=$1 AND status='running' AND provider_job_id IS NOT NULL`, id).Scan(&riverID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && riverID == nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	row, err := q.river.JobRetryTx(ctx, tx, *riverID)
	if errors.Is(err, river.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.State == rivertype.JobStateAvailable, nil
}
