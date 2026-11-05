// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/price"
)

// Google's connection has the site's name in the chat: on the card, on the
// line under a preview and on a model that needs the key, and the English
// line reads with it.
func TestProviderNameInChat(t *testing.T) {
	d := draft{pay: payKey, offer: catalog.Offer{Provider: catalog.Gemini, Kind: catalog.Image}, estimate: price.Estimate{Kind: price.About, USD: 0.04}}
	if got := previewCard("en", d).Label; got != "your AI Studio key" {
		t.Errorf("card: %q", got)
	}
	if got := priceLine("en", d.pay, d.offer.Provider, d.usage, d.estimate, d.quote); got != "about $0.04 · your AI Studio key" {
		t.Errorf("line: %q", got)
	}
	if got := i18n.T("en", "run.need_key", "provider", catalog.ProviderName(catalog.Gemini)); got != "Needs a key for AI Studio" {
		t.Errorf("a model that needs the key: %q", got)
	}
}
