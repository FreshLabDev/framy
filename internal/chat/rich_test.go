// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/price"
)

func inFamily(id, name, label string, rank int) catalog.Traits {
	return catalog.Traits{Family: &catalog.Family{ID: id, Name: name, Label: label, Rank: rank}}
}

// priced gives an offer one route at cost dollars an image.
func withPrice(o catalog.Offer, cost float64) catalog.Offer {
	o.Routes = []catalog.Route{{Prices: []catalog.Price{{Billable: "output_image", Unit: "image", CostUSD: cost}}}}
	return o
}

// richCatalog is a catalog in the order GET /api/catalog lists it
// (featured first, then by name): a family of two current variants, an
// outdated one and a fast mode, a model only OpenAI runs, one that takes no
// photo, one that needs one, makers written in either case, a vector model
// and a video one.
func richCatalog() []catalog.Model {
	edit := refs(4)
	edit["input_references"] = catalog.Param{Type: catalog.Range, Min: num(1), Max: num(4)}
	outdated := inFamily("nano", "Nano Banana", "1", 2)
	outdated.Family.OutdatedBy = "google/nano-banana-2"
	return []catalog.Model{
		{ID: "openai/gpt-image-2.5", Kind: catalog.Image, Name: "GPT Image 2.5", Maker: "OpenAI", Featured: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenAI, "openai/gpt-image-2.5", refs(16)),
		}},
		{ID: "google/nano-banana-2", Kind: catalog.Image, Name: "Nano Banana 2", Maker: "Google", Featured: true, Offers: []catalog.Offer{
			withPrice(offerOf(catalog.OpenRouter, "google/nano-banana-2", refs(14)), 0.034),
		}, Traits: inFamily("nano", "Nano Banana", "2", 1)},
		{ID: "google/nano-banana-pro", Kind: catalog.Image, Name: "Nano Banana Pro", Maker: "Google", Featured: true, Offers: []catalog.Offer{
			withPrice(offerOf(catalog.OpenRouter, "google/nano-banana-pro", refs(14)), 0.134),
		}, Traits: inFamily("nano", "Nano Banana", "Pro", 0)},
		{ID: "google/nano-banana-pro-fast", Kind: catalog.Image, Name: "Nano Banana Pro Fast", Maker: "Google", Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "google/nano-banana-pro-fast", refs(14)),
		}, Traits: catalog.Traits{FastOf: "google/nano-banana-pro"}},
		{ID: "google/nano-banana-1", Kind: catalog.Image, Name: "Nano Banana 1", Maker: "Google", Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "google/nano-banana-1", refs(4)),
		}, Traits: outdated},
		{ID: "black-forest-labs/flux-3-image-klein", Kind: catalog.Image, Name: "FLUX 3 Image Klein", Maker: "Black Forest Labs", Free: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "black-forest-labs/flux-3-image-klein", refs(10)),
		}},
		{ID: "bytedance/seededit-4", Kind: catalog.Image, Name: "SeedEdit 4", Maker: "bytedance", Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "bytedance/seededit-4", edit),
		}},
		{ID: "heygen/heygen-video-1", Kind: catalog.Video, Name: "HeyGen Video", Maker: "HeyGen", Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "heygen/heygen-video-1", nil),
		}},
		{ID: "microsoft/mai-image-2.6-flash", Kind: catalog.Image, Name: "MAI-Image-2.6 Flash", Maker: "Microsoft AI", Free: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "microsoft/mai-image-2.6-flash", refs(5)),
		}},
		{ID: "recraft/v4.1", Kind: catalog.Image, Name: "Recraft V4.1", Maker: "Recraft", Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "recraft/v4.1", nil),
		}, Traits: catalog.Traits{Family: &catalog.Family{ID: "recraft", Format: "raster"}}},
		{ID: "recraft/v4.1-vector", Kind: catalog.Image, Name: "Recraft V4.1 Vector", Maker: "Recraft", Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "recraft/v4.1-vector", nil),
		}, Traits: catalog.Traits{Family: &catalog.Family{ID: "recraft", Format: "vector"}}},
	}
}

// buttonsOf are the rich buttons of an HTML, in order: the text, and "-"
// for a disabled one, "*" for the marked one.
func buttonsOf(body string) []string {
	var out []string
	for _, part := range strings.Split(body, "<tg-button ")[1:] {
		head, rest, _ := strings.Cut(part, ">")
		text, _, _ := strings.Cut(rest, "</tg-button>")
		switch {
		case strings.Contains(head, `type="disabled"`):
			text = "-" + text
		case strings.Contains(head, `style="primary"`):
			text = "*" + text
		}
		out = append(out, text)
	}
	return out
}

// The list is the site's picker in its default order: makers A to Z
// whatever their case, each under its name even with one model, a maker's
// families in the catalog's order and a family's variants strongest first,
// named by family and label. A fast mode, an outdated variant, a vector
// model and a video are not listed. A model the photo does not suit, or
// that needs a key the person has not connected, keeps its place, disabled,
// with the reason.
func TestTheListIsTheSitesPicker(t *testing.T) {
	h := newHarness(t)
	h.store.models = richCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	body := h.tg.methods("sendRichMessage")[0].text
	free, key := i18n.T("en", "card.free"), i18n.T("en", "run.need_key", "provider", "OpenAI")
	want := []string{
		"FLUX 3 Image Klein · " + free,
		"SeedEdit 4",
		"Nano Banana Pro · $0.14", "Nano Banana 2 · $0.034",
		"*MAI-Image-2.6 Flash · " + free,
		"-GPT Image 2.5 · " + key,
		"-Recraft V4.1 · " + i18n.T("en", "web.misfit.no_photos"),
	}
	if got := buttonsOf(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("buttons = %q", got)
	}
	var makers []string
	for _, part := range strings.Split(body, "<p><b>")[1:] {
		name, _, _ := strings.Cut(part, "</b></p>")
		makers = append(makers, name)
	}
	if !reflect.DeepEqual(makers, []string{"Black Forest Labs", "bytedance", "Google", "Microsoft AI", "OpenAI", "Recraft"}) {
		t.Fatalf("makers = %q", makers)
	}
	if !strings.HasPrefix(body, `<img src="tg://photo?id=card"/><blockquote>make it night</blockquote><details><summary>Model: <b>MAI-Image-2.6 Flash</b></summary>`) {
		t.Fatalf("preview = %s", body)
	}
	// A model's button picks it by the hash of its id.
	if !strings.Contains(body, `data="j:1:`+pick("google/nano-banana-pro")+`">Nano Banana Pro`) {
		t.Fatalf("preview = %s", body)
	}

	// From words alone, the model that edits a photo needs one, and Recraft
	// takes the run.
	h = newHarness(t)
	h.store.models = richCatalog()
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	got := buttonsOf(h.tg.methods("sendRichMessage")[0].text)
	needs := i18n.T("en", "web.misfit.needs_photo")
	if got[1] != "-SeedEdit 4 · "+needs || got[len(got)-1] != "-Recraft V4.1 · "+i18n.T("en", "run.need_key", "provider", "OpenRouter") {
		t.Fatalf("buttons = %q", got)
	}
}

// A disabled model cannot be pressed; data forged for one changes nothing
// and says why.
func TestAModelTheRunCannotTakeIsRefused(t *testing.T) {
	h := newHarness(t)
	h.store.models = richCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	for model, alert := range map[string]string{
		"openai/gpt-image-2.5": i18n.T("en", "panel.key_needed"),
		"recraft/v4.1":         i18n.T("en", "web.misfit.no_photos"),
	} {
		h.tg.answers = nil
		if err := h.press(1, 7, pick(model)); err != nil {
			t.Fatal(err)
		}
		if got := h.tg.answers; len(got) != 1 || got[0] != "alert:"+alert {
			t.Fatalf("%s: answers = %q", model, got)
		}
	}
	if len(h.queue.revised) != 0 || len(h.tg.calls) != 0 {
		t.Fatalf("revised %+v, calls %+v", h.queue.revised, h.tg.calls)
	}
}

// A setting is picked by its name, and the card comes back with the
// settings open, the pick marked and in their title. A button sent before
// the values were hashed, which numbers the value, still works.
func TestASettingPickKeepsTheSettingsOpen(t *testing.T) {
	h := panelHarness(t)
	if err := h.press(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.press(1, 7, actPickSetting+"resolution.1"); err != nil {
		t.Fatal(err)
	}
	n := h.queue.revised[len(h.queue.revised)-1]
	if n.Free || n.Settings["resolution"] != "2K" || n.Settings["aspect_ratio"] != "auto" || n.Offer.ID != "openrouter:google/nano-banana-pro" {
		t.Fatalf("revision = %+v", n)
	}
	edits := h.tg.methods("editMessageRich")
	if len(edits) != 1 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	body := edits[0].text
	if !strings.Contains(body, "<details open><summary>Settings: <b>Auto · 2K</b></summary>") ||
		!strings.Contains(body, `<tg-button type="callback_data" style="primary" data="j:1:Sresolution#`+valueHash("2K")+`">2K</tg-button>`) {
		t.Fatalf("preview = %s", body)
	}
	// The same value again changes nothing.
	h.tg.reset()
	revised := len(h.queue.revised)
	if err := h.press(1, 7, actPickSetting+"resolution#"+valueHash("2K")); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != revised || len(h.tg.calls) != 0 {
		t.Fatalf("revised again: %+v", h.tg.calls)
	}
}

// A setting's button means the value it showed: the hourly sync may put a
// new value before it while the preview is up, and the press still picks
// it, not its new neighbour. One the model no longer has changes nothing,
// and the settings come back as they are now, with a line saying so.
func TestASettingPressMeansTheValueItShowed(t *testing.T) {
	h := panelHarness(t)
	if err := h.press(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	wide := actPickSetting + "aspect_ratio#" + valueHash("16:9")
	if body := h.tg.methods("editMessageRich")[0].text; !strings.Contains(body, `data="j:1:`+wide+`">16:9<`) {
		t.Fatalf("preview = %s", body)
	}
	// Both offers of the model share these parameters.
	h.store.models[0].Offers[0].Params["aspect_ratio"] = catalog.Param{Type: catalog.Enum, Values: []any{"auto", "1:1", "21:9", "16:9", "9:16"}}
	h.tg.reset()
	if err := h.press(1, 7, wide); err != nil {
		t.Fatal(err)
	}
	if n := h.queue.revised[len(h.queue.revised)-1]; n.Settings["aspect_ratio"] != "16:9" {
		t.Fatalf("revision = %+v", n.Settings)
	}
	h.tg.reset()
	revised := len(h.queue.revised)
	if err := h.press(1, 7, actPickSetting+"aspect_ratio#"+valueHash("4:3")); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != revised {
		t.Fatalf("revised: %+v", h.queue.revised[revised:])
	}
	edits := h.tg.methods("editMessageRich")
	if len(edits) != 1 || !strings.Contains(edits[0].text, "<details open><summary>Settings: <b>16:9 · 1K</b></summary>") ||
		!strings.Contains(edits[0].text, ">21:9<") || strings.Contains(edits[0].text, ">4:3<") {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if !reflect.DeepEqual(h.tg.answers, []string{i18n.T("en", "run.setting_gone")}) {
		t.Fatalf("answers = %q", h.tg.answers)
	}
}

// The settings are the composer's first ones: a size only where there is
// neither an aspect ratio nor a resolution, the counts up to four where
// results may be several, a long list of lengths cut to the round ones,
// and sound as two words.
func TestTheSettingsAreTheComposers(t *testing.T) {
	image := catalog.Offer{Kind: catalog.Image, Params: map[string]catalog.Param{
		"aspect_ratio": {Type: catalog.Enum, Values: []any{"1:1", "16:9"}},
		"size":         {Type: catalog.Enum, Values: []any{"1024x1024", "1536x1024"}},
		"quality":      {Type: catalog.Enum, Values: []any{"low", "medium", "high"}},
		"background":   {Type: catalog.Enum, Values: []any{"auto", "transparent", "opaque"}},
		"n":            {Type: catalog.Range, Min: num(1), Max: num(10)},
		"seed":         {Type: catalog.Boolean},
		"style":        {Type: catalog.Enum, Values: []any{"a", "b"}, Passthrough: true},
	}}
	names := func(sets []setting) []string {
		var out []string
		for _, s := range sets {
			out = append(out, s.name)
		}
		return out
	}
	sets := settingsOf(image, true)
	if got := names(sets); !reflect.DeepEqual(got, []string{"aspect_ratio", "quality", "background", "n"}) {
		t.Fatalf("settings = %v", got)
	}
	if !reflect.DeepEqual(sets[3].values, []any{1, 2, 3, 4}) {
		t.Fatalf("counts = %v", sets[3].values)
	}
	// One result is the rule where they may not be several.
	if got := names(settingsOf(image, false)); !reflect.DeepEqual(got, []string{"aspect_ratio", "quality", "background"}) {
		t.Fatalf("settings = %v", got)
	}
	// An upstream of OpenRouter that makes two at once makes the counts two.
	image.Routes = []catalog.Route{{Provider: "a", Params: map[string]catalog.Param{"n": {Type: catalog.Range, Max: num(2)}}}}
	if sets := settingsOf(image, true); !reflect.DeepEqual(sets[3].values, []any{1, 2}) {
		t.Fatalf("counts = %v", sets[3].values)
	}
	delete(image.Params, "aspect_ratio")
	if got := names(settingsOf(image, true)); got[len(got)-1] != "size" {
		t.Fatalf("settings = %v", got)
	}

	var lengths []any
	for s := 4.0; s <= 20; s++ {
		lengths = append(lengths, s)
	}
	video := catalog.Offer{Kind: catalog.Video, Params: map[string]catalog.Param{
		"duration":       {Type: catalog.Enum, Values: lengths},
		"generate_audio": {Type: catalog.Boolean},
		"size":           {Type: catalog.Enum, Values: []any{"1280x720", "720x1280"}},
	}}
	sets = settingsOf(video, true)
	if got := names(sets); !reflect.DeepEqual(got, []string{"duration", "generate_audio"}) {
		t.Fatalf("settings = %v", got)
	}
	if !reflect.DeepEqual(sets[0].values, []any{4.0, 5.0, 10.0, 15.0, 20.0}) {
		t.Fatalf("lengths = %v", sets[0].values)
	}
	d := draft{pay: payKey, offer: video, settings: map[string]any{"duration": 10, "generate_audio": true}}
	body := richSettings("en", 1, d, false)
	if !strings.HasPrefix(body, "<details><summary>Settings: <b>"+i18n.T("en", "web.unit.s", "n", "10")+" · "+i18n.T("en", "web.val.with_sound")+"</b></summary>") ||
		!strings.Contains(body, `data="j:1:Sgenerate_audio#`+valueHash(false)+`">`+i18n.T("en", "web.val.no_sound")+"<") {
		t.Fatalf("settings = %s", body)
	}
	// A free run has its settings fixed.
	d.pay = payFree
	if body := richSettings("en", 1, d, false); body != "" {
		t.Fatalf("settings = %s", body)
	}
}

// Generate says what the run costs on the person's key, in their
// currency; a free run's says nothing of money.
func TestGenerateSaysWhatTheRunCosts(t *testing.T) {
	d := draft{pay: payKey, offer: catalog.Offer{Kind: catalog.Image}, taken: 1,
		estimate: price.Estimate{Kind: price.About, USD: 0.134}, quote: fx.Quote{Code: "UAH", PerUSD: 41.5}}
	kb := richKeyboard("en", 1, d)
	if got := labels(kb); !reflect.DeepEqual(got, [][]string{
		{i18n.T("en", "run.generate") + " · ≈ " + fx.Format(0.134*41.5, "UAH", "en"), i18n.T("en", "run.no")},
		{i18n.T("en", "run.as_video")},
	}) {
		t.Fatalf("keyboard = %v", got)
	}
	d.pay = payFree
	if got := richKeyboard("en", 1, d).InlineKeyboard[0][0].Text; got != i18n.T("en", "run.generate") {
		t.Fatalf("generate = %q", got)
	}
}

// The local price says what the dollars say: a ceiling or a floor stays
// one in hryvnias, and only an exact price in dollars is "about" in them.
// The buttons show the local figure alone, so it must carry the bound.
func TestTheLocalPriceKeepsTheDollarsSign(t *testing.T) {
	q := fx.Quote{Code: "UAH", PerUSD: 41.5}
	for _, c := range []struct {
		e          price.Estimate
		usd, local string
	}{
		{price.Estimate{Kind: price.Exact, USD: 0.18}, "", "≈ "},
		{price.Estimate{Kind: price.About, USD: 0.18}, "≈ ", "≈ "},
		{price.Estimate{Kind: price.UpTo, USD: 0.05, MaxUSD: 0.18}, "≤ ", "≤ "},
		{price.Estimate{Kind: price.From, USD: 0.18}, "≥ ", "≥ "},
	} {
		usd, local, code := cardPrice("uk", c.e, q)
		if want := c.usd + fx.Format(0.18, fx.USD, "uk"); usd != want {
			t.Errorf("%s: usd = %q, want %q", c.e.Kind, usd, want)
		}
		if want := c.local + fx.Format(0.18*41.5, "UAH", "uk"); local != want {
			t.Errorf("%s: local = %q, want %q", c.e.Kind, local, want)
		}
		if want := c.local + fx.FormatCode(0.18*41.5, "UAH", "uk"); code != want {
			t.Errorf("%s: code = %q, want %q", c.e.Kind, code, want)
		}
		d := draft{pay: payKey, offer: catalog.Offer{Kind: catalog.Image}, taken: 1, estimate: c.e, quote: q}
		if got, want := richKeyboard("uk", 1, d).InlineKeyboard[0][0].Text, i18n.T("uk", "run.generate")+" · "+local; got != want {
			t.Errorf("%s: generate = %q, want %q", c.e.Kind, got, want)
		}
	}
}

// A long prompt folds, and its lines stay lines: a rich message reads its
// HTML as a page does.
func TestTheRichQuoteKeepsTheLines(t *testing.T) {
	if got := richQuote("a fox\n<in> snow"); got != "<blockquote>a fox<br>&lt;in&gt; snow</blockquote>" {
		t.Fatalf("quote = %q", got)
	}
	if got := richQuote(strings.Repeat("fox ", 60)); !strings.HasPrefix(got, "<blockquote expandable>") {
		t.Fatalf("quote = %q", got)
	}
}

// withKling adds a video model on the key with frames, lengths, sound
// and resolutions, priced by the second.
func withKling(h *harness) {
	of := catalog.Offer{
		ID: "openrouter:kwaivgi/kling-v3.0-pro", ModelID: "kwaivgi/kling-v3.0-pro", Provider: catalog.OpenRouter,
		ProviderModelID: "kwaivgi/kling-v3.0-pro", Kind: catalog.Video, InputModalities: []string{"text", "image"},
		Params: map[string]catalog.Param{
			"duration":       {Type: catalog.Enum, Values: []any{5.0, 10.0}},
			"resolution":     {Type: catalog.Enum, Values: []any{"720p", "1080p"}},
			"generate_audio": {Type: catalog.Boolean},
			"frame_images":   {Type: catalog.Enum, Values: []any{"first_frame", "last_frame"}},
		},
		Routes: []catalog.Route{{SKUs: map[string]string{"duration_seconds": "0.168"}}},
	}
	h.store.models = append(h.store.models, catalog.Model{
		ID: of.ModelID, Kind: catalog.Video, Name: "Kling 3.0 Pro", Maker: "Kuaishou", Offers: []catalog.Offer{of},
	})
}

// A video picks its model and its settings as an image does: every video
// model is listed, the one that edits a clip disabled, and a model on the
// key takes the photo as its first frame, with framy's defaults.
func TestAVideoPicksItsModelAndSettings(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	withFlux(h)
	withKling(h)
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	if err := h.command(photoReply("/img make it rain"), CommandImage, "make it rain"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	edits := h.tg.methods("editMessageRich")
	got := buttonsOf(edits[len(edits)-1].text)
	if len(got) != 3 || got[0] != "-FLUX Video Edit · "+i18n.T("en", "web.misfit.needs_clip") || got[1] != "*HeyGen Video · "+i18n.T("en", "card.free") ||
		!strings.HasPrefix(got[2], "Kling 3.0 Pro · ") {
		t.Fatalf("buttons = %q", got)
	}
	h.tg.reset()
	if err := h.press(1, 7, pick("kwaivgi/kling-v3.0-pro")); err != nil {
		t.Fatal(err)
	}
	n := h.queue.revised[len(h.queue.revised)-1]
	// The length the free run had carries over; the rest are framy's
	// defaults, and the photo is the first frame.
	settings, _ := json.Marshal(n.Settings)
	if n.Free || n.Offer.ID != "openrouter:kwaivgi/kling-v3.0-pro" || string(settings) != `{"duration":5,"frame_images":["first_frame"],"generate_audio":true,"resolution":"720p"}` ||
		n.Estimate == nil || len(n.Estimate.Lines) != 1 || n.Estimate.Lines[0].Quantity != 5 {
		t.Fatalf("revision = %s, estimate %+v", settings, n.Estimate)
	}
	body := h.tg.methods("editMessageRich")[0].text
	if !strings.Contains(body, "Settings: <b>") || !strings.Contains(body, `data="j:1:Sduration#`+valueHash(10)+`"`) {
		t.Fatalf("preview = %s", body)
	}
	h.tg.reset()
	if err := h.press(1, 7, actPickSetting+"duration#"+valueHash(10)); err != nil {
		t.Fatal(err)
	}
	settings, _ = json.Marshal(h.queue.revised[len(h.queue.revised)-1].Settings)
	if string(settings) != `{"duration":10,"frame_images":["first_frame"],"generate_audio":true,"resolution":"720p"}` {
		t.Fatalf("revision = %s", settings)
	}
	// The keyboard still turns it back into an image.
	if got := lastCard(t, h); !reflect.DeepEqual(got, [][]string{{"j:1:go", "j:1:no"}, {"j:1:i"}}) {
		t.Fatalf("keyboard = %v", got)
	}
}

// An edit of a clip keeps its model: its preview lists none, and data
// forged for one changes nothing.
func TestAnEditOfAClipKeepsItsModel(t *testing.T) {
	h := videoHarness(t)
	withKling(h)
	withKey(h)
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	if body := h.tg.methods("sendRichMessage")[0].text; strings.Contains(body, "<details") {
		t.Fatalf("preview = %s", body)
	}
	h.tg.reset()
	if err := h.press(1, 7, pick("kwaivgi/kling-v3.0-pro")); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != 0 || len(h.tg.calls) != 0 {
		t.Fatalf("revised %+v, calls %+v", h.queue.revised, h.tg.calls)
	}
}
