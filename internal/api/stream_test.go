// SPDX-License-Identifier: Apache-2.0
package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
)

const wait = 2 * time.Second

func doneState(id int64) State {
	return State{
		ID: id, Status: jobs.Done, Kind: "image", Offer: "openrouter:test/image",
		Results: []Result{{ID: 70, ContentType: "image/png", Bytes: 3, Width: 1, Height: 1}},
	}
}

func TestEveryRouteWantsASignedInPerson(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/uploads"}, {"POST", "/api/jobs"}, {"GET", "/api/jobs/7"}, {"GET", "/api/jobs/7/events"},
	} {
		resp := r.do(0, c.method, c.path, strings.NewReader("{}"), "Idempotency-Key", "k")
		m := jsonOf(t, resp)
		if resp.StatusCode != http.StatusUnauthorized || m["error"] != "sign_in" {
			t.Errorf("%s %s: %d %v", c.method, c.path, resp.StatusCode, m)
		}
		if resp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: %q %q", c.method, c.path, resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Type"))
		}
	}
}

func TestAJobIsItsOwnersAlone(t *testing.T) {
	r := newRig(t)
	r.data.set(alice, queuedState(7))
	missing := bodyOf(t, r.do(bob, "GET", "/api/jobs/999", nil))
	for _, path := range []string{"/api/jobs/7", "/api/jobs/7/events"} {
		resp := r.do(bob, "GET", path, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s as another person: %d", path, resp.StatusCode)
		}
		// The same answer as for a job that does not exist, so ids tell nothing.
		if got := bodyOf(t, resp); got != missing {
			t.Errorf("%s as another person: %s, want the answer for a missing job: %s", path, got, missing)
		}
	}
	for _, path := range []string{"/api/jobs/x", "/api/jobs/0", "/api/jobs/-3", "/api/jobs/x/events"} {
		if resp := r.do(alice, "GET", path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	if resp := r.do(alice, "GET", "/api/jobs/7", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("the owner: %d", resp.StatusCode)
	}
}

func TestPollingGivesTheStateOfTheStream(t *testing.T) {
	r := newRig(t)
	st := doneState(7)
	cost := 0.0391
	st.ActualUSD = &cost
	r.data.set(alice, st)
	m := jsonOf(t, r.do(alice, "GET", "/api/jobs/7", nil))
	if m["status"] != "done" || m["seq"] != float64(1) {
		t.Fatalf("%v", m)
	}
	res := m["results"].([]any)[0].(map[string]any)
	if res["url"] != "https://files.example/obj-70?sig=secret" || res["content_type"] != "image/png" ||
		res["download"] != "https://files.example/obj-70?sig=secret&save=framy-7-1.png" {
		t.Errorf("%v", res)
	}
	if c := m["cost"].(map[string]any); c["usd"] != cost || c["label"] != "$0.04" || c["currency"] != "USD" {
		t.Errorf("%v", c)
	}
}

func TestAStreamFollowsAJobFromStartToEnd(t *testing.T) {
	r := newRig(t)
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")

	h := s.resp.Header
	if h.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content type %q", h.Get("Content-Type"))
	}
	if cc := h.Get("Cache-Control"); !strings.Contains(cc, "no-cache") || !strings.Contains(cc, "no-transform") {
		t.Errorf("cache control %q", cc)
	}
	first, ok := s.next(wait)
	if !ok || first.Retry != "3000" {
		t.Fatalf("the first block should set the reconnect delay: %+v", first)
	}
	id, m := s.event(wait)
	if id != 1 || m["status"] != "queued" || m["seq"] != float64(1) {
		t.Fatalf("first event %d %v", id, m)
	}

	run := queuedState(7)
	run.Status = jobs.Running
	r.data.set(alice, run)
	r.api.hub.notify(7)
	if id, m := s.event(wait); id != 2 || m["status"] != "running" {
		t.Fatalf("second event %d %v", id, m)
	}

	r.data.set(alice, doneState(7))
	r.api.hub.notify(7)
	id, m = s.event(wait)
	if id != 3 || m["status"] != "done" {
		t.Fatalf("third event %d %v", id, m)
	}
	if res := m["results"].([]any); len(res) != 1 || res[0].(map[string]any)["url"] == "" {
		t.Errorf("the result of a done job comes with a link: %v", m["results"])
	}
	if !s.ended(wait) {
		t.Error("the stream should end after the final state")
	}
	eventually(t, "the stream to let go of its place", func() bool { return r.api.hub.open() == 0 })
}

func TestAStreamOpenedLateStartsFromTheCurrentState(t *testing.T) {
	r := newRig(t)
	run := queuedState(7)
	run.Status = jobs.Running
	r.data.set(alice, queuedState(7))
	r.data.set(alice, run)
	r.data.set(alice, run) // seq 3: what a stream that came late missed is not replayed
	s := r.openStream(alice, "/api/jobs/7/events")
	if id, m := s.event(wait); id != 3 || m["status"] != "running" {
		t.Fatalf("%d %v", id, m)
	}
	s.quiet(100 * time.Millisecond)
}

func TestLastEventIDResumesWithoutRepeating(t *testing.T) {
	r := newRig(t)
	run := queuedState(7)
	run.Status = jobs.Running
	r.data.set(alice, queuedState(7))
	r.data.set(alice, run) // seq 2

	// The client has seen event 2: it is told nothing until there is more.
	s := r.openStream(alice, "/api/jobs/7/events", "Last-Event-ID", "2")
	if ev, ok := s.next(wait); !ok || ev.Retry == "" {
		t.Fatalf("the headers and the delay come at once: %+v", ev)
	}
	s.quiet(150 * time.Millisecond)
	r.data.set(alice, doneState(7)) // seq 3
	r.api.hub.notify(7)
	if id, m := s.event(wait); id != 3 || m["status"] != "done" {
		t.Fatalf("%d %v", id, m)
	}

	// The client missed event 3 only: it gets the end, and the stream ends.
	s = r.openStream(alice, "/api/jobs/7/events", "Last-Event-ID", "2")
	if id, m := s.event(wait); id != 3 || m["status"] != "done" {
		t.Fatalf("%d %v", id, m)
	}
	if !s.ended(wait) {
		t.Error("the stream should end after the final state")
	}

	// The client has the end already: 204 makes EventSource stop reconnecting.
	s = r.openStream(alice, "/api/jobs/7/events", "Last-Event-ID", "3")
	if s.resp.StatusCode != http.StatusNoContent {
		t.Errorf("a client with the final state: %d", s.resp.StatusCode)
	}

	// An id the job has not reached was not given by this job.
	s = r.openStream(alice, "/api/jobs/7/events", "Last-Event-ID", "999")
	if s.resp.StatusCode != http.StatusOK {
		t.Fatalf("%d", s.resp.StatusCode)
	}
	if id, _ := s.event(wait); id != 3 {
		t.Errorf("event %d", id)
	}
	// Junk is no id.
	s = r.openStream(alice, "/api/jobs/7/events", "Last-Event-ID", "abc")
	if id, _ := s.event(wait); id != 3 {
		t.Errorf("event %d", id)
	}
}

func TestACoalescedBurstStillEndsOnTheTruth(t *testing.T) {
	r := newRig(t)
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	// Three changes before the stream looks: it reports where they ended,
	// with an id above everything it sent before.
	run := queuedState(7)
	run.Status = jobs.Running
	r.data.set(alice, run)
	r.data.set(alice, run)
	r.data.set(alice, doneState(7))
	r.api.hub.notify(7)
	r.api.hub.notify(7)
	r.api.hub.notify(7)
	id, m := s.event(wait)
	if id != 4 || m["status"] != "done" {
		t.Fatalf("%d %v", id, m)
	}
}

func TestAChangeBetweenTheFirstReadAndTheLoopIsNotMissed(t *testing.T) {
	r := newRig(t) // no ping to find it later: only the wake can
	r.data.set(alice, queuedState(7))
	r.data.afterRead = func() {
		r.data.set(alice, doneState(7))
		r.api.hub.notify(7)
	}
	s := r.openStream(alice, "/api/jobs/7/events")
	if id, m := s.event(wait); id != 1 || m["status"] != "queued" {
		t.Fatalf("%d %v", id, m)
	}
	if id, m := s.event(wait); id != 2 || m["status"] != "done" {
		t.Fatalf("a change that landed between the first read and the wait was lost: %d %v", id, m)
	}
}

func TestAChangeNoNoticeAnnouncedIsFoundByThePing(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Ping = 40 * time.Millisecond } })
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	r.data.set(alice, doneState(7)) // no notify: the listener is down, say
	if id, m := s.event(wait); id != 2 || m["status"] != "done" {
		t.Fatalf("%d %v", id, m)
	}
}

func TestPingsKeepAnIdleStreamAlive(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Ping = 30 * time.Millisecond } })
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	for range 3 {
		ev, ok := s.next(wait)
		if !ok || len(ev.Comments) != 1 || ev.Comments[0] != "ping" || ev.Data != "" {
			t.Fatalf("%+v", ev)
		}
	}
}

func TestTheServersWriteTimeoutDoesNotCutAStream(t *testing.T) {
	r := newRig(t, func(o *rigOptions) {
		o.writeTimeout = 150 * time.Millisecond
		o.cfg = func(c *Config) { c.Ping = 40 * time.Millisecond }
	})
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	time.Sleep(500 * time.Millisecond) // three times the server's own limit
	r.data.set(alice, doneState(7))
	r.api.hub.notify(7)
	for {
		ev, ok := s.next(wait)
		if !ok {
			t.Fatal("the stream was cut before the end")
		}
		if ev.Data != "" {
			break
		}
	}
}

func TestAPersonHoldsAtMostThreeStreams(t *testing.T) {
	r := newRig(t)
	for id := int64(7); id < 12; id++ {
		r.data.set(alice, queuedState(id))
	}
	r.data.set(bob, queuedState(20))
	var open []*stream
	for id := int64(7); id < 10; id++ {
		s := r.openStream(alice, "/api/jobs/"+strconv.FormatInt(id, 10)+"/events")
		if s.resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: %d", id, s.resp.StatusCode)
		}
		s.event(wait)
		open = append(open, s)
	}
	fourth := r.openStream(alice, "/api/jobs/10/events")
	m := jsonOf(t, fourth.resp)
	if fourth.resp.StatusCode != http.StatusTooManyRequests || m["error"] != "too_many_streams" || m["limit"] != float64(3) {
		t.Fatalf("the fourth: %d %v", fourth.resp.StatusCode, m)
	}
	if fourth.resp.Header.Get("Retry-After") == "" {
		t.Error("the refusal says when to come back")
	}
	// A second stream on a job already followed is still a stream.
	if again := r.openStream(alice, "/api/jobs/7/events"); again.resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("the same job again: %d", again.resp.StatusCode)
	}
	// Another person has their own three.
	if other := r.openStream(bob, "/api/jobs/20/events"); other.resp.StatusCode != http.StatusOK {
		t.Errorf("another person: %d", other.resp.StatusCode)
	}
	// A job that is not hers is a 404, not a place taken from her.
	if resp := r.do(alice, "GET", "/api/jobs/20/events", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another's job over the limit: %d", resp.StatusCode)
	}
	// One closes, and a place is free.
	open[0].close()
	eventually(t, "the closed stream to free its place", func() bool { return r.api.streams.count(alice) < 3 })
	if s := r.openStream(alice, "/api/jobs/10/events"); s.resp.StatusCode != http.StatusOK {
		t.Errorf("after one closed: %d", s.resp.StatusCode)
	}
}

func TestAClientThatStopsReadingDoesNotHoldTheHandler(t *testing.T) {
	r := newRig(t, func(o *rigOptions) {
		o.cfg = func(c *Config) { c.Ping, c.WriteTimeout = 30*time.Millisecond, 100*time.Millisecond }
	})
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	if n := r.api.streams.count(alice); n != 1 {
		t.Fatalf("%d streams held", n)
	}
	// From here on the client's receive window is closed: the next write
	// blocks. Only its deadline lets the handler go.
	r.stuck.stuck.Store(true)
	eventually(t, "the handler to give up on the stalled client", func() bool {
		return r.api.streams.count(alice) == 0 && r.api.hub.open() == 0
	})
}

func TestStreamsEndWhenTheServerStops(t *testing.T) {
	r := newRig(t)
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	r.api.Close()
	if !s.ended(wait) {
		t.Fatal("an open stream would hold up the server's shutdown")
	}
	// A client that comes back to a stopping server is sent away at once.
	resp := r.do(alice, "GET", "/api/jobs/7/events", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusServiceUnavailable || m["error"] != "shutting_down" {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
}

func TestAFailedReadDoesNotEndAStream(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Ping = 30 * time.Millisecond } })
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	r.data.mu.Lock()
	r.data.failed = errBoom
	r.data.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	r.data.mu.Lock()
	r.data.failed = nil
	r.data.mu.Unlock()
	r.data.set(alice, doneState(7))
	if id, m := s.event(wait); id != 2 || m["status"] != "done" {
		t.Fatalf("%d %v", id, m)
	}
}

func TestAJobGoneEndsItsStream(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Ping = 30 * time.Millisecond } })
	r.data.set(alice, queuedState(7))
	s := r.openStream(alice, "/api/jobs/7/events")
	s.event(wait)
	r.data.mu.Lock()
	delete(r.data.states, 7)
	r.data.mu.Unlock()
	if !s.ended(wait) {
		t.Error("a stream of a job that is gone should end")
	}
}
