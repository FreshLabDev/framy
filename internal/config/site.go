// SPDX-License-Identifier: Apache-2.0
package config

import (
	"errors"
	"net/url"
	"strings"
)

// TelegramLogin is the Telegram Login client that BotFather gives the site
// (Mini App settings, Login Widget): the id and the secret of an OpenID
// Connect client at oauth.telegram.org. Without it the site cannot sign
// anyone in, and the rest of the service runs.
type TelegramLogin struct {
	ClientID     string
	ClientSecret string
}

// Enabled: the client is set.
func (t TelegramLogin) Enabled() bool { return t.ClientID != "" }

// loadSite reads SITE_URL and the Telegram Login client.
func loadSite() (string, TelegramLogin, error) {
	var login TelegramLogin
	var err error
	if login.ClientID, err = secret("TELEGRAM_LOGIN_CLIENT_ID"); err != nil {
		return "", TelegramLogin{}, err
	}
	if login.ClientSecret, err = secret("TELEGRAM_LOGIN_CLIENT_SECRET"); err != nil {
		return "", TelegramLogin{}, err
	}
	var site string
	if raw := valueOrDefault("SITE_URL", ""); raw != "" {
		if site, err = ParseSiteURL(raw); err != nil {
			return "", TelegramLogin{}, errors.New("SITE_URL " + err.Error())
		}
	}
	switch {
	case (login.ClientID == "") != (login.ClientSecret == ""):
		return "", TelegramLogin{}, errors.New("TELEGRAM_LOGIN_CLIENT_ID and TELEGRAM_LOGIN_CLIENT_SECRET go together")
	case login.Enabled() && site == "":
		return "", TelegramLogin{}, errors.New("SITE_URL is required with the Telegram Login client: it makes the redirect URI")
	}
	return site, login, nil
}

// ParseSiteURL checks the site's public origin and returns it without a
// trailing slash. It has to be https, with no path, query or credentials; http
// is accepted for localhost only, where browsers take a Secure cookie.
func ParseSiteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("must be an origin such as https://framy.ws04.cc, with no path")
	}
	switch u.Scheme {
	case "https":
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			return "", errors.New("must be https, except for localhost")
		}
	default:
		return "", errors.New("must be an https URL")
	}
	return u.Scheme + "://" + u.Host, nil
}
