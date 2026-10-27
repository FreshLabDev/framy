// SPDX-License-Identifier: Apache-2.0
package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// OpenAI's limits that framy can check before it pays for a call.
const (
	// maxPrompt is the longest prompt, in characters.
	maxPrompt = 32000
	// maxInputBytes is the largest reference image. Photos framy keeps are
	// far below it (the long side is brought to 2048 px), so only a data URL
	// is measured: a link is OpenAI's to fetch.
	maxInputBytes = 50 << 20
	// A size is any WxH whose sides are multiples of 16, with a side up to
	// 3840, a ratio up to 3:1 and 655,360 to 8,294,400 pixels.
	sizeStep      = 16
	maxSide       = 3840
	maxRatio      = 3
	minPixels     = 655_360
	maxPixels     = 8_294_400
	defaultSize   = "1024x1024"
	referencesKey = "input_references"
)

// Endpoints of the Images API, below the base.
const (
	generationsPath = "/images/generations"
	editsPath       = "/images/edits"
)

// reserved are the fields framy fills itself, or sends only by a decision of
// its own; no setting may set them. moderation is left to OpenAI's default,
// auto.
var reserved = map[string]bool{
	"model": true, "prompt": true, "images": true, "mask": true, "user": true, "safety_identifier": true,
	"stream": true, "partial_images": true, "response_format": true, "moderation": true,
	"input_fidelity": true, "style": true, referencesKey: true,
}

// wholeNumbers are the settings whose value is an integer, which a JSON
// number from the database may spell 2.0.
var wholeNumbers = map[string]bool{"n": true, "output_compression": true}

// request is a call ready to go: where, with what, and the rates the answer's
// usage is priced with.
type request struct {
	path  string
	body  map[string]any
	rates rates
}

// imageRequest builds the call of a job. What framy can tell is wrong comes
// back as refused, so nothing is paid for it:
//
//   - quality is always one of the offer's levels. OpenAI's own default,
//     auto, lets the model decide how much to spend, and the person has been
//     shown a price;
//   - size is a concrete WxH within OpenAI's rules, never auto. Any such size
//     goes, not only the catalog's list: the list is what the composer
//     draws, the rules are OpenAI's;
//   - everything else must be a setting of the offer with a value the offer
//     allows;
//   - references make it an edit, and they go as links OpenAI fetches.
func imageRequest(j jobs.Job) (request, error) {
	of := j.Offer
	switch {
	case of.ProviderModelID == "":
		return request{}, refused("the offer %s has no model id", j.OfferID)
	case strings.TrimSpace(j.Prompt) == "":
		return request{}, refused("the prompt is empty")
	case utf8.RuneCountInString(j.Prompt) > maxPrompt:
		return request{}, refused("the prompt is longer than %d characters", maxPrompt)
	}
	var refs []map[string]any
	for i, in := range j.Inputs {
		ref, err := reference(i, in)
		if err != nil {
			return request{}, err
		}
		refs = append(refs, ref)
	}
	if err := takesReferences(of, len(refs)); err != nil {
		return request{}, err
	}
	rates, err := ratesOf(of, len(refs) > 0)
	if err != nil {
		return request{}, err
	}

	settings := map[string]any{}
	for name, v := range j.Settings {
		if v == nil {
			continue
		}
		if reserved[name] {
			return request{}, refused("%s is not a setting", name)
		}
		p, ok := of.Params[name]
		if !ok {
			return request{}, refused("%s is not a setting of %s", name, of.ID)
		}
		v = normalize(name, v)
		if name == "size" {
			if err := checkSize(v); err != nil {
				return request{}, err
			}
		} else if !accepts(name, p, v) {
			return request{}, refused("%s=%v is not allowed for %s", name, v, of.ID)
		}
		settings[name] = v
	}
	if _, listed := of.Params["size"]; listed {
		if _, set := settings["size"]; !set {
			// The size the estimate assumes when none is chosen.
			settings["size"] = defaultSize
		}
	}
	switch q, _ := settings["quality"].(string); q {
	case "":
		return request{}, refused("quality is not set; framy never sends OpenAI's auto, whose price cannot be known before the run")
	case "auto":
		return request{}, refused("quality auto is not offered; its price cannot be known before the run")
	}
	format, _ := settings["output_format"].(string)
	if _, set := settings["output_compression"]; set && format != "jpeg" && format != "webp" {
		return request{}, refused("output_compression needs output_format jpeg or webp")
	}
	if bg, _ := settings["background"].(string); bg == "transparent" && format != "" && format != "png" && format != "webp" {
		return request{}, refused("a transparent background needs output_format png or webp")
	}

	body := map[string]any{
		"model":  of.ProviderModelID,
		"prompt": j.Prompt,
		// A hash, never the Telegram id (jobs.SafetyID).
		"safety_identifier": jobs.SafetyID(j.UserID),
	}
	for name, v := range settings {
		body[name] = v
	}
	req := request{path: generationsPath, body: body, rates: rates}
	if len(refs) > 0 {
		req.path = editsPath
		body["images"] = refs
	}
	return req, nil
}

// reference is one input as OpenAI's edits take it: {"image_url": …}, which
// holds a fully qualified URL or a base64 data URL.
func reference(i int, in jobs.Input) (map[string]any, error) {
	switch in.ContentType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return nil, refused("input %d is %q, and OpenAI's edits take only PNG, JPEG and WebP", i+1, in.ContentType)
	}
	switch {
	case in.URL == "":
		return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d has no link", i+1)}
	case strings.HasPrefix(in.URL, "https://"):
	case strings.HasPrefix(in.URL, "data:"):
		meta, data, found := strings.Cut(in.URL, ",")
		if !found || !strings.HasSuffix(meta, ";base64") {
			return nil, refused("input %d is a data URL that is not base64", i+1)
		}
		if n := base64.StdEncoding.DecodedLen(len(data)); n > maxInputBytes {
			return nil, refused("input %d is %d MiB, and OpenAI takes at most %d MiB a file", i+1, n>>20, maxInputBytes>>20)
		}
	default:
		return nil, refused("input %d is neither an https link nor a data URL", i+1)
	}
	return map[string]any{"image_url": in.URL}, nil
}

// takesReferences checks the number of references against the offer.
func takesReferences(of catalog.Offer, n int) error {
	p, listed := of.Params[referencesKey]
	switch {
	case n == 0 && listed && p.Min != nil && *p.Min > 0:
		return refused("%s needs a reference image", of.ID)
	case n > 0 && !listed:
		return refused("%s takes no reference images", of.ID)
	case n > 0 && p.Max != nil && float64(n) > *p.Max:
		return refused("%s takes at most %v reference images, not %d", of.ID, *p.Max, n)
	}
	return nil
}

// checkSize holds a size to OpenAI's rules for the GPT image models.
func checkSize(v any) error {
	s, _ := v.(string)
	a, b, ok := strings.Cut(s, "x")
	w, errW := strconv.Atoi(a)
	h, errH := strconv.Atoi(b)
	if !ok || errW != nil || errH != nil || w <= 0 || h <= 0 {
		return refused("size %v is not WxH; auto is not offered, since its price cannot be known before the run", v)
	}
	long, short := max(w, h), min(w, h)
	switch {
	case w%sizeStep != 0 || h%sizeStep != 0:
		return refused("size %s: both sides must be multiples of %d", s, sizeStep)
	case long > maxSide:
		return refused("size %s: a side is longer than %d px", s, maxSide)
	case long > maxRatio*short:
		return refused("size %s: the long side is more than %d times the short one", s, maxRatio)
	case w*h < minPixels || w*h > maxPixels:
		return refused("size %s: %d pixels is outside %d to %d", s, w*h, minPixels, maxPixels)
	}
	return nil
}

// accepts says whether v is a value of p.
func accepts(name string, p catalog.Param, v any) bool {
	switch p.Type {
	case catalog.Enum:
		return slices.ContainsFunc(p.Values, func(x any) bool { return same(x, v) })
	case catalog.Range:
		f, ok := number(v)
		if !ok || (p.Min != nil && f < *p.Min) || (p.Max != nil && f > *p.Max) {
			return false
		}
		return !wholeNumbers[name] || f == math.Trunc(f)
	case catalog.Boolean:
		_, ok := v.(bool)
		return ok
	case catalog.Text:
		_, ok := v.(string)
		return ok
	}
	return false
}

// normalize writes a whole number as one, so 2 never goes as 2.0.
func normalize(name string, v any) any {
	if !wholeNumbers[name] {
		return v
	}
	if f, ok := number(v); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return int64(f)
	}
	return v
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// same compares a catalog value with a setting as JSON, so 1 and 1.0 match.
func same(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func refused(format string, args ...any) *jobs.Error {
	return &jobs.Error{Class: jobs.Refused, Err: fmt.Errorf(format, args...)}
}
