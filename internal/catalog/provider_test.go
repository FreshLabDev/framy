// SPDX-License-Identifier: Apache-2.0
package catalog

import "testing"

// One name a provider, wherever a person meets it: Google's connection is
// AI Studio on the site, so it is AI Studio in the bot and the chat too.
func TestProviderName(t *testing.T) {
	for p, want := range map[string]string{OpenRouter: "OpenRouter", OpenAI: "OpenAI", Gemini: "AI Studio", "replicate": "replicate"} {
		if got := ProviderName(p); got != want {
			t.Errorf("ProviderName(%q) = %q, want %q", p, got, want)
		}
	}
}
