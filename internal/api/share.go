// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/FreshLabDev/framy/internal/chat"
	"github.com/FreshLabDev/framy/internal/db"
)

// shareJSON is a prepared message: the id the Mini App passes to
// Telegram.WebApp.shareMessage, and when it stops working.
type shareJSON struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// share is POST /api/files/{id}/share: Send to Telegram of a result in the
// Mini App (docs/design.md, Mini App).
func (a *API) share(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	if a.cfg.Sharing == nil {
		e := newError(http.StatusServiceUnavailable, codeTelegramDown, "framy is not connected to Telegram")
		e.retryAfter = time.Minute
		e.write(w)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		newError(http.StatusNotFound, codeNotFound, "no such result of yours").write(w)
		return
	}
	s, err := a.cfg.Sharing.Share(r.Context(), user, id)
	switch {
	case errors.Is(err, db.ErrNoResult):
		newError(http.StatusNotFound, codeNotFound, "no such result of yours").write(w)
		return
	case errors.Is(err, chat.ErrNotShareable):
		newError(http.StatusUnprocessableEntity, codeNotShareable, "Telegram cannot be given this result; download it instead").write(w)
		return
	case err != nil:
		if r.Context().Err() == nil {
			a.log.Warn("result not prepared for Telegram", "file", id, "error", err)
			newError(http.StatusBadGateway, codeTelegramDown, "Telegram did not take the result; try again").write(w)
		}
		return
	}
	writeJSON(w, http.StatusOK, shareJSON{ID: s.ID, ExpiresAt: s.Expires.UTC()})
}
