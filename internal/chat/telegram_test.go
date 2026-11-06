// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
)

// Telegram refuses an answer to a press over 200 characters, and the person
// is left with a button that stopped spinning and said nothing. Every
// answer the flow gives is checked here in every language, with the longest
// values its placeholders take: the wait of a free limit in each of its
// forms, every provider's name, two-digit counts.
func TestAnswersFitTelegram(tt *testing.T) {
	h := newHarness(tt)
	waits := []time.Duration{time.Minute, 59 * time.Minute, 61 * time.Minute, 23*time.Hour + 59*time.Minute,
		24 * time.Hour, 6*24*time.Hour + 23*time.Hour, 7 * 24 * time.Hour}
	placeholder := regexp.MustCompile(`\{\w+\}`)
	for _, lang := range i18n.Codes() {
		var answers []string
		for _, w := range waits {
			answers = append(answers,
				h.flow.limitText(lang, &free.LimitError{Kind: catalog.Image, Limit: free.ImagesPerWindow, FreesAt: h.now.Add(w)}),
				h.flow.limitText(lang, &free.LimitError{Kind: catalog.Video, Limit: free.VideosPerWindow, FreesAt: h.now.Add(w)}))
		}
		answers = append(answers, closedText(lang, catalog.Image), closedText(lang, catalog.Video))
		for _, p := range []string{catalog.OpenRouter, catalog.OpenAI, catalog.Gemini} {
			name := catalog.ProviderName(p)
			answers = append(answers, t(lang, "run.need_key", "provider", name), t(lang, "run.stopped_key", "provider", name))
		}
		for _, key := range []string{"run.not_yours", "run.expired", "run.stopped_image", "run.stopped_video", "result.gone", "dm.expired",
			"panel.key_needed", "panel.free_fixed", "edit.taking",
			"web.misfit.needs_clip", "web.misfit.needs_photo", "web.misfit.no_photos", "web.misfit.max_photos"} {
			answers = append(answers, placeholder.ReplaceAllString(t(lang, key), "99"))
		}
		for _, a := range answers {
			if strings.Contains(a, "[") || strings.Contains(a, "{") {
				tt.Errorf("[%s] an answer is not filled in: %q", lang, a)
			}
			if n := utf16Len(a); n > maxAnswer {
				tt.Errorf("[%s] an answer is %d characters, over Telegram's %d: %q", lang, n, maxAnswer, a)
			}
		}
	}
}

// An answer that is still too long is cut to fit rather than refused.
func TestALongAnswerIsCutToFit(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("ä", 150) + strings.Repeat("😀", 60)
	if err := h.flow.cfg.Telegram.AnswerCallbackQueryAlert(context.Background(), "cq", long); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimPrefix(h.tg.answers[0], "alert:")
	if n := utf16Len(got); n > maxAnswer || !strings.HasSuffix(got, "…") || !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Fatalf("cut to %d units: %q", n, got)
	}
	for _, text := range []string{"", "Done.", strings.Repeat("a", maxAnswer)} {
		if got := fitAnswer(text); got != text {
			t.Errorf("%q, which fits, became %q", text, got)
		}
	}
}
