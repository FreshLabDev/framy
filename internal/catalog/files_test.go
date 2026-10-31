// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedFilesLoad(t *testing.T) {
	f := embeddedFiles(t)
	providers := map[string]int{}
	for _, of := range f.Offers {
		providers[of.Provider]++
		if len(of.Routes) != 1 || len(of.Routes[0].Prices) == 0 {
			t.Errorf("offer %s routes = %+v", of.ID, of.Routes)
		}
	}
	if providers[OpenAI] == 0 || providers[Gemini] == 0 {
		t.Fatalf("offers by provider = %v", providers)
	}
	if f.Overlay.HideExpiringWithinDays != 14 {
		t.Fatalf("hide_expiring_within_days = %d, the design says 14", f.Overlay.HideExpiringWithinDays)
	}
	if !f.Overlay.Models["microsoft/mai-image-2.6-flash"].Free {
		t.Fatal("mai-image-2.6-flash is the owner's free image model")
	}
	// OpenAI's auto values have no price before the run.
	for _, of := range f.Offers {
		for _, name := range []string{"quality", "size"} {
			for _, v := range of.Params[name].Values {
				if v == "auto" {
					t.Errorf("offer %s offers %s=auto", of.ID, name)
				}
			}
		}
	}
}

const gemini = `
provider: gemini
models:
  - id: gemini-x
    kind: image
    name: X
    supported_parameters:
      n: {type: range, min: 1, max: 1}
    pricing:
      - {billable: output_image, unit: token, cost_usd: 0.00003}
`

func TestLoadFilesRejects(t *testing.T) {
	joined := "models:\n  google/x:\n    offers: [gemini:gemini-x]\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no overlay", map[string]string{"gemini.yaml": gemini}, "overlay.yaml is missing"},
		{"unknown overlay field", map[string]string{"overlay.yaml": "models:\n  a/b:\n    featurd: true\n"}, "featurd"},
		{"unknown descriptor field", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "name: X", "nme: X", 1)}, "nme"},
		{"offer of no model", map[string]string{"overlay.yaml": "models: {}\n", "gemini.yaml": gemini}, "belongs to no model"},
		{"offer of two models", map[string]string{"overlay.yaml": joined + "  google/y:\n    offers: [gemini:gemini-x]\n", "gemini.yaml": gemini}, "listed under both"},
		{"offer not described", map[string]string{"overlay.yaml": "models:\n  a/b:\n    offers: [openai:gpt-z]\n"}, "not described"},
		{"offer without provider", map[string]string{"overlay.yaml": "models:\n  a/b:\n    offers: [gpt-z]\n"}, "<provider>:<model id>"},
		{"wrong file", map[string]string{"overlay.yaml": joined, "openai.yaml": gemini}, "belong in gemini.yaml"},
		{"bad pattern", map[string]string{"overlay.yaml": "hide:\n  - match: \"[\"\n    why: x\n"}, "not a valid pattern"},
		{"rule without reason", map[string]string{"overlay.yaml": "hide:\n  - match: a/*\n"}, "needs a reason"},
		{"hidden and featured", map[string]string{"overlay.yaml": "models:\n  a/b:\n    hide: gone\n    featured: true\n"}, "both hidden and featured"},
		{"bad faces", map[string]string{"overlay.yaml": "models:\n  a/b:\n    faces: sometimes\n"}, "faces must be"},
		{"empty source video", map[string]string{"overlay.yaml": "models:\n  a/b:\n    source_video: {}\n"}, "source_video says nothing"},
		{"negative source limit", map[string]string{"overlay.yaml": "models:\n  a/b:\n    source_video: {max_seconds: -1}\n"}, "cannot be negative"},
		{"unknown source field", map[string]string{"overlay.yaml": "models:\n  a/b:\n    source_video: {max_minutes: 1}\n"}, "max_minutes"},
		{"inverted source length", map[string]string{"overlay.yaml": "models:\n  a/b:\n    source_video: {min_seconds: 3, max_seconds: 2}\n"}, "min_seconds <= max_seconds"},
		{"empty references", map[string]string{"overlay.yaml": "models:\n  a/b:\n    references: {}\n"}, "references says nothing"},
		{"negative references", map[string]string{"overlay.yaml": "models:\n  a/b:\n    references: {image: -1}\n"}, "cannot be negative"},
		{"negative free images", map[string]string{"overlay.yaml": "models:\n  a/b:\n    price: {free_images: -1}\n"}, "cannot be negative"},
		{"negative clip floor", map[string]string{"overlay.yaml": "models:\n  a/b:\n    price: {clip_min_seconds: -3}\n"}, "cannot be negative"},
		{"bad size", map[string]string{"overlay.yaml": "models:\n  a/b:\n    price: {sizes: {480p: {\"16:9\": 864×496}}}\n"}, "not a size"},
		{"no sizes", map[string]string{"overlay.yaml": "models:\n  a/b:\n    price: {sizes: {480p: {}}}\n"}, "lists no sizes"},
		{"clip without count", map[string]string{"overlay.yaml": "models:\n  a/b:\n    references: {image: 2, video_clip: {max_seconds: 5}}\n"}, "video_clip without references.video"},
		{"inverted clip", map[string]string{"overlay.yaml": "models:\n  a/b:\n    references: {audio: 2, audio_clip: {min_seconds: 9, max_seconds: 5}}\n"}, "min_seconds <= max_seconds"},
		{"bad shutdown day", map[string]string{"overlay.yaml": "models:\n  a/b:\n    deprecated: {shuts_down: 2 November 2026}\n"}, "not a day"},
		{"own successor", map[string]string{"overlay.yaml": "models:\n  a/b:\n    deprecated: {successor: a/b}\n"}, "its own successor"},
		{"one effort value", map[string]string{"overlay.yaml": "models:\n  a/b:\n    effort: {parameter: quality, values: [high]}\n"}, "at least two values"},
		{"text in a descriptor", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "{type: range, min: 1, max: 1}", "{type: text}", 1)}, "text is only for pass-through"},
		{"empty enum", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "{type: range, min: 1, max: 1}", "{type: enum}", 1)}, "needs values"},
		{"inverted range", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "min: 1, max: 1", "min: 2, max: 1", 1)}, "min <= max"},
		{"free price", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "0.00003", "0", 1)}, "positive cost_usd"},
		{"bad kind", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "kind: image", "kind: audio", 1)}, "kind must be"},
		{"model id with a colon", map[string]string{"overlay.yaml": "models:\n  \"a:b\": {}\n"}, "no colon"},
		{"unknown family", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX}}\ntiers: [std, pro]\nmodels:\n  a/b:\n    family: {id: fluxx, gen: \"1\", tier: std, label: \"1\"}\n"}, "not in families"},
		{"bad generation", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX}}\ntiers: [std, pro]\nmodels:\n  a/b:\n    family: {id: flux, gen: v2, tier: std, label: \"2\"}\n"}, "not a version"},
		{"unknown tier", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX}}\ntiers: [std, pro]\nmodels:\n  a/b:\n    family: {id: flux, gen: \"2\", tier: ultra, label: \"2\"}\n"}, "not in tiers"},
		{"no label", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX}}\ntiers: [std, pro]\nmodels:\n  a/b:\n    family: {id: flux, gen: \"2\", tier: std}\n"}, "needs a label"},
		{"computed field written", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX}}\ntiers: [std, pro]\nmodels:\n  a/b:\n    family: {id: flux, gen: \"2\", tier: std, label: \"2\", outdated_by: a/c}\n"}, "outdated_by"},
		{"no plain tier", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX}}\ntiers: [pro]\nmodels:\n  a/b:\n    family: {id: flux, gen: \"2\", tier: pro, label: \"2\"}\n"}, "must list std"},
		{"tier twice", map[string]string{"overlay.yaml": "tiers: [std, std]\n"}, "listed twice"},
		{"nameless family", map[string]string{"overlay.yaml": "families: {flux: {}}\n"}, "has no name"},
		{"family opens elsewhere", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX, default: a/b}}\ntiers: [std, pro]\nmodels:\n  a/b: {}\n"}, "not one of its models"},
		{"fast mode of itself", map[string]string{"overlay.yaml": "models:\n  a/b:\n    fast_of: a/b\n"}, "of itself"},
		{"fast mode in a family", map[string]string{"overlay.yaml": "families: {flux: {name: FLUX}}\ntiers: [std, pro]\nmodels:\n  a/b:\n    fast_of: a/c\n    family: {id: flux, gen: \"2\", tier: std, label: \"2\"}\n"}, "has no family"},
		{"featured fast mode", map[string]string{"overlay.yaml": "models:\n  a/b:\n    fast_of: a/c\n    featured: true\n"}, "neither featured nor free"},
		{"unknown situation", map[string]string{"overlay.yaml": "recommend:\n  audio: [a/b]\n"}, "not a situation"},
		{"suggested twice", map[string]string{"overlay.yaml": "recommend:\n  image: [a/b, a/b]\n"}, "twice"},
		{"suggested fast mode", map[string]string{"overlay.yaml": "recommend:\n  video: [a/b]\nmodels:\n  a/b:\n    fast_of: a/c\n"}, "is a fast mode"},
		{"maker renamed on", map[string]string{"overlay.yaml": "makers:\n  A: B\n  B: C\n"}, "renamed again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, data := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(data)}
			}
			_, err := LoadFiles(fsys)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one with %q", err, tc.want)
			}
		})
	}
}

func TestLoadFilesJoinsAnOffer(t *testing.T) {
	f, err := LoadFiles(fstest.MapFS{
		"overlay.yaml": {Data: []byte("models:\n  google/x:\n    offers: [gemini:gemini-x]\n")},
		"gemini.yaml":  {Data: []byte(gemini)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Offers) != 1 || f.Overlay.modelOf(f.Offers[0]) != "google/x" {
		t.Fatalf("files = %+v", f)
	}
	if got := f.Overlay.modelOf(Offer{ID: "openrouter:a/b", ProviderModelID: "a/b"}); got != "a/b" {
		t.Fatalf("an OpenRouter offer went to %s", got)
	}
}
