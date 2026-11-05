// SPDX-License-Identifier: Apache-2.0
package free

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// The windows of the free allowance are written into the lines that name
// them, as numbers, in every language; the counts come as {limit} or {n},
// except in the bot's description and /help, which spell them out ("Five
// images ... and one video"). A change here changes those lines too.
func TestTheTextsSayTheAllowance(t *testing.T) {
	if ImagesPerWindow != 5 || VideosPerWindow != 1 {
		t.Errorf("bot.description and help.body spell out five images and one video in 16 languages: say %d and %d there", ImagesPerWindow, VideosPerWindow)
	}
	windows := []struct {
		window time.Duration
		unit   time.Duration
		keys   []string
	}{
		{ImageWindow, time.Hour, []string{"run.limit", "run.limit_at", "home.free_images_left", "web.free.per_day", "web.prefs.left_day", "bot.description", "help.body"}},
		{VideoWindow, 24 * time.Hour, []string{"edit.limit", "edit.limit_at", "home.free_videos_left", "web.free.per_week", "web.prefs.left_week", "bot.description", "help.body"}},
	}
	for _, w := range windows {
		n := strconv.Itoa(int(w.window / w.unit))
		for _, key := range w.keys {
			for _, lang := range i18n.Codes() {
				if s := i18n.T(lang, key); !strings.Contains(s, n) {
					t.Errorf("%s in %s does not say the window of %s: %q", key, lang, n, s)
				}
			}
		}
	}
}
