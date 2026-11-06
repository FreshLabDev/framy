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
		// Veo 3.1 makes a video from reference images at 8 s only; Lite
		// takes none.
		{"google/veo-3.1", Inputs{Images: &ImageInputs{Max: 3, Refs: 3, Frames: 2, RefDurations: []float64{8}}}},
		{"google/veo-3.1-lite", Inputs{Images: &ImageInputs{Max: 2, Frames: 2}}},
		// A video model with frames only.
		{"x-ai/grok-imagine-video-1.5", Inputs{Images: &ImageInputs{Max: 1, Frames: 1}}},
		// OpenRouter runs Wan 2.6 from text or a start frame, never from
		// reference images.
		{"alibaba/wan-2.6", Inputs{Images: &ImageInputs{Max: 1, Frames: 1}}},
		// One that makes a video only from its start frame.
		{"tbc/mischief-1", Inputs{Images: &ImageInputs{Min: 1, Max: 1, Frames: 1}}},
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
	editor := Inputs{Videos: &ClipInputs{Min: 1, Max: 1, Role: RoleSource, Secs: &Span{Max: 10}, MaxBytes: 48 << 20}}
	frameOnly := Inputs{Images: &ImageInputs{Max: 1, Frames: 1}}
	styles := Inputs{Images: &ImageInputs{Min: 1, Max: 10, Refs: 10}}
	veo := Inputs{Images: &ImageInputs{Max: 3, Refs: 3, Frames: 2, RefDurations: []float64{8}}}
	short := []float64{3, 4, 5, 6, 7, 8, 9, 10}
	omni := Inputs{
		Images: &ImageInputs{Max: 6, Refs: 6, Frames: 2, FrameDurations: short},
		Videos: &ClipInputs{Max: 1, Role: RoleSource, Secs: &Span{Max: 15}, MaxBytes: 48 << 20, Durations: short},
	}
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
		// A part cut to the limit comes out a frame longer.
		{"a part cut to 15 s at 59.94 fps", seedance, Video, Count{Videos: 1, VideoSecs: []float64{15.015}}, ""},
		{"a clip past the frame", seedance, Video, Count{Videos: 1, VideoSecs: []float64{15.2}}, "at most 15 s"},
		{"two parts cut to 15 s together", seedance, Video, Count{Videos: 2, VideoSecs: []float64{7.53, 7.53}}, ""},
		{"a short clip", seedance, Video, Count{Videos: 1, VideoSecs: []float64{1}}, "at least 2 s"},
		{"clips too long together", seedance, Video, Count{Videos: 2, VideoSecs: []float64{10, 10}}, "together"},
		{"a clip an editor takes", editor, Video, Count{Videos: 1, VideoSecs: []float64{8}, VideoBytes: []int64{30 << 20}}, ""},
		{"a clip too big for an editor", editor, Video, Count{Videos: 1, VideoSecs: []float64{8}, VideoBytes: []int64{49 << 20}}, "at most 50.3 MB"},
		{"a reference to a frame-only model", frameOnly, Video, Count{Images: 1}, "send the image as a frame"},
		{"a frame to a frame-only model", frameOnly, Video, Count{Images: 1, Frames: 1}, ""},
		{"no frame to a model that needs one", Inputs{Images: &ImageInputs{Min: 1, Max: 1, Frames: 1}}, Video, Count{}, "at least 1 images"},
		{"a video to a model without", frameOnly, Video, Count{Videos: 1}, "no videos"},
		{"a style without its image", styles, Image, Count{}, "at least 1 images"},
		{"styles", styles, Image, Count{Images: 3}, ""},
		{"an image to a text-only model", Inputs{}, Image, Count{Images: 1}, "at most 0 images"},
		// Veo makes a video from references at 8 s only, from frames or words
		// at any length it lists.
		{"references at 8 s", veo, Video, Count{Images: 2, Duration: 8}, ""},
		{"references at 6 s", veo, Video, Count{Images: 2, Duration: 6}, "from these files is 8 s long, and 6 s is set"},
		{"references with no length set", veo, Video, Count{Images: 2}, ""},
		{"frames at 6 s", veo, Video, Count{Images: 2, Frames: 2, Duration: 6}, ""},
		{"words at 4 s", veo, Video, Count{Duration: 4}, ""},
		{"references at 6 s where two lengths go", Inputs{Images: &ImageInputs{Max: 3, Refs: 3, RefDurations: []float64{8, 10}}}, Video, Count{Images: 1, Duration: 6}, "is 8 or 10 s long"},
		// Omni 1.2 makes 3 to 15 s from words and pictures, and 3 to 10 s
		// from frames, on a clip and going on from its own video.
		{"omni from words at 15 s", omni, Video, Count{Duration: 15}, ""},
		{"omni from references at 15 s", omni, Video, Count{Images: 4, Duration: 15}, ""},
		{"omni from frames at 10 s", omni, Video, Count{Images: 2, Frames: 2, Duration: 10}, ""},
		{"omni from frames at 15 s", omni, Video, Count{Images: 2, Frames: 2, Duration: 15}, "is 3 to 10 s long, and 15 s is set"},
		{"omni on a clip at 12 s", omni, Video, Count{Videos: 1, VideoSecs: []float64{8}, Duration: 12}, "is 3 to 10 s long"},
		{"omni on a clip at 6 s", omni, Video, Count{Videos: 1, VideoSecs: []float64{8}, Duration: 6}, ""},
		{"omni going on at 12 s", omni, Video, Count{Continues: true, Duration: 12}, "is 3 to 10 s long"},
		{"omni going on at 10 s", omni, Video, Count{Continues: true, Duration: 10}, ""},
		{"omni on a clip of 16 s", omni, Video, Count{Videos: 1, VideoSecs: []float64{16}}, "at most 15 s"},
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

func TestDefaultDuration(t *testing.T) {
	for _, tc := range []struct {
		d    []float64
		want float64
	}{
		{[]float64{8}, 8},
		{[]float64{3, 4, 5, 6, 7, 8, 9, 10}, 5},
		{[]float64{10, 6, 8}, 6},
		{[]float64{2, 3, 4}, 4},
		{nil, 0},
	} {
		if got := DefaultDuration(tc.d); got != tc.want {
			t.Errorf("DefaultDuration(%v) = %g, want %g", tc.d, got, tc.want)
		}
	}
}

func TestSecondsReadsARunAsARange(t *testing.T) {
	for d, want := range map[string]string{
		"8": seconds([]float64{8}), "8 or 10": seconds([]float64{8, 10}), "4, 6 or 8": seconds([]float64{4, 6, 8}),
		"3 to 10": seconds([]float64{3, 4, 5, 6, 7, 8, 9, 10}), "3, 4 or 6": seconds([]float64{3, 4, 6}),
	} {
		if d != want {
			t.Errorf("seconds = %q, want %q", want, d)
		}
	}
}
