// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"errors"
	"net/http"

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
// signs initData only when it opens.
//
// The person is recorded in core before the token is made, as a bot update
// would. The token is stored as a hash, like the site's cookie.
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
		if err := a.people.Touch(r.Context(), data.User, nil); err != nil {
			a.log.Error("record the person in core", "error", err)
			metrics.AuthResults.Inc("miniapp_error")
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		token, _, err := a.newSession(r.Context(), data.User.ID, KindMiniApp, MiniAppTokenLife)
		if err != nil {
			a.log.Error("start a Mini App session", "error", err)
			metrics.AuthResults.Inc("miniapp_error")
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		metrics.AuthResults.Inc("miniapp_ok")
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      token,
			"token_type": "Bearer",
			"expires_in": int(MiniAppTokenLife.Seconds()),
			"user_id":    data.User.ID,
		})
	})
}
