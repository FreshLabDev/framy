// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"maps"
	"strings"
	"testing"
	"testing/fstest"
)

// A GIF's frames go only with a GIF: a still gets none, a GIF gets
// OpenAI's 9 frames of 200 ms when none were chosen, and a GIF goes without
// a background, which OpenAI takes with none.
func TestAGIFsSettingsGoOnlyWithAGIF(t *testing.T) {
	m := modelByID(t, Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay), "openai/gpt-image-2.6-flare")
	of := m.Offers[0]
	if of.ID != "openai:gpt-image-2.6-flare" {
		t.Fatalf("offers = %v", offerIDs(m))
	}
	still := Defaults(of, 0)
	if still["output_format"] != "png" || still["background"] != "auto" {
		t.Errorf("a still's defaults %v, want png with background auto", still)
	}
	if _, ok := still["frame_count"]; ok {
		t.Errorf("a still has frames: %v", still)
	}
	for _, tc := range []struct {
		name string
		set  map[string]any
		want map[string]any
		gone []string
	}{
		{"a GIF nobody set", map[string]any{"output_format": "gif"}, map[string]any{"frame_count": 9.0, "frame_duration_ms": 200.0}, []string{"background"}},
		{"a GIF of its own", map[string]any{"output_format": "gif", "frame_count": 4, "frame_duration_ms": 80, "background": "transparent"},
			map[string]any{"frame_count": 4, "frame_duration_ms": 80}, []string{"background"}},
		{"a still with frames left over", map[string]any{"output_format": "png", "frame_count": 4, "frame_duration_ms": 80},
			map[string]any{"background": "auto"}, []string{"frame_count", "frame_duration_ms"}},
		{"nothing set", map[string]any{}, map[string]any{"output_format": "png", "background": "auto"}, []string{"frame_count", "frame_duration_ms"}},
	} {
		s := maps.Clone(tc.set)
		Fill(of, s, 0)
		for k, v := range tc.want {
			if !sameValue(s[k], v) {
				t.Errorf("%s: %s = %v, want %v (%v)", tc.name, k, s[k], v, s)
			}
		}
		for _, k := range tc.gone {
			if _, ok := s[k]; ok {
				t.Errorf("%s: %s goes along: %v", tc.name, k, s)
			}
		}
		if s["quality"] != "medium" || s["size"] != "1024x1024" {
			t.Errorf("%s: the rest of the defaults are missing: %v", tc.name, s)
		}
	}
	if Applies(of, "frame_count", map[string]any{"output_format": "webp"}) || !Applies(of, "frame_count", map[string]any{"output_format": "gif"}) {
		t.Error("frame_count applies by the format wrongly")
	}
	// The OpenRouter port, which shows no GIF, draws no format at all.
	if _, ok := Defaults(m.Offers[1], 0)["output_format"]; ok {
		t.Errorf("%s gets a format of framy's", m.Offers[1].ID)
	}
}

func TestAnOnlyIsHeldToItsModel(t *testing.T) {
	head := "provider: openai\nmodels:\n  - id: m\n    kind: image\n    name: M\n    pricing: [{billable: output_image, unit: image, cost_usd: 0.01}]\n    supported_parameters:\n"
	for name, tc := range map[string]struct{ params, want string }{
		"a setting it lacks":  {"      frame_count: {type: range, min: 2, max: 16, only: {output_format: [gif]}}\n", "does not list"},
		"a value it lacks":    {"      output_format: {type: enum, values: [png]}\n      frame_count: {type: range, min: 2, max: 16, only: {output_format: [gif]}}\n", "not a value"},
		"itself":              {"      output_format: {type: enum, values: [png, gif], only: {output_format: [gif]}}\n", "names itself"},
		"a range default out": {"      frame_count: {type: range, min: 2, max: 16, default: 20}\n", "not within"},
		"a range":             {"      n: {type: range, min: 1, max: 4}\n      frame_count: {type: range, min: 2, max: 16, only: {n: [2]}}\n", "not an enum"},
	} {
		_, err := LoadFiles(fstest.MapFS{"openai.yaml": {Data: []byte(head + tc.params)}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
