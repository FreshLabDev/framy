// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/i18n"
)

// inlineAnswer asks the flow what q asks and waits for the one answer.
func (h *harness) inlineAnswer(t *testing.T, q tg.InlineQuery) tg.InlineAnswer {
	t.Helper()
	h.tg.inlineAnswers = nil
	h.flow.Inline(context.Background(), &q)
	h.flow.Wait()
	if len(h.tg.inlineAnswers) != 1 {
		t.Fatalf("answers = %+v", h.tg.inlineAnswers)
	}
	if a := h.tg.methods("answerInlineQuery"); len(a) == 0 || a[len(a)-1].text != q.ID {
		t.Fatalf("answered %+v", a)
	}
	return h.tg.inlineAnswers[0]
}

func TestAnInlineQueryListsThePersonsResults(t *testing.T) {
	h := newHarness(t)
	h.flow.cfg.AppURL = "https://framy.test"
	h.store.inline = []db.InlineResult{
		{JobID: 9, Prompt: "Fox in the rain", Model: "Veo 3.1", FileID: "video-id", FileType: db.FileVideo},
		{JobID: 8, Prompt: "a red <fox>", Model: "Nano Banana", FileID: "photo-id", FileType: db.FilePhoto},
	}
	got := h.inlineAnswer(t, tg.InlineQuery{ID: "q", From: tg.User{ID: 7, LanguageCode: "en"}, Query: "  fox "})
	if want := (inlineAsk{user: 7, search: "fox", limit: inlinePage}); h.store.inlineAsked != want {
		t.Fatalf("asked %+v, want %+v", h.store.inlineAsked, want)
	}
	want := tg.InlineAnswer{
		Results: []tg.InlineQueryResult{
			tg.InlineQueryResultCachedVideo{ID: "j9", VideoFileID: "video-id", Title: "Fox in the rain", Description: "Veo 3.1",
				Caption: "<blockquote>Fox in the rain</blockquote>\n<b>Veo 3.1</b>"},
			tg.InlineQueryResultCachedPhoto{ID: "j8", PhotoFileID: "photo-id", Title: "a red <fox>", Description: "Nano Banana",
				Caption: "<blockquote>a red &lt;fox&gt;</blockquote>\n<b>Nano Banana</b>"},
		},
		CacheTime: inlineCache, IsPersonal: true,
		// The bot's chat, not the Mini App: one opened from inline mode
		// gets no initData and could not sign the person in.
		Button: &tg.InlineQueryResultsButton{Text: i18n.T("en", "btn.open"), StartParameter: inlineStart},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("answer =\n%+v\nwant\n%+v", got, want)
	}
}

// A full page says where the next starts, and the next asks for the results
// older than that.
func TestInlineResultsComeAPageAtATime(t *testing.T) {
	h := newHarness(t)
	for id := int64(100); id > 75; id-- {
		h.store.inline = append(h.store.inline, db.InlineResult{JobID: id, Prompt: "a fox", Model: "m", FileID: "f", FileType: db.FilePhoto})
	}
	first := h.inlineAnswer(t, tg.InlineQuery{ID: "q1", From: tg.User{ID: 7}})
	if len(first.Results) != inlinePage || first.NextOffset != "81" {
		t.Fatalf("first page: %d results, next %q", len(first.Results), first.NextOffset)
	}
	second := h.inlineAnswer(t, tg.InlineQuery{ID: "q2", From: tg.User{ID: 7}, Offset: first.NextOffset})
	if h.store.inlineAsked.before != 81 || len(second.Results) != 5 || second.NextOffset != "" {
		t.Fatalf("second page: asked %+v, %d results, next %q", h.store.inlineAsked, len(second.Results), second.NextOffset)
	}
	if second.Button == nil || second.Button.Text != i18n.T("en", "btn.open") {
		t.Fatalf("button = %+v", second.Button)
	}
	// An offset framy never gave is the end, and the database is not asked.
	h.store.inlineAsked = inlineAsk{}
	for _, offset := range []string{"x", "-3", "0"} {
		end := h.inlineAnswer(t, tg.InlineQuery{ID: "q3", From: tg.User{ID: 7}, Offset: offset})
		if len(end.Results) != 0 || end.NextOffset != "" || !end.IsPersonal || h.store.inlineAsked != (inlineAsk{}) {
			t.Fatalf("offset %q: %+v, asked %+v", offset, end, h.store.inlineAsked)
		}
	}
}

// With nothing found the button says so, in the person's language.
func TestAnInlineQueryWithNothingFound(t *testing.T) {
	h := newHarness(t)
	h.store.userLang[7] = "ru"
	got := h.inlineAnswer(t, tg.InlineQuery{ID: "q", From: tg.User{ID: 7, LanguageCode: "en"}, Query: "wolf"})
	want := &tg.InlineQueryResultsButton{Text: i18n.T("ru", "inline.empty"), StartParameter: inlineStart}
	if len(got.Results) != 0 || !reflect.DeepEqual(got.Button, want) {
		t.Fatalf("answer = %+v, button %+v", got, got.Button)
	}
}

// Only https opens as a Mini App.
func TestTheInlineButtonOpensOnlyHTTPS(t *testing.T) {
	for site, want := range map[string]string{
		"https://framy.app":     "https://framy.app",
		"http://localhost:8080": "",
		"framy.app":             "",
		"":                      "",
	} {
		if got := miniAppURL(site); got != want {
			t.Errorf("miniAppURL(%q) = %q, want %q", site, got, want)
		}
	}
}

// A title is the prompt on one line, cut; a result with no prompt has the
// model's name, since Telegram wants a video's title.
func TestAnInlineResultsTitle(t *testing.T) {
	long := strings.Repeat("лиса ", 30)
	r := inlineResult(db.InlineResult{JobID: 1, Prompt: "a fox\n\nin   the snow", Model: "m", FileID: "f", FileType: db.FileVideo}).(tg.InlineQueryResultCachedVideo)
	if r.Title != "a fox in the snow" {
		t.Errorf("title = %q", r.Title)
	}
	r = inlineResult(db.InlineResult{JobID: 1, Prompt: long, Model: "m", FileID: "f", FileType: db.FileVideo}).(tg.InlineQueryResultCachedVideo)
	if n := utf8.RuneCountInString(r.Title); n > inlineTitle+1 || !strings.HasSuffix(r.Title, "…") {
		t.Errorf("title of %d runes: %q", n, r.Title)
	}
	r = inlineResult(db.InlineResult{JobID: 1, Prompt: " ", Model: "Veo 3.1", FileID: "f", FileType: db.FileVideo}).(tg.InlineQueryResultCachedVideo)
	if r.Title != "Veo 3.1" {
		t.Errorf("title = %q", r.Title)
	}
	if id := inlineResult(db.InlineResult{JobID: 1234567, FileType: db.FilePhoto}).(tg.InlineQueryResultCachedPhoto).ID; id != "j"+strconv.Itoa(1234567) {
		t.Errorf("id = %q", id)
	}
}
