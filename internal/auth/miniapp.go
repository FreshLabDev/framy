// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// MiniApp exchanges a Mini App's initData for a bearer token. The body is
// {"init_data": "<the raw initData string>"}; the answer is
//
//	{"token": "...", "token_type": "Bearer", "expires_in": 3600, "user_id": 123}
//
// and the Mini App then sends `Authorization: Bearer <token>`. The hash is
// checked first, then the age of auth_date (MaxInitDataAge). A forged or
// edited initData is 401 `init_data_invalid`, an old one 401
// `init_data_expired`: the Mini App has to be opened again, since Telegram
// signs initData only when it opens. The token ends when the initData stops
// being accepted, an hour after auth_date, so expires_in is less than an
// hour for initData exchanged late, and initData with less than a minute
// left is too old already.
//
// The person is recorded in core before the token is made, as a bot update
// would. The token is stored as a hash, like the site's cookie. One launch
// gets one token: the same initData sent again while its token lasts is
// answered with that token, with nothing written, so trading it again and
// again neither adds rows nor writes to core.
func (a *Auth) MiniApp() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			InitData string `json:"init_data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InitData == "" {
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
		data, err := a.initData.Verify(req.InitData)
		now := a.now()
		// A token lasts no longer than the initData it came from: one
		// initData that got out is worth an hour after Telegram signed it,
		// not an hour more after its last minute.
		expires := minTime(now.Add(MiniAppTokenLife), data.AuthDate.Add(MaxInitDataAge))
		if err == nil && expires.Sub(now) < minTokenLife {
			err = ErrStale
		}
		switch {
		case errors.Is(err, ErrStale):
			metrics.AuthResults.Inc("miniapp_stale")
			writeError(w, http.StatusUnauthorized, "init_data_expired")
			return
		case err != nil:
			metrics.AuthResults.Inc("miniapp_bad")
			writeError(w, http.StatusUnauthorized, "init_data_invalid")
			return
		}
		lock := &a.mintLocks[data.Hash[0]%byte(len(a.mintLocks))]
		lock.Lock()
		defer lock.Unlock()
		if t, ok := a.mintedFor(r.Context(), data.Hash, data.User.ID); ok {
			metrics.AuthResults.Inc("miniapp_again")
			answerToken(w, t.secret, t.expires.Sub(now), data.User.ID)
			return
		}
		if err := a.people.Touch(r.Context(), data.User, nil); err != nil {
			a.log.Error("record the person in core", "error", err)
			metrics.AuthResults.Inc("miniapp_error")
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		name := strings.TrimSpace(data.User.FirstName + " " + data.User.LastName)
		if name == "" {
			name = data.User.Username
		}
		a.setCard(r.Context(), data.User.ID, name, data.PhotoURL)
		token, hash, err := a.newSession(r.Context(), data.User.ID, KindMiniApp, expires)
		if err != nil {
			a.log.Error("start a Mini App session", "error", err)
			metrics.AuthResults.Inc("miniapp_error")
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		a.mint(data.Hash, mintedToken{secret: token, hash: hash, user: data.User.ID, expires: expires})
		metrics.AuthResults.Inc("miniapp_ok")
		answerToken(w, token, expires.Sub(now), data.User.ID)
	})
}

func answerToken(w http.ResponseWriter, token string, left time.Duration, userID int64) {
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"expires_in": int(left.Seconds()),
		"user_id":    userID,
	})
}
