// SPDX-License-Identifier: Apache-2.0
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hooksEnv starts a test with no webhook setting in the environment, on top of
// a config that loads.
func hooksEnv(t *testing.T) {
	t.Helper()
	setBase(t)
	for _, k := range []string{"SITE_URL", "HOOK_PATH_SECRET", "HOOK_PATH_SECRET_FILE", "OPENROUTER_WEBHOOK_SECRET", "OPENROUTER_WEBHOOK_SECRET_FILE"} {
		t.Setenv(k, "")
	}
}

const (
	pathSecret = "k3v9Xq2mT8wZ5nB7cD4fG6hJ1lP0sRuVyAaEeIiOo=="
	hookSecret = "whsec_the-webhook-secret-of-the-workspace"
)

func TestWebhooksAreOffByDefault(t *testing.T) {
	hooksEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hooks != (Hooks{}) || cfg.Hooks.Enabled() {
		t.Fatalf("hooks = %+v", cfg.Hooks)
	}
}

func TestWebhooksNeedTheSiteURLAndThePathSecret(t *testing.T) {
	hooksEnv(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "hook-path-secret")
	if err := os.WriteFile(file, []byte(pathSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SITE_URL", "https://framy.example/")
	t.Setenv("HOOK_PATH_SECRET_FILE", file)
	t.Setenv("OPENROUTER_WEBHOOK_SECRET", hookSecret)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Hooks{PublicURL: "https://framy.example", PathSecret: pathSecret, OpenRouterSecret: hookSecret}
	if cfg.Hooks != want || !cfg.Hooks.Enabled() {
		t.Fatalf("hooks = %+v, want %+v", cfg.Hooks, want)
	}
}

// With the site's address and no secret the site is up and the callbacks
// are off: the poll finishes every video. An http address on a laptop is
// fine as long as no webhook is asked for.
func TestASiteURLAloneTurnsNoCallbackOn(t *testing.T) {
	hooksEnv(t)
	t.Setenv("SITE_URL", "http://localhost:8080")
	if cfg, err := Load(); err != nil || cfg.Hooks.Enabled() {
		t.Fatalf("hooks = %+v, %v", cfg.Hooks, err)
	}
	t.Setenv("SITE_URL", "https://framy.example")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hooks.PublicURL != "https://framy.example" || cfg.Hooks.Enabled() {
		t.Fatalf("hooks = %+v", cfg.Hooks)
	}
}

func TestWebhookSettingsThatCannotWorkAreRefusedAtStart(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"a path secret with an http address":   {map[string]string{"SITE_URL": "http://localhost:8080", "HOOK_PATH_SECRET": pathSecret}, "https SITE_URL"},
		"a path secret that is short":          {map[string]string{"SITE_URL": "https://framy.example", "HOOK_PATH_SECRET": "hunter2"}, "at least 32"},
		"a path secret with no address":        {map[string]string{"HOOK_PATH_SECRET": pathSecret}, "needs SITE_URL"},
		"a signing secret with no path secret": {map[string]string{"SITE_URL": "https://framy.example", "OPENROUTER_WEBHOOK_SECRET": hookSecret}, "needs HOOK_PATH_SECRET"},
	} {
		t.Run(name, func(t *testing.T) {
			hooksEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
			for _, secret := range []string{"hunter2", pathSecret, hookSecret} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error names a secret: %q", err)
				}
			}
		})
	}
}

func TestAWebhookSecretFromAFileThatIsMissingIsRefused(t *testing.T) {
	hooksEnv(t)
	t.Setenv("SITE_URL", "https://framy.example")
	t.Setenv("HOOK_PATH_SECRET_FILE", filepath.Join(t.TempDir(), "nothing"))
	if _, err := Load(); err == nil {
		t.Fatal("accepted")
	}
}
