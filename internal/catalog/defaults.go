// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// Defaults are the values framy gives the settings of an offer that nobody
// chose. The composer shows them picked, a quote prices them, and a run on a
// person's key goes with them, so what is priced is what is sent. A free run
// goes with its plan's settings only.
//
// shape is the width over the height of the first file the run takes, 0
// when it takes none: the result then keeps that shape.
//
// Only the settings the composer draws get one (drawn); the rest stay the
// provider's, as does a setting that some upstream of OpenRouter would not
// take with that value, since sending it would narrow the upstreams
// OpenRouter may try while the estimate counts the cheapest.
func Defaults(of Offer, shape float64) map[string]any {
	out := map[string]any{}
	for name, p := range of.Params {
		if !drawn[name] || p.Passthrough {
			continue
		}
		if _, ratio := of.Params["aspect_ratio"]; name == "size" && (of.Kind == Video || ratio) {
			// A size replaces the aspect ratio and the resolution: OpenRouter
			// refuses a video with both, and an image needs only one.
			continue
		}
		v, ok := defaultOf(of.Kind, name, p, shape)
		if ok && everyRouteTakes(of, name, v) {
			out[name] = v
		}
	}
	return out
}

// drawn are the settings the composer draws a control for. OpenRouter lists
// seed and an image's size as booleans that only say a route takes the
// field, so a boolean is a toggle only for sound, Google Search and Google
// Image Search.
var drawn = map[string]bool{
	"aspect_ratio": true, "size": true, "resolution": true, "quality": true, "background": true,
	"thinking_level": true, "n": true, "duration": true, "generate_audio": true, "google_search": true,
	"image_search": true, "upscale_factor": true, "creativity": true,
}

// Standard sizes: the side of an image and the height of a video that a
// default resolution reaches, and the length of a video.
const (
	imageSide   = 1024
	videoHeight = 720
	videoLength = 5
)

func defaultOf(kind Kind, name string, p Param, shape float64) (any, bool) {
	switch p.Type {
	case Enum:
		return enumDefault(kind, name, p.Values, shape)
	case Range:
		if p.Min == nil || p.Max == nil {
			return nil, false
		}
		return rangeDefault(name, *p.Min, *p.Max), true
	case Boolean:
		switch name {
		case "generate_audio":
			// OpenRouter's own default where a model has sound.
			return true, true
		case "google_search", "image_search":
			return false, true
		}
	}
	return nil, false
}

// enumDefault picks one of values:
//
//   - aspect_ratio: the shape of the first file, "auto" where the model
//     keeps it itself; without a file, the nearest to 1:1 for an image and
//     to 16:9 for a video;
//   - size: the same shape at about the standard area;
//   - resolution: the smallest that reaches 1K for an image or 720p for a
//     video, else the largest;
//   - duration: the shortest of at least 5 seconds, else the longest;
//   - quality: medium, else standard; background: auto;
//   - anything else: auto, else the first, which for an effort is the
//     least.
func enumDefault(kind Kind, name string, values []any, shape float64) (any, bool) {
	if len(values) == 0 || name == "frame_images" {
		return nil, false
	}
	target := shape
	if target <= 0 {
		target = 1
		if kind == Video {
			target = 16.0 / 9
		}
	}
	switch name {
	case "aspect_ratio":
		if shape > 0 {
			if v, ok := find(values, "auto"); ok {
				return v, true
			}
		}
		if v, ok := nearest(values, func(v any) (float64, bool) {
			r, ok := ratioOf(v)
			return math.Abs(math.Log(r / target)), ok
		}); ok {
			return v, true
		}
	case "size":
		area := float64(imageSide * imageSide)
		if kind == Video {
			area = videoHeight * videoHeight * 16.0 / 9
		}
		if v, ok := nearest(values, func(v any) (float64, bool) {
			w, h, ok := sizeOf(v)
			return math.Abs(math.Log(w/h/target)) + 0.15*math.Abs(math.Log(w*h/area)), ok
		}); ok {
			return v, true
		}
	case "resolution":
		standard := float64(imageSide)
		if kind == Video {
			standard = videoHeight
		}
		if v, ok := atLeast(values, standard, func(v any) (float64, bool) { return resolutionOf(kind, v) }); ok {
			return v, true
		}
	case "duration":
		if v, ok := atLeast(values, videoLength, number); ok {
			return v, true
		}
	case "quality":
		for _, want := range []string{"medium", "standard"} {
			if v, ok := find(values, want); ok {
				return v, true
			}
		}
	}
	if v, ok := find(values, "auto"); ok {
		return v, true
	}
	return values[0], true
}

// rangeDefault is one result, 5 seconds, or the middle.
func rangeDefault(name string, lo, hi float64) float64 {
	switch name {
	case "n":
		return min(max(lo, 1), hi)
	case "duration":
		return min(max(lo, videoLength), hi)
	}
	return (lo + hi) / 2
}

// everyRouteTakes reports whether every upstream of an OpenRouter image
// offer has this setting with this value. A route with no upstream, of a
// video or a direct provider, takes what the offer lists.
func everyRouteTakes(of Offer, name string, v any) bool {
	for _, r := range of.Routes {
		if r.Provider == "" {
			continue
		}
		p, ok := r.Params[name]
		if !ok {
			return false
		}
		switch p.Type {
		case Enum:
			if !slices.ContainsFunc(p.Values, func(x any) bool { return sameValue(x, v) }) {
				return false
			}
		case Range:
			f, _ := v.(float64)
			if (p.Min != nil && f < *p.Min) || (p.Max != nil && f > *p.Max) {
				return false
			}
		}
	}
	return true
}

func find(values []any, want string) (any, bool) {
	i := slices.IndexFunc(values, func(v any) bool { s, ok := v.(string); return ok && strings.EqualFold(s, want) })
	if i < 0 {
		return nil, false
	}
	return values[i], true
}

// nearest is the value of the least distance, the first of equals.
func nearest(values []any, distance func(any) (float64, bool)) (any, bool) {
	var best any
	bestD, found := math.Inf(1), false
	for _, v := range values {
		if d, ok := distance(v); ok && d < bestD-1e-9 {
			best, bestD, found = v, d, true
		}
	}
	return best, found
}

// atLeast is the smallest value that reaches want, else the largest.
func atLeast(values []any, want float64, measure func(any) (float64, bool)) (any, bool) {
	var reach, top any
	reachM, topM := math.Inf(1), math.Inf(-1)
	for _, v := range values {
		m, ok := measure(v)
		if !ok {
			continue
		}
		if m >= want && m < reachM {
			reach, reachM = v, m
		}
		if m > topM {
			top, topM = v, m
		}
	}
	switch {
	case reach != nil:
		return reach, true
	case top != nil:
		return top, true
	}
	return nil, false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// ratioOf reads "16:9" as 16/9.
func ratioOf(v any) (float64, bool) {
	s, _ := v.(string)
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return 0, false
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 0, false
	}
	return x / y, true
}

// sizeOf reads "1536x1024".
func sizeOf(v any) (w, h float64, ok bool) {
	s, _ := v.(string)
	a, b, cut := strings.Cut(strings.ToLower(s), "x")
	if !cut {
		return 0, 0, false
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 0, 0, false
	}
	return x, y, true
}

// resolutionOf measures a resolution in the unit of its standard: the side
// of an image ("1K" is 1024, "512" is 512) or the height of a video ("720p";
// "2K" is 1440).
func resolutionOf(kind Kind, v any) (float64, bool) {
	s, _ := v.(string)
	s = strings.ToLower(strings.TrimSpace(s))
	k := float64(imageSide)
	if kind == Video {
		k = videoHeight
	}
	switch {
	case strings.HasSuffix(s, "k"):
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "k"), 64)
		return f * k, err == nil && f > 0
	case strings.HasSuffix(s, "p"):
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "p"), 64)
		return f, err == nil && f > 0
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil && f > 0
}
