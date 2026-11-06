// SPDX-License-Identifier: Apache-2.0
package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeQueue struct {
	n     int
	since time.Time
	err   error
	asked time.Duration
}

func (q *fakeQueue) Stalled(_ context.Context, wait time.Duration) (int, time.Time, error) {
	q.asked = wait
	return q.n, q.since, q.err
}

type chat struct {
	sent []string
	fail bool
}

func (c *chat) notify(_ context.Context, text string) error {
	if c.fail {
		return errors.New("telegram is down")
	}
	c.sent = append(c.sent, text)
	return nil
}

// The disk is told once per step on the way up, again only after it fell
// clearly below, and a notice that failed is sent at the next look.
func TestTheDiskIsToldOncePerStep(t *testing.T) {
	ctx := context.Background()
	used := 0.5
	c := &chat{}
	w := New(Config{Dir: "/work", Notify: c.notify, Usage: func(dir string) (float64, error) {
		if dir != "/work" {
			t.Errorf("read %q", dir)
		}
		return used, nil
	}})

	for _, step := range []struct {
		used float64
		want string
	}{
		{0.5, ""},
		{0.79, ""},
		{0.81, "is 81% full, past 80%"},
		{0.82, ""},
		{0.79, ""},
		{0.80, ""},
		{0.91, "is 91% full, past 90%"},
		{0.97, "is 97% full, past 95%"},
		{0.99, ""},
		{0.70, ""},
		{0.85, "is 85% full, past 80%"},
	} {
		used = step.used
		c.sent = nil
		w.Check(ctx)
		switch {
		case step.want == "" && len(c.sent) != 0:
			t.Errorf("at %.2f: sent %q", step.used, c.sent)
		case step.want != "" && (len(c.sent) != 1 || !strings.Contains(c.sent[0], step.want) || !strings.Contains(c.sent[0], "/work")):
			t.Errorf("at %.2f: sent %q, want %q", step.used, c.sent, step.want)
		}
	}

	used, c.fail, c.sent = 0.92, true, nil
	w.Check(ctx)
	c.fail = false
	w.Check(ctx)
	if len(c.sent) != 1 || !strings.Contains(c.sent[0], "past 90%") {
		t.Errorf("after a failed notice: %q", c.sent)
	}
}

// Jobs nothing takes are told when it starts and when it ends, not every
// minute in between.
func TestAStalledQueueIsToldAtItsStartAndItsEnd(t *testing.T) {
	ctx := context.Background()
	q := &fakeQueue{}
	c := &chat{}
	w := New(Config{Queue: q, Notify: c.notify})

	w.Check(ctx)
	if q.asked != 10*time.Minute || len(c.sent) != 0 {
		t.Fatalf("a queue that moves: asked %v, sent %q", q.asked, c.sent)
	}
	q.n, q.since = 3, time.Date(2026, 11, 5, 2, 41, 0, 0, time.UTC)
	w.Check(ctx)
	w.Check(ctx)
	if len(c.sent) != 1 || !strings.Contains(c.sent[0], "3 jobs could start and have not, the oldest since 05.11 02:41 UTC") {
		t.Fatalf("stalled: %q", c.sent)
	}
	q.n = 1
	w.Check(ctx)
	if len(c.sent) != 1 {
		t.Errorf("told again while still stalled: %q", c.sent)
	}

	q.n, c.fail = 0, true
	w.Check(ctx)
	c.fail = false
	w.Check(ctx)
	w.Check(ctx)
	if len(c.sent) != 2 || c.sent[1] != "framy: jobs are taken again." {
		t.Errorf("moving again: %q", c.sent)
	}

	q.n = 1
	w.Check(ctx)
	if len(c.sent) != 3 || !strings.Contains(c.sent[2], "a job could start and has not") {
		t.Errorf("one job: %q", c.sent)
	}
}

// A queue that cannot be read says nothing to the chat: the database being
// gone is /healthz's to show.
func TestAQueueThatCannotBeReadIsOnlyLogged(t *testing.T) {
	q := &fakeQueue{err: errors.New("no database")}
	c := &chat{}
	New(Config{Queue: q, Notify: c.notify}).Check(context.Background())
	if len(c.sent) != 0 {
		t.Errorf("sent %q", c.sent)
	}
}

// Without an operator chat the watch still looks and logs.
func TestWithoutAChatItOnlyLogs(t *testing.T) {
	w := New(Config{Queue: &fakeQueue{n: 2, since: time.Now()}, Dir: "/work", Usage: func(string) (float64, error) { return 0.9, nil }})
	w.Check(context.Background())
	if !w.stalled || w.told != 2 {
		t.Errorf("stalled %v, told %d", w.stalled, w.told)
	}
}

func TestDiskUsage(t *testing.T) {
	dir := t.TempDir()
	used, err := DiskUsage(dir)
	if err != nil || used <= 0 || used > 1 {
		t.Fatalf("%s: %v, %v", dir, used, err)
	}
	// A directory not made yet is on its parent's disk.
	later, err := DiskUsage(dir + "/not/yet")
	if err != nil || later <= 0 || later > 1 {
		t.Errorf("a missing directory: %v, %v", later, err)
	}
}
