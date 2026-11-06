// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
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
	// inlineStart is what /start gets from the button above the results.
	inlineStart = "inline"
)

// Inline answers @framyin_bot typed in any chat: the person's results whose
// prompt holds the query, newest first, to send again by their file_id, and
// a button into the Mini App. It returns at once and answers in the
// background.
//
// Telegram sends a query for every letter typed and shows only the answer
// to the last, so a person's new query ends the one before it, and a first
// page waits inlineQuiet for the next letter before it reads the history:
// "a fox" typed at speed is one read, not five. At most inlineAtOnce
// answers are read at once, whoever types.
func (f *Flow) Inline(ctx context.Context, q *tg.InlineQuery) {
	if q == nil {
		return
	}
	ctx, done := f.typing.start(ctx, q.From.ID)
	f.wg.Go(func() {
		defer done()
		if q.Offset == "" && f.inlineWait(ctx) != nil {
			return
		}
		select {
		case f.inlineSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-f.inlineSlots }()
		// A select picks at random among what is ready, so a query the
		// next letter ended may have got this far: it reads nothing.
		if ctx.Err() != nil {
			return
		}
		if err := f.inline(ctx, q); err != nil && ctx.Err() == nil {
			f.log.Error("inline answer failed", "error", err)
		}
	})
}

const (
	// inlineQuiet is how long a first page waits for the next letter.
	inlineQuiet = 150 * time.Millisecond
	// inlineAtOnce bounds the inline answers read at once.
	inlineAtOnce = 8
)

// typing is the inline query each person is being answered, which their
// next one ends.
type typing struct {
	mu sync.Mutex
	m  map[int64]*query
}

type query struct{ cancel context.CancelFunc }

// start ends the person's query before this one and gives this one its
// context; done forgets it.
func (t *typing) start(ctx context.Context, user int64) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	q := &query{cancel: cancel}
	t.mu.Lock()
	if t.m == nil {
		t.m = map[int64]*query{}
	}
	if before := t.m[user]; before != nil {
		before.cancel()
	}
	t.m[user] = q
	t.mu.Unlock()
	return ctx, func() {
		cancel()
		t.mu.Lock()
		if t.m[user] == q {
			delete(t.m, user)
		}
		t.mu.Unlock()
	}
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

// inlineButton opens the bot's chat with /start inline, whose panel opens
// the Mini App. It does not open the Mini App itself: Telegram gives a Mini
// App opened from inline mode no initData, so it could not sign the person
// in and would ask them to sign in with Telegram inside Telegram. It says
// nothing was found when the answer is empty.
func (f *Flow) inlineButton(lang string, empty bool) *tg.InlineQueryResultsButton {
	b := &tg.InlineQueryResultsButton{Text: t(lang, "btn.open"), StartParameter: inlineStart}
	if empty {
		b.Text = t(lang, "inline.empty")
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
