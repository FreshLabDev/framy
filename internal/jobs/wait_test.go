// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"testing"
	"time"
)

// A background run the provider asks to wait is asked about again after
// the wait, but never later than framy's longest wait or the run's hour.
func TestABackgroundRunIsAskedAgainInTime(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	q := &Queue{cfg: Config{PollEvery: 30 * time.Second}, now: func() time.Time { return now }}
	at := func(ago time.Duration) Job { s := now.Add(-ago); return Job{SubmittedAt: &s} }
	for _, c := range []struct {
		name       string
		j          Job
		retryAfter time.Duration
		want       time.Duration
	}{
		{"no wait asked", at(time.Minute), 0, 30 * time.Second},
		{"the provider's wait", at(time.Minute), 2 * time.Minute, 2 * time.Minute},
		{"a day's wait", at(time.Minute), 24 * time.Hour, maxRetryWait},
		{"a wait past the hour", at(55 * time.Minute), 24 * time.Hour, 5 * time.Minute},
		{"the hour all but over", at(RunDeadline - time.Second), 24 * time.Hour, 30 * time.Second},
	} {
		if got := q.askAgain(c.j, c.retryAfter); got != c.want {
			t.Errorf("%s: asked again in %s, want %s", c.name, got, c.want)
		}
	}
}
