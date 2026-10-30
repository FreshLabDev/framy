// SPDX-License-Identifier: Apache-2.0
package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
)

// errorBody is OpenAI's error object: {"error": {"message", "type", "param",
// "code"}}. A refusal of the safety system adds the moderation's details,
// which the documentation places beside the code; both places are read.
type errorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
	// ModerationStage and ModerationDetails say where a block happened:
	// "input", "output" or "unknown".
	ModerationStage   string             `json:"moderation_stage"`
	ModerationDetails *moderationDetails `json:"moderation_details"`
}

type moderationDetails struct {
	Stage      string          `json:"moderation_stage"`
	Categories json.RawMessage `json:"categories"`
}

// APIError is a failure OpenAI reported, as the logs and the person's reason
// need it. It carries OpenAI's message but never the prompt, and never a key:
// OpenAI echoes the start of a wrong key in its 401, so keys are cut out of
// every message.
type APIError struct {
	// Status is the HTTP status; 200 for an error inside a successful answer.
	Status int
	// InBody: the error came in a 200, after OpenAI took the request.
	InBody bool
	// Type and Code are OpenAI's, such as image_generation_user_error and
	// moderation_blocked.
	Type, Code string
	Message    string
	// RequestID is OpenAI's x-request-id, the one its support asks for.
	RequestID string
	// Stage is the moderation_stage of a block: input, output or unknown.
	Stage string
	// Categories are the coarse categories of a block, such as sexual.
	Categories []string
}

// Said is OpenAI's words, for the person.
func (e *APIError) Said() string { return e.Message }

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "openai %d", e.Status)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	} else if e.Type != "" {
		b.WriteString(" " + e.Type)
	}
	if e.Stage != "" {
		fmt.Fprintf(&b, " at the %s stage", e.Stage)
	}
	if e.InBody {
		b.WriteString(" after OpenAI took the request")
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if len(e.Categories) > 0 {
		fmt.Fprintf(&b, " [categories: %s]", strings.Join(e.Categories, ", "))
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request %s]", e.RequestID)
	}
	return b.String()
}

// Moderated: OpenAI's safety system understood the request and declined it,
// or declined the image it made.
func (e *APIError) Moderated() bool { return e.Code == "moderation_blocked" || e.Stage != "" }

// NeedsVerification: the model needs a verified organization, which only the
// person can arrange. OpenAI sends it as a plain 403 with this wording.
func (e *APIError) NeedsVerification() bool {
	return e.Status == http.StatusForbidden && strings.Contains(strings.ToLower(e.Message), "verif")
}

// moneyCodes are the codes (or types) that mean credit or a spending limit,
// not a rate: the credit is gone, or the organization's or project's limit
// is reached. Waiting does not help, so they are not retried.
var moneyCodes = map[string]bool{
	"insufficient_quota":                true,
	"credit_balance_exhausted":          true,
	"billing_hard_limit_reached":        true,
	"billing_not_active":                true,
	"organization_spend_limit_exceeded": true,
	"project_spend_limit_exceeded":      true,
	"organization_usage_limit_exceeded": true,
}

// class is what the queue does with the error. Only a refusal before the run
// started is tried again: a 429 for the rate, a 503 for a busy engine. A 500
// or a 502 is not: nobody can tell whether the run was paid.
// KeyStatus tells the store of people's keys (internal/keys) what the answer
// proves about the key: a 401 is a key OpenAI does not take, and a 403 that
// asks for verification is an organization that has to verify. Money errors
// need no hint; their class says it. Anything else proves nothing.
func (e *APIError) KeyStatus() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return "invalid"
	case e.NeedsVerification():
		return "needs_verification"
	}
	return ""
}

func (e *APIError) class() jobs.Class {
	switch s := e.Status; {
	case e.Moderated():
		return jobs.Moderation
	case moneyCodes[e.Code] || moneyCodes[e.Type]:
		return jobs.Money
	case s == http.StatusUnauthorized:
		return jobs.Key
	case s == http.StatusForbidden && e.Code == "unsupported_country_region_territory":
		// The request came from framy's server, so the person's key is not
		// what is wrong.
		return jobs.ProviderFailed
	case s == http.StatusForbidden:
		// A project without access to the model, a missing scope, an
		// organization that is not verified.
		return jobs.Key
	case s == http.StatusNotFound && e.Code == "model_not_found":
		// OpenAI answers this for a model the key's project may not use.
		return jobs.Key
	case s == http.StatusTooManyRequests:
		return jobs.RateLimited
	case s == http.StatusServiceUnavailable:
		return jobs.Unavailable
	case s == http.StatusRequestTimeout || s == http.StatusGatewayTimeout || s == 524:
		// The run may go on, and be paid, after the answer gave up.
		return jobs.Timeout
	case s >= 400 && s < 500:
		return jobs.Refused
	}
	return jobs.ProviderFailed
}

// statusError reads an answer that is not a 200. key is cut out of the
// message.
func statusError(status int, h http.Header, data []byte, key string) *jobs.Error {
	e := &APIError{Status: status, RequestID: scrub(h.Get("X-Request-Id"), key)}
	var env struct {
		Error             *errorBody         `json:"error"`
		ModerationDetails *moderationDetails `json:"moderation_details"`
	}
	if json.Unmarshal(data, &env) == nil && env.Error != nil {
		if env.Error.ModerationDetails == nil {
			env.Error.ModerationDetails = env.ModerationDetails
		}
		e.fill(env.Error, key)
	} else {
		// Not OpenAI's body, such as Cloudflare's page for a 524.
		e.Message = http.StatusText(status)
	}
	pe := &jobs.Error{Class: e.class(), Err: e}
	if pe.Class == jobs.RateLimited || pe.Class == jobs.Unavailable {
		pe.RetryAfter = retryAfter(h, time.Now())
	}
	return pe
}

// bodyError reads an error inside a 200. OpenAI had taken the request, so
// nothing about it is tried again.
func bodyError(b *errorBody, key string) *jobs.Error {
	e := &APIError{InBody: true, Status: http.StatusOK}
	e.fill(b, key)
	class := e.class()
	if class == jobs.RateLimited || class == jobs.Unavailable {
		class = jobs.ProviderFailed
	}
	return &jobs.Error{Class: class, Err: e}
}

func (e *APIError) fill(b *errorBody, key string) {
	e.Message = scrub(strings.TrimSpace(b.Message), key)
	e.Type = scrub(b.Type, key)
	e.Code = scrub(text(b.Code), key)
	e.Stage = b.ModerationStage
	if d := b.ModerationDetails; d != nil {
		if d.Stage != "" {
			e.Stage = d.Stage
		}
		e.Categories = categories(d.Categories)
	}
	// A block with no stage named: the code alone says it.
	if e.Code == "moderation_blocked" && e.Stage == "" {
		e.Stage = "unknown"
	}
}

// categories reads the categories of a block, whether OpenAI lists the names
// or maps each name to whether it was flagged.
func categories(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var flags map[string]any
	if json.Unmarshal(raw, &flags) != nil {
		return nil
	}
	var out []string
	for name, v := range flags {
		if on, ok := v.(bool); ok && on {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func text(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case float64:
		return strconv.Itoa(int(c))
	}
	return ""
}

// keyShape matches a key, or the part of one OpenAI echoes: "sk-proj-****abcd".
var keyShape = regexp.MustCompile(`\bsk-[A-Za-z0-9_*.\-]+`)

// scrub takes a key out of a message that is stored and logged.
func scrub(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "<key>")
	}
	return keyShape.ReplaceAllString(s, "<key>")
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
