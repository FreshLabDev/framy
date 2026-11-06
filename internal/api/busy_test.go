// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
	"github.com/FreshLabDev/framy/internal/storage"
)

// busyCount reads framy_busy_total{what}.
func busyCount(t *testing.T, what string) int64 {
	t.Helper()
	var buf strings.Builder
	metrics.Write(&buf)
	want := `framy_busy_total{what="` + what + `"} `
	for _, line := range strings.Split(buf.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, want); ok {
			n, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	return 0
}

// The clip queue is everyone's: a person over their own three tasks is
// only counted, the queue full of everyone's tells the operator chat too,
// once a day, so the owner learns that people wait.
func TestAFullClipQueueIsCountedAndToldOnceADay(t *testing.T) {
	clock := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var told []string
	r := newRig(t, func(o *rigOptions) {
		o.cfg = func(c *Config) {
			c.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
			c.Notify = func(_ context.Context, text string) error {
				mu.Lock()
				defer mu.Unlock()
				told = append(told, text)
				return nil
			}
		}
	})
	hold := make(chan struct{})
	defer close(hold)
	work := func(ctx context.Context) (storage.Asset, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return storage.Asset{}, nil
	}
	person, queue := busyCount(t, fullClipPerson), busyCount(t, fullClipQueue)
	start := func(user int64, key string) bool {
		_, e := r.api.videos.start(r.api.bg, user, key, work)
		return e == nil
	}
	for i := range maxPersonVideoTasks {
		if !start(1, "k"+strconv.Itoa(i)) {
			t.Fatalf("task %d of one person's refused", i)
		}
	}
	if start(1, "one more") || busyCount(t, fullClipPerson) != person+1 {
		t.Fatalf("a fourth of one person's: counted %d", busyCount(t, fullClipPerson)-person)
	}
	for n := maxPersonVideoTasks; n < maxVideoTasks; n++ {
		if !start(int64(2+n), "k") {
			t.Fatalf("task %d of everyone's refused", n)
		}
	}
	for range 2 {
		if start(99, "k") {
			t.Fatal("the queue took more than it holds")
		}
	}
	r.api.notices.Wait()
	mu.Lock()
	if busyCount(t, fullClipQueue) != queue+2 || len(told) != 1 || !strings.Contains(told[0], "clip queue (16") {
		t.Errorf("counted %d, told %q", busyCount(t, fullClipQueue)-queue, told)
	}
	clock = clock.Add(24 * time.Hour)
	mu.Unlock()
	start(99, "k")
	r.api.notices.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(told) != 2 {
		t.Errorf("the next day: told %q", told)
	}
}

// A notice that did not go is tried again by the next busy answer.
func TestANoticeThatFailedIsTriedAgain(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	r := newRig(t, func(o *rigOptions) {
		o.cfg = func(c *Config) {
			c.Notify = func(context.Context, string) error {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if calls == 1 {
					return context.DeadlineExceeded
				}
				return nil
			}
		}
	})
	for range 3 {
		r.api.full(fullPhotoSlots)
		r.api.notices.Wait()
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("%d notices tried, want 2", calls)
	}
}
