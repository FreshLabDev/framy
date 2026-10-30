// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"reflect"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
)

var dm = tg.Chat{ID: 7, Type: "private"}

// said is a plain message of the DM.
func said(text string) *tg.Message {
	return &tg.Message{MessageID: 10, Chat: dm, From: &tg.User{ID: 7, LanguageCode: "en"}, Text: text}
}

// photoSaid is a photo of the DM with a caption.
func photoSaid(caption string) *tg.Message {
	m := said("")
	m.Caption = caption
	m.Photo = []tg.Photo{{FileID: "small", Width: 90, Height: 60}, {FileID: "big", Width: 1280, Height: 853, FileSize: 200_000}}
	return m
}

func dmHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	withHeyGen(h)
	return h
}

// ask works a plain message and returns the question it got, or 0.
func (h *harness) ask(t *testing.T, m *tg.Message) int64 {
	t.Helper()
	before := len(h.tg.methods("sendMessage"))
	if err := h.flow.plain(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.methods("sendMessage")) == before {
		return 0
	}
	return h.tg.next
}

// answer presses an answer of the question.
func (h *harness) answer(question int64, data string, from int64, reply *tg.Message) error {
	cq := &tg.CallbackQuery{ID: "cq", From: tg.User{ID: from, LanguageCode: "en"}, Data: data,
		Message: tg.Message{MessageID: question, Chat: dm, Date: 1, Text: "?", ReplyToMessage: reply}}
	return h.flow.answerAsk(context.Background(), cq)
}

func TestWordsInADirectChatAreAskedAbout(t *testing.T) {
	h := dmHarness(t)
	q := h.ask(t, said("a fox in the snow"))
	sent := h.tg.methods("sendMessage")
	if q == 0 || len(sent) != 1 || sent[0].messageID != 10 || sent[0].chatID != 7 || sent[0].text != i18n.T("en", "dm.ask") {
		t.Fatalf("question = %+v", sent)
	}
	kb := sent[0].markup.InlineKeyboard
	if len(kb) != 1 || len(kb[0]) != 2 || kb[0][0].CallbackData != "d:10:i" || kb[0][1].CallbackData != "d:10:v" ||
		kb[0][0].Text != i18n.T("en", "dm.image") || kb[0][1].Text != i18n.T("en", "dm.video") {
		t.Fatalf("buttons = %+v", kb)
	}
	if len(h.store.jobs) != 0 {
		t.Fatalf("%d jobs", len(h.store.jobs))
	}
	h.tg.reset()

	if err := h.answer(q, "d:10:i", 7, nil); err != nil {
		t.Fatal(err)
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != q {
		t.Fatalf("the question stayed: %+v", d)
	}
	job := h.store.jobs[1]
	if job == nil || !job.n.Free || job.n.Offer.ID != free.ImageMAI.Offer || job.n.Prompt != "a fox in the snow" || job.n.DedupeKey != "tg:7:10" {
		t.Fatalf("job = %+v", job)
	}
	if p := h.tg.methods("sendPhoto"); len(p) != 1 || p[0].messageID != 10 || p[0].ephemeral != nil {
		t.Fatalf("preview = %+v", p)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "" {
		t.Fatalf("answers = %v", h.tg.answers)
	}

	// The same update again, as after a restart, asks nothing.
	h.tg.reset()
	if q := h.ask(t, said("a fox in the snow")); q != 0 || len(h.tg.calls) != 0 {
		t.Fatalf("asked again: %+v", h.tg.calls)
	}
}

// Video makes the photo the start frame of the free video.
func TestVideoMakesTheFreeVideoOfThePhoto(t *testing.T) {
	h := dmHarness(t)
	q := h.ask(t, photoSaid("make it rain"))
	if err := h.answer(q, "d:10:v", 7, nil); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	want := map[string]any{"duration": 5, "resolution": "768p", "frame_images": []string{"first_frame"}}
	if job == nil || !job.n.Free || job.n.Offer.ID != free.VideoHeyGen.Offer || !reflect.DeepEqual(job.n.Settings, want) ||
		len(job.n.Inputs) != 1 || job.n.Prompt != "make it rain" || job.n.Estimate == nil || job.n.Estimate.USD <= 0 {
		t.Fatalf("job = %+v", job)
	}
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "big" {
		t.Fatalf("getFile = %+v", got)
	}
	if got := h.tg.methods("sendChatAction"); len(got) != 1 || got[0].action != "upload_video" {
		t.Fatalf("chat action = %+v", got)
	}
	p := h.renderer.previews[0]
	if p.Model != "HeyGen Video" || !p.Free || p.Left != 1 || !reflect.DeepEqual(p.Chips, []string{"0:05", "768p"}) {
		t.Fatalf("card = %+v", p)
	}
	r := run(t, h)
	if r.Kind != "video" || r.Taken != 1 || r.CommandMessageID != 10 || r.ChatID != 7 {
		t.Fatalf("run = %+v", r)
	}
	if got := lastCard(t, h); !reflect.DeepEqual(got, [][]string{{callback(1, actGenerate), callback(1, actNo)}, {callback(1, actImage)}}) {
		t.Fatalf("keyboard = %v", got)
	}
}

// After a restart the question still answers: its reply is the message.
func TestAQuestionAnswersAfterARestart(t *testing.T) {
	h := dmHarness(t)
	m := said("a fox")
	q := h.ask(t, m)
	h.flow.asks.Clear()
	if err := h.answer(q, "d:10:i", 7, m); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job == nil || job.n.Prompt != "a fox" {
		t.Fatalf("job = %+v", job)
	}
}

// A question whose message is gone, or an answer that is not the person's
// or not about that message, starts nothing.
func TestAQuestionWithoutItsMessageExpires(t *testing.T) {
	expired := "alert:" + i18n.T("en", "dm.expired")
	for name, c := range map[string]struct {
		data  string
		from  int64
		reply *tg.Message
		clear bool
	}{
		"gone":            {"d:10:i", 7, nil, true},
		"another message": {"d:11:i", 7, nil, false},
		"another person":  {"d:10:v", 8, nil, false},
		"a forged reply":  {"d:10:i", 7, &tg.Message{MessageID: 10, Chat: dm, From: &tg.User{ID: 8}, Text: "x"}, true},
	} {
		h := dmHarness(t)
		q := h.ask(t, said("a fox"))
		if c.clear {
			h.flow.asks.Clear()
		}
		h.tg.reset()
		if err := h.answer(q, c.data, c.from, c.reply); err != nil {
			t.Fatal(err)
		}
		if len(h.tg.answers) != 1 || h.tg.answers[0] != expired || len(h.store.jobs) != 0 {
			t.Errorf("%s: answers %v, jobs %d", name, h.tg.answers, len(h.store.jobs))
		}
		if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != q {
			t.Errorf("%s: the question stayed: %+v", name, d)
		}
	}
}

func TestAnswerDataIsRead(t *testing.T) {
	for data, want := range map[string]bool{
		"d:10:i": true, "d:10:v": true, "d:10:x": false, "d:0:i": false, "d:-1:i": false,
		"d:10": false, "d:a:i": false, "j:10:i": false, "d:10:i:": false,
	} {
		if _, _, ok := parseAsk(data); ok != want {
			t.Errorf("%q: %v", data, ok)
		}
	}
	// In a group there is no question to answer.
	h := dmHarness(t)
	cq := &tg.CallbackQuery{ID: "cq", From: tg.User{ID: 7}, Data: "d:10:i", Message: tg.Message{MessageID: 11, Chat: group, Date: 1}}
	if err := h.flow.answerAsk(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.calls) != 0 || len(h.tg.answers) != 1 || h.tg.answers[0] != "" {
		t.Fatalf("calls %+v, answers %v", h.tg.calls, h.tg.answers)
	}
}

// What can go only one way goes there at once; media without words gets a
// line on what to send; a sticker gets nothing.
func TestWhatCanGoOneWayIsNotAskedAbout(t *testing.T) {
	clip := said("")
	clip.Caption = "make it night"
	clip.Video = &tg.Video{FileID: "clip", Width: 1280, Height: 720, Duration: 4, FileSize: 3 << 20}
	bare := photoSaid("")
	bareClip := said("")
	bareClip.Video = clip.Video
	sticker := said("")
	sticker.Sticker = &tg.Sticker{FileID: "s"}
	hint := i18n.T("en", "dm.say_what")

	h := dmHarness(t)
	withFlux(h)
	if q := h.ask(t, clip); q != 0 {
		t.Fatal("a clip was asked about")
	}
	if job := h.store.jobs[1]; job == nil || job.n.Offer.ID != free.VideoEdit.Offer || job.n.Prompt != "make it night" {
		t.Fatalf("job = %+v", job)
	}
	for name, m := range map[string]*tg.Message{"a photo": bare, "a clip": bareClip} {
		h := dmHarness(t)
		_ = h.ask(t, m)
		if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != hint || got[0].markup != nil || len(h.store.jobs) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	h = dmHarness(t)
	if q := h.ask(t, sticker); q != 0 || len(h.tg.calls) != 0 {
		t.Fatalf("a sticker: %+v", h.tg.calls)
	}
}

// An album is answered once, about its item with words, and goes to /img
// since a video takes one start frame.
func TestAnAlbumOfADirectChatIsAnsweredOnce(t *testing.T) {
	items := []db.AlbumItem{
		{MessageID: 20, Kind: db.AlbumPhoto, FileID: "pa"}, {MessageID: 21, Kind: db.AlbumPhoto, FileID: "pb"}, {MessageID: 22, Kind: db.AlbumPhoto, FileID: "pc"},
	}
	for _, caption := range []string{"one style", ""} {
		h := dmHarness(t)
		gate := make(chan struct{})
		h.flow.SetAlbums(fakeAlbums{items: items, admin: true, gate: gate})
		for i, it := range items {
			m := photoSaid("")
			m.MessageID, m.MediaGroupID = it.MessageID, "g1"
			if i == 1 {
				m.Caption = caption
			}
			h.flow.Plain(context.Background(), m)
		}
		close(gate)
		h.flow.Wait()
		if caption == "" {
			if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "dm.say_what") || got[0].messageID != 20 {
				t.Fatalf("no words: %+v", got)
			}
			continue
		}
		job := h.store.jobs[1]
		if len(h.store.jobs) != 1 || len(job.n.Inputs) != 3 || job.n.Prompt != "one style" || job.n.DedupeKey != "tg:7:21" {
			t.Fatalf("jobs %d, job %+v", len(h.store.jobs), job)
		}
		if p := h.tg.methods("sendPhoto"); len(p) != 1 || p[0].messageID != 21 {
			t.Fatalf("preview = %+v", p)
		}
		// An item that comes late starts nothing.
		late := photoSaid("")
		late.MessageID, late.MediaGroupID = 23, "g1"
		h.flow.Plain(context.Background(), late)
		h.flow.Wait()
		if len(h.store.jobs) != 1 || len(h.tg.methods("sendMessage")) != 0 {
			t.Fatalf("the late item: jobs %d, %+v", len(h.store.jobs), h.tg.methods("sendMessage"))
		}
	}
}

// A reply to framy's own preview or question takes none of its pictures;
// a reply to a result is about the result.
func TestAReplyToFramyTakesOnlyAResult(t *testing.T) {
	bot := &tg.User{ID: 1, IsBot: true}
	h := dmHarness(t)
	m := said("now at night")
	m.ReplyToMessage = &tg.Message{MessageID: 9, Chat: dm, From: bot, Photo: []tg.Photo{{FileID: "preview-file"}}}
	q := h.ask(t, m)
	if err := h.answer(q, "d:10:i", 7, nil); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job == nil || len(job.n.Inputs) != 0 || len(h.tg.methods("getFile")) != 0 {
		t.Fatalf("job = %+v", job)
	}

	h = dmHarness(t)
	h.store.runs[5] = &db.ChatRun{JobID: 5, ChatID: 7, Stage: db.StageDone, ResultMessageID: 9}
	m.ReplyToMessage = &tg.Message{MessageID: 9, Chat: dm, From: bot, Photo: []tg.Photo{{FileID: "result-file"}}}
	q = h.ask(t, m)
	if err := h.answer(q, "d:10:i", 7, nil); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job == nil || len(job.n.Inputs) != 1 {
		t.Fatalf("job = %+v", job)
	}
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "result-file" {
		t.Fatalf("getFile = %+v", got)
	}
}

// Plain is for a person in a DM only.
func TestPlainIsForADirectChat(t *testing.T) {
	h := dmHarness(t)
	inGroup := said("a fox")
	inGroup.Chat = group
	fromBot := said("a fox")
	fromBot.From.IsBot = true
	for _, m := range []*tg.Message{inGroup, fromBot, nil} {
		h.flow.Plain(context.Background(), m)
	}
	h.flow.Wait()
	if len(h.tg.calls) != 0 {
		t.Fatalf("calls %+v", h.tg.calls)
	}
	h.flow.Plain(context.Background(), said("a fox"))
	h.flow.Wait()
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "dm.ask") {
		t.Fatalf("question = %+v", got)
	}
}
