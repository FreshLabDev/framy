// SPDX-License-Identifier: Apache-2.0
//go:build integration

package free

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// budgetCase is a set of free runs of the day and what they have cost framy.
type budgetCase struct {
	name string
	rows []row
	// spent is what they add up to, in dollars.
	spent float64
}

// The budget counts the estimate while a run is queued or running, the
// actual cost once it ended, and the estimate for a run that may have been
// paid and has no cost on the books. The test finds the sum by the edge: a
// run that costs what is left is allowed, and one micro-dollar more is not.
func TestTheBudgetCountsEstimatesThenActualCosts(t *testing.T) {
	const budget = 1.0
	// The edge of the day: a microsecond before midnight is yesterday's.
	today := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), time.Now().UTC().Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.Add(-time.Microsecond)
	cases := []budgetCase{
		{"nothing yet", nil, 0},
		{"a queued run costs its estimate", []row{{status: "queued", estimate: 0.30}}, 0.30},
		{"a running run costs its estimate", []row{{status: "running", estimate: 0.30}}, 0.30},
		{"a run in flight with a cost already known still counts the estimate", []row{{status: "running", estimate: 0.30, actual: ptr(0.10)}}, 0.30},
		{"a finished run costs what the provider charged", []row{{estimate: 0.30, actual: ptr(0.10)}}, 0.10},
		{"a finished run that cost more than the estimate", []row{{estimate: 0.30, actual: ptr(0.45)}}, 0.45},
		{"a finished run with no cost reported costs its estimate", []row{{estimate: 0.30}}, 0.30},
		{"a failed run costs nothing", []row{{status: "failed", class: "key", estimate: 0.30}}, 0},
		{"a run refused before it started costs nothing", []row{{status: "failed", class: "refused", estimate: 0.30}, {status: "failed", class: "storage", estimate: 0.30}}, 0},
		{"a run that failed for money costs nothing", []row{{status: "failed", class: "money", estimate: 0.30, finishedAgo: time.Hour}}, 0},
		{"a failed run that may have been paid costs its estimate", []row{
			{status: "failed", class: "timeout", estimate: 0.10}, {status: "failed", class: "interrupted", estimate: 0.20}, {status: "failed", class: "provider", estimate: 0.30},
		}, 0.60},
		{"a refused run costs what the provider charged for it", []row{{status: "failed", class: "moderation", estimate: 0.30, actual: ptr(0.05)}, {status: "failed", class: "moderation", estimate: 0.30}}, 0.05},
		{"a video the provider never finished may have been paid", []row{{status: "expired", estimate: 0.30}, {status: "expired", estimate: 0.30, actual: ptr(0.20)}}, 0.50},
		{"a cancelled run and a draft cost nothing", []row{{status: "cancelled", estimate: 0.30}, {status: "draft", estimate: 0.30}}, 0},
		{"a run on the person's own key is not the tier's", []row{{own: true, estimate: 0.30}}, 0},
		{"yesterday is not today", []row{{estimate: 0.30, at: yesterday}}, 0},
		{"midnight is today", []row{{estimate: 0.30, at: today}}, 0.30},
	}
	h := newHarness(t, budget)
	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := h.pool.Exec(ctx, `TRUNCATE framy.site_delivery, framy.chat_run, framy.job_input, framy.asset, framy.job, framy.river_job`); err != nil {
				t.Fatal(err)
			}
			for i, r := range c.rows {
				r.user, r.plan = alice+int64(i), ImageMAI
				h.insert(r)
			}
			left := budget - c.spent
			adm := jobs.Admission{UserID: bob, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings()}
			adm.EstimateUSD = left
			if err := h.a.Check(ctx, adm); err != nil {
				t.Fatalf("a run that costs the $%.6f that is left was refused: %v", left, err)
			}
			adm.EstimateUSD = left + 0.000001
			closed := asClosed(t, h.a.Check(ctx, adm))
			if closed.Fuse != FuseBudget {
				t.Fatalf("fuse = %s", closed.Fuse)
			}
		})
	}
}

// Two requests at the same moment must not both pass the last cents. The
// people differ, so only the budget's own lock can keep them apart.
func TestTwoRequestsAtOnceDoNotPassTheLastCents(t *testing.T) {
	h := newHarness(t, 1)
	h.a.pause = func() { time.Sleep(15 * time.Millisecond) }
	h.insert(row{user: alice, plan: ImageMAI, estimate: 0.10})

	const requests = 12
	errs := make([]error, requests)
	created := make([]bool, requests)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range requests {
		wg.Go(func() {
			<-start
			_, created[i], errs[i] = h.enqueue(bob+int64(i), ImageMAI, 0.30)
		})
	}
	close(start)
	wg.Wait()

	won := 0
	for i := range requests {
		if created[i] {
			won++
		} else if asClosed(t, errs[i]).Fuse != FuseBudget {
			t.Fatalf("request %d: %v", i, errs[i])
		}
	}
	// $0.10 spent, $0.90 left: three runs of $0.30 and not a fourth.
	if won != 3 {
		t.Fatalf("%d runs passed, want the 3 the budget pays for", won)
	}
	var spent float64
	if err := h.pool.QueryRow(context.Background(), `SELECT sum(estimate_usd)::float8 FROM job WHERE free`).Scan(&spent); err != nil || spent > 1.0000001 {
		t.Fatalf("the books hold $%.6f, %v; the budget is $1", spent, err)
	}
	h.a.Wait()
}

func TestASpentBudgetClosesTheTierUntilMidnightAndTheOperatorHearsOnce(t *testing.T) {
	h := newHarness(t, 0.05)
	ctx := context.Background()
	closedBefore := metric("framy_free_closed_total", "fuse", "budget")
	refusedBefore := metric("framy_free_refused_total", "reason", "budget")

	h.mustEnqueue(alice, ImageMAI, 0.0195)
	h.mustEnqueue(bob, ImageMAI, 0.0195)
	// $0.011 left: the third run of $0.0195 does not fit, and the message
	// says until when.
	_, created, err := h.enqueue(carol, ImageMAI, 0.0195)
	closed := asClosed(t, err)
	midnight := h.day().Add(24 * time.Hour)
	if created || closed.Fuse != FuseBudget || !closed.Until.Equal(midnight) {
		t.Fatalf("created %v, %+v; want the budget closing the tier until %s", created, closed, midnight)
	}
	// A cheaper run still fits what is left: the budget is never passed, and
	// never wasted either.
	h.mustEnqueue(carol, ImageKlein, 0.009)
	// People who ask again, at once, get the same answer, and the chat is
	// told once.
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			if _, _, err := h.enqueue(bob+int64(i)+1, ImageMAI, 0.0195); asClosed(t, err).Fuse != FuseBudget {
				t.Errorf("request %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	told, attempts := h.notices()
	if len(told) != 1 || attempts != 1 || !strings.Contains(told[0], "the free tier is closed until "+midnight.Format(time.RFC3339)) || !strings.Contains(told[0], "budget") {
		t.Fatalf("the operator chat heard %q in %d tries; want one notice, with the time it opens", told, attempts)
	}
	if got := metric("framy_free_closed_total", "fuse", "budget"); got != closedBefore+1 {
		t.Fatalf("closings = %d, want %d: once for the day", got, closedBefore+1)
	}
	if got := metric("framy_free_refused_total", "reason", "budget"); got != refusedBefore+7 {
		t.Fatalf("budget refusals = %d, want %d", got, refusedBefore+7)
	}
	// The people who were told "closed" can see why in the data, too.
	if err := h.a.Check(ctx, jobs.Admission{UserID: 1030, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings(), EstimateUSD: 0.0195}); asClosed(t, err).Fuse != FuseBudget {
		t.Fatal(err)
	}

	// The next day it opens again, and what yesterday spent is not counted.
	h.advance(24 * time.Hour)
	h.mustEnqueue(1031, ImageMAI, 0.0195)
	// Spent again, it closes again, and the chat hears a second time.
	h.insert(row{user: 1032, plan: ImageMAI, estimate: 0.04})
	asClosed(t, h.requireRefused(1033))
	told, _ = h.notices()
	if len(told) != 2 || !strings.Contains(told[1], h.day().Add(24*time.Hour).Format(time.RFC3339)) {
		t.Fatalf("the operator chat heard %q; want a second notice for the second day", told)
	}
}

func TestANoticeThatFailedIsTriedAgainByTheNextRefusal(t *testing.T) {
	h := newHarness(t, 0.01)
	h.setNotifyErr(errors.New("telegram is down"))
	asClosed(t, h.requireRefused(alice))
	if told, attempts := h.notices(); len(told) != 0 || attempts != 1 {
		t.Fatalf("told %q in %d tries", told, attempts)
	}
	// Telegram is back: the next refusal tells the chat, and the one after
	// has nothing to add.
	h.setNotifyErr(nil)
	asClosed(t, h.requireRefused(bob))
	asClosed(t, h.requireRefused(carol))
	if told, attempts := h.notices(); len(told) != 1 || attempts != 2 {
		t.Fatalf("told %q in %d tries, want one notice after two tries", told, attempts)
	}
	// With no operator chat the tier still closes and the refusal still counts.
	quiet := h.allowance(0.01, nil)
	err := quiet.Check(context.Background(), jobs.Admission{UserID: alice, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings(), EstimateUSD: 0.0195})
	asClosed(t, err)
	quiet.refused(err)
	quiet.Wait()
}

func TestAZeroBudgetKeepsTheTierClosed(t *testing.T) {
	h := newHarness(t, 0)
	closed := asClosed(t, h.requireRefused(alice))
	if closed.Fuse != FuseBudget {
		t.Fatalf("fuse = %s", closed.Fuse)
	}
}

func TestTheKeyClosesTheTierWhenItCannotPay(t *testing.T) {
	h := newHarness(t, 100)
	ctx := context.Background()
	adm := jobs.Admission{UserID: alice, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings(), EstimateUSD: 0.0195}
	midnight := h.day().Add(24 * time.Hour)

	// No reading, or a key without a limit, says nothing.
	for name, c := range map[string]struct {
		st KeyState
		ok bool
	}{
		"no reading":        {KeyState{}, false},
		"a key with no cap": {KeyState{}, true},
		"enough left":       {KeyState{Limited: true, RemainingUSD: 0.0195, Daily: true}, true},
		"plenty left":       {KeyState{Limited: true, RemainingUSD: 5}, true},
		"a stale reading":   {KeyState{Limited: true, RemainingUSD: 0}, false},
	} {
		h.key.set(c.st, c.ok)
		if err := h.a.Check(ctx, adm); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Less than a run left: closed until the key's daily limit resets.
	h.key.set(KeyState{Limited: true, RemainingUSD: 0.019499, Daily: true}, true)
	closed := asClosed(t, h.a.Check(ctx, adm))
	if closed.Fuse != FuseKey || !closed.Until.Equal(midnight) {
		t.Fatalf("%+v; want the key closing the tier until %s", closed, midnight)
	}
	// A limit that does not reset daily says no time: the owner opens it.
	h.key.set(KeyState{Limited: true, RemainingUSD: 0.01}, true)
	if closed := asClosed(t, h.a.Check(ctx, adm)); closed.Fuse != FuseKey || !closed.Until.IsZero() {
		t.Fatalf("%+v", closed)
	}

	// What OpenRouter reports leaves out the runs on their way.
	h.key.set(KeyState{Limited: true, RemainingUSD: 0.03, Daily: true}, true)
	if err := h.a.Check(ctx, adm); err != nil {
		t.Fatal(err)
	}
	h.insert(row{user: bob, plan: ImageMAI, status: "running", estimate: 0.02})
	if closed := asClosed(t, h.a.Check(ctx, adm)); closed.Fuse != FuseKey {
		t.Fatalf("%+v; $0.01 is left after the run that is on its way", closed)
	}
	// It is the tier that closes, for the person with the allowance left.
	before := metric("framy_free_refused_total", "reason", "key")
	asClosed(t, h.requireRefused(carol))
	if metric("framy_free_refused_total", "reason", "key") != before+1 {
		t.Fatal("the refusal was not counted")
	}
	told, _ := h.notices()
	if len(told) != 1 || !strings.Contains(told[0], "OpenRouter key") {
		t.Fatalf("the operator chat heard %q", told)
	}
}

// A free run that failed for lack of money shows the key cannot pay,
// whatever the last reading says.
func TestAFreeRunThatFailedForWantOfMoneyPausesTheTier(t *testing.T) {
	h := newHarness(t, 100)
	ctx := context.Background()
	adm := jobs.Admission{UserID: alice, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings(), EstimateUSD: 0.0195}

	// One on the person's own key says nothing about framy's.
	h.insert(row{user: bob, plan: ImageMAI, status: "failed", class: "money", own: true, finishedAgo: time.Minute})
	// Nor does an old one.
	h.insert(row{user: bob, plan: ImageMAI, status: "failed", class: "money", ago: time.Hour, finishedAgo: MoneyPause + time.Minute})
	if err := h.a.Check(ctx, adm); err != nil {
		t.Fatal(err)
	}

	_, at := h.insert(row{user: bob, plan: ImageMAI, status: "failed", class: "money", ago: 6 * time.Minute, finishedAgo: 5 * time.Minute})
	closed := asClosed(t, h.a.Check(ctx, adm))
	finished := h.clock().Add(-5 * time.Minute)
	if closed.Fuse != FuseKey || closed.Until.Before(finished.Add(MoneyPause).Add(-time.Second)) || closed.Until.After(finished.Add(MoneyPause).Add(time.Second)) {
		t.Fatalf("%+v (queued %s); want the tier closed for %s after the failure", closed, at, MoneyPause)
	}
	// The pause ends by itself.
	h.advance(MoneyPause)
	if err := h.a.Check(ctx, adm); err != nil {
		t.Fatalf("still closed: %v", err)
	}
}

// A process with no operator chat, as the test bot on production's schema
// is, leaves the day to the one that has someone to tell (docs/design.md,
// Stacks).
func TestAProcessWithNobodyToTellLeavesTheNoticeToProduction(t *testing.T) {
	h := newHarness(t, 0.01)
	quiet := h.allowance(0.01, nil)
	err := quiet.Check(context.Background(), jobs.Admission{UserID: alice, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings(), EstimateUSD: 0.0195})
	asClosed(t, err)
	quiet.refused(err)
	quiet.Wait()
	var day *time.Time
	if err := h.pool.QueryRow(context.Background(), `SELECT free_budget_alert_day FROM framy.runtime_state`).Scan(&day); err != nil || day != nil {
		t.Fatalf("the quiet process took the day: %v %v", day, err)
	}
	asClosed(t, h.requireRefused(bob))
	if told, _ := h.notices(); len(told) != 1 {
		t.Fatalf("production told %q, want one notice", told)
	}
}
