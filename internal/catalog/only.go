// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"fmt"
	"maps"
	"slices"
)

// checkOnly holds a setting's Only to the offer it is in: every setting it
// names is an enum of the offer, with the values listed among its own, and
// none of them goes only with another.
func checkOnly(name string, p Param, params map[string]Param) error {
	for other, values := range p.Only {
		q, ok := params[other]
		switch {
		case other == name:
			return fmt.Errorf("parameter %s: only names itself", name)
		case !ok:
			return fmt.Errorf("parameter %s: only names %s, which the model does not list", name, other)
		case q.Type != Enum:
			return fmt.Errorf("parameter %s: only names %s, which is not an enum", name, other)
		case len(q.Only) > 0:
			return fmt.Errorf("parameter %s: only names %s, which goes only with another setting itself", name, other)
		case len(values) == 0:
			return fmt.Errorf("parameter %s: only lists no value of %s", name, other)
		}
		for _, v := range values {
			if !slices.ContainsFunc(q.Values, func(x any) bool { return sameValue(x, v) }) {
				return fmt.Errorf("parameter %s: only lists %v, which is not a value of %s", name, v, other)
			}
		}
	}
	return nil
}

// Applies tells whether the offer's setting name goes with these settings:
// the offer lists it, and every setting its Only names has one of the values
// listed, as set, else as its default. frame_count goes with
// output_format gif, never with a PNG.
func Applies(of Offer, name string, settings map[string]any) bool {
	p, ok := of.Params[name]
	if !ok {
		return false
	}
	for other, values := range p.Only {
		v, set := settings[other]
		if !set || v == nil {
			v, set = defaultOf(of.Kind, other, of.Params[other], 0)
		}
		if !set || !slices.ContainsFunc(values, func(x any) bool { return sameValue(x, v) }) {
			return false
		}
	}
	return true
}

// Fill completes the settings of a run on a person's key, in place: every
// setting it left out takes its default (Defaults), a setting that goes only
// with another (Param.Only) takes its default once that one has a value it
// goes with, and is dropped while it has not. A GIF gets OpenAI's 9 frames
// of 200 ms when none were chosen, and a PNG goes without them, so what is
// priced is what is sent.
func Fill(of Offer, settings map[string]any, shape float64) {
	for name, v := range defaults(of, shape, settings) {
		if cur, set := settings[name]; !set || cur == nil {
			settings[name] = v
		}
	}
	for _, name := range slices.Sorted(maps.Keys(settings)) {
		if p, ok := of.Params[name]; ok && len(p.Only) > 0 && !Applies(of, name, settings) {
			delete(settings, name)
		}
	}
}
