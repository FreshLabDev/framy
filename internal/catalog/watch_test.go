// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestListedNotes(t *testing.T) {
	f := embeddedFiles(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		provider string
		listed   []Listed
		want     []string // substrings, one per note, in order
	}{
		{"what framy already knows is quiet", OpenAI, []Listed{
			{ID: "gpt-image-2.6-sunburst"}, {ID: "gpt-image-2.6-flare"}, {ID: "gpt-image-2.5-flare"}, {ID: "gpt-image-2"},
			// Hidden on purpose by the overlay, closing or closed.
			{ID: "gpt-image-1", ShutsDown: "2026-10-23"}, {ID: "gpt-image-1-mini", ShutsDown: "2026-12-01"},
			{ID: "gpt-image-1.5", ShutsDown: "2026-12-01"}, {ID: "gpt-image-1.5-2025-12-16", ShutsDown: "2026-12-01"},
			// Not image models, or not GPT Image at all.
			{ID: "gpt-5.2"}, {ID: "chatgpt-image-latest", ShutsDown: "2026-12-01"}, {ID: "text-embedding-3-small"}, {ID: "ft:gpt-image-x:acme"},
		}, nil},
		{"a new GPT Image", OpenAI, []Listed{{ID: "gpt-image-2.7-flare"}}, []string{"gpt-image-2.7-flare, which catalog/openai.yaml does not describe"}},
		{"a dated snapshot of a known model is not new", OpenAI, []Listed{{ID: "gpt-image-2.6-flare-2026-11-04"}}, nil},
		{"a dated snapshot of an unknown model is", OpenAI, []Listed{{ID: "gpt-image-2.7-flare-2026-12-04"}}, []string{"gpt-image-2.7-flare-2026-12-04"}},
		{"an unknown model already gone is not news", OpenAI, []Listed{{ID: "gpt-image-0.5", ShutsDown: "2026-01-01"}}, nil},
		{"an unknown model on its way out is", OpenAI, []Listed{{ID: "gpt-image-0.5", ShutsDown: "2027-01-01"}}, []string{"gpt-image-0.5"}},
		{"a shutdown day the overlay lacks", OpenAI, []Listed{{ID: "gpt-image-2.5-flare", ShutsDown: "2027-03-01"}},
			[]string{"gpt-image-2.5-flare shuts down on 2027-03-01, and catalog/overlay.yaml has no shutdown day for openai/gpt-image-2.5-flare"}},
		{"a malformed day is no day", OpenAI, []Listed{{ID: "gpt-image-2.5-flare", ShutsDown: "soon"}}, nil},
		{"the same thing twice is told once", OpenAI, []Listed{{ID: "gpt-image-3"}, {ID: "gpt-image-3"}}, []string{"gpt-image-3"}},
		{"Gemini has no such list", "gemini", []Listed{{ID: "gpt-image-3"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f.ListedNotes(tc.provider, tc.listed, now)
			if len(got) != len(tc.want) {
				t.Fatalf("notes = %q, want %d of them like %q", got, len(tc.want), tc.want)
			}
			for i := range got {
				if !strings.Contains(got[i], tc.want[i]) {
					t.Errorf("note %d = %q, want it to contain %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// A shutdown day that the overlay has already is not told again, and one it
// has differently is.
func TestListedNotesCompareWithTheOverlay(t *testing.T) {
	f := embeddedFiles(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	m := f.Overlay.Models["openai/gpt-image-2.5-flare"]
	m.Deprecated = &Deprecation{ShutsDown: "2027-03-01"}
	f.Overlay.Models = cloneModels(f.Overlay.Models)
	f.Overlay.Models["openai/gpt-image-2.5-flare"] = m

	if got := f.ListedNotes(OpenAI, []Listed{{ID: "gpt-image-2.5-flare", ShutsDown: "2027-03-01"}}, now); len(got) != 0 {
		t.Errorf("the same day: %q", got)
	}
	got := f.ListedNotes(OpenAI, []Listed{{ID: "gpt-image-2.5-flare", ShutsDown: "2027-04-01"}}, now)
	if len(got) != 1 || !strings.Contains(got[0], "has 2027-03-01") {
		t.Errorf("another day: %q", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("notes are not sorted: %q", got)
	}
}

func cloneModels(m map[string]ModelOverlay) map[string]ModelOverlay {
	out := make(map[string]ModelOverlay, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
