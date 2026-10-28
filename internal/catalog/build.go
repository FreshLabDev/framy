// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Build joins what OpenRouter listed with the hand-written offers and applies
// the overlay. It does not fail on upstream drift, such as an overlay entry
// for a model OpenRouter dropped: that becomes a warning, and the sync goes
// on.
func Build(up Upstream, files Files, now time.Time) Snapshot {
	o := files.Overlay
	snap := Snapshot{Keep: slices.Clone(up.Partial)}
	warn := func(format string, args ...any) {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf(format, args...))
	}

	expiringBefore := day(now).AddDate(0, 0, o.HideExpiringWithinDays)
	byModel := map[string][]Offer{}
	for _, offer := range append(slices.Clone(up.Offers), files.Offers...) {
		offer.ModelID = o.modelOf(offer)
		offer = applyOverlay(offer, o.Models[offer.ModelID])
		switch {
		case len(offer.Routes) == 0:
			offer.Hidden = "no route runs it"
		case offer.ExpiresOn != nil && offer.ExpiresOn.Before(expiringBefore):
			offer.Hidden = "expires " + offer.ExpiresOn.Format(time.DateOnly)
		}
		byModel[offer.ModelID] = append(byModel[offer.ModelID], offer)
	}
	// An offer the store keeps belongs to a model that must not look gone.
	kept := map[string]bool{}
	for _, id := range up.Partial {
		provider, model, _ := strings.Cut(id, ":")
		kept[o.modelOf(Offer{ID: id, Provider: provider, ProviderModelID: model})] = true
	}

	for _, id := range slices.Sorted(maps.Keys(byModel)) {
		offers := byModel[id]
		slices.SortFunc(offers, func(a, b Offer) int { return strings.Compare(a.ID, b.ID) })
		kind := offers[0].Kind
		offers = slices.DeleteFunc(offers, func(of Offer) bool {
			if of.Kind != kind {
				warn("model %s: offer %s is a %s model, the others are %s; left out", id, of.ID, of.Kind, kind)
				return true
			}
			return false
		})
		m := describe(id, kind, offers, o.Models[id], o.Makers)
		m.Hidden = hiddenBy(id, o)
		// The maker's own shutdown day hides a model as OpenRouter's does an
		// offer: nobody should start relying on it.
		if d := m.Traits.Deprecated; m.Hidden == "" && d != nil && d.ShutsDown != "" {
			if t, err := time.Parse(time.DateOnly, d.ShutsDown); err == nil && t.Before(expiringBefore) {
				m.Hidden = "shuts down " + d.ShutsDown
			}
		}
		if m.Featured && m.Hidden != "" {
			warn("model %s is featured but hidden: %s", id, m.Hidden)
		}
		if m.Featured && !slices.ContainsFunc(offers, func(of Offer) bool { return of.Hidden == "" }) {
			warn("model %s is featured but every offer is hidden", id)
		}
		if e := m.Traits.Effort; e != nil {
			checkEffort(id, *e, offers, warn)
		}
		if kind == Video && m.Hidden == "" {
			checkSourceVideo(id, m.Traits.SourceVideo, offers, warn)
		}
		if h := m.Traits.Price; h != nil {
			checkPrice(id, *h, offers, warn)
		}
		for _, name := range slices.Sorted(maps.Keys(o.Models[id].Passthrough)) {
			if !slices.ContainsFunc(offers, func(of Offer) bool { _, ok := of.Params[name]; return ok }) {
				warn("model %s: no offer accepts the pass-through setting %s", id, name)
			}
		}
		snap.Models = append(snap.Models, m)
	}
	for _, id := range slices.Sorted(maps.Keys(o.Models)) {
		if _, ok := byModel[id]; !ok && !kept[id] {
			warn("overlay: model %s is in no source", id)
		}
	}
	arrange(snap.Models, o, warn)
	return snap
}

// describe fills a model from the overlay first, then from OpenRouter's
// "Maker: Name", then from a hand-written offer. A maker's other name
// becomes the one shown.
func describe(id string, kind Kind, offers []Offer, ov ModelOverlay, makerNames map[string]string) Model {
	m := Model{
		ID:       id,
		Kind:     kind,
		Name:     ov.Name,
		Maker:    ov.Maker,
		Featured: ov.Featured,
		Free:     ov.Free,
		Offers:   offers,
		Traits: Traits{
			Effort:      ov.Effort,
			References:  ov.References,
			SourceVideo: ov.SourceVideo,
			PicksLength: ov.PicksLength,
			Price:       ov.Price,
			Continue:    ov.Continue,
			Faces:       ov.Faces,
			FastOf:      ov.FastOf,
			Deprecated:  ov.Deprecated,
		},
	}
	if ov.Family != nil {
		f := *ov.Family
		m.Traits.Family = &f
	}
	for _, of := range offers {
		if of.Provider != OpenRouter {
			continue
		}
		maker, name, ok := strings.Cut(of.Name, ": ")
		if !ok {
			maker, name = "", of.Name
		}
		if m.Name == "" {
			m.Name = name
		}
		if m.Maker == "" {
			m.Maker = maker
		}
		if m.Description == "" {
			m.Description = plain(of.Description)
		}
	}
	for _, of := range offers {
		if m.Name == "" {
			m.Name = of.Name
		}
		if m.Maker == "" {
			m.Maker = makers[of.Provider]
		}
		if m.Description == "" {
			m.Description = plain(of.Description)
		}
	}
	if shown, ok := makerNames[m.Maker]; ok {
		m.Maker = shown
	}
	return m
}

func hiddenBy(id string, o Overlay) string {
	if why := o.Models[id].Hide; why != "" {
		return why
	}
	for _, r := range o.Hide {
		if ok, _ := path.Match(r.Match, id); ok {
			return r.Why
		}
	}
	return ""
}

// applyOverlay removes the excluded values and adds the values of the
// pass-through settings the offer accepts, on the offer and on each route.
func applyOverlay(offer Offer, ov ModelOverlay) Offer {
	if len(ov.Exclude) == 0 && len(ov.Passthrough) == 0 {
		return offer
	}
	offer.Params = maps.Clone(offer.Params)
	offer.Routes = slices.Clone(offer.Routes)
	for i := range offer.Routes {
		offer.Routes[i].Params = maps.Clone(offer.Routes[i].Params)
	}
	for _, params := range allParams(&offer) {
		for name, drop := range ov.Exclude {
			p, ok := params[name]
			if !ok {
				continue
			}
			if p = p.without(drop); len(p.Values) == 0 {
				delete(params, name)
				continue
			}
			params[name] = p
		}
	}
	for _, name := range slices.Sorted(maps.Keys(ov.Passthrough)) {
		p := ov.Passthrough[name]
		p.Passthrough = true
		accepted := false
		for i := range offer.Routes {
			if offer.Routes[i].accepts(name) {
				accepted = true
				if offer.Routes[i].Params == nil {
					offer.Routes[i].Params = map[string]Param{}
				}
				offer.Routes[i].Params[name] = p
			}
		}
		if !accepted {
			// The model's other offer may take it; Build warns when none does.
			continue
		}
		if offer.Params == nil {
			offer.Params = map[string]Param{}
		}
		offer.Params[name] = p
	}
	return offer
}

func allParams(offer *Offer) []map[string]Param {
	out := []map[string]Param{offer.Params}
	for i := range offer.Routes {
		if offer.Routes[i].Params != nil {
			out = append(out, offer.Routes[i].Params)
		}
	}
	return out
}

// checkEffort warns when no offer has the effort's parameter, or an offer
// lacks one of its values.
func checkEffort(model string, e Effort, offers []Offer, warn func(string, ...any)) {
	found := false
	for _, of := range offers {
		p, ok := of.Params[e.Parameter]
		if !ok {
			continue
		}
		found = true
		if p.Type != Enum {
			warn("model %s: effort parameter %s of %s is a %s, not an enum", model, e.Parameter, of.ID, p.Type)
			continue
		}
		for _, v := range e.Values {
			if !slices.ContainsFunc(p.Values, func(x any) bool { return sameValue(x, v) }) {
				warn("model %s: effort value %s is not offered by %s", model, v, of.ID)
			}
		}
	}
	if !found {
		warn("model %s: no offer has the effort parameter %s", model, e.Parameter)
	}
}

// checkSourceVideo warns when the overlay's source_video and the offers'
// inputs disagree, and when a new model looks like an editor the overlay has
// not marked: one that takes text and video and nothing else.
func checkSourceVideo(model string, s *SourceVideo, offers []Offer, warn func(string, ...any)) {
	takes := func(of Offer) bool { return slices.Contains(of.InputModalities, "video") }
	if s != nil && !slices.ContainsFunc(offers, takes) {
		warn("model %s: source_video is set, but no offer takes a video", model)
	}
	if s != nil && s.Required {
		return
	}
	for _, of := range offers {
		if of.Hidden == "" && takes(of) && !slices.Contains(of.InputModalities, "image") {
			warn("model %s: %s takes only text and video; mark source_video.required", model, of.ID)
			return
		}
	}
}

var (
	markdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	// OpenRouter cuts descriptions short, sometimes inside a link's URL.
	cutLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*$`)
)

// plain drops Markdown links, which point at OpenRouter's own pages.
func plain(s string) string {
	s = markdownLink.ReplaceAllString(s, "$1")
	s = cutLink.ReplaceAllString(s, "$1...")
	return strings.TrimSpace(s)
}

func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// checkPrice warns when the overlay's price hints name what no offer bills or
// offers, so a renamed variant or a new resolution shows up in the log.
func checkPrice(model string, h PriceHints, offers []Offer, warn func(string, ...any)) {
	bills := func(match func(Price) bool) bool {
		for _, of := range offers {
			for _, r := range of.Routes {
				if slices.ContainsFunc(r.Prices, match) {
					return true
				}
			}
		}
		return false
	}
	offered := func(param string, value string) bool {
		return slices.ContainsFunc(offers, func(of Offer) bool {
			p, ok := of.Params[param]
			return ok && slices.ContainsFunc(p.Values, func(v any) bool { return sameValue(v, value) })
		})
	}
	for _, billable := range slices.Sorted(maps.Keys(h.Tokens)) {
		if !bills(func(p Price) bool { return p.Billable == billable && p.Unit == "token" }) {
			warn("model %s: price.tokens.%s, but no offer bills %s in tokens", model, billable, billable)
		}
		for _, res := range slices.Sorted(maps.Keys(h.Tokens[billable])) {
			if res != Any && !offered("resolution", res) {
				warn("model %s: price.tokens.%s.%s, but no offer has that resolution", model, billable, res)
			}
		}
	}
	for _, quality := range slices.Sorted(maps.Keys(h.OpenAITokens)) {
		if !offered("quality", quality) {
			warn("model %s: price.openai_tokens.%s, but no offer has that quality", model, quality)
		}
	}
	for _, variant := range slices.Sorted(maps.Keys(h.Variants)) {
		if !bills(func(p Price) bool { return p.Variant == variant }) {
			warn("model %s: price.variants.%s, but no offer has that variant", model, variant)
		}
	}
	for _, billable := range slices.Sorted(maps.Keys(h.Skip)) {
		if !bills(func(p Price) bool { return p.Billable == billable }) {
			warn("model %s: price.skip.%s, but no offer bills it", model, billable)
		}
	}
}
