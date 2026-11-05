// SPDX-License-Identifier: Apache-2.0
package price

import (
	"fmt"
	"maps"
	"slices"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// Check lists what the estimate cannot read in a catalog: a price line of an
// unknown billable or unit, a SKU of an unknown shape, an output image billed
// by the token with no count of its tokens in the overlay. The sync logs
// them, so a new kind of price, or a new model of a known one, shows up
// before someone sees "about" or "known after the run" on it.
func Check(snap catalog.Snapshot) []string {
	var out []string
	for _, m := range snap.Models {
		if m.Hidden != "" {
			continue
		}
		var skip map[string]string
		counted := false
		if h := m.Traits.Price; h != nil {
			skip = h.Skip
			counted = len(h.OpenAITokens) > 0 || len(h.Tokens[outputImage]) > 0
		}
		for _, of := range m.Offers {
			if of.Hidden != "" {
				continue
			}
			uncounted := false
			for _, r := range of.Routes {
				for _, p := range r.Prices {
					if _, skipped := skip[p.Billable]; !skipped && !knownLine(p.Billable, p.Unit) {
						out = append(out, fmt.Sprintf("price: %s bills %s per %s, which the estimate does not read", of.ID, p.Billable, p.Unit))
					}
					if p.Billable == outputImage && p.Unit == unitToken && !counted && !uncounted {
						// A new GPT Image or Gemini on OpenRouter comes like this.
						out = append(out, fmt.Sprintf("price: %s bills its output image by the token, and the overlay gives no count (price.openai_tokens or price.tokens.output_image)", of.ID))
						uncounted = true
					}
				}
				for _, name := range slices.Sorted(maps.Keys(r.SKUs)) {
					if _, err := parseSKU(name, r.SKUs[name]); err != nil {
						out = append(out, fmt.Sprintf("price: %s: %v", of.ID, err))
					}
				}
			}
		}
	}
	return out
}
