// SPDX-License-Identifier: Apache-2.0
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultTelegramAPIBase is Telegram's own server, enough for local work.
// Production points TELEGRAM_API_BASE at the family's telegram-bot-api in
// --local mode, which lifts the 20 MB download and 50 MB upload caps.
const DefaultTelegramAPIBase = "https://api.telegram.org"

// DefaultOpenRouterAPIBase is where the catalog sync reads OpenRouter's
// public catalogs.
const DefaultOpenRouterAPIBase = "https://openrouter.ai/api/v1"

type Config struct {
	TelegramBotToken string
	TelegramAPIBase  string
	DatabaseURL      string
	AutoMigrate      bool
	HTTPAddr         string
	// MetricsAddr serves /metrics apart from HTTPAddr, which the tunnel makes
	// public with the site. Empty turns it off.
	MetricsAddr string
	LogLevel    string
	// OpenRouterAPIBase and CatalogEvery drive the catalog sync; a zero
	// CatalogEvery turns it off.
	OpenRouterAPIBase string
	CatalogEvery      time.Duration
	// OpenRouterKey is framy's own key, which pays for the free allowance;
	// empty, the free allowance cannot run.
	OpenRouterKey string
	// JobsStopGrace is how long a stop waits for runs in progress before it
	// cuts them short. The container's stop_grace_period has to be longer.
	JobsStopGrace time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		TelegramAPIBase: strings.TrimRight(valueOrDefault("TELEGRAM_API_BASE", DefaultTelegramAPIBase), "/"),
		HTTPAddr:        valueOrDefault("HTTP_ADDR", ":8080"),
		MetricsAddr:     valueOrDefault("METRICS_ADDR", ":9090"),
		LogLevel:        valueOrDefault("LOG_LEVEL", "info"),

		OpenRouterAPIBase: strings.TrimRight(valueOrDefault("OPENROUTER_API_BASE", DefaultOpenRouterAPIBase), "/"),
	}
	var err error
	if cfg.TelegramBotToken, err = secret("TELEGRAM_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL, err = secret("DATABASE_URL"); err != nil {
		return Config{}, err
	}
	if cfg.OpenRouterKey, err = secret("OPENROUTER_API_KEY"); err != nil {
		return Config{}, err
	}
	if cfg.AutoMigrate, err = strconv.ParseBool(valueOrDefault("AUTO_MIGRATE", "true")); err != nil {
		return Config{}, fmt.Errorf("AUTO_MIGRATE must be a boolean: %w", err)
	}
	if !strings.HasPrefix(cfg.TelegramAPIBase, "http://") && !strings.HasPrefix(cfg.TelegramAPIBase, "https://") {
		return Config{}, fmt.Errorf("TELEGRAM_API_BASE must be an http or https URL")
	}
	if !strings.HasPrefix(cfg.OpenRouterAPIBase, "https://") && !strings.HasPrefix(cfg.OpenRouterAPIBase, "http://") {
		return Config{}, fmt.Errorf("OPENROUTER_API_BASE must be an http or https URL")
	}
	if every := valueOrDefault("CATALOG_SYNC_INTERVAL", "1h"); every != "off" {
		if cfg.CatalogEvery, err = time.ParseDuration(every); err != nil || cfg.CatalogEvery < time.Minute {
			return Config{}, fmt.Errorf("CATALOG_SYNC_INTERVAL must be a duration of at least 1m, or off")
		}
	}
	if cfg.JobsStopGrace, err = time.ParseDuration(valueOrDefault("JOBS_STOP_GRACE", "3m30s")); err != nil || cfg.JobsStopGrace < time.Second {
		return Config{}, fmt.Errorf("JOBS_STOP_GRACE must be a duration of at least 1s")
	}
	if cfg.MetricsAddr == "off" {
		cfg.MetricsAddr = ""
	}
	if cfg.MetricsAddr != "" && cfg.MetricsAddr == cfg.HTTPAddr {
		return Config{}, fmt.Errorf("METRICS_ADDR must differ from HTTP_ADDR: the metrics stay off the public listener")
	}
	if cfg.TelegramBotToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN or TELEGRAM_BOT_TOKEN_FILE is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL or DATABASE_URL_FILE is required")
	}
	return cfg, nil
}

// secret reads NAME, or the file NAME_FILE points at, as Docker secrets do.
// Both set is an error: which one wins would be a guess. The error never
// carries the value.
func secret(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	path := strings.TrimSpace(os.Getenv(name + "_FILE"))
	switch {
	case value != "" && path != "":
		return "", fmt.Errorf("set %s or %s_FILE, not both", name, name)
	case path != "":
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", name, err)
		}
		value = strings.TrimSpace(string(b))
		if value == "" {
			return "", fmt.Errorf("%s_FILE %s is empty", name, path)
		}
	}
	return value, nil
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
