// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Files is what catalog/ holds: the hand-written offers of the direct
// providers and the overlay. It is checked once when loaded, so a typo fails
// the build's tests and the start, not a sync an hour later.
type Files struct {
	Offers  []Offer
	Overlay Overlay
}

// Overlay adds to the sources what no API says.
type Overlay struct {
	// HideExpiringWithinDays hides an offer whose OpenRouter expiration date
	// is this close, so nobody starts relying on it.
	HideExpiringWithinDays int `yaml:"hide_expiring_within_days"`
	// Hide hides every model whose id matches, with the reason.
	Hide []HideRule `yaml:"hide"`
	// Models is keyed by model id: the OpenRouter id, or a new id for a
	// model only a direct provider carries.
	Models map[string]ModelOverlay `yaml:"models"`
}

type HideRule struct {
	// Match is a path.Match pattern over model ids, so "*" stays within one
	// segment: google/*-preview matches Google's previews only.
	Match string `yaml:"match"`
	Why   string `yaml:"why"`
}

type ModelOverlay struct {
	Name  string `yaml:"name"`
	Maker string `yaml:"maker"`
	// Offers joins offers of other providers to this model. An OpenRouter
	// offer belongs to the model of its own id unless listed elsewhere; a
	// hand-written offer must be listed.
	Offers   []string `yaml:"offers"`
	Featured bool     `yaml:"featured"`
	Free     bool     `yaml:"free"`
	// Hide hides the model, with the reason.
	Hide   string  `yaml:"hide"`
	Effort *Effort `yaml:"effort"`
	// Exclude removes values the composer must not offer, such as OpenAI's
	// quality=auto, whose price cannot be known before the run.
	Exclude map[string][]any `yaml:"exclude"`
	// Passthrough gives the values of settings an offer passes through to
	// its provider, which OpenRouter lists by name only. An offer gets the
	// setting only if one of its routes accepts it.
	Passthrough map[string]Param `yaml:"passthrough"`
	References  *References      `yaml:"references"`
	Continue    bool             `yaml:"continue"`
	Faces       string           `yaml:"faces"`
}

// descriptorFile is catalog/<provider>.yaml.
type descriptorFile struct {
	Provider string       `yaml:"provider"`
	Models   []descriptor `yaml:"models"`
}

// descriptor is one hand-written offer, in the form OpenRouter uses for an
// image endpoint.
type descriptor struct {
	ID                  string           `yaml:"id"`
	Kind                Kind             `yaml:"kind"`
	Name                string           `yaml:"name"`
	Description         string           `yaml:"description"`
	InputModalities     []string         `yaml:"input_modalities"`
	SupportedParameters map[string]Param `yaml:"supported_parameters"`
	Passthrough         []string         `yaml:"passthrough"`
	Pricing             []Price          `yaml:"pricing"`
}

// overlayFile is the overlay's file name in catalog/; every other .yaml file
// there describes one direct provider.
const overlayFile = "overlay.yaml"

// LoadFiles reads and checks catalog/.
func LoadFiles(fsys fs.FS) (Files, error) {
	var f Files
	names, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return Files{}, err
	}
	sawOverlay := false
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return Files{}, err
		}
		if name == overlayFile {
			sawOverlay = true
			if err := decodeStrict(data, &f.Overlay); err != nil {
				return Files{}, fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		var d descriptorFile
		if err := decodeStrict(data, &d); err != nil {
			return Files{}, fmt.Errorf("%s: %w", name, err)
		}
		offers, err := d.offers(name)
		if err != nil {
			return Files{}, err
		}
		f.Offers = append(f.Offers, offers...)
	}
	if !sawOverlay {
		return Files{}, fmt.Errorf("catalog: %s is missing", overlayFile)
	}
	if err := f.validate(); err != nil {
		return Files{}, err
	}
	return f, nil
}

// decodeStrict refuses unknown fields: a misspelt key would otherwise be a
// setting that silently does nothing.
func decodeStrict(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (d descriptorFile) offers(file string) ([]Offer, error) {
	if d.Provider != OpenAI && d.Provider != Gemini {
		return nil, fmt.Errorf("%s: provider must be %s or %s, not %q", file, OpenAI, Gemini, d.Provider)
	}
	if want := d.Provider + ".yaml"; file != want {
		return nil, fmt.Errorf("%s: the %s descriptors belong in %s", file, d.Provider, want)
	}
	seen := map[string]bool{}
	var out []Offer
	for _, m := range d.Models {
		if m.ID == "" || strings.ContainsAny(m.ID, ": ") {
			return nil, fmt.Errorf("%s: model id %q must be set and hold no colon or space", file, m.ID)
		}
		if seen[m.ID] {
			return nil, fmt.Errorf("%s: model %s is described twice", file, m.ID)
		}
		seen[m.ID] = true
		if !m.Kind.valid() {
			return nil, fmt.Errorf("%s: model %s: kind must be image or video", file, m.ID)
		}
		if m.Name == "" {
			return nil, fmt.Errorf("%s: model %s has no name", file, m.ID)
		}
		for name, p := range m.SupportedParameters {
			if err := p.validate(name, false); err != nil {
				return nil, fmt.Errorf("%s: model %s: %w", file, m.ID, err)
			}
		}
		if len(m.Pricing) == 0 {
			return nil, fmt.Errorf("%s: model %s has no pricing", file, m.ID)
		}
		for _, p := range m.Pricing {
			if p.Billable == "" || p.Unit == "" || p.CostUSD <= 0 {
				return nil, fmt.Errorf("%s: model %s: a price needs billable, unit and a positive cost_usd", file, m.ID)
			}
		}
		out = append(out, Offer{
			ID:              OfferID(d.Provider, m.ID),
			Provider:        d.Provider,
			ProviderModelID: m.ID,
			Kind:            m.Kind,
			Name:            m.Name,
			Description:     m.Description,
			InputModalities: m.InputModalities,
			Params:          m.SupportedParameters,
			Routes: []Route{{
				Params:      m.SupportedParameters,
				Passthrough: m.Passthrough,
				Prices:      m.Pricing,
			}},
		})
	}
	return out, nil
}

func (f Files) validate() error {
	o := f.Overlay
	if o.HideExpiringWithinDays < 0 {
		return fmt.Errorf("overlay: hide_expiring_within_days cannot be negative")
	}
	for _, r := range o.Hide {
		if r.Why == "" {
			return fmt.Errorf("overlay: hide rule %q needs a reason", r.Match)
		}
		if _, err := path.Match(r.Match, ""); err != nil || r.Match == "" {
			return fmt.Errorf("overlay: hide rule %q is not a valid pattern", r.Match)
		}
	}
	direct := map[string]bool{}
	for _, offer := range f.Offers {
		direct[offer.ID] = true
	}
	owner := map[string]string{}
	for id, m := range o.Models {
		if id == "" || strings.ContainsAny(id, ": ") {
			return fmt.Errorf("overlay: model id %q must hold no colon or space", id)
		}
		for _, offer := range m.Offers {
			provider, model, ok := strings.Cut(offer, ":")
			if !ok || model == "" || (provider != OpenRouter && provider != OpenAI && provider != Gemini) {
				return fmt.Errorf("overlay: model %s: offer %q is not <provider>:<model id>", id, offer)
			}
			if provider != OpenRouter && !direct[offer] {
				return fmt.Errorf("overlay: model %s: offer %s is not described in catalog/%s.yaml", id, offer, provider)
			}
			if prev, dup := owner[offer]; dup {
				return fmt.Errorf("overlay: offer %s is listed under both %s and %s", offer, prev, id)
			}
			owner[offer] = id
		}
		if m.Hide != "" && m.Featured {
			return fmt.Errorf("overlay: model %s is both hidden and featured", id)
		}
		if e := m.Effort; e != nil && (e.Parameter == "" || len(e.Values) < 2) {
			return fmt.Errorf("overlay: model %s: effort needs a parameter and at least two values", id)
		}
		for name, values := range m.Exclude {
			if len(values) == 0 {
				return fmt.Errorf("overlay: model %s: exclude %s lists no values", id, name)
			}
		}
		for name, p := range m.Passthrough {
			if err := p.validate(name, true); err != nil {
				return fmt.Errorf("overlay: model %s: %w", id, err)
			}
		}
		if m.Faces != "" && m.Faces != FacesRejected && m.Faces != FacesAdults {
			return fmt.Errorf("overlay: model %s: faces must be %s or %s", id, FacesRejected, FacesAdults)
		}
	}
	for _, offer := range f.Offers {
		if _, ok := owner[offer.ID]; !ok {
			// Names are not guessed: gemini:gemini-3-pro-image is not taken
			// for google/gemini-3-pro-image until the overlay says so.
			return fmt.Errorf("overlay: offer %s belongs to no model; list it under one", offer.ID)
		}
	}
	return nil
}

// modelOf returns the model an offer belongs to.
func (o Overlay) modelOf(offer Offer) string {
	for id, m := range o.Models {
		for _, listed := range m.Offers {
			if listed == offer.ID {
				return id
			}
		}
	}
	return offer.ProviderModelID
}
