// SPDX-License-Identifier: Apache-2.0

package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

func metricValue(t *testing.T, text, name string) int {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, name+" "); ok {
			n, err := strconv.Atoi(rest)
			if err != nil {
				t.Fatalf("metric %s: %v", name, err)
			}
			return n
		}
	}
	return 0
}

func limitedCount(t *testing.T, class Class) int {
	t.Helper()
	var sb strings.Builder
	metrics.Write(&sb)
	return metricValue(t, sb.String(), fmt.Sprintf(`framy_http_rate_limited_total{class=%q}`, class))
}

func TestDefaultClassLimitsEachAddress(t *testing.T) {
	clk := newClock()
	s := newSite(t, Config{Now: clk.Now, Limits: Limits{ClassDefault: {IP: perSecond(1, 3)}}})
	from := func(addr string) *httptest.ResponseRecorder {
		return s.do(t, req{target: "/page", remote: addr + ":4000"})
	}
	for i := 0; i < 3; i++ {
		if rr := from("198.51.100.1"); rr.Code != 200 {
			t.Fatalf("request %d within the burst: %d", i+1, rr.Code)
		}
	}
	rr := from("198.51.100.1")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("the fourth request: %d, want 429", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("the 429 is cacheable or untyped: %v", rr.Header())
	}
	if s.reached.Load() != 3 {
		t.Errorf("%d requests reached the handler, want 3", s.reached.Load())
	}
	// Someone else is not affected.
	if rr := from("198.51.100.2"); rr.Code != 200 {
		t.Errorf("another address: %d", rr.Code)
	}
	// The bucket refills at its rate.
	clk.Advance(1500 * time.Millisecond)
	if rr := from("198.51.100.1"); rr.Code != 200 {
		t.Errorf("after 1.5 s: %d, want one more token", rr.Code)
	}
	if rr := from("198.51.100.1"); rr.Code != http.StatusTooManyRequests {
		t.Errorf("a second request after 1.5 s: %d, want 429", rr.Code)
	}
}

func TestRetryAfterSaysHowLongToWait(t *testing.T) {
	clk := newClock()
	// One request per 20 s.
	s := newSite(t, Config{Now: clk.Now, Limits: Limits{ClassDefault: {IP: perMinute(3, 1)}}})
	s.get(t, "/page")
	rr := s.get(t, "/page")
	if rr.Code != 429 || rr.Header().Get("Retry-After") != "20" {
		t.Fatalf("status %d, Retry-After %q", rr.Code, rr.Header().Get("Retry-After"))
	}
	clk.Advance(10 * time.Second)
	if rr := s.get(t, "/page"); rr.Header().Get("Retry-After") != "10" {
		t.Errorf("ten seconds later Retry-After = %q, want 10", rr.Header().Get("Retry-After"))
	}
	clk.Advance(10 * time.Second)
	if rr := s.get(t, "/page"); rr.Code != 200 {
		t.Errorf("after the wait: %d", rr.Code)
	}
}

func TestIPv6VisitorsShareTheirSlash64(t *testing.T) {
	s := newSite(t, Config{Limits: Limits{ClassDefault: {IP: perSecond(1, 2)}}})
	from := func(addr string) int { return s.do(t, req{target: "/page", remote: "[" + addr + "]:4000"}).Code }
	from("2001:db8:1:2::1")
	from("2001:db8:1:2:aaaa:bbbb:cccc:dddd")
	if from("2001:db8:1:2::3") != 429 {
		t.Error("three addresses of one /64 got three buckets")
	}
	if from("2001:db8:1:3::1") != 200 {
		t.Error("another /64 was counted with the first")
	}
}

func TestStricterClassesAreByPathAndOption(t *testing.T) {
	// The default class is wide open here so only the class under test can refuse.
	wide := perSecond(1000, 1000)
	for _, tc := range []struct {
		name   string
		class  Class
		req    req
		burst  int
		limits Limits
	}{
		{"sign-in, by its path", ClassAuth, req{method: "POST", target: "/auth/login"}, 10, Limits{ClassDefault: {IP: wide}}},
		{"the OAuth callback, by its path", ClassAuth, req{target: "/oauth/openrouter/callback"}, 10, Limits{ClassDefault: {IP: wide}}},
		{"the Mini App's sign-in, by its path", ClassMiniApp, req{method: "POST", target: "/auth/miniapp"}, 60, Limits{ClassDefault: {IP: wide}}},
		{"a webhook, by its path", ClassHook, req{method: "POST", target: "/hooks/or/job1"}, 100, Limits{ClassDefault: {IP: wide}}},
		{"health, by its path", ClassHealth, req{target: "/healthz"}, 10, Limits{ClassDefault: {IP: wide}}},
		{"job creation, by Limit", ClassJob, req{method: "POST", target: "/api/jobs"}, 20, Limits{ClassDefault: {IP: wide}}},
		{"an upload, by Limit", ClassUpload, req{method: "POST", target: "/api/uploads"}, 40, Limits{ClassDefault: {IP: wide}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSite(t, Config{Now: newClock().Now, Limits: tc.limits})
			before := limitedCount(t, tc.class)
			for i := 1; i <= tc.burst; i++ {
				if rr := s.do(t, tc.req); rr.Code != 200 {
					t.Fatalf("request %d of %d: status %d", i, tc.burst, rr.Code)
				}
			}
			if rr := s.do(t, tc.req); rr.Code != http.StatusTooManyRequests {
				t.Fatalf("request %d: status %d, want 429", tc.burst+1, rr.Code)
			}
			if got := limitedCount(t, tc.class); got != before+1 {
				t.Errorf("framy_http_rate_limited_total{class=%q}: %d -> %d", tc.class, before, got)
			}
		})
	}
}

func TestStrictClassesAreStricterThanTheDefault(t *testing.T) {
	d := DefaultLimits()
	for _, c := range []Class{ClassAuth, ClassUpload, ClassJob} {
		if d[c].IP.Rate >= d[ClassDefault].IP.Rate || d[c].IP.Burst >= d[ClassDefault].IP.Burst {
			t.Errorf("%s allows an address %+v, no less than the default %+v", c, d[c].IP, d[ClassDefault].IP)
		}
	}
	for _, c := range []Class{ClassUpload, ClassJob} {
		if d[c].Person.Burst == 0 {
			t.Errorf("%s has no per-person limit", c)
		}
	}
}

func TestPersonLimitFollowsThePersonAcrossAddresses(t *testing.T) {
	wide := ClassLimit{IP: perSecond(1000, 1000)}
	s := newSite(t, Config{
		Now:    newClock().Now,
		Limits: Limits{ClassDefault: wide, ClassJob: {IP: perSecond(1000, 1000), Person: perSecond(1, 3)}},
		Person: func(r *http.Request) string { return r.Header.Get("X-Test-Person") },
	})
	job := func(person, addr string) int {
		h := map[string]string{}
		if person != "" {
			h["X-Test-Person"] = person
		}
		return s.do(t, req{method: "POST", target: "/api/jobs", remote: addr + ":1", header: h}).Code
	}
	// One person from three addresses is still one person.
	for i, addr := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		if job("42", addr) != 200 {
			t.Fatalf("request %d of the person was refused", i+1)
		}
	}
	if job("42", "198.51.100.4") != 429 {
		t.Error("a person over their bucket got through from a fresh address")
	}
	// Someone else on the same addresses is not held back by it.
	if job("43", "198.51.100.1") != 200 {
		t.Error("another person was counted with the first")
	}
	// Nobody signed in: only the address counts, and it is not limited here.
	for i := 0; i < 10; i++ {
		if job("", "198.51.100.9") != 200 {
			t.Fatalf("an anonymous request %d was limited by a person bucket", i+1)
		}
	}
}

func TestNoPersonHookMeansAddressesOnly(t *testing.T) {
	s := newSite(t, Config{
		Now:    newClock().Now,
		Limits: Limits{ClassDefault: {IP: perSecond(1000, 1000)}, ClassJob: {IP: perSecond(1000, 1000), Person: perSecond(1, 1)}},
	})
	for i := 0; i < 5; i++ {
		if rr := s.do(t, req{method: "POST", target: "/api/jobs", header: map[string]string{"X-Test-Person": "42"}}); rr.Code != 200 {
			t.Fatalf("request %d: %d", i+1, rr.Code)
		}
	}
}

func TestClassLimitsAddToTheDefaultOnes(t *testing.T) {
	// A visitor who has used up the general allowance is refused on every
	// route, the strict ones included.
	s := newSite(t, Config{
		Now:    newClock().Now,
		Limits: Limits{ClassDefault: {IP: perSecond(1, 2)}},
	})
	s.get(t, "/page")
	s.get(t, "/page")
	if rr := s.do(t, req{method: "POST", target: "/api/jobs"}); rr.Code != 429 {
		t.Fatalf("job creation after the general allowance: %d", rr.Code)
	}
}

func TestLimitsCanBeSwitchedOffPerClass(t *testing.T) {
	s := newSite(t, Config{Now: newClock().Now, Limits: Limits{ClassDefault: {}, ClassHealth: {}}})
	for i := 0; i < 500; i++ {
		if rr := s.get(t, "/healthz"); rr.Code != 200 {
			t.Fatalf("request %d: %d", i+1, rr.Code)
		}
	}
}

func TestUnknownClassesFailAtStart(t *testing.T) {
	if _, err := New(Config{Limits: Limits{"nope": {IP: perSecond(1, 1)}}}); err == nil {
		t.Error("a limit for a class that does not exist was accepted")
	}
	s := newSite(t, Config{})
	defer func() {
		if recover() == nil {
			t.Error("a route in an unknown class was accepted")
		}
	}()
	s.Handle("/x", http.NotFoundHandler(), Limit("nope"))
}

func TestBucketSetForgetsVisitorsWhoCameBack(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	set := newBucketSet(perSecond(1, 2))
	for i := 0; i < 1000; i++ {
		set.take(fmt.Sprintf("10.0.%d.%d", i/250, i%250), now)
	}
	if set.size() != 1000 {
		t.Fatalf("size = %d", set.size())
	}
	// Two seconds later every bucket has refilled; the next call that comes
	// after the sweep interval drops them all but its own.
	set.take("last", now.Add(sweepEvery+3*time.Second))
	if set.size() != 1 {
		t.Fatalf("size after the sweep = %d, want 1", set.size())
	}
	// A bucket still empty is kept, or its owner would get a fresh one.
	set = newBucketSet(perSecond(0.001, 1))
	set.take("busy", now)
	if ok, _ := set.take("busy", now); ok {
		t.Fatal("an empty bucket gave a token")
	}
	set.take("other", now.Add(2*sweepEvery))
	if ok, _ := set.take("busy", now.Add(2*sweepEvery)); ok {
		t.Fatal("the sweep gave a visitor over the limit a fresh bucket")
	}
}

func TestBucketSetHasABound(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	set := newBucketSet(perSecond(0.001, 1))
	set.max = 50
	for i := 0; i < 500; i++ {
		set.take(fmt.Sprintf("k%d", i), now)
		if set.size() > 50 {
			t.Fatalf("size %d passed the bound", set.size())
		}
	}
}
