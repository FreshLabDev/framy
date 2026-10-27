// SPDX-License-Identifier: Apache-2.0

// Package catalog keeps the models framy can run and the offers they come in.
// Three sources feed it: what OpenRouter publishes, what catalog/openai.yaml
// and catalog/gemini.yaml describe by hand in the same form, and what
// catalog/overlay.yaml adds on top, which no API says. Build turns them into
// one Snapshot; the store saves it and disables what left.
package catalog

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type Kind string

const (
	Image Kind = "image"
	Video Kind = "video"
)

func (k Kind) valid() bool { return k == Image || k == Video }

// Providers a person can connect. An offer's id is "<provider>:<model id>".
const (
	OpenRouter = "openrouter"
	OpenAI     = "openai"
	Gemini     = "gemini"
)

// makers names the company behind a direct provider's models, for a model
// OpenRouter does not carry and the overlay does not name.
var makers = map[string]string{OpenAI: "OpenAI", Gemini: "Google"}

// OfferID is the offer's key in the catalog and in jobs.
func OfferID(provider, providerModelID string) string {
	return provider + ":" + providerModelID
}

// Param is one setting of a model, in OpenRouter's form: an enum of values, a
// range or a boolean. The composer draws a control from it, and a parameter a
// model does not list is not drawn at all.
type Param struct {
	Type   string   `json:"type" yaml:"type"`
	Values []any    `json:"values,omitempty" yaml:"values,omitempty"`
	Min    *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max    *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	// Passthrough marks a setting the provider takes among its own options
	// rather than as a field of the request. The overlay supplies its values,
	// since OpenRouter lists only the names.
	Passthrough bool `json:"passthrough,omitempty" yaml:"-"`
}

// Parameter types. Text exists only for pass-through settings such as a
// negative prompt.
const (
	Enum    = "enum"
	Range   = "range"
	Boolean = "boolean"
	Text    = "text"
)

func (p Param) validate(name string, allowText bool) error {
	switch p.Type {
	case Enum:
		if len(p.Values) == 0 {
			return fmt.Errorf("parameter %s: an enum needs values", name)
		}
	case Range:
		if p.Min == nil || p.Max == nil || *p.Min > *p.Max {
			return fmt.Errorf("parameter %s: a range needs min <= max", name)
		}
	case Boolean:
	case Text:
		if !allowText {
			return fmt.Errorf("parameter %s: text is only for pass-through settings", name)
		}
	default:
		return fmt.Errorf("parameter %s: unknown type %q", name, p.Type)
	}
	return nil
}

// without returns p with the given enum values removed. Values compare as
// their JSON, so 4 from YAML equals 4 from JSON.
func (p Param) without(drop []any) Param {
	if p.Type != Enum {
		return p
	}
	out := p
	out.Values = nil
	for _, v := range p.Values {
		if !slices.ContainsFunc(drop, func(d any) bool { return sameValue(d, v) }) {
			out.Values = append(out.Values, v)
		}
	}
	return out
}

func sameValue(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// Price is one billable line of a route, as OpenRouter publishes it for image
// endpoints: what is billed, per which unit, at what cost, and for which
// variant (a resolution tier, a surcharge).
type Price struct {
	Billable string  `json:"billable" yaml:"billable"`
	Unit     string  `json:"unit" yaml:"unit"`
	CostUSD  float64 `json:"cost_usd" yaml:"cost_usd"`
	Variant  string  `json:"variant,omitempty" yaml:"variant,omitempty"`
}

// Route is a place an offer's request can go. OpenRouter routes an image model
// to one of several upstream providers, each with its own parameters and
// price; a video model and a direct provider have one route.
type Route struct {
	// Provider is OpenRouter's slug of the upstream, such as
	// "google-ai-studio"; empty for a direct provider and for video.
	Provider    string           `json:"provider,omitempty"`
	Params      map[string]Param `json:"params,omitempty"`
	Passthrough []string         `json:"passthrough,omitempty"`
	Prices      []Price          `json:"prices,omitempty"`
	// SKUs is OpenRouter's pricing_skus of a video model, kept as published:
	// the units are not documented, and the estimate reads them.
	SKUs map[string]string `json:"skus,omitempty"`
}

func (r Route) accepts(passthrough string) bool {
	return slices.Contains(r.Passthrough, passthrough)
}

// Offer is a model at one provider.
type Offer struct {
	ID              string
	ModelID         string
	Provider        string
	ProviderModelID string
	Kind            Kind
	// Name and Description are the source's own, before the overlay.
	Name            string
	Description     string
	InputModalities []string
	Params          map[string]Param
	Routes          []Route
	// ExpiresOn is the day OpenRouter says the model leaves, if it said so.
	ExpiresOn *time.Time
	// Hidden says why the picker leaves the offer out; empty while shown.
	Hidden string
}

// Effort is the one "Effort" control framy shows for whatever the model calls
// it, with the values in order from least to most.
type Effort struct {
	Parameter string   `json:"parameter" yaml:"parameter"`
	Values    []string `json:"values" yaml:"values"`
}

// References is how many inputs of each kind a video model takes; OpenRouter
// publishes no numbers.
type References struct {
	Image int `json:"image,omitempty" yaml:"image,omitempty"`
	Video int `json:"video,omitempty" yaml:"video,omitempty"`
	Audio int `json:"audio,omitempty" yaml:"audio,omitempty"`
}

// Face policies of a model towards photos of real people.
const (
	// FacesRejected: a photo with a real person's face is refused.
	FacesRejected = "rejected"
	// FacesAdults: adults are accepted, children are not.
	FacesAdults = "adults"
)

// SourceVideo is what a model needs from an attached video: an uploaded one,
// or a finished result through "Edit". Any model whose offer takes video
// input works on one; this says more only where a source does.
type SourceVideo struct {
	// Required: the model runs only on an attached video, as editors and
	// upscalers do.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`
	// MaxSeconds and MaxMiB are the longest and the largest source the model
	// takes, checked before the run.
	MaxSeconds int `json:"max_seconds,omitempty" yaml:"max_seconds,omitempty"`
	MaxMiB     int `json:"max_mib,omitempty" yaml:"max_mib,omitempty"`
	// KeepsLength: the result is as long as the source, so the source's
	// length sets the price.
	KeepsLength bool `json:"keeps_length,omitempty" yaml:"keeps_length,omitempty"`
}

// Length is a range of seconds.
type Length struct {
	Min int `json:"min" yaml:"min"`
	Max int `json:"max" yaml:"max"`
}

// Any stands for any value: of the resolution in PriceHints.Tokens, of a
// setting in PriceHints.Variants.
const Any = "*"

// PriceHints is what the price estimate needs beyond the listed rates.
type PriceHints struct {
	// Tokens is how many tokens a billable takes per image, by the value of
	// the resolution setting, or Any. Output video counts per second.
	Tokens map[string]map[string]int `json:"tokens,omitempty" yaml:"tokens"`
	// OpenAITokens is the base of OpenAI's image token formula by quality,
	// from the official calculator.
	OpenAITokens map[string]int `json:"openai_tokens,omitempty" yaml:"openai_tokens"`
	// InputTokensPerMP is how many tokens an input image takes per
	// megapixel, for a model that counts them by the image's size. It wins
	// over Tokens[input_image] when the sizes are known.
	InputTokensPerMP float64 `json:"input_tokens_per_mp,omitempty" yaml:"input_tokens_per_mp"`
	// Variants says when a price variant applies: when every listed setting
	// has its value, Any meaning any value. A variant not listed applies when
	// its parts are chosen values, as low_1k is quality low at resolution 1K.
	Variants map[string]map[string]any `json:"variants,omitempty" yaml:"variants"`
	// Skip names billables framy never sends, with the reason.
	Skip map[string]string `json:"skip,omitempty" yaml:"skip"`
	// From: the listed rates are the least a run costs, and the provider
	// sets the rest after it, as Riverflow 2.5 does.
	From string `json:"from,omitempty" yaml:"from"`
}

// Traits is what the overlay knows about a model and no API says.
type Traits struct {
	Effort      *Effort      `json:"effort,omitempty"`
	References  *References  `json:"references,omitempty"`
	SourceVideo *SourceVideo `json:"source_video,omitempty"`
	// PicksLength: the model chooses the video's length within this range,
	// so the price is known only up to its top.
	PicksLength *Length     `json:"picks_length,omitempty"`
	Price       *PriceHints `json:"price,omitempty"`
	// Continue: a finished result can be continued or edited on the same
	// model, through previous_job_id or Omni's previous_interaction_id.
	Continue bool   `json:"continue,omitempty"`
	Faces    string `json:"faces,omitempty"`
}

// Model is what a person picks: one model, offered by one or more providers.
type Model struct {
	ID          string
	Kind        Kind
	Name        string
	Maker       string
	Description string
	Featured    bool
	Free        bool
	Traits      Traits
	Hidden      string
	Offers      []Offer
}

// Snapshot is the whole catalog as the sources describe it now.
type Snapshot struct {
	Models []Model
	// Keep lists offers whose details could not be fetched this time: the
	// store leaves their rows as they are instead of disabling them.
	Keep []string
	// Warnings are mismatches between the overlay and the sources, such as a
	// featured model that left OpenRouter. They do not stop a sync.
	Warnings []string
}

// Offers returns every offer of the snapshot.
func (s Snapshot) Offers() []Offer {
	var out []Offer
	for _, m := range s.Models {
		out = append(out, m.Offers...)
	}
	return out
}
