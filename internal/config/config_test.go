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
	for _, k := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN_FILE", "DATABASE_URL", "DATABASE_URL_FILE", "TELEGRAM_API_BASE", "AUTO_MIGRATE", "HTTP_ADDR", "METRICS_ADDR", "LOG_LEVEL", "OPENROUTER_API_BASE", "CATALOG_SYNC_INTERVAL", "FX_SYNC", "JOBS_STOP_GRACE", "OPENROUTER_API_KEY", "OPENROUTER_API_KEY_FILE",
		"R2_ACCESS_KEY_ID", "R2_ACCESS_KEY_ID_FILE", "R2_SECRET_ACCESS_KEY", "R2_SECRET_ACCESS_KEY_FILE", "R2_ENDPOINT", "R2_ACCOUNT_ID", "R2_BUCKET",
		"WORK_DIR", "FFMPEG", "FFPROBE", "OPERATOR_CHAT_ID"} {
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
		cfg.OpenRouterAPIBase != DefaultOpenRouterAPIBase || cfg.CatalogEvery != time.Hour || cfg.JobsStopGrace != 210*time.Second ||
		cfg.OpenRouterKey != "" || cfg.R2.Enabled() || cfg.R2.Bucket != "framy" || cfg.FFmpeg != "ffmpeg" || cfg.FFprobe != "ffprobe" ||
		cfg.WorkDir != filepath.Join(os.TempDir(), "framy") || cfg.OperatorChatID != 0 || !cfg.FXSync {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadR2(t *testing.T) {
	setBase(t)
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "r2-secret")
	if err := os.WriteFile(secretFile, []byte("the-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("R2_ACCESS_KEY_ID", "key-id")
	t.Setenv("R2_SECRET_ACCESS_KEY_FILE", secretFile)
	t.Setenv("R2_ACCOUNT_ID", "abc123")
	t.Setenv("OPERATOR_CHAT_ID", "-1001234567890")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := R2{Endpoint: "https://abc123.r2.cloudflarestorage.com", Bucket: "framy", AccessKeyID: "key-id", SecretAccessKey: "the-secret"}
	if cfg.R2 != want || !cfg.R2.Enabled() || cfg.OperatorChatID != -1001234567890 {
		t.Fatalf("r2 = %+v, operator %d", cfg.R2, cfg.OperatorChatID)
	}
	for name, env := range map[string][2]string{
		"both endpoints":      {"R2_ENDPOINT", "https://x.r2.cloudflarestorage.com"},
		"half a token":        {"R2_SECRET_ACCESS_KEY_FILE", ""},
		"a bad operator chat": {"OPERATOR_CHAT_ID", "the-ops-chat"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			_, err := Load()
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), "the-secret") {
				t.Error("the error names the secret")
			}
		})
	}
	t.Setenv("R2_ACCOUNT_ID", "")
	if _, err := Load(); err == nil {
		t.Error("a token without an endpoint was accepted")
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

	key := filepath.Join(dir, "openrouter-key")
	if err := os.WriteFile(key, []byte("sk-or-v1-abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY_FILE", key)
	if cfg, err = Load(); err != nil || cfg.OpenRouterKey != "sk-or-v1-abc" {
		t.Fatalf("framy's OpenRouter key from a file: %v", err)
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
		{"bad fx sync", map[string]string{"FX_SYNC": "daily"}},
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

func TestFXSyncCanBeTurnedOff(t *testing.T) {
	setBase(t)
	t.Setenv("FX_SYNC", "off")
	cfg, err := Load()
	if err != nil || cfg.FXSync {
		t.Fatalf("FX_SYNC=off: %+v, %v", cfg.FXSync, err)
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
