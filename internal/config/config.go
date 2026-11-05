// SPDX-License-Identifier: Apache-2.0
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
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

// DefaultOpenRouterAPIBase is OpenRouter's API: the catalog sync, the runs
// on framy's key and on people's, their checks and the OAuth exchange.
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
	// BotAPIBotDir is where the bot's own media directory is mounted under
	// a name that is not its token (BOT_API_BOT_DIR). With it framy links
	// BotAPIFilesDir/<token> there at start, so no mount of the container
	// is named after the token and docker inspect does not show it.
	BotAPIBotDir string
	DatabaseURL  string
	AutoMigrate  bool
	HTTPAddr     string
	// MetricsAddr serves /metrics apart from HTTPAddr, which the tunnel makes
	// public with the site. Empty turns it off.
	MetricsAddr string
	// LogLevel is the least level logged (LOG_LEVEL: debug, info, warn or
	// error, in any case).
	LogLevel slog.Level
	// OpenRouterAPIBase is where every call to OpenRouter goes, with
	// framy's key, a person's key or an OAuth code, so it is https; http
	// reaches only this machine, for a stand-in. CatalogEvery drives the
	// catalog sync; zero turns it off.
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
	// OperatorChatID hears what needs the operator: the free tier closed by
	// a fuse, the bucket's size, the disk and the queue (internal/watch).
	// Zero leaves the notices in the log.
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
	// Incoming is where the site's browser puts a clip, with a presigned
	// PUT: a bucket of its own and a token limited to it, on the same
	// account. Empty puts clips into Bucket.
	Incoming Incoming
}

// Incoming is the bucket clips are put into and its own token.
type Incoming struct {
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// Enabled: the token is set.
func (r R2) Enabled() bool { return r.AccessKeyID != "" }

// stackPattern keeps a stack's name usable in a River queue's name.
var stackPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// Primary: this process is production, which runs the upkeep of the shared
// tables (the catalog, the rates, the uploads no job took, the thumbnails,
// the expired sessions, the jobs of deleted accounts) and tells the
// operator chat.
func (c Config) Primary() bool { return c.Stack == "" }

func Load() (Config, error) {
	cfg := Config{
		TelegramAPIBase: strings.TrimRight(valueOrDefault("TELEGRAM_API_BASE", DefaultTelegramAPIBase), "/"),
		HTTPAddr:        valueOrDefault("HTTP_ADDR", ":8080"),
		MetricsAddr:     valueOrDefault("METRICS_ADDR", ":9090"),

		OpenRouterAPIBase: strings.TrimRight(valueOrDefault("OPENROUTER_API_BASE", DefaultOpenRouterAPIBase), "/"),
	}
	cfg.Stack = valueOrDefault("FRAMY_STACK", "")
	if cfg.Stack != "" && !stackPattern.MatchString(cfg.Stack) {
		return Config{}, fmt.Errorf("FRAMY_STACK must be empty for production, or a short name of lowercase letters and digits")
	}
	// A level nobody can read was a typo, not a wish for info.
	if err := cfg.LogLevel.UnmarshalText([]byte(valueOrDefault("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
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
	cfg.BotAPIBotDir = filepath.Clean(valueOrDefault("BOT_API_BOT_DIR", "."))
	switch {
	case cfg.BotAPIBotDir == ".":
		cfg.BotAPIBotDir = ""
	case cfg.BotAPIFilesDir == "":
		return Config{}, fmt.Errorf("BOT_API_BOT_DIR needs BOT_API_FILES_DIR: it is where the server's paths are linked to")
	case !filepath.IsAbs(cfg.BotAPIBotDir):
		return Config{}, fmt.Errorf("BOT_API_BOT_DIR must be an absolute path")
	case cfg.BotAPIBotDir == cfg.BotAPIFilesDir || strings.HasPrefix(cfg.BotAPIBotDir, cfg.BotAPIFilesDir+"/"):
		return Config{}, fmt.Errorf("BOT_API_BOT_DIR must be outside BOT_API_FILES_DIR, where the link to it is made")
	}
	if err := keyBase(cfg.OpenRouterAPIBase); err != nil {
		return Config{}, fmt.Errorf("OPENROUTER_API_BASE %w", err)
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

// keyBase checks the base of an API that keys are sent to: https, or http to
// this machine only, where nothing crosses a network in the clear.
func keyBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be an https URL, such as " + DefaultOpenRouterAPIBase)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && localHost(u.Hostname()):
		return nil
	case u.Scheme == "http":
		return errors.New("must be https: keys are sent to it; http is for localhost only")
	}
	return errors.New("must be an https URL, such as " + DefaultOpenRouterAPIBase)
}

// localHost: the name or address of this machine itself.
func localHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
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
	if r.Incoming, err = loadIncoming(r.Bucket); err != nil {
		return R2{}, err
	}
	return r, nil
}

// loadIncoming reads the bucket the site's clips are put into,
// R2_INCOMING_BUCKET, and its token, R2_INCOMING_ACCESS_KEY_ID and
// R2_INCOMING_SECRET_ACCESS_KEY, each also as _FILE. The token is read only
// with the bucket, so a manifest may name its files before they exist.
//
// A presigned PUT signs the host alone. A token that reaches only this
// bucket cannot be made to copy anyone's file into it with a header the
// signature does not cover (docs/design.md, Storage and retention).
func loadIncoming(main string) (Incoming, error) {
	in := Incoming{Bucket: valueOrDefault("R2_INCOMING_BUCKET", "")}
	if in.Bucket == "" {
		return Incoming{}, nil
	}
	if in.Bucket == main || strings.Contains(in.Bucket, "/") {
		return Incoming{}, fmt.Errorf("R2_INCOMING_BUCKET must be a bucket other than R2_BUCKET")
	}
	var err error
	if in.AccessKeyID, err = secret("R2_INCOMING_ACCESS_KEY_ID"); err != nil {
		return Incoming{}, err
	}
	if in.SecretAccessKey, err = secret("R2_INCOMING_SECRET_ACCESS_KEY"); err != nil {
		return Incoming{}, err
	}
	if in.AccessKeyID == "" || in.SecretAccessKey == "" {
		return Incoming{}, fmt.Errorf("R2_INCOMING_BUCKET needs its own token: R2_INCOMING_ACCESS_KEY_ID and R2_INCOMING_SECRET_ACCESS_KEY")
	}
	return in, nil
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
