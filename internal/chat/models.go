// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strconv"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/price"
)

// The list of models, as the site's picker shows it (docs/design.md, Model
// picker) cut down to buttons: one row per family with what it costs, the
// families framy runs for free first, then the site's suggestions for the
// run, then the rest by name. A family of several variants opens on them;
// the families the person has no key for fold into one row at the end.

// family is a row of the list: the variants of one family the run can
// take, or a model of no family alone.
type family struct {
	id, name string
	list     []priced
}

// priced is a choice with what it would cost the person.
type priced struct {
	choice
	estimate price.Estimate
}

func (c priced) runnable() bool { return c.free || c.key }

// famHash names a family in a button's data, as modelHash a model.
func famHash(id string) string { return modelHash("family:" + id) }

// familyOf is the family a model is in, by id and name; a model of none is
// its own.
func familyOf(m catalog.Model) (id, name string) {
	if fam := m.Traits.Family; fam != nil && fam.ID != "" {
		return fam.ID, cmp.Or(fam.Name, m.Name)
	}
	return "model:" + m.ID, m.Name
}

// title is the row's text before the price: the model's own name when the
// family has one variant here, else the family's.
func (fm family) title() string {
	if len(fm.list) == 1 {
		return fm.list[0].model.Name
	}
	return fm.name
}

func (fm family) free() bool {
	return slices.ContainsFunc(fm.list, func(c priced) bool { return c.free })
}

func (fm family) runnable() bool { return slices.ContainsFunc(fm.list, priced.runnable) }

func (fm family) has(modelID string) bool {
	return slices.ContainsFunc(fm.list, func(c priced) bool { return c.model.ID == modelID })
}

// situation is the key of the site's suggestions that fits the run.
func situation(taken int) string {
	if taken > 5 {
		return "many_photos"
	}
	return "image"
}

// families groups the choices into rows in the list's order, each priced
// for the run as it is.
func (f *Flow) families(run db.ChatRun, s state, list []choice) []family {
	byID := map[string]int{}
	var out []family
	for _, c := range list {
		id, name := familyOf(c.model)
		i, ok := byID[id]
		if !ok {
			i = len(out)
			byID[id] = i
			out = append(out, family{id: id, name: name})
		}
		out[i].list = append(out[i].list, priced{choice: c, estimate: f.priceOf(run, s, c)})
	}
	key := situation(run.Taken)
	suggested := func(fm family) int {
		best := math.MaxInt
		for _, c := range fm.list {
			if r, ok := c.model.Traits.Recommend[key]; ok && r > 0 {
				best = min(best, r)
			}
		}
		return best
	}
	rank := func(c priced) int {
		if fam := c.model.Traits.Family; fam != nil {
			return fam.Rank
		}
		return 0
	}
	for i := range out {
		slices.SortStableFunc(out[i].list, func(a, b priced) int {
			return cmp.Or(cmp.Compare(boolRank(!a.runnable()), boolRank(!b.runnable())), cmp.Compare(rank(a), rank(b)))
		})
	}
	slices.SortStableFunc(out, func(a, b family) int {
		return cmp.Or(
			cmp.Compare(boolRank(!a.free()), boolRank(!b.free())),
			cmp.Compare(suggested(a), suggested(b)),
			cmp.Compare(a.title(), b.title()),
		)
	})
	return out
}

// priceOf is what a choice would cost the run: nothing on the free tier,
// else as pickModel would revise the draft to it.
func (f *Flow) priceOf(run db.ChatRun, s state, c choice) price.Estimate {
	settings := carried(s.settings, c.offer)
	if c.free {
		plan, _ := free.PlanOf(c.offer.ID)
		settings = plan.SettingsFor(run.Taken)
	}
	return price.Of(c.model, c.offer, requestOf(run.Prompt, settings, s.inputs))
}

// modelKeyboard is the list of models, a family's variants or the families
// that need a key, as v says.
func (f *Flow) modelKeyboard(ctx context.Context, run db.ChatRun, s state, v view) (*tg.InlineKeyboardMarkup, error) {
	list, err := f.choices(ctx, run, s.offer)
	if err != nil {
		return nil, err
	}
	fams := f.families(run, s, list)
	lang := run.Lang
	row := func(b tg.InlineKeyboardButton) []tg.InlineKeyboardButton { return []tg.InlineKeyboardButton{b} }
	var rows [][]tg.InlineKeyboardButton
	back := button(t(lang, "btn.back"), callback(run.JobID, actModel), "")

	// famRow opens a family of several variants and picks one of one.
	famRow := func(fm family) []tg.InlineKeyboardButton {
		text := fm.title()
		if p := famPrice(lang, fm, s.quote); p != "" {
			text += " · " + p
		}
		switch {
		case fm.has(s.model.ID):
			text = "✓ " + text
		case !fm.runnable():
			text = "🔑 " + text
		}
		data := callback(run.JobID, actFamily+famHash(fm.id))
		if len(fm.list) == 1 {
			data = callback(run.JobID, actPickModel+modelHash(fm.list[0].model.ID))
		}
		return row(button(text, data, ""))
	}

	switch v.screen {
	case screenFamily:
		i := slices.IndexFunc(fams, func(fm family) bool { return famHash(fm.id) == v.fam })
		if i < 0 {
			// The catalog changed since the list was drawn.
			return f.modelKeyboard(ctx, run, s, viewModel)
		}
		for _, c := range fams[i].list {
			text := variantLabel(c.model)
			if p := choicePrice(lang, c, s.quote); p != "" {
				text += " · " + p
			}
			switch {
			case c.model.ID == s.model.ID:
				text = "✓ " + text
			case !c.runnable():
				text = "🔑 " + text
			}
			rows = append(rows, row(button(text, callback(run.JobID, actPickModel+modelHash(c.model.ID)), "")))
		}
		if !fams[i].runnable() {
			back = button(t(lang, "btn.back"), callback(run.JobID, actLocked), "")
		}
		return &tg.InlineKeyboardMarkup{InlineKeyboard: append(rows, row(back))}, nil
	case screenLocked:
		for _, fm := range fams {
			if !fm.runnable() {
				rows = append(rows, famRow(fm))
			}
		}
		return &tg.InlineKeyboardMarkup{InlineKeyboard: append(rows, row(back))}, nil
	}
	locked := 0
	for _, fm := range fams {
		if !fm.runnable() && !fm.has(s.model.ID) {
			locked++
			continue
		}
		rows = append(rows, famRow(fm))
	}
	if locked > 0 {
		rows = append(rows, row(button("🔑 "+t(lang, "panel.more_key", "n", strconv.Itoa(locked)), callback(run.JobID, actLocked), "")))
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: append(rows, row(button(t(lang, "btn.back"), callback(run.JobID, actBack), "")))}, nil
}

// variantLabel is a variant's name inside its family's screen: "Pro" for
// Nano Banana Pro.
func variantLabel(m catalog.Model) string {
	if fam := m.Traits.Family; fam != nil && fam.Label != "" {
		return fam.Label
	}
	return m.Name
}

// choicePrice is what one variant costs on a button: "free", or the price
// in the person's currency, else in dollars. An unknown price is empty.
func choicePrice(lang string, c priced, q fx.Quote) string {
	if c.free {
		return t(lang, "card.free")
	}
	usd, local, _ := cardPrice(lang, c.estimate, q)
	return cmp.Or(local, usd)
}

// famPrice is what a family's row says it costs: "free" when a variant
// runs on the free tier, the one variant's price, or "from" the lowest.
func famPrice(lang string, fm family, q fx.Quote) string {
	if fm.free() {
		return t(lang, "card.free")
	}
	if len(fm.list) == 1 {
		return choicePrice(lang, fm.list[0], q)
	}
	low := math.Inf(1)
	for _, c := range fm.list {
		if c.estimate.Kind != price.Unknown {
			low = min(low, c.estimate.USD)
		}
	}
	if math.IsInf(low, 1) {
		return ""
	}
	amount := fx.Format(low, fx.USD, lang)
	if q.Code != "" && q.Code != fx.USD && q.PerUSD > 0 {
		amount = fx.Format(low*q.PerUSD, q.Code, lang)
	}
	return t(lang, "run.from", "price", amount)
}
