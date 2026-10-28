// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// The picker's families of the real catalog: the order inside a row, the
// outdated variants and what took their place, the variant a row opens on.
func TestThePickerArrangesTheCatalog(t *testing.T) {
	snap := Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay)
	family := func(id string) Family {
		t.Helper()
		f := modelByID(t, snap, id).Traits.Family
		if f == nil {
			t.Fatalf("%s has no family", id)
		}
		return *f
	}
	row := func(fam string) []string {
		var ids []string
		for _, m := range snap.Models {
			if f := m.Traits.Family; f != nil && f.ID == fam && shown(m) {
				ids = append(ids, m.ID)
			}
		}
		slices.SortFunc(ids, func(a, b string) int { return family(a).Rank - family(b).Rank })
		return ids
	}

	if got, want := row("flux"), []string{
		"black-forest-labs/flux-3-image", "black-forest-labs/flux-3-image-klein", "black-forest-labs/flux.2-max",
		"black-forest-labs/flux.2-pro", "black-forest-labs/flux.2-flex", "black-forest-labs/flux.2-klein-4b",
	}; !slices.Equal(got, want) {
		t.Errorf("FLUX row = %v", got)
	}
	for id, by := range map[string]string{
		// The same tier of a newer generation.
		"black-forest-labs/flux.2-klein-4b": "black-forest-labs/flux-3-image-klein",
		"google/gemini-3.1-flash-image":     "google/gemini-nano-banana-2.1",
		"recraft/recraft-v4-pro-vector":     "recraft/recraft-v4.1-pro-vector",
		// A plain variant gives way to the newest of any tier.
		"openai/gpt-image-2":      "openai/gpt-image-2.5-sunburst",
		"recraft/recraft-v3":      "recraft/recraft-v4.1",
		"microsoft/mai-image-2.5": "microsoft/mai-image-2.6",
		"bytedance/seedance-2.0":  "bytedance/seedance-2.5",
		"alibaba/wan-2.6":         "alibaba/wan-3.0",
		"minimax/hailuo-2.3":      "minimax/hailuo-3",
	} {
		if got := family(id).OutdatedBy; got != by {
			t.Errorf("%s outdated by %q, want %s", id, got, by)
		}
	}
	// A newer generation of another tier leaves a variant current: 2.1
	// Flash does not outdate Pro, nor V4.1 the V4 Styles line.
	for _, id := range []string{
		"google/gemini-3-pro-image", "google/gemini-3.1-flash-lite-image", "black-forest-labs/flux.2-pro",
		"microsoft/mai-image-2.5-pro", "bytedance/seedance-1-5-pro", "recraft/recraft-v4-styles",
	} {
		if by := family(id).OutdatedBy; by != "" {
			t.Errorf("%s is outdated by %s", id, by)
		}
	}
	for fam, want := range map[string]string{
		"flux": "black-forest-labs/flux-3-image", "recraft": "recraft/recraft-v4.1", "mai": "microsoft/mai-image-2.6",
		"wan": "alibaba/wan-3.0",
	} {
		var defaults []string
		for _, id := range row(fam) {
			if family(id).Default {
				defaults = append(defaults, id)
			}
		}
		if !slices.Equal(defaults, []string{want}) {
			t.Errorf("%s opens on %v, want %s", fam, defaults, want)
		}
	}
	if f := family("recraft/recraft-v4.1-pro-vector"); f.Name != "Recraft" || f.Line != "base" || f.Format != "vector" || f.Label != "V4.1 Pro Vector" {
		t.Errorf("Recraft V4.1 Pro Vector = %+v", f)
	}

	wan, prime := modelByID(t, snap, "alibaba/wan-3.0"), modelByID(t, snap, "alibaba/wan-3.0-prime")
	if wan.Traits.Fast != prime.ID || prime.Traits.FastOf != wan.ID || prime.Traits.Family != nil {
		t.Errorf("Wan 3.0 fast = %q, Prime fast of %q", wan.Traits.Fast, prime.Traits.FastOf)
	}

	for id, maker := range map[string]string{
		"x-ai/grok-imagine-image-quality": "xAI", "x-ai/grok-imagine-image-2.0": "xAI",
		"bytedance-seed/seedream-5-0-pro": "ByteDance", "bytedance/seedance-2.5": "ByteDance",
	} {
		if got := modelByID(t, snap, id).Maker; got != maker {
			t.Errorf("%s is made by %q, want %s", id, got, maker)
		}
	}
	if got := modelByID(t, snap, "kwaivgi/kling-v3.0-pro").Name; got != "Kling Video 3.0 Pro" {
		t.Errorf("Kling 3.0 Pro is named %q", got)
	}

	picks := map[string][]string{}
	for _, m := range snap.Models {
		for sit, place := range m.Traits.Recommend {
			for len(picks[sit]) < place {
				picks[sit] = append(picks[sit], "")
			}
			picks[sit][place-1] = m.ID
		}
	}
	want := map[string][]string{
		ForImage:      {"microsoft/mai-image-2.6-flash", "google/gemini-nano-banana-2.1", "openai/gpt-image-2.5-flare"},
		ForManyPhotos: {"black-forest-labs/flux-3-image-klein", "google/gemini-nano-banana-2.1", "openai/gpt-image-2.5-flare"},
		ForVideo:      {"heygen/heygen-video-1", "google/veo-3.1-fast", "kwaivgi/kling-v3.0-pro"},
		ForVideoEdit:  {"black-forest-labs/flux-video-edit", "runway/aleph-2", "bytedance/seedance-2.5"},
	}
	if !reflect.DeepEqual(picks, want) {
		t.Errorf("suggestions = %v", picks)
	}
}

// Only shown models count: a hidden one neither outdates another nor takes
// a place, and a suggestion that is gone leaves no hole in the places.
func TestThePickerCountsShownModelsOnly(t *testing.T) {
	model := func(id string, gen, tier string, hidden string) Model {
		return Model{ID: id, Kind: Image, Hidden: hidden, Offers: []Offer{{ID: "openrouter:" + id}},
			Traits: Traits{Family: &Family{ID: "x", Gen: gen, Tier: tier, Label: gen}}}
	}
	models := []Model{
		model("a/x-1", "1", PlainTier, ""),
		model("a/x-2", "2", PlainTier, "withdrawn"),
		model("a/x-3-pro", "3", "pro", ""),
	}
	o := Overlay{
		Families:  map[string]FamilyInfo{"x": {Name: "X", Default: "a/x-1"}},
		Tiers:     []string{PlainTier, "pro"},
		Recommend: map[string][]string{ForImage: {"a/x-2", "a/x-3-pro"}},
	}
	var warnings []string
	arrange(models, o, func(format string, args ...any) { warnings = append(warnings, format) })
	if by := models[0].Traits.Family.OutdatedBy; by != "a/x-3-pro" {
		t.Errorf("x 1 outdated by %q", by)
	}
	if models[0].Traits.Family.Default || !models[2].Traits.Family.Default {
		t.Errorf("the row opens on an outdated variant")
	}
	if got := models[2].Traits.Recommend; !maps.Equal(got, map[string]int{ForImage: 1}) {
		t.Errorf("places = %v", got)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "opens on") || !strings.Contains(warnings[1], "hidden") {
		t.Errorf("warnings = %q", warnings)
	}
}

// A note is keyed by its model's id, so a typo would lose it without a word.
func TestEveryNoteNamesAModel(t *testing.T) {
	models := embeddedFiles(t).Overlay.Models
	for _, k := range i18n.Keys() {
		if id, ok := strings.CutPrefix(k, "note."); ok {
			if _, known := models[id]; !known {
				t.Errorf("%s names no model of the overlay", k)
			}
		}
	}
}

func TestVersionsCompareByPart(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"2.10", "2.9", 1}, {"3", "2.5", 1}, {"2", "2.0", 0}, {"1.5", "2", -1}} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%s, %s) = %d", tc.a, tc.b, got)
		}
	}
}
