// SPDX-License-Identifier: Apache-2.0
package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
)

func TestAnErrorIsShownWhenItWasWrittenForPeople(t *testing.T) {
	for _, c := range []struct {
		class jobs.Class
		msg   string
		want  string
	}{
		{jobs.Moderation, "moderation: the provider refused the prompt (violence)", "moderation: the provider refused the prompt (violence)"},
		{jobs.Key, "the person has no OpenAI key", "the person has no OpenAI key"},
		{jobs.Storage, "fetch the result from files.example: 502", "fetch the result from files.example: 502"},
		// A Go error from inside framy may name a table or a path.
		{jobs.Internal, `insert into "job": relation does not exist`, ""},
		{jobs.DraftExpired, "anything", ""},
		{jobs.Class("a class from the future"), "anything", ""},
		// A link may open a person's file to whoever holds it.
		{jobs.Refused, "cannot fetch https://r2.example/u/1/in/a.jpg?X-Amz-Signature=abc&X-Amz-Expires=900 for the run", "cannot fetch <link> for the run"},
		{jobs.ProviderFailed, "   padded   ", "padded"},
	} {
		if got := publicMessage(c.class, c.msg); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.class, c.msg, got, c.want)
		}
	}
	long := publicMessage(jobs.ProviderFailed, strings.Repeat("я", 1000))
	if n := len([]rune(long)); n > maxMessage+3 {
		t.Errorf("%d characters", n)
	}
}

func TestAJobShowsItsErrorAndItsRetry(t *testing.T) {
	r := newRig(t)
	at := time.Date(2026, 10, 27, 12, 5, 0, 0, time.UTC)
	st := queuedState(7)
	st.ErrorClass, st.Error, st.RetryAt = jobs.Unavailable, "unavailable: 503", &at
	r.data.set(alice, st)
	m := jsonOf(t, r.do(alice, "GET", "/api/jobs/7", nil))
	e := m["error"].(map[string]any)
	if m["status"] != "queued" || e["class"] != "unavailable" || e["message"] != "unavailable: 503" || m["retry_at"] != "2026-10-27T12:05:00Z" {
		t.Errorf("%v", m)
	}
	failed := State{ID: 8, Status: jobs.Failed, Kind: "image", ErrorClass: jobs.Internal, Error: "pq: secret detail"}
	r.data.set(alice, failed)
	m = jsonOf(t, r.do(alice, "GET", "/api/jobs/8", nil))
	if e := m["error"].(map[string]any); e["class"] != "internal" || e["message"] != nil {
		t.Errorf("%v", e)
	}
}

func TestAResultWithoutAStoreHasNoLink(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Links = nil } })
	r.data.set(alice, doneState(7))
	m := jsonOf(t, r.do(alice, "GET", "/api/jobs/7", nil))
	res := m["results"].([]any)[0].(map[string]any)
	if _, ok := res["url"]; ok || res["id"] != float64(70) {
		t.Errorf("%v", res)
	}
}

func TestFirstTagOfAcceptLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"uk-UA,uk;q=0.9,en;q=0.8": "uk", "en": "en", "pl-PL": "pl", "*": "", "": "", "x": "", "fil-PH": "fil",
		"123": "", "uk;q=0.1": "uk", " BE-by ": "be",
	} {
		if got := firstTag(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestPersonKeyCountsByTheId(t *testing.T) {
	key := PersonKey(func(*http.Request) (int64, bool) { return 42, true })
	if got := key(nil); got != "42" {
		t.Errorf("%q", got)
	}
	if got := PersonKey(func(*http.Request) (int64, bool) { return 0, false })(nil); got != "" {
		t.Errorf("%q", got)
	}
}
