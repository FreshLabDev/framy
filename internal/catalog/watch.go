// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Listed is one entry of a direct provider's own list of models, as far as
// framy reads it: OpenAI's GET /v1/models gives an id and, for a model on its
// way out, a shutdown_date. ShutsDown is empty while none is announced.
type Listed struct {
	ID        string
	ShutsDown string
}

// watched is what an id of OpenAI's list has to look like for framy to say
// anything of it: an image model, in a plain id. The list also holds chat
// models, fine-tunes and whatever else the account has, none of them framy's.
var watched = regexp.MustCompile(`^gpt-image-[a-z0-9][a-z0-9._-]{0,60}$`)

// datedSnapshot is the day OpenAI appends to a snapshot of a model.
var datedSnapshot = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// ListedNotes compares a provider's own list of models with what catalog/
// knows and returns what the operator should hear, one plain sentence each,
// sorted: it replaces reading the provider's deprecation pages
// by hand. Only OpenAI has such a list with shutdown days; for any other
// provider it returns nothing. It says:
//
//   - an image model OpenAI lists that framy neither describes nor hides, a
//     new GPT Image that has to be written into catalog/openai.yaml before it
//     can be offered (a model whose shutdown day has passed is not new);
//   - a described model whose shutdown day OpenAI gives and the overlay does
//     not, or gives differently: deprecated.shuts_down there is what makes
//     the picker stop offering the model HideExpiringWithinDays before it.
//
// A model the overlay hides on purpose, such as the closing GPT Image 1
// line, and a dated snapshot of a known model are not mentioned: they would
// be told again by every check.
func (f Files) ListedNotes(provider string, listed []Listed, now time.Time) []string {
	if provider != OpenAI {
		return nil
	}
	described := map[string]Offer{}
	for _, of := range f.Offers {
		if of.Provider == provider {
			described[of.ProviderModelID] = of
		}
	}
	today := day(now)
	var notes []string
	for _, m := range listed {
		if !watched.MatchString(m.ID) {
			continue
		}
		shuts, err := time.Parse(time.DateOnly, m.ShutsDown)
		if err != nil {
			m.ShutsDown = ""
		}
		of, known := described[m.ID]
		if !known {
			base := datedSnapshot.ReplaceAllString(m.ID, "")
			_, snapshot := described[base]
			snapshot = snapshot && base != m.ID
			hidden := hiddenBy(provider+"/"+m.ID, f.Overlay) != "" || hiddenBy(provider+"/"+base, f.Overlay) != ""
			gone := m.ShutsDown != "" && shuts.Before(today)
			if !snapshot && !hidden && !gone {
				notes = append(notes, fmt.Sprintf("OpenAI lists %s, which catalog/openai.yaml does not describe and catalog/overlay.yaml does not hide. A new GPT Image has to be written there before framy can offer it.", m.ID))
			}
			continue
		}
		if m.ShutsDown == "" {
			continue
		}
		model := f.Overlay.modelOf(of)
		have := ""
		if d := f.Overlay.Models[model].Deprecated; d != nil {
			have = d.ShutsDown
		}
		switch {
		case have == m.ShutsDown:
		case have == "":
			notes = append(notes, fmt.Sprintf("OpenAI says %s shuts down on %s, and catalog/overlay.yaml has no shutdown day for %s. Set deprecated.shuts_down there: the picker then stops offering the model %d days before.",
				m.ID, m.ShutsDown, model, f.Overlay.HideExpiringWithinDays))
		default:
			notes = append(notes, fmt.Sprintf("OpenAI says %s shuts down on %s, and catalog/overlay.yaml has %s for %s. Correct deprecated.shuts_down there.",
				m.ID, m.ShutsDown, have, model))
		}
	}
	slices.Sort(notes)
	return slices.Compact(notes)
}
