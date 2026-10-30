// SPDX-License-Identifier: Apache-2.0

// Package jobs runs generations. A job is a row in framy.job, which the
// person's surfaces read, and a River job that works it (docs/design.md,
// Jobs). The rules about money live here:
//
//   - the mark that a paid request is out is written before the request goes,
//     so a run cut short by a crash is never sent again;
//   - a provider's job id is written the moment it comes back, so a retry
//     asks about the run instead of paying for another;
//   - only a 429 or a 5xx before the run started is retried;
//   - one person runs at most two jobs at once, and the rest wait in order.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/price"
)

// Status is where a job is:
//
//	draft ──Generate──> queued ──> running ──> done
//	  │                   │           ├──────> failed
//	  └──No / 10 min──> cancelled     └──────> expired
type Status string

const (
	// Draft: a chat preview waiting for "Generate".
	Draft     Status = "draft"
	Queued    Status = "queued"
	Running   Status = "running"
	Done      Status = "done"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
	// Expired: the provider did not finish in time.
	Expired Status = "expired"
)

// Final reports whether the job has ended.
func (s Status) Final() bool {
	return s == Done || s == Failed || s == Cancelled || s == Expired
}

// Surface is where the job was started.
type Surface string

const (
	Site    Surface = "site"
	MiniApp Surface = "mini_app"
	Group   Surface = "group"
	DM      Surface = "dm"
	// Guest is a chat framy is not in, from a guest message.
	Guest Surface = "guest"
)

// New is a job about to be created.
type New struct {
	UserID  int64
	Surface Surface
	// ChatID and MessageID are where the result goes in Telegram and the
	// preview it replaces; zero on the site.
	ChatID    int64
	MessageID int64
	// DedupeKey makes a repeated update or request start nothing: "tg:" and
	// the update id for Telegram, the request's key on the site. Empty: no
	// dedupe.
	DedupeKey string
	Offer     catalog.Offer
	Prompt    string
	Settings  map[string]any
	// Free: the run is on framy's key and the free allowance.
	Free bool
	// Estimate is the price shown before the run (internal/price).
	Estimate *price.Estimate
	// FX is the rate the estimate was shown in the person's currency with;
	// nil when it was shown in USD alone.
	FX *fx.Quote
	// Inputs are the person's files the run takes, in order: ids of
	// framy.asset rows that are theirs.
	Inputs []int64
}

// TelegramUpdate is the dedupe key of a job a Telegram update starts.
func TelegramUpdate(updateID int64) string { return fmt.Sprintf("tg:%d", updateID) }

// Job is a job as the providers see it.
type Job struct {
	ID             int64
	UserID         int64
	Surface        Surface
	ChatID         int64
	MessageID      int64
	OfferID        string
	ModelID        string
	Provider       string
	Kind           catalog.Kind
	Prompt         string
	Settings       map[string]any
	Free           bool
	Status         Status
	IdempotencyKey string
	// Submits counts the paid requests sent.
	Submits     int
	SubmittedAt *time.Time
	// RetryAt is when a request refused with a 429 or a 5xx goes again.
	RetryAt       *time.Time
	ProviderJobID string
	EstimateUSD   *float64
	ActualUSD     *float64
	ErrorClass    Class
	Error         string
	CreatedAt     time.Time
	// Offer is the job's offer as the catalog has it at the run: the
	// provider's model id, the routes and their settings.
	Offer catalog.Offer
	// Inputs are the files the run takes, found by Config.Inputs just before
	// the paid request.
	Inputs []Input
}

// Input is a file a run takes, such as a photo to edit: a link the provider
// can fetch for the length of one call, or a data URL.
type Input struct {
	ContentType string
	URL         string
}

// Inputs finds the files a job takes. An *Error fails the job before
// anything is paid; any other error is tried again.
type Inputs interface {
	Inputs(ctx context.Context, j Job) ([]Input, error)
}

// Admission is a free job about to be created or queued, as the free
// allowance sees it. It is asked inside the transaction that creates or
// queues the job, so the answer and the job are one step.
type Admission struct {
	UserID int64
	// DedupeKey is set for a job about to be created, so a request that
	// started a job already is not asked again; empty for a draft being
	// queued.
	DedupeKey string
	// Offer is the offer's id, and Kind its kind.
	Offer string
	Kind  catalog.Kind
	// Settings and Inputs are what the run was given: the settings, and how
	// many of the person's files it takes.
	Settings map[string]any
	Inputs   int
	// Videos is the length of each clip among the files, 0 where it is not
	// known: the free edit takes a clip up to 5 s.
	Videos []float64
	// EstimateUSD is the price shown before the run; zero when there is none.
	EstimateUSD float64
}

// Admitter decides whether a free job may start: the free allowance
// (internal/free). An error refuses the job and ends the transaction, so
// nothing is created or queued; Enqueue, Draft and Generate return it as it
// is.
type Admitter interface {
	Admit(ctx context.Context, tx pgx.Tx, a Admission) error
}

// SafetyID is how a provider tells framy's people apart in its abuse checks,
// as OpenRouter's user and OpenAI's safety_identifier: a hash of the
// person's id, never the id itself.
func SafetyID(userID int64) string {
	sum := sha256.Sum256([]byte("framy:" + strconv.FormatInt(userID, 10)))
	return hex.EncodeToString(sum[:16])
}

// Provider runs generations at one upstream; the adapters implement it.
type Provider interface {
	// Submit sends the paid request. A provider that answers with the result
	// returns it in the Outcome; one that runs in the background returns its
	// job id, and the result comes from Poll.
	Submit(ctx context.Context, j Job) (Submission, error)
	// Poll asks about a run Submit started. It costs nothing.
	Poll(ctx context.Context, j Job) (Outcome, error)
}

// Submission is what a paid request returned.
type Submission struct {
	ProviderJobID string
	Outcome       *Outcome
}

// Outcome is a run's state at the provider.
type Outcome struct {
	// Running: not finished yet; ask again later.
	Running bool
	Results []Result
	// CostUSD is what the provider charged, when it says.
	CostUSD *float64
	// Err ends the run without a result, as moderation does.
	Err *Error
	// Grounding is what a search-grounded run brought with its result.
	Grounding *Grounding
}

// Grounding is what a run grounded in Google Search brings with its result.
// Google's terms ask that the search suggestions be shown beside it
// (docs/design.md, Search grounding).
type Grounding struct {
	// Suggestions are Google's search suggestions: HTML to show as Google
	// wrote it, in a sandboxed frame.
	Suggestions []string `json:"suggestions,omitempty"`
	// Queries are the searches the model made.
	Queries []string `json:"queries,omitempty"`
	// Sources are the pages the answer cites.
	Sources []Source `json:"sources,omitempty"`
}

// Source is a page a grounded answer cites.
type Source struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// Empty reports whether g holds nothing to show.
func (g *Grounding) Empty() bool {
	return g == nil || len(g.Suggestions)+len(g.Queries)+len(g.Sources) == 0
}

// Result is one file of a finished run: the bytes, or a link to fetch them.
type Result struct {
	ContentType string
	URL         string
	Data        []byte
}

// Sink keeps a run's results in framy's storage the moment they are ready:
// provider links live from one hour to 48.
type Sink interface {
	Keep(ctx context.Context, j Job, results []Result) error
}

// Class is why a job failed, as the person and the logs see it.
type Class string

const (
	// RateLimited and Unavailable: a 429 or a 5xx before the run started.
	// The only errors that are retried.
	RateLimited Class = "rate_limited"
	Unavailable Class = "unavailable"
	// Money: no credit or over the key's limit.
	Money      Class = "money"
	Moderation Class = "moderation"
	// Refused: the provider refused the request itself, such as a setting.
	Refused Class = "refused"
	// Key: the key is wrong, revoked or not allowed the model.
	Key Class = "key"
	// Timeout: no answer in time; the run may have been paid.
	Timeout Class = "timeout"
	// Interrupted: the worker stopped while a paid request was out.
	Interrupted Class = "interrupted"
	// Storage: the results could not be kept.
	Storage Class = "storage"
	// NoAdapter: framy cannot run this provider yet.
	NoAdapter Class = "no_adapter"
	// ProviderFailed: anything else the provider reported.
	ProviderFailed Class = "provider"
	Internal       Class = "internal"
	// DraftExpired: the preview was not confirmed within 10 minutes.
	DraftExpired Class = "draft_expired"
)

// Error is a provider's failure, as the adapter classifies it.
type Error struct {
	Class Class
	// RetryAfter is the provider's own wait, from Retry-After.
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Class)
	}
	return string(e.Class) + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// retryable: the provider refused before the run started, so nothing was
// paid and sending again is safe.
func (e *Error) retryable() bool { return e.Class == RateLimited || e.Class == Unavailable }

// classify reads an error from a provider call made with ctx: the adapter's
// class, or a timeout or a stop for the context's own errors.
func classify(ctx context.Context, err error) *Error {
	var pe *Error
	switch {
	case errors.As(err, &pe):
		return pe
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &Error{Class: Timeout, Err: err}
	case errors.Is(err, context.Canceled), ctx.Err() != nil:
		return &Error{Class: Interrupted, Err: err}
	}
	return &Error{Class: ProviderFailed, Err: err}
}

// ErrNoInput: an input of a new job is not one of the person's files.
var ErrNoInput = errors.New("an input is not one of the person's files")

// ErrNotDraft: the job is no longer a draft: started, cancelled or expired.
var ErrNotDraft = errors.New("the job is not a draft any more")

// ErrRunning: a running job cannot be cancelled; it finishes and goes to the
// history.
var ErrRunning = errors.New("the job is running")
