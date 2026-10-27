// SPDX-License-Identifier: Apache-2.0
package free

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	catalogfiles "github.com/FreshLabDev/framy/catalog"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

func TestImagesGoToMAIUntilMoreThanFivePhotosComeIn(t *testing.T) {
	for photos, want := range map[int]Plan{0: ImageMAI, 1: ImageMAI, 5: ImageMAI, 6: ImageKlein, 10: ImageKlein} {
		got, err := ForImages(photos)
		if err != nil || got.Offer != want.Offer {
			t.Errorf("%d photos: %v, %v; want %s", photos, got.Offer, err, want.Offer)
		}
	}
	var notFree *NotFreeError
	if _, err := ForImages(11); !errors.As(err, &notFree) {
		t.Errorf("11 photos = %v; more than Klein takes needs the person's own key", err)
	}
	if ImageMAI.Offer != "openrouter:microsoft/mai-image-2.6-flash" || ImageKlein.Offer != "openrouter:black-forest-labs/flux-3-image-klein" {
		t.Errorf("offers = %s, %s", ImageMAI.Offer, ImageKlein.Offer)
	}
}

func TestAFreeRunIsTheFixedOne(t *testing.T) {
	ok := func(p Plan, inputs int) jobs.Admission {
		return jobs.Admission{UserID: 1, Offer: p.Offer, Kind: p.Kind, Settings: p.SettingsFor(inputs), Inputs: inputs, EstimateUSD: 0.02}
	}
	with := func(a jobs.Admission, f func(*jobs.Admission)) jobs.Admission {
		f(&a)
		return a
	}
	cases := []struct {
		name string
		adm  jobs.Admission
		want string // part of the reason; empty for allowed
	}{
		{"MAI with its settings", ok(ImageMAI, 0), ""},
		{"MAI with five photos", ok(ImageMAI, 5), ""},
		{"MAI with six photos", ok(ImageMAI, 6), "at most 5 files"},
		{"Klein with an album", ok(ImageKlein, 10), ""},
		{"Klein with eleven photos", ok(ImageKlein, 11), "at most 10 files"},
		{"the weekly video from a start frame", ok(VideoHeyGen, 1), ""},
		{"the cheaper weekly video from a start frame", ok(VideoGrok, 1), ""},
		{"a start frame that is not named as one", with(ok(VideoHeyGen, 1), func(a *jobs.Admission) { a.Settings = VideoHeyGen.Settings() }), "frame_images must be [first_frame]"},
		{"a start frame named a last frame", with(ok(VideoHeyGen, 1), func(a *jobs.Admission) { a.Settings["frame_images"] = []any{"last_frame"} }), "frame_images is locked to [first_frame]"},
		{"a frame named with no file", with(ok(VideoHeyGen, 0), func(a *jobs.Admission) { a.Settings["frame_images"] = []string{"first_frame"} }), "frame_images is locked"},
		{"the weekly video with references", ok(VideoHeyGen, 2), "at most 1 file"},
		{"the video edit on a clip", ok(VideoEdit, 1), ""},
		{"the video edit with no clip", ok(VideoEdit, 0), "takes 1 file"},
		{"a model that is not free", jobs.Admission{UserID: 1, Offer: "openrouter:openai/gpt-image-2", Kind: catalog.Image, EstimateUSD: 0.02}, "is not a model of the free allowance"},
		{"the same model from the person's key route", jobs.Admission{UserID: 1, Offer: "gemini:gemini-nano-banana-2.1", Kind: catalog.Image, EstimateUSD: 0.02}, "is not a model"},
		{"two results", with(ok(ImageKlein, 0), func(a *jobs.Admission) { a.Settings["n"] = 2 }), "n is locked to 1"},
		{"Klein at 4K", with(ok(ImageKlein, 0), func(a *jobs.Admission) { a.Settings["resolution"] = "4K" }), "resolution is locked to 1K"},
		{"an aspect ratio", with(ok(ImageKlein, 0), func(a *jobs.Admission) { a.Settings["aspect_ratio"] = "16:9" }), "aspect_ratio is locked"},
		{"Klein with no resolution", with(ok(ImageKlein, 0), func(a *jobs.Admission) { delete(a.Settings, "resolution") }), "resolution must be 1K"},
		{"a setting left empty", with(ok(ImageKlein, 0), func(a *jobs.Admission) { a.Settings["seed"] = nil }), ""},
		{"a number that came from a document", with(ok(ImageKlein, 0), func(a *jobs.Admission) { a.Settings["n"] = float64(1) }), ""},
		{"an image offer that says it is a video", jobs.Admission{UserID: 1, Offer: ImageMAI.Offer, Kind: catalog.Video, Settings: ImageMAI.Settings(), EstimateUSD: 0.02}, "makes images, not videos"},
		{"no estimate", jobs.Admission{UserID: 1, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings()}, "no price estimate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, found := planOf(c.adm.Offer)
			var err error
			if found {
				err = p.check(c.adm)
			} else {
				err = &NotFreeError{Reason: c.adm.Offer + " is not a model of the free allowance"}
			}
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "":
				var notFree *NotFreeError
				if !errors.As(err, &notFree) || !strings.Contains(notFree.Reason, c.want) {
					t.Fatalf("err = %v, want a NotFreeError with %q", err, c.want)
				}
			}
		})
	}
}

func TestPlanSettingsAreACopy(t *testing.T) {
	s := ImageKlein.Settings()
	s["resolution"] = "4K"
	if ImageKlein.Settings()["resolution"] != "1K" {
		t.Fatal("a surface changed the plan's settings")
	}
}

// A video from a start frame names the file as one, so the adapter sends it as
// a frame and OpenRouter prices it as one; no other run does.
func TestOnlyAVideoFromAStartFrameNamesItsFile(t *testing.T) {
	for _, tc := range []struct {
		plan   Plan
		inputs int
		frames bool
	}{
		{VideoHeyGen, 0, false},
		{VideoHeyGen, 1, true},
		{VideoGrok, 0, false},
		{VideoGrok, 1, true},
		{VideoEdit, 1, false},
		{ImageMAI, 0, false},
		{ImageMAI, 3, false},
	} {
		got, has := tc.plan.SettingsFor(tc.inputs)["frame_images"]
		if has != tc.frames {
			t.Errorf("%s with %d files: frame_images %v", tc.plan.Model, tc.inputs, got)
		}
		if tc.frames && len(got.([]string)) != 1 {
			t.Errorf("%s: frame_images %v, want one frame", tc.plan.Model, got)
		}
	}
	// Settings is the same as no file, and every call is a copy.
	s := VideoHeyGen.SettingsFor(1)
	s["frame_images"].([]string)[0] = "last_frame"
	s["duration"] = 15
	if again := VideoHeyGen.SettingsFor(1); again["frame_images"].([]string)[0] != "first_frame" || again["duration"] != 5 {
		t.Fatal("a surface changed the plan's settings")
	}
	if _, has := VideoHeyGen.Settings()["frame_images"]; has {
		t.Error("Settings() names a frame with no file")
	}
}

// The overlay flags the models the catalog lists as free, and the allowance
// runs exactly those: a model flagged and not planned would show a free badge
// the server refuses.
func TestTheOverlayFlagsTheModelsOfTheAllowance(t *testing.T) {
	files, err := catalog.LoadFiles(catalogfiles.FS)
	if err != nil {
		t.Fatal(err)
	}
	var flagged []string
	for id, m := range files.Overlay.Models {
		if m.Free {
			flagged = append(flagged, id)
		}
	}
	slices.Sort(flagged)
	planned := Models()
	slices.Sort(planned)
	if !slices.Equal(flagged, planned) {
		t.Fatalf("free in the overlay: %v\nplanned in free: %v", flagged, planned)
	}
}

func TestTheErrorsSayWhatToSay(t *testing.T) {
	at := time.Date(2026, 10, 28, 14, 5, 0, 0, time.UTC)
	for _, c := range []struct {
		err  error
		want []string
	}{
		{&LimitError{Kind: catalog.Image, Limit: 5, Window: ImageWindow, FreesAt: at}, []string{"5 images in 24 hours", "2026-10-28T14:05:00Z"}},
		{&LimitError{Kind: catalog.Video, Limit: 1, Window: VideoWindow, FreesAt: at}, []string{"1 video in 7 days"}},
		{&ClosedError{Fuse: FuseBudget, Until: at, Why: "the day's budget is spent"}, []string{"closed until 2026-10-28T14:05:00Z", "the day's budget is spent"}},
		{&ClosedError{Fuse: FuseKey, Why: "the key is out"}, []string{"closed until further notice"}},
		{&NotFreeError{Reason: "n is locked"}, []string{"not part of the free allowance: n is locked"}},
	} {
		msg := c.err.Error()
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%q does not say %q", msg, w)
			}
		}
		if strings.ContainsRune(msg, '\u2014') {
			t.Errorf("%q has an em dash", msg)
		}
	}
}

func TestAMeterCountsWhatIsLeft(t *testing.T) {
	if got := (Meter{Used: 4, Limit: 5}).Left(); got != 1 {
		t.Errorf("left = %d", got)
	}
	if got := (Meter{Used: 7, Limit: 5}).Left(); got != 0 {
		t.Errorf("left = %d; the allowance cannot be negative", got)
	}
}
