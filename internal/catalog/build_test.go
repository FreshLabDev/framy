// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	catalogfiles "github.com/FreshLabDev/framy/catalog"
)

// fixtureDay is when the fixtures were taken.
var fixtureDay = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func embeddedFiles(t *testing.T) Files {
	t.Helper()
	f, err := LoadFiles(catalogfiles.FS)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fixtureUpstream(t *testing.T) Upstream {
	t.Helper()
	up, err := source(fixtureServer(t, nil)).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return up
}

func modelByID(t *testing.T, snap Snapshot, id string) Model {
	t.Helper()
	for _, m := range snap.Models {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("model %s missing", id)
	return Model{}
}

func offerIDs(m Model) []string {
	var out []string
	for _, of := range m.Offers {
		out = append(out, of.ID)
	}
	return out
}

// The overlay as written must fit what OpenRouter publishes: a mismatch here
// is a warning in production, found at review instead.
func TestEmbeddedOverlayFitsOpenRouter(t *testing.T) {
	snap := Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay)
	for _, w := range snap.Warnings {
		t.Error(w)
	}
	if len(snap.Keep) != 0 {
		t.Errorf("keep = %v", snap.Keep)
	}
	for _, m := range snap.Models {
		if m.Name == "" || m.Maker == "" || !m.Kind.valid() {
			t.Errorf("model %s is not described: %+v", m.ID, m)
		}
		if strings.Contains(m.Description, "](") {
			t.Errorf("model %s keeps a Markdown link: %s", m.ID, m.Description)
		}
	}
}

func TestBuildJoinsNanoBanana(t *testing.T) {
	snap := Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay)
	nb := modelByID(t, snap, "google/gemini-nano-banana-2.1")
	want := []string{"gemini:gemini-nano-banana-2.1", "openrouter:google/gemini-nano-banana-2.1"}
	if got := offerIDs(nb); !slices.Equal(got, want) {
		t.Fatalf("offers = %v, want %v", got, want)
	}
	if nb.Name != "Nano Banana 2.1" || nb.Maker != "Google" || !nb.Featured || nb.Hidden != "" || nb.Kind != Image {
		t.Fatalf("model = %+v", nb)
	}
	for _, of := range nb.Offers {
		if of.ModelID != nb.ID || of.Hidden != "" {
			t.Fatalf("offer %s = %+v", of.ID, of)
		}
	}
	// thinking_level exists only on the direct offer.
	if _, ok := nb.Offers[0].Params["thinking_level"]; !ok {
		t.Fatal("the Gemini offer lost thinking_level")
	}
	if _, ok := nb.Offers[1].Params["thinking_level"]; ok {
		t.Fatal("the OpenRouter offer gained thinking_level")
	}

	omni := modelByID(t, snap, "google/gemini-omni-1.1-flash")
	if got := offerIDs(omni); !slices.Equal(got, []string{"gemini:gemini-omni-1.1-flash"}) || omni.Kind != Video || !omni.Traits.Continue {
		t.Fatalf("omni = %+v", omni)
	}
	pro := modelByID(t, snap, "google/gemini-3-pro-image")
	if pro.Name != "Nano Banana Pro" || len(pro.Offers) != 2 {
		t.Fatalf("pro = %+v", pro)
	}
}

func TestBuildHides(t *testing.T) {
	snap := Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay)
	for id, why := range map[string]string{
		"google/gemini-3.1-flash-image-preview": "preview",
		"openai/gpt-5-image":                    "chat model",
		"openai/gpt-image-1":                    "shut gpt-image-1 down",
		"openai/gpt-image-1-mini":               "closes gpt-image-1-mini",
		"meta/muse-image":                       "evaluation slug",
		"google/gemini-2.5-flash-image":         "deprecation",
		"runway/aleph-2":                        "action",
	} {
		if m := modelByID(t, snap, id); !strings.Contains(m.Hidden, why) {
			t.Errorf("%s hidden = %q, want a reason with %q", id, m.Hidden, why)
		}
	}
	// A Tencent preview is a model of its own; only Google's previews are
	// duplicates.
	if m := modelByID(t, snap, "tencent/hy-image-v3.5-preview"); m.Hidden != "" || !m.Featured {
		t.Errorf("hy image = %+v", m)
	}
	if of := modelByID(t, snap, "meta/muse-image").Offers[0]; of.Hidden == "" {
		t.Error("an offer with no route is shown")
	}

	// Seedance 1.5 Pro leaves on 11 November: shown 15 and 14 days before,
	// hidden from 13.
	for day, hidden := range map[int]bool{27: false, 28: false, 29: true} {
		snap := Build(fixtureUpstream(t), embeddedFiles(t), time.Date(2026, 10, day, 23, 0, 0, 0, time.UTC))
		of := modelByID(t, snap, "bytedance/seedance-1-5-pro").Offers[0]
		if (of.Hidden != "") != hidden {
			t.Errorf("on 10-%d seedance 1.5 hidden = %q, want hidden %v", day, of.Hidden, hidden)
		}
	}
}

func TestBuildAppliesExclusionsAndPassthrough(t *testing.T) {
	snap := Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay)
	sunburst := modelByID(t, snap, "openai/gpt-image-2.5-sunburst")
	or := sunburst.Offers[1]
	if or.ID != "openrouter:openai/gpt-image-2.5-sunburst" {
		t.Fatalf("offers = %v", offerIDs(sunburst))
	}
	for _, params := range append([]map[string]Param{or.Params}, or.Routes[0].Params) {
		if slices.Contains(params["quality"].Values, any("auto")) || slices.Contains(params["aspect_ratio"].Values, any("auto")) {
			t.Fatalf("auto is still offered: %v", params)
		}
		if !sameValue(params["quality"].Values, []string{"low", "medium", "high", "xhigh", "max"}) {
			t.Fatalf("quality = %v", params["quality"].Values)
		}
	}
	if e := sunburst.Traits.Effort; e == nil || e.Parameter != "quality" {
		t.Fatalf("effort = %+v", e)
	}

	veo := modelByID(t, snap, "google/veo-3.1").Offers[0]
	if p := veo.Params["negativePrompt"]; p.Type != Text || !p.Passthrough {
		t.Fatalf("veo negativePrompt = %+v", p)
	}
	if modelByID(t, snap, "google/veo-3.1").Traits.References.Image != 3 {
		t.Fatal("veo 3.1 lost its three reference images")
	}

	// The fixtures do not change under Build.
	up := fixtureUpstream(t)
	before := offerByID(t, up.Offers, "openrouter:openai/gpt-image-2.5-sunburst")
	Build(up, embeddedFiles(t), fixtureDay)
	if !slices.Contains(before.Params["quality"].Values, any("auto")) {
		t.Fatal("Build changed the upstream it was given")
	}
}

func TestBuildWarnsOnDrift(t *testing.T) {
	overlay := `
hide_expiring_within_days: 14
models:
  vendor/gone:
    featured: true
  google/gemini-nano-banana-2.1:
    effort: {parameter: thinking, values: [low, high]}
  openai/gpt-image-1:
    featured: true
  google/veo-3.1:
    passthrough:
      notAccepted: {type: text}
hide:
  - match: openai/gpt-image-1*
    why: closed
`
	f, err := LoadFiles(fstest.MapFS{"overlay.yaml": {Data: []byte(overlay)}})
	if err != nil {
		t.Fatal(err)
	}
	snap := Build(fixtureUpstream(t), f, fixtureDay)
	for _, want := range []string{
		"overlay: model vendor/gone is in no source",
		"no offer has the effort parameter thinking",
		"model openai/gpt-image-1 is featured but hidden: closed",
		"no offer accepts the pass-through setting notAccepted",
	} {
		if !slices.ContainsFunc(snap.Warnings, func(w string) bool { return strings.Contains(w, want) }) {
			t.Errorf("no warning %q in %q", want, snap.Warnings)
		}
	}
	if m := modelByID(t, snap, "google/veo-3.1"); m.Offers[0].Params["notAccepted"].Type != "" {
		t.Fatal("a pass-through no route accepts was offered")
	}
}

func TestBuildKeepsTheModelOfAPartialOffer(t *testing.T) {
	up := fixtureUpstream(t)
	up.Offers = slices.DeleteFunc(up.Offers, func(of Offer) bool { return of.ID == "openrouter:krea/k3-edit" })
	up.Partial = []string{"openrouter:krea/k3-edit"}
	f, err := LoadFiles(fstest.MapFS{"overlay.yaml": {Data: []byte("models:\n  krea/k3-edit:\n    featured: true\n")}})
	if err != nil {
		t.Fatal(err)
	}
	snap := Build(up, f, fixtureDay)
	if !slices.Equal(snap.Keep, up.Partial) || len(snap.Warnings) != 0 {
		t.Fatalf("keep = %v, warnings = %v", snap.Keep, snap.Warnings)
	}
}

func TestPlainDropsLinks(t *testing.T) {
	got := plain("distilled from [Grok Imagine Video 1.5](/x-ai/grok-imagine-video-1.5). It ")
	if got != "distilled from Grok Imagine Video 1.5. It" {
		t.Fatal(got)
	}
	if got := plain("member of the [MAI-Image-2.6](/mic..."); got != "member of the MAI-Image-2.6..." {
		t.Fatal(got)
	}
}
