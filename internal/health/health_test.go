// SPDX-License-Identifier: Apache-2.0
package health

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

type fakeStore struct{ err error }

func (f fakeStore) Ping(context.Context) error { return f.err }

func handler(store Store, lastPoll time.Time, initialized bool, startedAt time.Time) *Handler {
	return New(Config{
		Store:       store,
		LastPoll:    func() time.Time { return lastPoll },
		Initialized: func() bool { return initialized },
		StartedAt:   startedAt,
		Build:       Build{Version: "v1", Commit: "abc", Date: "today"},
	})
}

// serve asks the public /healthz for its status and the metrics listener's
// for the parts.
func serve(t *testing.T, store Store, lastPoll time.Time, initialized bool, startedAt time.Time) (int, report) {
	t.Helper()
	h := handler(store, lastPoll, initialized, startedAt)
	public := httptest.NewRecorder()
	h.Public(public, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	detail := httptest.NewRecorder()
	h.Detail(detail, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	// Read by hand with busybox wget, which shows no body of an error.
	if detail.Code != http.StatusOK {
		t.Errorf("the detail answered %d", detail.Code)
	}
	var got report
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return public.Code, got
}

func TestHealthy(t *testing.T) {
	now := time.Now()
	code, got := serve(t, fakeStore{}, now, true, now.Add(-time.Hour))
	if code != http.StatusOK || !got.OK || !got.DB || got.Version != "v1" || got.Commit != "abc" {
		t.Fatalf("code = %d, response = %+v", code, got)
	}
}

func TestUnhealthy(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name        string
		store       Store
		lastPoll    time.Time
		initialized bool
		startedAt   time.Time
	}{
		{"database down", fakeStore{err: errors.New("down")}, now, true, now},
		{"not initialized", fakeStore{}, now, false, now},
		{"polling stale", fakeStore{}, now.Add(-2 * pollMaxAge), true, now.Add(-time.Hour)},
		{"never polled after grace", fakeStore{}, time.Time{}, true, now.Add(-2 * pollMaxAge)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, got := serve(t, tc.store, tc.lastPoll, tc.initialized, tc.startedAt)
			if code != http.StatusServiceUnavailable || got.OK {
				t.Fatalf("code = %d, response = %+v", code, got)
			}
		})
	}
}

// The public /healthz says whether framy works and nothing else: not the
// commit, not the build, not which part failed.
func TestPublicSaysOnlyOK(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		store Store
		code  int
		ok    bool
	}{
		{"healthy", fakeStore{}, http.StatusOK, true},
		{"database down", fakeStore{err: errors.New("down")}, http.StatusServiceUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler(tc.store, now, true, now.Add(-time.Hour)).Public(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tc.code || got["ok"] != tc.ok || !slices.Equal(slices.Collect(maps.Keys(got)), []string{"ok"}) {
				t.Fatalf("code = %d, body = %s", rr.Code, rr.Body)
			}
		})
	}
}

// The local Bot API answers getUpdates when it has lost Telegram, as on 3
// November: calls that keep failing, with none answered, make framy
// unhealthy while polling looks fresh.
func TestTelegramCallsFailing(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		calls   func(c *Calls)
		failing bool
	}{
		{"no calls", func(c *Calls) {}, false},
		{"answered", func(c *Calls) { c.Answered(now.Add(-time.Minute)) }, false},
		{"failing for three minutes", func(c *Calls) {
			c.Answered(now.Add(-10 * time.Minute))
			c.Failed(now.Add(-3 * time.Minute))
			c.Failed(now.Add(-30 * time.Second))
		}, true},
		// One call's retries, or a restart of the Bot API.
		{"failing for a minute", func(c *Calls) {
			c.Failed(now.Add(-time.Minute))
			c.Failed(now.Add(-time.Second))
		}, false},
		{"answered since", func(c *Calls) {
			c.Failed(now.Add(-4 * time.Minute))
			c.Failed(now.Add(-time.Minute))
			c.Answered(now.Add(-30 * time.Second))
		}, false},
		{"failures that ended long ago", func(c *Calls) {
			c.Failed(now.Add(-time.Hour))
			c.Failed(now.Add(-50 * time.Minute))
		}, false},
		{"failing again after an answer", func(c *Calls) {
			c.Failed(now.Add(-20 * time.Minute))
			c.Answered(now.Add(-10 * time.Minute))
			c.Failed(now.Add(-3 * time.Minute))
			c.Failed(now.Add(-time.Minute))
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := new(Calls)
			tc.calls(calls)
			h := New(Config{
				Store:       fakeStore{},
				LastPoll:    func() time.Time { return now },
				LastUpdate:  func() time.Time { return now.Add(-time.Hour) },
				Initialized: func() bool { return true },
				Telegram:    calls,
				StartedAt:   now.Add(-time.Hour),
			})
			rr := httptest.NewRecorder()
			h.Public(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			got := h.check(context.Background())
			if got.TelegramCallsFailing != tc.failing || got.OK == tc.failing || (rr.Code == http.StatusOK) == tc.failing {
				t.Fatalf("failing %v, ok %v, public %d; want failing %v", got.TelegramCallsFailing, got.OK, rr.Code, tc.failing)
			}
			if got.TelegramLastUpdateAt == nil || !got.TelegramPollingFresh {
				t.Errorf("polling looked at: %+v", got)
			}
		})
	}
}

func TestStartingProcessIsFresh(t *testing.T) {
	now := time.Now()
	_, got := serve(t, fakeStore{}, time.Time{}, true, now)
	if !got.TelegramPollingFresh {
		t.Fatal("a process that just started is reported stale")
	}
}
