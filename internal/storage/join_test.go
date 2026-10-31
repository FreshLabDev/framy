// SPDX-License-Identifier: Apache-2.0
package storage

import (
	"slices"
	"testing"
)

func TestThePartsOfAJoinedClip(t *testing.T) {
	for _, c := range []struct {
		name    string
		earlier []float64
		nextAt  float64
		want    []float64
	}{
		{"a clip of one part", nil, 5, []float64{0, 5}},
		{"a joined clip", []float64{0, 5}, 9.96, []float64{0, 5, 9.96}},
		// The frame was taken before the fade at the end of the last part.
		{"cut inside its last part", []float64{0, 5, 9.96}, 14.55, []float64{0, 5, 9.96, 14.55}},
		{"cut before a part began", []float64{0, 5, 9.96}, 9.9, []float64{0, 5, 9.9}},
	} {
		if got := partsBefore(c.earlier, c.nextAt); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
