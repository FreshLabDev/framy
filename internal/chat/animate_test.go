// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"path/filepath"
	"reflect"
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

// image is the result of the image runs below.
var image = storage.Asset{ID: 50, UserID: 7, Kind: "result", ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}

// delivered makes the run's result go out as a reply, and returns the
// message it went out in.
func (h *harness) delivered(t *testing.T, id int64, result storage.Asset) int64 {
	t.Helper()
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{result}
	if h.files.assets == nil {
		h.files.assets = map[int64]storage.Asset{}
	}
	h.files.assets[result.ID] = result
	h.now = h.now.Add(40 * time.Second)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
	h.tg.reset()
	return h.store.runs[id].ResultMessageID
}

// animate presses Animate on the result message, as press qid.
func (h *harness) animate(id, from int64, chat tg.Chat, qid string) error {
	run := h.store.runs[id]
	cq := &tg.CallbackQuery{ID: qid, From: tg.User{ID: from, LanguageCode: "en"}, Data: callback(id, actAnimate),
		Message: tg.Message{MessageID: run.ResultMessageID, Chat: chat, Date: 1, Photo: []tg.Photo{{FileID: "result-file"}}}}
	return h.flow.press(context.Background(), cq)
}

func animateHarness(t *testing.T) (*harness, int64, int64) {
	t.Helper()
	h := newHarness(t)
	withHeyGen(h)
	id := h.generating(t)
	return h, id, h.delivered(t, id, image)
}

// In a group the preview goes to the requester alone, which the press lets
// framy do, admin or not, and answers the result.
func TestAnimateMakesAVideoOfTheImage(t *testing.T) {
	h, id, result := animateHarness(t)
	if result == 0 {
		t.Fatal("no result message")
	}
	if err := h.animate(id, 7, group, "cq-1"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[2]
	want := map[string]any{"duration": 5, "resolution": "768p", "frame_images": []string{"first_frame"}}
	if job == nil || !job.n.Free || job.n.Offer.ID != free.VideoHeyGen.Offer || !reflect.DeepEqual(job.n.Settings, want) ||
		!reflect.DeepEqual(job.n.Inputs, []int64{image.ID}) || job.n.Prompt != "a fox" || job.n.DedupeKey != "tg:cb:cq-1" ||
		job.n.Estimate == nil || job.n.Estimate.USD <= 0 {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].messageID != result || sent[0].ephemeral == nil ||
		*sent[0].ephemeral != (tg.Ephemeral{ReceiverUserID: 7, CallbackQueryID: "cq-1"}) {
		t.Fatalf("preview = %+v", sent)
	}
	if got := lastCard(t, h); !reflect.DeepEqual(got, [][]string{{callback(2, actGenerate), callback(2, actNo)}, {callback(2, actImage)}}) {
		t.Fatalf("keyboard = %v", got)
	}
	p := h.renderer.previews[len(h.renderer.previews)-1]
	if p.Model != "HeyGen Video" || !p.Free || !reflect.DeepEqual(p.Chips, []string{"0:05", "768p"}) {
		t.Fatalf("card = %+v", p)
	}
	if paths := h.renderer.albums[len(h.renderer.albums)-1]; len(paths) != 1 || filepath.Base(paths[0]) != "in-0" {
		t.Fatalf("the card's picture: %v", paths)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "" {
		t.Fatalf("answers = %v", h.tg.answers)
	}
	r, err := h.store.ChatRun(context.Background(), 2)
	if err != nil || r.CommandMessageID != result || !r.PreviewEphemeral || r.Taken != 1 || r.UserID != 7 || r.ChatID != group.ID {
		t.Fatalf("run = %+v, %v", r, err)
	}
	// The image's run is done and stays so.
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}

	// Generate on it: the loop answers the image.
	h.tg.reset()
	if err := h.press(2, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[2].status != jobs.Queued {
		t.Fatalf("status %s", h.store.jobs[2].status)
	}
	if loop := h.tg.methods("sendAnimation"); len(loop) != 1 || loop[0].messageID != result {
		t.Fatalf("loop = %+v", loop)
	}
}

// A double tap is one press; a press a while later is another video.
func TestADoubleTapOnAnimateMakesOneDraft(t *testing.T) {
	h, id, _ := animateHarness(t)
	for _, q := range []string{"cq-1", "cq-2"} {
		if err := h.animate(id, 7, group, q); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.store.jobs) != 2 || len(h.tg.methods("sendPhoto")) != 1 {
		t.Fatalf("jobs %d, previews %d", len(h.store.jobs), len(h.tg.methods("sendPhoto")))
	}
	// The same press again, as Telegram may send it twice, makes nothing.
	h.now = h.now.Add(10 * time.Second)
	if err := h.animate(id, 7, group, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 2 {
		t.Fatalf("jobs %d", len(h.store.jobs))
	}
	if err := h.animate(id, 7, group, "cq-3"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 3 {
		t.Fatalf("jobs %d", len(h.store.jobs))
	}
}

func TestOnlyTheRequesterAnimates(t *testing.T) {
	h, id, _ := animateHarness(t)
	if err := h.animate(id, 8, group, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "alert:"+i18n.T("en", "run.not_yours") || len(h.store.jobs) != 1 {
		t.Fatalf("answers %v, jobs %d", h.tg.answers, len(h.store.jobs))
	}
}

// Animate is on the result: from the preview of another run, or with the
// data of a run whose result is elsewhere, it makes nothing.
func TestAnimateFromAnotherMessageMakesNothing(t *testing.T) {
	h, id, result := animateHarness(t)
	cq := &tg.CallbackQuery{ID: "cq-1", From: tg.User{ID: 7}, Data: callback(id, actAnimate),
		Message: tg.Message{MessageID: result + 1, Chat: group, Date: 1}}
	if err := h.flow.press(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	cq.Message = tg.Message{EphemeralMessageID: result, Chat: group, Date: 1}
	if err := h.flow.press(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 1 || len(h.tg.calls) != 0 {
		t.Fatalf("jobs %d, calls %+v", len(h.store.jobs), h.tg.calls)
	}
}

// In a DM the preview is an ordinary reply to the image.
func TestAnimateInADirectChat(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	dm := tg.Chat{ID: 7, Type: "private"}
	m := command("/img a fox")
	m.Chat = dm
	if err := h.command(m, CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	run := h.store.runs[1]
	if err := h.pressOn(1, 7, actGenerate, tg.Message{MessageID: run.PreviewMessageID, Chat: dm, Date: 1}); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	result := h.delivered(t, 1, image)
	if err := h.animate(1, 7, dm, "cq-1"); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].messageID != result || sent[0].ephemeral != nil || sent[0].chatID != 7 {
		t.Fatalf("preview = %+v", sent)
	}
	if r := h.store.runs[2]; r == nil || r.PreviewEphemeral || r.PreviewMessageID != h.tg.next {
		t.Fatalf("run = %+v", r)
	}
}

func TestPastTheFreeVideoAnimateNeedsAKey(t *testing.T) {
	limit := &free.LimitError{Kind: catalog.Video, Limit: 1, FreesAt: clock.Add(3 * time.Hour)}
	h, id, _ := animateHarness(t)
	h.queue.refuse = limit
	if err := h.animate(id, 7, group, "cq-1"); err != nil {
		t.Fatal(err)
	}
	want := "alert:" + i18n.T("en", "edit.limit", "limit", "1", "wait", "3 h 0 min")
	if len(h.tg.answers) != 1 || h.tg.answers[0] != want || len(h.store.jobs) != 1 || len(h.tg.calls) != 0 {
		t.Fatalf("answers %v, jobs %d, calls %+v", h.tg.answers, len(h.store.jobs), h.tg.calls)
	}

	h, id, _ = animateHarness(t)
	withKey(h)
	h.queue.refuse = limit
	if err := h.animate(id, 7, group, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[2]; job == nil || job.n.Free || job.n.Offer.ID != free.VideoHeyGen.Offer {
		t.Fatalf("job = %+v", job)
	}
	if p := h.renderer.previews[len(h.renderer.previews)-1]; p.Free || p.Label != i18n.T("en", "card.key", "provider", "OpenRouter") {
		t.Fatalf("card = %+v", p)
	}
}

// A video is no start frame: its run has no Animate, and the data changes
// nothing.
func TestAVideoResultIsNotAnimated(t *testing.T) {
	h := videoHarness(t)
	withHeyGen(h)
	id := h.generatingVideo(t)
	h.delivered(t, id, storage.Asset{ID: 60, UserID: 7, Kind: "result", ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 4.2, Bytes: 2 << 20})
	if err := h.animate(id, 7, group, "cq-1"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 1 || len(h.tg.calls) != 0 {
		t.Fatalf("jobs %d, calls %+v", len(h.store.jobs), h.tg.calls)
	}
}

// A failure after the draft was made cancels it.
func TestAnAnimateThatFailsCancelsItsDraft(t *testing.T) {
	h, id, _ := animateHarness(t)
	h.tg.fail["sendPhoto"] = errBoom
	if err := h.animate(id, 7, group, "cq-1"); err == nil {
		t.Fatal("no error")
	}
	if h.store.jobs[2] == nil || h.store.jobs[2].status != jobs.Cancelled || h.store.runs[2] != nil {
		t.Fatalf("job %+v, run %+v", h.store.jobs[2], h.store.runs[2])
	}
}
