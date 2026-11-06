// SPDX-License-Identifier: Apache-2.0

package web

import (
	"cmp"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// Cloudflare Free gives one rate-limit rule, so the limits live here: a token
// bucket per address and, once sign-in exists, per person (x/time/rate). They
// are in memory, which is enough for one process; a restart gives everyone a
// full bucket. Quotas that must survive a restart, such as the free
// allowance, are counted from Postgres, not here.
//
// Every request is counted in the default class by address. A route in
// another class is counted in that class as well, so a burst of job
// creations also uses the visitor's general allowance.

// Class is a kind of route with its own limits.
type Class string

const (
	// ClassDefault is every route, by address.
	ClassDefault Class = "default"
	// ClassAuth is sign-in and the OAuth callbacks: /auth and /oauth.
	ClassAuth Class = "auth"
	// ClassMiniApp is the Mini App's sign-in, POST /auth/miniapp. It checks
	// an HMAC, so there is nothing to guess, and everyone who opens the Mini
	// App calls it, many of them from behind one carrier address.
	ClassMiniApp Class = "miniapp"
	// ClassUpload is the routes that take a photo or a clip, or make one.
	ClassUpload Class = "upload"
	// ClassJob is the routes that start a generation.
	ClassJob Class = "job"
	// ClassHook is /hooks, the providers' webhooks.
	ClassHook Class = "hook"
	// ClassHealth is /healthz, which pings the database.
	ClassHealth Class = "health"
)

// classOfPath gives a route its class from its path when Limit does not.
func classOfPath(p string) Class {
	switch {
	case p == "/auth/miniapp":
		return ClassMiniApp
	case underPath(p, "/auth"), underPath(p, "/oauth"):
		return ClassAuth
	case underPath(p, "/hooks"):
		return ClassHook
	case p == "/healthz":
		return ClassHealth
	}
	return ClassDefault
}

// Bucket is a token bucket: Burst requests at once, then Rate per second. A
// zero Bucket is no limit.
type Bucket struct {
	Rate  rate.Limit
	Burst int
}

func perSecond(n float64, burst int) Bucket { return Bucket{rate.Limit(n), burst} }
func perMinute(n float64, burst int) Bucket { return Bucket{rate.Limit(n / 60), burst} }

// ClassLimit is what a class allows one address and one person.
type ClassLimit struct{ IP, Person Bucket }

// Limits maps a class to its limit.
type Limits map[Class]ClassLimit

// DefaultLimits are the starting numbers. Address buckets are generous, since
// a mobile carrier puts many people behind one address; the strict ones are
// the routes that cost money or guess secrets. They are guesses until real
// traffic says otherwise.
func DefaultLimits() Limits {
	return Limits{
		ClassDefault: {IP: perSecond(30, 120)},
		ClassAuth:    {IP: perMinute(20, 10)},
		ClassMiniApp: {IP: perMinute(120, 60)},
		ClassUpload:  {IP: perMinute(40, 40), Person: perMinute(20, 20)},
		ClassJob:     {IP: perMinute(20, 20), Person: perMinute(10, 10)},
		ClassHook:    {IP: perSecond(20, 100)},
		ClassHealth:  {IP: perSecond(1, 10)},
	}
}

// classLimiter holds the buckets of one class; a nil set is no limit.
type classLimiter struct{ ip, person *bucketSet }

func newLimiters(override Limits) (map[Class]*classLimiter, error) {
	merged := DefaultLimits()
	for c, l := range override {
		if _, ok := merged[c]; !ok {
			return nil, fmt.Errorf("web: unknown rate class %q", c)
		}
		merged[c] = l
	}
	out := make(map[Class]*classLimiter, len(merged))
	for c, l := range merged {
		out[c] = &classLimiter{ip: newBucketSet(l.IP), person: newBucketSet(l.Person)}
	}
	return out, nil
}

// limited counts the request in the class and, when it is over, answers 429,
// or a redirect to location when the route has one (OnLimit), and reports
// true.
func (s *Server) limited(w http.ResponseWriter, r *http.Request, class Class, location string) bool {
	cl := s.limits[class]
	now := s.now()
	if cl.ip != nil {
		if ok, wait := cl.ip.take(bucketKey(ClientIP(r)), now); !ok {
			s.tooMany(w, class, wait, location)
			return true
		}
	}
	if cl.person != nil && s.person != nil {
		if id := s.person(r); id != "" {
			if ok, wait := cl.person.take(id, now); !ok {
				s.tooMany(w, class, wait, location)
				return true
			}
		}
	}
	return false
}

func (s *Server) tooMany(w http.ResponseWriter, class Class, wait time.Duration, location string) {
	metrics.HTTPRateLimited.Inc(string(class))
	if location != "" {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}

// maxKeys bounds one bucket set. Behind Cloudflare an address cannot be
// invented, so reaching it takes an attack from this many addresses. The set
// then makes room by dropping the buckets that are nearest to full, which
// are the ones closest to what a new visitor gets anyway; the buckets of
// whoever has used the allowance up stay until they refill, so filling the
// set never gives anyone a fresh allowance.
const maxKeys = 200_000

// evictShare is the part of a full set dropped at once, so that the scan
// that picks it is paid for once per many new visitors.
const evictShare = 10

// sweepEvery is how often buckets that have refilled are dropped. A full
// bucket is the same as a new one, so nothing is lost.
const sweepEvery = time.Minute

// bucketSet is one limiter per key. There is no goroutine: the set sweeps
// itself on the way through, so there is nothing to stop.
type bucketSet struct {
	mu      sync.Mutex
	b       Bucket
	m       map[string]*rate.Limiter
	max     int
	sweepAt time.Time
}

func newBucketSet(b Bucket) *bucketSet {
	if b.Rate <= 0 || b.Burst <= 0 {
		return nil
	}
	return &bucketSet{b: b, m: map[string]*rate.Limiter{}, max: maxKeys}
}

// take spends one token of key's bucket. When there is none it spends nothing
// and says how long until there is.
func (s *bucketSet) take(key string, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !now.Before(s.sweepAt) {
		s.sweep(now)
	}
	l := s.m[key]
	if l == nil {
		if len(s.m) >= s.max {
			s.makeRoom(now)
		}
		l = rate.NewLimiter(s.b.Rate, s.b.Burst)
		s.m[key] = l
	}
	res := l.ReserveN(now, 1)
	if !res.OK() {
		return false, time.Second
	}
	if wait := res.DelayFrom(now); wait > 0 {
		res.CancelAt(now)
		return false, wait
	}
	return true, 0
}

func (s *bucketSet) sweep(now time.Time) {
	s.sweepAt = now.Add(sweepEvery)
	for k, l := range s.m {
		if l.TokensAt(now) >= float64(s.b.Burst) {
			delete(s.m, k)
		}
	}
}

// makeRoom frees space in a full set: first the buckets that have refilled,
// and when that is not enough, the tenth with the most tokens left.
func (s *bucketSet) makeRoom(now time.Time) {
	s.sweep(now)
	if len(s.m) < s.max {
		return
	}
	type held struct {
		key    string
		tokens float64
	}
	all := make([]held, 0, len(s.m))
	for k, l := range s.m {
		all = append(all, held{k, l.TokensAt(now)})
	}
	slices.SortFunc(all, func(a, b held) int { return cmp.Compare(b.tokens, a.tokens) })
	for _, h := range all[:max(1, len(all)/evictShare)] {
		delete(s.m, h.key)
	}
}

// size is the number of keys held, for tests.
func (s *bucketSet) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
