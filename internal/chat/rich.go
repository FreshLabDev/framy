// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/anim"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/price"
)

// The preview as a rich message (docs/design.md, `/img` and `/edit` in a
// group, step 4): the card on top, the prompt under it, and under them the
// model and the settings, each folded into a <details> that opens in place
// for whoever taps it. The list is the site's picker in its default order
// (internal/web/app/src/catalog.js and picker.js): makers A to Z, a maker's
// families in the catalog's order, a family's variants strongest first. A
// model the run cannot take keeps its place, disabled, with the reason. The
// buttons carry the actions a keyboard's did, and only the requester's
// presses act.

// cardID names the card in the rich message's media.
const cardID = "card"

// entry is a model as the list shows it: the choice it would run as, with
// the settings and the price it would get, or why the run cannot take it.
type entry struct {
	choice
	// name is the model's name as the site's picker writes it.
	name     string
	settings map[string]any
	estimate price.Estimate
	// why is the reason the run cannot take the model, in the run's
	// language; empty when it can. locked: the reason is a key the person
	// has not connected.
	why    string
	locked bool
}

// listing is every model of the run's kind that the site lists, by maker.
type listing struct {
	groups []makerGroup
	// current and currentID are the name and the id of the run's model.
	current, currentID string
	// quote is the currency the prices are in.
	quote fx.Quote
}

type makerGroup struct {
	maker string
	picks []entry
}

// find is the model a button's hash names.
func (l listing) find(hash string) (entry, bool) {
	for _, g := range l.groups {
		for _, p := range g.picks {
			if modelHash(p.model.ID) == hash {
				return p, true
			}
		}
	}
	return entry{}, false
}

// listed tells whether the site's picker lists m among the models of kind:
// not a fast mode of a model in the catalog (that is a switch of the model),
// not one a model in the catalog has outdated, and not a vector one, which
// Telegram shows as a file rather than a picture.
func listed(m catalog.Model, kind catalog.Kind, byID map[string]bool) bool {
	if m.Kind != kind || vector(m) || len(m.Offers) == 0 {
		return false
	}
	if m.Traits.FastOf != "" && byID[m.Traits.FastOf] {
		return false
	}
	fam := m.Traits.Family
	return fam == nil || fam.OutdatedBy == "" || !byID[fam.OutdatedBy]
}

// familyKey is what the site groups a model's variants by.
func familyKey(m catalog.Model) string {
	if fam := m.Traits.Family; fam != nil && fam.ID != "" {
		return fam.ID
	}
	return m.ID
}

func familyRank(m catalog.Model) int {
	if fam := m.Traits.Family; fam != nil {
		return fam.Rank
	}
	return 0
}

// listing lists the models of the run's kind for the rich preview. The
// catalog comes in the order GET /api/catalog lists it, which is the order
// of the site's families. An edit of a clip keeps its model, so it lists
// none.
func (f *Flow) listing(ctx context.Context, user int64, lang, prompt string, d draft) (listing, error) {
	if d.clip {
		return listing{}, nil
	}
	models, err := f.cfg.Store.Catalog(ctx)
	if err != nil {
		return listing{}, err
	}
	have, err := f.keyProviders(ctx, user)
	if err != nil {
		return listing{}, err
	}
	byID := map[string]bool{}
	for _, m := range models {
		byID[m.ID] = true
	}
	// A family's place is its first model's, and its name goes before the
	// variant's label when it has several, as on the site.
	order, members := map[string]int{}, map[string]int{}
	for i, m := range models {
		if m.Traits.FastOf != "" && byID[m.Traits.FastOf] {
			continue
		}
		k := familyKey(m)
		if _, ok := order[k]; !ok {
			order[k] = i
		}
		members[k]++
	}
	name := func(m catalog.Model) string {
		if fam := m.Traits.Family; fam != nil && fam.Name != "" && members[familyKey(m)] > 1 {
			return fam.Name + " " + cmp.Or(fam.Label, m.Name)
		}
		return m.Name
	}
	providers := append([]string{d.offer.Provider}, providerOrder...)
	l := listing{current: name(d.model), currentID: d.model.ID, quote: d.quote}
	byMaker := map[string]int{}
	for _, m := range models {
		if !listed(m, d.offer.Kind, byID) {
			continue
		}
		p := pickOf(lang, prompt, d, m, have, providers)
		p.name = name(m)
		i, ok := byMaker[m.Maker]
		if !ok {
			i = len(l.groups)
			byMaker[m.Maker] = i
			l.groups = append(l.groups, makerGroup{maker: m.Maker})
		}
		l.groups[i].picks = append(l.groups[i].picks, p)
	}
	slices.SortStableFunc(l.groups, func(a, b makerGroup) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.maker), strings.ToLower(b.maker)), cmp.Compare(a.maker, b.maker))
	})
	for _, g := range l.groups {
		slices.SortStableFunc(g.picks, func(a, b entry) int {
			return cmp.Or(cmp.Compare(order[familyKey(a.model)], order[familyKey(b.model)]), cmp.Compare(familyRank(a.model), familyRank(b.model)))
		})
	}
	return l, nil
}

// pickOf is how the run would go on m: the run as it is for its own model;
// on the free tier when m is free and its plan takes the run's photos; else
// on the person's key, the offers ranked by providers. The settings that go
// to another model are framy's defaults with what the run set and the model
// takes, and a video's photo is its first frame where the model has frames.
func pickOf(lang, prompt string, d draft, m catalog.Model, have map[string]bool, providers []string) entry {
	if m.ID == d.model.ID {
		return entry{
			choice:   choice{model: m, offer: d.offer, free: d.pay == payFree, key: d.pay != payFree},
			settings: d.settings, estimate: d.estimate,
		}
	}
	if m.Free {
		for _, o := range m.Offers {
			plan, ok := free.PlanOf(o.ID)
			if ok && d.taken >= plan.MinInputs && d.taken <= plan.MaxInputs && misfit(lang, m, o, d.taken) == "" {
				s := plan.SettingsFor(d.taken)
				return entry{
					choice:   choice{model: m, offer: o, free: true, key: have[o.Provider]},
					settings: s, estimate: price.Of(m, o, priceRequest(prompt, s, d.images, nil)),
				}
			}
		}
	}
	offers := slices.Clone(m.Offers)
	slices.SortStableFunc(offers, func(a, b catalog.Offer) int {
		return cmp.Compare(providerRank(providers, a.Provider), providerRank(providers, b.Provider))
	})
	var fit []catalog.Offer
	why := ""
	for _, o := range offers {
		w := misfit(lang, m, o, d.taken)
		if w == "" {
			fit = append(fit, o)
		} else if why == "" {
			why = w
		}
	}
	if len(fit) == 0 {
		return entry{choice: choice{model: m, offer: offers[0]}, why: why}
	}
	for _, o := range fit {
		if have[o.Provider] {
			s := keySettings(m, o, d.settings, d.taken, d.images)
			return entry{
				choice:   choice{model: m, offer: o, key: true},
				settings: s, estimate: price.Of(m, o, priceRequest(prompt, s, d.images, nil)),
			}
		}
	}
	return entry{
		choice: choice{model: m, offer: fit[0]},
		why:    t(lang, "run.need_key", "provider", providerName(fit[0].Provider)), locked: true,
	}
}

func providerRank(order []string, provider string) int {
	if i := slices.Index(order, provider); i >= 0 {
		return i
	}
	return len(order)
}

func providerName(provider string) string { return cmp.Or(providerNames[provider], provider) }

// misfit is why an offer cannot take a run of taken photos, as the site's
// picker words it; empty when it can.
func misfit(lang string, m catalog.Model, o catalog.Offer, taken int) string {
	in := catalog.InputsOf(m, o)
	switch {
	case in.Videos != nil && in.Videos.Min >= 1:
		return t(lang, "web.misfit.needs_clip")
	case taken == 0 && in.Images != nil && in.Images.Min >= 1:
		return t(lang, "web.misfit.needs_photo")
	}
	limit := 0
	if in.Images != nil {
		limit = in.Images.Max
	}
	switch {
	case taken <= limit:
		return ""
	case limit > 0:
		return t(lang, "web.misfit.max_photos", "n", strconv.Itoa(limit))
	}
	return t(lang, "web.misfit.no_photos")
}

// keySettings are the settings a run takes to another model on the
// person's key: framy's defaults for the shape of the first photo, what
// the run set that the model takes, and for a video from photos, the
// photos as its first frames where the model has frames.
func keySettings(m catalog.Model, o catalog.Offer, current map[string]any, taken int, images []price.Image) map[string]any {
	shape := 0.0
	if len(images) > 0 && images[0].Width > 0 && images[0].Height > 0 {
		shape = float64(images[0].Width) / float64(images[0].Height)
	}
	s := catalog.Defaults(o, shape)
	for k, v := range carried(current, o) {
		s[k] = v
	}
	if m.Kind == catalog.Video && taken > 0 {
		var types []string
		for _, v := range o.Params[framesSetting].Values {
			if name, ok := v.(string); ok && len(types) < taken {
				types = append(types, name)
			}
		}
		if len(types) > 0 {
			s[framesSetting] = types
		}
	}
	return s
}

// framesSetting names which of a video's first pictures are frames.
const framesSetting = "frame_images"

// drawnSettings are the settings the rich preview offers, in its order: the
// ones the site's composer shows first. A size goes only where the model has
// neither an aspect ratio nor a resolution, and never on a video, as
// catalog.Defaults decides. n is how many results, up to maxResults.
var drawnSettings = []string{"aspect_ratio", "resolution", "quality", "background", "n", "duration", "generate_audio", "size"}

// settingsOf are the settings of an offer the rich preview offers, with
// their values.
func settingsOf(o catalog.Offer) []setting {
	_, ratio := o.Params["aspect_ratio"]
	_, res := o.Params["resolution"]
	var out []setting
	for _, name := range drawnSettings {
		p, ok := o.Params[name]
		if !ok || p.Passthrough || (name == "size" && (ratio || res || o.Kind == catalog.Video)) {
			continue
		}
		var values []any
		switch p.Type {
		case catalog.Enum:
			values = p.Values
		case catalog.Range:
			values = results(p)
		case catalog.Boolean:
			if name == "generate_audio" {
				values = []any{false, true}
			}
		}
		if name == "duration" {
			values = someLengths(values)
		}
		if len(values) > 1 {
			out = append(out, setting{name: name, values: values})
		}
	}
	return out
}

// results are the counts of results n offers, up to maxResults; none for
// another range.
func results(p catalog.Param) []any {
	if p.Max == nil {
		return nil
	}
	lo := 1
	if p.Min != nil && *p.Min > 1 {
		lo = int(*p.Min)
	}
	var out []any
	for v := lo; v <= min(int(*p.Max), maxResults); v++ {
		out = append(out, v)
	}
	return out
}

// someLengths keeps a long list of lengths to the shortest, the longest and
// the round ones, so it fits a row or two.
func someLengths(values []any) []any {
	if len(values) <= 5 {
		return values
	}
	num := func(v any) float64 {
		switch x := v.(type) {
		case float64:
			return x
		case int:
			return float64(x)
		}
		return 0
	}
	lo, hi := num(values[0]), num(values[len(values)-1])
	var out []any
	for _, v := range values {
		switch n := num(v); n {
		case lo, hi, 5, 10, 15, 20, 30, 60:
			out = append(out, v)
		}
	}
	return out
}

// valueWords are the values the site writes as words.
var valueWords = map[string]bool{
	"auto": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true, "minimal": true,
	"transparent": true, "opaque": true,
}

// valueLabel is a setting's value on its button, in the site's words.
func valueLabel(lang, name string, v any) string {
	switch name {
	case "duration":
		return t(lang, "web.unit.s", "n", fmt.Sprint(v))
	case "n":
		return "×" + fmt.Sprint(v)
	case "generate_audio":
		if b, _ := v.(bool); b {
			return t(lang, "web.val.with_sound")
		}
		return t(lang, "web.val.no_sound")
	}
	s := fmt.Sprint(v)
	if valueWords[s] {
		return t(lang, "web.val."+s)
	}
	return s
}

// richQuote is the prompt as the rich preview quotes it, folded when long.
// A rich message reads its HTML as a page does, so a line break is a <br>.
func richQuote(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if utf8.RuneCountInString(prompt) > captionPrompt {
		prompt = strings.TrimSpace(string([]rune(prompt)[:captionPrompt])) + "…"
	}
	open := "<blockquote>"
	if utf8.RuneCountInString(prompt) > expandAfter || strings.Count(prompt, "\n") > 2 {
		open = "<blockquote expandable>"
	}
	return open + strings.ReplaceAll(html.EscapeString(prompt), "\n", "<br>") + "</blockquote>"
}

// unfold is the section of the preview that comes open: the settings after
// one was picked, the models after Another model on an error card.
type unfold int

const (
	foldAll unfold = iota
	openSettings
	openModels
)

// richPreview is the preview's rich HTML: the card, the prompt, the note
// on what was taken, the price where the card could not draw it, and the
// model and the settings, folded but for the one open names.
func richPreview(lang, prompt string, jobID int64, d draft, l listing, labelDrawn bool, open unfold) string {
	var b strings.Builder
	b.WriteString(`<img src="tg://photo?id=` + cardID + `"/>`)
	b.WriteString(richQuote(prompt))
	if note := d.noteText(lang); note != "" {
		b.WriteString("<p>" + note + "</p>")
	}
	if !labelDrawn {
		b.WriteString("<p>" + priceLine(lang, d.pay, d.offer.Provider, d.usage, d.estimate, d.quote) + "</p>")
	}
	b.WriteString(richModels(lang, jobID, l, open == openModels))
	b.WriteString(richSettings(lang, jobID, d, open == openSettings))
	return b.String()
}

// richButton is a button inside the rich message; primary marks the
// current choice.
func richButton(text, data string, primary bool) string {
	style := ""
	if primary {
		style = ` style="primary"`
	}
	return `<tg-button type="callback_data"` + style + ` data="` + html.EscapeString(data) + `">` + html.EscapeString(text) + `</tg-button>`
}

func richRows(buttons []string, perRow int) string {
	var b strings.Builder
	for row := range slices.Chunk(buttons, perRow) {
		b.WriteString("<tg-button-row>" + strings.Join(row, "") + "</tg-button-row>")
	}
	return b.String()
}

// summary is a <details>' title: the label, and the value in bold.
func summary(label, value string) string {
	if value == "" {
		return "<summary>" + html.EscapeString(label) + "</summary>"
	}
	return "<summary>" + html.EscapeString(label) + ": <b>" + html.EscapeString(value) + "</b></summary>"
}

// richModels is the list of models: each maker under its name, each model
// a button of its own with what it costs the run, or why the run cannot
// take it.
func richModels(lang string, jobID int64, l listing, open bool) string {
	if len(l.groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(details(open) + summary(t(lang, "run.model"), l.current))
	for _, g := range l.groups {
		var buttons []string
		for _, p := range g.picks {
			text := p.name
			if label := l.pickLabel(lang, p); label != "" {
				text += " · " + label
			}
			if p.why != "" {
				buttons = append(buttons, `<tg-button type="disabled">`+html.EscapeString(text)+`</tg-button>`)
				continue
			}
			buttons = append(buttons, richButton(text, callback(jobID, actPickModel+modelHash(p.model.ID)), p.model.ID == l.currentID))
		}
		b.WriteString("<p><b>" + html.EscapeString(g.maker) + "</b></p>" + richRows(buttons, 1))
	}
	b.WriteString("</details>")
	return b.String()
}

// pickLabel is what a model's button says after its name: why the run
// cannot take it, "free", or the price in the person's currency.
func (l listing) pickLabel(lang string, p entry) string {
	switch {
	case p.why != "":
		return p.why
	case p.free:
		return t(lang, "card.free")
	}
	usd, local, _ := cardPrice(lang, p.estimate, l.quote)
	return cmp.Or(local, usd)
}

// labelled are the settings that read as words, which a caption names:
// "Low, Medium, High" alone does not say of what.
func labelled(name string) bool { return name == "quality" || name == "background" }

// richSettings are the run's settings, each a row of its values with the
// current one marked, and in the title the values the run has. A free run
// has its settings fixed, and an edit of a clip none to change.
func richSettings(lang string, jobID int64, d draft, open bool) string {
	if d.pay == payFree || d.clip {
		return ""
	}
	sets := settingsOf(d.offer)
	if len(sets) == 0 {
		return ""
	}
	var (
		rows strings.Builder
		now  []string
	)
	for _, set := range sets {
		cur, has := d.settings[set.name]
		if has {
			now = append(now, valueLabel(lang, set.name, cur))
		}
		var buttons []string
		for vi, v := range set.values {
			data := callback(jobID, actPickSetting+set.name+"."+strconv.Itoa(vi))
			buttons = append(buttons, richButton(valueLabel(lang, set.name, v), data, has && sameSetting(cur, v)))
		}
		per := 5
		if labelled(set.name) {
			rows.WriteString("<p>" + html.EscapeString(t(lang, "web.set."+set.name)) + "</p>")
			per = 3
		}
		rows.WriteString(richRows(buttons, per))
	}
	return details(open) + summary(t(lang, "run.settings"), strings.Join(now, " · ")) + rows.String() + "</details>"
}

// details opens a section of the preview, unfolded when open is true.
func details(open bool) string {
	if open {
		return "<details open>"
	}
	return "<details>"
}

// richKeyboard: Generate, with what the run costs on the person's key, and
// No; under them what the run turns into: a video of its prompt and photo,
// an image again, or the other length of its clip. The model and the
// settings are in the message.
func richKeyboard(lang string, jobID int64, d draft) *tg.InlineKeyboardMarkup {
	gen := t(lang, "run.generate")
	if d.pay != payFree {
		usd, local, _ := cardPrice(lang, d.estimate, d.quote)
		if p := cmp.Or(local, usd); p != "" {
			gen += " · " + p
		}
	}
	rows := [][]tg.InlineKeyboardButton{{
		button(gen, callback(jobID, actGenerate), "success"),
		button(t(lang, "run.no"), callback(jobID, actNo), "danger"),
	}}
	var more tg.InlineKeyboardButton
	switch {
	case d.clip:
		c, ok := d.otherLength()
		if !ok {
			return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
		}
		more = button(c.label(lang, d), callback(jobID, c.action), "")
	case d.video():
		more = button(t(lang, "run.as_image"), callback(jobID, actImage), "")
	case d.taken <= free.VideoHeyGen.MaxInputs:
		more = button(t(lang, "run.as_video"), callback(jobID, actVideo), "")
	default:
		return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: append(rows, []tg.InlineKeyboardButton{more})}
}

// withCard opens the drawn card and hands it over as the rich message's
// picture, uploaded: a card is drawn for one run, so Telegram has no
// file_id of it to reuse.
func withCard(pic anim.Media, send func([]tg.RichMedia) error) error {
	file, err := os.Open(pic.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	name := "framy.png"
	if pic.ContentType == "image/jpeg" {
		name = "framy.jpg"
	}
	return send([]tg.RichMedia{{ID: cardID, Media: tg.InputMediaPhoto{Media: tg.InputFileUpload{Filename: name, Data: file}}}})
}

// sendRich sends the rich preview with the card.
func (f *Flow) sendRich(ctx context.Context, chatID int64, body string, pic anim.Media, opts tg.RichOptions) (tg.Message, error) {
	var sent tg.Message
	err := withCard(pic, func(media []tg.RichMedia) error {
		opts.Media = media
		var err error
		sent, err = f.cfg.Telegram.SendRich(ctx, chatID, body, opts)
		return err
	})
	return sent, err
}

// richState is the preview of s as a rich message: its HTML with the card
// drawn in dir from the pictures at paths, and its keyboard.
func (f *Flow) richState(ctx context.Context, user int64, lang, prompt string, jobID int64, d draft, dir string, paths []string, open unfold) (body string, pic anim.Media, kb *tg.InlineKeyboardMarkup, err error) {
	l, err := f.listing(ctx, user, lang, prompt, d)
	if err != nil {
		return "", anim.Media{}, nil, err
	}
	pic, err = f.cfg.Renderer.Preview(ctx, dir, previewCard(lang, d), paths)
	if err != nil {
		return "", anim.Media{}, nil, fmt.Errorf("draw the preview: %w", err)
	}
	return richPreview(lang, prompt, jobID, d, l, pic.TextDrawn, open), pic, richKeyboard(lang, jobID, d), nil
}

// drawRich draws the card of s again and puts the rich preview in place of
// the one the run has, in the chat or the requester's own.
func (f *Flow) drawRich(ctx context.Context, run db.ChatRun, v view, s state, dir string, paths []string) error {
	open := foldAll
	if v == viewSettings {
		open = openSettings
	}
	body, pic, kb, err := f.richState(ctx, run.UserID, run.Lang, run.Prompt, run.JobID, s.draft, dir, paths, open)
	if err != nil {
		return err
	}
	err = withCard(pic, func(media []tg.RichMedia) error {
		if run.PreviewEphemeral {
			return f.cfg.Telegram.EditEphemeralMessageRich(ctx, run.ChatID, run.UserID, run.PreviewMessageID, body, media, kb)
		}
		return f.cfg.Telegram.EditMessageRich(ctx, run.ChatID, run.PreviewMessageID, body, media, kb)
	})
	if err != nil {
		return fmt.Errorf("preview not redrawn: %w", err)
	}
	return nil
}
