// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"net/http"
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
	wait := time.NewTimer(a.cfg.UploadWait)
	select {
	case a.slots <- struct{}{}:
		wait.Stop()
		defer func() { <-a.slots }()
	case <-r.Context().Done():
		wait.Stop()
		return
	case <-wait.C:
		e := newError(http.StatusServiceUnavailable, codeBusy, "photos are being converted; try again in a moment")
		e.retryAfter = 3 * time.Second
		e.write(w)
		return
	}
	asset, err := a.cfg.Uploads.Upload(r.Context(), user, r.Body)
	if err != nil {
		a.uploadFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, uploadJSON{
		ID: asset.ID, ContentType: asset.ContentType, Bytes: asset.Bytes, Width: asset.Width, Height: asset.Height,
	})
}

func (a *API) uploadFailed(w http.ResponseWriter, r *http.Request, err error) {
	var tooBig *http.MaxBytesError
	switch {
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
