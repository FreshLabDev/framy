// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
)

// Edited takes an edit of a message that asks framy for a run
// (docs/design.md, Step by step): /img or /edit, named by command, with
// prompt the words after it, or framy's mention in a group or a plain
// message of a DM, with no command and prompt their words. While the run
// the message started is still a preview, the words of the edit become its
// prompt, and the preview shows them and their price; the photos and the
// clip it took stay. A question of a DM not answered yet asks about the
// message as it reads now. After Generate an edit changes nothing, and an
// edit never starts a run: Telegram sends the edit of a message from days
// ago too.
func (f *Flow) Edited(ctx context.Context, m *tg.Message, command, prompt string) {
	if m == nil || m.From == nil || m.MessageID == 0 {
		return
	}
	f.wg.Go(func() {
		if err := f.edited(ctx, m, command, prompt); err != nil && ctx.Err() == nil {
			f.log.Warn("an edit not taken", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
		}
	})
}

func (f *Flow) edited(ctx context.Context, m *tg.Message, command, prompt string) error {
	if prompt = strings.TrimSpace(prompt); prompt == "" {
		// Words taken out leave the run the ones it has.
		return nil
	}
	if command == "" && m.Chat.Type == "private" {
		if err := f.editAsk(ctx, m); err != nil {
			return err
		}
	}
	id, seen, err := f.cfg.Store.JobByKey(ctx, dedupeKey(f.cfg.Stack, m))
	if err != nil || !seen {
		return err
	}
	unlock := f.lock(id)
	defer unlock()
	run, err := f.cfg.Store.ChatRun(ctx, id)
	if errors.Is(err, db.ErrNoRun) {
		return nil
	}
	if err != nil {
		return err
	}
	prompt = cutPrompt(prompt)
	if !editsPreview(run, m, command) || prompt == run.Prompt {
		return nil
	}
	model, offer, err := f.cfg.Store.Offer(ctx, run.OfferID)
	if err != nil {
		return err
	}
	inputs, err := f.cfg.Files.JobInputs(ctx, run.JobID)
	if err != nil {
		return err
	}
	est := price.Of(model, offer, requestOf(prompt, wholeNumbers(run.Settings), inputs))
	// The card is drawn first and the draft takes the words after, so
	// Generate never runs words the preview does not show.
	shown := run
	shown.Prompt = prompt
	drawn := f.redraw(ctx, shown, "", onPreview, viewMain)
	if tg.IsMessageNotModified(drawn) {
		// The card reads as it did: the words changed past what it quotes
		// (a prompt it cuts), and the draft is to take them all the same.
		drawn = nil
	}
	if drawn != nil && untouched(drawn) {
		return drawn
	}
	err = f.cfg.Queue.Reprompt(ctx, run.JobID, prompt, &est)
	switch {
	case errors.Is(err, jobs.ErrNotDraft):
		// Its ten minutes ran out meanwhile, and the preview goes as they do.
		return drawn
	case err != nil:
		// The draft keeps its words, and so does the card again: on the
		// preview the run has now, which a requester's own one sent anew
		// changed.
		if rerr := f.redrawAfter(ctx, run, "", onPreview, viewMain); rerr != nil {
			f.log.Warn("preview not drawn back", "job", run.JobID, "error", rerr)
		}
		return err
	}
	return drawn
}

// editsPreview tells whether an edit of m may change the words of run: the
// run is its message's, still a preview, and the edit asks for the same
// kind of run. /img changed to /edit, or back, asks for another run. A
// preview only the requester sees is sent to them anew with the new words,
// as a press has it sent (resendRich).
func editsPreview(run db.ChatRun, m *tg.Message, command string) bool {
	if run.Stage != db.StagePreview || guest(run) || run.PreviewMessageID == 0 {
		return false
	}
	if run.ChatID != m.Chat.ID || run.CommandMessageID != m.MessageID || run.UserID != m.From.ID {
		return false
	}
	switch command {
	case CommandImage:
		return run.ClipFileID == ""
	case CommandVideo:
		return run.ClipFileID != ""
	}
	return true
}

// untouched tells a redraw that failed and surely left the preview as it
// was: nothing reached Telegram, or Telegram refused the edit. An edit that
// got no answer, or a 5xx, may have changed it.
func untouched(err error) bool {
	if unsent(err) {
		return true
	}
	var api *tg.APIError
	return errors.As(err, &api) && cmp.Or(api.ErrorCode, api.StatusCode) < http.StatusInternalServerError
}

// editAsk keeps the message a question of a DM asks about as it reads now,
// so the answer starts the run with the words the person left.
func (f *Flow) editAsk(ctx context.Context, m *tg.Message) error {
	var (
		key askKey
		was *tg.Message
	)
	f.asks.Range(func(k, v any) bool {
		if q := v.(*tg.Message); q.Chat.ID == m.Chat.ID && q.MessageID == m.MessageID {
			key, was = k.(askKey), q
			return false
		}
		return true
	})
	if was == nil {
		return nil
	}
	now, err := f.ownSources(ctx, m)
	if err != nil {
		return err
	}
	// An answer that took the question meanwhile has it no more.
	f.asks.CompareAndSwap(key, was, now)
	return nil
}
