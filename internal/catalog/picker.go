// SPDX-License-Identifier: Apache-2.0
package catalog

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// arrange fills what the picker reads from the whole catalog: each family's
// order, the variant it opens on and the outdated ones, the fast modes, and
// the places of the suggestions. Only shown models count, so a hidden one
// neither outdates another nor takes a place.
func arrange(models []Model, o Overlay, warn func(string, ...any)) {
	index := make(map[string]int, len(models))
	for i, m := range models {
		index[m.ID] = i
	}
	tier := make(map[string]int, len(o.Tiers))
	for i, t := range o.Tiers {
		tier[t] = i
	}

	members := map[string][]int{}
	for i := range models {
		f := models[i].Traits.Family
		if f == nil {
			continue
		}
		f.Name = o.Families[f.ID].Name
		if shown(models[i]) {
			members[f.ID] = append(members[f.ID], i)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(o.Families)) {
		if len(members[id]) == 0 {
			warn("overlay: family %s has no shown model", id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(members)) {
		list := members[id]
		fam := func(i int) *Family { return models[i].Traits.Family }
		slices.SortFunc(list, func(a, b int) int {
			fa, fb := fam(a), fam(b)
			return cmp.Or(
				compareVersions(fb.Gen, fa.Gen),
				cmp.Compare(tier[fb.Tier], tier[fa.Tier]),
				strings.Compare(fa.Line, fb.Line),
				strings.Compare(fa.Format, fb.Format),
				strings.Compare(models[a].ID, models[b].ID),
			)
		})
		for rank, i := range list {
			fam(i).Rank = rank
		}
		// A newer generation of the same tier takes a variant's place; a
		// plain variant gives way to the newest one of any tier.
		by := map[int]int{}
		for _, i := range list {
			plain := -1
			for _, j := range list {
				f, g := fam(i), fam(j)
				if g.Line != f.Line || g.Format != f.Format || compareVersions(g.Gen, f.Gen) <= 0 {
					continue
				}
				if g.Tier == f.Tier {
					by[i] = j
					break
				}
				if f.Tier == PlainTier && plain < 0 {
					plain = j
				}
			}
			if _, ok := by[i]; !ok && plain >= 0 {
				by[i] = plain
			}
		}
		for _, i := range list {
			j, ok := by[i]
			// The model that took the place may have given its own up too;
			// generations only grow, so the chain ends.
			for ok {
				fam(i).OutdatedBy = models[j].ID
				j, ok = by[j]
			}
		}
		current := func(i int) bool { return fam(i).OutdatedBy == "" }
		def := -1
		if want := o.Families[id].Default; want != "" {
			if i, ok := index[want]; ok && slices.Contains(list, i) && current(i) {
				def = i
			} else {
				warn("overlay: family %s opens on %s, which is not shown or is outdated", id, want)
			}
		}
		for _, pick := range []func(int) bool{func(i int) bool { return current(i) && models[i].Featured }, current} {
			if def >= 0 {
				break
			}
			if k := slices.IndexFunc(list, pick); k >= 0 {
				def = list[k]
			}
		}
		if def >= 0 {
			fam(def).Default = true
		}
	}

	for i := range models {
		target := models[i].Traits.FastOf
		if target == "" || !shown(models[i]) {
			continue
		}
		j, ok := index[target]
		switch {
		case !ok:
			warn("model %s: fast_of %s, which is in no source", models[i].ID, target)
		case models[j].Kind != models[i].Kind:
			warn("model %s: fast_of %s, which is a %s model", models[i].ID, target, models[j].Kind)
		case !shown(models[j]):
			warn("model %s: fast_of %s, which is hidden", models[i].ID, target)
		case models[j].Traits.Fast != "":
			warn("model %s: fast_of %s, which has the fast mode %s already", models[i].ID, target, models[j].Traits.Fast)
		default:
			models[j].Traits.Fast = models[i].ID
		}
	}

	for _, sit := range slices.Sorted(maps.Keys(o.Recommend)) {
		place := 0
		for _, id := range o.Recommend[sit] {
			i, ok := index[id]
			switch {
			case !ok:
				warn("overlay: recommend %s names %s, which is in no source", sit, id)
			case models[i].Kind != situations[sit]:
				warn("overlay: recommend %s names %s, a %s model", sit, id, models[i].Kind)
			case !shown(models[i]):
				warn("overlay: recommend %s names %s, which is hidden", sit, id)
			default:
				if f := models[i].Traits.Family; f != nil && f.OutdatedBy != "" {
					warn("overlay: recommend %s names %s, which %s has outdated", sit, id, f.OutdatedBy)
				}
				place++
				if models[i].Traits.Recommend == nil {
					models[i].Traits.Recommend = map[string]int{}
				}
				models[i].Traits.Recommend[sit] = place
			}
		}
	}
}

// shown reports whether the picker shows the model: it is not hidden, and
// one of its offers is not.
func shown(m Model) bool {
	return m.Hidden == "" && slices.ContainsFunc(m.Offers, func(of Offer) bool { return of.Hidden == "" })
}

// compareVersions compares versions such as 2.5 and 2.10 part by part, a
// missing part as 0.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}
