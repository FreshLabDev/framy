// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// pressJob makes job of person 7 a done job of press "p1" on the site, its
// results the frames at, in the model.
func pressJob(h *harness, job int64, prompt, model string, at []int32, results ...storage.Asset) {
	h.store.jobs[job] = &fakeJob{n: jobs.New{UserID: 7, Surface: jobs.Site, Prompt: prompt, Batch: "p1", BatchFrames: at}, status: jobs.Done}
	h.store.site = append(h.store.site, db.SiteResult{JobID: job, UserID: 7, Prompt: prompt, Kind: "image", Model: model,
		Deliver: true, Batch: "p1", Frames: at})
	h.store.userLang[7] = "en"
	if h.files.results == nil {
		h.files.results = map[int64][]storage.Asset{}
	}
	h.files.results[job] = results
	if h.files.assets == nil {
		h.files.assets = map[int64]storage.Asset{}
	}
	for _, a := range results {
		h.files.assets[a.ID] = a
	}
}

// pressImage is a picture result with its own id and key, so the order it is
// read in shows.
func pressImage(id int64) storage.Asset {
	a := picture
	a.ID, a.Key = id, "u/7/out/"+string(rune('a'+id-70))+".png"
	return a
}

// The frames of one press arrive as one album in their order, whichever
// job made them, with one caption: the prompt once, and a line per frame
// with its model. Each job's run is its first frame's message.
func TestAPressArrivesAsOneAlbum(t *testing.T) {
	h := newHarness(t)
	pressJob(h, 41, "a fox", "Model A", []int32{2}, pressImage(70))
	pressJob(h, 42, "a fox", "Model B", []int32{0, 3}, pressImage(71), pressImage(72))
	pressJob(h, 43, "a fox", "Model C", []int32{1}, pressImage(73))
	h.flow.sendSite(context.Background())

	if a := h.tg.methods("sendChatAction"); len(a) != 1 || a[0].chatID != 7 || a[0].action != "upload_photo" {
		t.Fatalf("action = %+v", a)
	}
	sent := h.tg.methods("sendMediaGroup")
	if len(sent) != 1 || sent[0].chatID != 7 || strings.Count(sent[0].file, "photo:upload:framy.png") != 4 {
		t.Fatalf("sent = %+v", sent)
	}
	if want := []string{"u/7/out/b.png", "u/7/out/d.png", "u/7/out/a.png", "u/7/out/c.png"}; !reflect.DeepEqual(h.files.opened, want) {
		t.Fatalf("album order %q, want %q", h.files.opened, want)
	}
	want := "<blockquote>a fox</blockquote>\n<b>1</b> Model B\n<b>2</b> Model C\n<b>3</b> Model A\n<b>4</b> Model B"
	if sent[0].text != want {
		t.Fatalf("caption = %q, want %q", sent[0].text, want)
	}
	if n := len(h.tg.methods("sendPhoto")); n != 0 {
		t.Fatalf("%d photos sent alone", n)
	}
	album := h.tg.next - 3 // the album's first message
	for job, at := range map[int64]int64{42: album, 43: album + 1, 41: album + 2} {
		if h.store.delivery(job) != db.DeliverySent {
			t.Fatalf("job %d: delivery %q", job, h.store.delivery(job))
		}
		r := h.store.runs[job]
		if r == nil || r.Stage != db.StageDone || r.ChatID != 7 || r.Lang != "en" || r.ResultMessageID != at {
			t.Fatalf("job %d: run = %+v, want message %d", job, r, at)
		}
	}
	if res := h.store.results[43]; res.FileID != "album-file-1" || res.FileType != db.FilePhoto {
		t.Fatalf("result of 43 = %+v", res)
	}

	h.tg.reset()
	h.flow.sendSite(context.Background())
	if len(h.tg.calls) != 0 {
		t.Fatalf("sent again: %+v", h.tg.calls)
	}
}

// What the frames share is said once: one model under the prompts of
// each, or nothing per frame when nothing differs.
func TestAPressCaptionSaysWhatDiffers(t *testing.T) {
	for name, c := range map[string]struct {
		prompts, models [2]string
		want            string
	}{
		"prompts": {[2]string{"a fox", "a  red\nfox"}, [2]string{"M", "M"},
			"<b>1</b> · a fox\n<b>2</b> · a red fox\n<b>M</b>"},
		"both": {[2]string{"a fox", "a <cat>"}, [2]string{"M & N", "O"},
			"<b>1</b> M &amp; N · a fox\n<b>2</b> O · a &lt;cat&gt;"},
		"nothing": {[2]string{"a fox", "a fox"}, [2]string{"M", "M"},
			"<blockquote>a fox</blockquote>\n<b>M</b>"},
	} {
		h := newHarness(t)
		pressJob(h, 41, c.prompts[0], c.models[0], []int32{0}, pressImage(70))
		pressJob(h, 42, c.prompts[1], c.models[1], []int32{1}, pressImage(71))
		h.flow.sendSite(context.Background())
		if sent := h.tg.methods("sendMediaGroup"); len(sent) != 1 || sent[0].text != c.want {
			t.Fatalf("%s: sent = %+v, want caption %q", name, sent, c.want)
		}
	}
	long := strings.Repeat("ж", albumPrompt+5)
	if got := shortPrompt(long); got != strings.Repeat("ж", albumPrompt)+"…" {
		t.Fatalf("short prompt = %q", got)
	}
}

// The videos of a press go as one album, each with its cover, and a run
// Continue joined goes as its joined clip.
func TestAPressOfVideosIsAnAlbumWithCovers(t *testing.T) {
	h := newHarness(t)
	clip := func(id int64, key string) storage.Asset {
		return storage.Asset{ID: id, UserID: 7, Kind: "result", Key: key, ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 5, Bytes: 2 << 20}
	}
	pressJob(h, 41, "a fox runs", "Kling", []int32{0}, clip(80, "u/7/out/41.mp4"))
	pressJob(h, 42, "a fox runs", "Veo", []int32{1}, clip(81, "u/7/out/42.mp4"))
	h.files.joined = map[int64]storage.Asset{42: clip(82, "u/7/out/42-joined.mp4")}
	h.flow.sendSite(context.Background())

	if a := h.tg.methods("sendChatAction"); len(a) != 1 || a[0].action != "upload_video" {
		t.Fatalf("action = %+v", a)
	}
	sent := h.tg.methods("sendMediaGroup")
	want := "video:upload:framy.mp4+cover:upload:cover.jpg,video:upload:framy.mp4+cover:upload:cover.jpg"
	if len(sent) != 1 || sent[0].file != want || !strings.Contains(sent[0].text, "<b>1</b> Kling") {
		t.Fatalf("sent = %+v", sent)
	}
	if !reflect.DeepEqual(h.files.opened, []string{"u/7/out/41.mp4", "u/7/out/42-joined.mp4"}) {
		t.Fatalf("read %q", h.files.opened)
	}
	if res := h.store.results[42]; res.FileType != db.FileVideo || res.FileID != "album-file-1" || res.AssetID != 82 {
		t.Fatalf("result of 42 = %+v", res)
	}
	if res := h.store.results[41]; res.FileID != "album-file" || res.AssetID != 0 {
		t.Fatalf("result of 41 = %+v", res)
	}

	// Covers Telegram refuses do not keep the videos back.
	h = newHarness(t)
	pressJob(h, 41, "a fox runs", "Kling", []int32{0}, clip(80, "u/7/out/41.mp4"))
	pressJob(h, 42, "a fox runs", "Veo", []int32{1}, clip(81, "u/7/out/42.mp4"))
	h.tg.fail["cover"] = telegramError(400, "Bad Request: wrong cover")
	h.flow.sendSite(context.Background())
	if sent := h.tg.methods("sendMediaGroup"); len(sent) != 1 || sent[0].file != "video:upload:framy.mp4,video:upload:framy.mp4" {
		t.Fatalf("sent = %+v", sent)
	}
	if h.store.delivery(41) != db.DeliverySent || h.store.delivery(42) != db.DeliverySent {
		t.Fatalf("delivery %q and %q", h.store.delivery(41), h.store.delivery(42))
	}
}

// A press whose other jobs failed is one result, sent as any: with Animate.
func TestAPressOfOneResultIsSentAlone(t *testing.T) {
	h := newHarness(t)
	pressJob(h, 41, "a fox", "Model A", []int32{1}, pressImage(70))
	h.flow.sendSite(context.Background())
	if n := len(h.tg.methods("sendMediaGroup")); n != 0 {
		t.Fatalf("%d albums", n)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].markup == nil || sent[0].markup.InlineKeyboard[0][0].CallbackData != callback(41, actAnimate) {
		t.Fatalf("sent = %+v", sent)
	}
	if h.store.delivery(41) != db.DeliverySent {
		t.Fatalf("delivery %q", h.store.delivery(41))
	}

	// A job of a press without a file fails, and the other goes alone.
	h = newHarness(t)
	pressJob(h, 41, "a fox", "Model A", []int32{0})
	pressJob(h, 42, "a fox", "Model B", []int32{1}, pressImage(71))
	h.flow.sendSite(context.Background())
	if h.store.delivery(41) != db.DeliveryFailed || h.store.delivery(42) != db.DeliverySent {
		t.Fatalf("delivery %q and %q", h.store.delivery(41), h.store.delivery(42))
	}
	if len(h.tg.methods("sendPhoto")) != 1 || len(h.tg.methods("sendMediaGroup")) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}

// A press ends whole: turned off, where the bot may not write, refused by
// Telegram; a hiccup gives the whole press back for the next round.
func TestAPressEndsWhole(t *testing.T) {
	press := func(h *harness) {
		pressJob(h, 41, "a fox", "Model A", []int32{0}, pressImage(70))
		pressJob(h, 42, "a fox", "Model B", []int32{1}, pressImage(71))
	}
	for name, c := range map[string]struct {
		set  func(h *harness)
		want string
	}{
		"off": {func(h *harness) {
			for i := range h.store.site {
				h.store.site[i].Deliver = false
			}
		}, db.DeliveryOff},
		"closed": {func(h *harness) {
			h.tg.fail["sendChatAction"] = telegramError(403, "Forbidden: bot was blocked by the user")
		}, db.DeliveryClosed},
		"refused": {func(h *harness) { h.tg.fail["sendMediaGroup"] = telegramError(400, "Bad Request: wrong file") }, db.DeliveryFailed},
		"hiccup":  {func(h *harness) { h.tg.fail["sendChatAction"] = errors.New("connection reset") }, ""},
	} {
		h := newHarness(t)
		press(h)
		c.set(h)
		h.flow.sendSite(context.Background())
		for _, job := range []int64{41, 42} {
			if got := h.store.delivery(job); got != c.want {
				t.Fatalf("%s: job %d delivery %q, want %q", name, job, got, c.want)
			}
		}
	}
}

// A round that was full may have cut its last press: that press waits for
// the next round, which starts with it.
func TestAPressIsNotSentInPieces(t *testing.T) {
	r := func(job int64, batch string) db.SiteResult { return db.SiteResult{JobID: job, Batch: batch} }
	ids := func(ps [][]db.SiteResult) [][]int64 {
		var out [][]int64
		for _, p := range ps {
			out = append(out, jobIDs(p))
		}
		return out
	}
	results := []db.SiteResult{r(1, ""), r(2, "a"), r(3, "a"), r(4, ""), r(5, "b"), r(6, "b")}
	if got, want := ids(pressesOf(results, false)), [][]int64{{1}, {2, 3}, {4}, {5, 6}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("presses %v, want %v", got, want)
	}
	if got, want := ids(pressesOf(results, true)), [][]int64{{1}, {2, 3}, {4}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("full round: presses %v, want %v", got, want)
	}
	if got, want := ids(pressesOf(results[:4], true)), [][]int64{{1}, {2, 3}, {4}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("full round ending alone: presses %v, want %v", got, want)
	}
}
