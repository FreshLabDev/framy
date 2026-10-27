// SPDX-License-Identifier: Apache-2.0
package config

import (
	"fmt"
	"strings"
)

// Hooks is how the providers' webhooks reach framy: the public address they
// are told, and the secrets that keep a stranger from posting to it.
type Hooks struct {
	// PublicURL is the address the webhooks are told: SITE_URL. OpenRouter
	// takes a callback_url over HTTPS only, so with webhooks on it is https.
	PublicURL string
	// PathSecret keys the MAC at the end of every webhook path
	// (internal/providers/openrouter, Callbacks). Without it no request
	// carries a callback_url, and the 30 s poll finishes every video.
	PathSecret string
	// OpenRouterSecret is the signing secret of the OpenRouter workspace
	// framy's own key belongs to; it checks X-OpenRouter-Signature on the
	// webhooks of free runs. A person's key belongs to their own workspace, so
	// theirs are not checked.
	OpenRouterSecret string
}

// Enabled: requests carry a callback_url and the hook accepts paths.
func (h Hooks) Enabled() bool { return h.PublicURL != "" && h.PathSecret != "" }

// minPathSecret is the shortest HOOK_PATH_SECRET: 32 random bytes in base64
// are 44 characters.
const minPathSecret = 32

// loadHooks reads HOOK_PATH_SECRET and OPENROUTER_WEBHOOK_SECRET (each also
// as _FILE); the address is site, SITE_URL as loadSite checked it. A setting
// that cannot work is refused at start rather than found out when a video is
// paid. No error names a secret.
func loadHooks(site string) (Hooks, error) {
	h := Hooks{PublicURL: site}
	var err error
	if h.PathSecret, err = secret("HOOK_PATH_SECRET"); err != nil {
		return Hooks{}, err
	}
	if h.OpenRouterSecret, err = secret("OPENROUTER_WEBHOOK_SECRET"); err != nil {
		return Hooks{}, err
	}
	switch {
	case h.PathSecret != "" && len(h.PathSecret) < minPathSecret:
		return Hooks{}, fmt.Errorf("HOOK_PATH_SECRET must be at least %d characters; make one with: head -c 32 /dev/urandom | base64", minPathSecret)
	case h.PathSecret != "" && site == "":
		return Hooks{}, fmt.Errorf("HOOK_PATH_SECRET needs SITE_URL: the address the webhooks are told")
	case h.PathSecret != "" && !strings.HasPrefix(site, "https://"):
		return Hooks{}, fmt.Errorf("HOOK_PATH_SECRET needs an https SITE_URL: OpenRouter posts webhooks over HTTPS only")
	case h.OpenRouterSecret != "" && h.PathSecret == "":
		return Hooks{}, fmt.Errorf("OPENROUTER_WEBHOOK_SECRET needs HOOK_PATH_SECRET and SITE_URL: without a callback no webhook arrives")
	}
	return h, nil
}
