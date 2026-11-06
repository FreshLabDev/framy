// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"net/url"
	"slices"
)

// SettingsURL is the site at site with its settings open at provider's key,
// where a person connects one or replaces one the provider refused: the site
// opens its settings when the address has ?settings=<provider> (main.js).
// A provider framy does not know opens the settings at the top. It is site
// itself when site is not an address.
func SettingsURL(site, provider string) string {
	u, err := url.Parse(site)
	if err != nil || u.Host == "" {
		return site
	}
	if !slices.Contains(Providers, provider) {
		provider = ""
	}
	if u.Path == "" {
		u.Path = "/"
	}
	q := u.Query()
	q.Set("settings", provider)
	u.RawQuery = q.Encode()
	return u.String()
}
