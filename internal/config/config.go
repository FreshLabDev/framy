// SPDX-License-Identifier: Apache-2.0
package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
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
	// Stack names the bot this process is when two share the framy schema
	// (FRAMY_STACK; docs/design.md, Stacks): empty for production, "test"
	// for the test bot. Each stack works its own River queues and tells only
	// its own bot's chats; the upkeep both would share runs on production
	// only.
	Stack            string
	TelegramBotToken string
	TelegramAPIBase  string
	// BotAPIFilesDir is where this process sees the media directory of a Bot
	// API server started with --local (BOT_API_FILES_DIR). Such a server
	// answers getFile with an absolute path on its own disk and serves
	// nothing over HTTP, so the bot's own subdirectory, named after its
	// token, is mounted here at the server's path. Required with any server
	// but Telegram's own.
	BotAPIFilesDir string
	DatabaseURL    string
	AutoMigrate    bool
	HTTPAddr       string
	// MetricsAddr serves /metrics apart from HTTPAddr, which the tunnel makes
	// public with the site. Empty turns it off.
	MetricsAddr string
	LogLevel    string
	// OpenRouterAPIBase and CatalogEvery drive the catalog sync; a zero
	// CatalogEvery turns it off.
	OpenRouterAPIBase string
	CatalogEvery      time.Duration
	// FXSync keeps the currency rates (internal/fx); off for work without
	// the network.
	FXSync bool
	// OpenRouterKey is framy's own key, which pays for the free allowance;
	// empty, the free allowance cannot run.
	OpenRouterKey string
	// JobsStopGrace is how long a stop waits for runs in progress before it
	// cuts them short. The container's stop_grace_period has to be longer.
	JobsStopGrace time.Duration
	// R2 is where inputs and results live. Without it no job can keep a
	// result, and each ends as storage before anything is paid.
	R2 R2
	// WorkDir holds the working files of uploads and results.
	WorkDir string
	// AnimCache keeps the pictures every chat run shares: the generating
	// loop, the still of a preview with no photo, the error cards. Nothing
	// in it is a person's; it is drawn again when it is gone.
	AnimCache string
	// FFmpeg and FFprobe are the binaries that normalize photos.
	FFmpeg, FFprobe string
	// OperatorChatID hears about the bucket's size; zero logs instead.
	OperatorChatID int64
	// Web is the public HTTP server's trust in its proxy (web.go).
	Web Web
	// FreeDailyBudgetUSD is the most the free tier may cost in a UTC day
	// (owner, 27 October: $1). Zero closes the free tier.
	FreeDailyBudgetUSD float64
	// KEKFile is the path of the key-encryption key file (internal/keys).
	// Empty: framy keeps no keys of people's, and a run that needs one fails
	// as key. It is a path and never a value: an environment variable shows
	// in docker inspect.
	KEKFile string
	// SiteURL is the site's public origin, https://framy.ws04.cc (SITE_URL).
	// The redirect URI of sign-in and the OAuth callback URL given to
	// OpenRouter are built from it, and so is every other absolute link the
	// service makes. Empty: sign-in on the site and the OpenRouter connect
	// are off.
	SiteURL string
	// TelegramLogin is the client of sign-in on the site (internal/auth).
	TelegramLogin TelegramLogin
	// Hooks is the address and the secrets of the providers' webhooks (hooks.go).
	Hooks Hooks
}

// R2 is the bucket and the token limited to it.
type R2 struct {
	Endpoint string
	Bucket   string
	// Prefix goes before every object key, so a test bot can share the
	// bucket without writing where production does: "test/". Empty for
	// production.
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}

// Enabled: the token is set.
func (r R2) Enabled() bool { return r.AccessKeyID != "" }

// stackPattern keeps a stack's name usable in a River queue's name.
var stackPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// Primary: this process is production, which runs the upkeep of the shared
// tables (the catalog, the rates, the uploads no job took, the thumbnails)
// and tells the operator chat.
func (c Config) Primary() bool { return c.Stack == "" }

func Load() (Config, error) {
	cfg := Config{
		TelegramAPIBase: strings.TrimRight(valueOrDefault("TELEGRAM_API_BASE", DefaultTelegramAPIBase), "/"),
		HTTPAddr:        valueOrDefault("HTTP_ADDR", ":8080"),
		MetricsAddr:     valueOrDefault("METRICS_ADDR", ":9090"),
		LogLevel:        valueOrDefault("LOG_LEVEL", "info"),

		OpenRouterAPIBase: strings.TrimRight(valueOrDefault("OPENROUTER_API_BASE", DefaultOpenRouterAPIBase), "/"),
	}
	cfg.Stack = valueOrDefault("FRAMY_STACK", "")
	if cfg.Stack != "" && !stackPattern.MatchString(cfg.Stack) {
		return Config{}, fmt.Errorf("FRAMY_STACK must be empty for production, or a short name of lowercase letters and digits")
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
	cfg.BotAPIFilesDir = strings.TrimRight(valueOrDefault("BOT_API_FILES_DIR", ""), "/")
	if cfg.TelegramAPIBase != DefaultTelegramAPIBase && cfg.BotAPIFilesDir == "" {
		return Config{}, fmt.Errorf("BOT_API_FILES_DIR is required with a self-hosted TELEGRAM_API_BASE: such a server hands over files by absolute path and serves none over HTTP, so its media directory must be mounted")
	}
	if cfg.BotAPIFilesDir != "" && !filepath.IsAbs(cfg.BotAPIFilesDir) {
		return Config{}, fmt.Errorf("BOT_API_FILES_DIR must be an absolute path: the server's own, as it names files")
	}
	if !strings.HasPrefix(cfg.OpenRouterAPIBase, "https://") && !strings.HasPrefix(cfg.OpenRouterAPIBase, "http://") {
		return Config{}, fmt.Errorf("OPENROUTER_API_BASE must be an http or https URL")
	}
	if every := valueOrDefault("CATALOG_SYNC_INTERVAL", "1h"); every != "off" {
		if cfg.CatalogEvery, err = time.ParseDuration(every); err != nil || cfg.CatalogEvery < time.Minute {
			return Config{}, fmt.Errorf("CATALOG_SYNC_INTERVAL must be a duration of at least 1m, or off")
		}
	}
	switch v := valueOrDefault("FX_SYNC", "on"); v {
	case "on":
		cfg.FXSync = true
	case "off":
	default:
		return Config{}, fmt.Errorf("FX_SYNC must be on or off")
	}
	if cfg.JobsStopGrace, err = time.ParseDuration(valueOrDefault("JOBS_STOP_GRACE", "3m30s")); err != nil || cfg.JobsStopGrace < time.Second {
		return Config{}, fmt.Errorf("JOBS_STOP_GRACE must be a duration of at least 1s")
	}
	if cfg.R2, err = loadR2(); err != nil {
		return Config{}, err
	}
	cfg.WorkDir = valueOrDefault("WORK_DIR", filepath.Join(os.TempDir(), "framy"))
	cfg.AnimCache = valueOrDefault("ANIM_CACHE_DIR", filepath.Join(os.TempDir(), "framy-anim"))
	cfg.FFmpeg = valueOrDefault("FFMPEG", "ffmpeg")
	cfg.FFprobe = valueOrDefault("FFPROBE", "ffprobe")
	if v := valueOrDefault("OPERATOR_CHAT_ID", ""); v != "" {
		if cfg.OperatorChatID, err = strconv.ParseInt(v, 10, 64); err != nil || cfg.OperatorChatID == 0 {
			return Config{}, fmt.Errorf("OPERATOR_CHAT_ID must be a Telegram chat id")
		}
	}
	if cfg.Web, err = loadWeb(); err != nil {
		return Config{}, err
	}
	if cfg.FreeDailyBudgetUSD, err = strconv.ParseFloat(valueOrDefault("FREE_DAILY_BUDGET_USD", "1"), 64); err != nil ||
		math.IsNaN(cfg.FreeDailyBudgetUSD) || math.IsInf(cfg.FreeDailyBudgetUSD, 0) || cfg.FreeDailyBudgetUSD < 0 {
		return Config{}, fmt.Errorf("FREE_DAILY_BUDGET_USD must be a number of dollars, zero or more")
	}
	if os.Getenv("FRAMY_KEK") != "" {
		return Config{}, fmt.Errorf("the KEK comes from FRAMY_KEK_FILE only: an environment variable shows in docker inspect")
	}
	cfg.KEKFile = valueOrDefault("FRAMY_KEK_FILE", "")
	if cfg.SiteURL, cfg.TelegramLogin, err = loadSite(); err != nil {
		return Config{}, err
	}
	if cfg.Hooks, err = loadHooks(cfg.SiteURL); err != nil {
		return Config{}, err
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

// loadR2 reads the bucket: R2_ENDPOINT, or R2_ACCOUNT_ID for R2's own
// endpoint, R2_BUCKET, and the token's two halves, each also as _FILE.
func loadR2() (R2, error) {
	r := R2{Bucket: valueOrDefault("R2_BUCKET", "framy"), Prefix: valueOrDefault("R2_PREFIX", "")}
	if !validPrefix(r.Prefix) {
		return R2{}, fmt.Errorf("R2_PREFIX must be lowercase letters, digits and dashes ending in a slash, such as test/")
	}
	var err error
	if r.AccessKeyID, err = secret("R2_ACCESS_KEY_ID"); err != nil {
		return R2{}, err
	}
	if r.SecretAccessKey, err = secret("R2_SECRET_ACCESS_KEY"); err != nil {
		return R2{}, err
	}
	endpoint, account := valueOrDefault("R2_ENDPOINT", ""), valueOrDefault("R2_ACCOUNT_ID", "")
	switch {
	case endpoint != "" && account != "":
		return R2{}, fmt.Errorf("set R2_ENDPOINT or R2_ACCOUNT_ID, not both")
	case account != "":
		endpoint = "https://" + account + ".r2.cloudflarestorage.com"
	}
	r.Endpoint = strings.TrimRight(endpoint, "/")
	switch {
	case r.AccessKeyID == "" && r.SecretAccessKey == "":
		return r, nil
	case r.AccessKeyID == "" || r.SecretAccessKey == "":
		return R2{}, fmt.Errorf("R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY go together")
	case r.Endpoint == "":
		return R2{}, fmt.Errorf("R2_ACCOUNT_ID or R2_ENDPOINT is required with the R2 token")
	case !strings.HasPrefix(r.Endpoint, "https://") && !strings.HasPrefix(r.Endpoint, "http://"):
		return R2{}, fmt.Errorf("R2_ENDPOINT must be an http or https URL")
	}
	return r, nil
}

// validPrefix: empty, or one path segment of a-z, 0-9 and dashes with its
// slash, so a prefix can never reach into production's u/.
func validPrefix(p string) bool {
	if p == "" {
		return true
	}
	name, ok := strings.CutSuffix(p, "/")
	if !ok || name == "" || name == "u" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
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
