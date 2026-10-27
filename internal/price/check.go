// SPDX-License-Identifier: Apache-2.0
package price

import (
	"fmt"
	"maps"
	"slices"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// Check lists what the estimate cannot read in a catalog: a price line of an
// unknown billable or unit, a SKU of an unknown shape. The sync logs them,
// so a new kind of price shows up before someone sees "about" on it.
func Check(snap catalog.Snapshot) []string {
	var out []string
	for _, m := range snap.Models {
		if m.Hidden != "" {
			continue
		}
		var skip map[string]string
		if m.Traits.Price != nil {
			skip = m.Traits.Price.Skip
		}
		for _, of := range m.Offers {
			if of.Hidden != "" {
				continue
			}
			for _, r := range of.Routes {
				for _, p := range r.Prices {
					if _, skipped := skip[p.Billable]; !skipped && !knownLine(p.Billable, p.Unit) {
						out = append(out, fmt.Sprintf("price: %s bills %s per %s, which the estimate does not read", of.ID, p.Billable, p.Unit))
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
