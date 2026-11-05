// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// /img from an anonymous admin makes no run, which would be nobody's: the
// chat is told why, as a reply in its topic, even where framy is an admin,
// since an ephemeral message needs a person to go to.
func TestAnAnonymousCommandIsToldWhyNothingIsMade(t *testing.T) {
	h := newHarness(t)
	h.flow.SetAlbums(fakeAlbums{admin: true})
	m := command("/img a fox")
	m.From = &tg.User{ID: 1087968824, IsBot: true, Username: "GroupAnonymousBot"}
	m.IsTopicMessage, m.MessageThreadID = true, 3
	h.flow.Anonymous(context.Background(), m)
	h.flow.Wait()
	sent := h.tg.methods("sendMessage")
	if len(sent) != 1 || sent[0].text != i18n.T("en", "run.anonymous") || sent[0].messageID != 10 || sent[0].thread != 3 || sent[0].ephemeral != nil {
		t.Fatalf("calls %+v", h.tg.calls)
	}
	if len(h.store.jobs) != 0 || len(h.tg.methods("sendRichMessage")) != 0 {
		t.Fatalf("a run started: %d jobs, %+v", len(h.store.jobs), h.tg.calls)
	}
}

// A key pasted after an anonymous /img is as public as anyone's: it is
// deleted, and the chat is told where keys go rather than how to send the
// command.
func TestAKeyInAnAnonymousCommandIsDeleted(t *testing.T) {
	h := newHarness(t)
	m := command("/img " + openRouterKey)
	m.From = &tg.User{ID: 1087968824, IsBot: true, Username: "GroupAnonymousBot"}
	h.flow.Anonymous(context.Background(), m)
	h.flow.Wait()
	if del := h.tg.methods("deleteMessage"); len(del) != 1 || del[0].chatID == 0 || del[0].messageID != 10 {
		t.Fatalf("deletes %+v", del)
	}
	if sent := h.tg.methods("sendMessage"); len(sent) != 1 || sent[0].text != i18n.T("en", "msg.key_deleted") {
		t.Fatalf("calls %+v", h.tg.calls)
	}
	if len(h.store.jobs) != 0 {
		t.Fatalf("%d jobs", len(h.store.jobs))
	}
}
