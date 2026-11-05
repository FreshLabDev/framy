// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/storage"
)

// The person's default models (docs/design.md, `/img` and `/edit` in a
// group, step 3): the model of each kind the site's composer opens on,
// chosen in the site's settings. Where the chat picks a model for the
// person (a command, the answer to the question of a DM, Make a video,
// Make an image and Animate) it opens on that model too, as the site does,
// when the run can go on it: on its free plan while a free run of the kind
// is left, else on a key of theirs. Otherwise framy's own choice stands,
// the free model first. A model the run already has stays: Again, the
// error card's buttons and an edit of a clip keep theirs.

// ownModel is the run d on the person's default model of its kind, as the
// Model list would take it: with its settings, its price and who pays. ok
// is false when they chose none, the run is on it already, it is no longer
// listed, or the run cannot go on it: it does not take the run's photos,
// or it needs a key they have not connected. A read that fails leaves
// framy's choice, since the run can still go; it is logged.
func (f *Flow) ownModel(ctx context.Context, user int64, lang, prompt string, d draft) (entry, bool) {
	if d.clip {
		return entry{}, false
	}
	st, err := f.cfg.Store.Settings(ctx, user)
	if err != nil {
		f.log.Warn("default models unread", "user", user, "error", err)
		return entry{}, false
	}
	id := st.DefaultImage
	if d.video() {
		id = st.DefaultVideo
	}
	if id == "" || id == d.model.ID {
		return entry{}, false
	}
	models, err := f.cfg.Store.Catalog(ctx)
	if err != nil {
		f.log.Warn("catalog unread for the default model", "user", user, "error", err)
		return entry{}, false
	}
	byID := map[string]bool{}
	for _, m := range models {
		byID[m.ID] = true
	}
	i := slices.IndexFunc(models, func(m catalog.Model) bool { return m.ID == id })
	if i < 0 || !listed(models[i], d.offer.Kind, byID) {
		return entry{}, false
	}
	have, err := f.workingKeys(ctx, user)
	if err != nil {
		f.log.Warn("keys unread for the default model", "user", user, "error", err)
		return entry{}, false
	}
	providers := append([]string{d.offer.Provider}, providerOrder...)
	p := pickOf(lang, prompt, d, models[i], have, providers, f.freeLeft(ctx, user, d.offer.Kind))
	if p.why != "" || (!p.free && !p.key) {
		return entry{}, false
	}
	return p, true
}

// workingKeys are the providers the person has a key for that no run has
// found wanting: not refused, and not out of credits, billing or
// verification. A default on such a key would take the place of a free run
// that works with a run that fails, so the default waits until the site's
// Re-check or a run clears the status.
func (f *Flow) workingKeys(ctx context.Context, user int64) (map[string]bool, error) {
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
		switch c.Status {
		case keys.Invalid, keys.NoCredits, keys.NeedsBilling, keys.NeedsVerification:
		default:
			have[c.Provider] = true
		}
	}
	return have, nil
}

// onOwn moves the draft to the entry ownModel gave.
func (d *draft) onOwn(e entry) {
	d.model, d.offer, d.settings, d.estimate = e.model, e.offer, e.settings, e.estimate
	d.pay = payKey
	if e.free {
		d.pay = payFree
	}
}

// ownFor is ownModel for a preview that turns into the other kind (Make a
// video, Make an image): the run's photos on the free plan given, which is
// where the press goes when the person has no default the run can take.
func (f *Flow) ownFor(ctx context.Context, run db.ChatRun, model catalog.Model, offer catalog.Offer, settings map[string]any) (entry, bool, error) {
	inputs, err := f.cfg.Files.JobInputs(ctx, run.JobID)
	if err != nil {
		return entry{}, false, err
	}
	d := draft{model: model, offer: offer, pay: payFree, settings: settings, taken: run.Taken, many: severalResults(run)}
	d.images = imagesOf(inputs)
	e, ok := f.ownModel(ctx, run.UserID, run.Lang, run.Prompt, d)
	return e, ok, nil
}

// imagesOf are the sizes of the pictures among a run's files.
func imagesOf(inputs []storage.Asset) []price.Image {
	var out []price.Image
	for _, a := range inputs {
		if !strings.HasPrefix(a.ContentType, "video/") {
			out = append(out, price.Image{Width: a.Width, Height: a.Height})
		}
	}
	return out
}
