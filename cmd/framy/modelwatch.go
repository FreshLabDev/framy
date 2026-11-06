// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// modelWatch reads what OpenAI's own list of models says, at the moments
// framy already asks for it (a person saves or rechecks a key), and tells the
// operator what the catalog does not know yet: a new GPT Image, and a
// shutdown day OpenAI has given that the overlay lacks. OpenAI is the one
// maker with such a list; Google's deprecation pages are prose.
//
// Each sentence is told once per process, so a person pressing "Check again"
// does not repeat it. Production tells the operator chat; every stack logs.
type modelWatch struct {
	files  catalog.Files
	log    *slog.Logger
	notify func(context.Context, string) error
	now    func() time.Time

	mu   sync.Mutex
	told map[string]bool
}

func (w *modelWatch) listed(provider string, models []catalog.Listed) {
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	for _, note := range w.files.ListedNotes(provider, models, now()) {
		w.mu.Lock()
		if w.told == nil {
			w.told = map[string]bool{}
		}
		seen := w.told[note]
		w.told[note] = true
		w.mu.Unlock()
		if seen {
			continue
		}
		w.log.Warn("the provider's list of models differs from the catalog", "provider", provider, "note", note)
		if w.notify != nil {
			// The check that called us must not wait for Telegram.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := w.notify(ctx, "framy: "+note); err != nil {
					w.log.Error("tell the operator chat", "error", err)
					// The next check tries again.
					w.mu.Lock()
					delete(w.told, note)
					w.mu.Unlock()
				}
			}()
		}
	}
}
