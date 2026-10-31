// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// Clips (docs/design.md, Storage and retention). The browser puts a clip
// straight into the bucket by a presigned PUT, since Cloudflare takes at
// most 100 MB in a request to the service, and then asks framy to take it.
// Taking a clip reads it from the bucket and may encode it again, which can
// outlast the 100 s Cloudflare waits for an answer, so it is a task the
// client follows: POST /api/videos starts it, GET /api/videos/{task} says
// how it went.
const (
	// DefaultVideoSlots is how many clips are worked on at once: ffmpeg
	// takes every core it gets for one.
	DefaultVideoSlots = 2
	// maxVideoTasks is how many clips may wait or be worked on at once,
	// everyone's, and maxPersonVideoTasks one person's.
	maxVideoTasks       = 16
	maxPersonVideoTasks = 3
	// videoTaskTTL is how long a task's answer is kept once it ended, and
	// videoTaskTimeout how long a task may take, the wait included.
	videoTaskTTL     = 15 * time.Minute
	videoTaskTimeout = 10 * time.Minute
)

// Videos keeps clips: *storage.Store.
type Videos interface {
	VideoUpload(userID int64) (storage.VideoUpload, error)
	TakeVideo(ctx context.Context, userID int64, token string, cut *media.Cut) (storage.Asset, error)
	CutVideo(ctx context.Context, userID, assetID int64, cut media.Cut) (storage.Asset, error)
	LastFrame(ctx context.Context, userID, assetID int64) (storage.Asset, error)
}

// videoUploadJSON is the answer of POST /api/videos/uploads.
type videoUploadJSON struct {
	Token string `json:"token"`
	// URL takes one PUT of the clip, until ExpiresAt.
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expires_at"`
	MaxBytes   int64     `json:"max_bytes"`
	MaxSeconds float64   `json:"max_seconds"`
}

// clipJSON is a clip as framy keeps it, or the frame taken of one: the
// upload's fields, and a clip's length, frame rate and sound.
type clipJSON struct {
	uploadJSON
	Seconds float64 `json:"seconds,omitempty"`
	FPS     float64 `json:"fps,omitempty"`
	Audio   *bool   `json:"audio,omitempty"`
}

func clipJSONOf(a storage.Asset) clipJSON {
	out := clipJSON{uploadJSON: uploadJSON{ID: a.ID, ContentType: a.ContentType, Bytes: a.Bytes, Width: a.Width, Height: a.Height}}
	if a.Seconds > 0 {
		audio := a.Audio
		out.Seconds, out.FPS, out.Audio = a.Seconds, a.FPS, &audio
	}
	return out
}

// videoRequest is the body of POST /api/videos: the clip, put under an
// upload's token or kept already, and the part of it to keep.
type videoRequest struct {
	Upload  string   `json:"upload"`
	File    int64    `json:"file"`
	Start   *float64 `json:"start"`
	Seconds *float64 `json:"seconds"`
}

// taskJSON is a task as GET /api/videos/{task} says it.
type taskJSON struct {
	Task   string    `json:"task"`
	Status string    `json:"status"`
	File   *clipJSON `json:"file,omitempty"`
}

const (
	taskProcessing = "processing"
	taskDone       = "done"
	taskFailed     = "failed"
)

// videoUpload answers POST /api/videos/uploads: where to put one clip.
func (a *API) videoUpload(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	if a.cfg.Videos == nil {
		storageDown(w)
		return
	}
	up, err := a.cfg.Videos.VideoUpload(user)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, videoUploadJSON{
		Token: up.Token, URL: up.URL, ExpiresAt: up.ExpiresAt.UTC(), MaxBytes: up.MaxBytes, MaxSeconds: up.MaxSeconds,
	})
}

// videoStart answers POST /api/videos: 202 with the task that takes the
// clip, or cuts a part of one kept already. The same request again while
// the task runs, or after it went well, is answered with the same task.
func (a *API) videoStart(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	if a.cfg.Videos == nil {
		storageDown(w)
		return
	}
	req, cut, aerr := decodeVideo(r)
	if aerr != nil {
		aerr.write(w)
		return
	}
	var key string
	var work func(ctx context.Context) (storage.Asset, error)
	if req.Upload != "" {
		key = "upload:" + req.Upload
		work = func(ctx context.Context) (storage.Asset, error) {
			return a.cfg.Videos.TakeVideo(ctx, user, req.Upload, cut)
		}
	} else {
		key = "file:" + strconv.FormatInt(req.File, 10)
		work = func(ctx context.Context) (storage.Asset, error) {
			return a.cfg.Videos.CutVideo(ctx, user, req.File, *cut)
		}
	}
	if cut != nil {
		key += fmt.Sprintf(":%g:%g", cut.Start, cut.Seconds)
	}
	t, aerr := a.videos.start(a.bg, user, key, work)
	if aerr != nil {
		aerr.write(w)
		return
	}
	w.Header().Set("Location", "/api/videos/"+t)
	writeJSON(w, http.StatusAccepted, taskJSON{Task: t, Status: taskProcessing})
}

// videoTask answers GET /api/videos/{task}: 200 while it runs and once it
// went well, with the file then; the error the clip got, with its status,
// once it failed.
func (a *API) videoTask(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	id := r.PathValue("task")
	t, found := a.videos.get(user, id)
	switch {
	case !found:
		newError(http.StatusNotFound, codeNotFound, "no such task of yours; it may have ended over 15 minutes ago").write(w)
	case !t.ended:
		writeJSON(w, http.StatusOK, taskJSON{Task: id, Status: taskProcessing})
	case t.err != nil:
		e := a.videoFailed(r, t.err)
		e.with("task", id).with("status", taskFailed).write(w)
	default:
		file := clipJSONOf(t.file)
		writeJSON(w, http.StatusOK, taskJSON{Task: id, Status: taskDone, File: &file})
	}
}

// lastFrame answers POST /api/files/{id}/last-frame: 201 with the last frame
// of a clip of the person's, kept as a photo, for a run that continues the
// clip from it. It reads the last second only, so it is answered at once.
func (a *API) lastFrame(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	if a.cfg.Videos == nil {
		storageDown(w)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		newError(http.StatusNotFound, codeNotFound, "no such file of yours").write(w)
		return
	}
	wait := time.NewTimer(a.cfg.UploadWait)
	select {
	case a.videos.slots <- struct{}{}:
		wait.Stop()
		defer func() { <-a.videos.slots }()
	case <-r.Context().Done():
		wait.Stop()
		return
	case <-wait.C:
		busy(w, "clips are being worked on; try again in a moment")
		return
	}
	asset, err := a.cfg.Videos.LastFrame(r.Context(), user, id)
	if err != nil {
		if r.Context().Err() == nil {
			a.videoFailed(r, err).write(w)
		}
		return
	}
	writeJSON(w, http.StatusCreated, clipJSONOf(asset))
}

// videoFailed is the answer to what taking or cutting a clip ran into.
func (a *API) videoFailed(r *http.Request, err error) *apiError {
	var long *media.TooLongError
	switch {
	case errors.As(err, &long):
		return newError(http.StatusUnprocessableEntity, codeVideoTooLong, long.Error()).
			with("seconds", math.Round(long.Seconds*100)/100).with("max_seconds", long.Max)
	case errors.Is(err, storage.ErrNoUpload):
		return newError(http.StatusNotFound, codeNoUpload, "nothing was put under that token: PUT the clip to the upload's link first")
	case errors.Is(err, storage.ErrNotFound):
		return newError(http.StatusNotFound, codeNotFound, "no such file of yours")
	case errors.Is(err, storage.ErrNotVideo):
		return newError(http.StatusUnprocessableEntity, codeNotAVideo, "the file is a picture, not a clip")
	case errors.Is(err, storage.ErrTooBig):
		return newError(http.StatusRequestEntityTooLarge, codeTooLarge, "the clip is too big")
	case errors.Is(err, media.ErrUnsupportedVideo):
		return newError(http.StatusUnsupportedMediaType, codeUnsupportedType, "send an MP4, MOV, WebM or MKV clip, or an MP3, WAV, M4A, Ogg, FLAC or AAC sound")
	case errors.Is(err, media.ErrUnreadableVideo):
		return newError(http.StatusUnprocessableEntity, codeUnreadable, "the clip cannot be read")
	case errors.Is(err, media.ErrVideoTooLarge):
		return newError(http.StatusRequestEntityTooLarge, codeFrameTooLarge, "the clip's frames have too many pixels; the limit is 64 megapixels")
	case errors.Is(err, media.ErrBadCut):
		return newError(http.StatusUnprocessableEntity, codeBadCut, "that part is not in the clip")
	case errors.Is(err, context.Canceled):
		e := newError(http.StatusServiceUnavailable, codeShuttingDown, "framy restarted while the clip was worked on; start it again")
		e.retryAfter = 10 * time.Second
		return e
	case errors.Is(err, context.DeadlineExceeded):
		e := newError(http.StatusServiceUnavailable, codeBusy, "the clip took too long; try again, or pick a shorter part")
		e.retryAfter = 10 * time.Second
		return e
	}
	return a.internalError(r, err)
}

// decodeVideo reads and checks the body of POST /api/videos.
func decodeVideo(r *http.Request) (videoRequest, *media.Cut, *apiError) {
	var req videoRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return req, nil, newError(http.StatusRequestEntityTooLarge, codeTooLarge, "the request is too big")
		}
		return req, nil, badRequest("", "the body must be a JSON object: "+jsonProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return req, nil, badRequest("", "the body must be one JSON object")
	}
	var cut *media.Cut
	if req.Start != nil || req.Seconds != nil {
		if req.Start == nil || req.Seconds == nil {
			return req, nil, badRequest("seconds", "a part is start and seconds, both")
		}
		cut = &media.Cut{Start: *req.Start, Seconds: *req.Seconds}
		if !cut.Valid() {
			return req, nil, badRequest("seconds", fmt.Sprintf("start is 0 or more, and seconds at least %g", media.MinSeconds))
		}
	}
	switch {
	case (req.Upload == "") == (req.File == 0):
		return req, nil, badRequest("upload", "send the token of an upload, or the id of a file of yours")
	case req.Upload != "" && !storage.ValidToken(req.Upload):
		return req, nil, newError(http.StatusNotFound, codeNoUpload, "no upload has that token")
	case req.File < 0:
		return req, nil, badRequest("file", "file is the id of a file of yours")
	case req.File != 0 && cut == nil:
		return req, nil, badRequest("seconds", "a file kept already is cut: send start and seconds")
	}
	return req, cut, nil
}

func storageDown(w http.ResponseWriter) {
	e := newError(http.StatusServiceUnavailable, codeStorageDown, "framy keeps no files right now")
	e.retryAfter = time.Minute
	e.write(w)
}

func busy(w http.ResponseWriter, msg string) {
	e := newError(http.StatusServiceUnavailable, codeBusy, msg)
	e.retryAfter = 3 * time.Second
	e.write(w)
}

// videoTasks are the clips being taken or cut, and the answers of those
// that ended, in memory: a restart loses them, and the client starts the
// task again from the clip, which stays in the bucket until it is taken.
type videoTasks struct {
	mu    sync.Mutex
	tasks map[string]*videoTask
	slots chan struct{}
	now   func() time.Time
}

type videoTask struct {
	user  int64
	key   string
	ended bool
	at    time.Time
	file  storage.Asset
	err   error
}

func newVideoTasks(slots int, now func() time.Time) *videoTasks {
	return &videoTasks{tasks: map[string]*videoTask{}, slots: make(chan struct{}, slots), now: now}
}

// start runs work in its own goroutine, one of the slots at a time, and
// returns the task's id. A task of the person's with the same key that is
// running, or ended well, is that task.
func (v *videoTasks) start(ctx context.Context, user int64, key string, work func(context.Context) (storage.Asset, error)) (string, *apiError) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	running, theirs := 0, 0
	for id, t := range v.tasks {
		switch {
		case t.ended && now.Sub(t.at) > videoTaskTTL:
			delete(v.tasks, id)
			continue
		case t.user == user && t.key == key && (!t.ended || t.err == nil):
			return id, nil
		case t.ended:
			continue
		}
		running++
		if t.user == user {
			theirs++
		}
	}
	if running >= maxVideoTasks || theirs >= maxPersonVideoTasks {
		e := newError(http.StatusServiceUnavailable, codeBusy, "clips are being worked on; try again in a moment")
		e.retryAfter = 10 * time.Second
		return "", e
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	t := &videoTask{user: user, key: key, at: now}
	v.tasks[id] = t
	go func() {
		ctx, cancel := context.WithTimeout(ctx, videoTaskTimeout)
		defer cancel()
		var file storage.Asset
		var err error
		select {
		case v.slots <- struct{}{}:
			file, err = work(ctx)
			<-v.slots
		case <-ctx.Done():
			err = ctx.Err()
		}
		v.mu.Lock()
		t.ended, t.at, t.file, t.err = true, v.now(), file, err
		v.mu.Unlock()
	}()
	return id, nil
}

// get is a task of the person's, as it is now.
func (v *videoTasks) get(user int64, id string) (videoTask, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok := v.tasks[id]
	if !ok || t.user != user {
		return videoTask{}, false
	}
	return *t, true
}
