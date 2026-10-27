// SPDX-License-Identifier: Apache-2.0
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setBase(t *testing.T) {
	t.Helper()
	for _, k := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN_FILE", "DATABASE_URL", "DATABASE_URL_FILE", "TELEGRAM_API_BASE", "AUTO_MIGRATE", "HTTP_ADDR", "METRICS_ADDR", "LOG_LEVEL", "OPENROUTER_API_BASE", "CATALOG_SYNC_INTERVAL", "JOBS_STOP_GRACE"} {
		t.Setenv(k, "")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("DATABASE_URL", "postgres://framy@localhost/framy")
}

func TestLoadDefaults(t *testing.T) {
	setBase(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramAPIBase != DefaultTelegramAPIBase || cfg.HTTPAddr != ":8080" || cfg.MetricsAddr != ":9090" || !cfg.AutoMigrate || cfg.LogLevel != "info" ||
		cfg.OpenRouterAPIBase != DefaultOpenRouterAPIBase || cfg.CatalogEvery != time.Hour || cfg.JobsStopGrace != 210*time.Second {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadReadsSecretsFromFiles(t *testing.T) {
	setBase(t)
	dir := t.TempDir()
	token := filepath.Join(dir, "bot-token")
	if err := os.WriteFile(token, []byte("456:def\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", token)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramBotToken != "456:def" {
		t.Fatal("token from file was not read and trimmed")
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"no token", map[string]string{"TELEGRAM_BOT_TOKEN": ""}},
		{"no database", map[string]string{"DATABASE_URL": ""}},
		{"value and file", map[string]string{"TELEGRAM_BOT_TOKEN_FILE": empty}},
		{"empty file", map[string]string{"TELEGRAM_BOT_TOKEN": "", "TELEGRAM_BOT_TOKEN_FILE": empty}},
		{"missing file", map[string]string{"DATABASE_URL": "", "DATABASE_URL_FILE": filepath.Join(dir, "nope")}},
		{"bad api base", map[string]string{"TELEGRAM_API_BASE": "api.telegram.org"}},
		{"bad auto migrate", map[string]string{"AUTO_MIGRATE": "sometimes"}},
		{"metrics on the public listener", map[string]string{"HTTP_ADDR": ":8080", "METRICS_ADDR": ":8080"}},
		{"bad openrouter base", map[string]string{"OPENROUTER_API_BASE": "openrouter.ai"}},
		{"bad catalog interval", map[string]string{"CATALOG_SYNC_INTERVAL": "hourly"}},
		{"catalog interval too short", map[string]string{"CATALOG_SYNC_INTERVAL": "5s"}},
		{"bad stop grace", map[string]string{"JOBS_STOP_GRACE": "soon"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setBase(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestSecretErrorsDoNotLeakTheValue(t *testing.T) {
	setBase(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "999:very-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "/nonexistent")
	_, err := Load()
	if err == nil || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestMetricsCanBeTurnedOff(t *testing.T) {
	setBase(t)
	t.Setenv("METRICS_ADDR", "off")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsAddr != "" {
		t.Fatalf("MetricsAddr = %q", cfg.MetricsAddr)
	}
}

func TestCatalogSyncCanBeTurnedOff(t *testing.T) {
	setBase(t)
	t.Setenv("CATALOG_SYNC_INTERVAL", "off")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CatalogEvery != 0 {
		t.Fatalf("CatalogEvery = %v", cfg.CatalogEvery)
	}
}
