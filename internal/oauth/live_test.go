// SPDX-License-Identifier: Apache-2.0
//go:build live

// The two parts of the flow that can be checked against OpenRouter itself
// with no account, no browser and no credit: that its authorization page
// takes the request this package builds, and that its code exchange answers
// a code it did not issue with a refusal this package reads as one. The
// rest, a person pressing Authorize in a browser, is checked by hand.
//
//	FRAMY_LIVE_OAUTH=1 go test -tags=live -run Live -v ./internal/oauth/
//
// Nothing is saved and no key is made. The probe sends a made-up code and a
// made-up verifier; the test prints neither.
package oauth

import (
	"net/http"
	"os"
	"testing"
	"time"
)

func liveConnector(t *testing.T) *Connector {
	t.Helper()
	if os.Getenv("FRAMY_LIVE_OAUTH") != "1" {
		t.Skip("set FRAMY_LIVE_OAUTH=1 to run the live OAuth checks against openrouter.ai")
	}
	conn, err := New(Config{
		Store: newMemStore(), Keys: &fakeKeys{}, Person: person,
		SiteURL: "https://framy.example", UserAgent: "framy-live-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestLiveAuthPageTakesTheRequest(t *testing.T) {
	conn := liveConnector(t)
	state, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := hc.Get(conn.authRedirect(state, challenge(verifier)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The research saw a 307 to the login page for a person who is not
	// signed in; anything but an error is what this checks.
	if resp.StatusCode >= 400 {
		t.Fatalf("openrouter.ai/auth answered %d to the request the start route builds", resp.StatusCode)
	}
	t.Logf("openrouter.ai/auth answered %d", resp.StatusCode)
}

func TestLiveExchangeRefusesACodeItDidNotIssue(t *testing.T) {
	conn := liveConnector(t)
	verifier, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	key, result, reason := conn.exchange(ctx, "framy-live-probe-"+verifier[:8], verifier)
	if key != "" {
		t.Fatal("OpenRouter made a key for a made-up code")
	}
	if result != Refused {
		t.Fatalf("a made-up code ended as %q (%s), want %q: a 400 from the exchange", result, reason, Refused)
	}
	t.Logf("the exchange refused the probe: %s", reason)
}
