// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"fmt"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// What was full when a request was answered 503 busy: the label of
// framy_busy_total. The person's own limits keep one account from taking
// what everyone shares; the shared ones are the machine's.
const (
	// fullPhotoSlots: every conversion of a photo stayed taken for
	// UploadWait.
	fullPhotoSlots = "photo_slots"
	// fullPhotoPerson: maxPersonUploads photos of the person's were coming
	// in.
	fullPhotoPerson = "photo_person"
	// fullClipQueue: maxVideoTasks clips of everyone's waited or were worked
	// on.
	fullClipQueue = "clip_queue"
	// fullClipPerson: maxPersonVideoTasks clips of the person's were.
	fullClipPerson = "clip_person"
	// fullClipSlots: a clip's last frame waited UploadWait for a slot.
	fullClipSlots = "clip_slots"
)

// shared words what everyone shares for the operator chat, with how much
// of it there is; ok is false for a person's own limit.
func (a *API) shared(what string) (words string, ok bool) {
	switch what {
	case fullPhotoSlots:
		return fmt.Sprintf("every photo conversion (%d at once)", cap(a.slots)), true
	case fullClipQueue:
		return fmt.Sprintf("the clip queue (%d clips waiting or worked on)", maxVideoTasks), true
	case fullClipSlots:
		return fmt.Sprintf("every clip slot (%d at once)", cap(a.videos.slots)), true
	}
	return "", false
}

// noticeTimeout bounds a notice to the operator chat.
const noticeTimeout = 10 * time.Second

// full counts a busy answer. The first time in a UTC day that something
// everyone shares was full, the operator chat hears of it: people wait
// then, and only more room in the code helps. A process restarted that day
// may tell it again.
func (a *API) full(what string) {
	metrics.Busy.Inc(what)
	words, ok := a.shared(what)
	if !ok {
		return
	}
	a.log.Warn("full: people are asked to try again", "what", what)
	if a.cfg.Notify == nil || !a.claimNotice(what) {
		return
	}
	text := fmt.Sprintf("framy: %s was full today, and people were asked to try again in a moment. framy_busy_total{what=%q} counts each time.", words, what)
	a.notices.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
		defer cancel()
		if err := a.cfg.Notify(ctx, text); err != nil {
			a.log.Error("tell the operator chat", "error", err)
			a.releaseNotice(what)
		}
	})
}

// claimNotice says whether the operator chat still has to hear today that
// what was full.
func (a *API) claimNotice(what string) bool {
	a.noticeMu.Lock()
	defer a.noticeMu.Unlock()
	now := a.now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if a.told[what].Equal(day) {
		return false
	}
	if a.told == nil {
		a.told = map[string]time.Time{}
	}
	a.told[what] = day
	return true
}

// releaseNotice lets the next busy answer try the notice again.
func (a *API) releaseNotice(what string) {
	a.noticeMu.Lock()
	defer a.noticeMu.Unlock()
	delete(a.told, what)
}
