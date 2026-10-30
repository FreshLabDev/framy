// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
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
	Type string
	// Code is the code of the provider behind OpenRouter when OpenRouter
	// passes its answer on, such as
	// InputVideoSensitiveContentDetected.PrivacyInformation from BytePlus.
	Code string
	// Message is the provider's words: OpenRouter's own, or those of the
	// provider whose answer it passed on, without the request id.
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
	// noBody: the answer had no error of OpenRouter's, and Message is the
	// status's name, such as Cloudflare's page for a 524.
	noBody bool
}

// Said is the provider's words, for the person.
func (e *APIError) Said() string {
	if e.noBody {
		return ""
	}
	return e.Message
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("openrouter")
	if e.Status != 0 {
		fmt.Fprintf(&b, " %d", e.Status)
	}
	if e.Type != "" {
		b.WriteString(" " + e.Type)
	}
	if e.Code != "" {
		b.WriteString(" " + e.Code)
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
	if len(e.Reasons) > 0 || e.InputFlagged || e.Type == "content_policy_violation" || e.Type == "refusal" {
		return true
	}
	return e.Status == http.StatusBadRequest && e.Type == "" && safetyWords(e.Message)
}

// safetyPhrases are how a provider's own filter words its refusal when
// OpenRouter passes it on as a bare 400, with no error_type and no reasons.
// OpenAI, live on 27 October 2026: "Your request was rejected by the safety
// system."
var safetyPhrases = []string{"safety system", "moderation_blocked", "safety_violations"}

func safetyWords(message string) bool {
	m := strings.ToLower(message)
	for _, p := range safetyPhrases {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
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
	case facesCode.MatchString(e.Code):
		return jobs.Faces
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

// KeyStatus tells internal/keys what the answer shows about the key. A 401 is
// a key OpenRouter does not know. A 403 shows nothing: it can be a guardrail
// on the model, with the key itself fine. Money errors need no hint; the
// class says it.
func (e *APIError) KeyStatus() string {
	if e.Status == http.StatusUnauthorized || e.Type == "authentication" {
		return "invalid"
	}
	return ""
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
		e.Message, e.noBody = http.StatusText(status), true
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
	e.Code, e.Message = passedOn(strings.TrimSpace(b.Message))
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

// facesCode is a provider's refusal of a real person in an input: BytePlus
// answers Seedance's with InputVideoSensitiveContentDetected.PrivacyInformation
// for a clip and InputImageSensitiveContentDetected.PrivacyInformation for a
// picture (live, 30 October 2026).
var facesCode = regexp.MustCompile(`^Input\w*SensitiveContentDetected\.PrivacyInformation$`)

var (
	// wrapped is the provider's own answer as OpenRouter passes it on in its
	// message: "HTTP 400: {"error": {"code", "message"}}".
	wrapped = regexp.MustCompile(`(?s)^HTTP \d{3}:\s*(\{.*\})$`)
	// requestID is the request id a provider ends its words with.
	requestID = regexp.MustCompile(`(?i)[\s.]*\brequest id:?\s*[\w-]+\.?$`)
)

// passedOn reads OpenRouter's message: when it passes on the provider's own
// answer, the provider's code and its words without the wrapping and the
// request id; any other message as it is.
func passedOn(message string) (code, words string) {
	m := wrapped.FindStringSubmatch(message)
	if m == nil {
		return "", message
	}
	var body struct {
		Error *struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(m[1]), &body) != nil {
		return "", message
	}
	c, w := body.Code, body.Message
	if body.Error != nil {
		c, w = body.Error.Code, body.Error.Message
	}
	if c != nil {
		code = strings.TrimSpace(fmt.Sprint(c))
	}
	if w = strings.TrimSpace(requestID.ReplaceAllString(w, "")); w == "" {
		return code, message
	}
	if !strings.HasSuffix(w, ".") && !strings.HasSuffix(w, "!") && !strings.HasSuffix(w, "?") {
		w += "."
	}
	return code, w
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
