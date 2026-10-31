// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// States of a result sent from the site (migrations/020_site_delivery.sql).
const (
	DeliverySending = "sending"
	DeliverySent    = "sent"
	DeliveryClosed  = "closed"
	DeliveryFailed  = "failed"
	// DeliveryOff: the person turned the DM off in the settings
	// (migrations/025_settings.sql).
	DeliveryOff = "off"
)

// SiteResult is a job made on the site whose result the person's DM has not
// been sent. Model is the model's name, as ChatRun has it. Deliver is the
// person's setting: false when they turned the DM off.
type SiteResult struct {
	JobID   int64
	UserID  int64
	Prompt  string
	Kind    string
	Model   string
	Deliver bool
}

// SiteResults are the jobs this stack's site made that ended done after
// since and that no sender has taken, oldest first, at most limit of them. A
// deleted job is left out.
func (s *Store) SiteResults(ctx context.Context, since time.Time, limit int) ([]SiteResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.user_id, j.prompt, j.kind, COALESCE(m.name, o.name, j.model_id), COALESCE(p.deliver_to_telegram, true)
		FROM job j
		LEFT JOIN catalog_offer o ON o.id = j.offer_id
		LEFT JOIN catalog_model m ON m.id = j.model_id
		LEFT JOIN profile p ON p.user_id = j.user_id
		WHERE j.surface = 'site' AND j.status = 'done' AND j.deleted_at IS NULL AND j.finished_at > $1 AND j.stack = $3
		  AND NOT EXISTS (SELECT 1 FROM site_delivery d WHERE d.job_id = j.id)
		ORDER BY j.finished_at, j.id
		LIMIT $2`, since, limit, s.stack)
	if err != nil {
		return nil, fmt.Errorf("site results: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SiteResult, error) {
		var r SiteResult
		err := row.Scan(&r.JobID, &r.UserID, &r.Prompt, &r.Kind, &r.Model, &r.Deliver)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("site results: %w", err)
	}
	return out, nil
}

// ClaimSiteResult marks the job's result as being sent and tells whether this
// call did: one sender ever gets it.
func (s *Store) ClaimSiteResult(ctx context.Context, jobID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO site_delivery (job_id, state) VALUES ($1, 'sending') ON CONFLICT (job_id) DO NOTHING`, jobID)
	if err != nil {
		return false, fmt.Errorf("claim the site result of job %d: %w", jobID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReleaseSiteResult gives back the claim on a result nothing of which went
// out, so a later round tries again.
func (s *Store) ReleaseSiteResult(ctx context.Context, jobID int64) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM site_delivery WHERE job_id = $1 AND state = 'sending'`, jobID); err != nil {
		return fmt.Errorf("release the site result of job %d: %w", jobID, err)
	}
	return nil
}

// EndSiteResult records that the claimed result will not be sent: state is
// DeliveryClosed, DeliveryFailed or DeliveryOff.
func (s *Store) EndSiteResult(ctx context.Context, jobID int64, state string) error {
	if state != DeliveryClosed && state != DeliveryFailed && state != DeliveryOff {
		return fmt.Errorf("end the site result of job %d: %q is not an end", jobID, state)
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE site_delivery SET state = $2, updated_at = now() WHERE job_id = $1 AND state = 'sending'`, jobID, state)
	if err != nil {
		return fmt.Errorf("end the site result of job %d: %w", jobID, err)
	}
	return nil
}

// SentSiteResult records the claimed result as sent to the DM run.ChatID in
// res, and gives the job a chat run that is done there, so Animate and a
// reply to the result work as on any result in a chat. Of run only JobID,
// ChatID and Lang are read.
func (s *Store) SentSiteResult(ctx context.Context, run ChatRun, res ChatResult) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE site_delivery SET state = 'sent', message_id = $2, updated_at = now()
			WHERE job_id = $1 AND state = 'sending'`, run.JobID, res.MessageID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO chat_run (job_id, chat_id, command_message_id, lang, stage,
			                      result_message_id, result_file_id, result_file_unique_id, result_file_type, result_asset_id, stack)
			VALUES ($1, $2, 0, $3, 'done', $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), NULLIF($8::bigint, 0), $9)
			ON CONFLICT (job_id) DO NOTHING`,
			run.JobID, run.ChatID, run.Lang, res.MessageID, res.FileID, res.FileUniqueID, res.FileType, res.AssetID, s.stack)
		return err
	})
	if err != nil {
		return fmt.Errorf("record the site result of job %d as sent: %w", run.JobID, err)
	}
	return nil
}
