// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
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
	plan, err := free.ForImages(len(inputs))
	if err != nil {
		return answer("")
	}
	model, offer, err := f.cfg.Store.Offer(ctx, plan.Offer)
	if err != nil {
		_ = answer("")
		return fmt.Errorf("the free model %s: %w", plan.Offer, err)
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
