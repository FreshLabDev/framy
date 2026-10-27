// SPDX-License-Identifier: Apache-2.0

// Package keys keeps the provider keys people paste into the settings of the
// site and the Mini App (docs/design.md, Providers and keys).
//
//   - Save checks a key with the provider, encrypts it and replaces what the
//     person had for that provider. List, Delete and Recheck are the rest of
//     what a settings page calls.
//   - At rest a key is an envelope: its own data key, AES-256-GCM, the data
//     key wrapped by a key-encryption key (KEK) from a file (envelope.go).
//     Rotating the KEK rewraps the data keys and leaves the keys alone
//     (Rotate).
//   - JobKey is the key source of the adapters: the decrypted key of the
//     person a job belongs to, for the length of one call.
//   - Track wraps an adapter so that what a run shows about its key is
//     written down as the connection's status.
//
// A key leaves this package only through JobKey, into an adapter's request.
// Everything else, the types a page gets, the errors, the log lines and the
// metrics, holds at most the last four characters.
package keys

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/metrics"
)

var (
	// ErrDisabled: framy was started without a KEK file, so it keeps no keys.
	ErrDisabled = errors.New("framy keeps no keys: no KEK file is configured")
	// ErrUnsupportedProvider: not one of Providers.
	ErrUnsupportedProvider = errors.New("keys of this provider are not kept")
	// ErrBadKey: the text cannot be a key: too short or long, or with a space
	// or a character outside printable ASCII in it. The check never sees it.
	ErrBadKey = errors.New("this is not a key")
	// ErrNotFound: the person has no connection to this provider.
	ErrNotFound = errors.New("no such connection")
	// ErrUnreadable: the stored key cannot be decrypted: the KEK that wrapped
	// it is not in the KEK file, or the row was altered. The person enters
	// the key again.
	ErrUnreadable = errors.New("the stored key cannot be read")
)

// Connection is a connection as a settings page sees it. It has no key in any
// form: only the last four characters.
type Connection struct {
	ID       string `json:"id"`
	UserID   int64  `json:"-"`
	Provider string `json:"provider"`
	Last4    string `json:"last4"`
	Status   Status `json:"status"`
	// LastSuccessAt is the last run that succeeded on the key.
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	// LastErrorClass is the class (internal/jobs) of the last run that
	// failed on it, empty after a success.
	LastErrorClass string    `json:"last_error_class,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Config is what the service needs.
type Config struct {
	Pool *pgxpool.Pool
	KEK  *KEK
	// HTTP makes the checks; a client with a 15 s timeout when nil. It is
	// copied, and never follows a redirect: a header with a key in it must
	// not leave for another host.
	HTTP      *http.Client
	UserAgent string
	// The providers' roots, their defaults when empty. Tests point them at a
	// local server.
	OpenAIBase, GeminiBase, OpenRouterBase string
	Log                                    *slog.Logger
}

// Service keeps the keys. A nil *Service is a framy without a KEK file: Save
// and the rest answer ErrDisabled, and JobKey answers every job with a Key
// error, so a person's run never falls back to another key.
type Service struct {
	pool  *pgxpool.Pool
	kek   *KEK
	check checker
	log   *slog.Logger
}

// New builds the service on pool, whose search_path is framy.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.KEK == nil {
		return nil, errors.New("keys: a pool and a KEK are required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{pool: cfg.Pool, kek: cfg.KEK, check: newChecker(cfg), log: cfg.Log}, nil
}

func newChecker(cfg Config) checker {
	hc := &http.Client{Timeout: 15 * time.Second}
	if cfg.HTTP != nil {
		c := *cfg.HTTP
		hc = &c
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return checker{
		http:      hc,
		userAgent: cfg.UserAgent,
		bases: map[string]string{
			catalog.OpenAI:     strings.TrimRight(orDefault(cfg.OpenAIBase, DefaultOpenAIBase), "/"),
			catalog.Gemini:     strings.TrimRight(orDefault(cfg.GeminiBase, DefaultGeminiBase), "/"),
			catalog.OpenRouter: strings.TrimRight(orDefault(cfg.OpenRouterBase, DefaultOpenRouterBase), "/"),
		},
	}
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

const (
	minKeyLen = 20
	maxKeyLen = 512
)

// normalizeKey trims the paste and refuses what cannot be a key. It is a
// shape check, not a prefix check: providers change how their keys begin.
func normalizeKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if len(key) < minKeyLen || len(key) > maxKeyLen {
		return "", ErrBadKey
	}
	for i := 0; i < len(key); i++ {
		if c := key[i]; c <= ' ' || c > '~' {
			return "", ErrBadKey
		}
	}
	return key, nil
}

// columns are what a settings page may read of a row; the key's columns are
// not among them.
const columns = `id::text, user_id, provider, last4, status, last_success_at, COALESCE(last_error_class, ''), created_at, updated_at`

func scanConnection(row pgx.Row) (Connection, error) {
	var c Connection
	err := row.Scan(&c.ID, &c.UserID, &c.Provider, &c.Last4, &c.Status, &c.LastSuccessAt, &c.LastErrorClass, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// Save checks a key with the provider and keeps it, replacing the person's
// earlier key for that provider. The check is the provider's list of models
// (for OpenRouter the key's own record): free, and enough to tell a key the
// provider accepts, so the connection starts as Valid. A key the provider
// refuses is not stored, nor is one that could not be checked: ErrRejected,
// ErrForbidden and ErrUnavailable say which, and none carries the key.
//
// The person has to be in core already (Store.Touch): the row references
// core.person.
func (s *Service) Save(ctx context.Context, userID int64, provider, key string) (Connection, error) {
	if s == nil {
		return Connection{}, ErrDisabled
	}
	if !supported(provider) {
		return Connection{}, ErrUnsupportedProvider
	}
	key, err := normalizeKey(key)
	if err != nil {
		return Connection{}, err
	}
	if err := s.check.check(ctx, provider, key); err != nil {
		return Connection{}, err
	}
	id, err := newID()
	if err != nil {
		return Connection{}, err
	}
	env, err := s.kek.seal(binding{userID: userID, provider: provider, connectionID: id}, []byte(key))
	if err != nil {
		return Connection{}, fmt.Errorf("keep the key: %w", err)
	}
	// A replaced key is a new connection: a run on the old key that reports
	// afterwards finds no row to change.
	c, err := scanConnection(s.pool.QueryRow(ctx, `
		INSERT INTO connection (id, user_id, provider, ciphertext, wrapped_dek, kek_version, last4, status)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, 'valid')
		ON CONFLICT (user_id, provider) DO UPDATE SET
		  id = EXCLUDED.id, ciphertext = EXCLUDED.ciphertext, wrapped_dek = EXCLUDED.wrapped_dek,
		  kek_version = EXCLUDED.kek_version, last4 = EXCLUDED.last4, status = 'valid',
		  last_success_at = NULL, last_error_class = NULL, created_at = now(), updated_at = now()
		RETURNING `+columns,
		id, userID, provider, env.ciphertext, env.wrappedDEK, env.kekVersion, key[len(key)-4:]))
	if err != nil {
		return Connection{}, fmt.Errorf("save the connection: %w", err)
	}
	s.log.Info("key saved", "connection", c.ID, "provider", provider, "status", c.Status)
	return c, nil
}

// List is the person's connections, by provider.
func (s *Service) List(ctx context.Context, userID int64) ([]Connection, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	rows, err := s.pool.Query(ctx, `SELECT `+columns+` FROM connection WHERE user_id = $1 ORDER BY provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("list connections: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete removes the person's connection to the provider, the row and with it
// the encrypted key. Revoking the key at the provider is the person's step.
func (s *Service) Delete(ctx context.Context, userID int64, provider string) error {
	if s == nil {
		return ErrDisabled
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM connection WHERE user_id = $1 AND provider = $2`, userID, provider)
	if err != nil {
		return fmt.Errorf("delete the connection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.log.Info("key deleted", "provider", provider)
	return nil
}

// row is a connection with the key's columns, for the code that opens it.
type row struct {
	id       string
	userID   int64
	provider string
	status   Status
	env      envelope
}

func (r row) binding() binding {
	return binding{userID: r.userID, provider: r.provider, connectionID: r.id}
}

func (s *Service) load(ctx context.Context, userID int64, provider string) (row, error) {
	r := row{userID: userID, provider: provider}
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, status, ciphertext, wrapped_dek, kek_version
		FROM connection WHERE user_id = $1 AND provider = $2`, userID, provider,
	).Scan(&r.id, &r.status, &r.env.ciphertext, &r.env.wrappedDEK, &r.env.kekVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, ErrNotFound
	}
	if err != nil {
		return row{}, fmt.Errorf("read the connection: %w", err)
	}
	return r, nil
}

// open decrypts a row, counting and logging a failure without the key.
func (s *Service) open(r row) ([]byte, error) {
	key, err := s.kek.open(r.binding(), r.env)
	if err != nil {
		metrics.KeyDecryptFailures.Inc()
		s.log.Error("a stored key cannot be decrypted", "connection", r.id, "provider", r.provider, "kek_version", r.env.kekVersion)
		return nil, ErrUnreadable
	}
	return key, nil
}

// Recheck asks the provider again whether it accepts the stored key. What the
// answer settles is all it changes:
//
//   - the provider refuses the key: Invalid;
//   - the provider accepts it: Valid, unless a generation already succeeded
//     (Verified stays). The list of models says nothing of billing, credit or
//     verification, so a status that named such a problem is cleared to Valid
//     rather than kept: the person who fixed it presses this, and the next
//     run sets the status again if it was not fixed;
//   - the check reached no answer, or the provider refuses the list (403):
//     nothing changes and the error says why.
func (s *Service) Recheck(ctx context.Context, userID int64, provider string) (Connection, error) {
	if s == nil {
		return Connection{}, ErrDisabled
	}
	r, err := s.load(ctx, userID, provider)
	if err != nil {
		return Connection{}, err
	}
	key, err := s.open(r)
	if err != nil {
		return Connection{}, err
	}
	defer clear(key)
	var status Status
	switch err := s.check.check(ctx, provider, string(key)); {
	case err == nil:
		status = Valid
		if r.status == Verified {
			status = Verified
		}
	case errors.Is(err, ErrRejected):
		status = Invalid
	default:
		return Connection{}, err
	}
	// Tied to the id read above: a key replaced meanwhile is not touched.
	c, err := scanConnection(s.pool.QueryRow(ctx, `
		UPDATE connection SET status = $2,
		  last_error_class = CASE WHEN $2 = 'valid' THEN NULL ELSE last_error_class END,
		  updated_at = now()
		WHERE id = $1::uuid RETURNING `+columns, r.id, string(status)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, fmt.Errorf("save the status: %w", err)
	}
	s.log.Info("key rechecked", "connection", c.ID, "provider", provider, "status", c.Status)
	return c, nil
}

// JobKey is the key source of the adapters: the decrypted key of the person
// the job belongs to, for the provider the job runs at. It has the signature
// of an adapter's Config.Key.
//
// There is a key only when the person has a connection for that provider and
// the provider has not refused it (Invalid). A key marked as lacking billing,
// verification or credit is still returned: the run costs nothing when the
// provider refuses it, and it is how the status gets fixed once the person
// has fixed the account. Otherwise, and for a key that cannot be read, the
// answer is a jobs.Error of class Key. A free run is refused here: it is paid
// by framy's key, and a person's key must not pay for what is shown as free.
// The messages hold no key.
func (s *Service) JobKey(ctx context.Context, j jobs.Job) (string, error) {
	if s == nil {
		return "", keyError("people's keys are not available: no KEK file is configured")
	}
	name := providerName(j.Provider)
	if j.Free {
		return "", keyError("a free run uses no key of the person's")
	}
	r, err := s.load(ctx, j.UserID, j.Provider)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", keyError("the person has no " + name + " key")
	case err != nil:
		return "", err
	case r.status == Invalid:
		return "", keyError("the " + name + " key was refused by the provider; the person has to replace it")
	}
	key, err := s.open(r)
	if err != nil {
		return "", keyError("the stored " + name + " key cannot be read; the person has to enter it again")
	}
	defer clear(key)
	noteUse(ctx, r.id)
	return string(key), nil
}

func keyError(msg string) error {
	return &jobs.Error{Class: jobs.Key, Err: errors.New(msg)}
}

// newID is a random UUID (version 4), made here because the connection's id
// is part of what the key is bound to and so is needed before the insert.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make an id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
