// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/storage"
)

// gifResult is a GIF of 1024×1024, as GPT Image 2.6 makes one.
var gifResult = storage.Asset{ID: 70, ContentType: "image/gif", Bytes: 3 << 20, Width: 1024, Height: 1024}

func gifOf(t *testing.T, w, h int) []byte {
	t.Helper()
	pal := color.Palette{color.NRGBA{R: 255, A: 255}, color.NRGBA{B: 255, A: 255}}
	frame := func(i uint8) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for k := range p.Pix {
			p.Pix[k] = i
		}
		return p
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{frame(0), frame(1)}, Delay: []int{20, 20}}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A GIF that came quickly takes the loop's place as an animation, which
// Telegram loops, with Again under it: no Animate, since no video model
// takes a GIF as a frame, and no Original.
func TestAQuickGIFTakesTheLoopsPlaceAsAnAnimation(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	h.tg.reset()
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{gifResult}
	h.now = h.now.Add(12 * time.Second)
	h.flow.step(context.Background(), id)
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].messageID != loop || edits[0].file != "animation upload:framy.gif" {
		t.Fatalf("edits %+v, calls %+v", edits, h.tg.calls)
	}
	if got := labels(edits[0].markup); !slices.Equal(slices.Concat(got...), []string{i18n.T("en", "web.act.again")}) {
		t.Errorf("buttons = %v, want Again alone", got)
	}
	if len(h.tg.methods("sendPhoto")) != 0 || h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}

// One that came late is a reply to the command, as an animation, and the
// loop goes.
func TestALateGIFRepliesAsAnAnimation(t *testing.T) {
	h := newHarness(t)
	id := h.dmGenerating(t)
	loop := h.store.runs[id].PreviewMessageID
	h.tg.reset()
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{gifResult}
	h.now = h.now.Add(10 * time.Minute)
	h.flow.step(context.Background(), id)
	sent := h.tg.methods("sendAnimation")
	if len(sent) != 1 || sent[0].file != "upload:framy.gif" || !strings.Contains(sent[0].text, "a fox") || len(h.tg.methods("editMessageMedia")) != 0 {
		t.Fatalf("sent %+v, calls %+v", sent, h.tg.calls)
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != loop {
		t.Fatalf("the loop stayed: %+v", d)
	}
	if res := h.store.results[id]; res.FileID != "anim-file" || res.FileType != db.FileAnimation {
		t.Fatalf("result %+v", res)
	}
}

// An album takes no animation: several GIFs come one after another, the
// caption on the first, and the run is on the first.
func TestSeveralGIFsComeOneByOne(t *testing.T) {
	h := newHarness(t)
	h.store.models = panelCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	if err := h.command(photoSaid("make it move"), CommandImage, "make it move"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.store.setStatus(1, jobs.Done, "")
	second := gifResult
	second.ID = 71
	h.files.results[1] = []storage.Asset{gifResult, second}
	h.now = h.now.Add(12 * time.Second)
	before := h.tg.next
	h.flow.step(context.Background(), 1)
	sent := h.tg.methods("sendAnimation")
	if len(sent) != 2 || !strings.Contains(sent[0].text, "make it move") || sent[1].text != "" || len(h.tg.methods("sendMediaGroup")) != 0 {
		t.Fatalf("sent %+v, calls %+v", sent, h.tg.calls)
	}
	first := before + 1 + int64(slices.IndexFunc(h.tg.calls, func(c call) bool { return c.method == "sendAnimation" }))
	if h.store.stage(1) != db.StageDone || h.store.results[1].MessageID != first {
		t.Fatalf("stage %s, result %+v", h.store.stage(1), h.store.results[1])
	}
}

// A GIF sent before goes again from inline mode by its file_id, as the
// MP4 Telegram made of it.
func TestAGIFGoesAgainInlineAsTheClipTelegramMade(t *testing.T) {
	r := inlineResult(db.InlineResult{JobID: 5, Prompt: "a fox that moves", Model: "GPT Image 2.6 Sunburst", FileID: "anim", FileType: db.FileAnimation})
	g, ok := r.(tg.InlineQueryResultCachedMpeg4Gif)
	if !ok || g.ID != "j5" || g.Mpeg4FileID != "anim" || g.Caption != resultCaption("a fox that moves", "GPT Image 2.6 Sunburst") {
		t.Fatalf("result = %#v", r)
	}
}

// A GIF that never went out goes by a link to the MP4 framy makes of it,
// the clip Telegram plays an animation as, with its first frame as the
// thumbnail; it has no photo or video link.
func TestAGIFIsSharedAsTheClipTelegramPlays(t *testing.T) {
	h := shareHarness(t, db.ShareSource{AssetID: 70, JobID: 9, Key: "u/7/r/70.gif", ContentType: "image/gif", Bytes: 400,
		Width: 64, Height: 32, Prompt: "a fox that moves", Model: "GPT Image 2.6 Sunburst"})
	raw := gifOf(t, 64, 32)
	h.files.blobs["u/7/r/70.gif"] = raw
	if _, err := h.flow.Share(context.Background(), 7, 70); err != nil {
		t.Fatal(err)
	}
	g, ok := h.tg.prepared[0].(tg.InlineQueryResultMpeg4Gif)
	if !ok || g.ID != "f70" || g.Mpeg4Width != 64 || g.Mpeg4Height != 32 || g.Title != "a fox that moves" ||
		!strings.HasSuffix(g.Mpeg4URL, ".mp4") || !strings.HasSuffix(g.ThumbnailURL, ".jpg") {
		t.Fatalf("result = %+v", h.tg.prepared[0])
	}
	rec := h.fetch(t, g.Mpeg4URL)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "video/mp4" || !bytes.Equal(rec.Body.Bytes(), append([]byte("mp4:"), raw...)) {
		t.Fatalf("clip: %d %v", rec.Code, rec.Header())
	}
	thumb := jpegSize(t, h.fetch(t, g.ThumbnailURL))
	if b := thumb.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		t.Fatalf("thumbnail %v", b)
	}
	// The first frame is red.
	if r, _, b, _ := thumb.At(32, 16).RGBA(); r>>8 < 200 || b>>8 > 60 {
		t.Errorf("the thumbnail is not the first frame: r %d b %d", r>>8, b>>8)
	}
	for _, part := range []byte{sharePhoto, shareVideo, shareCover} {
		if rec := h.fetch(t, h.flow.shareURL(7, 70, part)); rec.Code != http.StatusNotFound {
			t.Errorf("part %c of a GIF: %d", part, rec.Code)
		}
	}
	if rec := h.fetch(t, h.flow.shareURL(7, 70, shareGIF)); rec.Code != http.StatusOK {
		t.Errorf("a GIF's clip: %d", rec.Code)
	}
}

// gifCatalog has GPT Image 2.6 on OpenAI, as catalog/openai.yaml lists it.
func gifCatalog() []catalog.Model {
	params := refs(16)
	params["size"] = catalog.Param{Type: catalog.Enum, Values: []any{"1024x1024", "1536x1024", "2048x2048"}}
	params["quality"] = catalog.Param{Type: catalog.Enum, Values: []any{"low", "medium", "high"}}
	params["background"] = catalog.Param{Type: catalog.Enum, Values: []any{"auto", "transparent", "opaque"},
		Only: map[string][]any{"output_format": {"png", "jpeg", "webp"}}}
	params["output_format"] = catalog.Param{Type: catalog.Enum, Values: []any{"png", "jpeg", "webp", "gif"}, Default: "png"}
	params["frame_count"] = catalog.Param{Type: catalog.Range, Min: num(2), Max: num(16), Default: 9.0, Only: map[string][]any{"output_format": {"gif"}}}
	params["frame_duration_ms"] = catalog.Param{Type: catalog.Range, Min: num(20), Max: num(2000), Default: 200.0, Only: map[string][]any{"output_format": {"gif"}}}
	return []catalog.Model{{ID: "openai/gpt-image-2.6-sunburst", Kind: catalog.Image, Name: "GPT Image 2.6 Sunburst", Maker: "OpenAI", Featured: true,
		Offers: []catalog.Offer{offerOf(catalog.OpenAI, "openai/gpt-image-2.6-sunburst", params)}}}
}

// /img asks for a GIF as the site does: Image or GIF on the preview, and
// a GIF brings its frames and their length, 9 of 200 ms until changed, and
// leaves the background, which OpenAI takes none of with a GIF.
func TestTheChatAsksForAGIF(t *testing.T) {
	h := newHarness(t)
	h.store.models = gifCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenAI, Status: keys.Valid}}}
	m := command("/img a fox that waves")
	m.Chat = dm
	if err := h.command(m, CommandImage, "a fox that waves"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, pick("openai/gpt-image-2.6-sunburst")); err != nil {
		t.Fatal(err)
	}
	rich := h.tg.methods("editMessageRich")
	if len(rich) == 0 {
		t.Fatalf("calls %+v", h.tg.calls)
	}
	still := rich[len(rich)-1].text
	gifButton := `data="j:1:` + actPickSetting + "output_format#" + valueHash("gif") + `">GIF<`
	if !strings.Contains(still, gifButton) || !strings.Contains(still, ">Image<") || strings.Contains(still, i18n.T("en", "web.set.frame_count")) ||
		!strings.Contains(still, i18n.T("en", "web.set.background")) {
		t.Fatalf("preview = %s", still)
	}
	h.tg.reset()
	if err := h.press(1, 7, actPickSetting+"output_format#"+valueHash("gif")); err != nil {
		t.Fatal(err)
	}
	n := h.queue.revised[len(h.queue.revised)-1]
	if n.Settings["output_format"] != "gif" || n.Settings["frame_count"] != 9.0 || n.Settings["frame_duration_ms"] != 200.0 {
		t.Fatalf("revision = %+v", n.Settings)
	}
	if _, ok := n.Settings["background"]; ok {
		t.Fatalf("a GIF went with a background: %+v", n.Settings)
	}
	rich = h.tg.methods("editMessageRich")
	if len(rich) != 1 {
		t.Fatalf("calls %+v", h.tg.calls)
	}
	gifs := rich[0].text
	for _, want := range []string{i18n.T("en", "web.set.frame_count"), i18n.T("en", "web.set.frame_duration_ms"), ">16<", ">200 ms<",
		"GIF · " + i18n.T("en", "web.unit.frames", "n", "9") + " · 200 ms"} {
		if !strings.Contains(gifs, want) {
			t.Errorf("the GIF's preview has no %q: %s", want, gifs)
		}
	}
	if strings.Contains(gifs, i18n.T("en", "web.set.background")) {
		t.Errorf("a GIF's preview offers a background: %s", gifs)
	}
	// Twelve frames, and back to an image, which leaves them.
	if err := h.press(1, 7, actPickSetting+"frame_count#"+valueHash(12)); err != nil {
		t.Fatal(err)
	}
	if n := h.queue.revised[len(h.queue.revised)-1]; n.Settings["frame_count"] != 12 {
		t.Fatalf("revision = %+v", n.Settings)
	}
	if err := h.press(1, 7, actPickSetting+"output_format#"+valueHash("png")); err != nil {
		t.Fatal(err)
	}
	n = h.queue.revised[len(h.queue.revised)-1]
	if _, ok := n.Settings["frame_count"]; ok || n.Settings["output_format"] != "png" || n.Settings["background"] == nil {
		t.Fatalf("revision = %+v", n.Settings)
	}
}

// A guest run's GIF takes the answer's place as an animation Telegram
// fetches: the clip framy makes of it.
func TestAGuestGIFIsAnAnimationByLink(t *testing.T) {
	h := guestHarness(t)
	run := h.guestRun(t, "make it snow")
	if err := h.pressInline(1, 7, actGenerate, run.InlineMessageID); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.store.setStatus(1, jobs.Done, "")
	h.files.results[1] = []storage.Asset{gifResult}
	h.flow.step(context.Background(), 1)
	res := h.tg.methods("editInlineMessageMedia")
	if len(res) != 1 || !strings.HasPrefix(res[0].file, "animation "+shareSite+SharePath) || !strings.Contains(res[0].file, ".mp4") ||
		res[0].text != resultCaption("make it snow", "MAI Image 2.6 Flash") {
		t.Fatalf("result = %+v", res)
	}
	if h.store.stage(1) != db.StageDone || len(h.chatCalls()) != 0 {
		t.Errorf("stage %s, chat calls %+v", h.store.stage(1), h.chatCalls())
	}
}
