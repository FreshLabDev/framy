// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/storage"
)

// framyUser is framy as the sender of its results.
var framyUser = &tg.User{ID: 999, IsBot: true, Username: "framyin_bot"}

// replyTo is words of a member of the group answering message id of
// framy's, which shows a picture.
func replyTo(id, from int64, text string) *tg.Message {
	return &tg.Message{MessageID: 20, Chat: group, From: &tg.User{ID: from, LanguageCode: "en"}, Text: text,
		ReplyToMessage: &tg.Message{MessageID: id, Chat: group, From: framyUser, Photo: []tg.Photo{{FileID: "result-file", Width: 1024, Height: 1024}}}}
}

// replyHarness has a result of person 7's in a group where framy is an
// admin, and returns its run and its message.
func replyHarness(t *testing.T) (*harness, int64, int64) {
	t.Helper()
	h, id, result := animateHarness(t)
	h.flow.SetAlbums(fakeAlbums{admin: true})
	return h, id, result
}

// Words in reply to one's own result, where framy is an admin, change the
// result as /img in reply to it would: the preview goes to that person
// alone, and the chat sees nothing of it, not even framy sending a picture.
func TestWordsToOnesOwnResultChangeIt(t *testing.T) {
	h, _, result := replyHarness(t)
	if err := h.flow.answerReply(context.Background(), replyTo(result, 7, "@framyin_bot make it brighter")); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[2]
	if job == nil || !job.n.Free || job.n.Offer.ID != free.ImageMAI.Offer || job.n.Prompt != "make it brighter" || len(job.n.Inputs) != 1 ||
		job.n.DedupeKey != "tg:-100:20" {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].ephemeral == nil || sent[0].ephemeral.ReceiverUserID != 7 || sent[0].messageID != 20 {
		t.Fatalf("preview = %+v", sent)
	}
	if a := h.tg.methods("sendChatAction"); len(a) != 0 {
		t.Fatalf("chat action = %+v", a)
	}
	if r, err := h.store.ChatRun(context.Background(), 2); err != nil || !r.PreviewEphemeral || r.CommandMessageID != 20 || r.UserID != 7 {
		t.Fatalf("run = %+v, %v", r, err)
	}
}

// Everything else stays unanswered, as before: words to someone else's
// result, to a message of framy's that is no result, a sticker, and any
// reply where framy is no admin, since its preview would be the chat's.
func TestOtherRepliesStayUnanswered(t *testing.T) {
	for name, c := range map[string]struct {
		admin bool
		m     func(result int64) *tg.Message
	}{
		"someone else's result": {true, func(r int64) *tg.Message { return replyTo(r, 8, "make it brighter") }},
		"a preview":             {true, func(r int64) *tg.Message { return replyTo(r+100, 7, "make it brighter") }},
		"a sticker": {true, func(r int64) *tg.Message {
			m := replyTo(r, 7, "")
			m.Sticker = &tg.Sticker{FileID: "s"}
			return m
		}},
		"no admin": {false, func(r int64) *tg.Message { return replyTo(r, 7, "make it brighter") }},
	} {
		h, _, result := animateHarness(t)
		h.flow.SetAlbums(fakeAlbums{admin: c.admin})
		if err := h.flow.answerReply(context.Background(), c.m(result)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(h.store.jobs) != 1 || len(h.tg.calls) != 0 {
			t.Errorf("%s: jobs %d, calls %+v", name, len(h.store.jobs), h.tg.calls)
		}
	}
}

// A run a reply started says nothing the chat would see: a refusal is
// left unsaid, and a preview Telegram will not show the person alone is
// not shown to everyone instead.
func TestAReplysRunIsNeverTheChats(t *testing.T) {
	h, _, result := replyHarness(t)
	h.queue.refuse = &free.LimitError{Kind: catalog.Image, Limit: 5, FreesAt: clock.Add(3 * time.Hour)}
	if err := h.flow.answerReply(context.Background(), replyTo(result, 7, "make it brighter")); err != nil {
		t.Fatal(err)
	}
	if toldTheChat(h) {
		t.Fatalf("calls = %+v", h.tg.calls)
	}

	h, _, result = replyHarness(t)
	h.tg.fail["ephemeral"] = errEphemeral
	if err := h.flow.answerReply(context.Background(), replyTo(result, 7, "make it brighter")); err != nil {
		t.Fatal(err)
	}
	if toldTheChat(h) {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if len(h.queue.cancelled) != 1 {
		t.Fatalf("cancelled = %v", h.queue.cancelled)
	}
}

// Words to a video result edit the video, as /edit in reply to it would.
func TestWordsToAVideoResultEditIt(t *testing.T) {
	h, _, result := replyHarness(t)
	m := replyTo(result, 7, "make it slower")
	m.ReplyToMessage.Photo = nil
	m.ReplyToMessage.Video = &tg.Video{FileID: "clip", FileSize: 1 << 20, Duration: 4}
	h.files.results[1] = []storage.Asset{{ID: 60, ContentType: "video/mp4"}}
	if err := h.flow.answerReply(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[2]; job == nil || job.n.Offer.ID != free.VideoEdit.Offer || job.n.Prompt != "make it slower" {
		t.Fatalf("job = %+v", job)
	}
}

// toldTheChat tells whether framy sent the chat anything: a message, a
// picture or a chat action.
func toldTheChat(h *harness) bool {
	for _, c := range h.tg.calls {
		switch c.method {
		case "getFile", "download":
		default:
			return true
		}
	}
	return false
}
