// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/anim"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/storage"
)

// The Model and Settings buttons (docs/design.md, `/img` and `/edit` in a
// group, step 4). In a group the requester gets an ephemeral panel in place
// of the preview, so the chat sees neither the list nor the changes until the
// card changes; a preview only they see, or one in the bot's DM, shows the
// list itself. A pick revises the draft and draws the card again.

// view is what a run's keyboard shows: the main buttons, the list of
// models, one family's variants, the models that need a key, or the
// settings.
type view struct {
	screen screen
	// fam names the family screenFamily lists, as famHash writes it.
	fam string
}

type screen int

const (
	screenMain screen = iota
	screenModels
	screenFamily
	screenLocked
	screenSettings
)

var (
	viewMain     = view{}
	viewModel    = view{screen: screenModels}
	viewLocked   = view{screen: screenLocked}
	viewSettings = view{screen: screenSettings}
)

// viewFamily lists the variants of the family hash names.
func viewFamily(hash string) view { return view{screen: screenFamily, fam: hash} }

// shownSettings are the settings the panel offers, in its order; the rest
// stay as the model has them. n is how many results, up to maxResults.
var shownSettings = []string{"aspect_ratio", "resolution", "quality", "size", "n"}

const (
	maxResults = 4
	// perRow is how many values of a setting go in a row.
	perRow = 4
)

// providerOrder is which offer of a model runs when the person has keys
// for several: the one the run is on, then OpenRouter, then the direct ones.
var providerOrder = []string{catalog.OpenRouter, catalog.OpenAI, catalog.Gemini}

// choice is a model the panel lists, with the offer it would run on.
type choice struct {
	model catalog.Model
	offer catalog.Offer
	// free: framy's key runs it on the free tier; key: the person's does.
	free, key bool
}

// modelHash names a model in a button's data, which is at most 64 bytes.
func modelHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:4])
}

// state is a run's draft as the card shows it, read back from the job.
type state struct {
	draft
	inputs []storage.Asset
}

// stateOf reads what a run's card shows: the job's model, settings and
// price, and the files it takes.
func (f *Flow) stateOf(ctx context.Context, run db.ChatRun) (state, error) {
	model, offer, err := f.cfg.Store.Offer(ctx, run.OfferID)
	if err != nil {
		return state{}, fmt.Errorf("offer %s: %w", run.OfferID, err)
	}
	inputs, err := f.cfg.Files.JobInputs(ctx, run.JobID)
	if err != nil {
		return state{}, err
	}
	s := state{inputs: inputs}
	s.id, s.model, s.offer = run.JobID, model, offer
	s.settings = wholeNumbers(run.Settings)
	s.taken, s.album, s.note = run.Taken, run.Album, run.Note
	for _, a := range inputs {
		if strings.HasPrefix(a.ContentType, "video/") {
			s.clip, s.keptSeconds = true, a.Seconds
		}
	}
	if s.clip {
		s.source = clip{fileID: run.ClipFileID, size: run.ClipBytes}
		s.clipSeconds = max(run.ClipSeconds, s.keptSeconds)
	}
	s.estimate = price.Of(model, offer, requestOf(run.Prompt, s.settings, inputs))
	if s.quote, err = f.quote(ctx, run.UserID, run.Lang); err != nil {
		return state{}, err
	}
	s.pay, s.hasKey = payKey, true
	if !run.Free && !s.clip {
		return s, nil
	}
	u, err := f.cfg.Allowance.Usage(ctx, run.UserID)
	if err != nil {
		f.log.Warn("free usage unread", "user", run.UserID, "error", err)
	}
	if !run.Free {
		// An edit on the key may go back to the free video.
		s.freeLeft = u.Videos.Limit > 0 && u.Videos.Left() > 0
		return s, nil
	}
	s.pay, s.usage = payFree, u.Images
	if s.video() {
		s.usage = u.Videos
	}
	s.hasKey = false
	if s.clip && s.clipSeconds > free.VideoEdit.MaxSourceSeconds+clipSlack {
		// The preview offers the clip on the key, marked when there is none.
		if s.hasKey, err = f.hasKey(ctx, run.UserID, offer.Provider); err != nil {
			return state{}, err
		}
	}
	return s, nil
}

// wholeNumbers turns the whole numbers a job's settings come back with from
// JSON into ints, as the run was made with.
func wholeNumbers(settings map[string]any) map[string]any {
	out := make(map[string]any, len(settings))
	for k, v := range settings {
		if x, ok := v.(float64); ok && x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			v = int(x)
		}
		out[k] = v
	}
	return out
}

// keyProviders are the providers the person has a key for that the provider
// has not refused.
func (f *Flow) keyProviders(ctx context.Context, user int64) (map[string]bool, error) {
	have := map[string]bool{}
	if f.cfg.Keys == nil {
		return have, nil
	}
	conns, err := f.cfg.Keys.List(ctx, user)
	if errors.Is(err, keys.ErrDisabled) {
		return have, nil
	}
	if err != nil {
		return nil, err
	}
	for _, c := range conns {
		if c.Status != keys.Invalid {
			have[c.Provider] = true
		}
	}
	return have, nil
}

// choices are the image models the panel lists for a run that takes taken
// photos: the free ones first, then the featured. A model whose offers
// cannot take the photos is left out, and so is a vector one: Telegram shows
// an SVG as a file, not a picture, so it is the site's.
func (f *Flow) choices(ctx context.Context, run db.ChatRun, current catalog.Offer) ([]choice, error) {
	models, err := f.cfg.Store.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	have, err := f.keyProviders(ctx, run.UserID)
	if err != nil {
		return nil, err
	}
	order := append([]string{current.Provider}, providerOrder...)
	rank := func(provider string) int {
		if i := slices.Index(order, provider); i >= 0 {
			return i
		}
		return len(order)
	}
	var out []choice
	for _, m := range models {
		if m.Kind != catalog.Image || (!m.Free && !m.Featured) || vector(m) {
			continue
		}
		if c, ok := freeChoice(m, run.Taken, have); ok {
			out = append(out, c)
			continue
		}
		offers := slices.Clone(m.Offers)
		offers = slices.DeleteFunc(offers, func(o catalog.Offer) bool { return !takesPhotos(o, run.Taken) })
		if len(offers) == 0 {
			continue
		}
		slices.SortStableFunc(offers, func(a, b catalog.Offer) int {
			return cmp.Or(cmp.Compare(boolRank(!have[a.Provider]), boolRank(!have[b.Provider])), cmp.Compare(rank(a.Provider), rank(b.Provider)))
		})
		out = append(out, choice{model: m, offer: offers[0], key: have[offers[0].Provider]})
	}
	slices.SortStableFunc(out, func(a, b choice) int { return cmp.Compare(boolRank(!a.free), boolRank(!b.free)) })
	return out, nil
}

// vector tells whether a model draws vector images, as the overlay says.
func vector(m catalog.Model) bool {
	return m.Traits.Family != nil && m.Traits.Family.Format == "vector"
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// freeChoice is a free model on its plan, when the plan takes the photos.
func freeChoice(m catalog.Model, taken int, have map[string]bool) (choice, bool) {
	if !m.Free {
		return choice{}, false
	}
	for _, o := range m.Offers {
		if plan, ok := free.PlanOf(o.ID); ok && taken >= plan.MinInputs && taken <= plan.MaxInputs {
			return choice{model: m, offer: o, free: true, key: have[o.Provider]}, true
		}
	}
	return choice{}, false
}

// takesPhotos tells whether an offer takes n reference images, as its
// parameters say.
func takesPhotos(o catalog.Offer, n int) bool {
	p, listed := o.Params["input_references"]
	switch {
	case n == 0:
		return !listed || p.Min == nil || *p.Min <= 0
	case !listed:
		return false
	}
	return p.Max == nil || float64(n) <= *p.Max
}

// setting is one setting the panel offers, with its values.
type setting struct {
	name   string
	values []any
}

// settingsOf are the settings of an offer the panel offers.
func settingsOf(o catalog.Offer) []setting {
	var out []setting
	for _, name := range shownSettings {
		p, ok := o.Params[name]
		if !ok {
			continue
		}
		var values []any
		switch p.Type {
		case catalog.Enum:
			values = p.Values
		case catalog.Range:
			if name != "n" || p.Max == nil {
				continue
			}
			lo := 1
			if p.Min != nil && *p.Min > 1 {
				lo = int(*p.Min)
			}
			for v := lo; v <= min(int(*p.Max), maxResults); v++ {
				values = append(values, v)
			}
		}
		if len(values) > 1 {
			out = append(out, setting{name: name, values: values})
		}
	}
	return out
}

// valueLabel is a setting's value on its button.
func valueLabel(lang string, v any) string {
	s := fmt.Sprint(v)
	switch s {
	case "auto", "low", "medium", "high":
		return t(lang, "set."+s)
	}
	return s
}

// keyboardOf is the keyboard of a view.
func (f *Flow) keyboardOf(ctx context.Context, run db.ChatRun, s state, v view) (*tg.InlineKeyboardMarkup, error) {
	back := []tg.InlineKeyboardButton{button(t(run.Lang, "btn.back"), callback(run.JobID, actBack), "")}
	switch v.screen {
	case screenModels, screenFamily, screenLocked:
		return f.modelKeyboard(ctx, run, s, v)
	case screenSettings:
		var rows [][]tg.InlineKeyboardButton
		for pi, set := range settingsOf(s.offer) {
			rows = append(rows, []tg.InlineKeyboardButton{{Text: t(run.Lang, "set."+set.name), Disabled: &tg.DisabledButton{}}})
			for vi, v := range set.values {
				text := valueLabel(run.Lang, v)
				if cur, ok := s.settings[set.name]; ok && sameSetting(cur, v) {
					text = "✓ " + text
				}
				b := button(text, callback(run.JobID, actPickSetting+strconv.Itoa(pi)+"."+strconv.Itoa(vi)), "")
				if vi%perRow == 0 {
					rows = append(rows, nil)
				}
				rows[len(rows)-1] = append(rows[len(rows)-1], b)
			}
		}
		return &tg.InlineKeyboardMarkup{InlineKeyboard: append(rows, back)}, nil
	}
	return previewKeyboard(run.Lang, run.JobID, s.draft), nil
}

func sameSetting(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// isGroup: a group's chat id is negative, a person's positive.
func isGroup(chatID int64) bool { return chatID < 0 }

// view shows a run's list of models, its settings or its main keyboard.
func (f *Flow) view(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place, v view) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StagePreview {
		return answer("")
	}
	if v == viewSettings && run.Free {
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "panel.free_fixed"))
	}
	s, err := f.stateOf(ctx, run)
	if err != nil {
		_ = answer("")
		return err
	}
	kb, err := f.keyboardOf(ctx, run, s, v)
	if err != nil {
		_ = answer("")
		return err
	}
	if on == onPreview && !run.PreviewEphemeral && !guest(run) && isGroup(run.ChatID) && v != viewMain {
		if f.openPanel(ctx, cq, run, s, kb) {
			return answer("")
		}
	}
	_ = answer("")
	f.setKeyboard(ctx, run, on, kb)
	return nil
}

// openPanel sends the requester the card with kb in place of the preview,
// which the chat keeps seeing as it was. It reports whether Telegram took
// it; the press must be fresh, which it is when it comes here.
func (f *Flow) openPanel(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, s state, kb *tg.InlineKeyboardMarkup) bool {
	id := fileIDOf(*cq.Message)
	if cq.Message.Date == 0 || id == "" {
		return false
	}
	card := previewCard(run.Lang, s.draft)
	opts := tg.CaptionOptions{
		ThreadID: run.ThreadID, Caption: previewCaption(run.Lang, run.Prompt, s.draft, labelDrawn(card)), Markup: kb,
		Ephemeral: &tg.Ephemeral{ReceiverUserID: run.UserID, CallbackQueryID: cq.ID, ReplaceCallbackQueryMessage: true},
	}
	var sent tg.Message
	var err error
	if cq.Message.Animation != nil {
		sent, err = f.cfg.Telegram.SendAnimation(ctx, run.ChatID, tg.InputFileString{Data: id}, tg.AnimationOptions{CaptionOptions: opts})
	} else {
		sent, err = f.cfg.Telegram.SendPhoto(ctx, run.ChatID, tg.InputFileString{Data: id}, opts)
	}
	if err != nil || sent.EphemeralMessageID == 0 {
		f.log.Warn("panel not sent; the chat sees the list", "job", run.JobID, "error", err)
		return false
	}
	f.dropPanel(ctx, run)
	if err := f.cfg.Store.SetChatRunPanel(ctx, run.JobID, sent.EphemeralMessageID); err != nil {
		f.log.Warn("panel not recorded", "job", run.JobID, "error", err)
		f.deleteEphemeral(ctx, run.ChatID, run.UserID, sent.EphemeralMessageID)
		return false
	}
	return true
}

// labelDrawn tells whether the card carries its price line, as
// anim.Renderer.Preview says, without drawing it.
func labelDrawn(p anim.Preview) bool { return p.Label != "" && anim.Covers(p.Label) }

// setKeyboard puts kb on the message pressed.
func (f *Flow) setKeyboard(ctx context.Context, run db.ChatRun, on place, kb *tg.InlineKeyboardMarkup) {
	var err error
	switch {
	case guest(run):
		err = f.cfg.Telegram.EditInlineMessageReplyMarkup(ctx, run.InlineMessageID, kb)
	case on == onPanel:
		err = f.cfg.Telegram.EditEphemeralMessageReplyMarkup(ctx, run.ChatID, run.UserID, run.PanelMessageID, kb)
	case run.PreviewEphemeral:
		err = f.cfg.Telegram.EditEphemeralMessageReplyMarkup(ctx, run.ChatID, run.UserID, run.PreviewMessageID, kb)
	default:
		err = f.cfg.Telegram.EditMessageReplyMarkup(ctx, run.ChatID, run.PreviewMessageID, kb)
	}
	if err != nil && !tg.IsMessageNotModified(err) {
		f.log.Warn("keyboard not changed", "job", run.JobID, "error", err)
	}
}

// pickModel moves the draft to another model and draws its card.
func (f *Flow) pickModel(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place, hash string) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StagePreview {
		return answer("")
	}
	_, current, err := f.cfg.Store.Offer(ctx, run.OfferID)
	if err != nil {
		_ = answer("")
		return err
	}
	list, err := f.choices(ctx, run, current)
	if err != nil {
		_ = answer("")
		return err
	}
	i := slices.IndexFunc(list, func(c choice) bool { return modelHash(c.model.ID) == hash })
	if i < 0 {
		// The catalog changed since the list was drawn.
		return answer("")
	}
	c := list[i]
	if c.model.ID == current.ModelID {
		_ = answer("")
		return f.redrawAfter(ctx, run, on, viewMain)
	}
	if !c.free && !c.key {
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "panel.key_needed"))
	}
	var settings map[string]any
	if c.free {
		plan, _ := free.PlanOf(c.offer.ID)
		settings = plan.SettingsFor(run.Taken)
	} else {
		settings = carried(wholeNumbers(run.Settings), c.offer)
	}
	return f.revise(ctx, cq, run, on, c.model, c.offer, settings, c.free, viewMain)
}

// carried are the settings that go to another model: those it takes.
func carried(settings map[string]any, o catalog.Offer) map[string]any {
	out := map[string]any{}
	for _, set := range settingsOf(o) {
		if v, ok := settings[set.name]; ok && slices.ContainsFunc(set.values, func(x any) bool { return sameSetting(x, v) }) {
			out[set.name] = v
		}
	}
	return out
}

// pickSetting changes one setting of a run on the person's key.
func (f *Flow) pickSetting(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place, pick string) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StagePreview {
		return answer("")
	}
	if run.Free {
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "panel.free_fixed"))
	}
	ps, vs, ok := strings.Cut(pick, ".")
	pi, err1 := strconv.Atoi(ps)
	vi, err2 := strconv.Atoi(vs)
	if !ok || err1 != nil || err2 != nil {
		return answer("")
	}
	model, offer, err := f.cfg.Store.Offer(ctx, run.OfferID)
	if err != nil {
		_ = answer("")
		return err
	}
	sets := settingsOf(offer)
	if pi < 0 || pi >= len(sets) || vi < 0 || vi >= len(sets[pi].values) {
		return answer("")
	}
	settings := wholeNumbers(run.Settings)
	name, v := sets[pi].name, sets[pi].values[vi]
	if cur, ok := settings[name]; ok && sameSetting(cur, v) {
		return answer("")
	}
	settings[name] = v
	return f.revise(ctx, cq, run, on, model, offer, settings, false, viewSettings)
}

// revise moves the draft to the offer and settings given, on the free tier
// when free says so and the tier allows it, on the person's key otherwise,
// and draws the card again.
func (f *Flow) revise(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place, model catalog.Model, offer catalog.Offer, settings map[string]any, onFree bool, v view) error {
	inputs, err := f.cfg.Files.JobInputs(ctx, run.JobID)
	if err != nil {
		_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
		return err
	}
	est := price.Of(model, offer, requestOf(run.Prompt, settings, inputs))
	q, err := f.quote(ctx, run.UserID, run.Lang)
	if err != nil {
		_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
		return err
	}
	n := jobs.New{UserID: run.UserID, Offer: offer, Prompt: run.Prompt, Settings: settings, Free: onFree, Estimate: &est}
	if q.Code != fx.USD {
		n.FX = &q
	}
	err = f.cfg.Queue.Revise(ctx, run.JobID, n)
	var (
		limit  *free.LimitError
		closed *free.ClosedError
	)
	if onFree && (errors.As(err, &limit) || errors.As(err, &closed)) {
		has, kerr := f.hasKey(ctx, run.UserID, offer.Provider)
		if kerr != nil {
			_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
			return kerr
		}
		if !has {
			text := closedText(run.Lang, offer.Kind)
			if limit != nil {
				text = f.limitText(run.Lang, limit)
			}
			return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, text)
		}
		n.Free = false
		err = f.cfg.Queue.Revise(ctx, run.JobID, n)
	}
	switch {
	case errors.Is(err, jobs.ErrNotDraft):
		_ = f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(run.Lang, "run.expired"))
		f.close(ctx, run, db.StagePreview)
		return nil
	case err != nil:
		_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
		return err
	}
	_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	return f.redrawAfter(ctx, run, on, v)
}

// redrawAfter reads the run again after a change and draws its card.
func (f *Flow) redrawAfter(ctx context.Context, run db.ChatRun, on place, v view) error {
	fresh, err := f.cfg.Store.ChatRun(ctx, run.JobID)
	if err != nil {
		return err
	}
	return f.redraw(ctx, fresh, on, v)
}

// redraw draws the run's card again from its files and puts it on the
// preview and on the panel.
func (f *Flow) redraw(ctx context.Context, run db.ChatRun, on place, v view) error {
	s, err := f.stateOf(ctx, run)
	if err != nil {
		return err
	}
	if guest(run) {
		// Telegram fetches the card framy draws from the run now.
		kb, err := f.keyboardOf(ctx, run, s, v)
		if err != nil {
			return err
		}
		err = f.guestCard(ctx, run, previewCaption(run.Lang, run.Prompt, s.draft, labelDrawn(previewCard(run.Lang, s.draft))), kb)
		if err != nil && !tg.IsMessageNotModified(err) {
			return fmt.Errorf("guest card not redrawn: %w", err)
		}
		return nil
	}
	dir, done, err := f.cfg.Work.Dir("chat")
	if err != nil {
		return err
	}
	defer done()
	paths, err := f.cardPictures(ctx, dir, s)
	if err != nil {
		return err
	}
	return f.drawCard(ctx, run, on, v, s, dir, paths)
}

// cardPictures reads what the card holds into dir: the run's photos, or
// the first frame of its clip.
func (f *Flow) cardPictures(ctx context.Context, dir string, s state) ([]string, error) {
	if !s.clip {
		return f.fetch(ctx, dir, s.inputs)
	}
	for _, a := range s.inputs {
		if !strings.HasPrefix(a.ContentType, "video/") {
			continue
		}
		if err := f.save(ctx, a, filepath.Join(dir, media.In)); err != nil {
			return nil, fmt.Errorf("the clip of the run: %w", err)
		}
		poster, err := f.cfg.Clips.Poster(ctx, dir)
		if err != nil {
			return nil, err
		}
		return []string{poster}, nil
	}
	return nil, nil
}

// drawCard draws the card of s with the pictures at paths and puts it on
// the preview and on the panel. The chat's preview keeps its main keyboard
// while the requester has a panel; the message pressed gets the view's.
func (f *Flow) drawCard(ctx context.Context, run db.ChatRun, on place, v view, s state, dir string, paths []string) error {
	kb, err := f.keyboardOf(ctx, run, s, v)
	if err != nil {
		return err
	}
	pic, err := f.cfg.Renderer.Preview(ctx, dir, previewCard(run.Lang, s.draft), paths)
	if err != nil {
		return fmt.Errorf("draw the preview: %w", err)
	}
	caption := previewCaption(run.Lang, run.Prompt, s.draft, pic.TextDrawn)
	fileID := ""
	if !run.PreviewEphemeral {
		public := kb
		if on == onPanel {
			public = previewKeyboard(run.Lang, run.JobID, s.draft)
		}
		msg, err := f.edit(ctx, run.ChatID, run.PreviewMessageID, pic, caption, public)
		if err != nil && !tg.IsMessageNotModified(err) {
			return fmt.Errorf("preview not redrawn: %w", err)
		}
		fileID = fileIDOf(msg)
	}
	target := int64(0)
	switch {
	case on == onPanel:
		target = run.PanelMessageID
	case run.PreviewEphemeral:
		target = run.PreviewMessageID
	}
	if target == 0 {
		return nil
	}
	err = f.editEphemeralPicture(ctx, run, target, pic, fileID, caption, kb)
	if err != nil && !tg.IsMessageNotModified(err) {
		return fmt.Errorf("ephemeral card not redrawn: %w", err)
	}
	return nil
}

// editEphemeralPicture puts the picture on an ephemeral message: by the
// file_id the chat's preview got, or as an upload.
func (f *Flow) editEphemeralPicture(ctx context.Context, run db.ChatRun, id int64, pic anim.Media, fileID, caption string, kb *tg.InlineKeyboardMarkup) error {
	if fileID != "" {
		return f.cfg.Telegram.EditEphemeralMessageMedia(ctx, run.ChatID, run.UserID, id, mediaItem(pic, tg.InputFileString{Data: fileID}, caption), kb)
	}
	file, err := os.Open(pic.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	name := "framy.png"
	if pic.ContentType == "image/jpeg" {
		name = "framy.jpg"
	}
	return f.cfg.Telegram.EditEphemeralMessageMedia(ctx, run.ChatID, run.UserID, id, mediaItem(pic, tg.InputFileUpload{Filename: name, Data: file}, caption), kb)
}

// mediaItem is the picture as an edit puts it on a message.
func mediaItem(pic anim.Media, file tg.InputFile, caption string) tg.InputMedia {
	if isAnimation(pic) {
		return tg.InputMediaAnimation{Media: file, Caption: caption, Width: pic.Width, Height: pic.Height, Duration: seconds(pic.Duration)}
	}
	return tg.InputMediaPhoto{Media: file, Caption: caption}
}

// fetch reads a run's files from the bucket into dir, for the card.
func (f *Flow) fetch(ctx context.Context, dir string, inputs []storage.Asset) ([]string, error) {
	var paths []string
	for i, a := range inputs {
		path := filepath.Join(dir, fmt.Sprintf("in-%d", i))
		if err := f.save(ctx, a, path); err != nil {
			return nil, fmt.Errorf("file %d of the run: %w", i, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (f *Flow) save(ctx context.Context, a storage.Asset, path string) error {
	body, err := f.cfg.Files.Open(ctx, a)
	if err != nil {
		return err
	}
	defer body.Close()
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
