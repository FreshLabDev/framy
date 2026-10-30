// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
)

func num(v float64) *float64 { return &v }

// refs is the input_references parameter of an offer that takes up to max.
func refs(max float64) map[string]catalog.Param {
	return map[string]catalog.Param{"input_references": {Type: catalog.Range, Min: num(0), Max: num(max)}}
}

func offerOf(provider, model string, params map[string]catalog.Param) catalog.Offer {
	return catalog.Offer{ID: catalog.OfferID(provider, model), ModelID: model, Provider: provider, ProviderModelID: model, Kind: catalog.Image, Params: params}
}

// panelCatalog: the two free models, a featured one on OpenRouter and
// Gemini with settings, one only OpenAI runs, one that takes no photo, and
// a video model, which the panel of /img leaves out.
func panelCatalog() []catalog.Model {
	nano := refs(14)
	nano["aspect_ratio"] = catalog.Param{Type: catalog.Enum, Values: []any{"auto", "1:1", "16:9", "9:16", "4:3"}}
	nano["resolution"] = catalog.Param{Type: catalog.Enum, Values: []any{"1K", "2K", "4K"}}
	nano["n"] = catalog.Param{Type: catalog.Range, Min: num(1), Max: num(8)}
	nano["seed"] = catalog.Param{Type: catalog.Boolean}
	return []catalog.Model{
		{ID: "google/nano-banana-pro", Kind: catalog.Image, Name: "Nano Banana Pro", Maker: "Google", Featured: true, Offers: []catalog.Offer{
			offerOf(catalog.Gemini, "google/nano-banana-pro", nano), offerOf(catalog.OpenRouter, "google/nano-banana-pro", nano),
		}},
		{ID: "openai/gpt-image-2.5", Kind: catalog.Image, Name: "GPT Image 2.5", Maker: "OpenAI", Featured: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenAI, "openai/gpt-image-2.5", refs(16)),
		}},
		{ID: "recraft/v4.1", Kind: catalog.Image, Name: "Recraft V4.1", Maker: "Recraft", Featured: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "recraft/v4.1", nil),
		}, Traits: catalog.Traits{Family: &catalog.Family{ID: "recraft", Format: "raster"}}},
		{ID: "recraft/v4.1-vector", Kind: catalog.Image, Name: "Recraft V4.1 Vector", Maker: "Recraft", Featured: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "recraft/v4.1-vector", nil),
		}, Traits: catalog.Traits{Family: &catalog.Family{ID: "recraft", Format: "vector"}}},
		{ID: "heygen/heygen-video-1", Kind: catalog.Video, Name: "HeyGen Video", Featured: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "heygen/heygen-video-1", nil),
		}},
		{ID: "black-forest-labs/flux-3-image-klein", Kind: catalog.Image, Name: "FLUX 3 Image Klein", Maker: "Black Forest Labs", Free: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "black-forest-labs/flux-3-image-klein", refs(10)),
		}},
		{ID: "microsoft/mai-image-2.6-flash", Kind: catalog.Image, Name: "MAI-Image-2.6 Flash", Maker: "Microsoft AI", Free: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "microsoft/mai-image-2.6-flash", refs(5)),
		}},
	}
}

// panelHarness has the catalog above and an OpenRouter key, and ran
// /img on a photo in a group where framy is not an admin.
func panelHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.store.models = panelCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	return h
}

func run(t *testing.T, h *harness) db.ChatRun {
	t.Helper()
	r, err := h.store.ChatRun(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// labels are the texts of a keyboard, row by row.
func labels(kb *tg.InlineKeyboardMarkup) [][]string {
	var out [][]string
	for _, row := range kb.InlineKeyboard {
		var r []string
		for _, b := range row {
			r = append(r, b.Text)
		}
		out = append(out, r)
	}
	return out
}

// pick is the data of a model's button.
func pick(model string) string { return actPickModel + modelHash(model) }

// A free run has its settings fixed, so its preview has the model and no
// settings; a run on the key has both. The keyboard keeps Generate, No and
// Make a video.
func TestThePreviewHasModelAndSettingsWhenThereAreAny(t *testing.T) {
	onFree := newHarness(t)
	onFree.store.models = panelCatalog()
	if err := onFree.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	p := onFree.tg.methods("sendRichMessage")[0]
	if got := labels(p.markup); !reflect.DeepEqual(got, [][]string{
		{i18n.T("en", "run.generate"), i18n.T("en", "run.no")}, {i18n.T("en", "run.as_video")},
	}) || p.markup.InlineKeyboard[1][0].CallbackData != "j:1:v" {
		t.Fatalf("keyboard = %v", got)
	}
	if !strings.Contains(p.text, "<details><summary>Model: <b>MAI-Image-2.6 Flash</b></summary>") || strings.Contains(p.text, "Settings") {
		t.Fatalf("preview = %s", p.text)
	}

	h := panelHarness(t)
	if err := h.press(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	edits := h.tg.methods("editMessageRich")
	if len(edits) != 1 || edits[0].messageID != run(t, h).PreviewMessageID || edits[0].file != "card photo upload:framy.png" {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	e := edits[0]
	if got := lastCard(t, h); !reflect.DeepEqual(got, [][]string{{"j:1:go", "j:1:no"}, {"j:1:v"}}) {
		t.Fatalf("keyboard = %v", got)
	}
	// The catalog here has no prices: Generate says nothing of one.
	if gen := e.markup.InlineKeyboard[0][0].Text; gen != i18n.T("en", "run.generate") {
		t.Fatalf("generate = %q", gen)
	}
	// After a model, both lists are folded.
	if !strings.Contains(e.text, "<details><summary>Model: <b>Nano Banana Pro</b></summary>") ||
		!strings.Contains(e.text, "<details><summary>Settings: <b>Auto · 1K · ×1</b></summary>") {
		t.Fatalf("preview = %s", e.text)
	}
}

// The keyboard's panels below are those of a preview sent as a photo, as
// previews were before they were rich messages: such a preview keeps them
// until its draft expires, and a guest run has them still.

// In a group the list goes to the requester alone, over the preview the
// chat keeps seeing.
func TestModelOpensAPanelOnlyTheRequesterSees(t *testing.T) {
	h := panelHarness(t)
	before := run(t, h)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	p := sent[0]
	e := p.ephemeral
	if e == nil || e.ReceiverUserID != 7 || e.CallbackQueryID != "cq" || !e.ReplaceCallbackQueryMessage || p.file != "preview-file" || p.messageID != 0 {
		t.Fatalf("panel = %+v, %+v", p, e)
	}
	if !strings.HasPrefix(p.text, "<blockquote>make it night</blockquote>") {
		t.Fatalf("caption = %q", p.text)
	}
	// One to a row with its price, the free ones first, the current one
	// marked; a model the photo does not suit and the video are left out;
	// OpenAI's needs a key, so it folds into the last row.
	got := labels(p.markup)
	want := [][]string{{"FLUX 3 Image Klein · free"}, {"✓ MAI-Image-2.6 Flash · free"}, {"Nano Banana Pro"},
		{"🔑 " + i18n.T("en", "panel.more_key", "n", "1")}, {i18n.T("en", "btn.back")}}
	if len(got) != len(want) {
		t.Fatalf("list = %v", got)
	}
	for i := range want {
		if strings.Join(got[i], "|") != strings.Join(want[i], "|") {
			t.Fatalf("list = %v", got)
		}
	}
	if r := run(t, h); r.PanelMessageID == 0 || r.PreviewMessageID != before.PreviewMessageID {
		t.Fatalf("run = %+v", r)
	}
	if len(h.tg.methods("editMessageReplyMarkup")) != 0 {
		t.Fatal("the chat's preview changed")
	}
}

func TestARefusedPanelShowsTheListOnThePreview(t *testing.T) {
	h := panelHarness(t)
	h.tg.fail["ephemeral"] = errBoom
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	edits := h.tg.methods("editMessageReplyMarkup")
	if len(edits) != 1 || edits[0].messageID != run(t, h).PreviewMessageID || len(edits[0].markup.InlineKeyboard) != 5 {
		t.Fatalf("edits = %+v", edits)
	}
	if run(t, h).PanelMessageID != 0 {
		t.Fatal("a panel recorded")
	}
}

func TestPickingAModelRevisesTheDraftAndRedrawsTheCard(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	panel := run(t, h).PanelMessageID
	h.tg.reset()
	if err := h.pressPanel(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	// The OpenRouter key runs it, the offer of the provider with a key.
	if len(h.queue.revised) != 1 {
		t.Fatalf("revised = %+v", h.queue.revised)
	}
	// framy's defaults for the shape of the photo, as the site gives them,
	// with the one result the free run had.
	n := h.queue.revised[0]
	want := map[string]any{"aspect_ratio": "auto", "resolution": "1K", "n": 1}
	if n.Offer.ID != "openrouter:google/nano-banana-pro" || n.Free || n.Estimate == nil || !reflect.DeepEqual(n.Settings, want) {
		t.Fatalf("revision = %+v", n)
	}
	card := h.renderer.previews[len(h.renderer.previews)-1]
	if card.Model != "Nano Banana Pro" || card.Free || card.Label != i18n.T("en", "card.key", "provider", "OpenRouter") {
		t.Fatalf("card = %+v", card)
	}
	if photos := h.renderer.albums[len(h.renderer.albums)-1]; len(photos) != 1 {
		t.Fatalf("drawn from %v", photos)
	}
	// The chat's preview gets the card and keeps Generate; the panel gets
	// the same file and its main keyboard.
	pub := h.tg.methods("editMessageMedia")
	if len(pub) != 1 || pub[0].messageID != run(t, h).PreviewMessageID || pub[0].markup.InlineKeyboard[0][0].CallbackData != "j:1:go" {
		t.Fatalf("preview = %+v", pub)
	}
	eph := h.tg.methods("editEphemeralMessageMedia")
	if len(eph) != 1 || eph[0].messageID != panel || eph[0].action != "7" || eph[0].file != "photo result-file" ||
		eph[0].markup.InlineKeyboard[0][0].CallbackData != "j:1:go" {
		t.Fatalf("panel = %+v", eph)
	}
	if r := run(t, h); r.Free || r.OfferID != "openrouter:google/nano-banana-pro" || r.Model != "Nano Banana Pro" {
		t.Fatalf("run = %+v", r)
	}
	if got := h.tg.answers; len(got) != 1 || got[0] != "" {
		t.Fatalf("answers = %q", got)
	}
}

func TestAModelWithoutAKeyAsksForOne(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.pressPanel(1, 7, pick("openai/gpt-image-2.5")); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.answers; len(got) != 1 || got[0] != "alert:"+i18n.T("en", "panel.key_needed") {
		t.Fatalf("answers = %q", got)
	}
	if len(h.queue.revised) != 0 || len(h.tg.calls) != 0 {
		t.Fatalf("revised %+v, calls %+v", h.queue.revised, h.tg.calls)
	}
}

func TestTheSettingsOfAFreeRunAreFixed(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actSettings); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.answers; len(got) != 1 || got[0] != "alert:"+i18n.T("en", "panel.free_fixed") {
		t.Fatalf("answers = %q", got)
	}
	if len(h.tg.calls) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}

func TestASettingChangesTheRunOnTheKey(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	if err := h.pressPanel(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.pressPanel(1, 7, actSettings); err != nil {
		t.Fatal(err)
	}
	edits := h.tg.methods("editEphemeralMessageReplyMarkup")
	if len(edits) != 1 || edits[0].messageID != run(t, h).PanelMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	kb := edits[0].markup
	got := labels(kb)
	// A label row, then the values; the boolean seed is not offered.
	if got[0][0] != i18n.T("en", "set.aspect_ratio") || kb.InlineKeyboard[0][0].Disabled == nil || kb.InlineKeyboard[0][0].CallbackData != "" ||
		strings.Join(got[1], "|") != "✓ "+i18n.T("en", "web.val.auto")+"|1:1|16:9|9:16" || strings.Join(got[2], "|") != "4:3" ||
		got[3][0] != i18n.T("en", "set.resolution") || strings.Join(got[4], "|") != "✓ 1K|2K|4K" ||
		got[5][0] != i18n.T("en", "set.n") || strings.Join(got[6], "|") != "✓ ×1|×2|×3|×4" || len(got) != 8 {
		t.Fatalf("settings = %v", got)
	}
	if kb.InlineKeyboard[1][2].CallbackData != "j:1:S0.2" {
		t.Fatalf("16:9 = %+v", kb.InlineKeyboard[1][2])
	}
	h.tg.reset()
	if err := h.pressPanel(1, 7, actPickSetting+"0.2"); err != nil {
		t.Fatal(err)
	}
	n := h.queue.revised[len(h.queue.revised)-1]
	if n.Free || n.Settings["aspect_ratio"] != "16:9" || n.Settings["n"] != 1 || n.Offer.ID != "openrouter:google/nano-banana-pro" {
		t.Fatalf("revision = %+v", n)
	}
	if card := h.renderer.previews[len(h.renderer.previews)-1]; card.Aspect != "16:9" {
		t.Fatalf("card = %+v", card)
	}
	// The panel stays on the settings, with the pick marked.
	eph := h.tg.methods("editEphemeralMessageMedia")
	if len(eph) != 1 || labels(eph[0].markup)[1][2] != "✓ 16:9" {
		t.Fatalf("panel = %+v", eph)
	}
	// The same value again changes nothing.
	h.tg.reset()
	revised := len(h.queue.revised)
	if err := h.pressPanel(1, 7, actPickSetting+"0.2"); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != revised || len(h.tg.calls) != 0 {
		t.Fatalf("revised again: %+v", h.tg.calls)
	}
}

// A free model past the tier runs on the person's key when they have one.
func TestAFreeModelPastTheTierGoesOnTheKey(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	h.queue.refuse = &free.LimitError{Limit: 5}
	if err := h.pressPanel(1, 7, pick("black-forest-labs/flux-3-image-klein")); err != nil {
		t.Fatal(err)
	}
	n := h.queue.revised[len(h.queue.revised)-1]
	if n.Free || n.Offer.ID != free.ImageKlein.Offer || n.Settings["resolution"] != "1K" {
		t.Fatalf("revision = %+v", n)
	}
}

func TestAPanelGoesWithNo(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	r := run(t, h)
	h.tg.reset()
	if err := h.pressPanel(1, 7, actNo); err != nil {
		t.Fatal(err)
	}
	if del := h.tg.methods("deleteEphemeralMessage"); len(del) != 1 || del[0].messageID != r.PanelMessageID || del[0].action != "7" {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if del := h.tg.methods("deleteMessage"); len(del) != 1 || del[0].messageID != r.PreviewMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}

func TestGenerateOnThePanelTurnsThePreviewIntoTheLoop(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	r := run(t, h)
	h.tg.reset()
	if err := h.pressPanel(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if del := h.tg.methods("deleteEphemeralMessage"); len(del) != 1 || del[0].messageID != r.PanelMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if loop := h.tg.methods("editMessageMedia"); len(loop) != 1 || loop[0].messageID != r.PreviewMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if got := run(t, h); got.Stage != db.StageGenerating || got.PanelMessageID != 0 {
		t.Fatalf("run = %+v", got)
	}
}

// A press that names the run from a message that is not its own does
// nothing: an old panel, or an ephemeral id the preview never had.
func TestAPressFromAnotherEphemeralMessageDoesNothing(t *testing.T) {
	h := panelHarness(t)
	err := h.pressOn(1, 7, actNo, tg.Message{EphemeralMessageID: 999, Chat: group, Date: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.tg.calls) != 0 || h.store.stage(1) != db.StagePreview {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}

func TestAnAdminSendsThePreviewToTheRequesterAlone(t *testing.T) {
	h := newHarness(t)
	h.store.models = panelCatalog()
	h.flow.SetAlbums(fakeAlbums{admin: true})
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	p := h.tg.methods("sendRichMessage")[0]
	if p.ephemeral == nil || p.ephemeral.ReceiverUserID != 7 || p.ephemeral.CallbackQueryID != "" || p.messageID != 10 {
		t.Fatalf("preview = %+v", p)
	}
	r := run(t, h)
	if !r.PreviewEphemeral || r.PreviewMessageID == 0 || r.CommandEphemeral {
		t.Fatalf("run = %+v", r)
	}
	// A model changes the preview itself: it is the requester's already.
	h.tg.reset()
	if err := h.press(1, 7, pick("black-forest-labs/flux-3-image-klein")); err != nil {
		t.Fatal(err)
	}
	eph := h.tg.methods("editEphemeralMessageRich")
	if len(eph) != 1 || eph[0].messageID != r.PreviewMessageID || eph[0].action != "7" || eph[0].file != "card photo upload:framy.png" ||
		len(h.tg.methods("editMessageRich")) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	// Generate: the chat sees the loop, as a reply to the command.
	h.tg.reset()
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if del := h.tg.methods("deleteEphemeralMessage"); len(del) != 1 || del[0].messageID != r.PreviewMessageID {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	loop := h.tg.methods("sendAnimation")
	if len(loop) != 1 || loop[0].ephemeral != nil || loop[0].messageID != 10 || loop[0].markup.InlineKeyboard[0][0].CallbackData != "j:1:stop" {
		t.Fatalf("loop = %+v", loop)
	}
	got := run(t, h)
	if got.PreviewEphemeral || got.PreviewMessageID == 0 || got.PreviewMessageID == r.PreviewMessageID || got.Stage != db.StageGenerating {
		t.Fatalf("run = %+v", got)
	}
}

func TestARefusedEphemeralPreviewGoesToTheChat(t *testing.T) {
	h := newHarness(t)
	h.flow.SetAlbums(fakeAlbums{admin: true})
	h.tg.fail["ephemeral"] = errBoom
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].ephemeral != nil || sent[0].messageID != 10 {
		t.Fatalf("preview = %+v", sent)
	}
	if r := run(t, h); r.PreviewEphemeral {
		t.Fatalf("run = %+v", r)
	}
}

// A stop that cuts the ephemeral send short is not a refusal: the preview
// may have reached the requester already, so the chat gets no second one.
func TestAStopDuringTheEphemeralPreviewSendsNothingToTheChat(t *testing.T) {
	h := newHarness(t)
	h.flow.SetAlbums(fakeAlbums{admin: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.tg.beforeEphemeral = cancel
	h.tg.fail["ephemeral"] = context.Canceled
	_ = h.flow.command(ctx, command("/img a fox"), CommandImage, "a fox")
	if sent := h.tg.methods("sendRichMessage"); len(sent) != 0 {
		t.Fatalf("the chat got the preview: %+v", sent)
	}
}

// An ephemeral command is answered within its 15 seconds by a reply to it,
// which only its sender sees.
func TestAnEphemeralCommandGetsAnEphemeralPreview(t *testing.T) {
	h := newHarness(t)
	m := command("/img a fox")
	m.MessageID, m.EphemeralMessageID, m.Date = 0, 5, 1700000000
	if err := h.command(m, CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job.n.DedupeKey != "tg:-100:e5:1700000000" {
		t.Fatalf("key = %q", job.n.DedupeKey)
	}
	p := h.tg.methods("sendRichMessage")[0]
	if p.ephemeral == nil || p.ephemeral.ReceiverUserID != 7 || p.replyEphemeral != 5 || p.messageID != 0 {
		t.Fatalf("preview = %+v", p)
	}
	r := run(t, h)
	if !r.PreviewEphemeral || !r.CommandEphemeral {
		t.Fatalf("run = %+v", r)
	}
	// Generate posts the loop in the chat, with no command to reply to.
	h.tg.reset()
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	loop := h.tg.methods("sendAnimation")
	if len(loop) != 1 || loop[0].messageID != 0 || loop[0].ephemeral != nil {
		t.Fatalf("loop = %+v", loop)
	}
}

func TestAnEphemeralCommandIsRefusedToItsSenderAlone(t *testing.T) {
	h := newHarness(t)
	m := command("/img")
	m.MessageID, m.EphemeralMessageID = 0, 5
	if err := h.command(m, CommandImage, ""); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendMessage")
	if len(sent) != 1 || sent[0].ephemeral == nil || sent[0].ephemeral.ReceiverUserID != 7 || sent[0].replyEphemeral != 5 || sent[0].text != i18n.T("en", "run.empty") {
		t.Fatalf("reply = %+v", sent)
	}
	h.tg.reset()
	h.tg.fail["ephemeral"] = errBoom
	if err := h.command(m, CommandImage, ""); err != nil {
		t.Fatal(err)
	}
	if sent := h.tg.methods("sendMessage"); len(sent) != 1 || sent[0].ephemeral != nil {
		t.Fatalf("reply = %+v", sent)
	}
}

// In the bot's DM the list is on the preview itself.
func TestInADirectChatTheListIsOnThePreview(t *testing.T) {
	h := newHarness(t)
	h.store.models = panelCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	m := command("/img a fox")
	m.Chat = tg.Chat{ID: 7, Type: "private"}
	if err := h.command(m, CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	edits := h.tg.methods("editMessageReplyMarkup")
	if len(edits) != 1 || len(h.tg.methods("sendPhoto")) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	// From words alone Recraft, which takes no photo, is listed too, but
	// not its vector variant: Telegram shows an SVG as a file.
	if got := labels(edits[0].markup); len(got) != 6 || got[3][0] != "Recraft V4.1" || got[4][0] != "🔑 "+i18n.T("en", "panel.more_key", "n", "1") {
		t.Fatalf("list = %v", got)
	}
	h.tg.reset()
	if err := h.pressPhoto(1, 7, actBack); err != nil {
		t.Fatal(err)
	}
	if edits := h.tg.methods("editMessageReplyMarkup"); len(edits) != 1 || edits[0].markup.InlineKeyboard[0][0].CallbackData != "j:1:go" {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}

// After Generate the panel's buttons do nothing.
func TestThePanelIsForAPreviewOnly(t *testing.T) {
	h := panelHarness(t)
	if err := h.pressPhoto(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.calls) != 0 || h.store.jobs[1].status != jobs.Queued {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}
