// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
)

const (
	// inlinePage is how many results an inline answer lists; Telegram takes
	// at most 50.
	inlinePage = 20
	// inlineCache is how long Telegram may give the same answer again, in
	// seconds: short, so a result just made is there on the next query.
	inlineCache = 5
	// inlineTitle is how much of the prompt a result's title shows.
	inlineTitle = 64
	// inlineStart is what /start gets from the button when there is no Mini
	// App to open.
	inlineStart = "inline"
)

// Inline answers @framyin_bot typed in any chat: the person's results whose
// prompt holds the query, newest first, to send again by their file_id, and
// a button into the Mini App. It returns at once and answers in the
// background.
func (f *Flow) Inline(ctx context.Context, q *tg.InlineQuery) {
	if q == nil {
		return
	}
	f.wg.Go(func() {
		if err := f.inline(ctx, q); err != nil && ctx.Err() == nil {
			f.log.Error("inline answer failed", "error", err)
		}
	})
}

func (f *Flow) inline(ctx context.Context, q *tg.InlineQuery) error {
	var before int64
	if q.Offset != "" {
		id, err := strconv.ParseInt(q.Offset, 10, 64)
		if err != nil || id <= 0 {
			// Not an offset framy gave: the last page.
			return f.cfg.Telegram.AnswerInlineQuery(ctx, q.ID, tg.InlineAnswer{CacheTime: inlineCache, IsPersonal: true})
		}
		before = id
	}
	results, err := f.cfg.Store.InlineResults(ctx, q.From.ID, strings.TrimSpace(q.Query), before, inlinePage)
	if err != nil {
		return err
	}
	lang := f.personLang(ctx, q.From)
	answer := tg.InlineAnswer{CacheTime: inlineCache, IsPersonal: true, Button: f.inlineButton(lang, before == 0 && len(results) == 0)}
	for _, r := range results {
		answer.Results = append(answer.Results, inlineResult(r))
	}
	if len(results) == inlinePage {
		answer.NextOffset = strconv.FormatInt(results[len(results)-1].JobID, 10)
	}
	return f.cfg.Telegram.AnswerInlineQuery(ctx, q.ID, answer)
}

// inlineButton opens the Mini App, or the bot's chat when there is none.
// It says nothing was found when the answer is empty.
func (f *Flow) inlineButton(lang string, empty bool) *tg.InlineQueryResultsButton {
	b := &tg.InlineQueryResultsButton{Text: t(lang, "btn.open")}
	if empty {
		b.Text = t(lang, "inline.empty")
	}
	if f.cfg.AppURL != "" {
		b.WebApp = &tg.WebAppInfo{URL: f.cfg.AppURL}
	} else {
		b.StartParameter = inlineStart
	}
	return b
}

// inlineResult sends r again with the caption it had in the chat. It has no
// buttons: a press on an inline message comes from a chat framy may not be
// in.
func inlineResult(r db.InlineResult) tg.InlineQueryResult {
	id := "j" + strconv.FormatInt(r.JobID, 10)
	title := cmp.Or(inlineTitleOf(r.Prompt), r.Model)
	caption := resultCaption(r.Prompt, r.Model)
	if r.FileType == db.FileVideo {
		return tg.InlineQueryResultCachedVideo{ID: id, VideoFileID: r.FileID, Title: title, Description: r.Model, Caption: caption}
	}
	return tg.InlineQueryResultCachedPhoto{ID: id, PhotoFileID: r.FileID, Title: title, Description: r.Model, Caption: caption}
}

// inlineTitleOf is the start of the prompt on one line.
func inlineTitleOf(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	if utf8.RuneCountInString(prompt) > inlineTitle {
		prompt = strings.TrimSpace(string([]rune(prompt)[:inlineTitle])) + "…"
	}
	return prompt
}

// miniAppURL is site when Telegram can open it as a Mini App: https only.
func miniAppURL(site string) string {
	u, err := url.Parse(site)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return site
}
