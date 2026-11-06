// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ticket asks for a ticket as the Mini App of user does and returns the
// start's address in the answer, as a path and query of the site.
func (h *harness) ticket(user int64) string {
	h.t.Helper()
	rr := h.browser(user).request(http.MethodPost, TicketPath)
	if rr.Code != http.StatusOK {
		h.t.Fatalf("the ticket answered %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		URL     string `json:"url"`
		Expires string `json:"expires_at"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		h.t.Fatal(err)
	}
	u, err := url.Parse(got.URL)
	if err != nil || u.Scheme+"://"+u.Host != siteOrigin || u.Path != StartPath || len(u.Query().Get("ticket")) != 43 {
		h.t.Fatalf("the ticket's address is %q", got.URL)
	}
	if want := h.clock.Now().Add(TicketLifetime).Format(time.RFC3339); got.Expires != want {
		h.t.Errorf("expires_at = %q, want %q", got.Expires, want)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		h.t.Errorf("Cache-Control = %q", rr.Header().Get("Cache-Control"))
	}
	return u.RequestURI()
}

// fromTelegram reads where a start or a callback of a ticketed flow sent the
// browser: the site's result, marked as one the Mini App started.
func (h *harness) fromTelegram(rr *http.Response) Result {
	h.t.Helper()
	loc, err := url.Parse(rr.Header.Get("Location"))
	q := loc.Query()
	if rr.StatusCode != http.StatusSeeOther || err != nil || loc.Path != "/" || q.Get("connect") != "openrouter" || q.Get("from") != "telegram" || len(q) != 3 {
		h.t.Fatalf("answered %d to %q, want /?connect=openrouter&result=…&from=telegram", rr.StatusCode, rr.Header.Get("Location"))
	}
	return Result(q.Get("result"))
}

func TestATicketConnectsInABrowserNobodyIsSignedInOn(t *testing.T) {
	h := newHarness(t)
	start := h.ticket(ann)
	tk := mustQuery(t, siteOrigin+start).Get("ticket")
	b := h.browser(0)
	rr := b.get(start)
	authURL := rr.Header().Get("Location")
	if rr.Code != http.StatusSeeOther || !strings.HasPrefix(authURL, h.or.URL+"/auth?") {
		t.Fatalf("the start answered %d to %q", rr.Code, authURL)
	}
	if f := h.store.peek(t, mustQuery(t, authURL).Get("state")); f.UserID != ann || !f.Ticketed {
		t.Fatalf("the flow is %+v, want ann's and ticketed", f)
	}
	if got := h.fromTelegram(b.get(b.approve(authURL)).Result()); got != Connected {
		t.Fatalf("result %q; log:\n%s", got, h.logs.String())
	}
	if got := h.keys.saved(); len(got) != 1 || got[0].user != ann {
		t.Errorf("saved %+v, want the key for ann", got)
	}
	if strings.Contains(h.logs.String(), tk) || strings.Contains(h.logs.String(), url.QueryEscape(tk)) {
		t.Error("the log holds the ticket")
	}
	// Spent by the start: the same link again starts nothing.
	if got := h.fromTelegram(h.browser(0).get(start).Result()); got != Expired {
		t.Errorf("the ticket again: %q, want expired", got)
	}
}

func TestATicketWorksWhereThePersonIsSignedIn(t *testing.T) {
	h := newHarness(t)
	b := h.browser(ann)
	rr := b.get(h.ticket(ann))
	if got := h.fromTelegram(b.get(b.approve(rr.Header().Get("Location"))).Result()); got != Connected {
		t.Fatalf("result %q", got)
	}
}

func TestATicketDoesNotStartWhereAnotherPersonIsSignedIn(t *testing.T) {
	h := newHarness(t)
	start := h.ticket(ann)
	if got := h.fromTelegram(h.browser(bob).get(start).Result()); got != Expired {
		t.Errorf("result %q, want expired", got)
	}
	if h.store.count() != 0 {
		t.Error("a flow was opened")
	}
	// The ticket is spent all the same: it cannot be tried again in another browser.
	if got := h.fromTelegram(h.browser(0).get(start).Result()); got != Expired {
		t.Errorf("the ticket after the refusal: %q, want expired", got)
	}
}

func TestATicketedCallbackWhereAnotherPersonSignedInIsRefused(t *testing.T) {
	h := newHarness(t)
	b := h.browser(0)
	authURL := b.get(h.ticket(ann)).Header().Get("Location")
	target := b.approve(authURL)
	b.user = bob
	if got := h.fromTelegram(b.get(target).Result()); got != Expired {
		t.Errorf("result %q, want expired", got)
	}
	if len(h.keys.saved()) != 0 {
		t.Error("a key was saved")
	}
}

func TestATicketExpiresAfterFiveMinutes(t *testing.T) {
	h := newHarness(t)
	start := h.ticket(ann)
	h.clock.Advance(TicketLifetime)
	if got := h.fromTelegram(h.browser(0).get(start).Result()); got != Expired {
		t.Errorf("result %q, want expired", got)
	}
}

func TestANewerTicketReplacesTheEarlierOne(t *testing.T) {
	h := newHarness(t)
	first := h.ticket(ann)
	second := h.ticket(ann)
	if got := h.fromTelegram(h.browser(0).get(first).Result()); got != Expired {
		t.Errorf("the first ticket: %q, want expired", got)
	}
	if rr := h.browser(0).get(second); !strings.HasPrefix(rr.Header().Get("Location"), h.or.URL+"/auth?") {
		t.Errorf("the second ticket goes to %q", rr.Header().Get("Location"))
	}
}

func TestOddTicketsAreRefused(t *testing.T) {
	h := newHarness(t)
	for _, tk := range []string{"nope", strings.Repeat("a", 43), strings.Repeat("a", 4000)} {
		if got := h.fromTelegram(h.browser(0).get(StartPath + "?ticket=" + tk).Result()); got != Expired {
			t.Errorf("%.10s…: %q, want expired", tk, got)
		}
	}
}

func TestATicketIsOnlyForSomeoneSignedIn(t *testing.T) {
	h := newHarness(t)
	rr := h.browser(0).request(http.MethodPost, TicketPath)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), `"sign_in"`) {
		t.Errorf("answered %d: %s", rr.Code, rr.Body.String())
	}
	if rr := h.browser(ann).get(TicketPath); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d", rr.Code)
	}
}

func TestATicketThatCannotBeKeptSaysSo(t *testing.T) {
	h := newHarness(t)
	h.store.failPut = true
	rr := h.browser(ann).request(http.MethodPost, TicketPath)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"unavailable"`) {
		t.Errorf("answered %d: %s", rr.Code, rr.Body.String())
	}
}
