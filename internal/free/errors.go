// SPDX-License-Identifier: Apache-2.0
package free

import (
	"fmt"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// The errors below are what the surfaces word for the person (#24 for the
// bot, #19 for the site). They carry the facts; the English in Error is for
// logs and the operator, not for people.

// LimitError: the person has used the free allowance of a kind. A slot frees
// up at FreesAt, when the oldest run that still counts leaves the window.
type LimitError struct {
	Kind catalog.Kind
	// Limit is the runs allowed in Window.
	Limit  int
	Window time.Duration
	// FreesAt is when one more run is allowed. Runs still waiting or running
	// count, so it is the latest it can be: one that fails frees its slot
	// sooner.
	FreesAt time.Time
}

func (e *LimitError) Error() string {
	noun := string(e.Kind)
	if e.Limit != 1 {
		noun += "s"
	}
	return fmt.Sprintf("the free allowance (%d %s in %s) is used; the next run is free from %s",
		e.Limit, noun, windowName(e.Window), e.FreesAt.UTC().Format(time.RFC3339))
}

// Fuse is what closed the free tier.
type Fuse string

const (
	// FuseBudget: the day's budget is spent.
	FuseBudget Fuse = "budget"
	// FuseKey: framy's OpenRouter key cannot pay for a run: near its limit,
	// or a run just failed for lack of money.
	FuseKey Fuse = "key"
)

// ClosedError: the free tier is closed for everyone.
type ClosedError struct {
	Fuse Fuse
	// Until is when it opens again: 00:00 UTC for the budget, and for the
	// key the reset of its daily limit or the end of the pause after a
	// failure. Zero when nothing says: the owner raising the limit opens it.
	Until time.Time
	// Why is for logs and the operator chat.
	Why string
}

func (e *ClosedError) Error() string {
	until := "further notice"
	if !e.Until.IsZero() {
		until = e.Until.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("the free tier is closed until %s: %s", until, e.Why)
}

// NotFreeError: the run is not what the free allowance gives, so it needs
// the person's own key. The controls are locked to the allowance's settings
// (docs/design.md, Free tier).
type NotFreeError struct {
	Reason string
}

func (e *NotFreeError) Error() string { return "not part of the free allowance: " + e.Reason }

// windowName writes a window the way the allowance is spoken of.
func windowName(d time.Duration) string {
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d hours", d/time.Hour)
	}
	return d.String()
}
