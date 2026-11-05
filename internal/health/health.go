// SPDX-License-Identifier: Apache-2.0

// Package health answers whether framy works. The public listener's
// /healthz says only that; the same check with each of its parts, the
// version and the commit answers on the metrics listener, which stays
// inside the container (docs/design.md, Security and operations).
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Store interface {
	Ping(context.Context) error
}

type Build struct {
	Version string
	Commit  string
	Date    string
}

// Config is what the check looks at.
type Config struct {
	Store Store
	// LastPoll is when getUpdates last answered; LastUpdate, when it last
	// brought an update; Initialized, whether the bot has started.
	LastPoll    func() time.Time
	LastUpdate  func() time.Time
	Initialized func() bool
	// Telegram is what the other calls to Telegram showed. Nil: not looked at.
	Telegram  *Calls
	StartedAt time.Time
	Build     Build
	Log       *slog.Logger
}

type Handler struct {
	cfg Config
}

// pollMaxAge is how long polling may stay silent. getUpdates holds a request for
// 25 seconds, so anything past three of those is a stuck poller.
const pollMaxAge = 90 * time.Second

func New(cfg Config) *Handler {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Handler{cfg: cfg}
}

// report is the check with its parts, as the metrics listener gives it.
type report struct {
	OK                   bool       `json:"ok"`
	Version              string     `json:"version"`
	Commit               string     `json:"commit"`
	BuiltAt              string     `json:"built_at"`
	DB                   bool       `json:"db"`
	TelegramInitialized  bool       `json:"telegram_initialized"`
	TelegramPollingFresh bool       `json:"telegram_polling_fresh"`
	TelegramLastPollAt   *time.Time `json:"telegram_last_poll_at,omitempty"`
	TelegramLastUpdateAt *time.Time `json:"telegram_last_update_at,omitempty"`
	TelegramCallsFailing bool       `json:"telegram_calls_failing"`
	TelegramLastAnswerAt *time.Time `json:"telegram_last_answer_at,omitempty"`
	TelegramLastFailedAt *time.Time `json:"telegram_last_failed_at,omitempty"`
}

// Public answers whether framy is healthy and nothing more: 200 with
// {"ok":true}, or 503 with {"ok":false}. The tunnel makes it public and the
// repository is open, so which commit runs and which part fails are not for
// everyone; Docker and a watch from outside need only the answer.
func (h *Handler) Public(w http.ResponseWriter, r *http.Request) {
	result := h.check(r.Context())
	write(w, result.OK, struct {
		OK bool `json:"ok"`
	}{result.OK})
}

// Detail answers with every part of the check, for the metrics listener.
// It is read by a person, with the image's busybox wget, which prints no
// body of a 503: so it is a 200 whatever "ok" says.
func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	write(w, true, h.check(r.Context()))
}

func (h *Handler) check(ctx context.Context) report {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	b := h.cfg.Build
	result := report{Version: b.Version, Commit: b.Commit, BuiltAt: b.Date}
	if err := h.cfg.Store.Ping(ctx); err != nil {
		h.cfg.Log.Error("health database check failed", "error", err)
	} else {
		result.DB = true
	}
	lastPoll := h.cfg.LastPoll()
	if !lastPoll.IsZero() {
		result.TelegramLastPollAt = &lastPoll
	}
	result.TelegramPollingFresh = fresh(h.cfg.StartedAt, lastPoll, pollMaxAge)
	if h.cfg.LastUpdate != nil {
		result.TelegramLastUpdateAt = when(h.cfg.LastUpdate())
	}
	if c := h.cfg.Telegram; c != nil {
		answered, failed, failing := c.state(time.Now())
		result.TelegramCallsFailing = failing
		result.TelegramLastAnswerAt, result.TelegramLastFailedAt = when(answered), when(failed)
	}
	result.TelegramInitialized = h.cfg.Initialized != nil && h.cfg.Initialized()
	result.OK = result.DB && result.TelegramInitialized && result.TelegramPollingFresh && !result.TelegramCallsFailing
	return result
}

func when(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Calls follows what framy's calls to Telegram show, getUpdates apart.
// The family's Bot API in --local mode answers getUpdates from the host
// even when it has lost Telegram, so polling stays fresh through an outage
// (3 November); calls that keep failing show it. Its zero value is ready.
type Calls struct {
	mu sync.Mutex
	// answered is the last call Telegram answered; firstFailed, the first
	// failure since then; failed, the last failure.
	answered, firstFailed, failed time.Time
}

const (
	// failingFor is how long calls must keep failing, with none answered,
	// before Telegram counts as out of reach. A restart of the Bot API, or
	// one call's retries, end sooner.
	failingFor = 2 * time.Minute
	// failingRecent: a failure older than this says nothing about now. A
	// bot nobody uses makes no calls, so it cannot tell.
	failingRecent = 5 * time.Minute
)

// Answered: a call at the time given got Telegram's answer.
func (c *Calls) Answered(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answered, c.firstFailed = at, time.Time{}
}

// Failed: a call at the time given got no answer, or a 5xx.
func (c *Calls) Failed(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.firstFailed.IsZero() {
		c.firstFailed = at
	}
	c.failed = at
}

// state: the last answer and failure, and whether calls have failed for
// failingFor with none answered, the last of them within failingRecent.
func (c *Calls) state(now time.Time) (answered, failed time.Time, failing bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	failing = !c.firstFailed.IsZero() && c.failed.Sub(c.firstFailed) >= failingFor && now.Sub(c.failed) < failingRecent
	return c.answered, c.failed, failing
}

func write(w http.ResponseWriter, ok bool, body any) {
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(body)
}

// fresh gives a process that has not polled yet the same grace period from its
// start, so a container is not reported unhealthy while it connects.
func fresh(startedAt, lastSeen time.Time, maxAge time.Duration) bool {
	if lastSeen.IsZero() {
		return time.Since(startedAt) < maxAge
	}
	return time.Since(lastSeen) < maxAge
}
