// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// The settings say how long a sign-in lasts, as a number in every language:
// a change to SessionLife changes that line too.
func TestTheSiteSaysHowLongASignInLasts(t *testing.T) {
	days := strconv.Itoa(int(SessionLife / (24 * time.Hour)))
	for _, lang := range i18n.Codes() {
		if s := i18n.T(lang, "web.prefs.signed_sub"); !strings.Contains(s, days) {
			t.Errorf("%s: %q does not say %s days", lang, s, days)
		}
	}
}
