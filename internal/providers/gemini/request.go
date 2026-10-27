// SPDX-License-Identifier: Apache-2.0
package gemini

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// referencesParam is how the catalog says how many reference images a model
// takes. It is not a setting: the references are the job's inputs.
const referencesParam = "input_references"

// imageSizes are the values of image_size the API takes where the catalog's
// resolution differs: the 512 option is written "512px", and the K is always
// upper case.
var imageSizes = map[string]string{"512": "512px"}

// imageTypes are the formats of a reference image Gemini reads.
var imageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "image/heic": true, "image/heif": true,
}

// request is the body of POST /interactions.
type request struct {
	Model            string            `json:"model"`
	Input            []inputBlock      `json:"input"`
	ResponseFormat   *responseFormat   `json:"response_format,omitempty"`
	GenerationConfig *generationConfig `json:"generation_config,omitempty"`
	Tools            []tool            `json:"tools,omitempty"`
	// Store is always sent, and false: framy keeps the results itself, and
	// Google then keeps neither the prompt nor the photos for 55 days.
	Store bool `json:"store"`
}

type inputBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

type responseFormat struct {
	Type        string `json:"type"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	ImageSize   string `json:"image_size,omitempty"`
}

type generationConfig struct {
	ThinkingLevel string `json:"thinking_level,omitempty"`
}

type tool struct {
	Type string `json:"type"`
}

// plan is a run that framy has checked, before anything is fetched or paid.
type plan struct {
	model  string
	prompt string
	format *responseFormat
	config *generationConfig
	tools  []tool
	// inputs are the references, in order.
	inputs []jobs.Input
}

// planFor checks the run against the offer's descriptor. What framy can tell
// is wrong comes back as refused, so nothing is paid for it:
//
//   - a setting the offer does not list, or a value it does not allow;
//   - a setting the catalog lists but this adapter does not know how to send,
//     which would otherwise be dropped without a word;
//   - more references than the model takes, or one that is not an image.
func planFor(j jobs.Job) (plan, error) {
	of := j.Offer
	if of.ProviderModelID == "" {
		return plan{}, refused("the offer %s has no model id", j.OfferID)
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return plan{}, refused("the prompt is empty")
	}
	p := plan{model: of.ProviderModelID, prompt: j.Prompt, inputs: j.Inputs}
	for i, in := range j.Inputs {
		switch {
		case !imageTypes[in.ContentType]:
			return plan{}, refused("input %d is %q, and Gemini reads only PNG, JPEG, WebP, HEIC and HEIF images", i+1, in.ContentType)
		case in.URL == "":
			return plan{}, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d has no link", i+1)}
		}
	}
	if err := takesReferences(of, len(j.Inputs)); err != nil {
		return plan{}, err
	}

	var format responseFormat
	for _, name := range slices.Sorted(maps.Keys(j.Settings)) {
		v := j.Settings[name]
		if v == nil {
			continue
		}
		param, listed := of.Params[name]
		if !listed || name == referencesParam {
			return plan{}, refused("%s is not a setting of %s", name, of.ID)
		}
		if !accepts(param, v) {
			return plan{}, refused("%s=%v is not allowed for %s", name, v, of.ID)
		}
		switch name {
		case "resolution":
			s, _ := v.(string)
			format.ImageSize = cmp.Or(imageSizes[s], s)
		case "aspect_ratio":
			format.AspectRatio, _ = v.(string)
		case "thinking_level":
			level, _ := v.(string)
			p.config = &generationConfig{ThinkingLevel: level}
		case "google_search":
			if on, _ := v.(bool); on {
				p.tools = append(p.tools, tool{Type: "google_search"})
			}
		case "n":
			// The catalog allows one; Gemini has no such field.
		default:
			return plan{}, refused("framy does not send %s to Gemini yet", name)
		}
	}
	if format != (responseFormat{}) {
		format.Type = "image"
		p.format = &format
	}
	return p, nil
}

// takesReferences checks the number of references against the descriptor.
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

// body builds the request once the references are fetched. The prompt goes
// first and the pictures after it, as Google's editing example does.
func (p plan) body(files []file) ([]byte, error) {
	input := []inputBlock{{Type: "text", Text: p.prompt}}
	for _, f := range files {
		input = append(input, inputBlock{Type: "image", MimeType: f.contentType, Data: base64.StdEncoding.EncodeToString(f.data)})
	}
	raw, err := json.Marshal(request{
		Model: p.model, Input: input, ResponseFormat: p.format, GenerationConfig: p.config, Tools: p.tools,
	})
	if err != nil {
		return nil, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	return raw, nil
}

func refused(format string, args ...any) *jobs.Error {
	return &jobs.Error{Class: jobs.Refused, Err: fmt.Errorf(format, args...)}
}
