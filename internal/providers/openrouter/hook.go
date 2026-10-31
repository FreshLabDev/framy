// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// HookPattern is the route of OpenRouter's video webhooks on the public
// server: web.Server.Handle(openrouter.HookPattern, hook).
const HookPattern = "POST " + hookPrefix + "{job}/{mac}"

const (
	signatureHeader   = "X-OpenRouter-Signature"
	idempotencyHeader = "X-OpenRouter-Idempotency-Key"
	// SignatureWindow is how far a signed delivery's timestamp may be from
	// the clock, either way.
	SignatureWindow = 300 * time.Second
	// maxHookBody bounds a delivery: a job object is a few hundred bytes.
	maxHookBody = 64 << 10
)

// HookJob is what the hook needs to know about the job a webhook names.
type HookJob struct {
	// Free: framy's own key pays for it, so a delivery for it comes from the
	// workspace whose signing secret framy holds.
	Free bool
	// Waiting: the run is out at OpenRouter, and its next poll can be brought
	// forward.
	Waiting bool
}

// ErrNoJob: the job a webhook names does not exist.
var ErrNoJob = errors.New("no such job")

// Deliveries is the hook's side of the database.
type Deliveries interface {
	// Job says who pays for a job and whether it waits for OpenRouter.
	Job(ctx context.Context, id int64) (HookJob, error)
	// Record notes a delivery under its keys and, if none was seen before,
	// brings the job's next poll forward, in one step. fresh is false for a
	// delivery seen before, which did nothing; woken says whether a poll was
	// brought forward.
	Record(ctx context.Context, id int64, keys []string) (fresh, woken bool, err error)
}

// Hook receives OpenRouter's video webhooks. A webhook is a hint that a run
// changed, never the result: it brings the job's next poll forward and the
// poll asks OpenRouter, with the key that pays for the job. Nothing a webhook
// carries is believed, so the worst a forged one can do is cost one poll,
// which costs nothing.
//
// Its checks, in order:
//
//   - the MAC in the path, in constant time, before anything else is read:
//     the path is the capability to talk to the job at all;
//   - on a free job, the signature of framy's own workspace
//     (X-OpenRouter-Signature: t=<unix>,v1=<hex of HMAC-SHA256 of "t,body">)
//     within SignatureWindow, when framy has the secret. A person's job is
//     paid from their own key and workspace, whose secret framy cannot know,
//     so their deliveries are not checked beyond the path;
//   - the delivery's keys (X-OpenRouter-Idempotency-Key and, when signed, the
//     signature) against deliveries already acted on, so a retry or a replay
//     within the window does nothing twice.
//
// Every answer that is not a failure of framy's own is a 2xx or a 4xx that
// OpenRouter has no reason to retry; the work happens in the queue, never in
// the request.
type Hook struct {
	Callbacks  Callbacks
	Deliveries Deliveries
	// SigningSecret is the signing secret of the workspace framy's own key
	// belongs to (OPENROUTER_WEBHOOK_SECRET). Empty: framy cannot tell a
	// signed delivery from a forged one, and a free job's delivery is taken
	// as a hint like a person's.
	SigningSecret []byte
	Log           *slog.Logger
	// Now is the clock, for tests.
	Now func() time.Time
}

func (h *Hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := h.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}

	id, err := strconv.ParseInt(r.PathValue("job"), 10, 64)
	if err != nil || id <= 0 || !h.Callbacks.Valid(id, r.PathValue("mac")) {
		// The path is never logged: its last segment is the capability.
		metrics.OpenRouterHooks.Inc("bad_path")
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, http.StatusText(status), status)
		return
	}

	job, err := h.Deliveries.Job(r.Context(), id)
	switch {
	case errors.Is(err, ErrNoJob):
		metrics.OpenRouterHooks.Inc("ignored")
		log.Debug("openrouter webhook for a job that is gone", "job", id)
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		metrics.OpenRouterHooks.Inc("error")
		log.Error("openrouter webhook: look up the job", "job", id, "error", err)
		http.Error(w, "try again", http.StatusInternalServerError)
		return
	}

	signed := job.Free && len(h.SigningSecret) > 0
	if signed {
		if err := verifySignature(r.Header.Get(signatureHeader), body, h.SigningSecret, now()); err != nil {
			metrics.OpenRouterHooks.Inc("bad_signature")
			log.Warn("openrouter webhook refused", "job", id, "reason", err.Error())
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
	}
	if !job.Waiting {
		// Ended, or the id is not saved yet: the poll that is due finds out.
		metrics.OpenRouterHooks.Inc("ignored")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	fresh, woken, err := h.Deliveries.Record(r.Context(), id, deliveryKeys(r, signed))
	switch {
	case err != nil:
		metrics.OpenRouterHooks.Inc("error")
		log.Error("openrouter webhook: record the delivery", "job", id, "error", err)
		http.Error(w, "try again", http.StatusInternalServerError)
		return
	case !fresh:
		metrics.OpenRouterHooks.Inc("duplicate")
		log.Debug("openrouter webhook seen before", "job", id)
	case woken:
		metrics.OpenRouterHooks.Inc("woken")
		log.Debug("openrouter webhook brought a poll forward", "job", id, "signed", signed, "free", job.Free)
	default:
		metrics.OpenRouterHooks.Inc("ignored")
	}
	w.WriteHeader(http.StatusNoContent)
}

// deliveryKeys names a delivery for the table of those already acted on. The
// keys are digests, so the table holds nothing a request could be rebuilt
// from.
func deliveryKeys(r *http.Request, signed bool) []string {
	var keys []string
	if v := strings.TrimSpace(r.Header.Get(idempotencyHeader)); v != "" {
		keys = append(keys, "idem:"+digest(v))
	}
	if signed {
		// The idempotency header is not part of what is signed, so a replay
		// can change it; the signature cannot be changed.
		keys = append(keys, "sig:"+digest(r.Header.Get(signatureHeader)))
	}
	return keys
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Why a signature was refused. They go in the log; the sender is told only
// that the signature is bad.
var (
	errNoSignature    = errors.New("no signature")
	errBadSignature   = errors.New("signature does not match")
	errStaleSignature = errors.New("signature outside its window")
)

// verifySignature checks header, "t=<unix seconds>,v1=<hex>", against body.
// The signed text is t, a comma and the raw body, and the compare is in
// constant time. More than one v1 is allowed: a sender rotating its secret
// signs with both.
func verifySignature(header string, body, secret []byte, now time.Time) error {
	if strings.TrimSpace(header) == "" {
		return errNoSignature
	}
	var t string
	var sigs [][]byte
	for part := range strings.SplitSeq(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		switch {
		case !ok:
		case k == "t":
			t = v
		case k == "v1":
			if sig, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, sig)
			}
		}
	}
	secs, err := strconv.ParseInt(t, 10, 64)
	if err != nil || len(sigs) == 0 {
		return errBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(t + ","))
	mac.Write(body)
	want := mac.Sum(nil)
	match := false
	for _, sig := range sigs {
		// Every candidate is compared, so the number tried says nothing.
		if hmac.Equal(sig, want) {
			match = true
		}
	}
	if !match {
		return errBadSignature
	}
	// OpenRouter's documents say seconds. A number too large to be a date in
	// seconds is read as milliseconds, so a unit mistake on their side does
	// not turn every delivery away.
	if secs >= 1e11 {
		secs /= 1000
	}
	if d := now.Sub(time.Unix(secs, 0)); d > SignatureWindow || d < -SignatureWindow {
		return errStaleSignature
	}
	return nil
}

// HookStore is Deliveries on PostgreSQL. Wake is jobs.Queue.PollNowTx.
type HookStore struct {
	Pool *pgxpool.Pool
	Wake func(ctx context.Context, tx pgx.Tx, jobID int64) (bool, error)
}

// deliveryTTL is how long a delivery is remembered: far longer than any
// sender retries or a signature stays valid.
const deliveryTTL = "7 days"

func (s *HookStore) Job(ctx context.Context, id int64) (HookJob, error) {
	var j HookJob
	var status string
	var providerJobID *string
	err := s.Pool.QueryRow(ctx, `
		SELECT free, status, provider_job_id FROM job WHERE id=$1 AND provider='openrouter'`, id).Scan(&j.Free, &status, &providerJobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return HookJob{}, ErrNoJob
	}
	if err != nil {
		return HookJob{}, fmt.Errorf("look up job %d: %w", id, err)
	}
	j.Waiting = status == "running" && providerJobID != nil
	return j, nil
}

func (s *HookStore) Record(ctx context.Context, id int64, keys []string) (fresh, woken bool, err error) {
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		fresh = true
		if len(keys) > 0 {
			tag, err := tx.Exec(ctx, `
				INSERT INTO webhook_delivery (provider, job_id, key)
				SELECT 'openrouter', $1, k FROM unnest($2::text[]) AS k
				ON CONFLICT DO NOTHING`, id, keys)
			if err != nil {
				return err
			}
			fresh = tag.RowsAffected() == int64(len(keys))
			if _, err := tx.Exec(ctx, `DELETE FROM webhook_delivery WHERE received_at < now() - interval '`+deliveryTTL+`'`); err != nil {
				return err
			}
		}
		if !fresh {
			return nil
		}
		woken, err = s.Wake(ctx, tx, id)
		return err
	})
	return fresh, woken, err
}
