// SPDX-License-Identifier: Apache-2.0

// Package storage keeps people's files in R2 (docs/design.md, Storage and
// retention): the photos they bring, normalized, and what their runs make,
// copied the moment a run ends since a provider's link lives an hour or
// two. A file stays until its owner deletes it, except an upload no job
// took within an hour, which is left from a failure. The host keeps only
// working files, in a directory per upload or result that goes when the
// work does.
//
// The object is written before its row and removed before it, so a row
// never names an object that was not written, and a failed delete can be
// made again.
package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/r2"
)

const (
	// DefaultMaxUpload bounds a photo brought to framy. A Telegram photo is
	// at most 10 MB, a document 20 MB through the cloud Bot API.
	DefaultMaxUpload = 50 << 20
	// DefaultMaxResult bounds a result fetched from a provider's link.
	DefaultMaxResult = 512 << 20
	// DefaultInputTTL is how long a provider may fetch an input: one call,
	// with room for a slow queue. Each submit presigns anew.
	DefaultInputTTL = 15 * time.Minute
	// DefaultLinkTTL is how long the owner's link to a file lives at the
	// least: from the end of the window it was made in.
	DefaultLinkTTL = 10 * time.Minute
	// DefaultLinkWindow: the owner's links are signed at the start of the
	// window they are made in, so a file keeps one link through the window
	// and the browser takes it from its cache.
	DefaultLinkWindow = time.Hour
	// DefaultAlertStep: the operator chat hears each time the bucket passes
	// another 10 GB, the first being where R2's free storage ends.
	DefaultAlertStep = 10_000_000_000
	// DefaultFetchTimeout bounds the download of one result.
	DefaultFetchTimeout = 5 * time.Minute
	// maxThumbTries is how many times FillThumbs tries a result's thumbnail.
	maxThumbTries = 3
	// UploadTTL is how long an upload waits for a job to take it. The site
	// uploads on Generate and the chat right before its draft, so an upload
	// older than this that no job took is left from a failure.
	UploadTTL = time.Hour
)

var (
	// ErrNotFound: no such file, or not the person's.
	ErrNotFound = errors.New("no such file")
	// ErrTooBig: the upload is over the limit.
	ErrTooBig = errors.New("the file is too big")
)

// Bucket is where the files are: *r2.Client.
type Bucket interface {
	Put(ctx context.Context, key, contentType string, body io.ReaderAt, size int64, sum string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Head(ctx context.Context, key string) (r2.Object, error)
	Delete(ctx context.Context, key string) error
	PresignGet(key string, ttl time.Duration, disposition string) (string, error)
	PresignGetAt(key string, at time.Time, ttl time.Duration, disposition, cacheControl string) (string, error)
	PresignPut(key string, ttl time.Duration) (string, error)
}

// Config is the store's setup. Pool's search_path is framy.
type Config struct {
	Pool   *pgxpool.Pool
	Bucket Bucket
	Media  *media.Converter
	Work   media.Work
	// Fetch downloads results from providers' links: safehttp.Client, so a
	// link cannot point framy at its own network.
	Fetch *http.Client
	// Prefix goes before every object key: "test/" for a test bot that
	// shares the bucket, empty for production.
	Prefix string
	// Notify tells the operator chat; nil logs instead.
	Notify    func(ctx context.Context, text string) error
	Log       *slog.Logger
	MaxUpload int64
	MaxResult int64
	// MaxVideo bounds a clip put into incoming/, IncomingTTL the link to
	// put it.
	MaxVideo    int64
	IncomingTTL time.Duration
	InputTTL    time.Duration
	// LinkTTL and LinkWindow: a link to an owner's file is signed at the
	// start of the LinkWindow it is made in and lives LinkWindow plus
	// LinkTTL, so it has LinkTTL left at the least.
	LinkTTL    time.Duration
	LinkWindow time.Duration
	AlertStep  int64
	// Now is the clock of the links, for tests.
	Now func() time.Time
}

// Store keeps the files.
type Store struct {
	pool   *pgxpool.Pool
	bucket Bucket
	media  *media.Converter
	work   media.Work
	fetch  *http.Client
	notify func(ctx context.Context, text string) error
	log    *slog.Logger
	cfg    Config

	// thumbErr says why results get no thumbnails: an ffmpeg without WebP.
	thumbOnce sync.Once
	thumbErr  error
}

// New checks cfg and fills its defaults.
func New(cfg Config) (*Store, error) {
	if cfg.Pool == nil || cfg.Bucket == nil || cfg.Media == nil || cfg.Work.Root == "" || cfg.Fetch == nil {
		return nil, errors.New("storage: the pool, the bucket, the converter, the work root and the fetch client are needed")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	setDefault(&cfg.MaxUpload, DefaultMaxUpload)
	setDefault(&cfg.MaxResult, DefaultMaxResult)
	setDefault(&cfg.MaxVideo, DefaultMaxVideo)
	setDefault(&cfg.IncomingTTL, DefaultIncomingTTL)
	setDefault(&cfg.InputTTL, DefaultInputTTL)
	setDefault(&cfg.LinkTTL, DefaultLinkTTL)
	setDefault(&cfg.LinkWindow, DefaultLinkWindow)
	setDefault(&cfg.AlertStep, DefaultAlertStep)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Store{
		pool: cfg.Pool, bucket: cfg.Bucket, media: cfg.Media, work: cfg.Work, fetch: cfg.Fetch,
		notify: cfg.Notify, log: cfg.Log, cfg: cfg,
	}, nil
}

func setDefault[T int64 | time.Duration](v *T, d T) {
	if *v <= 0 {
		*v = d
	}
}

// Asset is a file of a person's.
type Asset struct {
	ID          int64
	UserID      int64
	Kind        string
	JobID       int64
	Key         string
	ContentType string
	Bytes       int64
	Width       int
	Height      int
	// Seconds, FPS and Audio are a clip's, as ffprobe read them; Seconds is
	// 0 for a picture and for a video kept before framy read them.
	Seconds   float64
	FPS       float64
	Audio     bool
	SHA256    string
	CreatedAt time.Time
	// Thumb is a result's thumbnail, nil until it has one.
	Thumb *Thumb
}

// Thumb is the smaller copy of a result the site's library shows: a WebP at
// most media.ThumbLongSide on the long side, of a video its poster frame.
// It lies next to the result in the bucket (thumbKey).
type Thumb struct {
	Key           string
	Width, Height int
	Bytes         int64
}

// columns are the thumbnail's values for the asset row: NULL without one.
func (t *Thumb) columns() (key *string, width, height *int, bytes *int64) {
	if t == nil {
		return nil, nil, nil, nil
	}
	return &t.Key, &t.Width, &t.Height, &t.Bytes
}

// audio is a clip's sound for its row: NULL for a picture, and for a video
// whose length is not known.
func (a Asset) audio() *bool {
	if a.Seconds == 0 {
		return nil
	}
	return &a.Audio
}

// Upload normalizes a photo the person brought and keeps it. The person is
// in core.person already, as core.touch puts them. A file that is not a
// picture framy takes comes back as media.ErrUnsupported, ErrTooLarge or
// ErrUnreadable, one over the limit as ErrTooBig.
func (s *Store) Upload(ctx context.Context, userID int64, r io.Reader) (Asset, error) {
	return s.UploadCopy(ctx, userID, r, "")
}

// UploadCopy is Upload that also leaves a copy of the normalized picture at
// copyTo, a path of the caller's, when it is set: the chat draws its preview
// from the same picture the provider gets.
func (s *Store) UploadCopy(ctx context.Context, userID int64, r io.Reader, copyTo string) (Asset, error) {
	dir, done, err := s.work.Dir("upload")
	if err != nil {
		return Asset{}, err
	}
	defer done()
	f, err := os.OpenFile(filepath.Join(dir, media.In), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Asset{}, err
	}
	n, err := io.Copy(f, io.LimitReader(r, s.cfg.MaxUpload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return Asset{}, fmt.Errorf("read the upload: %w", err)
	case n > s.cfg.MaxUpload:
		return Asset{}, ErrTooBig
	}
	img, err := s.media.Normalize(ctx, dir)
	if err != nil {
		return Asset{}, err
	}
	if copyTo != "" {
		if err := copyFile(img.Path, copyTo); err != nil {
			return Asset{}, fmt.Errorf("copy the upload: %w", err)
		}
	}
	a := Asset{
		UserID: userID, Kind: "upload", Key: s.cfg.Prefix + fmt.Sprintf("u/%d/in/%s%s", userID, random(), ext(img.ContentType)),
		ContentType: img.ContentType, Bytes: img.Bytes, Width: img.Width, Height: img.Height,
	}
	if err := s.putFile(ctx, &a, img.Path); err != nil {
		return Asset{}, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO asset (user_id, kind, object_key, content_type, bytes, width, height, sha256)
		VALUES ($1, 'upload', $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		a.UserID, a.Key, a.ContentType, a.Bytes, a.Width, a.Height, a.SHA256).Scan(&a.ID, &a.CreatedAt)
	if err != nil {
		// No row names the object: take it back rather than leave it.
		if derr := s.bucket.Delete(context.WithoutCancel(ctx), a.Key); derr != nil {
			s.log.Error("an upload's object outlived its row", "key", a.Key, "error", derr)
		}
		return Asset{}, fmt.Errorf("record the upload: %w", err)
	}
	s.checkBucket(ctx)
	return a, nil
}

// Inputs presigns a job's files for its provider: jobs.Inputs. A file the
// person deleted fails the job before anything is paid.
func (s *Store) Inputs(ctx context.Context, j jobs.Job) ([]jobs.Input, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.position, a.object_key, a.content_type
		FROM job_input i LEFT JOIN asset a ON a.id = i.asset_id
		WHERE i.job_id = $1
		ORDER BY i.position`, j.ID)
	if err != nil {
		return nil, err
	}
	type input struct {
		pos              int
		key, contentType *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (input, error) {
		var in input
		return in, r.Scan(&in.pos, &in.key, &in.contentType)
	})
	if err != nil {
		return nil, err
	}
	out := make([]jobs.Input, 0, len(list))
	for _, in := range list {
		if in.key == nil {
			return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d was deleted before the run", in.pos+1)}
		}
		link, err := s.bucket.PresignGet(*in.key, s.cfg.InputTTL, "")
		if err != nil {
			return nil, &jobs.Error{Class: jobs.Storage, Err: err}
		}
		out = append(out, jobs.Input{ContentType: *in.contentType, URL: link})
	}
	return out, nil
}

// Keep copies a run's results into the bucket: jobs.Sink. The keys follow
// the job and the result's place, so keeping them again after a crash
// writes the same objects and adds no rows.
func (s *Store) Keep(ctx context.Context, j jobs.Job, results []jobs.Result) error {
	for i, r := range results {
		if err := s.keep(ctx, j, i, r); err != nil {
			return fmt.Errorf("result %d: %w", i+1, err)
		}
	}
	// The run's own video is kept whatever becomes of the join.
	if err := s.join(ctx, j); err != nil && ctx.Err() == nil {
		s.log.Warn("a continuation is kept without its clip joined to it", "job", j.ID, "error", err)
	}
	s.checkBucket(ctx)
	return nil
}

func (s *Store) keep(ctx context.Context, j jobs.Job, i int, r jobs.Result) error {
	a := Asset{UserID: j.UserID, Kind: "result", JobID: j.ID, ContentType: r.ContentType}
	switch {
	case r.Data != nil:
		a.ContentType = resultType(r.ContentType, r.Data)
		if err := s.checkType(a.ContentType); err != nil {
			return err
		}
		a.Key = resultKey(s.cfg.Prefix, j, i, a.ContentType)
		info, _ := media.Inspect(bytes.NewReader(r.Data), int64(len(r.Data)))
		a.Width, a.Height = info.Width, info.Height
		if strings.HasPrefix(a.ContentType, "video/") {
			if err := s.probeData(ctx, &a, r.Data); err != nil {
				return err
			}
		}
		sum := sha256.Sum256(r.Data)
		a.SHA256, a.Bytes = hex.EncodeToString(sum[:]), int64(len(r.Data))
		if err := s.bucket.Put(ctx, a.Key, a.ContentType, bytes.NewReader(r.Data), a.Bytes, a.SHA256); err != nil {
			return err
		}
		a.Thumb = s.keepThumb(ctx, a, func(dst string) error { return os.WriteFile(dst, r.Data, 0o600) })
	case r.URL != "":
		dir, done, err := s.work.Dir("result")
		if err != nil {
			return err
		}
		defer done()
		path, contentType, err := s.download(ctx, r.URL, dir)
		if err != nil {
			return err
		}
		a.ContentType = contentType
		if r.ContentType != "" {
			a.ContentType = r.ContentType
		}
		if err := s.checkType(a.ContentType); err != nil {
			return err
		}
		a.Key = resultKey(s.cfg.Prefix, j, i, a.ContentType)
		if f, err := os.Open(path); err == nil {
			if st, err := f.Stat(); err == nil {
				info, _ := media.Inspect(f, st.Size())
				a.Width, a.Height = info.Width, info.Height
			}
			f.Close()
		}
		s.probeResult(ctx, &a, path)
		if err := s.putFile(ctx, &a, path); err != nil {
			return err
		}
		a.Thumb = s.keepThumb(ctx, a, func(dst string) error { return linkOrCopy(path, dst) })
	default:
		return errors.New("the result has neither bytes nor a link")
	}
	thumb, tw, th, tb := a.Thumb.columns()
	// Kept again after a crash without a thumbnail, a result keeps the one
	// it had: the key is the same.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, width, height, sha256, seconds, fps, audio,
		  thumb_key, thumb_width, thumb_height, thumb_bytes)
		VALUES ($1, 'result', $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), $8, NULLIF($9::float8, 0), NULLIF($10::float8, 0), $11, $12, $13, $14, $15)
		ON CONFLICT (object_key) DO UPDATE SET bytes = EXCLUDED.bytes, sha256 = EXCLUDED.sha256,
		  content_type = EXCLUDED.content_type, width = EXCLUDED.width, height = EXCLUDED.height,
		  seconds = EXCLUDED.seconds, fps = EXCLUDED.fps, audio = EXCLUDED.audio,
		  thumb_key = COALESCE(EXCLUDED.thumb_key, asset.thumb_key), thumb_width = COALESCE(EXCLUDED.thumb_width, asset.thumb_width),
		  thumb_height = COALESCE(EXCLUDED.thumb_height, asset.thumb_height), thumb_bytes = COALESCE(EXCLUDED.thumb_bytes, asset.thumb_bytes)`,
		a.UserID, a.JobID, a.Key, a.ContentType, a.Bytes, a.Width, a.Height, a.SHA256, a.Seconds, a.FPS, a.audio(),
		thumb, tw, th, tb)
	if err != nil {
		return fmt.Errorf("record the result: %w", err)
	}
	return nil
}

// thumbs tells whether this ffmpeg writes thumbnails, asking it once.
func (s *Store) thumbs(ctx context.Context) bool {
	s.thumbOnce.Do(func() {
		if s.thumbErr = s.media.CheckThumb(context.WithoutCancel(ctx)); s.thumbErr != nil {
			s.log.Warn("results get no thumbnails", "error", s.thumbErr)
		}
	})
	return s.thumbErr == nil
}

// keepThumb makes the thumbnail of the result a as it is kept. A failure
// never holds the result back: it is kept without one, and FillThumbs tries
// again.
func (s *Store) keepThumb(ctx context.Context, a Asset, fill func(dst string) error) *Thumb {
	if !s.thumbs(ctx) {
		return nil
	}
	t, err := s.makeThumb(ctx, a, fill)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("a result is kept without a thumbnail for now", "key", a.Key, "error", err)
		}
		return nil
	}
	return t
}

// makeThumb makes the thumbnail of the result a, of a video from its poster
// frame, and puts it next to the result. fill writes the result's file to
// the path it is given.
func (s *Store) makeThumb(ctx context.Context, a Asset, fill func(dst string) error) (*Thumb, error) {
	dir, done, err := s.work.Dir("thumb")
	if err != nil {
		return nil, err
	}
	defer done()
	src := filepath.Join(dir, media.In)
	if err := fill(src); err != nil {
		return nil, err
	}
	if strings.HasPrefix(a.ContentType, "video/") {
		if src, err = s.media.Poster(ctx, dir); err != nil {
			return nil, err
		}
	}
	out := filepath.Join(dir, "thumb.webp")
	w, h, err := s.media.Thumb(ctx, src, out)
	if err != nil {
		return nil, err
	}
	t := Asset{Key: thumbKey(a.Key), ContentType: "image/webp"}
	if err := s.putFile(ctx, &t, out); err != nil {
		return nil, err
	}
	return &Thumb{Key: t.Key, Width: w, Height: h, Bytes: t.Bytes}, nil
}

// thumbKey is where the thumbnail of the object under key goes: next to it,
// u/<person>/out/<job>-<n>.thumb.webp for a result.
func thumbKey(key string) string {
	return strings.TrimSuffix(key, filepath.Ext(key)) + ".thumb.webp"
}

// FillThumbs makes the thumbnails results lack: those kept before framy made
// them, and those whose thumbnail failed, each tried maxThumbTries times.
// The newest go first, as the library shows them. It makes up to limit and
// returns how many it made.
func (s *Store) FillThumbs(ctx context.Context, limit int) (int, error) {
	if !s.thumbs(ctx) {
		return 0, nil
	}
	made := 0
	for range limit {
		found, ok, err := s.fillThumb(ctx)
		if err != nil || !found {
			return made, err
		}
		if ok {
			made++
		}
	}
	return made, nil
}

// fillThumb makes the thumbnail of one result that lacks it, and reports
// false when none does. The row is locked while the thumbnail is made, so a
// delete of the result waits for it and then takes the thumbnail too.
func (s *Store) fillThumb(ctx context.Context) (found, made bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var a Asset
		err := tx.QueryRow(ctx, `
			SELECT id, object_key, content_type FROM asset
			WHERE kind IN ('result', 'joined') AND thumb_key IS NULL AND thumb_tries < $1
			ORDER BY thumb_tries, id DESC
			LIMIT 1
			FOR UPDATE SKIP LOCKED`, maxThumbTries).Scan(&a.ID, &a.Key, &a.ContentType)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		t, err := s.makeThumb(ctx, a, func(dst string) error { return s.fetchObject(ctx, a.Key, dst, s.cfg.MaxResult) })
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Warn("no thumbnail for a result", "asset", a.ID, "error", err)
			_, err := tx.Exec(ctx, `UPDATE asset SET thumb_tries = thumb_tries + 1 WHERE id = $1`, a.ID)
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE asset SET thumb_key = $2, thumb_width = $3, thumb_height = $4, thumb_bytes = $5 WHERE id = $1`,
			a.ID, t.Key, t.Width, t.Height, t.Bytes)
		made = err == nil
		return err
	})
	return found, made, err
}

// download fetches a provider's link into dir under the result limit.
func (s *Store) download(ctx context.Context, link, dir string) (path, contentType string, err error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", "", fmt.Errorf("the result's link: %w", err)
	}
	resp, err := s.fetch.Do(req)
	if err != nil {
		// The provider's link may carry a token: the error names the host
		// only.
		return "", "", fmt.Errorf("fetch the result from %s: %w", host(link), unwrapURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fetch the result from %s: %d", host(link), resp.StatusCode)
	}
	if resp.ContentLength > s.cfg.MaxResult {
		return "", "", fmt.Errorf("the result is %d bytes, over %d", resp.ContentLength, s.cfg.MaxResult)
	}
	path = filepath.Join(dir, "result")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	head := &headBuffer{max: 512}
	n, err := io.Copy(io.MultiWriter(f, head), io.LimitReader(resp.Body, s.cfg.MaxResult+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return "", "", fmt.Errorf("fetch the result from %s: %w", host(link), unwrapURL(err))
	case n > s.cfg.MaxResult:
		return "", "", fmt.Errorf("the result is over %d bytes", s.cfg.MaxResult)
	}
	contentType = resp.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mt
	}
	return path, resultType(contentType, head.b), nil
}

// putFile hashes the file at path and puts it under a.Key.
func (s *Store) putFile(ctx context.Context, a *Asset, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	a.Bytes, a.SHA256 = n, hex.EncodeToString(h.Sum(nil))
	return s.bucket.Put(ctx, a.Key, a.ContentType, f, n, a.SHA256)
}

func (s *Store) checkType(contentType string) error {
	if strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "video/") {
		return nil
	}
	return fmt.Errorf("the result is %q, not a picture or a video", contentType)
}

// Delete removes a file of the person's: the object, then the row. A job
// that was to take it fails before it is paid.
func (s *Store) Delete(ctx context.Context, userID, assetID int64) error {
	var key string
	var thumb *string
	err := s.pool.QueryRow(ctx, `SELECT object_key, thumb_key FROM asset WHERE id=$1 AND user_id=$2`, assetID, userID).Scan(&key, &thumb)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := s.bucket.Delete(ctx, key); err != nil {
		return err
	}
	if thumb != nil {
		if err := s.bucket.Delete(ctx, *thumb); err != nil {
			return err
		}
	}
	var after *string
	err = s.pool.QueryRow(ctx, `DELETE FROM asset WHERE id=$1 AND user_id=$2 RETURNING thumb_key`, assetID, userID).Scan(&after)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.dropLateThumb(ctx, thumb, after)
	return nil
}

// dropLateThumb deletes a thumbnail FillThumbs made while its result was
// being deleted: the row named it only as it went, after the delete waited
// for the row's lock. A failure leaves an object no row names, and is
// logged.
func (s *Store) dropLateThumb(ctx context.Context, before, after *string) {
	if after == nil || (before != nil && *before == *after) {
		return
	}
	if err := s.bucket.Delete(context.WithoutCancel(ctx), *after); err != nil {
		s.log.Error("a thumbnail outlived its result", "key", *after, "error", err)
	}
}

// SweepUploads deletes the uploads no job took within age, the object and
// then the row, and returns how many went. The row is locked while its
// object goes, so a job taking the file at that moment either waits and
// finds it gone, or holds it first and the sweep passes it by.
func (s *Store) SweepUploads(ctx context.Context, age time.Duration) (int, error) {
	n := 0
	for {
		gone, err := s.sweepUpload(ctx, age)
		if err != nil || !gone {
			return n, err
		}
		n++
	}
}

// sweepUpload deletes the oldest upload no job took within age, and reports
// false when there is none.
func (s *Store) sweepUpload(ctx context.Context, age time.Duration) (bool, error) {
	var gone bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		var key string
		err := tx.QueryRow(ctx, `
			SELECT a.id, a.object_key FROM asset a
			WHERE a.kind = 'upload' AND a.created_at <= now() - make_interval(secs => $1)
			  AND NOT EXISTS (SELECT 1 FROM job_input i WHERE i.asset_id = a.id)
			ORDER BY a.id
			LIMIT 1
			FOR UPDATE SKIP LOCKED`, age.Seconds()).Scan(&id, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.bucket.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete the upload %d: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM asset WHERE id=$1`, id); err != nil {
			return err
		}
		gone = true
		return nil
	})
	return gone, err
}

// DeleteResults removes every result of a job of the person's, and the clip
// joined to it, and returns how many went.
func (s *Store) DeleteResults(ctx context.Context, userID, jobID int64) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM asset WHERE job_id=$1 AND user_id=$2 AND kind IN ('result', 'joined') ORDER BY id`, jobID, userID)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	for n, id := range ids {
		if err := s.Delete(ctx, userID, id); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
	}
	return len(ids), nil
}

// DeleteJobFiles removes what a job of the person's leaves behind when the
// person deletes it: its results, and the uploads it took that no other job
// still takes. An upload another job takes stays with that job. It returns
// how many files went.
func (s *Store) DeleteJobFiles(ctx context.Context, userID, jobID int64) (int, error) {
	n, err := s.DeleteResults(ctx, userID, jobID)
	if err != nil {
		return n, err
	}
	for {
		gone, err := s.deleteInput(ctx, userID, jobID)
		if err != nil || !gone {
			return n, err
		}
		n++
	}
}

// deleteInput deletes one upload job takes and no other job does, and
// reports false when there is none. As in sweepUpload, the row is locked
// while its object goes, so a job taking the file at that moment either
// holds it first, and the file stays, or finds it gone.
func (s *Store) deleteInput(ctx context.Context, userID, jobID int64) (bool, error) {
	var gone bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		var key string
		err := tx.QueryRow(ctx, `
			SELECT a.id, a.object_key FROM job_input i JOIN asset a ON a.id = i.asset_id
			WHERE i.job_id = $1 AND a.user_id = $2 AND a.kind = 'upload'
			  AND NOT EXISTS (
			    SELECT 1 FROM job_input o JOIN job j ON j.id = o.job_id
			    WHERE o.asset_id = a.id AND o.job_id <> $1 AND j.deleted_at IS NULL)
			ORDER BY a.id
			LIMIT 1
			FOR UPDATE OF a SKIP LOCKED`, jobID, userID).Scan(&id, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.bucket.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete the upload %d: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM asset WHERE id = $1`, id); err != nil {
			return err
		}
		gone = true
		return nil
	})
	return gone, err
}

// deleteWorkers is how many objects DeleteAll deletes at once.
const deleteWorkers = 8

// DeleteAll removes every file of the person's kept before before, uploads
// and results, the object first and then its row, and returns how many went.
// A file kept later, by a run that started while the account was being
// deleted, is left. It stops at the first object that would not go; what
// went stays gone, so a second call carries on.
func (s *Store) DeleteAll(ctx context.Context, userID int64, before time.Time) (int, error) {
	n := 0
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT id, object_key, thumb_key FROM asset WHERE user_id = $1 AND created_at <= $2
			ORDER BY id LIMIT 256`, userID, before)
		if err != nil {
			return n, err
		}
		type file struct {
			id    int64
			key   string
			thumb *string
		}
		files, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (file, error) {
			var f file
			return f, r.Scan(&f.id, &f.key, &f.thumb)
		})
		if err != nil || len(files) == 0 {
			return n, err
		}
		thumbs := make(map[int64]*string, len(files))
		for _, f := range files {
			thumbs[f.id] = f.thumb
		}
		errs := make([]error, len(files))
		next := make(chan int)
		var wg sync.WaitGroup
		for range min(deleteWorkers, len(files)) {
			wg.Go(func() {
				for i := range next {
					errs[i] = s.bucket.Delete(ctx, files[i].key)
					if errs[i] == nil && files[i].thumb != nil {
						errs[i] = s.bucket.Delete(ctx, *files[i].thumb)
					}
				}
			})
		}
		for i := range files {
			next <- i
		}
		close(next)
		wg.Wait()
		var ids []int64
		var first error
		for i, f := range files {
			if errs[i] != nil {
				if first == nil {
					first = fmt.Errorf("delete the file %d: %w", f.id, errs[i])
				}
				continue
			}
			ids = append(ids, f.id)
		}
		if len(ids) > 0 {
			rows, err := s.pool.Query(ctx, `DELETE FROM asset WHERE id = ANY($1) AND user_id = $2 RETURNING id, thumb_key`, ids, userID)
			if err != nil {
				return n, err
			}
			type gone struct {
				id    int64
				thumb *string
			}
			deleted, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (gone, error) {
				var g gone
				return g, r.Scan(&g.id, &g.thumb)
			})
			if err != nil {
				return n, err
			}
			for _, g := range deleted {
				s.dropLateThumb(ctx, thumbs[g.id], g.thumb)
			}
			n += len(ids)
		}
		if first != nil {
			return n, first
		}
	}
}

// Results are the files job id made, in order.
func (s *Store) Results(ctx context.Context, jobID int64) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, object_key, content_type, bytes, COALESCE(width, 0), COALESCE(height, 0),
		  COALESCE(seconds, 0), COALESCE(fps, 0), COALESCE(audio, false), sha256, created_at
		FROM asset WHERE job_id=$1 AND kind='result' ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		a := Asset{Kind: "result", JobID: jobID}
		if err := rows.Scan(&a.ID, &a.UserID, &a.Key, &a.ContentType, &a.Bytes, &a.Width, &a.Height,
			&a.Seconds, &a.FPS, &a.Audio, &a.SHA256, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// JobInputs are the files job id takes, in order, as kept: the chat draws
// the preview card from them again when Model or Settings changes the run.
// A file deleted since is left out.
func (s *Store) JobInputs(ctx context.Context, jobID int64) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.user_id, a.kind, a.object_key, a.content_type, a.bytes, COALESCE(a.width, 0), COALESCE(a.height, 0),
		  COALESCE(a.seconds, 0), COALESCE(a.fps, 0), COALESCE(a.audio, false), a.sha256, a.created_at
		FROM job_input i JOIN asset a ON a.id = i.asset_id
		WHERE i.job_id = $1
		ORDER BY i.position`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.UserID, &a.Kind, &a.Key, &a.ContentType, &a.Bytes, &a.Width, &a.Height,
			&a.Seconds, &a.FPS, &a.Audio, &a.SHA256, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Open reads a file the bot is about to send to its owner's chat. The
// caller closes it.
func (s *Store) Open(ctx context.Context, a Asset) (io.ReadCloser, error) {
	return s.bucket.Get(ctx, a.Key)
}

// Link is a presigned link to a file of the person's, for the person only,
// living minutes. With a file name the file downloads under it.
func (s *Store) Link(ctx context.Context, userID, assetID int64, filename string) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx, `SELECT object_key FROM asset WHERE id=$1 AND user_id=$2`, assetID, userID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.view(key, disposition(filename))
}

// view is a presigned link to key for the owner. It is signed at the start
// of the window it is made in, so every view in the window gets the same
// link, and it tells the browser to keep the file for as long as the link
// lives: the library opened again comes from the browser's cache. The
// cache is the person's own browser ("private").
func (s *Store) view(key, disposition string) (string, error) {
	ttl := s.cfg.LinkWindow + s.cfg.LinkTTL
	at := s.cfg.Now().Truncate(s.cfg.LinkWindow)
	return s.bucket.PresignGetAt(key, at, ttl, disposition, fmt.Sprintf("private, max-age=%d", int64(ttl/time.Second)))
}

// FileLinks are the presigned links to one file: URL opens it, and
// Download, when the file was given a name, saves it under that name.
// Thumb is a result's thumbnail, nil until it has one.
type FileLinks struct {
	URL, Download string
	Thumb         *ThumbLink
}

// ThumbLink is the link to a thumbnail, with its size.
type ThumbLink struct {
	URL           string
	Width, Height int
}

// LinkAll is the presigned links to each of ids that is a file of the
// person's, by id, as Link makes them: one read for a page of the history
// instead of one per file. A file named in names also gets a Download link,
// which saves it as that name; the browser takes a download name only from
// the response, since a link to another origin cannot ask for one. An id
// that is not theirs has no links.
func (s *Store) LinkAll(ctx context.Context, userID int64, ids []int64, names map[int64]string) (map[int64]FileLinks, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, object_key, thumb_key, COALESCE(thumb_width, 0), COALESCE(thumb_height, 0)
		FROM asset WHERE id = ANY($1) AND user_id = $2`, ids, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]FileLinks, len(ids))
	for rows.Next() {
		var id int64
		var key string
		var thumb *string
		var tw, th int
		if err := rows.Scan(&id, &key, &thumb, &tw, &th); err != nil {
			return nil, err
		}
		var l FileLinks
		if l.URL, err = s.view(key, ""); err != nil {
			return nil, err
		}
		if name := names[id]; name != "" {
			if l.Download, err = s.view(key, disposition(name)); err != nil {
				return nil, err
			}
		}
		if thumb != nil {
			t := ThumbLink{Width: tw, Height: th}
			if t.URL, err = s.view(*thumb, ""); err != nil {
				return nil, err
			}
			l.Thumb = &t
		}
		out[id] = l
	}
	return out, rows.Err()
}

// Usage is what a person keeps.
type Usage struct {
	Files int
	Bytes int64
}

// Usage sums the person's files, for the settings.
func (s *Store) Usage(ctx context.Context, userID int64) (Usage, error) {
	var u Usage
	err := s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(bytes + COALESCE(thumb_bytes, 0)), 0)::bigint FROM asset WHERE user_id=$1`, userID).Scan(&u.Files, &u.Bytes)
	return u, err
}

// checkBucket tells the operator chat when the bucket passes another step.
// Two writes crossing it at once tell it once: the step is swapped, not
// set. It never fails the write that called it.
func (s *Store) checkBucket(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	var total int64
	var step int
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT sum(bytes + COALESCE(thumb_bytes, 0)) FROM asset), 0)::bigint, bucket_alert_step FROM runtime_state`).Scan(&total, &step); err != nil {
		s.log.Error("bucket usage", "error", err)
		return
	}
	now := int(total / s.cfg.AlertStep)
	if now == step {
		return
	}
	tag, err := s.pool.Exec(ctx, `UPDATE runtime_state SET bucket_alert_step=$1, updated_at=now() WHERE bucket_alert_step=$2`, now, step)
	if err != nil || tag.RowsAffected() != 1 || now < step {
		// Lower after deletes, so the next crossing is told again; or
		// another write got there first.
		return
	}
	text := fmt.Sprintf("framy: the R2 bucket holds %.1f GB, past %d GB. Storage beyond the free 10 GB is billed at $0.015 per GB a month.",
		float64(total)/1e9, int64(now)*s.cfg.AlertStep/1e9)
	s.log.Warn("bucket usage", "bytes", total, "step", now)
	if s.notify == nil {
		return
	}
	if err := s.notify(ctx, text); err != nil {
		s.log.Error("tell the operator chat", "error", err)
		// The next write tries again.
		_, _ = s.pool.Exec(ctx, `UPDATE runtime_state SET bucket_alert_step=$1 WHERE bucket_alert_step=$2`, step, now)
	}
}

// resultType keeps a provider's type unless it is missing or generic, and
// reads the bytes then.
func resultType(declared string, head []byte) string {
	if declared != "" && declared != "application/octet-stream" && declared != "binary/octet-stream" {
		return declared
	}
	if f := media.Sniff(head); f != "" {
		return "image/" + string(f)
	}
	// An ISO BMFF that is not a still image is a video; Go's own sniffing
	// knows only the mp4 brands.
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		if string(head[8:12]) == "qt  " {
			return "video/quicktime"
		}
		return "video/mp4"
	}
	return http.DetectContentType(head)
}

func resultKey(prefix string, j jobs.Job, i int, contentType string) string {
	return prefix + fmt.Sprintf("u/%d/out/%d-%d%s", j.UserID, j.ID, i, ext(contentType))
}

var exts = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/avif": ".avif", "image/heif": ".heic",
	"image/gif": ".gif", "video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
}

// Ext is the file extension of a content type framy keeps, with its dot:
// ".bin" for any other.
func Ext(contentType string) string { return ext(contentType) }

func ext(contentType string) string {
	if e, ok := exts[contentType]; ok {
		return e
	}
	return ".bin"
}

func random() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// disposition makes a download header for a file name: an ASCII fallback
// and the name itself in RFC 5987 form.
func disposition(filename string) string {
	if filename == "" {
		return ""
	}
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7E || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, filename)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(filename)
}

func host(link string) string {
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		return u.Host
	}
	return "the provider"
}

// unwrapURL drops the URL a *url.Error carries.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// headBuffer keeps the first bytes written to it.
type headBuffer struct {
	b   []byte
	max int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// copyFile copies src to dst, which must not exist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
