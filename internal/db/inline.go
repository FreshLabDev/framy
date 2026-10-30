// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"
)

// InlineResult is a result a person can send again from inline mode: a
// photo or a video Telegram keeps, by its file_id. Model is the model's
// name, as ChatRun has it.
type InlineResult struct {
	JobID    int64
	Prompt   string
	Model    string
	FileID   string
	FileType string
}

// InlineResults are person user's results that went out as a photo or a
// video, newest first: at most limit of them, older than job before when it
// is not zero, and with search in the prompt, case aside, when it is not
// empty. A deleted job is left out.
func (s *Store) InlineResults(ctx context.Context, user int64, search string, before int64, limit int) ([]InlineResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.prompt, COALESCE(m.name, o.name, j.model_id), r.result_file_id, r.result_file_type
		FROM job j
		JOIN chat_run r ON r.job_id = j.id
		LEFT JOIN catalog_offer o ON o.id = j.offer_id
		LEFT JOIN catalog_model m ON m.id = j.model_id
		WHERE j.user_id = $1 AND j.deleted_at IS NULL AND j.queued_at IS NOT NULL
		  AND ($3::bigint = 0 OR j.id < $3)
		  AND ($2::text = '' OR strpos(lower(j.prompt), lower($2)) > 0)
		  AND r.stage = 'done' AND r.result_file_id IS NOT NULL AND r.result_file_type IN ('photo', 'video')
		ORDER BY j.id DESC
		LIMIT $4`, user, search, before, limit)
	if err != nil {
		return nil, fmt.Errorf("inline results: %w", err)
	}
	defer rows.Close()
	var out []InlineResult
	for rows.Next() {
		var r InlineResult
		if err := rows.Scan(&r.JobID, &r.Prompt, &r.Model, &r.FileID, &r.FileType); err != nil {
			return nil, fmt.Errorf("inline results: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inline results: %w", err)
	}
	return out, nil
}
