// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"reflect"
	"testing"
)

func enumOf(values ...any) Param { return Param{Type: Enum, Values: values} }

func rangeOf(lo, hi float64) Param { return Param{Type: Range, Min: &lo, Max: &hi} }

func TestDefaultsFollowOneRule(t *testing.T) {
	image := Offer{Kind: Image, Params: map[string]Param{
		"aspect_ratio":  enumOf("21:9", "16:9", "4:3", "1:1", "3:4", "9:16", "auto"),
		"resolution":    enumOf("768", "1K", "1.5K", "2K", "4K"),
		"quality":       enumOf("low", "medium", "high"),
		"background":    enumOf("transparent", "auto", "opaque"),
		"n":             rangeOf(1, 10),
		"seed":          {Type: Boolean},
		"size":          {Type: Boolean},
		"google_search": {Type: Boolean},
		"negative":      {Type: Text, Passthrough: true},
	}}
	video := Offer{Kind: Video, Params: map[string]Param{
		"aspect_ratio":     enumOf("1:1", "9:16", "16:9", "21:9"),
		"resolution":       enumOf("480p", "768p", "2K"),
		"duration":         enumOf(4.0, 6.0, 8.0),
		"generate_audio":   {Type: Boolean},
		"frame_images":     enumOf("first_frame", "last_frame"),
		"upscale_factor":   rangeOf(1.5, 3),
		"creativity":       enumOf(0.0, 1.0),
		"size":             enumOf("1280x720", "720x1280"),
		"input_references": rangeOf(0, 4),
	}}
	for _, tc := range []struct {
		name  string
		of    Offer
		shape float64
		want  map[string]any
	}{
		{"an image from text", image, 0, map[string]any{
			"aspect_ratio": "1:1", "resolution": "1K", "quality": "medium", "background": "auto", "n": 1.0, "google_search": false,
		}},
		{"an image from a photo keeps its shape", image, 4.0 / 3, map[string]any{
			"aspect_ratio": "auto", "resolution": "1K", "quality": "medium", "background": "auto", "n": 1.0, "google_search": false,
		}},
		{"a video from text", video, 0, map[string]any{
			"aspect_ratio": "16:9", "resolution": "768p", "duration": 6.0, "generate_audio": true,
			"upscale_factor": 2.25, "creativity": 0.0,
		}},
		{"a video from a tall frame", video, 9.0 / 16, map[string]any{
			"aspect_ratio": "9:16", "resolution": "768p", "duration": 6.0, "generate_audio": true,
			"upscale_factor": 2.25, "creativity": 0.0,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Defaults(tc.of, tc.shape); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("defaults = %v\nwant       %v", got, tc.want)
			}
		})
	}
}

func TestDefaultsReachTheStandardSize(t *testing.T) {
	for _, tc := range []struct {
		kind   Kind
		name   string
		values []any
		want   any
	}{
		{Image, "resolution", []any{"512", "1K", "2K", "4K"}, "1K"},
		{Image, "resolution", []any{"2K", "4K"}, "2K"},
		{Image, "resolution", []any{"auto", "512"}, "512"},
		{Video, "resolution", []any{"768p", "480p"}, "768p"},
		{Video, "resolution", []any{"360p", "720p", "1080p", "4K"}, "720p"},
		{Video, "duration", []any{6.0, 10.0}, 6.0},
		{Video, "duration", []any{2.0, 3.0}, 3.0},
		{Image, "quality", []any{"standard", "hd"}, "standard"},
		{Image, "thinking_level", []any{"minimal", "high"}, "minimal"},
		{Image, "aspect_ratio", []any{"9:16", "16:9", "4:3"}, "4:3"},
		{Image, "size", []any{"1536x1024", "1024x1024", "1024x1536", "2048x2048"}, "1024x1024"},
	} {
		got, ok := Defaults(Offer{Kind: tc.kind, Params: map[string]Param{tc.name: enumOf(tc.values...)}}, 0)[tc.name]
		if !ok || got != tc.want {
			t.Errorf("%s %s of %v = %v, want %v", tc.kind, tc.name, tc.values, got, tc.want)
		}
	}
	// A wide photo picks the wide size of about the same area.
	sizes := enumOf("1024x1024", "1536x1024", "1024x1536", "3840x2160")
	if got := Defaults(Offer{Kind: Image, Params: map[string]Param{"size": sizes}}, 1.5)["size"]; got != "1536x1024" {
		t.Errorf("size for 3:2 = %v", got)
	}
}

// A value one route lacks would send the run only to the others, maybe a
// dearer one, while the estimate counts the cheapest: such a setting is
// left to the model.
func TestADefaultNeverNarrowsTheRoutes(t *testing.T) {
	of := Offer{Kind: Image, Params: map[string]Param{
		"aspect_ratio": enumOf("1:1", "16:9"),
		"background":   enumOf("auto", "opaque"),
	}, Routes: []Route{
		{Provider: "a", Params: map[string]Param{"aspect_ratio": enumOf("1:1", "16:9"), "background": enumOf("auto")}},
		{Provider: "b", Params: map[string]Param{"aspect_ratio": enumOf("16:9", "1:1")}},
	}}
	if got := Defaults(of, 0); !reflect.DeepEqual(got, map[string]any{"aspect_ratio": "1:1"}) {
		t.Fatalf("defaults = %v", got)
	}
	// A video's one route takes the offer's settings, whatever pass-through
	// ones the overlay put on it.
	video := Offer{Kind: Video, Params: map[string]Param{"duration": enumOf(5.0, 10.0)},
		Routes: []Route{{Params: map[string]Param{"negative_prompt": {Type: Text, Passthrough: true}}}}}
	if got := Defaults(video, 0); !reflect.DeepEqual(got, map[string]any{"duration": 5.0}) {
		t.Fatalf("video defaults = %v", got)
	}
}

// Every offer of the real catalog gets a default for what the composer
// draws, and every default is one of the offer's own values.
func TestEveryOfferHasItsDefaults(t *testing.T) {
	snap := Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay)
	for _, m := range snap.Models {
		for _, of := range m.Offers {
			d := Defaults(of, 0)
			for name, v := range d {
				p := of.Params[name]
				if p.Type == Enum && !containsValue(p.Values, v) {
					t.Errorf("%s: %s=%v is not one of %v", of.ID, name, v, p.Values)
				}
			}
			for _, name := range []string{"aspect_ratio", "duration"} {
				if _, has := of.Params[name]; has {
					if _, ok := d[name]; !ok && everyRouteHas(of, name) {
						t.Errorf("%s has %s and no default", of.ID, name)
					}
				}
			}
		}
	}
}

func containsValue(values []any, v any) bool {
	for _, x := range values {
		if sameValue(x, v) {
			return true
		}
	}
	return false
}

func everyRouteHas(of Offer, name string) bool {
	for _, r := range of.Routes {
		if _, ok := r.Params[name]; r.Params != nil && !ok {
			return false
		}
	}
	return true
}
