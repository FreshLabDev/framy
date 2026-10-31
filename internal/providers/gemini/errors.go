// SPDX-License-Identifier: Apache-2.0
package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
)

// Key statuses a Gemini answer proves (docs/design.md, Providers and keys).
// The connection store of a person's keys writes them.
const (
	// StatusNeedsBilling: the key's project has no billing, so the image
	// models answer 429 with "limit: 0".
	StatusNeedsBilling = "needs_billing"
	// StatusNoCredits: the project's prepaid balance is spent.
	StatusNoCredits = "no_credits"
	// StatusInvalid: the key is wrong, revoked or expired.
	StatusInvalid = "invalid"
)

// KeyError says what a run proved about the person's key. It is the Err of
// a jobs.Error of class Key or Money, so the job fails at once and is never
// sent again, and whoever records the key's status reads it with KeyStatusOf
// or with errors.As into an interface{ KeyStatus() string }, without
// importing this package. The job row keeps it as text too: the error
// column starts "key: gemini key status needs_billing", or "money: …".
type KeyError struct {
	// Status is one of the Status constants, or empty when the answer says
	// the key is refused without saying why (a 403 about a restriction).
	Status string
	// HTTPStatus and Code are the answer's own, for the logs.
	HTTPStatus int
	Code       string
	Message    string
}

func (e *KeyError) Error() string {
	var b strings.Builder
	b.WriteString("gemini key")
	if e.Status != "" {
		b.WriteString(" status " + e.Status)
	}
	if e.HTTPStatus != 0 {
		fmt.Fprintf(&b, " (%d", e.HTTPStatus)
		if e.Code != "" {
			b.WriteString(" " + e.Code)
		}
		b.WriteString(")")
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// KeyStatus is the status the answer proved, or "".
func (e *KeyError) KeyStatus() string { return e.Status }

// KeyStatusOf returns the key status an error from this adapter proved:
// needs_billing, no_credits or invalid; "" for any other error.
func KeyStatusOf(err error) string {
	var k interface{ KeyStatus() string }
	if errors.As(err, &k) {
		return k.KeyStatus()
	}
	return ""
}

// Refusal is a run Gemini declined: a filter blocked the prompt or the
// image, or the model answered in words and drew nothing. It is the Err of
// a jobs.Error of class Moderation, and it never holds the person's prompt.
type Refusal struct {
	// Reason is Google's own word, upper case: IMAGE_SAFETY, PROHIBITED_CONTENT,
	// SAFETY, OTHER, or NO_IMAGE (NO_VIDEO) when the model answered in text.
	Reason string
	// Stage is where it stopped: "prompt" (a blockReason: nothing was made),
	// "result" (a finishReason or an error code: the image was withheld) or
	// "" when the answer does not say.
	Stage string
	// Text is what the model said in place of an image or a video: the
	// explanation a person should read when Reason is NO_IMAGE or NO_VIDEO.
	// Empty otherwise.
	Text string
	// Message is Google's own explanation of the block, when it gave one.
	Message string
}

const (
	stagePrompt = "prompt"
	stageResult = "result"

	// ReasonNoImage: the model answered in text instead of drawing.
	ReasonNoImage = "NO_IMAGE"
	// ReasonNoVideo: the model answered in text instead of making a video.
	// Google documents no such code; framy names a finished video run that
	// holds only words so.
	ReasonNoVideo = "NO_VIDEO"

	// maxText bounds the model's words kept in a refusal: a job row holds
	// 2,000 characters of error.
	maxText = 1000
)

func (r *Refusal) Error() string {
	var b strings.Builder
	switch r.Stage {
	case stagePrompt:
		b.WriteString("gemini blocked the prompt")
	case stageResult:
		b.WriteString("gemini withheld the result")
	default:
		b.WriteString("gemini declined the run")
	}
	b.WriteString(" (" + r.Reason + ")")
	switch {
	case r.Text != "":
		b.WriteString(": the model answered in text: " + r.Text)
	case r.Message != "":
		b.WriteString(": " + r.Message)
	}
	return b.String()
}

// clipText keeps a model's words to what a row can hold, on a rune boundary.
func clipText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxText {
		return s
	}
	cut := maxText
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// declined are the words Google uses for a request, an image or a video it
// will not serve, as an error code of the Interactions API (snake_case) or as
// a blockReason or finishReason (upper case). The table is the API errors
// page's "generation blocked" codes, no_image, and OTHER: an image model
// that answers OTHER with no image has been filtered. The VIDEO_ codes are
// the IMAGE_ ones for Omni, by analogy: Google lists none of its own.
var declined = map[string]bool{
	"SAFETY": true, "RECITATION": true, "LANGUAGE": true, "PROHIBITED_CONTENT": true,
	"SPII": true, "BLOCKLIST": true, "IMAGE_SAFETY": true, "IMAGE_PROHIBITED_CONTENT": true,
	"IMAGE_RECITATION": true, "IMAGE_OTHER": true, "CONTENT_BLOCKED": true,
	"VIDEO_SAFETY": true, "VIDEO_PROHIBITED_CONTENT": true, "VIDEO_RECITATION": true, "VIDEO_OTHER": true,
	ReasonNoImage: true, ReasonNoVideo: true, "OTHER": true,
}

// errorBody is the error object of an answer. The Interactions API writes
// {"error": {"code": "quota_exceeded", "message": "…"}}; the older RPC form
// has a numeric code, a status such as RESOURCE_EXHAUSTED and details.
type errorBody struct {
	Code    any    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Details []any  `json:"details"`
}

// APIError is a failure Gemini reported, for the logs and the person's
// reason.
type APIError struct {
	// Status is the HTTP status, or 0 for an error inside a 200.
	Status int
	// InBody: the error came in a 200, after Google took the request.
	InBody bool
	// Code is the error code in snake_case: the API's own, or the RPC
	// status lower-cased (resource_exhausted).
	Code    string
	Message string
	// Reason is the machine reason of the RPC details, such as
	// API_KEY_INVALID.
	Reason string
	// QuotaZero: the answer names a quota whose limit is 0. A project with
	// no billing has no image quota at all, so this is not a wait.
	QuotaZero bool
	// RetryDelay is Google's own wait, from RetryInfo or the message.
	RetryDelay time.Duration
}

// Said is Google's words, for the person.
func (e *APIError) Said() string { return e.Message }

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("gemini")
	if e.Status != 0 {
		fmt.Fprintf(&b, " %d", e.Status)
	}
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.InBody {
		b.WriteString(" after the request was taken")
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

var (
	// limitZero reads "limit: 0" and not "limit: 0.5" or "limit: 10".
	limitZero = regexp.MustCompile(`(?i)\blimit:\s*0(?:[^\d.]|$)`)
	// retryIn reads "Please retry in 11.89s".
	retryIn = regexp.MustCompile(`(?i)retry in ([0-9.]+)s`)
)

// fill reads the error object into e. key is scrubbed from the message:
// Google does not echo it, but a key must never reach a log.
func (e *APIError) fill(b *errorBody, key string) {
	msg := strings.TrimSpace(b.Message)
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "<key>")
	}
	e.Message = clip(msg)
	switch c := b.Code.(type) {
	case string:
		// The reference calls the code a URI that names the error; its last
		// part is the snake_case word the errors page lists.
		c = strings.TrimSpace(c)
		if i := strings.LastIndexAny(c, "/#"); i >= 0 {
			c = c[i+1:]
		}
		e.Code = strings.ToLower(c)
	case float64:
		if e.Status == 0 {
			e.Status = int(c)
		}
	}
	if e.Code == "" {
		e.Code = strings.ToLower(strings.TrimSpace(b.Status))
	}
	e.QuotaZero = limitZero.MatchString(msg)
	if m := retryIn.FindStringSubmatch(msg); m != nil {
		if secs, err := strconv.ParseFloat(m[1], 64); err == nil {
			e.RetryDelay = time.Duration(secs * float64(time.Second))
		}
	}
	for _, d := range b.Details {
		detail, _ := d.(map[string]any)
		if r, _ := detail["reason"].(string); r != "" && e.Reason == "" {
			e.Reason = r
		}
		if s, _ := detail["retryDelay"].(string); s != "" {
			if dur, err := time.ParseDuration(s); err == nil {
				e.RetryDelay = dur
			}
		}
		violations, _ := detail["violations"].([]any)
		for _, v := range violations {
			violation, _ := v.(map[string]any)
			switch q := violation["quotaValue"].(type) {
			case string:
				if q == "0" {
					e.QuotaZero = true
				}
			case float64:
				if q == 0 {
					e.QuotaZero = true
				}
			}
		}
	}
}

// clip keeps a message to a size a log line and a row can hold.
func clip(s string) string {
	const limit = 600
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// billingWords are what a 400 failed_precondition says when the project has
// no billing, as "Gemini API free tier is not available in your country.
// Please enable billing on your project".
var billingWords = regexp.MustCompile(`(?i)enable billing|billing (is )?(not (enabled|set up)|disabled)|disabled billing`)

// prepayDepleted is the message of a project whose prepaid balance is spent.
var prepayDepleted = regexp.MustCompile(`(?i)prepa(y|id|yment)[^.]*(depleted|exhausted|insufficient)`)

// keyWords are what a 400 says about a key that is wrong or expired.
var keyWords = regexp.MustCompile(`(?i)api key (not valid|expired|invalid)|api_key_invalid|api key not found`)

// classify turns the error into what the queue does with it. Only a refusal
// before the run started is tried again: a 429 that is a wait, a service
// that is down or overloaded. A 500 is not: nobody can tell whether the run
// was paid.
func (e *APIError) classify() *jobs.Error {
	reason := strings.ToUpper(e.Code)
	switch s := e.Status; {
	case declined[reason] && reason != "OTHER":
		return &jobs.Error{Class: jobs.Moderation, Err: &Refusal{Reason: reason, Stage: stageFor(reason), Message: e.Message}}
	case s == http.StatusTooManyRequests && e.QuotaZero:
		// A project with no billing has a quota of 0 for the image models:
		// waiting changes nothing, and the person has to add billing.
		return e.keyError(jobs.Key, StatusNeedsBilling)
	case prepayDepleted.MatchString(e.Message), s == http.StatusPaymentRequired, e.Code == "payment_required":
		return e.keyError(jobs.Money, StatusNoCredits)
	case s == http.StatusTooManyRequests:
		return &jobs.Error{Class: jobs.RateLimited, RetryAfter: e.RetryDelay, Err: e}
	case e.Reason == "API_KEY_INVALID", s == http.StatusUnauthorized, e.Code == "authentication", e.Code == "unauthenticated",
		s == http.StatusBadRequest && keyWords.MatchString(e.Message):
		return e.keyError(jobs.Key, StatusInvalid)
	case s == http.StatusBadRequest && (e.Code == "failed_precondition" || e.Code == "") && billingWords.MatchString(e.Message):
		return e.keyError(jobs.Key, StatusNeedsBilling)
	case s == http.StatusForbidden:
		// A restricted key, or an API that is off in the project.
		return e.keyError(jobs.Key, "")
	case s == http.StatusBadGateway || s == http.StatusServiceUnavailable || s == 529 || e.Code == "service_unavailable":
		return &jobs.Error{Class: jobs.Unavailable, RetryAfter: e.RetryDelay, Err: e}
	case s == http.StatusRequestTimeout || s == http.StatusGatewayTimeout || s == 524 || e.Code == "deadline_exceeded":
		// The run may go on, and be paid, after the answer gave up.
		return &jobs.Error{Class: jobs.Timeout, Err: e}
	case s >= 400 && s < 500 && s != 499:
		return &jobs.Error{Class: jobs.Refused, Err: e}
	}
	return &jobs.Error{Class: jobs.ProviderFailed, Err: e}
}

func (e *APIError) keyError(class jobs.Class, status string) *jobs.Error {
	return &jobs.Error{Class: class, Err: &KeyError{Status: status, HTTPStatus: e.Status, Code: e.Code, Message: e.Message}}
}

// stageFor says where a refusal code stops a run: the image and video codes
// are the output's, the rest do not say.
func stageFor(reason string) string {
	if strings.HasPrefix(reason, "IMAGE_") || strings.HasPrefix(reason, "VIDEO_") || reason == ReasonNoImage || reason == ReasonNoVideo {
		return stageResult
	}
	return ""
}

// statusError reads an answer that is not a 200.
func statusError(status int, h http.Header, data []byte, key string) *jobs.Error {
	e := &APIError{Status: status}
	var env struct {
		Error *errorBody `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil && env.Error != nil {
		e.fill(env.Error, key)
	} else {
		// Not Google's body, such as a proxy's page for a 502.
		e.Message = http.StatusText(status)
	}
	if e.Code == "" {
		// "Any error code not listed falls back to the snake_case version of
		// the HTTP status."
		e.Code = strings.ReplaceAll(strings.ToLower(http.StatusText(status)), " ", "_")
	}
	pe := e.classify()
	if pe.Class == jobs.RateLimited || pe.Class == jobs.Unavailable {
		if wait := retryAfter(h, time.Now()); wait > 0 {
			pe.RetryAfter = wait
		}
	}
	return pe
}

// bodyError reads an error inside a 200. Google had taken the request, so
// nothing about it is tried again.
func bodyError(b *errorBody, key string) *jobs.Error {
	e := &APIError{InBody: true}
	e.fill(b, key)
	pe := e.classify()
	if pe.Class == jobs.RateLimited || pe.Class == jobs.Unavailable {
		pe.Class, pe.RetryAfter = jobs.ProviderFailed, 0
	}
	return pe
}

// retryAfter reads Retry-After as seconds or as a date.
func retryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return max(time.Duration(secs)*time.Second, 0)
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}
