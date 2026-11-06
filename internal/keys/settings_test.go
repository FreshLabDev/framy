// SPDX-License-Identifier: Apache-2.0
package keys

import "testing"

func TestSettingsURLOpensTheSettingsAtTheKey(t *testing.T) {
	for _, c := range []struct{ site, provider, want string }{
		{"https://framy.ws04.cc", "openrouter", "https://framy.ws04.cc/?settings=openrouter"},
		{"https://framy.ws04.cc/", "gemini", "https://framy.ws04.cc/?settings=gemini"},
		{"https://framy.ws04.cc/?lang=uk", "openai", "https://framy.ws04.cc/?lang=uk&settings=openai"},
		{"https://framy.ws04.cc", "someone", "https://framy.ws04.cc/?settings="},
		{"", "openrouter", ""},
	} {
		if got := SettingsURL(c.site, c.provider); got != c.want {
			t.Errorf("SettingsURL(%q, %q) = %q, want %q", c.site, c.provider, got, c.want)
		}
	}
}
