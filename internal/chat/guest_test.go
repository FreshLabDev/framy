// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// guestChat is a chat framy is not in. Its id is one framy may know as
// another chat.
var guestChat = tg.Chat{ID: -100, Type: "supergroup"}

// guestReply is "@framyin_bot <prompt>" from person 7 in reply to a photo,
// as Telegram sends it, with the mention taken out by the bot.
func guestReply(query string) *tg.Message {
	m := photoReply("")
	m.MessageID, m.Chat, m.GuestQueryID = 0, guestChat, query
	return m
}

func guestHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.flow.cfg.AppURL = shareSite
	h.renderer.pictures = true
	return h
}

// guestRun answers a guest message and returns its run.
func (h *harness) guestRun(t *testing.T, prompt string) db.ChatRun {
	t.Helper()
	if err := h.flow.guest(context.Background(), guestReply("gq-1"), prompt); err != nil {
		t.Fatal(err)
	}
	run, err := h.store.ChatRun(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// pressInline presses a button of an inline message, as a guest run's
// presses come: no message, the inline message's id.
func (h *harness) pressInline(jobID, from int64, action, inline string) error {
	cq := &tg.CallbackQuery{ID: "cq", From: tg.User{ID: from, LanguageCode: "en"}, Data: callback(jobID, action), InlineMessageID: inline}
	return h.flow.press(context.Background(), cq)
}

// chatCalls are the calls that would reach the chat itself, which framy is
// not in. Reading the photo is not one.
func (h *harness) chatCalls() []call {
	var out []call
	for _, c := range h.tg.calls {
		switch c.method {
		case "answerGuestQuery", "answerCallbackQuery", "getFile", "download":
			continue
		}
		if c.inline == "" {
			out = append(out, c)
		}
	}
	return out
}

func isShareLink(link, ext string) bool {
	return strings.HasPrefix(link, shareSite+SharePath) && strings.Contains(link, ext+"?v=")
}

// A guest message is answered once with the preview a group gets, as text:
// the prompt and the price, Generate and No, and above them the card
// Telegram fetches from framy as the link's preview. Nothing goes to the
// chat itself, and the run waits on that answer.
func TestAGuestMessageIsAnsweredWithItsPreview(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	if len(h.tg.guestAnswers) != 1 {
		t.Fatalf("answers = %+v", h.tg.guestAnswers)
	}
	a, ok := h.tg.guestAnswers[0].(tg.InlineQueryResultArticle)
	text, _ := a.InputMessageContent.(tg.InputTextMessageContent)
	if !ok || !strings.Contains(text.MessageText, "make it snow") || a.ReplyMarkup == nil || a.ReplyMarkup.InlineKeyboard[0][0].CallbackData != callback(1, actGenerate) {
		t.Fatalf("answer = %+v", h.tg.guestAnswers[0])
	}
	if p := text.LinkPreview; p == nil || !isShareLink(p.URL, ".jpg") || !p.PreferLargeMedia || !p.ShowAboveText {
		t.Fatalf("preview = %+v", p)
	}
	if run.InlineMessageID != "inline-gq-1" || run.Stage != db.StagePreview || run.Lang != "en" {
		t.Fatalf("run = %+v", run)
	}
	if s := h.store.jobs[1].n.Surface; s != jobs.Guest {
		t.Errorf("surface = %s", s)
	}
	if c := h.chatCalls(); len(c) != 0 {
		t.Errorf("sent to the chat: %+v", c)
	}
	// Telegram fetches the card: framy draws it from the run, with the
	// photo, and serves it as a JPEG.
	before := len(h.renderer.previews)
	jpegSize(t, h.fetch(t, text.LinkPreview.URL))
	if len(h.renderer.previews) != before+1 || len(h.renderer.albums[before]) != 1 {
		t.Errorf("cards drawn %d, of %v", len(h.renderer.previews)-before, h.renderer.albums)
	}
}

// Generate, the time and the result are edits of the one answer: the loop
// by a link, Stop under it, then the picture.
func TestAGuestRunIsOneInlineMessage(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	h.tg.reset()
	if err := h.pressInline(1, 7, actGenerate, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	loop := h.tg.methods("editInlineMessageMedia")
	if len(loop) != 1 || loop[0].inline != run.InlineMessageID || !strings.HasPrefix(loop[0].file, "animation ") ||
		!isShareLink(strings.TrimPrefix(loop[0].file, "animation "), ".mp4") || !isStop(loop[0].markup, 1) {
		t.Fatalf("loop = %+v", loop)
	}
	rec := h.fetch(t, strings.TrimPrefix(loop[0].file, "animation "))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("loop link: %d %v", rec.Code, rec.Header())
	}

	h.tg.reset()
	h.store.setStatus(1, jobs.Running, "")
	h.now = h.now.Add(11 * time.Second)
	h.flow.step(context.Background(), 1)
	caps := h.tg.methods("editInlineMessageCaption")
	if len(caps) != 1 || !strings.Contains(caps[0].text, "0:11") || !isStop(caps[0].markup, 1) {
		t.Fatalf("captions = %+v", caps)
	}

	h.tg.reset()
	h.store.setStatus(1, jobs.Done, "")
	h.files.results[1] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(40 * time.Second)
	h.flow.step(context.Background(), 1)
	res := h.tg.methods("editInlineMessageMedia")
	if len(res) != 1 || !strings.HasPrefix(res[0].file, "photo "+shareSite+SharePath) || !strings.HasSuffix(res[0].file, ".jpg") ||
		res[0].markup != nil || res[0].text != resultCaption("make it snow", "MAI Image 2.6 Flash") {
		t.Fatalf("result = %+v", res)
	}
	if h.store.stage(1) != db.StageDone || len(h.chatCalls()) != 0 {
		t.Errorf("stage %s, chat calls %+v", h.store.stage(1), h.chatCalls())
	}
}

// Only the one who summoned framy presses, and only on the run's own
// answer.
func TestAGuestRunTakesPressesFromItsAnswerAlone(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	h.tg.reset()
	if err := h.pressInline(1, 7, actGenerate, "another-inline"); err != nil {
		t.Fatal(err)
	}
	if err := h.pressInline(1, 8, actGenerate, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	// A press of a chat's message names no guest run.
	if err := h.pressOn(1, 7, actGenerate, tg.Message{MessageID: 0, Chat: guestChat}); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.methods("editInlineMessageMedia")) != 0 || h.store.stage(1) != db.StagePreview {
		t.Fatalf("calls %+v", h.tg.calls)
	}
	if len(h.tg.answers) != 3 || h.tg.answers[1] != "alert:"+i18n.T("en", "run.not_yours") {
		t.Errorf("answers = %q", h.tg.answers)
	}
}

// An inline message cannot be deleted: No and an expired preview leave the
// answer one line, Cancelled, with no card and no buttons.
func TestAGuestPreviewThatEndsSaysCancelled(t *testing.T) {
	for name, end := range map[string]func(h *harness, run db.ChatRun){
		"No": func(h *harness, run db.ChatRun) {
			if err := h.pressInline(1, 7, actNo, run.InlineMessageID); err != nil {
				t.Fatal(err)
			}
		},
		"expired": func(h *harness, _ db.ChatRun) {
			h.store.setStatus(1, jobs.Cancelled, "")
			h.flow.step(context.Background(), 1)
		},
	} {
		h := guestHarness(t)
		run := h.guestRun(t, "make it snow")
		h.tg.reset()
		end(h, run)
		texts := h.tg.methods("editInlineMessageText")
		if len(texts) != 1 || texts[0].inline != run.InlineMessageID || texts[0].text != i18n.T("en", "guest.cancelled") ||
			texts[0].preview != "" || texts[0].markup != nil || len(h.tg.calls) != len(texts)+len(h.tg.methods("answerCallbackQuery")) {
			t.Errorf("%s: calls = %+v", name, h.tg.calls)
		}
		if h.store.stage(1) != db.StageGone || len(h.chatCalls()) != 0 {
			t.Errorf("%s: stage %s, chat calls %+v", name, h.store.stage(1), h.chatCalls())
		}
	}
}

// Stop on the loop cannot take the answer back to text: it gets the card
// the run started from, Stopped, and no buttons.
func TestAStoppedGuestRunLeavesItsCard(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	if err := h.pressInline(1, 7, actGenerate, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.pressInline(1, 7, actStop, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	cards := h.tg.methods("editInlineMessageMedia")
	if len(cards) != 1 || !isShareLink(strings.TrimPrefix(cards[0].file, "photo "), ".jpg") || cards[0].markup != nil ||
		!strings.Contains(cards[0].text, "make it snow") || !strings.HasSuffix(cards[0].text, i18n.T("en", "guest.stopped")) {
		t.Fatalf("cards = %+v", cards)
	}
	if h.store.stage(1) != db.StageGone || len(h.chatCalls()) != 0 {
		t.Errorf("stage %s, chat calls %+v", h.store.stage(1), h.chatCalls())
	}
}

// When Telegram takes no loop, the answer gets the card under the same
// words and Stop; the time then goes in its caption.
func TestAGuestLoopFallsBackToTheCard(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	h.tg.reset()
	h.renderer.loopErr = errBoom
	if err := h.pressInline(1, 7, actGenerate, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	cards := h.tg.methods("editInlineMessageMedia")
	if len(cards) != 1 || !strings.HasPrefix(cards[0].file, "photo ") || !isStop(cards[0].markup, 1) {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}

// Model lists the models on the answer itself, and a pick has Telegram
// fetch the card drawn again.
func TestAGuestRunPicksItsModelOnTheAnswer(t *testing.T) {
	h := guestHarness(t)
	h.store.models = panelCatalog()
	run := h.guestRun(t, "make it snow")
	h.tg.reset()
	if err := h.pressInline(1, 7, actModel, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	kbs := h.tg.methods("editInlineMessageReplyMarkup")
	if len(kbs) != 1 || kbs[0].inline != run.InlineMessageID || len(h.chatCalls()) != 0 {
		t.Fatalf("keyboards %+v, chat calls %+v", kbs, h.chatCalls())
	}
	h.tg.reset()
	if err := h.pressInline(1, 7, pick("black-forest-labs/flux-3-image-klein"), run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	cards := h.tg.methods("editInlineMessageText")
	if len(cards) != 1 || !isShareLink(cards[0].preview, ".jpg") || cards[0].markup == nil || len(h.tg.methods("editInlineMessageMedia")) != 0 {
		t.Fatalf("cards = %+v", cards)
	}
}

// A failure shows the error card, which Telegram fetches as well.
func TestAGuestFailureShowsTheErrorCard(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	if err := h.pressInline(1, 7, actGenerate, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.store.setStatus(1, jobs.Failed, jobs.Moderation)
	h.flow.step(context.Background(), 1)
	cards := h.tg.methods("editInlineMessageMedia")
	if len(cards) != 1 || !isShareLink(strings.TrimPrefix(cards[0].file, "photo "), ".jpg") || cards[0].markup != nil {
		t.Fatalf("cards = %+v", cards)
	}
	jpegSize(t, h.fetch(t, strings.TrimPrefix(cards[0].file, "photo ")))
	if last := h.renderer.cards[len(h.renderer.cards)-1]; last != i18n.T("en", "err.moderation") || h.store.stage(1) != db.StageFailed {
		t.Errorf("card %q, stage %s", last, h.store.stage(1))
	}
}

// A video Telegram cannot fetch, over 20 MB, stays in the history, and the
// answer says so.
func TestAGuestVideoTooBigToFetchStaysInTheHistory(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	if err := h.pressInline(1, 7, actGenerate, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.store.setStatus(1, jobs.Done, "")
	h.files.results[1] = []storage.Asset{{ID: 60, ContentType: "video/mp4", Bytes: 21 << 20, Width: 1280, Height: 720, Seconds: 5}}
	h.flow.step(context.Background(), 1)
	caps := h.tg.methods("editInlineMessageCaption")
	if len(caps) != 1 || !strings.Contains(caps[0].text, i18n.T("en", "guest.elsewhere")) || len(h.tg.methods("editInlineMessageMedia")) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if h.store.stage(1) != db.StageDone {
		t.Errorf("stage %s", h.store.stage(1))
	}
}

// What a guest message cannot start gets one text answer: no prompt, a
// video, the free tier used up without a key. A replayed message, and one
// with no https site to fetch from, get none.
func TestWhatAGuestMessageCannotStartIsSaidOnce(t *testing.T) {
	text := func(h *harness) string {
		t.Helper()
		if len(h.tg.guestAnswers) != 1 {
			t.Fatalf("answers = %+v", h.tg.guestAnswers)
		}
		a, ok := h.tg.guestAnswers[0].(tg.InlineQueryResultArticle)
		if !ok {
			t.Fatalf("answer = %+v", h.tg.guestAnswers[0])
		}
		return a.InputMessageContent.(tg.InputTextMessageContent).MessageText
	}
	ctx := context.Background()

	h := guestHarness(t)
	if err := h.flow.guest(ctx, guestReply("gq-1"), ""); err != nil || text(h) != i18n.T("en", "guest.empty") {
		t.Errorf("no prompt: %v", err)
	}

	h = guestHarness(t)
	m := guestReply("gq-1")
	m.ReplyToMessage.Photo, m.ReplyToMessage.Video = nil, &tg.Video{FileID: "clip", FileSize: 1 << 20}
	if err := h.flow.guest(ctx, m, "make it night"); err != nil || text(h) != i18n.T("en", "guest.video") {
		t.Errorf("a video: %v", err)
	}

	h = guestHarness(t)
	h.queue.refuse = &free.LimitError{Kind: catalog.Image, Limit: 5, Window: free.ImageWindow, FreesAt: h.now.Add(2*time.Hour + 14*time.Minute + 30*time.Second)}
	if err := h.flow.guest(ctx, guestReply("gq-1"), "make it snow"); err != nil || text(h) != i18n.T("en", "run.limit", "limit", "5", "wait", "2 h 15 min") {
		t.Errorf("no free left: %v", err)
	}

	h = guestHarness(t)
	h.guestRun(t, "make it snow")
	if err := h.flow.guest(ctx, guestReply("gq-1"), "make it snow"); err != nil || len(h.tg.guestAnswers) != 1 {
		t.Errorf("a replay: %v, %d answers", err, len(h.tg.guestAnswers))
	}

	h = guestHarness(t)
	h.flow.cfg.AppURL = ""
	if err := h.flow.guest(ctx, guestReply("gq-1"), "make it snow"); err != nil || len(h.tg.calls) != 0 {
		t.Errorf("no site: %v, %+v", err, h.tg.calls)
	}
}

// When Telegram refuses the answer the draft goes, and the run with it.
func TestARefusedGuestAnswerLeavesNoRun(t *testing.T) {
	h := guestHarness(t)
	h.tg.fail["answerGuestQuery"] = errBoom
	if err := h.flow.guest(context.Background(), guestReply("gq-1"), "make it snow"); err == nil {
		t.Fatal("no error")
	}
	if h.store.stage(1) != db.StageGone || len(h.queue.cancelled) != 1 {
		t.Errorf("stage %s, cancelled %v", h.store.stage(1), h.queue.cancelled)
	}
}

// Make a video revises the draft as in a group, and the answer gets the
// card of the video.
func TestAGuestRunTurnsIntoAVideoOnTheAnswer(t *testing.T) {
	h := guestHarness(t)
	withHeyGen(h)
	run := h.guestRun(t, "make it snow")
	h.tg.reset()
	if err := h.pressInline(1, 7, actVideo, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; !job.n.Free || job.n.Offer.ID != free.VideoHeyGen.Offer {
		t.Fatalf("job = %+v", job.n)
	}
	cards := h.tg.methods("editInlineMessageText")
	if len(cards) != 1 || !isShareLink(cards[0].preview, ".jpg") || len(h.chatCalls()) != 0 {
		t.Fatalf("cards %+v, chat calls %+v", cards, h.chatCalls())
	}
	// The card is drawn when Telegram fetches it, as the run is now.
	jpegSize(t, h.fetch(t, cards[0].preview))
	if p := h.renderer.previews[len(h.renderer.previews)-1]; p.Model != "HeyGen Video" {
		t.Fatalf("card = %+v", p)
	}
}
