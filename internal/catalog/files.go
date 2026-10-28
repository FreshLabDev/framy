// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

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
	// Makers maps another name of a maker to the one shown: OpenRouter names
	// one company two ways, and the picker groups models by maker.
	Makers map[string]string `yaml:"makers"`
	// Families names the picker's families. Default is the variant a family
	// opens on, when that is not its first featured one.
	Families map[string]FamilyInfo `yaml:"families"`
	// Tiers orders the tiers of a family's variants, least first.
	Tiers []string `yaml:"tiers"`
	// Recommend lists by situation the models the picker suggests first,
	// best first.
	Recommend map[string][]string `yaml:"recommend"`
	// Models is keyed by model id: the OpenRouter id, or a new id for a
	// model only a direct provider carries.
	Models map[string]ModelOverlay `yaml:"models"`
}

type FamilyInfo struct {
	Name    string `yaml:"name"`
	Default string `yaml:"default"`
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
	SourceVideo *SourceVideo     `yaml:"source_video"`
	PicksLength *Length          `yaml:"picks_length"`
	Price       *PriceHints      `yaml:"price"`
	Continue    bool             `yaml:"continue"`
	Faces       string           `yaml:"faces"`
	// Family places the model in one of the overlay's families.
	Family *Family `yaml:"family"`
	// FastOf makes the model a faster mode of another, shown as its switch.
	FastOf string `yaml:"fast_of"`
	// Deprecated: the maker said the model goes away.
	Deprecated *Deprecation `yaml:"deprecated"`
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
		if s := m.SourceVideo; s != nil {
			if *s == (SourceVideo{}) {
				return fmt.Errorf("overlay: model %s: source_video says nothing", id)
			}
			if s.MinSeconds < 0 || s.MaxSeconds < 0 || s.MaxMiB < 0 {
				return fmt.Errorf("overlay: model %s: source_video limits cannot be negative", id)
			}
			if s.MaxSeconds > 0 && s.MinSeconds > float64(s.MaxSeconds) {
				return fmt.Errorf("overlay: model %s: source_video needs min_seconds <= max_seconds", id)
			}
		}
		if err := m.References.validate(); err != nil {
			return fmt.Errorf("overlay: model %s: %w", id, err)
		}
		if d := m.Deprecated; d != nil {
			if d.ShutsDown != "" {
				if _, err := time.Parse(time.DateOnly, d.ShutsDown); err != nil {
					return fmt.Errorf("overlay: model %s: shuts_down %q is not a day such as 2026-11-02", id, d.ShutsDown)
				}
			}
			if d.Successor == id {
				return fmt.Errorf("overlay: model %s is its own successor", id)
			}
		}
		if l := m.PicksLength; l != nil && (l.Min <= 0 || l.Max < l.Min) {
			return fmt.Errorf("overlay: model %s: picks_length needs 0 < min <= max", id)
		}
		if err := m.Price.validate(); err != nil {
			return fmt.Errorf("overlay: model %s: %w", id, err)
		}
	}
	for _, offer := range f.Offers {
		if _, ok := owner[offer.ID]; !ok {
			// Names are not guessed: gemini:gemini-3-pro-image is not taken
			// for google/gemini-3-pro-image until the overlay says so.
			return fmt.Errorf("overlay: offer %s belongs to no model; list it under one", offer.ID)
		}
	}
	return o.validatePicker()
}

var (
	version = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
	word    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	slug    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// validatePicker checks what the model picker reads: makers, families,
// tiers, fast modes and suggestions.
func (o Overlay) validatePicker() error {
	tiers := map[string]bool{}
	for _, t := range o.Tiers {
		if !word.MatchString(t) {
			return fmt.Errorf("overlay: tier %q is not a lowercase word", t)
		}
		if tiers[t] {
			return fmt.Errorf("overlay: tier %s is listed twice", t)
		}
		tiers[t] = true
	}
	for from, to := range o.Makers {
		if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" || from == to {
			return fmt.Errorf("overlay: maker %q -> %q renames nothing", from, to)
		}
		if _, on := o.Makers[to]; on {
			return fmt.Errorf("overlay: maker %s becomes %s, which is renamed again", from, to)
		}
	}
	for id, f := range o.Families {
		if !slug.MatchString(id) {
			return fmt.Errorf("overlay: family id %q is not lowercase letters, digits and dashes", id)
		}
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("overlay: family %s has no name", id)
		}
		if m, ok := o.Models[f.Default]; f.Default != "" && (!ok || m.Family == nil || m.Family.ID != id) {
			return fmt.Errorf("overlay: family %s opens on %s, which is not one of its models", id, f.Default)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(o.Models)) {
		m := o.Models[id]
		if f := m.Family; f != nil {
			switch _, known := o.Families[f.ID]; {
			case !known:
				return fmt.Errorf("overlay: model %s: family %q is not in families", id, f.ID)
			case !version.MatchString(f.Gen):
				return fmt.Errorf("overlay: model %s: gen %q is not a version such as 2.5", id, f.Gen)
			case !tiers[f.Tier]:
				return fmt.Errorf("overlay: model %s: tier %q is not in tiers", id, f.Tier)
			case strings.TrimSpace(f.Label) == "":
				return fmt.Errorf("overlay: model %s: the family needs a label", id)
			case f.Line != "" && !word.MatchString(f.Line), f.Format != "" && !word.MatchString(f.Format):
				return fmt.Errorf("overlay: model %s: line and format are lowercase words", id)
			}
			if !tiers[PlainTier] {
				return fmt.Errorf("overlay: tiers must list %s, the tier of a plain variant", PlainTier)
			}
		}
		if m.FastOf != "" {
			switch {
			case m.FastOf == id:
				return fmt.Errorf("overlay: model %s is a fast mode of itself", id)
			case m.Family != nil:
				return fmt.Errorf("overlay: model %s: a fast mode is shown on its model, so it has no family", id)
			case m.Featured || m.Free:
				return fmt.Errorf("overlay: model %s: a fast mode is neither featured nor free", id)
			case o.Models[m.FastOf].FastOf != "":
				return fmt.Errorf("overlay: model %s: fast_of %s, which is a fast mode too", id, m.FastOf)
			}
		}
	}
	for sit, ids := range o.Recommend {
		if _, ok := situations[sit]; !ok {
			return fmt.Errorf("overlay: recommend %q is not a situation the picker knows", sit)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			switch {
			case id == "":
				return fmt.Errorf("overlay: recommend %s lists an empty id", sit)
			case seen[id]:
				return fmt.Errorf("overlay: recommend %s lists %s twice", sit, id)
			case o.Models[id].FastOf != "":
				return fmt.Errorf("overlay: recommend %s: %s is a fast mode, suggested through its model", sit, id)
			}
			seen[id] = true
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

func (r *References) validate() error {
	if r == nil {
		return nil
	}
	if *r == (References{}) {
		return errors.New("references says nothing")
	}
	if r.Image < 0 || r.Video < 0 || r.Audio < 0 || r.Files < 0 {
		return errors.New("references cannot be negative")
	}
	for name, c := range map[string]*Clip{"video_clip": r.VideoClip, "audio_clip": r.AudioClip} {
		if c == nil {
			continue
		}
		switch {
		case *c == (Clip{}):
			return fmt.Errorf("references.%s says nothing", name)
		case c.MinSeconds < 0 || c.MaxSeconds < 0 || c.TotalSeconds < 0 || c.MaxMB < 0:
			return fmt.Errorf("references.%s cannot be negative", name)
		case c.MaxSeconds > 0 && c.MinSeconds > c.MaxSeconds:
			return fmt.Errorf("references.%s needs min_seconds <= max_seconds", name)
		}
	}
	if r.VideoClip != nil && r.Video == 0 {
		return errors.New("references.video_clip without references.video")
	}
	if r.AudioClip != nil && r.Audio == 0 {
		return errors.New("references.audio_clip without references.audio")
	}
	return nil
}

func (h *PriceHints) validate() error {
	if h == nil {
		return nil
	}
	if len(h.Tokens) == 0 && len(h.OpenAITokens) == 0 && h.InputTokensPerMP == 0 && len(h.Variants) == 0 && len(h.Skip) == 0 && h.From == "" && h.FreeImages == 0 {
		return errors.New("price says nothing")
	}
	if h.FreeImages < 0 {
		return errors.New("price.free_images cannot be negative")
	}
	if h.InputTokensPerMP < 0 || math.IsNaN(h.InputTokensPerMP) || math.IsInf(h.InputTokensPerMP, 0) {
		return errors.New("price.input_tokens_per_mp must be above zero")
	}
	for billable, counts := range h.Tokens {
		if len(counts) == 0 {
			return fmt.Errorf("price.tokens.%s lists no counts", billable)
		}
		for res, n := range counts {
			if n <= 0 {
				return fmt.Errorf("price.tokens.%s.%s must be above zero", billable, res)
			}
		}
	}
	for quality, n := range h.OpenAITokens {
		if n <= 0 {
			return fmt.Errorf("price.openai_tokens.%s must be above zero", quality)
		}
	}
	for variant, when := range h.Variants {
		if len(when) == 0 {
			return fmt.Errorf("price.variants.%s lists no settings", variant)
		}
	}
	for billable, why := range h.Skip {
		if why == "" {
			return fmt.Errorf("price.skip.%s needs a reason", billable)
		}
	}
	return nil
}
