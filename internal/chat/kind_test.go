// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"reflect"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// videoModel puts a free video model of the plan in the catalog: a prompt
// and a start frame, priced by the second.
func videoModel(h *harness, plan free.Plan, name, maker string) {
	of := catalog.Offer{
		ID: plan.Offer, ModelID: plan.Model, Provider: catalog.OpenRouter, ProviderModelID: plan.Model,
		Kind: catalog.Video, Name: name, InputModalities: []string{"text", "image"},
		Routes: []catalog.Route{{SKUs: map[string]string{"duration_seconds": "0.05"}}},
	}
	h.store.models = append(h.store.models, catalog.Model{
		ID: of.ModelID, Kind: catalog.Video, Name: name, Maker: maker, Free: true, Offers: []catalog.Offer{of},
	})
}

func withHeyGen(h *harness) { videoModel(h, free.VideoHeyGen, "HeyGen Video", "HeyGen") }
func withGrok(h *harness)   { videoModel(h, free.VideoGrok, "Grok Imagine Video Lite", "xAI") }

// lastCard is the keyboard of the latest card on the preview: as sent, or
// as last edited.
func lastCard(t *testing.T, h *harness) [][]string {
	t.Helper()
	calls := append(h.tg.methods("sendPhoto"), h.tg.methods("editMessageMedia")...)
	if len(calls) == 0 {
		t.Fatal("no card")
	}
	var out [][]string
	for _, row := range calls[len(calls)-1].markup.InlineKeyboard {
		var r []string
		for _, b := range row {
			r = append(r, b.CallbackData)
		}
		out = append(out, r)
	}
	return out
}

func TestMakeAVideoTurnsAPromptIntoTheFreeVideo(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	id := h.preview(t)
	if err := h.press(id, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[id]
	want := map[string]any{"duration": 5, "resolution": "768p"}
	if !job.n.Free || job.n.Offer.ID != free.VideoHeyGen.Offer || !reflect.DeepEqual(job.n.Settings, want) ||
		job.n.Estimate == nil || job.n.Estimate.USD <= 0 {
		t.Fatalf("job = %+v", job.n)
	}
	if r := run(t, h); r.Kind != string(catalog.Video) || r.Taken != 0 {
		t.Fatalf("run = %+v", r)
	}
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].messageID != h.store.runs[id].PreviewMessageID {
		t.Fatalf("edits = %+v", edits)
	}
	p := h.renderer.previews[len(h.renderer.previews)-1]
	if p.Model != "HeyGen Video" || !p.Free || p.Left != 1 || p.Limit != 1 || !reflect.DeepEqual(p.Chips, []string{"0:05", "768p"}) {
		t.Fatalf("card = %+v", p)
	}
	// A video has no Model or Settings yet; it goes back to an image.
	want2 := [][]string{{callback(id, actGenerate), callback(id, actNo)}, {callback(id, actImage)}}
	if got := lastCard(t, h); !reflect.DeepEqual(got, want2) {
		t.Fatalf("keyboard = %v", got)
	}
	if got := edits[0].markup.InlineKeyboard[1][0].Text; got != i18n.T("en", "run.as_image") {
		t.Errorf("label = %q", got)
	}
	h.tg.reset()

	if err := h.press(id, 7, actImage); err != nil {
		t.Fatal(err)
	}
	job = h.store.jobs[id]
	if !job.n.Free || job.n.Offer.ID != free.ImageMAI.Offer || !reflect.DeepEqual(job.n.Settings, map[string]any{"n": 1}) {
		t.Fatalf("job = %+v", job.n)
	}
	if got := lastCard(t, h); len(got) != 2 || got[1][1] != callback(id, actVideo) {
		t.Fatalf("keyboard = %v", got)
	}
}

// The photo of a run becomes the video's start frame, and stays the
// image's source on the way back.
func TestAPhotoBecomesTheStartFrame(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	if err := h.command(photoReply("/img make it rain"), CommandImage, "make it rain"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.press(1, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	want := map[string]any{"duration": 5, "resolution": "768p", "frame_images": []string{"first_frame"}}
	if job.n.Offer.ID != free.VideoHeyGen.Offer || !reflect.DeepEqual(job.n.Settings, want) || len(job.n.Inputs) != 1 {
		t.Fatalf("job = %+v", job.n)
	}
	if err := h.press(1, 7, actImage); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job.n.Offer.ID != free.ImageMAI.Offer || len(job.n.Inputs) != 1 {
		t.Fatalf("job = %+v", job.n)
	}
}

// An album is no start frame: its preview has no such button, and old data
// changes nothing.
func TestAnAlbumDoesNotBecomeAVideo(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	m := photoReply("/img a collage")
	m.ReplyToMessage.MediaGroupID = "g"
	h.flow.SetAlbums(fakeAlbums{items: []db.AlbumItem{
		{MessageID: 20, Kind: db.AlbumPhoto, FileID: "pa"}, {MessageID: 21, Kind: db.AlbumPhoto, FileID: "pb"},
	}, admin: true})
	if err := h.command(m, CommandImage, "a collage"); err != nil {
		t.Fatal(err)
	}
	if got := lastCard(t, h); len(got) != 2 || h.store.runs[1].Taken != 2 {
		t.Fatalf("keyboard = %v, run %+v", got, h.store.runs[1])
	}
	h.tg.reset()
	if err := h.press(1, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != 0 || len(h.tg.calls) != 0 {
		t.Fatalf("revised %d, calls %+v", len(h.queue.revised), h.tg.calls)
	}
}

// While the catalog does not offer HeyGen, Grok Lite is the free video.
func TestWithoutHeyGenTheVideoIsGrok(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	withGrok(h)
	h.store.hidden = map[string]bool{free.VideoHeyGen.Offer: true}
	id := h.preview(t)
	if err := h.press(id, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[id]
	if job.n.Offer.ID != free.VideoGrok.Offer || !reflect.DeepEqual(job.n.Settings, map[string]any{"duration": 5, "resolution": "720p"}) {
		t.Fatalf("job = %+v", job.n)
	}
}

func TestPastTheFreeVideoAVideoNeedsAKey(t *testing.T) {
	limit := &free.LimitError{Kind: catalog.Video, Limit: 1, FreesAt: clock.Add(3 * time.Hour)}
	h := newHarness(t)
	withHeyGen(h)
	id := h.preview(t)
	h.queue.refuse = limit
	if err := h.press(id, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	want := "alert:" + i18n.T("en", "edit.limit", "limit", "1", "wait", "3 h 0 min")
	if len(h.tg.answers) != 1 || h.tg.answers[0] != want {
		t.Fatalf("answers = %v", h.tg.answers)
	}
	if job := h.store.jobs[id]; job.n.Offer.ID != free.ImageMAI.Offer || len(h.tg.calls) != 0 {
		t.Fatalf("job %+v, calls %+v", job.n, h.tg.calls)
	}

	// With a key the video runs on it, at the price the card shows.
	h = newHarness(t)
	withHeyGen(h)
	withKey(h)
	id = h.preview(t)
	h.queue.refuse = limit
	if err := h.press(id, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[id]
	if job.n.Free || job.n.Offer.ID != free.VideoHeyGen.Offer || job.status != jobs.Draft {
		t.Fatalf("job = %+v", job.n)
	}
	p := h.renderer.previews[len(h.renderer.previews)-1]
	if p.Free || p.Price == "" || p.Label != i18n.T("en", "card.key", "provider", "OpenRouter") {
		t.Fatalf("card = %+v", p)
	}
}

// An edit of a clip stays one: it has no Make an image, and its data
// changes nothing; Make a video is for an image run only.
func TestAnEditOfAClipDoesNotBecomeAnImage(t *testing.T) {
	h := videoHarness(t)
	withHeyGen(h)
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	for _, action := range []string{actImage, actVideo} {
		if err := h.press(1, 7, action); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.queue.revised) != 0 || len(h.tg.calls) != 0 || h.store.jobs[1].n.Offer.ID != free.VideoEdit.Offer {
		t.Fatalf("revised %d, calls %+v", len(h.queue.revised), h.tg.calls)
	}
}
