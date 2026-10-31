// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNoResult: no result of the person's by that id, or its job was deleted.
var ErrNoResult = errors.New("no such result")

// ShareSource is a result as Send to Telegram needs it: the file, what was
// asked for it, and the file Telegram already has of it.
type ShareSource struct {
	AssetID     int64
	JobID       int64
	Key         string
	ContentType string
	Bytes       int64
	Width       int
	Height      int
	Seconds     float64
	Prompt      string
	// Model is the model's name, as ChatRun has it.
	Model string
	// FileID and FileType are the file the result went out as in a chat:
	// only for the asset that message carries, the job's first result, since
	// a chat run keeps the first message of an album, or the joined clip a
	// site result went to the DM as.
	FileID   string
	FileType string
}

// ShareSource is result assetID of userID's, from a job that is done and not
// deleted; ErrNoResult for any other.
func (s *Store) ShareSource(ctx context.Context, userID, assetID int64) (ShareSource, error) {
	var src ShareSource
	var first bool
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, j.id, a.object_key, a.content_type, a.bytes, COALESCE(a.width, 0), COALESCE(a.height, 0),
		       COALESCE(a.seconds, 0), j.prompt, COALESCE(m.name, o.name, j.model_id),
		       COALESCE(a.id = COALESCE(r.result_asset_id, (SELECT min(f.id) FROM asset f WHERE f.job_id = j.id AND f.kind = 'result')), false),
		       COALESCE(r.result_file_id, ''), COALESCE(r.result_file_type, '')
		FROM asset a
		JOIN job j ON j.id = a.job_id
		LEFT JOIN chat_run r ON r.job_id = j.id
		LEFT JOIN catalog_offer o ON o.id = j.offer_id
		LEFT JOIN catalog_model m ON m.id = j.model_id
		WHERE a.id = $2 AND a.user_id = $1 AND a.kind IN ('result', 'joined') AND j.user_id = $1
		  AND j.status = 'done' AND j.deleted_at IS NULL`, userID, assetID).Scan(
		&src.AssetID, &src.JobID, &src.Key, &src.ContentType, &src.Bytes, &src.Width, &src.Height,
		&src.Seconds, &src.Prompt, &src.Model, &first, &src.FileID, &src.FileType)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareSource{}, ErrNoResult
	}
	if err != nil {
		return ShareSource{}, fmt.Errorf("share source %d: %w", assetID, err)
	}
	if !first {
		src.FileID, src.FileType = "", ""
	}
	return src, nil
}
