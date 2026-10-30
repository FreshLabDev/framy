// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Stages of a chat run (migrations/014_chat_run.sql).
const (
	StagePreview    = "preview"
	StageGenerating = "generating"
	StageDone       = "done"
	StageFailed     = "failed"
	StageGone       = "gone"
)

// ErrNoRun is a job that no chat follows.
var ErrNoRun = errors.New("no chat run for the job")

// ChatRun is what a chat shows of a job it started: where the messages are
// and how far the chat has been told. UserID, Prompt, Free, Kind, Status,
// ErrorClass, Error, Model and Created come from the job and are only read.
type ChatRun struct {
	JobID            int64
	ChatID           int64
	ThreadID         int
	CommandMessageID int64
	PreviewMessageID int64
	// PreviewEphemeral: the preview is an ephemeral message to the requester,
	// and PreviewMessageID its ephemeral id.
	PreviewEphemeral bool
	// CommandEphemeral: the command was ephemeral, and nothing in the chat
	// can reply to it.
	CommandEphemeral bool
	// PanelMessageID is the ephemeral Model and Settings panel the requester
	// sees in place of the preview; zero for none.
	PanelMessageID int64
	Lang           string
	Taken, Album   int
	Note           string
	// ClipFileID and ClipBytes are the clip the run edits as Telegram has
	// it, and ClipSeconds its whole length; empty and zero for a run on
	// pictures.
	ClipFileID  string
	ClipBytes   int64
	ClipSeconds float64
	Stage       string
	// GenerateAt is when Generate was pressed; zero before.
	GenerateAt time.Time
	Stopped    bool
	// ResultMessageID is the message the result went out in; zero before.
	ResultMessageID int64

	UserID int64
	Prompt string
	Free   bool
	// Kind is what the job makes, "image" or "video", as catalog.Kind spells
	// it.
	Kind string
	// Status and ErrorClass are the job's, as jobs.Status and jobs.Class
	// spell them; plain strings, since jobs' own tests import db.
	Status     string
	ErrorClass string
	Error      string
	// Model is the name of the job's model, as the catalog has it: the
	// model's own, "Nano Banana Pro", not the offer's with its maker before it.
	Model string
	// OfferID and Settings are what the job runs with.
	OfferID  string
	Settings map[string]any
	Created  time.Time
}

const chatRunColumns = `
	r.job_id, r.chat_id, r.thread_id, r.command_message_id, COALESCE(r.preview_message_id, 0),
	r.preview_ephemeral, r.command_ephemeral, COALESCE(r.panel_message_id, 0), r.lang,
	r.taken, r.album, r.note, r.clip_file_id, r.clip_bytes, r.clip_seconds, r.stage, r.generate_at, r.stopped,
	COALESCE(r.result_message_id, 0), j.user_id, j.prompt, j.free, j.kind, j.status, COALESCE(j.error_class, ''), COALESCE(j.error, ''), COALESCE(m.name, o.name, j.model_id),
	j.offer_id, j.settings, j.created_at`

const chatRunFrom = `
	FROM chat_run r JOIN job j ON j.id = r.job_id
	LEFT JOIN catalog_offer o ON o.id = j.offer_id
	LEFT JOIN catalog_model m ON m.id = j.model_id`

func scanChatRun(row pgx.Row) (ChatRun, error) {
	var r ChatRun
	var at *time.Time
	err := row.Scan(&r.JobID, &r.ChatID, &r.ThreadID, &r.CommandMessageID, &r.PreviewMessageID,
		&r.PreviewEphemeral, &r.CommandEphemeral, &r.PanelMessageID, &r.Lang,
		&r.Taken, &r.Album, &r.Note, &r.ClipFileID, &r.ClipBytes, &r.ClipSeconds, &r.Stage, &at, &r.Stopped,
		&r.ResultMessageID, &r.UserID, &r.Prompt, &r.Free, &r.Kind, &r.Status, &r.ErrorClass, &r.Error, &r.Model,
		&r.OfferID, &r.Settings, &r.Created)
	if at != nil {
		r.GenerateAt = *at
	}
	return r, err
}

// AddChatRun records the preview of a job that is a draft now. A job has one
// run: a second for the same job changes nothing and is no error, since the
// update that made it may come again.
func (s *Store) AddChatRun(ctx context.Context, r ChatRun) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat_run (job_id, chat_id, thread_id, command_message_id, preview_message_id, lang, taken, album, note,
		                      preview_ephemeral, command_ephemeral, clip_file_id, clip_bytes, clip_seconds)
		VALUES ($1, $2, $3, $4, NULLIF($5::bigint, 0), $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (job_id) DO NOTHING`,
		r.JobID, r.ChatID, r.ThreadID, r.CommandMessageID, r.PreviewMessageID, r.Lang, r.Taken, r.Album, r.Note,
		r.PreviewEphemeral, r.CommandEphemeral, r.ClipFileID, r.ClipBytes, r.ClipSeconds)
	if err != nil {
		return fmt.Errorf("add the chat run of job %d: %w", r.JobID, err)
	}
	return nil
}

// ChatRun reads the run of job id, or ErrNoRun.
func (s *Store) ChatRun(ctx context.Context, jobID int64) (ChatRun, error) {
	r, err := scanChatRun(s.pool.QueryRow(ctx, `SELECT `+chatRunColumns+chatRunFrom+` WHERE r.job_id = $1`, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatRun{}, ErrNoRun
	}
	if err != nil {
		return ChatRun{}, fmt.Errorf("chat run of job %d: %w", jobID, err)
	}
	return r, nil
}

// OpenChatRuns are the runs a chat still has to be told about: a preview or
// the generating loop, oldest first.
func (s *Store) OpenChatRuns(ctx context.Context) ([]ChatRun, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+chatRunColumns+chatRunFrom+`
		WHERE r.stage IN ('preview', 'generating') ORDER BY r.job_id`)
	if err != nil {
		return nil, fmt.Errorf("open chat runs: %w", err)
	}
	defer rows.Close()
	var out []ChatRun
	for rows.Next() {
		r, err := scanChatRun(rows)
		if err != nil {
			return nil, fmt.Errorf("open chat runs: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MoveChatRun moves the run of job id from stage from to stage to, and tells
// whether it was at from. Moving to generating stamps the press of Generate.
func (s *Store) MoveChatRun(ctx context.Context, jobID int64, from, to string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat_run SET stage = $3, updated_at = now(),
		       generate_at = CASE WHEN $3 = 'generating' THEN now() ELSE generate_at END
		WHERE job_id = $1 AND stage = $2`, jobID, from, to)
	if err != nil {
		return false, fmt.Errorf("move the chat run of job %d to %s: %w", jobID, to, err)
	}
	return tag.RowsAffected() == 1, nil
}

// SetChatRunPreview records the message that shows the run now: the loop
// Generate posted in the chat in place of an ephemeral preview.
func (s *Store) SetChatRunPreview(ctx context.Context, jobID, messageID int64, ephemeral bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE chat_run SET preview_message_id = NULLIF($2::bigint, 0), preview_ephemeral = $3, updated_at = now()
		WHERE job_id = $1`, jobID, messageID, ephemeral)
	if err != nil {
		return fmt.Errorf("set the preview of job %d: %w", jobID, err)
	}
	return nil
}

// SetChatRunPanel records the ephemeral panel of a run; zero for none.
func (s *Store) SetChatRunPanel(ctx context.Context, jobID, panelID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE chat_run SET panel_message_id = NULLIF($2::bigint, 0), updated_at = now()
		WHERE job_id = $1`, jobID, panelID)
	if err != nil {
		return fmt.Errorf("set the panel of job %d: %w", jobID, err)
	}
	return nil
}

// SetChatRunNote records the line about what the run takes now: another
// length of its clip is another note, or none.
func (s *Store) SetChatRunNote(ctx context.Context, jobID int64, note string) error {
	_, err := s.pool.Exec(ctx, `UPDATE chat_run SET note = $2, updated_at = now() WHERE job_id = $1`, jobID, note)
	if err != nil {
		return fmt.Errorf("set the note of job %d: %w", jobID, err)
	}
	return nil
}

// ResultRun is the run whose result went out as the message, if any: a
// reply to framy's own message takes its picture only when it is a result.
func (s *Store) ResultRun(ctx context.Context, chatID, messageID int64) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT job_id FROM chat_run WHERE chat_id = $1 AND result_message_id = $2`, chatID, messageID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("the run of result %d in %d: %w", messageID, chatID, err)
	}
	return id, true, nil
}

// StopChatRun marks a generating run whose job could not be cancelled: its
// result goes to the person's history and not to the chat. It tells whether
// the run was generating.
func (s *Store) StopChatRun(ctx context.Context, jobID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat_run SET stopped = true, stage = 'gone', updated_at = now()
		WHERE job_id = $1 AND stage = 'generating'`, jobID)
	if err != nil {
		return false, fmt.Errorf("stop the chat run of job %d: %w", jobID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// File types of a result, as the message carried it.
const (
	FilePhoto     = "photo"
	FileVideo     = "video"
	FileAnimation = "animation"
	FileDocument  = "document"
)

// ChatResult is the message a result went out in.
type ChatResult struct {
	MessageID    int64
	FileID       string
	FileUniqueID string
	// FileType is FilePhoto, FileVideo, FileAnimation or FileDocument;
	// empty for a message with no file.
	FileType string
}

// FinishChatRun ends a generating run as done, with the message of its
// result, or as failed. It tells whether the run was generating.
func (s *Store) FinishChatRun(ctx context.Context, jobID int64, stage string, res ChatResult) (bool, error) {
	if stage != StageDone && stage != StageFailed {
		return false, fmt.Errorf("finish the chat run of job %d: %q is not an end", jobID, stage)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat_run SET stage = $2, updated_at = now(),
		       result_message_id = NULLIF($3::bigint, 0), result_file_id = NULLIF($4, ''), result_file_unique_id = NULLIF($5, ''),
		       result_file_type = NULLIF($6, '')
		WHERE job_id = $1 AND stage = 'generating'`, jobID, stage, res.MessageID, res.FileID, res.FileUniqueID, res.FileType)
	if err != nil {
		return false, fmt.Errorf("finish the chat run of job %d: %w", jobID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// JobByKey is the job a dedupe key started, if any.
func (s *Store) JobByKey(ctx context.Context, key string) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM job WHERE dedupe_key = $1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("job by key: %w", err)
	}
	return id, true, nil
}
