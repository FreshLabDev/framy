// SPDX-License-Identifier: Apache-2.0
package catalog

// AtOnce is how many results one run of the offer can make: the highest n
// the offer takes, and no more than any upstream of OpenRouter takes, since
// OpenRouter may fall back to any of them. 1 for an offer without n.
func AtOnce(of Offer) int {
	p, ok := of.Params["n"]
	if !ok || p.Type != Range || p.Max == nil {
		return 1
	}
	most := int(*p.Max)
	for _, r := range of.Routes {
		if r.Provider == "" {
			continue
		}
		rp, ok := r.Params["n"]
		if !ok {
			return 1
		}
		if rp.Max != nil {
			most = min(most, int(*rp.Max))
		}
	}
	return max(most, 1)
}
