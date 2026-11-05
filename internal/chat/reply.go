// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"

	"github.com/FreshLabDev/tg"
)

// A reply to a result in a group (docs/design.md, `/img` and `/edit` in a
// group): the person whose run made a result answers it with words and no
// command, "make it brighter", and expects framy to go on from it. Telegram
// gives the bot such a reply even in privacy mode. framy takes it as the
// command would be taken in reply to the result, /img for a picture and
// /edit for a video, when the preview can go to that person alone: framy is
// an admin of the group. Anything else stays unanswered, as before: a reply
// can be "thanks" as well as an instruction, and the chat should not see a
// card or a refusal for it. A command in reply works for anyone, anywhere.

// Reply takes a message of a group that answers framy's own message without
// a command, in the background.
func (f *Flow) Reply(ctx context.Context, m *tg.Message) {
	if m == nil || m.From == nil || m.From.IsBot || m.Chat.Type == "private" || m.ReplyToMessage == nil {
		return
	}
	f.wg.Go(func() {
		// A stop turns no one away here: words to a result may be no
		// request at all, and the chat would read "send it again" to them.
		f.withSlot(ctx, nil, func() {
			if err := f.answerReply(ctx, m); err != nil && ctx.Err() == nil {
				f.log.Error("reply to a result failed", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
			}
		})
	})
}

// answerReply starts the run a reply asks for, when it is words alone
// answering a result of the sender's own run, and framy can answer them
// alone.
func (f *Flow) answerReply(ctx context.Context, m *tg.Message) error {
	prompt := f.words(m)
	if m.Text == "" || prompt == "" || !fromBot(m.ReplyToMessage) {
		// Media of its own, a sticker or a voice: not words to a result.
		return nil
	}
	jobID, result, err := f.cfg.Store.ResultRun(ctx, m.Chat.ID, m.ReplyToMessage.MessageID)
	if err != nil || !result {
		return err
	}
	run, err := f.cfg.Store.ChatRun(ctx, jobID)
	if err != nil {
		return err
	}
	if run.UserID != m.From.ID || !f.isAdmin(ctx, m.Chat.ID) {
		return nil
	}
	command := CommandImage
	if sourcesOf(m).clip != nil {
		command = CommandVideo
	}
	err = f.command(quietly(ctx), m, command, prompt)
	if errors.Is(err, errNotAlone) {
		f.log.Info("a reply to a result left unanswered: the preview would be the chat's", "chat", m.Chat.ID)
		return nil
	}
	return err
}

// errNotAlone: a quiet run's preview could not go to its requester alone.
var errNotAlone = errors.New("the preview cannot go to the requester alone")

type quietKey struct{}

// quietly marks a run started by a reply rather than a command: nothing of
// it goes where the chat sees it before Generate, neither the preview nor a
// refusal.
func quietly(ctx context.Context) context.Context { return context.WithValue(ctx, quietKey{}, true) }

func isQuiet(ctx context.Context) bool {
	q, _ := ctx.Value(quietKey{}).(bool)
	return q
}
