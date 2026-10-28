// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// What a run of each kind of model takes, from the real catalog.
func TestInputsOfTheCatalog(t *testing.T) {
	snap := Build(fixtureUpstream(t), embeddedFiles(t), fixtureDay)
	inputs := func(id string) Inputs {
		t.Helper()
		m := modelByID(t, snap, id)
		return InputsOf(m, m.Offers[0])
	}
	for _, tc := range []struct {
		id   string
		want Inputs
	}{
		// Frames or references: OpenRouter takes one or the other.
		{"bytedance/seedance-2.0", Inputs{
			Images: &ImageInputs{Max: 9, Refs: 9, Frames: 2},
			Videos: &ClipInputs{Max: 3, Role: RoleReference, Secs: &Span{Min: 2, Max: 15}, TotalSecs: 15, MaxBytes: 200_000_000},
			Audio:  &ClipInputs{Max: 3, Role: RoleReference, Secs: &Span{Min: 2, Max: 15}, TotalSecs: 15, MaxBytes: 15_000_000},
		}},
		{"minimax/hailuo-3", Inputs{
			Images: &ImageInputs{Max: 9, Refs: 9, Frames: 2},
			Videos: &ClipInputs{Max: 3, Role: RoleReference, Secs: &Span{Min: 2, Max: 15}, TotalSecs: 15, MaxBytes: 50_000_000},
			Audio:  &ClipInputs{Max: 3, Role: RoleReference, Secs: &Span{Max: 15}},
			Files:  12,
		}},
		// A video model with frames only.
		{"x-ai/grok-imagine-video-1.5", Inputs{Images: &ImageInputs{Max: 1, Frames: 1}}},
		// The maker takes fewer references than OpenRouter lists.
		{"bytedance-seed/seedream-5-0-pro", Inputs{Images: &ImageInputs{Max: 10, Refs: 10}}},
		{"recraft/recraft-v4-styles", Inputs{Images: &ImageInputs{Min: 1, Max: 10, Refs: 10}}},
		{"black-forest-labs/flux-video-edit", Inputs{
			Videos: &ClipInputs{Min: 1, Max: 1, Role: RoleSource, Secs: &Span{Min: 0.7, Max: 15}, MaxBytes: 50 << 20},
		}},
	} {
		if got := inputs(tc.id); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s takes %s, want %s", tc.id, show(got), show(tc.want))
		}
	}
}

func show(in Inputs) string {
	out := fmt.Sprintf("files %d", in.Files)
	if in.Images != nil {
		out += fmt.Sprintf(" images %+v", *in.Images)
	}
	for _, c := range []*ClipInputs{in.Videos, in.Audio} {
		if c != nil {
			out += fmt.Sprintf(" clips %+v", *c)
			if c.Secs != nil {
				out += fmt.Sprintf(" secs %+v", *c.Secs)
			}
		}
	}
	return out
}

func TestInputsCheck(t *testing.T) {
	seedance := Inputs{
		Images: &ImageInputs{Max: 9, Refs: 9, Frames: 2},
		Videos: &ClipInputs{Max: 3, Role: RoleReference, Secs: &Span{Min: 2, Max: 15}, TotalSecs: 15},
		Audio:  &ClipInputs{Max: 3, Role: RoleReference},
		Files:  12,
	}
	frameOnly := Inputs{Images: &ImageInputs{Max: 1, Frames: 1}}
	styles := Inputs{Images: &ImageInputs{Min: 1, Max: 10, Refs: 10}}
	for _, tc := range []struct {
		name string
		in   Inputs
		kind Kind
		c    Count
		want string
	}{
		{"text only", seedance, Video, Count{}, ""},
		{"two frames", seedance, Video, Count{Images: 2, Frames: 2}, ""},
		{"references of every kind", seedance, Video, Count{Images: 4, Videos: 2, Audio: 1, VideoSecs: []float64{5, 6}}, ""},
		{"three frames", seedance, Video, Count{Images: 3, Frames: 3}, "at most 2 frames"},
		{"frames and a video", seedance, Video, Count{Images: 1, Frames: 1, Videos: 1}, "do not go together"},
		{"ten images", seedance, Video, Count{Images: 10}, "at most 9 images"},
		{"too many files", seedance, Video, Count{Images: 9, Videos: 3, Audio: 1}, "at most 12 files"},
		{"a long clip", seedance, Video, Count{Videos: 1, VideoSecs: []float64{16}}, "at most 15 s"},
		{"a short clip", seedance, Video, Count{Videos: 1, VideoSecs: []float64{1}}, "at least 2 s"},
		{"clips too long together", seedance, Video, Count{Videos: 2, VideoSecs: []float64{10, 10}}, "together"},
		{"a reference to a frame-only model", frameOnly, Video, Count{Images: 1}, "send the image as a frame"},
		{"a frame to a frame-only model", frameOnly, Video, Count{Images: 1, Frames: 1}, ""},
		{"a video to a model without", frameOnly, Video, Count{Videos: 1}, "no videos"},
		{"a style without its image", styles, Image, Count{}, "at least 1 images"},
		{"styles", styles, Image, Count{Images: 3}, ""},
		{"an image to a text-only model", Inputs{}, Image, Count{Images: 1}, "at most 0 images"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Check(tc.kind, tc.c)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("err = %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want one with %q", err, tc.want)
			}
		})
	}
}
