// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// continueSettings is the setting each provider continues a video by: the
// id of the job that made it, at the provider (docs/design.md, Actions on a
// result). Which models take it is the overlay's continue trait.
var continueSettings = map[string]string{
	catalog.OpenRouter: "previous_job_id",
	catalog.Gemini:     "previous_interaction_id",
}

// continuationSetting names the settings framy fills itself.
var continuationSetting = func() map[string]bool {
	out := map[string]bool{}
	for _, name := range continueSettings {
		out[name] = true
	}
	return out
}()

// Continuation is a job of the person's as a run that continues it needs
// it: who made the video, at which provider, and the video itself.
type Continuation struct {
	ModelID       string
	Provider      string
	ProviderJobID string
	Status        jobs.Status
	Free          bool
	// Video is the job's first video result, nil when it has none kept.
	Video *InputAsset
}

// continued is what a run that continues a job takes from it.
type continued struct {
	setting, providerJobID string
	video                  InputAsset
}

// continuation checks that the run may continue job id, and says why not.
// The earlier video goes on: on the same model, through the same provider,
// on the person's own key, since the provider keeps the job under the key
// that made it.
func (a *API) continuation(r *http.Request, user, id int64, m catalog.Model, offer catalog.Offer, isFree bool) (continued, *apiError) {
	refuse := func(msg string) (continued, *apiError) {
		return continued{}, newError(http.StatusUnprocessableEntity, codeCannotContinue, msg).with("continues", id)
	}
	setting, known := continueSettings[offer.Provider]
	switch {
	case !m.Traits.Continue || !known:
		return refuse(m.ID + " does not continue a video")
	case isFree:
		return refuse("a run that continues a video goes on your own key")
	}
	c, err := a.data.Continuation(r.Context(), user, id)
	if errors.Is(err, ErrNotFound) {
		return refuse("job " + strconv.FormatInt(id, 10) + " is not one of yours")
	}
	if err != nil {
		return continued{}, a.internalError(r, err)
	}
	switch {
	case c.Status != jobs.Done:
		return refuse(fmt.Sprintf("job %d is %s, not done", id, c.Status))
	case c.ModelID != m.ID:
		return refuse(fmt.Sprintf("job %d was made by %s; a video goes on with the model that made it", id, c.ModelID))
	case c.Provider != offer.Provider:
		return refuse(fmt.Sprintf("job %d was made through %s; it goes on through the same provider", id, c.Provider))
	case c.Free:
		// It was made on framy's key, which the person's run cannot reach.
		return refuse(fmt.Sprintf("job %d ran on the free allowance, and only a run on your own key goes on", id))
	case c.ProviderJobID == "":
		return refuse(fmt.Sprintf("the provider gave job %d no id to go on from", id))
	case c.Video == nil:
		return refuse(fmt.Sprintf("job %d has no video kept", id))
	case c.Video.Seconds <= 0:
		return refuse(fmt.Sprintf("the length of job %d's video is not known", id))
	}
	return continued{setting: setting, providerJobID: c.ProviderJobID, video: *c.Video}, nil
}
