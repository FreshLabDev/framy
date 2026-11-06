// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"regexp"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// TestProviderNames keeps the site's names of the providers (PNAME in
// catalog.js) the names the bot and the chat use, so a person who connected
// AI Studio on the site does not meet Gemini in Telegram.
func TestProviderNames(t *testing.T) {
	src, err := os.ReadFile("app/src/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export const PNAME = \{([^}]*)\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no PNAME in catalog.js")
	}
	site := map[string]string{}
	for _, kv := range regexp.MustCompile(`(\w+): "([^"]*)"`).FindAllSubmatch(m[1], -1) {
		site[string(kv[1])] = string(kv[2])
	}
	for _, p := range []string{catalog.OpenRouter, catalog.OpenAI, catalog.Gemini} {
		if site[p] != catalog.ProviderName(p) {
			t.Errorf("%s: the site says %q, Telegram %q", p, site[p], catalog.ProviderName(p))
		}
	}
	if len(site) != 3 {
		t.Errorf("PNAME names %d providers: %v", len(site), site)
	}
}
