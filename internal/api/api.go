// SPDX-License-Identifier: Apache-2.0

// Package api is the JSON API the site and the Mini App talk to: the
// catalog and the price of a run before it starts, who is signed in, photo
// uploads, starting a job, reading it, and following it over SSE
// (docs/design.md, Jobs). No request waits for a generation: POST /api/jobs
// answers 202 at once and the client follows /api/jobs/{id}/events, with
// GET /api/jobs/{id} as the fallback.
//
// Who is asking is not decided here. Config.Person names the signed-in
// person behind a request, or says there is none: main gives it
// internal/auth's Person. GET /api/catalog and POST /api/quote answer anyone,
// since the site shows models and prices before a sign-in; every other route
// answers 401 {"error":"sign_in"} without a person, and a job is visible to
// its owner only: anyone else gets 404, as for a job that does not exist.
//
// Progress comes from the database. A trigger on framy.job bumps the job's
// event_seq and does pg_notify('framy_events', …) in the transaction that
// changes the row; one listener per process (Run) wakes the open streams,
// and each stream rereads its job. The reread is the truth, the notice only
// says when to look, so a lost or doubled notice costs nothing.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/chat"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/storage"
	"github.com/FreshLabDev/framy/internal/web"
)

// Defaults.
const (
	// DefaultMaxStreams is how many event streams one person may hold open
	// (docs/design.md, Jobs). A fourth is refused with 429.
	DefaultMaxStreams = 3
	// DefaultPing is how often an idle stream writes a ": ping" comment. It
	// has to stay well under the 100 s after which Cloudflare drops a
	// connection that sends nothing.
	DefaultPing = 15 * time.Second
	// DefaultWriteTimeout is how long one write to a stream may take. A
	// client that stops reading fills its socket buffer, and the write then
	// blocks until this runs out.
	DefaultWriteTimeout = 10 * time.Second
	// DefaultUploadSlots is how many photos are converted at once: ffmpeg
	// takes a core for every one of them.
	DefaultUploadSlots = 4

	// uploadRead and uploadWrite are the deadlines of POST /api/uploads,
	// counted from the start of the request: a slow phone connection may take
	// minutes to send 50 MB, and the answer comes after the conversion.
	uploadRead  = 5 * time.Minute
	uploadWrite = 6 * time.Minute
	// uploadSlack is room above the largest photo for what the body may carry
	// besides it, so the pipeline's own "too big" answer comes first.
	uploadSlack = 1 << 20
	// uploadWait is how long an upload waits for a free conversion slot.
	uploadWait = 20 * time.Second
	// retryMillis is the reconnect delay an event stream suggests.
	retryMillis = 3000
	// DefaultCatalogTTL is how long the catalog is kept in memory.
	DefaultCatalogTTL = time.Minute
)

// Queue starts and stops jobs: *jobs.Queue. Cancel ends a draft or a queued
// job and answers jobs.ErrRunning for a running one.
type Queue interface {
	Enqueue(ctx context.Context, n jobs.New) (id int64, created bool, err error)
	Cancel(ctx context.Context, id int64) error
}

// Store is what the API asks the database about beyond jobs: *db.Store.
type Store interface {
	// Offer is an offer that is on and shown, with its model; db.ErrNoOffer
	// for any other.
	Offer(ctx context.Context, id string) (catalog.Model, catalog.Offer, error)
	Quote(ctx context.Context, code string, day time.Time) (fx.Quote, error)
	// Currency is the person's chosen currency, "" when they chose none.
	Currency(ctx context.Context, userID int64) (string, error)
	// UserLang is the person's language in core, "" when it has none.
	UserLang(ctx context.Context, userID int64) (string, error)
	// SetCurrency keeps the person's currency, "" for the default.
	SetCurrency(ctx context.Context, userID int64, code string) error
	// SetUserLang is the person's pick in core, for every bot of the family;
	// ClearUserLang withdraws framy's pick.
	SetUserLang(ctx context.Context, userID int64, lang string) error
	ClearUserLang(ctx context.Context, userID int64) error
	// Card is the name and photo of the person's last sign-in; a zero Card
	// when there is none.
	Card(ctx context.Context, userID int64) (db.Card, error)
	// Catalog is every model that is on and shown, with its offers that are,
	// and CatalogSyncedAt when the last sync saved it.
	Catalog(ctx context.Context) ([]catalog.Model, error)
	CatalogSyncedAt(ctx context.Context) (time.Time, error)
}

// Allowance is the free allowance: *free.Allowance. Usage reads a person's
// meters; Check answers what the queue's admission would answer now,
// without taking a slot.
type Allowance interface {
	Usage(ctx context.Context, userID int64) (free.Usage, error)
	Check(ctx context.Context, a jobs.Admission) error
}

// Uploads keeps a photo: *storage.Store.
type Uploads interface {
	Upload(ctx context.Context, userID int64, r io.Reader) (storage.Asset, error)
}

// Links presigns files of the person's: *storage.Store. An id that is not
// theirs gets no link, and a file named in names a download link as well.
type Links interface {
	LinkAll(ctx context.Context, userID int64, ids []int64, names map[int64]string) (map[int64]storage.FileLinks, error)
}

// Sharing prepares a result for shareMessage in the Mini App: *chat.Flow.
// It answers db.ErrNoResult for a file that is not a done result of the
// person's, and chat.ErrNotShareable for one Telegram cannot be given.
type Sharing interface {
	Share(ctx context.Context, userID, assetID int64) (chat.Shared, error)
}

// Connections are the person's provider keys: *keys.Service. A nil
// *keys.Service answers keys.ErrDisabled, which is no connection.
type Connections interface {
	List(ctx context.Context, userID int64) ([]keys.Connection, error)
	Save(ctx context.Context, userID int64, provider, key string) (keys.Connection, error)
	Delete(ctx context.Context, userID int64, provider string) error
	Recheck(ctx context.Context, userID int64, provider string) (keys.Connection, error)
}

// Files deletes the person's files and sums them: *storage.Store.
type Files interface {
	// DeleteJobFiles removes a job's results and the uploads no other job
	// takes.
	DeleteJobFiles(ctx context.Context, userID, jobID int64) (int, error)
	// DeleteAll removes every file of the person's kept before before.
	DeleteAll(ctx context.Context, userID int64, before time.Time) (int, error)
	Usage(ctx context.Context, userID int64) (storage.Usage, error)
}

// Config is the API's setup.
type Config struct {
	// Pool's search_path is framy. The API reads job and asset rows with it,
	// and the listener opens its own connection from its configuration.
	Pool  *pgxpool.Pool
	Queue Queue
	Store Store
	// Uploads, Links and Files are nil while framy keeps no files (no R2
	// token): uploading then answers 503, a finished job lists its results
	// without links, and deleting a job that has files answers 503.
	Uploads Uploads
	Links   Links
	Files   Files
	// Videos is nil while framy keeps no files: the clip routes answer 503.
	Videos Videos
	// Sharing is nil without the bot's flow: sharing answers 503.
	Sharing Sharing
	// Keys is nil without a KEK file, which is no connection for anyone.
	Keys Connections
	// Allowance is nil in tests that leave the free allowance out: /api/me
	// then says nothing of it, and a quote does not say whether a free run
	// would go.
	Allowance Allowance
	// Person names the signed-in person behind a request. No person: ok is
	// false. It is asked on every request, so it has to be cheap.
	Person func(*http.Request) (userID int64, ok bool)
	// Revoke ends every session of the person, on the site and in the Mini
	// App: internal/auth's RevokeAll. Deleting an account calls it last.
	Revoke func(ctx context.Context, userID int64) error
	Log    *slog.Logger

	// MaxStreams per person, Ping, WriteTimeout and UploadSlots default to
	// the constants above. MaxUpload is the largest photo the pipeline takes
	// (storage.Config.MaxUpload); it sets the body cap of the upload route.
	MaxStreams   int
	Ping         time.Duration
	WriteTimeout time.Duration
	UploadSlots  int
	// VideoSlots is how many clips are worked on at once.
	VideoSlots int
	// UploadWait is how long an upload waits for a free slot before it is
	// sent away with 503.
	UploadWait time.Duration
	MaxUpload  int64
	// Keepalive is how often the listener checks its connection while no
	// notice comes; Backoff is the pause after the first lost connection,
	// doubling up to 30 s. Tests shorten both.
	Keepalive time.Duration
	Backoff   time.Duration
	Now       func() time.Time

	// CatalogTTL is how long the catalog is kept in memory between reads of
	// the database: DefaultCatalogTTL when zero. The sync changes it once an
	// hour.
	CatalogTTL time.Duration

	// data replaces the reads of job and asset rows, and listening is told
	// each time the listener is connected; tests only.
	data      data
	listening func()
}

// API serves the routes.
type API struct {
	cfg     Config
	log     *slog.Logger
	data    data
	hub     *hub
	streams *streamLimiter
	slots   chan struct{}
	now     func() time.Time
	maxBody int64
	catalog *catalogCache
	videos  *videoTasks
	// bg is the context of the work that outlives a request, a clip's task;
	// Run's end and Close cancel it.
	bg     context.Context
	stopBG context.CancelFunc
}

// New checks cfg and fills its defaults.
func New(cfg Config) (*API, error) {
	switch {
	case cfg.Queue == nil || cfg.Store == nil || cfg.Person == nil || cfg.Revoke == nil:
		return nil, errors.New("api: the queue, the store, Person and Revoke are needed")
	case cfg.Pool == nil && cfg.data == nil:
		return nil, errors.New("api: the pool is needed")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	setDefault(&cfg.MaxStreams, DefaultMaxStreams)
	setDefault(&cfg.Ping, DefaultPing)
	setDefault(&cfg.WriteTimeout, DefaultWriteTimeout)
	setDefault(&cfg.UploadSlots, DefaultUploadSlots)
	setDefault(&cfg.VideoSlots, DefaultVideoSlots)
	setDefault(&cfg.UploadWait, uploadWait)
	setDefault(&cfg.MaxUpload, storage.DefaultMaxUpload)
	setDefault(&cfg.Keepalive, 30*time.Second)
	setDefault(&cfg.Backoff, time.Second)
	setDefault(&cfg.CatalogTTL, DefaultCatalogTTL)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &API{
		cfg: cfg, log: cfg.Log, data: cfg.data, hub: newHub(),
		streams: newStreamLimiter(cfg.MaxStreams),
		slots:   make(chan struct{}, cfg.UploadSlots),
		now:     cfg.Now,
		maxBody: cfg.MaxUpload + uploadSlack,
		catalog: &catalogCache{ttl: cfg.CatalogTTL},
		videos:  newVideoTasks(cfg.VideoSlots, cfg.Now),
	}
	a.bg, a.stopBG = context.WithCancel(context.Background())
	if a.data == nil {
		a.data = pgData{pool: cfg.Pool}
	}
	return a, nil
}

func setDefault[T int | int64 | time.Duration](v *T, d T) {
	if *v <= 0 {
		*v = d
	}
}

// Register puts the routes on the public server.
func (a *API) Register(site *web.Server) {
	site.HandleFunc("POST /api/uploads", a.upload,
		web.Limit(web.ClassUpload), web.Body(a.maxBody), web.Deadlines(uploadRead, uploadWrite))
	site.HandleFunc("POST /api/videos/uploads", a.videoUpload, web.Limit(web.ClassUpload))
	site.HandleFunc("POST /api/videos", a.videoStart, web.Limit(web.ClassUpload), web.Body(smallBody))
	site.HandleFunc("GET /api/videos/{task}", a.videoTask)
	// The last frame reads the clip from the bucket first.
	site.HandleFunc("POST /api/files/{id}/last-frame", a.lastFrame, web.Limit(web.ClassUpload),
		web.Deadlines(10*time.Second, 3*time.Minute))
	site.HandleFunc("POST /api/files/{id}/share", a.share, web.Limit(web.ClassJob))
	site.HandleFunc("POST /api/jobs", a.create, web.Limit(web.ClassJob))
	site.HandleFunc("GET /api/jobs", a.history)
	site.HandleFunc("GET /api/jobs/{id}", a.get)
	site.HandleFunc("DELETE /api/jobs/{id}", a.remove)
	site.HandleFunc("POST /api/jobs/{id}/cancel", a.cancel)
	site.HandleFunc("GET /api/jobs/{id}/events", a.events, web.Stream())
	site.HandleFunc("GET /api/me", a.me)
	site.HandleFunc("PATCH /api/me", a.settings, web.Body(smallBody))
	// Deleting an account removes every file, which may take minutes.
	site.HandleFunc("DELETE /api/me", a.deleteAccount, web.Limit(web.ClassAuth), web.Body(smallBody),
		web.Deadlines(10*time.Second, accountDeadline+10*time.Second))
	// Saving and checking a key ask the provider; the key check takes up to
	// 15 s.
	site.HandleFunc("PUT /api/connections/{provider}", a.saveConnection, web.Limit(web.ClassJob), web.Body(smallBody),
		web.Deadlines(10*time.Second, 30*time.Second))
	site.HandleFunc("DELETE /api/connections/{provider}", a.deleteConnection)
	site.HandleFunc("POST /api/connections/{provider}/check", a.checkConnectionRoute, web.Limit(web.ClassJob),
		web.Deadlines(10*time.Second, 30*time.Second))
	site.HandleFunc("GET /api/catalog", a.catalogRoute)
	site.HandleFunc("POST /api/quote", a.quoteRoute)
}

// PersonKey makes web.Config.Person of the API's Person: the per-person rate
// limits count by it.
func PersonKey(person func(*http.Request) (int64, bool)) func(*http.Request) string {
	return func(r *http.Request) string {
		if id, ok := person(r); ok {
			return strconv.FormatInt(id, 10)
		}
		return ""
	}
}

// Run listens for job events until ctx ends, then ends every open stream
// and stops the clips being worked on.
// A lost connection is made again, and the streams reread their jobs when it
// is. Call it once, in its own goroutine.
func (a *API) Run(ctx context.Context) {
	defer a.hub.close()
	defer a.stopBG()
	if a.cfg.Pool == nil {
		<-ctx.Done()
		return
	}
	l := &listener{
		cfg: a.cfg.Pool.Config().ConnConfig.Copy(), hub: a.hub, log: a.log.With("component", "events"),
		keepalive: a.cfg.Keepalive, backoff: a.cfg.Backoff, connected: a.cfg.listening,
	}
	l.run(ctx)
}

// Close ends every open stream now. Run does it when its context ends; a
// server that stops without cancelling that context calls Close.
func (a *API) Close() {
	a.stopBG()
	a.hub.close()
}

// ---- answers ----

// Error codes. The frontend words them; message is English, for logs and
// for whoever reads a curl answer.
const (
	codeSignIn            = "sign_in"
	codeBadRequest        = "bad_request"
	codeNotFound          = "not_found"
	codeUnknownOffer      = "unknown_offer"
	codePriceChanged      = "price_changed"
	codeUnknownInput      = "unknown_input"
	codeWrongInputs       = "wrong_inputs"
	codeNotFree           = "not_free"
	codeFreeLimit         = "free_limit"
	codeFreeClosed        = "free_closed"
	codeNoConnection      = "no_connection"
	codeConnectionInvalid = "connection_invalid"
	codeTooManyStreams    = "too_many_streams"
	codeTooLarge          = "too_large"
	codePictureTooLarge   = "picture_too_large"
	codeUnsupportedType   = "unsupported_type"
	codeUnreadable        = "unreadable"
	codeStorageDown       = "storage_unavailable"
	codeBusy              = "busy"
	codeShuttingDown      = "shutting_down"
	codeInternal          = "internal"
	codeJobActive         = "job_active"
	codeJobRunning        = "job_running"
	codeJobDraft          = "job_draft"
	codeJobsActive        = "jobs_active"
	codeUnknownProvider   = "unknown_provider"
	codeKeysOff           = "keys_off"
	codeBadKey            = "bad_key"
	codeKeyRejected       = "key_rejected"
	codeKeyForbidden      = "key_forbidden"
	codeKeyUnreadable     = "key_unreadable"
	codeCheckUnavailable  = "check_unavailable"
	codeCannotContinue    = "cannot_continue"
	codeNoUpload          = "no_upload"
	codeNotAVideo         = "not_a_video"
	codeVideoTooLong      = "video_too_long"
	codeFrameTooLarge     = "frame_too_large"
	codeBadCut            = "bad_cut"
	codeNotShareable      = "not_shareable"
	codeTelegramDown      = "telegram_unavailable"
)

// apiError is an answer that is not a success.
type apiError struct {
	status  int
	code    string
	message string
	// retryAfter, when positive, goes out as Retry-After in whole seconds.
	retryAfter time.Duration
	// extra is more fields of the body: frees_at, until, field, provider.
	extra map[string]any
}

func (e *apiError) with(k string, v any) *apiError {
	if e.extra == nil {
		e.extra = map[string]any{}
	}
	e.extra[k] = v
	return e
}

func newError(status int, code, message string) *apiError {
	return &apiError{status: status, code: code, message: message}
}

func badRequest(field, message string) *apiError {
	e := newError(http.StatusBadRequest, codeBadRequest, message)
	if field != "" {
		e.with("field", field)
	}
	return e
}

func (e *apiError) write(w http.ResponseWriter) {
	body := map[string]any{"error": e.code, "message": e.message}
	for k, v := range e.extra {
		body[k] = v
	}
	if e.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(wholeSeconds(e.retryAfter)))
	}
	writeJSON(w, e.status, body)
}

// wholeSeconds rounds up, and is at least one.
func wholeSeconds(d time.Duration) int {
	return max(1, int((d+time.Second-1)/time.Second))
}

func signIn(w http.ResponseWriter) {
	newError(http.StatusUnauthorized, codeSignIn, "sign in first").write(w)
}

func notFound(w http.ResponseWriter) {
	newError(http.StatusNotFound, codeNotFound, "no such job").write(w)
}

func (a *API) internal(w http.ResponseWriter, r *http.Request, err error) {
	a.internalError(r, err).write(w)
}

// internalError logs err and is the answer to it.
func (a *API) internalError(r *http.Request, err error) *apiError {
	a.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	return newError(http.StatusInternalServerError, codeInternal, "something went wrong; try again")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// person is the signed-in person, or false after answering 401.
func (a *API) person(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := a.cfg.Person(r)
	if !ok || id == 0 {
		signIn(w)
		return 0, false
	}
	return id, true
}

// language is the person's language for writing amounts: core's, else the
// first tag of Accept-Language, else English. userID 0 is nobody signed in.
func (a *API) language(ctx context.Context, r *http.Request, userID int64) string {
	var lang string
	if userID != 0 {
		var err error
		if lang, err = a.cfg.Store.UserLang(ctx, userID); err != nil {
			a.log.Warn("user language", "error", err)
		}
	}
	if lang == "" {
		lang = firstTag(r.Header.Get("Accept-Language"))
	}
	if lang == "" {
		lang = "en"
	}
	return lang
}

// firstTag is the first language of an Accept-Language value, without its
// region: "uk-UA,uk;q=0.9,en;q=0.8" gives "uk". "*" and junk give "".
func firstTag(h string) string {
	first, _, _ := strings.Cut(h, ",")
	first, _, _ = strings.Cut(first, ";")
	tag, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(first)), "-")
	if len(tag) < 2 || len(tag) > 3 {
		return ""
	}
	for _, c := range tag {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return tag
}

// A mutex-guarded set of the streams a person holds.
type streamLimiter struct {
	mu  sync.Mutex
	max int
	n   map[int64]int
}

func newStreamLimiter(max int) *streamLimiter {
	return &streamLimiter{max: max, n: map[int64]int{}}
}

// acquire takes one of the person's streams, or says there is none left.
func (l *streamLimiter) acquire(userID int64) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[userID] >= l.max {
		return nil, false
	}
	l.n[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.n[userID]--; l.n[userID] <= 0 {
				delete(l.n, userID)
			}
		})
	}, true
}
