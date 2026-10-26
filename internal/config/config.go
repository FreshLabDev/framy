// SPDX-License-Identifier: Apache-2.0
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultTelegramAPIBase is Telegram's own server, enough for local work.
// Production points TELEGRAM_API_BASE at the family's telegram-bot-api in
// --local mode, which lifts the 20 MB download and 50 MB upload caps.
const DefaultTelegramAPIBase = "https://api.telegram.org"

type Config struct {
	TelegramBotToken string
	TelegramAPIBase  string
	DatabaseURL      string
	AutoMigrate      bool
	HTTPAddr         string
	LogLevel         string
}

func Load() (Config, error) {
	cfg := Config{
		TelegramAPIBase: strings.TrimRight(valueOrDefault("TELEGRAM_API_BASE", DefaultTelegramAPIBase), "/"),
		HTTPAddr:        valueOrDefault("HTTP_ADDR", ":8080"),
		LogLevel:        valueOrDefault("LOG_LEVEL", "info"),
	}
	var err error
	if cfg.TelegramBotToken, err = secret("TELEGRAM_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL, err = secret("DATABASE_URL"); err != nil {
		return Config{}, err
	}
	if cfg.AutoMigrate, err = strconv.ParseBool(valueOrDefault("AUTO_MIGRATE", "true")); err != nil {
		return Config{}, fmt.Errorf("AUTO_MIGRATE must be a boolean: %w", err)
	}
	if !strings.HasPrefix(cfg.TelegramAPIBase, "http://") && !strings.HasPrefix(cfg.TelegramAPIBase, "https://") {
		return Config{}, fmt.Errorf("TELEGRAM_API_BASE must be an http or https URL")
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
