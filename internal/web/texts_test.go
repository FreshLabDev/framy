// SPDX-License-Identifier: Apache-2.0
package web

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/media"
)

// The site says how long the free tier pauses after framy's key ran out, and
// closes it for as long on its side, from its own constant; the server's is
// free.MoneyPause. The longest clip, for an answer that did not say it, is
// media.DefaultMaxSeconds.
func TestTheSiteSaysTheServersNumbers(t *testing.T) {
	src, err := os.ReadFile("app/src/errors.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		want int
	}{
		{"MONEY_PAUSE_MIN", int(free.MoneyPause / time.Minute)},
		{"CLIP_MAX_S", media.DefaultMaxSeconds},
	} {
		m := regexp.MustCompile(`const ` + c.name + ` = (\d+);`).FindSubmatch(src)
		if m == nil {
			t.Errorf("errors.js has no %s", c.name)
			continue
		}
		if got, _ := strconv.Atoi(string(m[1])); got != c.want {
			t.Errorf("errors.js says %s = %d, the server %d", c.name, got, c.want)
		}
	}
}
