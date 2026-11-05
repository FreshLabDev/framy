// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"unicode/utf16"
)

// maxAnswer is the longest answer to a press Telegram takes, an alert or a
// toast: 200 characters, counted as UTF-16 code units as Telegram counts
// text. A longer one is refused with a 400, and the person gets nothing at
// all: the button only stops spinning.
const maxAnswer = 200

// guarded is the Telegram the flow calls: it keeps what framy sends within
// what Telegram takes. The answers to presses are written to fit
// (TestAnswersFitTelegram); one that still does not, a new translation say,
// is cut to fit with an ellipsis rather than lost.
type guarded struct{ Telegram }

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
