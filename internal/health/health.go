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
	// LastPoll is when getUpdates last answered; Initialized, whether the
	// bot has started.
	LastPoll    func() time.Time
	Initialized func() bool
	StartedAt   time.Time
	Build       Build
	Log         *slog.Logger
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
	result.TelegramInitialized = h.cfg.Initialized != nil && h.cfg.Initialized()
	result.OK = result.DB && result.TelegramInitialized && result.TelegramPollingFresh
	return result
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
