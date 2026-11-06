// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/FreshLabDev/tg"
)

// maxAnswer is the longest answer to a press Telegram takes, an alert or a
// toast: 200 characters, counted as UTF-16 code units as Telegram counts
// text. A longer one is refused with a 400, and the person gets nothing at
// all: the button only stops spinning.
const maxAnswer = 200

// guarded is the Telegram the flow calls: it keeps what framy sends within
// what Telegram takes. The answers to presses are written to fit
// (TestAnswersFitTelegram); one that still does not, a new translation say,
// is cut to fit with an ellipsis rather than lost. A chat that Telegram
// asked to leave alone for a while, with a 429 and its retry_after, gets
// nothing until then (pace).
type guarded struct {
	Telegram
	pace *pace
}

func (g guarded) AnswerCallbackQuery(ctx context.Context, callbackID, text string) error {
	return g.Telegram.AnswerCallbackQuery(ctx, callbackID, fitAnswer(text))
}

func (g guarded) AnswerCallbackQueryAlert(ctx context.Context, callbackID, text string) error {
	return g.Telegram.AnswerCallbackQueryAlert(ctx, callbackID, fitAnswer(text))
}

// fitAnswer is text cut to maxAnswer code units, the last of them an
// ellipsis, on a whole character.
func fitAnswer(text string) string {
	if utf16Len(text) <= maxAnswer {
		return text
	}
	n := 0
	for i, r := range text {
		w := utf16.RuneLen(r)
		if n+w > maxAnswer-1 {
			return text[:i] + "…"
		}
		n += w
	}
	return text
}

// utf16Len is the length of s as Telegram counts it.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// captionLen is the length of an HTML caption as Telegram counts it against
// maxCaption: the text once the tags are read, "&amp;" as one "&". framy
// escapes every text it puts in a caption, so a "<" is always a tag's.
func captionLen(caption string) int {
	var text strings.Builder
	for {
		open := strings.IndexByte(caption, '<')
		if open < 0 {
			text.WriteString(caption)
			break
		}
		text.WriteString(caption[:open])
		end := strings.IndexByte(caption[open:], '>')
		if end < 0 {
			break
		}
		caption = caption[open+end+1:]
	}
	return utf16Len(html.UnescapeString(text.String()))
}

// The pace of a chat (docs/design.md, Limits of the chat). Telegram takes
// about 20 messages a minute in a group, and past that answers a send or an
// edit with 429 and how long to wait. tg waits that once and sends again;
// a second 429 is the flow's to handle, and every call to that chat before
// the time is up would only get another one, and make the wait longer.
//
// So a 429 holds the chat until its retry_after is over. A call to a held
// chat waits for it when it answers a person, a command or a press, who
// would rather have the answer late than not at all; one that waits longer
// than maxHold is refused at once, so a long flood wait does not hold one of
// the few command slots. The rounds that follow the runs and send the
// site's results do not wait (noWait): a held chat is skipped and gets its
// turn in a round after the hold, so one chat never stalls the others. A
// delete that cleans up after a message does not wait in the call either,
// and goes in the background once the hold is over (Flow.deleteMessage).
const maxHold = 30 * time.Second

type pace struct {
	mu    sync.Mutex
	until map[int64]time.Time
	// now and sleep are the clock, which tests stop.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func newPace() *pace {
	return &pace{until: map[int64]time.Time{}, now: time.Now, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// heldUntil is when chat's hold ends; zero when it is not held.
func (p *pace) heldUntil(chat int64) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.until[chat]
	if ok && !p.now().Before(until) {
		delete(p.until, chat)
		return time.Time{}
	}
	return until
}

// held tells whether chat is held now.
func (p *pace) held(chat int64) bool { return !p.heldUntil(chat).IsZero() }

// left is how long chat is held for yet; zero when it is not held.
func (p *pace) left(chat int64) time.Duration {
	until := p.heldUntil(chat)
	if until.IsZero() {
		return 0
	}
	return max(until.Sub(p.now()), 0)
}

// hold holds chat for d from now, or longer when it is held longer already.
func (p *pace) hold(chat int64, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if until := now.Add(d); until.After(p.until[chat]) {
		p.until[chat] = until
	}
	if len(p.until) > 1000 {
		for c, until := range p.until {
			if !now.Before(until) {
				delete(p.until, c)
			}
		}
	}
}

// errHeld is the answer to a call to a held chat that does not wait.
var errHeld = errors.New("telegram asked to wait before writing to this chat again")

type noWaitKey struct{}

// noWait marks calls made under ctx as ones that do not wait for a held
// chat: they fail with errHeld.
func noWait(ctx context.Context) context.Context { return context.WithValue(ctx, noWaitKey{}, true) }

// wait waits until chat is no longer held, as ctx allows.
func (g guarded) wait(ctx context.Context, chat int64) error {
	until := g.pace.heldUntil(chat)
	if until.IsZero() {
		return nil
	}
	left := until.Sub(g.pace.now())
	if ctx.Value(noWaitKey{}) == true || left > maxHold {
		return fmt.Errorf("chat %d for %s more: %w", chat, left.Round(time.Second), errHeld)
	}
	return g.pace.sleep(ctx, left)
}

// note holds chat when err is Telegram asking to wait. tg's error carries
// the wait even when its own wait was cut short by ctx.
func (g guarded) note(chat int64, err error) error {
	if d := tg.RetryAfter(err); d > 0 {
		g.pace.hold(chat, d)
	}
	return err
}

// paced runs a call to chat under the chat's pace. Its failure is marked
// as the call's (callFailure), unlike a wait for the chat, which calls nothing.
func paced[T any](g guarded, ctx context.Context, chat int64, call func() (T, error)) (T, error) {
	if err := g.wait(ctx, chat); err != nil {
		var zero T
		return zero, err
	}
	v, err := call()
	if err != nil {
		err = callFailure{err}
	}
	return v, g.note(chat, err)
}

// callFailure is the failure of a call to Telegram: its answer, or no
// answer. What fails without it failed before any call, when nothing of a
// send had gone out yet: the file unread from the bucket, the video not
// readied (site.go, notSent).
type callFailure struct{ err error }

func (e callFailure) Error() string { return e.err.Error() }
func (e callFailure) Unwrap() error { return e.err }

// unsent tells a failure of a send that surely took nothing to the chat:
// one before Telegram was called, or a call that never connected to the
// Bot API server, as while it restarts. Anything else Telegram may have
// delivered: a refusal did not, but an answer lost on the way, or a 5xx a
// gateway gives while the send goes on behind it, may have.
func unsent(err error) bool {
	var call callFailure
	if !errors.As(err, &call) {
		return true
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func paced0(g guarded, ctx context.Context, chat int64, call func() error) error {
	_, err := paced(g, ctx, chat, func() (struct{}, error) { return struct{}{}, call() })
	return err
}

func (g guarded) SendReply(ctx context.Context, chatID, replyTo int64, threadID int, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) {
		return g.Telegram.SendReply(ctx, chatID, replyTo, threadID, text, markup)
	})
}

func (g guarded) SendEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) {
		return g.Telegram.SendEphemeralMessage(ctx, chatID, receiverUserID, ephemeralMessageID, text, markup)
	})
}

func (g guarded) SendPhoto(ctx context.Context, chatID int64, photo tg.InputFile, opts tg.CaptionOptions) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) { return g.Telegram.SendPhoto(ctx, chatID, photo, opts) })
}

func (g guarded) SendAnimation(ctx context.Context, chatID int64, animation tg.InputFile, opts tg.AnimationOptions) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) { return g.Telegram.SendAnimation(ctx, chatID, animation, opts) })
}

func (g guarded) SendDocument(ctx context.Context, chatID int64, document tg.InputFile, opts tg.DocumentOptions) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) { return g.Telegram.SendDocument(ctx, chatID, document, opts) })
}

func (g guarded) SendVideo(ctx context.Context, chatID int64, video tg.InputFile, opts tg.VideoOptions) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) { return g.Telegram.SendVideo(ctx, chatID, video, opts) })
}

func (g guarded) SendMediaGroup(ctx context.Context, chatID int64, media []tg.InputMedia, threadID int) ([]tg.Message, error) {
	return paced(g, ctx, chatID, func() ([]tg.Message, error) { return g.Telegram.SendMediaGroup(ctx, chatID, media, threadID) })
}

func (g guarded) EditMessageMediaMessage(ctx context.Context, chatID, messageID int64, media tg.InputMedia, markup *tg.InlineKeyboardMarkup) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) {
		return g.Telegram.EditMessageMediaMessage(ctx, chatID, messageID, media, markup)
	})
}

func (g guarded) EditMessageCaption(ctx context.Context, chatID, messageID int64, caption string, opts tg.EditCaptionOptions) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) {
		return g.Telegram.EditMessageCaption(ctx, chatID, messageID, caption, opts)
	})
}

func (g guarded) EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *tg.InlineKeyboardMarkup) error {
	return paced0(g, ctx, chatID, func() error { return g.Telegram.EditMessageReplyMarkup(ctx, chatID, messageID, markup) })
}

func (g guarded) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return paced0(g, ctx, chatID, func() error { return g.Telegram.DeleteMessage(ctx, chatID, messageID) })
}

func (g guarded) EditEphemeralMessageMedia(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, media tg.InputMedia, markup *tg.InlineKeyboardMarkup) error {
	return paced0(g, ctx, chatID, func() error {
		return g.Telegram.EditEphemeralMessageMedia(ctx, chatID, receiverUserID, ephemeralMessageID, media, markup)
	})
}

func (g guarded) EditEphemeralMessageReplyMarkup(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, markup *tg.InlineKeyboardMarkup) error {
	return paced0(g, ctx, chatID, func() error {
		return g.Telegram.EditEphemeralMessageReplyMarkup(ctx, chatID, receiverUserID, ephemeralMessageID, markup)
	})
}

func (g guarded) SendRich(ctx context.Context, chatID int64, html string, opts tg.RichOptions) (tg.Message, error) {
	return paced(g, ctx, chatID, func() (tg.Message, error) { return g.Telegram.SendRich(ctx, chatID, html, opts) })
}

func (g guarded) EditMessageRich(ctx context.Context, chatID, messageID int64, html string, media []tg.RichMedia, markup *tg.InlineKeyboardMarkup, opts ...tg.RichOption) error {
	return paced0(g, ctx, chatID, func() error {
		return g.Telegram.EditMessageRich(ctx, chatID, messageID, html, media, markup, opts...)
	})
}

func (g guarded) DeleteEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64) error {
	return paced0(g, ctx, chatID, func() error {
		return g.Telegram.DeleteEphemeralMessage(ctx, chatID, receiverUserID, ephemeralMessageID)
	})
}

func (g guarded) SendChatAction(ctx context.Context, chatID int64, threadID int, action string) error {
	return paced0(g, ctx, chatID, func() error { return g.Telegram.SendChatAction(ctx, chatID, threadID, action) })
}
