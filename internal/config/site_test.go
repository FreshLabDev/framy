// SPDX-License-Identifier: Apache-2.0
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setSite(t *testing.T) {
	t.Helper()
	setBase(t)
	for _, k := range []string{"SITE_URL", "TELEGRAM_LOGIN_CLIENT_ID", "TELEGRAM_LOGIN_CLIENT_SECRET", "TELEGRAM_LOGIN_CLIENT_ID_FILE", "TELEGRAM_LOGIN_CLIENT_SECRET_FILE"} {
		t.Setenv(k, "")
	}
}

func TestLoadSiteDefaultsToNothing(t *testing.T) {
	setSite(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SiteURL != "" || cfg.TelegramLogin.Enabled() {
		t.Fatalf("site %q, login %+v", cfg.SiteURL, cfg.TelegramLogin)
	}
}

func TestLoadTelegramLogin(t *testing.T) {
	setSite(t)
	secretFile := filepath.Join(t.TempDir(), "login-secret")
	if err := os.WriteFile(secretFile, []byte("the-client-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_LOGIN_CLIENT_ID", "1234567890")
	t.Setenv("TELEGRAM_LOGIN_CLIENT_SECRET_FILE", secretFile)
	t.Setenv("SITE_URL", "https://framy.ws04.cc/")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SiteURL != "https://framy.ws04.cc" || cfg.TelegramLogin != (TelegramLogin{ClientID: "1234567890", ClientSecret: "the-client-secret"}) || !cfg.TelegramLogin.Enabled() {
		t.Fatalf("site %q, login %+v", cfg.SiteURL, cfg.TelegramLogin)
	}

	// The site alone is fine: other features use it.
	t.Setenv("TELEGRAM_LOGIN_CLIENT_ID", "")
	t.Setenv("TELEGRAM_LOGIN_CLIENT_SECRET_FILE", "")
	if cfg, err = Load(); err != nil || cfg.SiteURL != "https://framy.ws04.cc" || cfg.TelegramLogin.Enabled() {
		t.Fatalf("site alone: %q %+v %v", cfg.SiteURL, cfg.TelegramLogin, err)
	}
}

func TestLoadTelegramLoginRefusals(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"an id without a secret":     {"TELEGRAM_LOGIN_CLIENT_ID": "1", "SITE_URL": "https://framy.ws04.cc"},
		"a secret without an id":     {"TELEGRAM_LOGIN_CLIENT_SECRET": "s3cret-value", "SITE_URL": "https://framy.ws04.cc"},
		"a client without a site":    {"TELEGRAM_LOGIN_CLIENT_ID": "1", "TELEGRAM_LOGIN_CLIENT_SECRET": "s3cret-value"},
		"a site over http":           {"SITE_URL": "http://framy.ws04.cc"},
		"a site with a path":         {"SITE_URL": "https://framy.ws04.cc/app"},
		"a site that is not a URL":   {"SITE_URL": "framy.ws04.cc"},
		"the secret twice":           {"TELEGRAM_LOGIN_CLIENT_SECRET": "s3cret-value", "TELEGRAM_LOGIN_CLIENT_SECRET_FILE": "/nonexistent", "TELEGRAM_LOGIN_CLIENT_ID": "1", "SITE_URL": "https://framy.ws04.cc"},
		"a secret file that is gone": {"TELEGRAM_LOGIN_CLIENT_SECRET_FILE": "/nonexistent/secret", "TELEGRAM_LOGIN_CLIENT_ID": "1", "SITE_URL": "https://framy.ws04.cc"},
	} {
		t.Run(name, func(t *testing.T) {
			setSite(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil {
				t.Fatal("Load accepted it")
			}
			if strings.Contains(err.Error(), "s3cret-value") {
				t.Fatalf("the error holds the secret: %v", err)
			}
		})
	}
}

func TestParseSiteURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://framy.ws04.cc":      "https://framy.ws04.cc",
		" https://framy.ws04.cc/ ":   "https://framy.ws04.cc",
		"https://framy.ws04.cc:8443": "https://framy.ws04.cc:8443",
		"http://localhost:8080":      "http://localhost:8080",
		"http://127.0.0.1:8080/":     "http://127.0.0.1:8080",
		"http://[::1]:8080":          "http://[::1]:8080",
	} {
		if got, err := ParseSiteURL(in); err != nil || got != want {
			t.Errorf("ParseSiteURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "framy.ws04.cc", "ftp://framy.ws04.cc", "http://framy.ws04.cc", "https://framy.ws04.cc/app",
		"https://framy.ws04.cc?x=1", "https://framy.ws04.cc#x", "https://user:pass@framy.ws04.cc", "https://", "//framy.ws04.cc",
	} {
		if got, err := ParseSiteURL(in); err == nil {
			t.Errorf("ParseSiteURL(%q) = %q, want an error", in, got)
		}
	}
}
