// SPDX-License-Identifier: Apache-2.0
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/r2"
)

const (
	// DefaultMaxVideo bounds a clip put into incoming/: a minute of 4K from
	// a phone fits.
	DefaultMaxVideo = 512 << 20
	// DefaultIncomingTTL is how long the link to put a clip lives. A slow
	// uplink takes minutes for a large clip, and the link has to last until
	// the PUT begins.
	DefaultIncomingTTL = 30 * time.Minute
)

var (
	// ErrNoUpload: nothing was put under that token, or not by the person.
	ErrNoUpload = errors.New("nothing was uploaded under that token")
	// ErrNotVideo: the file is a picture, not a clip.
	ErrNotVideo = errors.New("the file is not a video")
)

// VideoUpload is where the browser puts a clip: a presigned PUT straight
// into the bucket's incoming/, which the bucket's lifecycle rule clears
// after a day (docs/design.md, Storage and retention). Cloudflare takes at
// most 100 MB in a request to the service, and a clip is often more.
type VideoUpload struct {
	Token      string
	URL        string
	ExpiresAt  time.Time
	MaxBytes   int64
	MaxSeconds float64
}

// VideoUpload makes a link to put one clip. The token names it to TakeVideo
// and is the person's alone: the key it stands for has their id in it.
func (s *Store) VideoUpload(userID int64) (VideoUpload, error) {
	token := random()
	link, err := s.bucket.PresignPut(s.incomingKey(userID, token), s.cfg.IncomingTTL)
	if err != nil {
		return VideoUpload{}, err
	}
	return VideoUpload{
		Token: token, URL: link, ExpiresAt: time.Now().Add(s.cfg.IncomingTTL),
		MaxBytes: s.cfg.MaxVideo, MaxSeconds: s.media.MaxClipSeconds(),
	}, nil
}

// incomingKey is under incoming/ whatever the prefix, so the lifecycle rule
// clears a test bot's clips too.
func (s *Store) incomingKey(userID int64, token string) string {
	return "incoming/" + s.cfg.Prefix + fmt.Sprintf("%d/%s", userID, token)
}

// ValidToken: 32 lowercase hex digits, as VideoUpload makes them.
func ValidToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	for _, r := range token {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// TakeVideo makes the clip put under token, or the part cut of it, a file
// of the person's, as an upload a job takes. The size is asked of the
// bucket before anything is read, and the content is read from its bytes.
// A clip framy cannot take goes from incoming/ at once; one that is only too
// long stays, so the person can pick a part without sending it again.
func (s *Store) TakeVideo(ctx context.Context, userID int64, token string, cut *media.Cut) (Asset, error) {
	if !ValidToken(token) {
		return Asset{}, ErrNoUpload
	}
	key := s.incomingKey(userID, token)
	obj, err := s.bucket.Head(ctx, key)
	switch {
	case errors.Is(err, r2.ErrNotFound):
		return Asset{}, ErrNoUpload
	case err != nil:
		return Asset{}, err
	case obj.Size > s.cfg.MaxVideo:
		s.dropIncoming(ctx, key)
		return Asset{}, ErrTooBig
	}
	dir, done, err := s.work.Dir("video")
	if err != nil {
		return Asset{}, err
	}
	defer done()
	if err := s.fetchObject(ctx, key, filepath.Join(dir, media.In), s.cfg.MaxVideo); err != nil {
		if errors.Is(err, ErrTooBig) {
			s.dropIncoming(ctx, key)
		}
		return Asset{}, err
	}
	a, err := s.keepVideo(ctx, userID, dir, cut)
	if err != nil {
		if errors.Is(err, media.ErrUnsupportedVideo) || errors.Is(err, media.ErrUnreadableVideo) || errors.Is(err, media.ErrVideoTooLarge) {
			s.dropIncoming(ctx, key)
		}
		return Asset{}, err
	}
	// The clip is kept; the copy in incoming/ is not needed. When this
	// fails, the lifecycle rule takes it within a day.
	s.dropIncoming(ctx, key)
	return a, nil
}

func (s *Store) dropIncoming(ctx context.Context, key string) {
	if err := s.bucket.Delete(context.WithoutCancel(ctx), key); err != nil {
		s.log.Warn("a clip stays in incoming/ until the lifecycle rule", "key", key, "error", err)
	}
}

// CutVideo makes the part cut of a clip of the person's, an upload or a
// result, a new upload: a result longer than an editor takes goes to it so.
func (s *Store) CutVideo(ctx context.Context, userID, assetID int64, cut media.Cut) (Asset, error) {
	src, err := s.video(ctx, userID, assetID)
	if err != nil {
		return Asset{}, err
	}
	dir, done, err := s.work.Dir("cut")
	if err != nil {
		return Asset{}, err
	}
	defer done()
	if err := s.fetchObject(ctx, src.Key, filepath.Join(dir, media.In), s.cfg.MaxResult); err != nil {
		return Asset{}, err
	}
	return s.keepVideo(ctx, userID, dir, &cut)
}

// LastFrame keeps the last frame of a clip of the person's as a new upload,
// through the photo pipeline: the start frame of a run that continues the
// clip on a model that starts from a frame.
func (s *Store) LastFrame(ctx context.Context, userID, assetID int64) (Asset, error) {
	src, err := s.video(ctx, userID, assetID)
	if err != nil {
		return Asset{}, err
	}
	dir, done, err := s.work.Dir("frame")
	if err != nil {
		return Asset{}, err
	}
	defer done()
	if err := s.fetchObject(ctx, src.Key, filepath.Join(dir, media.In), s.cfg.MaxResult); err != nil {
		return Asset{}, err
	}
	path, err := s.media.LastFrame(ctx, dir)
	if err != nil {
		return Asset{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Asset{}, err
	}
	defer f.Close()
	return s.UploadCopy(ctx, userID, f, "")
}

// video is a clip of the person's: ErrNotFound for a file that is not
// theirs, ErrNotVideo for a picture.
func (s *Store) video(ctx context.Context, userID, assetID int64) (Asset, error) {
	a := Asset{ID: assetID, UserID: userID}
	err := s.pool.QueryRow(ctx, `SELECT kind, object_key, content_type FROM asset WHERE id=$1 AND user_id=$2`,
		assetID, userID).Scan(&a.Kind, &a.Key, &a.ContentType)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Asset{}, ErrNotFound
	case err != nil:
		return Asset{}, err
	case !strings.HasPrefix(a.ContentType, "video/"):
		return Asset{}, ErrNotVideo
	}
	return a, nil
}

// fetchObject reads key into path, at most limit bytes.
func (s *Store) fetchObject(ctx context.Context, key, path string, limit int64) error {
	body, err := s.bucket.Get(ctx, key)
	if errors.Is(err, r2.ErrNotFound) {
		return ErrNoUpload
	}
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("read %s: %w", key, err)
	case n > limit:
		return ErrTooBig
	}
	return nil
}

// keepVideo puts the clip at dir/In through the pipeline and keeps it as an
// upload, the object before the row.
func (s *Store) keepVideo(ctx context.Context, userID int64, dir string, cut *media.Cut) (Asset, error) {
	v, err := s.media.NormalizeVideo(ctx, dir, cut)
	if err != nil {
		return Asset{}, err
	}
	a := Asset{
		UserID: userID, Kind: "upload", Key: s.cfg.Prefix + fmt.Sprintf("u/%d/in/%s%s", userID, random(), ext(v.ContentType)),
		ContentType: v.ContentType, Width: v.Width, Height: v.Height, Seconds: v.Seconds, FPS: v.FPS, Audio: v.Audio,
	}
	if err := s.putFile(ctx, &a, v.Path); err != nil {
		return Asset{}, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO asset (user_id, kind, object_key, content_type, bytes, width, height, sha256, seconds, fps, audio)
		VALUES ($1, 'upload', $2, $3, $4, $5, $6, $7, $8, NULLIF($9, 0), $10)
		RETURNING id, created_at`,
		a.UserID, a.Key, a.ContentType, a.Bytes, a.Width, a.Height, a.SHA256, a.Seconds, a.FPS, a.Audio).Scan(&a.ID, &a.CreatedAt)
	if err != nil {
		if derr := s.bucket.Delete(context.WithoutCancel(ctx), a.Key); derr != nil {
			s.log.Error("a clip's object outlived its row", "key", a.Key, "error", derr)
		}
		return Asset{}, fmt.Errorf("record the clip: %w", err)
	}
	s.checkBucket(ctx)
	return a, nil
}

// probeData is probeResult for a result that came as bytes.
func (s *Store) probeData(ctx context.Context, a *Asset, data []byte) error {
	dir, done, err := s.work.Dir("result")
	if err != nil {
		return err
	}
	defer done()
	path := filepath.Join(dir, "result")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	s.probeResult(ctx, a, path)
	return nil
}

// probeResult fills a video result's size, length, frame rate and sound
// from the file at path. A result ffprobe cannot read is kept as it came,
// without them: the run is paid for, and the person sees what they got.
func (s *Store) probeResult(ctx context.Context, a *Asset, path string) {
	if !strings.HasPrefix(a.ContentType, "video/") {
		return
	}
	v, err := s.media.ProbeVideo(ctx, path)
	if err != nil {
		s.log.Warn("a video result ffprobe cannot read", "job", a.JobID, "error", err)
		return
	}
	a.Width, a.Height, a.Seconds, a.FPS, a.Audio = v.Width, v.Height, v.Seconds, v.FPS, v.Audio
}
