// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"testing"

	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
)

// "@framyin_bot a fox" in a group is /img a fox; in reply to a video it is
// /edit, since that is what the words can mean there. Only the mention gets
// the hint of a mention, not of /img.
func TestAMentionIsACommandByName(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	if err := h.flow.byName(ctx, photoReply("@framytest_bot make it snow"), "make it snow"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || job.n.Prompt != "make it snow" || len(job.n.Inputs) != 1 || len(h.tg.methods("sendRichMessage")) != 1 {
		t.Fatalf("image: job %+v, calls %+v", job, h.tg.calls)
	}

	h = videoHarness(t)
	if err := h.flow.byName(ctx, videoReply("@framytest_bot make it night"), "make it night"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job == nil || job.n.Offer.ID != free.VideoEdit.Offer || job.n.Prompt != "make it night" {
		t.Fatalf("video: job %+v", job)
	}

	h = newHarness(t)
	if err := h.flow.byName(ctx, command("@framytest_bot"), ""); err != nil {
		t.Fatal(err)
	}
	got := h.tg.methods("sendMessage")
	if len(h.store.jobs) != 0 || len(got) != 1 || got[0].text != i18n.T("en", "guest.empty") || got[0].messageID != 10 {
		t.Fatalf("empty: %+v", got)
	}
}
