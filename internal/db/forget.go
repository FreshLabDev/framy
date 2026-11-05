// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"
)

// ForgetJobs takes the person's id off the jobs of deleted accounts whose
// forget_at has come (migrations/035_job_forget.sql) and says how many. The
// account's deletion sets forget_at: when a free run leaves the allowance's
// window, and at once for the rest. A job whose chat run is still there
// keeps the id until the bot has taken its message down and dropped the
// run, since an ephemeral message is deleted by its person's id. What stays
// is the kind, the cost and the times, which the day's budget counts.
//
// It is upkeep of a shared table: production runs it at start and every
// hour.
func (s *Store) ForgetJobs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job SET user_id = NULL, forget_at = NULL
		WHERE forget_at <= now() AND deleted_at IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM chat_run r WHERE r.job_id = job.id)`)
	if err != nil {
		return 0, fmt.Errorf("forget the jobs of deleted accounts: %w", err)
	}
	return tag.RowsAffected(), nil
}
