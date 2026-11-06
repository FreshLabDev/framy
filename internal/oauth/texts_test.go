// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// An expired connection says how long one lasts, as a number in every
// language: a change to FlowLifetime changes that line too.
func TestTheSiteSaysHowLongAConnectionWaits(t *testing.T) {
	minutes := strconv.Itoa(int(FlowLifetime / time.Minute))
	for _, lang := range i18n.Codes() {
		if s := i18n.T(lang, "web.oauth.expired"); !strings.Contains(s, minutes) {
			t.Errorf("%s: %q does not say %s minutes", lang, s, minutes)
		}
	}
}
