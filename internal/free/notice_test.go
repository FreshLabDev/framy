// SPDX-License-Identifier: Apache-2.0
package free

import (
	"testing"
	"time"
)

func TestAFloodOfRefusalsLooksIntoTellingTheChatOncePerFusePerDay(t *testing.T) {
	now := time.Date(2026, 10, 27, 23, 59, 0, 0, time.UTC)
	a := &Allowance{told: map[Fuse]time.Time{}, now: func() time.Time { return now }}

	if !a.claimNotice(FuseBudget) {
		t.Fatal("the first refusal of the day does not look into telling the chat")
	}
	for range 50 {
		if a.claimNotice(FuseBudget) {
			t.Fatal("a second refusal on the same day looked into it again")
		}
	}
	if !a.claimNotice(FuseKey) {
		t.Error("the key fuse shares the budget fuse's notice")
	}

	a.releaseNotice(FuseBudget)
	if !a.claimNotice(FuseBudget) {
		t.Error("a notice that failed is not tried again")
	}

	now = now.Add(2 * time.Minute) // 00:01 UTC, the next day
	if !a.claimNotice(FuseBudget) {
		t.Error("the next day does not start with a notice")
	}
}
