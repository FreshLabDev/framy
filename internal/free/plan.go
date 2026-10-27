// SPDX-License-Identifier: Apache-2.0
package free

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// What the owner allows (docs/design.md, Free tier): 5 images in any 24
// hours and 1 video in any 7 days, per Telegram user id. Changing a count or
// a window is the owner's call.
const (
	ImagesPerWindow = 5
	ImageWindow     = 24 * time.Hour
	VideosPerWindow = 1
	VideoWindow     = 7 * 24 * time.Hour
)

// limit is the allowance of a kind of run.
type limit struct {
	runs   int
	window time.Duration
}

func limitOf(kind catalog.Kind) limit {
	if kind == catalog.Video {
		return limit{VideosPerWindow, VideoWindow}
	}
	return limit{ImagesPerWindow, ImageWindow}
}

// Plan is how one model runs on the allowance: framy's key, one result, and
// settings nobody can change. Every other control is locked with "with your
// key".
type Plan struct {
	// Model is the OpenRouter id of the model, and Offer the offer's id in
	// the catalog.
	Model string
	Offer string
	Kind  catalog.Kind
	// MinInputs and MaxInputs are how many of the person's files the run
	// takes: photos to edit or a start frame, or the clip a video edit works
	// on.
	MinInputs, MaxInputs int
	// fixed is every setting of the run, with its value. A job may not set
	// another, or the same to another value.
	fixed map[string]any
}

// Settings returns the settings the run goes with, to put in the job.
func (p Plan) Settings() map[string]any { return maps.Clone(p.fixed) }

func openRouterPlan(model string, kind catalog.Kind, minInputs, maxInputs int, fixed map[string]any) Plan {
	return Plan{
		Model: model, Offer: catalog.OfferID(catalog.OpenRouter, model), Kind: kind,
		MinInputs: minInputs, MaxInputs: maxInputs, fixed: fixed,
	}
}

var (
	// ImageMAI is the owner's free image model (26 October): about 1K, which
	// MAI has no setting for, and one result. It takes 5 photos.
	ImageMAI = openRouterPlan("microsoft/mai-image-2.6-flash", catalog.Image, 0, 5, map[string]any{"n": 1})
	// ImageKlein takes the whole album when more than 5 photos come in, and
	// is the fallback; the design leaves it to the blind test to confirm.
	ImageKlein = openRouterPlan("black-forest-labs/flux-3-image-klein", catalog.Image, 0, 10, map[string]any{"n": 1, "resolution": "1K"})
	// VideoHeyGen is the weekly video: 5 s at 768p, with an optional start
	// frame and no references. A proposal until the blind test.
	VideoHeyGen = openRouterPlan("heygen/heygen-video-1", catalog.Video, 0, 1, map[string]any{"duration": 5, "resolution": "768p"})
	// VideoGrok is the cheaper video from another vendor, if HeyGen fails.
	VideoGrok = openRouterPlan("x-ai/grok-imagine-video-1.5-lite", catalog.Video, 0, 1, map[string]any{"duration": 5, "resolution": "720p"})
	// VideoEdit is the weekly video as an edit of a clip up to 5 s (owner,
	// 27 October). The clip is the run's one input; its length is the
	// surface's to check, since the price follows it.
	VideoEdit = openRouterPlan("black-forest-labs/flux-video-edit", catalog.Video, 1, 1, map[string]any{})

	plans = []Plan{ImageMAI, ImageKlein, VideoHeyGen, VideoGrok, VideoEdit}
)

// Models are the OpenRouter ids of the models the allowance runs; the overlay
// marks them `free`.
func Models() []string {
	out := make([]string, len(plans))
	for i, p := range plans {
		out[i] = p.Model
	}
	return out
}

// ForImages is the plan of an image run that takes the given number of
// photos: MAI up to 5, Klein for more. More than Klein takes comes back as a
// *NotFreeError, and the person's own key can run it.
func ForImages(photos int) (Plan, error) {
	switch {
	case photos <= ImageMAI.MaxInputs:
		return ImageMAI, nil
	case photos <= ImageKlein.MaxInputs:
		return ImageKlein, nil
	}
	return Plan{}, &NotFreeError{Reason: fmt.Sprintf("%d photos are more than the free models take", photos)}
}

// planOf finds the plan of an offer.
func planOf(offer string) (Plan, bool) {
	for _, p := range plans {
		if p.Offer == offer {
			return p, true
		}
	}
	return Plan{}, false
}

// check says why the job is not the plan's run, or returns nil. An estimate
// is needed because the budget is counted from it.
func (p Plan) check(a jobs.Admission) error {
	if a.Kind != "" && a.Kind != p.Kind {
		return &NotFreeError{Reason: fmt.Sprintf("%s makes %ss, not %ss", a.Offer, p.Kind, a.Kind)}
	}
	if a.Inputs < p.MinInputs || a.Inputs > p.MaxInputs {
		return &NotFreeError{Reason: fmt.Sprintf("%s takes %s on the free allowance, not %d", a.Offer, inputsName(p), a.Inputs)}
	}
	set := map[string]any{}
	for name, v := range a.Settings {
		if v != nil {
			set[name] = v
		}
	}
	for name, v := range set {
		want, ok := p.fixed[name]
		if !ok {
			return &NotFreeError{Reason: name + " is locked on the free allowance"}
		}
		if !sameValue(want, v) {
			return &NotFreeError{Reason: fmt.Sprintf("%s is locked to %v on the free allowance", name, want)}
		}
	}
	for name, want := range p.fixed {
		if _, ok := set[name]; !ok {
			return &NotFreeError{Reason: fmt.Sprintf("%s must be %v on the free allowance", name, want)}
		}
	}
	if math.IsNaN(a.EstimateUSD) || a.EstimateUSD <= 0 {
		return &NotFreeError{Reason: "the run has no price estimate, and the budget is counted from it"}
	}
	return nil
}

func inputsName(p Plan) string {
	files := func(n int) string {
		if n == 1 {
			return "1 file"
		}
		return fmt.Sprintf("%d files", n)
	}
	switch {
	case p.MinInputs == p.MaxInputs:
		return files(p.MaxInputs)
	case p.MinInputs == 0:
		return "at most " + files(p.MaxInputs)
	}
	return fmt.Sprintf("%d to %d files", p.MinInputs, p.MaxInputs)
}

// sameValue compares as JSON, so 5 from a Go int and 5 from a decoded
// document are the same.
func sameValue(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}
