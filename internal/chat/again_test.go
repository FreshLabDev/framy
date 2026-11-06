// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/storage"
)

// onResult presses a button under the run's result, as press qid.
func (h *harness) onResult(id, from int64, action, qid string) error {
	run := h.store.runs[id]
	cq := &tg.CallbackQuery{ID: qid, From: tg.User{ID: from, LanguageCode: "en"}, Data: callback(id, action),
		Message: &tg.Message{MessageID: run.ResultMessageID, Chat: group, Date: 1, Photo: []tg.Photo{{FileID: "result-file"}}}}
	return h.flow.press(context.Background(), cq)
}

// onCard presses a button of the run's error card, as press qid.
func (h *harness) onCard(id, from int64, action, qid string) error {
	run := h.store.runs[id]
	cq := &tg.CallbackQuery{ID: qid, From: tg.User{ID: from, LanguageCode: "en"}, Data: callback(id, action),
		Message: &tg.Message{MessageID: run.PreviewMessageID, Chat: group, Date: 1, Photo: []tg.Photo{{FileID: "card-file"}}}}
	return h.flow.press(context.Background(), cq)
}

// failedRun is a run of /img that failed as class, its card shown and the
// calls forgotten.
func failedRun(t *testing.T, class jobs.Class) (*harness, int64) {
	t.Helper()
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Failed, class)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageFailed {
		t.Fatalf("stage %s", h.store.stage(id))
	}
	h.tg.reset()
	return h, id
}

// Again under a result makes a new draft of the run: its prompt, its files,
// its model and its settings, on the free tier as it was. The preview
// answers the result and, in a group, goes to the requester alone.
func TestAgainMakesANewDraftOfTheRun(t *testing.T) {
	h := newHarness(t)
	if err := h.command(photoReply("/img make it snow"), CommandImage, "make it snow"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	result := h.delivered(t, 1, picture)
	if err := h.onResult(1, 7, actAgain, "cq-1"); err != nil {
		t.Fatal(err)
	}
	first, job := h.store.jobs[1], h.store.jobs[2]
	if job == nil || job.status != jobs.Draft || !job.n.Free || job.n.Offer.ID != first.n.Offer.ID ||
		!reflect.DeepEqual(job.n.Settings, first.n.Settings) || !reflect.DeepEqual(job.n.Inputs, first.n.Inputs) ||
		job.n.Prompt != "make it snow" || job.n.DedupeKey != "tg:cb:cq-1" || job.n.MessageID != result || len(job.n.Inputs) != 1 {
		t.Fatalf("job = %+v, first %+v", job, first)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].messageID != result || sent[0].ephemeral == nil ||
		*sent[0].ephemeral != (tg.Ephemeral{ReceiverUserID: 7, CallbackQueryID: "cq-1"}) ||
		strings.Contains(sent[0].text, "<details open>") {
		t.Fatalf("preview = %+v", sent)
	}
	if paths := h.renderer.albums[len(h.renderer.albums)-1]; len(paths) != 1 {
		t.Fatalf("the card's pictures: %v", paths)
	}
	r, err := h.store.ChatRun(context.Background(), 2)
	if err != nil || r.CommandMessageID != result || !r.PreviewEphemeral || r.Taken != 1 || r.UserID != 7 || r.Stage != db.StagePreview {
		t.Fatalf("run = %+v, %v", r, err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "" || h.store.stage(1) != db.StageDone {
		t.Fatalf("answers %v, stage %s", h.tg.answers, h.store.stage(1))
	}
	// A double tap is one press; one a while later is another draft.
	if err := h.onResult(1, 7, actAgain, "cq-2"); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(10 * time.Second)
	if err := h.onResult(1, 7, actAgain, "cq-3"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 3 {
		t.Fatalf("jobs %d", len(h.store.jobs))
	}
}

// A run on the person's key goes again on it only while the key is there.
func TestAgainOnAKeyRunNeedsTheKey(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.jobs[id].n.Free = false
	h.delivered(t, id, picture)
	if err := h.onResult(id, 7, actAgain, "cq-1"); err != nil {
		t.Fatal(err)
	}
	want := "alert:" + i18n.T("en", "run.need_key", "provider", "OpenRouter")
	if len(h.tg.answers) != 1 || h.tg.answers[0] != want || len(h.store.jobs) != 1 {
		t.Fatalf("answers %v, jobs %d", h.tg.answers, len(h.store.jobs))
	}
	withKey(h)
	h.tg.reset()
	if err := h.onResult(id, 7, actAgain, "cq-2"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[2]; job == nil || job.n.Free {
		t.Fatalf("job = %+v", job)
	}
}

// Again on a free run past the free tier, with a key only for another
// provider than the free model's, goes on a model that key runs, as a
// command's draft does: the preview shows it and its price before anything
// runs.
func TestAgainPastTheFreeTierGoesOnAnotherProvidersKey(t *testing.T) {
	h := newHarness(t)
	h.store.models = panelCatalog()
	if err := h.command(photoReply("/img make it snow"), CommandImage, "make it snow"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.delivered(t, 1, picture)
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.Gemini, Status: keys.Valid}}}
	h.queue.refuse = &free.LimitError{Kind: catalog.Image, Limit: 5, Window: free.ImageWindow, FreesAt: clock.Add(time.Hour)}
	h.tg.reset()
	if err := h.onResult(1, 7, actAgain, "cq-1"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[2]
	if job == nil || job.status != jobs.Draft || job.n.Free || job.n.Offer.ID != "gemini:google/nano-banana-pro" || job.n.Estimate == nil {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || !strings.Contains(sent[0].text, "Model: <b>Nano Banana Pro</b>") {
		t.Fatalf("preview = %+v", sent)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "" {
		t.Fatalf("answers = %v", h.tg.answers)
	}
}

// A file of the run deleted on the site since leaves nothing to run again.
func TestAgainWithoutTheFilesMakesNothing(t *testing.T) {
	h := newHarness(t)
	if err := h.command(photoReply("/img make it snow"), CommandImage, "make it snow"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.delivered(t, 1, picture)
	h.store.jobs[1].n.Inputs = nil
	if err := h.onResult(1, 7, actAgain, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "alert:"+i18n.T("en", "result.gone") || len(h.store.jobs) != 1 {
		t.Fatalf("answers %v, jobs %d", h.tg.answers, len(h.store.jobs))
	}
}

// Again is on the result: from the error card, someone else's press, or a
// run whose result is elsewhere, it makes nothing.
func TestAgainFromAnotherMessageMakesNothing(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	result := h.delivered(t, id, picture)
	if err := h.onResult(id, 8, actAgain, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "alert:"+i18n.T("en", "run.not_yours") {
		t.Fatalf("answers %v", h.tg.answers)
	}
	h.tg.reset()
	for _, msg := range []tg.Message{
		{MessageID: result + 1, Chat: group, Date: 1},
		{EphemeralMessageID: result, Chat: group, Date: 1},
	} {
		if err := h.pressOn(id, 7, actAgain, msg); err != nil {
			t.Fatal(err)
		}
	}
	// Try again is the error card's, and this run is done.
	if err := h.onResult(id, 7, actRetry, "cq-2"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 1 || len(h.tg.methods("sendRichMessage")) != 0 {
		t.Fatalf("jobs %d, calls %+v", len(h.store.jobs), h.tg.calls)
	}
}

// Try again on the error card makes a new draft that answers the card.
func TestTryAgainAnswersTheErrorCard(t *testing.T) {
	h, id := failedRun(t, jobs.Internal)
	card := h.store.runs[id].PreviewMessageID
	if err := h.onCard(id, 7, actRetry, "cq-1"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[2]
	if job == nil || job.status != jobs.Draft || job.n.Prompt != "a fox" || job.n.Offer.ID != h.store.jobs[id].n.Offer.ID || job.n.MessageID != card {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].messageID != card || strings.Contains(sent[0].text, "<details open>") {
		t.Fatalf("preview = %+v", sent)
	}
	if r := h.store.runs[2]; r == nil || r.CommandMessageID != card {
		t.Fatalf("run = %+v", r)
	}
	// Again is the result's, and this run failed.
	h.tg.reset()
	h.now = h.now.Add(10 * time.Second)
	if err := h.onCard(id, 7, actAgain, "cq-2"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 2 {
		t.Fatalf("jobs %d", len(h.store.jobs))
	}
}

// Another model makes the same draft with the list of models open.
func TestAnotherModelOpensTheList(t *testing.T) {
	h := newHarness(t)
	h.store.models = richCatalog()
	id := h.generating(t)
	h.store.setStatus(id, jobs.Failed, jobs.Faces)
	h.flow.step(context.Background(), id)
	h.tg.reset()
	if err := h.onCard(id, 7, actOther, "cq-1"); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || !strings.Contains(sent[0].text, "<details open><summary>"+i18n.T("en", "run.model")) {
		t.Fatalf("preview = %+v", sent)
	}
	if h.store.jobs[2] == nil {
		t.Fatal("no draft")
	}
}

// A model that refused its own sound goes again without it.
func TestTryWithoutSound(t *testing.T) {
	h := newHarness(t)
	withKey(h)
	id := h.generating(t)
	j := h.store.jobs[id]
	j.n.Free = false
	j.n.Settings = map[string]any{"generate_audio": true}
	h.store.setStatus(id, jobs.Failed, jobs.Sound)
	h.flow.step(context.Background(), id)
	if got := buttons(h.tg.methods("editMessageMedia")[0].markup); !reflect.DeepEqual(got, [][]string{{callback(id, actQuiet)}}) {
		t.Fatalf("buttons %v", got)
	}
	h.tg.reset()
	if err := h.onCard(id, 7, actQuiet, "cq-1"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[2]
	if job == nil || job.n.Free || job.n.Settings["generate_audio"] != false {
		t.Fatalf("job = %+v", job)
	}
	// The run that failed keeps its own settings.
	if j.n.Settings["generate_audio"] != true {
		t.Fatalf("the failed run's settings: %v", j.n.Settings)
	}
}

// When the card cannot be shown, the reply that says why carries its
// buttons and takes its place.
func TestAFailureReplyCarriesTheButtons(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.tg.fail["editMessageMedia"] = errBoom
	h.store.setStatus(id, jobs.Failed, jobs.Internal)
	h.flow.step(context.Background(), id)
	sent := h.tg.methods("sendMessage")
	if len(sent) != 1 || !reflect.DeepEqual(buttons(sent[0].markup), [][]string{{callback(id, actRetry), callback(id, actOther)}}) {
		t.Fatalf("reply = %+v", sent)
	}
	loop := h.tg.methods("deleteMessage")
	if r := h.store.runs[id]; len(loop) != 1 || r.PreviewMessageID == loop[0].messageID || r.PreviewMessageID == 0 || r.PreviewEphemeral {
		t.Fatalf("run = %+v, deleted %+v", r, loop)
	}
	delete(h.tg.fail, "editMessageMedia")
	h.tg.reset()
	if err := h.onCard(id, 7, actRetry, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[2] == nil {
		t.Fatal("no draft from the reply")
	}
}

// Original sends the image as a file, once, as a reply to the result, and
// takes its button away.
func TestOriginalSendsTheFileOnce(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	result := h.delivered(t, id, picture)
	if err := h.onResult(id, 7, actOriginal, "cq-1"); err != nil {
		t.Fatal(err)
	}
	docs := h.tg.methods("sendDocument")
	if len(docs) != 1 || docs[0].messageID != result || docs[0].chatID != group.ID || docs[0].file != "upload:framy.png" {
		t.Fatalf("documents = %+v", docs)
	}
	edits := h.tg.methods("editMessageReplyMarkup")
	if len(edits) != 1 || edits[0].messageID != result ||
		!reflect.DeepEqual(buttons(edits[0].markup), [][]string{{callback(id, actAnimate), callback(id, actAgain)}}) {
		t.Fatalf("keyboard edits = %+v", edits)
	}
	if err := h.onResult(id, 7, actOriginal, "cq-2"); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.methods("sendDocument")) != 1 || len(h.store.jobs) != 1 {
		t.Fatalf("calls %+v", h.tg.calls)
	}
}

// A picture that went as a document already, and a video, have no
// Original.
func TestOriginalOnlyUnderAPhoto(t *testing.T) {
	big := storage.Asset{ID: 51, UserID: 7, Kind: "result", ContentType: "image/png", Bytes: 12 << 20, Width: 4096, Height: 4096}
	if kb := resultKeyboard("en", 1, big, true, true); !reflect.DeepEqual(buttons(kb), [][]string{{callback(1, actAnimate), callback(1, actAgain)}}) {
		t.Fatalf("a document's buttons: %v", buttons(kb))
	}
	clip := storage.Asset{ID: 60, ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 4.2}
	if kb := resultKeyboard("en", 1, clip, true, true); !reflect.DeepEqual(buttons(kb), [][]string{{callback(1, actAgain)}}) {
		t.Fatalf("a video's buttons: %v", buttons(kb))
	}
	if kb := resultKeyboard("en", 1, clip, false, true); kb != nil {
		t.Fatalf("a video of the site: %v", buttons(kb))
	}

	h := newHarness(t)
	id := h.generating(t)
	h.delivered(t, id, big)
	if err := h.onResult(id, 7, actOriginal, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.methods("sendDocument")) != 0 {
		t.Fatalf("calls %+v", h.tg.calls)
	}
}

// A video result has Again under it.
func TestAVideoResultHasAgain(t *testing.T) {
	h := videoHarness(t)
	withHeyGen(h)
	id := h.generatingVideo(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 60, UserID: 7, Kind: "result", ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 4.2, Bytes: 2 << 20}}
	h.flow.step(context.Background(), id)
	sent := h.tg.methods("sendVideo")
	if len(sent) != 1 || !reflect.DeepEqual(buttons(sent[0].markup), [][]string{{callback(id, actAgain)}}) {
		t.Fatalf("video = %+v", sent)
	}
}

// A video sent to a large group comes back with no id while Telegram
// re-encodes it. Again under it, once it is in the chat, is pressed on the
// message with the result's file, which is then the result's: a new draft
// answers it, and a press on another message still makes nothing. Before,
// Again under such a video did nothing for good.
func TestAgainUnderAVideoThatHadNoIDWorks(t *testing.T) {
	h := videoHarness(t)
	withHeyGen(h)
	h.tg.videoUnnumbered = true
	id := h.generatingVideo(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 60, UserID: 7, Kind: "result", ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 4.2, Bytes: 2 << 20}}
	h.flow.step(context.Background(), id)
	if r := h.store.runs[id]; r.Stage != db.StageDone || r.ResultMessageID != 0 || r.ResultFileUniqueID != "video-unique" {
		t.Fatalf("run = %+v", r)
	}
	press := func(qid string, msg tg.Message) {
		t.Helper()
		cq := &tg.CallbackQuery{ID: qid, From: tg.User{ID: 7, LanguageCode: "en"}, Data: callback(id, actAgain), Message: &msg}
		if err := h.flow.press(context.Background(), cq); err != nil {
			t.Fatal(err)
		}
	}
	// Another message of the chat, and one with another file, are not it.
	press("cq-0", tg.Message{MessageID: 555, Chat: group, Date: 1, Video: &tg.Video{FileUniqueID: "other"}})
	press("cq-1", tg.Message{MessageID: 555, Chat: group, Date: 1})
	if len(h.store.jobs) != 1 || h.store.runs[id].ResultMessageID != 0 {
		t.Fatalf("jobs %d, run %+v", len(h.store.jobs), h.store.runs[id])
	}
	press("cq-2", tg.Message{MessageID: 777, Chat: group, Date: 1, Video: &tg.Video{FileID: "video-file", FileUniqueID: "video-unique"}})
	if r := h.store.runs[id]; r.ResultMessageID != 777 {
		t.Fatalf("result message not recorded: %+v", r)
	}
	if job := h.store.jobs[2]; job == nil || job.status != jobs.Draft || job.n.MessageID != 777 {
		t.Fatalf("job = %+v", job)
	}
	// From now on the result is that message and no other.
	h.now = h.now.Add(10 * time.Second)
	press("cq-3", tg.Message{MessageID: 778, Chat: group, Date: 1, Video: &tg.Video{FileUniqueID: "video-unique"}})
	if len(h.store.jobs) != 2 {
		t.Fatalf("jobs %d", len(h.store.jobs))
	}
}

// When its file changed in the re-encoding, the result is framy's video
// answering the command.
func TestAVideoThatHadNoIDIsFramysAnswerToTheCommand(t *testing.T) {
	run := db.ChatRun{JobID: 1, ChatID: group.ID, CommandMessageID: 10, Stage: db.StageDone, ResultFileUniqueID: "video-unique"}
	bot := &tg.User{ID: 1, IsBot: true}
	for _, c := range []struct {
		msg  tg.Message
		want bool
	}{
		{tg.Message{MessageID: 5, Chat: group, From: bot, Video: &tg.Video{FileUniqueID: "re-encoded"}, ReplyToMessage: &tg.Message{MessageID: 10}}, true},
		{tg.Message{MessageID: 5, Chat: group, From: bot, Video: &tg.Video{FileUniqueID: "re-encoded"}, ReplyToMessage: &tg.Message{MessageID: 11}}, false},
		{tg.Message{MessageID: 5, Chat: group, From: &tg.User{ID: 7}, Video: &tg.Video{FileUniqueID: "re-encoded"}, ReplyToMessage: &tg.Message{MessageID: 10}}, false},
		{tg.Message{MessageID: 5, Chat: group, From: bot, Photo: []tg.Photo{{FileUniqueID: "p"}}, ReplyToMessage: &tg.Message{MessageID: 10}}, false},
		{tg.Message{EphemeralMessageID: 5, Chat: group, From: bot, Video: &tg.Video{FileUniqueID: "video-unique"}}, false},
	} {
		cq := &tg.CallbackQuery{Data: callback(1, actAgain), Message: &c.msg}
		if _, ok := pressedOn(cq, run, actAgain); ok != c.want {
			t.Errorf("%+v: pressed %v", c.msg, ok)
		}
	}
	// A run not yet done has no result to press on.
	run.Stage = db.StageGenerating
	cq := &tg.CallbackQuery{Data: callback(1, actAgain), Message: &tg.Message{MessageID: 5, Chat: group, Video: &tg.Video{FileUniqueID: "video-unique"}}}
	if _, ok := pressedOn(cq, run, actAgain); ok {
		t.Error("pressed on a run still generating")
	}
}

// A guest run's buttons are on its inline answer, which has none of these.
func TestAGuestRunHasNoResultButtons(t *testing.T) {
	run := db.ChatRun{JobID: 1, InlineMessageID: "inline-1", Stage: db.StageFailed}
	for _, action := range []string{actAgain, actOriginal, actRetry, actOther, actQuiet} {
		cq := &tg.CallbackQuery{InlineMessageID: "inline-1", Data: callback(1, action)}
		if _, ok := pressedOn(cq, run, action); ok {
			t.Errorf("%s pressed on a guest run", action)
		}
	}
	if kb := failureKeyboard(run); kb != nil {
		t.Fatalf("a guest card's buttons: %v", buttons(kb))
	}
}

// Two buttons share a row only when both labels fit half of it; a long
// one, as in Belarusian, takes a row of its own, so neither is cut.
func TestALongLabelTakesARowOfItsOwn(t *testing.T) {
	run := db.ChatRun{JobID: 1, CommandMessageID: 10, Status: string(jobs.Failed), ErrorClass: string(jobs.Internal)}
	for lang, want := range map[string][][]string{
		"en": {{callback(1, actRetry), callback(1, actOther)}},
		"be": {{callback(1, actRetry)}, {callback(1, actOther)}},
	} {
		run.Lang = lang
		if got := buttons(failureKeyboard(run)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v (%q), want %v", lang, got, labels(failureKeyboard(run)), want)
		}
	}
	// The same for what a guest run's card can change: Model and Make a
	// video share a row in English, not in French.
	d := draft{pay: payFree, offer: catalog.Offer{Kind: catalog.Image}, taken: 1}
	for lang, rows := range map[string]int{"en": 2, "fr": 3} {
		if got := previewKeyboard(lang, 1, d).InlineKeyboard; len(got) != rows {
			t.Errorf("%s: %q", lang, labels(previewKeyboard(lang, 1, d)))
		}
	}
	// Three that fit go two and one.
	three := []tg.InlineKeyboardButton{{Text: "a"}, {Text: "b"}, {Text: "c"}}
	if rows := inPairs(three); len(rows) != 2 || len(rows[0]) != 2 || len(rows[1]) != 1 {
		t.Errorf("rows = %v", rows)
	}
}
