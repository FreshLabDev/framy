// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	catalogfiles "github.com/FreshLabDev/framy/catalog"
	"github.com/FreshLabDev/framy/internal/catalog"
)

// A note is told once, however many times a person checks the key, and told
// again after a failed send.
func TestModelWatchTellsEachNoteOnce(t *testing.T) {
	files, err := catalog.LoadFiles(catalogfiles.FS)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent []string
	fail := true
	told := make(chan struct{}, 8)
	w := &modelWatch{
		files: files,
		log:   slog.New(slog.DiscardHandler),
		now:   func() time.Time { return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) },
		notify: func(_ context.Context, text string) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, text)
			told <- struct{}{}
			if fail {
				fail = false
				return errors.New("telegram is down")
			}
			return nil
		},
	}
	models := []catalog.Listed{{ID: "gpt-image-2.6-flare"}, {ID: "gpt-image-9"}}
	wait := func() {
		select {
		case <-told:
		case <-time.After(5 * time.Second):
			t.Fatal("nothing was sent")
		}
	}

	w.listed("openai", models)
	wait()
	// The failed send is forgotten, so the next check tells it again.
	time.Sleep(50 * time.Millisecond)
	w.listed("openai", models)
	wait()
	time.Sleep(50 * time.Millisecond)
	w.listed("openai", models)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent %d messages: %q", len(sent), sent)
	}
	if !strings.HasPrefix(sent[0], "framy: OpenAI lists gpt-image-9") {
		t.Errorf("message = %q", sent[0])
	}
}
