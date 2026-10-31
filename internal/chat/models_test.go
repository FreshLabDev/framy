// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"reflect"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
)

// pricedOffer is an offer that costs usd an image.
func pricedOffer(provider, model string, usd float64) catalog.Offer {
	of := offerOf(provider, model, refs(8))
	of.Routes = []catalog.Route{{Prices: []catalog.Price{{Billable: "output_image", Unit: "image", CostUSD: usd}}}}
	return of
}

// familyCatalog: the two free models, Nano Banana in two variants the
// site suggests for an image, GPT Image that only OpenAI runs, and a model
// of no family the site does not suggest.
func familyCatalog() []catalog.Model {
	return []catalog.Model{
		{ID: "tencent/hy-image-3.5-preview", Kind: catalog.Image, Name: "Hy Image 3.5 Preview", Maker: "Tencent", Featured: true, Offers: []catalog.Offer{
			pricedOffer(catalog.OpenRouter, "tencent/hy-image-3.5-preview", 0.03),
		}},
		{ID: "google/nano-banana-2.1", Kind: catalog.Image, Name: "Nano Banana 2.1", Maker: "Google", Featured: true, Offers: []catalog.Offer{
			pricedOffer(catalog.OpenRouter, "google/nano-banana-2.1", 0.04),
		}, Traits: catalog.Traits{
			Family:    &catalog.Family{ID: "nano-banana", Name: "Nano Banana", Label: "2.1", Rank: 1},
			Recommend: map[string]int{"image": 2},
		}},
		{ID: "google/nano-banana-pro", Kind: catalog.Image, Name: "Nano Banana Pro", Maker: "Google", Featured: true, Offers: []catalog.Offer{
			pricedOffer(catalog.OpenRouter, "google/nano-banana-pro", 0.13),
		}, Traits: catalog.Traits{Family: &catalog.Family{ID: "nano-banana", Name: "Nano Banana", Label: "Pro", Rank: 0}}},
		{ID: "openai/gpt-image-2.5", Kind: catalog.Image, Name: "GPT Image 2.5", Maker: "OpenAI", Featured: true, Offers: []catalog.Offer{
			pricedOffer(catalog.OpenAI, "openai/gpt-image-2.5", 0.05),
		}, Traits: catalog.Traits{Family: &catalog.Family{ID: "gpt-image", Name: "GPT Image", Label: "2.5"}}},
		{ID: "black-forest-labs/flux-3-image-klein", Kind: catalog.Image, Name: "FLUX 3 Image Klein", Maker: "Black Forest Labs", Free: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "black-forest-labs/flux-3-image-klein", refs(10)),
		}},
		{ID: "microsoft/mai-image-2.6-flash", Kind: catalog.Image, Name: "MAI-Image-2.6 Flash", Maker: "Microsoft AI", Free: true, Offers: []catalog.Offer{
			offerOf(catalog.OpenRouter, "microsoft/mai-image-2.6-flash", refs(5)),
		}},
	}
}

// The list is one row a family with its price: the free ones first, then
// the site's suggestion, then the rest; a family of two opens on its
// variants, and what needs a key folds into one row.
func TestTheListGroupsFamiliesAndFoldsWhatNeedsAKey(t *testing.T) {
	h := newHarness(t)
	h.store.models = familyCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	m := command("/img a fox")
	m.Chat = tg.Chat{ID: 7, Type: "private"}
	if err := h.command(m, CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	list := func(action string) [][]string {
		t.Helper()
		h.tg.reset()
		if err := h.press(1, 7, action); err != nil {
			t.Fatal(err)
		}
		edits := h.tg.methods("editMessageReplyMarkup")
		if len(edits) != 1 {
			t.Fatalf("calls = %+v", h.tg.calls)
		}
		var out [][]string
		for _, row := range edits[0].markup.InlineKeyboard {
			if len(row) != 1 {
				t.Fatalf("a row of %d", len(row))
			}
			out = append(out, []string{row[0].Text, row[0].CallbackData})
		}
		return out
	}
	back := i18n.T("en", "btn.back")
	free := " · " + i18n.T("en", "card.free")
	nano, gpt := famHash("nano-banana"), "openai/gpt-image-2.5"
	if got, want := list(actModel), [][]string{
		{"FLUX 3 Image Klein" + free, "j:1:" + pick("black-forest-labs/flux-3-image-klein")},
		{"✓ MAI-Image-2.6 Flash" + free, "j:1:" + pick("microsoft/mai-image-2.6-flash")},
		{"Nano Banana · " + i18n.T("en", "run.from", "price", "$0.04"), "j:1:" + actFamily + nano},
		{"Hy Image 3.5 Preview · $0.03", "j:1:" + pick("tencent/hy-image-3.5-preview")},
		{"🔑 " + i18n.T("en", "panel.more_key", "n", "1"), "j:1:" + actLocked},
		{back, "j:1:" + actBack},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %v", got)
	}
	// The family's variants, newest first, each with its own price.
	if got, want := list(actFamily+nano), [][]string{
		{"Pro · $0.13", "j:1:" + pick("google/nano-banana-pro")},
		{"2.1 · $0.04", "j:1:" + pick("google/nano-banana-2.1")},
		{back, "j:1:" + actModel},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("family = %v", got)
	}
	if got, want := list(actLocked), [][]string{
		{"🔑 GPT Image 2.5 · $0.05", "j:1:" + pick(gpt)},
		{back, "j:1:" + actModel},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("on a key = %v", got)
	}
	// A family gone from the catalog falls back to the list.
	if got := list(actFamily + famHash("gone")); len(got) != 6 {
		t.Fatalf("list = %v", got)
	}

	h.tg.reset()
	if err := h.press(1, 7, pick("google/nano-banana-2.1")); err != nil {
		t.Fatal(err)
	}
	if r := run(t, h); r.Model != "Nano Banana 2.1" {
		t.Fatalf("run = %+v", r)
	}
	if got := list(actModel); got[0][0] != "FLUX 3 Image Klein"+free || got[1][0] != "MAI-Image-2.6 Flash"+free ||
		got[2][0] != "✓ Nano Banana · "+i18n.T("en", "run.from", "price", "$0.04") {
		t.Fatalf("list = %v", got)
	}
	if got := list(actFamily + nano); got[1][0] != "✓ 2.1 · $0.04" {
		t.Fatalf("family = %v", got)
	}
}
