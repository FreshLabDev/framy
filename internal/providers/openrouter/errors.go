// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
)

// errorBody is OpenRouter's error object: {"error": {"code", "message",
// "metadata"}}.
type errorBody struct {
	Code     any            `json:"code"`
	Message  string         `json:"message"`
	Metadata map[string]any `json:"metadata"`
}

// APIError is a failure OpenRouter reported, as the logs and the person's
// reason need it. It never holds the flagged text itself.
type APIError struct {
	// Status is the HTTP status, or the code inside a 200's body.
	Status int
	// InBody: the error came in a 200, after the provider took the request.
	InBody bool
	// Type is metadata.error_type, such as content_policy_violation.
	Type     string
	Message  string
	Provider string
	// Reasons are the moderation's categories.
	Reasons []string
	// InputFlagged: the moderation flagged the prompt or a reference, not
	// the result.
	InputFlagged bool
	// LimitSource says which limit a 402 or a 403 hit, such as
	// openrouter_key_limit.
	LimitSource string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "openrouter %d", e.Status)
	if e.Type != "" {
		b.WriteString(" " + e.Type)
	}
	if e.Provider != "" {
		fmt.Fprintf(&b, " at %s", e.Provider)
	}
	if e.InBody {
		b.WriteString(" after the provider took the request")
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if len(e.Reasons) > 0 {
		fmt.Fprintf(&b, " [reasons: %s]", strings.Join(e.Reasons, ", "))
	}
	if e.InputFlagged {
		b.WriteString(" [the input was flagged]")
	}
	if e.LimitSource != "" {
		fmt.Fprintf(&b, " [limit: %s]", e.LimitSource)
	}
	return b.String()
}

// Moderated: the request was understood and declined by a content filter
// or by the model.
func (e *APIError) Moderated() bool {
	return len(e.Reasons) > 0 || e.InputFlagged || e.Type == "content_policy_violation" || e.Type == "refusal"
}

// budgetLimits are the limits a 403 names when money, not permission, is
// short.
var budgetLimits = map[string]bool{
	"openrouter_credits":          true,
	"openrouter_key_limit":        true,
	"openrouter_guardrail_budget": true,
	"openrouter_workspace_budget": true,
}

// class is what the queue does with the error. Only a refusal before the
// run started is tried again: a 429, a provider that is down or overloaded,
// a 402 for money other runs are holding. A 500 is not: nobody can tell
// whether the run was paid.
func (e *APIError) class() jobs.Class {
	switch s := e.Status; {
	case e.Moderated():
		return jobs.Moderation
	case s == http.StatusPaymentRequired || e.Type == "payment_required":
		return jobs.Money
	case s == http.StatusForbidden && budgetLimits[e.LimitSource]:
		return jobs.Money
	case s == http.StatusUnauthorized || e.Type == "authentication":
		return jobs.Key
	case s == http.StatusForbidden:
		// A guardrail or a key that may not use the model.
		return jobs.Key
	case s == http.StatusTooManyRequests:
		return jobs.RateLimited
	case s == http.StatusBadGateway || s == http.StatusServiceUnavailable || s == 529:
		return jobs.Unavailable
	case s == http.StatusRequestTimeout || s == http.StatusGatewayTimeout || s == 524:
		// The run may go on, and be paid, after the answer gave up.
		return jobs.Timeout
	case s >= 400 && s < 500:
		return jobs.Refused
	}
	return jobs.ProviderFailed
}

// statusError reads an answer that is not a 200.
func statusError(status int, h http.Header, data []byte) *jobs.Error {
	e := &APIError{Status: status}
	var env struct {
		Error *errorBody `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil && env.Error != nil {
		e.fill(env.Error)
	} else {
		// Not OpenRouter's body, such as Cloudflare's page for a 524.
		e.Message = http.StatusText(status)
	}
	pe := &jobs.Error{Class: e.class(), Err: e}
	wait := retryAfter(h, time.Now())
	switch {
	case pe.Class == jobs.RateLimited || pe.Class == jobs.Unavailable:
		pe.RetryAfter = wait
	case status == http.StatusPaymentRequired && e.LimitSource == "openrouter_in_flight_budget" && wait > 0:
		// Runs in progress hold the budget, and it frees up when they end.
		pe.Class, pe.RetryAfter = jobs.RateLimited, wait
	}
	return pe
}

// bodyError reads an error inside a 200. The provider had taken the request,
// so nothing about it is tried again.
func bodyError(b *errorBody) *jobs.Error {
	e := &APIError{InBody: true, Status: code(b.Code)}
	e.fill(b)
	class := e.class()
	if class == jobs.RateLimited || class == jobs.Unavailable {
		class = jobs.ProviderFailed
	}
	return &jobs.Error{Class: class, Err: e}
}

func (e *APIError) fill(b *errorBody) {
	e.Message = strings.TrimSpace(b.Message)
	if e.Status == 0 {
		e.Status = code(b.Code)
	}
	md := b.Metadata
	e.Type = str(md["error_type"])
	e.Provider = str(md["provider_name"])
	e.LimitSource = str(md["limit_source"])
	e.Reasons = strs(md["reasons"])
	switch f := md["flagged_input"].(type) {
	case nil:
	case string:
		e.InputFlagged = f != ""
	default:
		e.InputFlagged = true
	}
}

func code(v any) int {
	switch c := v.(type) {
	case float64:
		return int(c)
	case string:
		n, _ := strconv.Atoi(c)
		return n
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strs(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
			out = append(out, s)
		}
	}
	return out
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
