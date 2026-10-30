// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
)

// In a DM the progress of a run is also a draft (Bot API 10.3): a line
// Telegram shows under the loop, as if framy were writing, with a Stop
// button of Telegram's own. The draft's id is the job's, so a press names
// the run. A draft lasts 30 seconds, and the caption's tick sends it again;
// the result or the error card is sent as a new message, which takes the
// draft's place. Where Telegram refuses the draft, the loop keeps its own
// Stop and the time in its caption, as in a group.

// inDM tells a run of a DM: its chat is the person's own.
func inDM(run db.ChatRun) bool { return run.ChatID == run.UserID && run.ChatID > 0 }

// streamDraft streams the run's progress into the DM with Stop, and tells whether
// Telegram took it.
func (f *Flow) streamDraft(ctx context.Context, run db.ChatRun, elapsed time.Duration) bool {
	err := f.cfg.Telegram.SendMessageDraft(ctx, run.ChatID, run.JobID, generatingLine(run.Lang, run.Model, elapsed),
		tg.DraftOptions{ThreadID: run.ThreadID, CanStop: true})
	if err != nil {
		f.log.Debug("draft not sent; the loop has Stop", "job", run.JobID, "error", err)
		return false
	}
	return true
}

// StopDraft takes a press of Stop on a run's draft in a DM. It returns at
// once and works in the background, as Callback does.
func (f *Flow) StopDraft(ctx context.Context, s *tg.MessageGenerationStopped) {
	if s == nil || s.DraftID <= 0 {
		return
	}
	f.wg.Go(func() {
		if err := f.stopDraft(ctx, s); err != nil && ctx.Err() == nil {
			f.log.Error("draft stop failed", "job", s.DraftID, "error", err)
		}
	})
}

// stopDraft is Stop on the loop, for a press that has no message to answer:
// a run that had started is told stopped in the DM.
func (f *Flow) stopDraft(ctx context.Context, s *tg.MessageGenerationStopped) error {
	jobID := s.DraftID
	unlock := f.lock(jobID)
	defer unlock()
	run, err := f.cfg.Store.ChatRun(ctx, jobID)
	if errors.Is(err, db.ErrNoRun) {
		return nil
	}
	if err != nil {
		return err
	}
	// Only the person's own DM has the run's draft.
	if !inDM(run) || run.ChatID != s.Chat.ID || run.Stage != db.StageGenerating {
		return nil
	}
	running, err := f.halt(ctx, run)
	if err != nil || !running {
		return err
	}
	if _, err := f.cfg.Telegram.SendReply(ctx, run.ChatID, run.CommandMessageID, run.ThreadID, t(run.Lang, "run.stopped"), nil); err != nil {
		f.log.Warn("stop not told", "job", jobID, "error", err)
	}
	return nil
}
