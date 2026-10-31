// SPDX-License-Identifier: Apache-2.0
package catalog

import "testing"

func TestAtOnceIsWhatEveryRouteTakes(t *testing.T) {
	one, four, six := 1.0, 4.0, 6.0
	n := func(hi *float64) map[string]Param { return map[string]Param{"n": {Type: Range, Min: &one, Max: hi}} }
	for _, c := range []struct {
		name string
		of   Offer
		want int
	}{
		{"no n", Offer{}, 1},
		{"n without a top", Offer{Params: map[string]Param{"n": {Type: Range, Min: &one}}}, 1},
		{"up to 6", Offer{Params: n(&six)}, 6},
		{"a direct provider's routes have no upstream", Offer{Params: n(&four), Routes: []Route{{}}}, 4},
		{"an upstream takes less", Offer{Params: n(&six), Routes: []Route{{Provider: "a", Params: n(&six)}, {Provider: "b", Params: n(&four)}}}, 4},
		{"an upstream without n", Offer{Params: n(&six), Routes: []Route{{Provider: "a", Params: n(&six)}, {Provider: "b"}}}, 1},
	} {
		if got := AtOnce(c.of); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
