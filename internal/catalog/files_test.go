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
		{"one effort value", map[string]string{"overlay.yaml": "models:\n  a/b:\n    effort: {parameter: quality, values: [high]}\n"}, "at least two values"},
		{"text in a descriptor", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "{type: range, min: 1, max: 1}", "{type: text}", 1)}, "text is only for pass-through"},
		{"empty enum", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "{type: range, min: 1, max: 1}", "{type: enum}", 1)}, "needs values"},
		{"inverted range", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "min: 1, max: 1", "min: 2, max: 1", 1)}, "min <= max"},
		{"free price", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "0.00003", "0", 1)}, "positive cost_usd"},
		{"bad kind", map[string]string{"overlay.yaml": joined, "gemini.yaml": strings.Replace(gemini, "kind: image", "kind: audio", 1)}, "kind must be"},
		{"model id with a colon", map[string]string{"overlay.yaml": "models:\n  \"a:b\": {}\n"}, "no colon"},
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
