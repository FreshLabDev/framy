// SPDX-License-Identifier: Apache-2.0
package free

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// KeyState is what OpenRouter says about framy's own key. The owner sets the
// key's limit in OpenRouter, a daily one to match the day's budget.
type KeyState struct {
	// Limited: the key has a spending limit. An unlimited key says nothing
	// about when it runs out.
	Limited bool
	// RemainingUSD is limit_remaining.
	RemainingUSD float64
	// Daily: the limit resets at 00:00 UTC.
	Daily bool
}

// KeySource reads the state of the key: GET /api/v1/key.
type KeySource func(ctx context.Context) (KeyState, error)

// DefaultKeyEvery is how often the key is asked about.
const DefaultKeyEvery = time.Minute

// KeyWatch keeps the last reading of the key, so a request for a free run
// never waits on OpenRouter while it holds the allowance's locks.
type KeyWatch struct {
	source KeySource
	every  time.Duration
	log    *slog.Logger
	now    func() time.Time

	mu   sync.Mutex
	last KeyState
	at   time.Time
}

// NewKeyWatch builds the watch; every is DefaultKeyEvery when zero.
func NewKeyWatch(source KeySource, every time.Duration, log *slog.Logger) *KeyWatch {
	if every <= 0 {
		every = DefaultKeyEvery
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &KeyWatch{source: source, every: every, log: log, now: time.Now}
}

// Run reads the key now and then every interval, until ctx ends.
func (w *KeyWatch) Run(ctx context.Context) {
	tick := time.NewTicker(w.every)
	defer tick.Stop()
	for {
		if err := w.Refresh(ctx); err != nil && ctx.Err() == nil {
			// The last reading stays until it is too old to trust.
			w.log.Warn("the OpenRouter key's limit could not be read", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Refresh reads the key once.
func (w *KeyWatch) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st, err := w.source(ctx)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.last, w.at = st, w.now()
	w.mu.Unlock()
	return nil
}

// State is the last reading. A reading older than five intervals is no
// reading: the allowance then goes by the budget alone, and OpenRouter's own
// refusal, which closes it too, if the key really is out.
func (w *KeyWatch) State() (KeyState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.at.IsZero() || w.now().Sub(w.at) > 5*w.every {
		return KeyState{}, false
	}
	return w.last, true
}
