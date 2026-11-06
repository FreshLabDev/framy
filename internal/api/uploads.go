// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// uploadJSON is the answer of POST /api/uploads: the file as framy keeps it
// after the pipeline. The id goes into "inputs" of a job.
type uploadJSON struct {
	ID          int64  `json:"id"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// upload answers POST /api/uploads: one photo per request, as the raw body.
// The kind of picture is read from its bytes, never from Content-Type, so
// the header can say anything. The pipeline (internal/media) refuses what it
// does not take, drops the metadata and brings the picture to 2048 px on its
// long side; what comes back is that, not what was sent.
func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	if a.cfg.Uploads == nil {
		e := newError(http.StatusServiceUnavailable, codeStorageDown, "framy keeps no files right now")
		e.retryAfter = time.Minute
		e.write(w)
		return
	}
	// A body that says it is over the cap is turned away before it is read.
	if r.ContentLength > a.maxBody {
		tooLarge(w)
		return
	}
	done, ok := a.uploading.start(user)
	if !ok {
		a.full(fullPhotoPerson)
		busy(w, "photos of yours are on their way in already; send this one when one of them is in")
		return
	}
	defer done()
	// The body comes in whole before a conversion slot is taken, so a
	// phone on a slow line keeps no one else's photo waiting.
	asset, err := a.cfg.Uploads.Upload(r.Context(), user, r.Body, a.convertSlot)
	if err != nil {
		a.uploadFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, uploadJSON{
		ID: asset.ID, ContentType: asset.ContentType, Bytes: asset.Bytes, Width: asset.Width, Height: asset.Height,
	})
}

// errConverting: every conversion slot stayed taken while a photo waited.
var errConverting = errors.New("every conversion slot is taken")

// convertSlot waits up to UploadWait for one of the slots photos are
// converted in: storage.Gate.
func (a *API) convertSlot(ctx context.Context) (func(), error) {
	wait := time.NewTimer(a.cfg.UploadWait)
	defer wait.Stop()
	select {
	case a.slots <- struct{}{}:
		return func() { <-a.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-wait.C:
		return nil, errConverting
	}
}

// personUploads counts each person's photos on their way in, read or
// converted, so one account cannot fill the working directory with bodies
// sent slowly.
type personUploads struct {
	mu  sync.Mutex
	now map[int64]int
}

// start counts one more upload of user's, unless maxPersonUploads are in
// already, and returns what ends it.
func (p *personUploads) start(user int64) (func(), bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now[user] >= maxPersonUploads {
		return nil, false
	}
	if p.now == nil {
		p.now = map[int64]int{}
	}
	p.now[user]++
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.now[user]--; p.now[user] <= 0 {
			delete(p.now, user)
		}
	}, true
}

func (a *API) uploadFailed(w http.ResponseWriter, r *http.Request, err error) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, errConverting):
		a.full(fullPhotoSlots)
		busy(w, "photos are being converted; try again in a moment")
	case errors.As(err, &tooBig), errors.Is(err, storage.ErrTooBig):
		tooLarge(w)
	case errors.Is(err, media.ErrTooLarge):
		newError(http.StatusRequestEntityTooLarge, codePictureTooLarge, "the picture has too many pixels; the limit is 64 megapixels").write(w)
	case errors.Is(err, media.ErrUnsupported):
		newError(http.StatusUnsupportedMediaType, codeUnsupportedType, "send a JPEG, PNG, WebP, HEIC or AVIF photo").write(w)
	case errors.Is(err, media.ErrUnreadable):
		newError(http.StatusUnprocessableEntity, codeUnreadable, "the picture cannot be read").write(w)
	case r.Context().Err() != nil:
		// The client left; nobody reads the answer.
		return
	default:
		a.internal(w, r, err)
	}
}

func tooLarge(w http.ResponseWriter) {
	newError(http.StatusRequestEntityTooLarge, codeTooLarge, "the photo is too big").write(w)
}
