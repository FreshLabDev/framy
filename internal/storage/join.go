// SPDX-License-Identifier: Apache-2.0
package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
)

const (
	// joinLimit bounds joining a clip to the run that went on from it.
	joinLimit = 2 * time.Minute
	// joinRoom is what ending the job still needs after the join, within
	// the time Keep is given; below joinLeast the join is not begun.
	joinRoom  = 30 * time.Second
	joinLeast = 10 * time.Second
)

// join keeps the clip a run went on from with the run's video after it, as
// the job's joined file, when the run started from a frame LastFrame took:
// Continue on a model that starts from a frame (docs/design.md, Actions on
// a result). The clip goes up to the frame, and the run's video follows
// without its first frame, which is that frame again. The joined clip
// keeps where each part begins: those of a joined clip it went on from,
// then the run's. It returns nil when the run went on from no clip.
func (s *Store) join(ctx context.Context, j jobs.Job) error {
	if !startsFromFrame(j.Settings) {
		return nil
	}
	var clip, part Asset
	var at *float64
	var earlier []float64
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.object_key, f.frame_at, c.segments
		FROM job_input i
		JOIN asset f ON f.id = i.asset_id AND f.user_id = $2
		JOIN asset c ON c.id = f.frame_of AND c.user_id = $2
		WHERE i.job_id = $1 AND i.position = 0`, j.ID, j.UserID).Scan(&clip.ID, &clip.Key, &at, &earlier)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the clip the run went on from: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		SELECT id, object_key FROM asset
		WHERE job_id = $1 AND kind = 'result' AND content_type LIKE 'video/%'
		ORDER BY id LIMIT 1`, j.ID).Scan(&part.ID, &part.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the run's video: %w", err)
	}

	limit := joinLimit
	if deadline, ok := ctx.Deadline(); ok {
		limit = min(limit, time.Until(deadline)-joinRoom)
	}
	if limit < joinLeast {
		return errors.New("no time is left to join the clips")
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	dir, done, err := s.work.Dir("join")
	if err != nil {
		return err
	}
	defer done()
	if err := s.fetchObject(ctx, clip.Key, filepath.Join(dir, media.JoinFirst), s.cfg.MaxResult); err != nil {
		return fmt.Errorf("read clip %d: %w", clip.ID, err)
	}
	if err := s.fetchObject(ctx, part.Key, filepath.Join(dir, media.JoinNext), s.cfg.MaxResult); err != nil {
		return fmt.Errorf("read result %d: %w", part.ID, err)
	}
	upTo := 0.0
	if at != nil {
		upTo = *at
	}
	v, nextAt, err := s.media.Join(ctx, dir, upTo)
	if err != nil {
		return err
	}
	segments := partsBefore(earlier, nextAt)
	a := Asset{
		UserID: j.UserID, Kind: "joined", JobID: j.ID, Key: joinedKey(s.cfg.Prefix, j),
		ContentType: v.ContentType, Width: v.Width, Height: v.Height, Seconds: v.Seconds, FPS: v.FPS, Audio: v.Audio,
	}
	if err := s.putFile(ctx, &a, v.Path); err != nil {
		return err
	}
	a.Thumb = s.keepThumb(ctx, a, func(dst string) error { return linkOrCopy(v.Path, dst) })
	thumb, tw, th, tb := a.Thumb.columns()
	// Joined again after a crash, the clip keeps its row: the key is the same.
	_, err = s.pool.Exec(ctx, `
		INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, width, height, sha256, seconds, fps, audio,
		  thumb_key, thumb_width, thumb_height, thumb_bytes, segments)
		VALUES ($1, 'joined', $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), $8, NULLIF($9::float8, 0), NULLIF($10::float8, 0), $11, $12, $13, $14, $15, $16)
		ON CONFLICT (object_key) DO UPDATE SET bytes = EXCLUDED.bytes, sha256 = EXCLUDED.sha256,
		  width = EXCLUDED.width, height = EXCLUDED.height, seconds = EXCLUDED.seconds, fps = EXCLUDED.fps, audio = EXCLUDED.audio,
		  segments = EXCLUDED.segments,
		  thumb_key = COALESCE(EXCLUDED.thumb_key, asset.thumb_key), thumb_width = COALESCE(EXCLUDED.thumb_width, asset.thumb_width),
		  thumb_height = COALESCE(EXCLUDED.thumb_height, asset.thumb_height), thumb_bytes = COALESCE(EXCLUDED.thumb_bytes, asset.thumb_bytes)`,
		a.UserID, a.JobID, a.Key, a.ContentType, a.Bytes, a.Width, a.Height, a.SHA256, a.Seconds, a.FPS, a.audio(),
		thumb, tw, th, tb, segments)
	if err != nil {
		return fmt.Errorf("record the joined clip: %w", err)
	}
	return nil
}

// partsBefore is where each part of a joined clip begins: those of the clip
// it went on from that begin before nextAt (one at 0 for a clip of one
// part), then nextAt, where the run's video begins.
func partsBefore(earlier []float64, nextAt float64) []float64 {
	out := []float64{0}
	for _, at := range earlier {
		if at > 0 && at < nextAt {
			out = append(out, at)
		}
	}
	return append(out, nextAt)
}

// startsFromFrame: the run's first picture is the first frame of its video.
func startsFromFrame(settings map[string]any) bool {
	switch list := settings["frame_images"].(type) {
	case []string:
		return len(list) > 0 && list[0] == "first_frame"
	case []any:
		return len(list) > 0 && list[0] == "first_frame"
	}
	return false
}

// joinedKey is where the joined clip of job j goes, beside its results.
func joinedKey(prefix string, j jobs.Job) string {
	return prefix + fmt.Sprintf("u/%d/out/%d-joined.mp4", j.UserID, j.ID)
}
