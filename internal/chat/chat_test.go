// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/storage"
)

func TestATextCommandShowsAPreviewOfTheFreeRun(t *testing.T) {
	h := newHarness(t)
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || !job.n.Free || job.n.Surface != jobs.Group || job.n.Offer.ID != free.ImageMAI.Offer ||
		job.n.Prompt != "a fox" || job.n.DedupeKey != "tg:-100:10" || len(job.n.Inputs) != 0 {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 {
		t.Fatalf("previews = %+v", h.tg.calls)
	}
	p := sent[0]
	// The card, as a reply to the command; the caption is the prompt alone.
	if p.chatID != -100 || p.messageID != 10 || p.file != "upload:framy.png" || p.text != "<blockquote>a fox</blockquote>" {
		t.Fatalf("preview = %+v", p)
	}
	card := h.renderer.previews[0]
	if card.Model != "MAI Image 2.6 Flash" || card.Maker != "Microsoft AI" || !card.Free || card.Left != 5 || card.Limit != 5 ||
		card.Label != i18n.T("en", "card.free") || len(h.renderer.albums[0]) != 0 {
		t.Fatalf("card = %+v", card)
	}
	row := p.markup.InlineKeyboard[0]
	if len(row) != 2 || row[0].CallbackData != "j:1:go" || row[0].Style != "success" || row[1].CallbackData != "j:1:no" || row[1].Style != "danger" {
		t.Fatalf("buttons = %+v", row)
	}
	run, err := h.store.ChatRun(context.Background(), 1)
	if err != nil || run.Stage != db.StagePreview || run.PreviewMessageID == 0 || run.CommandMessageID != 10 || run.Lang != "en" {
		t.Fatalf("run = %+v, %v", run, err)
	}
}

// A card the faces cannot label, as in Japanese, leaves the price to the
// caption.
func TestACardWithoutItsLabelPutsThePriceInTheCaption(t *testing.T) {
	h := newHarness(t)
	h.store.chatLang[-100] = "ja"
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	p := h.tg.methods("sendPhoto")[0]
	if !strings.Contains(p.text, i18n.T("ja", "run.free", "left", "5", "limit", "5")) {
		t.Fatalf("caption = %q", p.text)
	}
}

func TestAReplyToAPhotoTakesThePhoto(t *testing.T) {
	h := newHarness(t)
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "big" {
		t.Fatalf("the largest size was not taken: %+v", got)
	}
	job := h.store.jobs[1]
	if len(job.n.Inputs) != 1 || job.n.Offer.ID != free.ImageMAI.Offer {
		t.Fatalf("job = %+v", job.n)
	}
	if len(h.renderer.albums) != 1 || len(h.renderer.albums[0]) != 1 {
		t.Fatalf("card drawn from %v", h.renderer.albums)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].messageID != 10 {
		t.Fatalf("preview = %+v", sent)
	}
}

func TestTheCaptionOfOnesOwnPhotoTakesIt(t *testing.T) {
	h := newHarness(t)
	m := command("")
	m.Caption = "/img make it night"
	m.Photo = []tg.Photo{{FileID: "own", Width: 800, Height: 600}}
	if err := h.command(m, CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "own" {
		t.Fatalf("files = %+v", got)
	}
}

func TestAnAlbumIsTakenWholeWhenFramyIsAnAdmin(t *testing.T) {
	h := newHarness(t)
	var items []db.AlbumItem
	for i := range 7 {
		items = append(items, db.AlbumItem{MessageID: int64(20 + i), Kind: db.AlbumPhoto, FileID: "p" + string(rune('a'+i))})
	}
	items = append(items, db.AlbumItem{MessageID: 30, Kind: db.AlbumVideo, FileID: "v"})
	h.flow.SetAlbums(fakeAlbums{items: items, admin: true})
	m := photoReply("/img one style")
	m.ReplyToMessage.MediaGroupID = "g1"
	if err := h.command(m, CommandImage, "one style"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	// Seven photos are more than MAI takes: Klein takes the album; the video
	// is no photo to edit.
	if len(job.n.Inputs) != 7 || job.n.Offer.ID != free.ImageKlein.Offer {
		t.Fatalf("job = %d inputs on %s", len(job.n.Inputs), job.n.Offer.ID)
	}
	run, _ := h.store.ChatRun(context.Background(), 1)
	if run.Note != "" || run.Taken != 7 {
		t.Fatalf("run = %+v", run)
	}
}

func TestWithoutAdminRightsThePreviewSaysWhatItSaw(t *testing.T) {
	h := newHarness(t)
	h.flow.SetAlbums(fakeAlbums{items: []db.AlbumItem{{MessageID: 9, Kind: db.AlbumPhoto, FileID: "big"}}})
	m := photoReply("/img one style")
	m.ReplyToMessage.MediaGroupID = "g1"
	if err := h.command(m, CommandImage, "one style"); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || !strings.Contains(sent[0].text, i18n.T("en", noteAlbumOne)) {
		t.Fatalf("preview = %+v", sent)
	}
}

func TestMoreThanTenPhotosAreCut(t *testing.T) {
	h := newHarness(t)
	var items []db.AlbumItem
	for i := range 12 {
		items = append(items, db.AlbumItem{MessageID: int64(20 + i), Kind: db.AlbumPhoto, FileID: "p"})
	}
	h.flow.SetAlbums(fakeAlbums{items: items, admin: true})
	m := photoReply("/img x")
	m.ReplyToMessage.MediaGroupID = "g1"
	if err := h.command(m, CommandImage, "x"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.store.jobs[1].n.Inputs); n != free.ImageKlein.MaxInputs {
		t.Fatalf("%d inputs", n)
	}
	sent := h.tg.methods("sendPhoto")
	if !strings.Contains(sent[0].text, i18n.T("en", noteCut, "max", "10", "left", "2")) {
		t.Fatalf("caption = %q", sent[0].text)
	}
}

func TestPastTheFreeTierWithoutAKeyTheReasonIsTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.queue.refuse = &free.LimitError{Kind: catalog.Image, Limit: 5, Window: free.ImageWindow, FreesAt: h.now.Add(2*time.Hour + 14*time.Minute + 30*time.Second)}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 0 || len(h.tg.methods("sendPhoto")) != 0 {
		t.Fatal("a preview went out")
	}
	got := h.tg.methods("sendMessage")
	if len(got) != 1 || got[0].text != i18n.T("en", "run.limit", "limit", "5", "wait", "2 h 15 min") || got[0].messageID != 10 {
		t.Fatalf("answer = %+v", got)
	}
}

func TestPastTheFreeTierTheKeyRunsIt(t *testing.T) {
	h := newHarness(t)
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	h.queue.refuse = &free.ClosedError{}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || job.n.Free {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendPhoto")
	card := h.renderer.previews[0]
	if len(sent) != 1 || card.Free || card.Label != i18n.T("en", "card.key", "provider", "OpenRouter") || card.Price == "" {
		t.Fatalf("preview = %+v, card = %+v", sent, card)
	}
}

func TestARefusedKeyDoesNotRunIt(t *testing.T) {
	h := newHarness(t)
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Invalid}}}
	h.queue.refuse = &free.ClosedError{}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.closed") {
		t.Fatalf("answer = %+v", got)
	}
}

func TestEmptyCommandsVideosAndRepeats(t *testing.T) {
	h := newHarness(t)
	if err := h.command(command("/img"), CommandImage, ""); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.empty") {
		t.Fatalf("empty: %+v", got)
	}
	h.tg.reset()
	if err := h.command(command("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	m := command("/img rain")
	m.ReplyToMessage = &tg.Message{MessageID: 9, Video: &tg.Video{FileID: "v"}}
	if err := h.command(m, CommandImage, "rain"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 2 || got[0].text != i18n.T("en", "run.video_soon") || got[1].text != got[0].text {
		t.Fatalf("video: %+v", got)
	}
	h.tg.reset()
	// The same update again starts nothing.
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.tg.methods("sendPhoto")); n != 1 || len(h.store.jobs) != 1 {
		t.Fatalf("%d previews, %d jobs", n, len(h.store.jobs))
	}
}

func TestAPhotoFramyCannotReadIsSaidSo(t *testing.T) {
	h := newHarness(t)
	h.files.fail = storage.ErrTooBig
	err := h.command(photoReply("/img x"), CommandImage, "x")
	if err == nil {
		t.Fatal("no error")
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.too_big") {
		t.Fatalf("answer = %+v", got)
	}
	if len(h.store.jobs) != 0 {
		t.Fatal("a job was made")
	}
}

func TestAFailedPreviewCancelsTheDraft(t *testing.T) {
	h := newHarness(t)
	h.tg.fail["sendPhoto"] = errBoom
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err == nil {
		t.Fatal("no error")
	}
	if len(h.queue.cancelled) != 1 || h.store.jobs[1].status != jobs.Cancelled {
		t.Fatalf("cancelled = %v", h.queue.cancelled)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.failed") {
		t.Fatalf("answer = %+v", got)
	}
}

func TestTheGroupsLanguageWritesThePreview(t *testing.T) {
	h := newHarness(t)
	h.store.chatLang[-100] = "uk"
	if err := h.command(command("/img лис"), CommandImage, "лис"); err != nil {
		t.Fatal(err)
	}
	p := h.tg.methods("sendPhoto")[0]
	if p.markup.InlineKeyboard[0][0].Text != i18n.T("uk", "run.generate") {
		t.Fatalf("buttons = %+v", p.markup.InlineKeyboard)
	}
}

// preview makes a run waiting for Generate and returns its job id.
func (h *harness) preview(t *testing.T) int64 {
	t.Helper()
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	return 1
}

func TestOnlyTheRequesterPresses(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 8, actGenerate); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "alert:"+i18n.T("en", "run.not_yours") {
		t.Fatalf("answers = %v", h.tg.answers)
	}
	if h.store.jobs[id].status != jobs.Draft {
		t.Fatal("someone else's press queued the job")
	}
	// A press against another message does nothing.
	cq := &tg.CallbackQuery{ID: "x", From: tg.User{ID: 7}, Data: callback(id, actGenerate), Message: tg.Message{MessageID: 1, Chat: group}}
	if err := h.flow.press(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Draft {
		t.Fatal("a press on another message queued the job")
	}
}

func TestGenerateTurnsThePreviewIntoTheLoop(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Queued || h.store.stage(id) != db.StageGenerating {
		t.Fatalf("status %s, stage %s", h.store.jobs[id].status, h.store.stage(id))
	}
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].file != "animation upload:framy.mp4" || edits[0].markup.InlineKeyboard[0][0].CallbackData != "j:1:stop" {
		t.Fatalf("edits = %+v", edits)
	}
	if !strings.Contains(edits[0].text, i18n.T("en", "run.generating", "model", "<b>MAI Image 2.6 Flash</b>")) {
		t.Fatalf("caption = %q", edits[0].text)
	}
	// A second press starts nothing.
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if n := len(h.tg.methods("editMessageMedia")); n != 1 {
		t.Fatalf("%d edits", n)
	}
	// The next run's loop goes by its file_id.
	h.store.runs[2] = &db.ChatRun{JobID: 2, ChatID: -100, PreviewMessageID: 500, Lang: "en", Stage: db.StagePreview}
	h.store.jobs[2] = &fakeJob{n: jobs.New{UserID: 7}, status: jobs.Draft}
	if err := h.press(2, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if e := h.tg.methods("editMessageMedia"); e[len(e)-1].file != "animation loop-file" {
		t.Fatalf("second loop = %+v", e[len(e)-1])
	}
}

func TestGenerateOnAnExpiredDraftDeletesThePreview(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.queue.generate = jobs.ErrNotDraft
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.tg.answers[0] != "alert:"+i18n.T("en", "run.expired") || h.store.stage(id) != db.StageGone || len(h.tg.methods("deleteMessage")) != 1 {
		t.Fatalf("answers %v, stage %s, calls %+v", h.tg.answers, h.store.stage(id), h.tg.calls)
	}
}

func TestGenerateWhenTheFreeTierRanOut(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.queue.generate = &free.LimitError{Limit: 5, FreesAt: h.now.Add(40 * time.Second)}
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.tg.answers[0] != "alert:"+i18n.T("en", "run.limit", "limit", "5", "wait", "1 min") || h.store.stage(id) != db.StagePreview {
		t.Fatalf("answers %v, stage %s", h.tg.answers, h.store.stage(id))
	}
}

func TestNoCancelsAndDeletes(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 7, actNo); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Cancelled || h.store.stage(id) != db.StageGone {
		t.Fatalf("status %s, stage %s", h.store.jobs[id].status, h.store.stage(id))
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != h.store.runs[id].PreviewMessageID {
		t.Fatalf("deletes = %+v", d)
	}
}

func TestStopARunningJob(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.store.setStatus(id, jobs.Running, "")
	h.queue.cancelErr = jobs.ErrRunning
	h.tg.reset()
	if err := h.press(id, 7, actStop); err != nil {
		t.Fatal(err)
	}
	if !h.store.runs[id].Stopped || h.store.stage(id) != db.StageGone || len(h.tg.methods("deleteMessage")) != 1 {
		t.Fatalf("run %+v, calls %+v", h.store.runs[id], h.tg.calls)
	}
	if h.tg.answers[0] != i18n.T("en", "run.stopped") {
		t.Fatalf("answers = %v", h.tg.answers)
	}
}

func TestStopAQueuedJob(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if err := h.press(id, 7, actStop); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Cancelled || h.store.stage(id) != db.StageGone || h.store.runs[id].Stopped {
		t.Fatalf("status %s, run %+v", h.store.jobs[id].status, h.store.runs[id])
	}
}

// generating makes a run whose job is queued, Generate pressed at h.now.
func (h *harness) generating(t *testing.T) int64 {
	t.Helper()
	id := h.preview(t)
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	return id
}

func TestAQuickResultReplacesTheLoop(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(12 * time.Second)
	h.flow.step(context.Background(), id)
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].file != "photo upload:framy.png" || edits[0].markup != nil {
		t.Fatalf("edits = %+v", edits)
	}
	if !strings.Contains(edits[0].text, "a fox") || !strings.Contains(edits[0].text, "MAI Image 2.6 Flash") {
		t.Fatalf("caption = %q", edits[0].text)
	}
	if h.store.stage(id) != db.StageDone || len(h.tg.methods("deleteMessage")) != 0 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}

func TestASlowResultIsAReply(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(40 * time.Second)
	h.flow.step(context.Background(), id)
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].messageID != 10 {
		t.Fatalf("sent = %+v", sent)
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != h.store.runs[id].PreviewMessageID {
		t.Fatalf("the loop stayed: %+v", d)
	}
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

func TestAHugeResultIsADocument(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 30 << 20, Width: 8000, Height: 8000}}
	h.flow.step(context.Background(), id)
	if d := h.tg.methods("sendDocument"); len(d) != 1 || d[0].file != "upload:framy.png" {
		t.Fatalf("documents = %+v", d)
	}
}

func TestAFailedRunShowsTheCard(t *testing.T) {
	for _, tc := range []struct {
		status jobs.Status
		class  jobs.Class
		free   bool
		want   string
	}{
		{jobs.Failed, jobs.Moderation, true, "err.moderation"},
		{jobs.Failed, jobs.Money, false, "err.money"},
		{jobs.Failed, jobs.Money, true, "err.failed"},
		{jobs.Failed, jobs.Key, false, "err.key"},
		// A free run is paid by framy's key, not the person's.
		{jobs.Failed, jobs.Key, true, "err.failed"},
		{jobs.Expired, "", true, "err.timeout"},
		{jobs.Failed, jobs.Internal, true, "err.failed"},
	} {
		h := newHarness(t)
		id := h.generating(t)
		h.store.jobs[id].n.Free = tc.free
		h.store.setStatus(id, tc.status, tc.class)
		h.flow.step(context.Background(), id)
		if len(h.renderer.cards) != 1 || h.renderer.cards[0] != i18n.T("en", tc.want) {
			t.Errorf("%s/%s: cards %v, want %s", tc.status, tc.class, h.renderer.cards, tc.want)
			continue
		}
		edits := h.tg.methods("editMessageMedia")
		if len(edits) != 1 || edits[0].file != "photo upload:framy.png" || h.store.stage(id) != db.StageFailed {
			t.Errorf("%s/%s: edits %+v, stage %s", tc.status, tc.class, edits, h.store.stage(id))
		}
	}
}

func TestACardWithoutTheLettersPutsTheLineInTheCaption(t *testing.T) {
	h := newHarness(t)
	h.store.chatLang[-100] = "ja"
	id := h.generating(t)
	h.store.setStatus(id, jobs.Failed, jobs.Moderation)
	h.flow.step(context.Background(), id)
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || !strings.Contains(edits[0].text, i18n.T("ja", "err.moderation")) {
		t.Fatalf("edits = %+v", edits)
	}
}

func TestAnUnpressedPreviewGoesWhenItsDraftIsCancelled(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.flow.step(context.Background(), id)
	if len(h.tg.calls) != 0 {
		t.Fatalf("a waiting preview was touched: %+v", h.tg.calls)
	}
	h.store.setStatus(id, jobs.Cancelled, jobs.DraftExpired)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGone || len(h.tg.methods("deleteMessage")) != 1 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}

func TestAQueuedJobWhoseRunStayedInPreviewMovesOn(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.store.setStatus(id, jobs.Queued, "")
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGenerating || len(h.tg.methods("editMessageMedia")) != 1 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}

func TestProgressKeepsItsCadence(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Running, "")
	ctx := context.Background()
	h.flow.step(ctx, id) // right after the press: nothing yet
	if len(h.tg.calls) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	h.now = h.now.Add(5 * time.Second)
	h.flow.step(ctx, id)
	if len(h.tg.methods("sendChatAction")) != 1 || len(h.tg.methods("editMessageCaption")) != 0 {
		t.Fatalf("at 5 s: %+v", h.tg.calls)
	}
	h.now = h.now.Add(6 * time.Second)
	h.flow.step(ctx, id)
	caps := h.tg.methods("editMessageCaption")
	if len(caps) != 1 || !strings.Contains(caps[0].text, "0:11") {
		t.Fatalf("at 11 s: %+v", caps)
	}
}

func TestFollowWorksEveryOpenRun(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 100, Width: 10, Height: 10}}
	h.flow.follow(context.Background())
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

func TestCallbackData(t *testing.T) {
	for data, ok := range map[string]bool{"j:1:go": true, "j:123456789012:stop": true, "j:0:go": false, "j:x:go": false, "j:1:": false, "m:1:home": false} {
		_, _, got := parseCallback(data)
		if got != ok {
			t.Errorf("%q: %v", data, got)
		}
	}
	if d := callback(123456789012, actStop); len(d) > 64 {
		t.Fatalf("%q is over Telegram's 64 bytes", d)
	}
}

func TestPriceLabels(t *testing.T) {
	q := fx.Quote{Code: fx.UAH, PerUSD: 41.5}
	for _, tc := range []struct {
		e    price.Estimate
		lang string
		want string
	}{
		{price.Estimate{Kind: price.Exact, USD: 0.034, MaxUSD: 0.034}, "en", "$0.034 · ₴1.42"},
		{price.Estimate{Kind: price.About, USD: 0.034, MaxUSD: 0.04}, "uk", i18n.T("uk", "run.about", "price", "0,034\u00a0$ · 1,42\u00a0₴")},
		{price.Estimate{Kind: price.UpTo, USD: 0.02, MaxUSD: 0.05}, "en", i18n.T("en", "run.up_to", "price", "$0.05 · ₴2.08")},
		{price.Estimate{Kind: price.Unknown}, "en", i18n.T("en", "run.price_unknown")},
	} {
		if got := priceLabel(tc.lang, tc.e, q); got != tc.want {
			t.Errorf("%s %v: %q, want %q", tc.lang, tc.e.Kind, got, tc.want)
		}
	}
	if got := priceLabel("en", price.Estimate{Kind: price.Exact, USD: 0.034}, fx.Dollar); got != "$0.034" {
		t.Errorf("in dollars: %q", got)
	}
}

func TestPromptQuote(t *testing.T) {
	if got := promptQuote(strings.Repeat("a", 700)); !strings.HasPrefix(got, "<blockquote expandable>") || !strings.Contains(got, "…") {
		t.Fatalf("long prompt = %q", got[:40])
	}
	if got := promptQuote("<b>x</b>"); got != "<blockquote>&lt;b&gt;x&lt;/b&gt;</blockquote>" {
		t.Fatalf("prompt not escaped: %q", got)
	}
}

// The wait is rounded up: a free run is never promised before it is there.
func TestTheWaitIsInHoursAndMinutes(t *testing.T) {
	for _, c := range []struct {
		lang string
		d    time.Duration
		want string
	}{
		{"en", 0, "1 min"},
		{"en", 59*time.Minute + time.Second, "1 h 0 min"},
		{"en", 23*time.Hour + 59*time.Minute, "23 h 59 min"},
		{"ru", 3*time.Hour + 5*time.Minute, "3 ч 5 мин"},
		{"uk", 12 * time.Minute, "12 хв"},
	} {
		if got := waitText(c.lang, c.d); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.lang, c.d, got, c.want)
		}
	}
}
