// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asterfield-dev/tg"

	"github.com/asterfield-dev/framy/internal/catalog"
	"github.com/asterfield-dev/framy/internal/db"
	"github.com/asterfield-dev/framy/internal/free"
)

// Image / Video on a preview (docs/design.md, `/img` and `/edit` in a
// group, step 4). A run from a prompt and at most one photo turns into a
// video of them, the photo its start frame, and back into an image: on the
// person's default model of the kind where the run can go on it (own.go),
// else on the free video's model and the free image model. The files stay:
// the draft is revised in place, past the free tier on the person's key,
// and the card is drawn again.

// toVideo turns an image run into a video from its prompt and its photo.
func (f *Flow) toVideo(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StagePreview {
		return answer("")
	}
	plan, model, offer, err := f.videoPlan(ctx)
	if err != nil {
		_ = answer("")
		return err
	}
	if run.Taken > plan.MaxInputs {
		// An album is not one start frame; its preview has no such button.
		return answer("")
	}
	return f.reviseOwn(ctx, cq, run, on, model, offer, plan.SettingsFor(run.Taken))
}

// toImage turns a video from a prompt back into an image, on the free plan
// for its photos. An edit of a clip stays one.
func (f *Flow) toImage(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place) error {
	answer := func(text string) error { return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, text) }
	if run.Stage != db.StagePreview || run.ClipFileID != "" {
		return answer("")
	}
	inputs, err := f.cfg.Files.JobInputs(ctx, run.JobID)
	if err != nil {
		_ = answer("")
		return err
	}
	for _, a := range inputs {
		if strings.HasPrefix(a.ContentType, "video/") {
			return answer("")
		}
	}
	plan, model, offer, err := f.imagePlan(ctx, len(inputs))
	if err != nil {
		_ = answer("")
		var nf *free.NotFreeError
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	return f.reviseOwn(ctx, cq, run, on, model, offer, plan.SettingsFor(len(inputs)))
}

// reviseOwn turns the run into the other kind: on the person's default
// model of that kind where the run can go on it, else on the free plan
// given, past the free tier on a key.
func (f *Flow) reviseOwn(ctx context.Context, cq *tg.CallbackQuery, run db.ChatRun, on place, model catalog.Model, offer catalog.Offer, settings map[string]any) error {
	e, ok, err := f.ownFor(ctx, run, model, offer, settings)
	if err != nil {
		_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
		return err
	}
	if ok {
		return f.revise(ctx, cq, run, on, e.model, e.offer, e.settings, e.free, true, viewMain)
	}
	return f.revise(ctx, cq, run, on, model, offer, settings, true, true, viewMain)
}

// imagePlan is the free plan of an image from this many photos, with its
// model and offer: words alone go on free.ImageWords while the catalog lists
// its free variant and framy's account has free requests left today, and
// everything else, or words when it cannot, on free.ForImages. More photos
// than the free models take come back as a *free.NotFreeError.
func (f *Flow) imagePlan(ctx context.Context, photos int) (free.Plan, catalog.Model, catalog.Offer, error) {
	if photos == 0 && f.wordsOpen() {
		model, offer, err := f.cfg.Store.Offer(ctx, free.ImageWords.Offer)
		switch {
		case err != nil:
			f.log.Warn("the free variant's model is not in the catalog", "offer", free.ImageWords.Offer, "error", err)
		case unmeteredTakes(offer, photos):
			return free.ImageWords, model, offer, nil
		}
	}
	plan, err := free.ForImages(photos)
	if err != nil {
		return free.Plan{}, catalog.Model{}, catalog.Offer{}, err
	}
	model, offer, err := f.cfg.Store.Offer(ctx, plan.Offer)
	if err != nil {
		return free.Plan{}, catalog.Model{}, catalog.Offer{}, fmt.Errorf("the free model %s: %w", plan.Offer, err)
	}
	return plan, model, offer, nil
}

// unmeteredTakes reports whether the offer's free variant takes a run of
// this many photos and one result now.
func unmeteredTakes(offer catalog.Offer, photos int) bool {
	_, ok := offer.FreeRoute(time.Now(), photos, 1)
	return ok
}

// wordsOpen reports whether framy's account still has requests to free
// models today (free.Allowance.Words); yes when the allowance does not say.
func (f *Flow) wordsOpen() bool {
	if w, ok := f.cfg.Allowance.(interface{ Words() bool }); ok {
		return w.Words()
	}
	return true
}
