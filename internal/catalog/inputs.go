// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"fmt"
	"slices"
)

// Inputs is how many files of each kind one run of an offer takes, from the
// offer's parameters and the overlay's references and source_video. A kind
// the offer does not take is nil.
type Inputs struct {
	Images *ImageInputs `json:"images,omitempty"`
	Videos *ClipInputs  `json:"videos,omitempty"`
	Audio  *ClipInputs  `json:"audio,omitempty"`
	// Files is the most files of all kinds together, where the maker caps
	// them below the sum.
	Files int `json:"files,omitempty"`
}

// ImageInputs are the pictures of a run. On a video model a picture is a
// frame or a reference, never both in one run: OpenRouter keeps the frames
// and drops the references.
type ImageInputs struct {
	Min int `json:"min"`
	Max int `json:"max"`
	// Refs is the most reference images, and Frames how many frame types
	// the model has (first, then last).
	Refs   int `json:"refs"`
	Frames int `json:"frames"`
}

// ClipInputs are the attached videos or sounds of a run.
type ClipInputs struct {
	Min int `json:"min"`
	Max int `json:"max"`
	// Role is "reference" for a clip the model imitates, or "source" for
	// the one video it edits.
	Role      string  `json:"role"`
	Secs      *Span   `json:"secs,omitempty"`
	TotalSecs float64 `json:"total_secs,omitempty"`
	MaxBytes  int64   `json:"max_bytes,omitempty"`
}

// Span is a range of seconds; 0 is no bound.
type Span struct {
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
}

// Roles of an attached clip.
const (
	RoleReference = "reference"
	RoleSource    = "source"
)

// The parameters that say how many pictures an offer takes: reference
// images of an image model, and the frame types of a video model.
const (
	referencesParam = "input_references"
	framesParam     = "frame_images"
)

// InputsOf works out what one run of the offer takes.
func InputsOf(m Model, of Offer) Inputs {
	takes := func(kind string) bool { return slices.Contains(of.InputModalities, kind) }
	refs := m.Traits.References
	if refs == nil {
		refs = &References{}
	}
	var in Inputs

	img := ImageInputs{}
	if p, ok := of.Params[referencesParam]; ok && p.Type == Range && p.Max != nil {
		img.Refs = int(*p.Max)
		if p.Min != nil {
			img.Min = int(*p.Min)
		}
	}
	if m.Kind == Video && takes("image") {
		img.Refs = refs.Image
		if p, ok := of.Params[framesParam]; ok {
			img.Frames = len(p.Values)
		}
		if refs.FrameRequired && img.Frames > 0 {
			img.Min = 1
		}
	} else if refs.Image > 0 && refs.Image < img.Refs {
		// The maker takes fewer than OpenRouter lists.
		img.Refs = refs.Image
	}
	img.Max = max(img.Refs, img.Frames)
	if img.Max > 0 {
		in.Images = &img
	}

	switch s := m.Traits.SourceVideo; {
	case refs.Video > 0 && takes("video"):
		in.Videos = clipInputs(refs.Video, refs.VideoClip)
	case s != nil && takes("video"):
		v := &ClipInputs{Max: 1, Role: RoleSource}
		if s.Required {
			v.Min = 1
		}
		if s.MinSeconds > 0 || s.MaxSeconds > 0 {
			v.Secs = &Span{Min: s.MinSeconds, Max: float64(s.MaxSeconds)}
		}
		v.MaxBytes = int64(s.MaxMiB) << 20
		in.Videos = v
	}
	if refs.Audio > 0 && takes("audio") {
		in.Audio = clipInputs(refs.Audio, refs.AudioClip)
	}
	in.Files = refs.Files
	return in
}

func clipInputs(n int, c *Clip) *ClipInputs {
	out := &ClipInputs{Max: n, Role: RoleReference}
	if c == nil {
		return out
	}
	if c.MinSeconds > 0 || c.MaxSeconds > 0 {
		out.Secs = &Span{Min: c.MinSeconds, Max: c.MaxSeconds}
	}
	out.TotalSecs = c.TotalSeconds
	out.MaxBytes = int64(c.MaxMB) * 1_000_000
	return out
}

// Count is the files of one run by kind. Frames is how many of the images
// go as frames; the rest are references.
type Count struct {
	Images, Frames, Videos, Audio int
	// VideoSecs and AudioSecs are the length of each clip, and VideoBytes
	// and AudioBytes its size, where known.
	VideoSecs, AudioSecs   []float64
	VideoBytes, AudioBytes []int64
}

// Check says what the run's files break of the offer's limits, nil when
// nothing.
func (in Inputs) Check(kind Kind, c Count) error {
	img := ImageInputs{}
	if in.Images != nil {
		img = *in.Images
	}
	refs := c.Images - c.Frames
	switch {
	case c.Frames > c.Images:
		return fmt.Errorf("%d frames named and %d images attached", c.Frames, c.Images)
	case c.Images > img.Max:
		return fmt.Errorf("at most %d images, and %d are attached", img.Max, c.Images)
	case c.Images < img.Min:
		return fmt.Errorf("at least %d images, and %d are attached", img.Min, c.Images)
	case c.Frames > img.Frames:
		return fmt.Errorf("at most %d frames, and %d are named", img.Frames, c.Frames)
	case kind == Video && refs > img.Refs:
		if img.Refs == 0 {
			return fmt.Errorf("no reference images; send the image as a frame")
		}
		return fmt.Errorf("at most %d reference images, and %d are attached", img.Refs, refs)
	case kind == Video && c.Frames > 0 && (refs > 0 || c.Videos > 0 || c.Audio > 0):
		return fmt.Errorf("frames and references do not go together: OpenRouter keeps the frames and drops the rest")
	case in.Files > 0 && c.Images+c.Videos+c.Audio > in.Files:
		return fmt.Errorf("at most %d files in all, and %d are attached", in.Files, c.Images+c.Videos+c.Audio)
	}
	if err := in.Videos.check("videos", c.Videos, c.VideoSecs, c.VideoBytes); err != nil {
		return err
	}
	return in.Audio.check("sounds", c.Audio, c.AudioSecs, c.AudioBytes)
}

// secsSlack is how much longer than its limit a clip may be. A part cut to
// 15 s comes out a frame longer (15.015 s at 59.94 fps), as the container
// counts it from the first frame's offset; free.sourceSlack is the same.
const secsSlack = 0.1

func (l *ClipInputs) check(what string, n int, secs []float64, sizes []int64) error {
	if l == nil {
		if n > 0 {
			return fmt.Errorf("no %s", what)
		}
		return nil
	}
	switch {
	case n > l.Max:
		return fmt.Errorf("at most %d %s, and %d are attached", l.Max, what, n)
	case n < l.Min:
		return fmt.Errorf("at least %d %s, and %d are attached", l.Min, what, n)
	}
	total, counted := 0.0, 0
	for _, s := range secs {
		switch {
		case s <= 0:
			continue
		case l.Secs != nil && l.Secs.Min > 0 && s < l.Secs.Min:
			return fmt.Errorf("%s of at least %g s, and one is %g s", what, l.Secs.Min, s)
		case l.Secs != nil && l.Secs.Max > 0 && s > l.Secs.Max+secsSlack:
			return fmt.Errorf("%s of at most %g s, and one is %g s", what, l.Secs.Max, s)
		}
		total += s
		counted++
	}
	if l.TotalSecs > 0 && total > l.TotalSecs+secsSlack*float64(counted) {
		return fmt.Errorf("%s of at most %g s together, and these are %g s", what, l.TotalSecs, total)
	}
	for _, b := range sizes {
		if l.MaxBytes > 0 && b > l.MaxBytes {
			return fmt.Errorf("%s of at most %.1f MB, and one is %.1f MB", what, float64(l.MaxBytes)/1e6, float64(b)/1e6)
		}
	}
	return nil
}
