// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/chat"
	"github.com/FreshLabDev/framy/internal/db"
)

// fakeSharing prepares alice's result 70; 71 is a video too big for
// Telegram, and 72 one Telegram refuses.
type fakeSharing struct{ asked []int64 }

func (f *fakeSharing) Share(_ context.Context, userID, assetID int64) (chat.Shared, error) {
	f.asked = append(f.asked, assetID)
	switch {
	case userID != alice:
		return chat.Shared{}, db.ErrNoResult
	case assetID == 70:
		return chat.Shared{ID: "prep-70", Expires: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}, nil
	case assetID == 71:
		return chat.Shared{}, fmt.Errorf("%w: a video of 21 MB", chat.ErrNotShareable)
	case assetID == 72:
		return chat.Shared{}, errors.New("telegram savePreparedInlineMessage: Bad Request")
	}
	return chat.Shared{}, db.ErrNoResult
}

func shareRig(t *testing.T, s Sharing) *rig {
	return newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Sharing = s } })
}

func TestAResultIsPreparedForShareMessage(t *testing.T) {
	r := shareRig(t, &fakeSharing{})
	resp := r.do(alice, http.MethodPost, "/api/files/70/share", nil)
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusOK || m["id"] != "prep-70" || m["expires_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
}

func TestWhatCannotBeSharedSaysWhy(t *testing.T) {
	sharing := &fakeSharing{}
	r := shareRig(t, sharing)
	for _, c := range []struct {
		user   int64
		path   string
		status int
		code   string
	}{
		{bob, "/api/files/70/share", http.StatusNotFound, "not_found"},
		{alice, "/api/files/99/share", http.StatusNotFound, "not_found"},
		{alice, "/api/files/x/share", http.StatusNotFound, "not_found"},
		{alice, "/api/files/71/share", http.StatusUnprocessableEntity, "not_shareable"},
		{alice, "/api/files/72/share", http.StatusBadGateway, "telegram_unavailable"},
		{0, "/api/files/70/share", http.StatusUnauthorized, "sign_in"},
	} {
		resp := r.do(c.user, http.MethodPost, c.path, nil)
		if m := jsonOf(t, resp); resp.StatusCode != c.status || m["error"] != c.code {
			t.Errorf("%d %s: %d %v", c.user, c.path, resp.StatusCode, m)
		}
	}
	if len(sharing.asked) != 4 {
		t.Errorf("asked %v: a bad id or no person should not ask", sharing.asked)
	}
}

func TestWithoutTheBotNothingIsShared(t *testing.T) {
	r := shareRig(t, nil)
	resp := r.do(alice, http.MethodPost, "/api/files/70/share", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusServiceUnavailable || m["error"] != "telegram_unavailable" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
}
