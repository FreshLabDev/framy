// SPDX-License-Identifier: Apache-2.0
package price

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// Check lists what the estimate cannot read in a catalog, so the sync logs
// it and a model shows up before someone sees "$?" or "about" on it:
//
//   - a price line of an unknown billable or unit, a SKU of an unknown
//     shape;
//   - a SKU with a word the estimate does not know, which makes every run of
//     the offer a range up to its rate, unless price.variants names it; a
//     long SKU without price.long;
//   - an offer with no price before the run at the settings it opens with,
//     as a model billed in output tokens with no count of them;
//   - an image offer that takes references and cannot price one, as a
//     model billed in input tokens with no count of them. The text and
//     thinking tokens are left out: no model has a count of them.
func Check(snap catalog.Snapshot) []string {
	var out []string
	for _, m := range snap.Models {
		if m.Hidden != "" {
			continue
		}
		var hints catalog.PriceHints
		if m.Traits.Price != nil {
			hints = *m.Traits.Price
		}
		for _, of := range m.Offers {
			if of.Hidden != "" {
				continue
			}
			for _, r := range of.Routes {
				for _, p := range r.Prices {
					if _, skipped := hints.Skip[p.Billable]; !skipped && !knownLine(p.Billable, p.Unit) {
						out = append(out, fmt.Sprintf("price: %s bills %s per %s, which the estimate does not read", of.ID, p.Billable, p.Unit))
					}
				}
				for _, name := range slices.Sorted(maps.Keys(r.SKUs)) {
					s, err := parseSKU(name, r.SKUs[name])
					if err != nil {
						out = append(out, fmt.Sprintf("price: %s: %v", of.ID, err))
						continue
					}
					if _, named := hints.Variants[name]; len(s.open) > 0 && !named {
						out = append(out, fmt.Sprintf("price: %s: SKU %s says %s, which the estimate does not read; name its settings in price.variants", of.ID, name, strings.Join(s.open, "_")))
					}
					if s.long && hints.Long == nil {
						out = append(out, fmt.Sprintf("price: %s: SKU %s is for long videos; say from how long in price.long", of.ID, name))
					}
				}
			}
			out = append(out, unpriced(m, of)...)
		}
	}
	return out
}

// unpriced prices the offer as the composer opens it, and an image offer
// with one reference too, and says what it cannot price.
func unpriced(m catalog.Model, of catalog.Offer) []string {
	req := Request{Settings: catalog.Defaults(of, 0), Prompt: "a fox in the snow"}
	if s := m.Traits.SourceVideo; s != nil && s.Required {
		req.Video = &Video{Seconds: 5, Width: 1280, Height: 720, FPS: 30}
	}
	if r := m.Traits.References; r != nil && r.FrameRequired {
		req.Frames = 1
		req.Settings["frame_images"] = []string{"first_frame"}
	}
	var out []string
	if e := Of(m, of, req); e.Kind == Unknown {
		out = append(out, fmt.Sprintf("price: %s has no price before the run at its default settings: %s", of.ID, strings.Join(e.Unknown, ", ")))
	}
	if p, ok := of.Params["input_references"]; of.Kind != catalog.Image || !ok || p.Max == nil || *p.Max < 1 {
		return out
	}
	req.Images = []Image{{Width: 1024, Height: 1024}}
	e := Of(m, of, req)
	gaps := slices.DeleteFunc(slices.Clone(e.Unknown), func(u string) bool { return u == "the text and thinking tokens" })
	if e.Kind != Unknown && len(gaps) > 0 {
		out = append(out, fmt.Sprintf("price: %s cannot price a reference: %s", of.ID, strings.Join(gaps, ", ")))
	}
	return out
}
