// SPDX-License-Identifier: Apache-2.0
package gemini

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// referencesParam is how the catalog says how many reference images a model
// takes. It is not a setting: the references are the job's inputs.
const referencesParam = "input_references"

// Settings of a video that are not catalog parameters, named as the
// OpenRouter adapter names them where they mean the same.
const (
	// framesSetting names which of the job's first inputs are frames: a list
	// of the model's frame types, such as ["first_frame", "last_frame"], one
	// per input in order. It is a catalog parameter too, whose values are the
	// types the model takes.
	framesSetting = "frame_images"
	// previousSetting continues or edits a video Omni made: the id of the
	// interaction that made it, on the same key.
	previousSetting = "previous_interaction_id"
)

// imageSizes are the values of image_size the API takes where the catalog's
// resolution differs: the 512 option is written "512px", and the K is always
// upper case.
var imageSizes = map[string]string{"512": "512px"}

// videoResolutions are the values of a video's resolution the API takes where
// the catalog's differ: the Interactions API writes 4K "4k".
var videoResolutions = map[string]string{"4K": "4k"}

// imageTypes are the formats of a reference image Gemini reads.
var imageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "image/heic": true, "image/heif": true,
}

// videoTypes are the formats of a video framy keeps that Gemini reads, by the
// name Gemini knows them by: QuickTime is "video/mov" there.
var videoTypes = map[string]string{
	"video/mp4": "video/mp4", "video/quicktime": "video/mov", "video/webm": "video/webm", "video/mpeg": "video/mpeg",
}

// interactionID is what an interaction's id looks like (v1_ and base64url),
// loosely: the id goes into a path, so only a plain run of characters is
// taken.
var interactionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,511}$`)

// request is the body of POST /interactions.
type request struct {
	Model            string            `json:"model"`
	Input            []inputBlock      `json:"input"`
	ResponseFormat   *responseFormat   `json:"response_format,omitempty"`
	GenerationConfig *generationConfig `json:"generation_config,omitempty"`
	Tools            []tool            `json:"tools,omitempty"`
	// PreviousInteractionID continues the conversation of an earlier run,
	// which Google must have stored.
	PreviousInteractionID string `json:"previous_interaction_id,omitempty"`
	// Background answers at once with the interaction's id; the run goes on
	// at Google and GET /interactions/{id} says where it is.
	Background bool `json:"background,omitempty"`
	// Store is always sent. An image sends false: framy keeps the results
	// itself, and Google then keeps neither the prompt nor the photos. A
	// video sends true, since Google refuses background without it and a
	// video is continued from the stored interaction.
	Store bool `json:"store"`
}

type inputBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
	// Resolution is how finely Gemini reads an image or a video:
	// media_resolution, set per part in the Interactions API.
	Resolution string `json:"resolution,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
	// MimeType is an image's format, PNG when absent.
	MimeType    string `json:"mime_type,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	// ImageSize is an image's resolution, Resolution a video's.
	ImageSize  string `json:"image_size,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	// Duration is a video's length, such as "6s".
	Duration string `json:"duration,omitempty"`
}

type generationConfig struct {
	ThinkingLevel string `json:"thinking_level,omitempty"`
}

type tool struct {
	Type string `json:"type"`
	// SearchTypes of google_search: web_search, image_search or both. Web
	// search alone when absent.
	SearchTypes []string `json:"search_types,omitempty"`
}

// plan is a run that framy has checked, before anything is fetched or paid.
type plan struct {
	kind   catalog.Kind
	model  string
	prompt string
	format *responseFormat
	config *generationConfig
	tools  []tool
	// detail is media_resolution, for every image and video sent.
	detail string
	// previous is the interaction a video continues.
	previous string
	// inputs are the files in the order they are sent: the first frame
	// before the last, then the rest as the job lists them.
	inputs []jobs.Input
}

// planFor checks the run against the offer's descriptor. What framy can tell
// is wrong comes back as refused, so nothing is paid for it:
//
//   - a setting the offer does not list, or a value it does not allow;
//   - a setting the catalog lists but this adapter does not know how to send,
//     which would otherwise be dropped without a word;
//   - more reference images than an image model takes, a file Gemini does
//     not read, or a video for a model that takes none.
//
// How many files of each kind a video takes is the catalog's to check
// (catalog.Inputs), before the job is made.
func planFor(j jobs.Job) (plan, error) {
	of := j.Offer
	video := j.Kind == catalog.Video
	if of.ProviderModelID == "" {
		return plan{}, refused("the offer %s has no model id", j.OfferID)
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return plan{}, refused("the prompt is empty")
	}
	p := plan{kind: j.Kind, model: of.ProviderModelID, prompt: j.Prompt, inputs: j.Inputs}
	takesVideo := slices.Contains(of.InputModalities, "video")
	images := 0
	for i, in := range j.Inputs {
		_, isVideo := videoTypes[in.ContentType]
		switch {
		case imageTypes[in.ContentType]:
			images++
		case isVideo && !takesVideo:
			return plan{}, refused("input %d is a video, and %s takes none", i+1, of.ID)
		case !isVideo && !takesVideo:
			return plan{}, refused("input %d is %q, and Gemini reads only PNG, JPEG, WebP, HEIC and HEIF images", i+1, in.ContentType)
		case !isVideo:
			return plan{}, refused("input %d is %q, and Gemini reads only images and MP4, QuickTime, WebM and MPEG videos", i+1, in.ContentType)
		}
		if in.URL == "" {
			return plan{}, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d has no link", i+1)}
		}
	}
	if !video {
		if err := takesReferences(of, images); err != nil {
			return plan{}, err
		}
	}

	var format responseFormat
	var frames []string
	web, imageSearch := false, false
	for _, name := range slices.Sorted(maps.Keys(j.Settings)) {
		v := j.Settings[name]
		if v == nil {
			continue
		}
		switch {
		case video && name == framesSetting:
			list, err := frameTypes(of, v)
			if err != nil {
				return plan{}, err
			}
			frames = list
			continue
		case video && name == previousSetting:
			id, ok := v.(string)
			if !ok || !interactionID.MatchString(id) {
				return plan{}, refused("%s is not the id of an interaction", name)
			}
			p.previous = id
			continue
		}
		param, listed := of.Params[name]
		if !listed || name == referencesParam {
			return plan{}, refused("%s is not a setting of %s", name, of.ID)
		}
		if !accepts(param, v) {
			return plan{}, refused("%s=%v is not allowed for %s", name, v, of.ID)
		}
		s, _ := v.(string)
		switch name {
		case "resolution":
			if video {
				format.Resolution = cmp.Or(videoResolutions[s], s)
			} else {
				format.ImageSize = cmp.Or(imageSizes[s], s)
			}
		case "aspect_ratio":
			format.AspectRatio = s
		case "duration":
			// Google writes seconds as "6s"; the catalog may list either.
			if secs, isNumber := number(v); isNumber {
				s = fmt.Sprintf("%ds", int(secs))
			}
			format.Duration = s
		case "output_format":
			format.MimeType = "image/" + s
		case "thinking_level":
			p.config = &generationConfig{ThinkingLevel: s}
		case "google_search":
			web, _ = v.(bool)
		case "image_search":
			imageSearch, _ = v.(bool)
		case "media_resolution":
			p.detail = s
		case "n":
			// The catalog allows one; Gemini has no such field.
		default:
			return plan{}, refused("framy does not send %s to Gemini yet", name)
		}
	}
	switch {
	case web && imageSearch:
		p.tools = []tool{{Type: "google_search", SearchTypes: []string{"web_search", "image_search"}}}
	case imageSearch:
		p.tools = []tool{{Type: "google_search", SearchTypes: []string{"image_search"}}}
	case web:
		p.tools = []tool{{Type: "google_search"}}
	}
	if len(frames) > 0 {
		var err error
		if p.inputs, p.prompt, err = framed(frames, j.Inputs, j.Prompt); err != nil {
			return plan{}, err
		}
	}
	if format != (responseFormat{}) {
		format.Type = "image"
		if video {
			format.Type = "video"
		}
		p.format = &format
	}
	return p, nil
}

// frameTypes reads the frame_images setting: types the offer takes, each
// once.
func frameTypes(of catalog.Offer, v any) ([]string, error) {
	var list []string
	switch l := v.(type) {
	case []string:
		list = l
	case []any:
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				return nil, refused("%s must list frame types such as first_frame", framesSetting)
			}
			list = append(list, s)
		}
	default:
		return nil, refused("%s must list frame types such as first_frame", framesSetting)
	}
	takes, listed := of.Params[framesSetting]
	seen := map[string]bool{}
	for _, f := range list {
		switch {
		case !listed:
			return nil, refused("%s takes no frame images", of.ID)
		case !accepts(takes, f):
			return nil, refused("%s does not take a frame of type %q", of.ID, f)
		case seen[f]:
			return nil, refused("two frames of type %q", f)
		}
		seen[f] = true
	}
	return list, nil
}

// framed puts the frames first, the first frame before the last, and names
// their roles in the prompt with Omni's tags: <FIRST_FRAME>, then
// <LAST_FRAME>, which Google takes only with a first frame. Without the tags
// Omni decides itself what two pictures are for.
func framed(frames []string, inputs []jobs.Input, prompt string) ([]jobs.Input, string, error) {
	if len(frames) > len(inputs) {
		return nil, "", refused("%s names %d frames, and the run has %d files", framesSetting, len(frames), len(inputs))
	}
	at := map[string]int{}
	for i, f := range frames {
		if !imageTypes[inputs[i].ContentType] {
			return nil, "", refused("input %d is a frame, and a frame is an image", i+1)
		}
		at[f] = i
	}
	first, hasFirst := at["first_frame"]
	if !hasFirst {
		return nil, "", refused("Omni takes a last frame only together with a first one")
	}
	out := []jobs.Input{inputs[first]}
	tags := "<FIRST_FRAME>"
	if last, ok := at["last_frame"]; ok {
		out = append(out, inputs[last])
		tags += " <LAST_FRAME>"
	}
	out = append(out, inputs[len(frames):]...)
	return out, tags + " " + prompt, nil
}

// takesReferences checks the number of reference images of an image model
// against the descriptor.
func takesReferences(of catalog.Offer, refs int) error {
	p, listed := of.Params[referencesParam]
	switch {
	case refs == 0 && listed && p.Min != nil && *p.Min > 0:
		return refused("%s needs a reference image", of.ID)
	case refs > 0 && !listed:
		return refused("%s takes no reference images", of.ID)
	case refs > 0 && p.Max != nil && float64(refs) > *p.Max:
		return refused("%s takes at most %v reference images, not %d", of.ID, *p.Max, refs)
	}
	return nil
}

// accepts says whether v is a value of p.
func accepts(p catalog.Param, v any) bool {
	switch p.Type {
	case catalog.Enum:
		return slices.ContainsFunc(p.Values, func(x any) bool { return same(x, v) })
	case catalog.Range:
		f, ok := number(v)
		return ok && f == math.Trunc(f) && (p.Min == nil || f >= *p.Min) && (p.Max == nil || f <= *p.Max)
	case catalog.Boolean:
		_, ok := v.(bool)
		return ok
	}
	return false
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

// same compares a catalog value with a setting as JSON.
func same(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// body builds the request once the files are fetched. An image's prompt goes
// first and the pictures after it, as Google's editing example does; a
// video's files go first and the words after them, as every Omni example
// does.
func (p plan) body(files []file) ([]byte, error) {
	media := make([]inputBlock, 0, len(files))
	for _, f := range files {
		b := inputBlock{Type: "image", MimeType: f.contentType, Data: base64.StdEncoding.EncodeToString(f.data), Resolution: p.detail}
		if name, isVideo := videoTypes[f.contentType]; isVideo {
			b.Type, b.MimeType = "video", name
		}
		media = append(media, b)
	}
	text := inputBlock{Type: "text", Text: p.prompt}
	r := request{
		Model: p.model, ResponseFormat: p.format, GenerationConfig: p.config, Tools: p.tools,
		PreviousInteractionID: p.previous,
	}
	if p.kind == catalog.Video {
		r.Input = append(media, text)
		r.Background, r.Store = true, true
	} else {
		r.Input = append([]inputBlock{text}, media...)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	return raw, nil
}

// refused is a run the adapter does not send, since it would fail: framy's
// own words, before anything reaches the provider.
func refused(format string, args ...any) *jobs.Error {
	return &jobs.Error{Class: jobs.Invalid, Err: fmt.Errorf(format, args...)}
}
