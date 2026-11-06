// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// The ways a check ends without a key that passed. Each wraps nothing from
// the provider's answer: OpenAI's 401 message echoes a prefix of the key.
var (
	// ErrRejected: the provider refused the key itself: a 401, or Gemini's
	// 400 API_KEY_INVALID. It is revoked, mistyped or not a key of this
	// provider. Also OpenAI's 401 for a key that an IP allowlist keeps
	// framy's server away from, which the answer does not set apart.
	ErrRejected = errors.New("the provider does not accept this key")
	// ErrForbidden: the provider knows the key but refuses it the list of
	// models, a 403. The key may be restricted to other calls, or to other
	// addresses. A restricted OpenAI key that cannot list models might still
	// generate; the list is all a check can see, so nothing is said of such
	// a key.
	ErrForbidden = errors.New("the provider refuses this key the list of models")
	// ErrRegion: the provider does not serve the country framy's server is
	// in: Gemini's 400 "User location is not supported", OpenAI's 403
	// unsupported_country_region_territory. Nothing is known of the key, and
	// no run on it could go until the server's address changes.
	ErrRegion = errors.New("the provider does not serve the country framy's server is in")
	// ErrUnavailable: the check did not reach an answer about the key: the
	// network, a 429 or a 5xx. Nothing is known; try again.
	ErrUnavailable = errors.New("the key could not be checked just now")
)

// The provider's roots. A check appends its path.
const (
	DefaultOpenAIBase     = "https://api.openai.com"
	DefaultGeminiBase     = "https://generativelanguage.googleapis.com"
	DefaultOpenRouterBase = "https://openrouter.ai/api/v1"
)

// maxCheckBody bounds what is read of an answer. A Gemini error is parsed, and
// it is a few hundred bytes. OpenAI's list of models is read too and is a few
// tens of kilobytes at most; one that does not fit is not read, and the check
// stands.
const maxCheckBody = 64 << 10

type checker struct {
	http      *http.Client
	userAgent string
	bases     map[string]string
	// listed hears the models a provider's list named when a check passed:
	// OpenAI's, with the day each one is shut down. Nil hears nothing.
	listed func(provider string, models []catalog.Listed)
}

// check asks the provider whether it accepts the key, with a call that costs
// nothing: the list of models, or for OpenRouter the key's own record. The key
// goes in a header, never in the URL, which a log would carry.
func (c *checker) check(ctx context.Context, provider, key string) error {
	var target, header string
	switch provider {
	case catalog.OpenAI:
		target, header = c.bases[provider]+"/v1/models", "Authorization"
	case catalog.Gemini:
		target, header = c.bases[provider]+"/v1beta/models?pageSize=1", "x-goog-api-key"
	case catalog.OpenRouter:
		target, header = c.bases[provider]+"/key", "Authorization"
	default:
		return ErrUnsupportedProvider
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%w: build the request", ErrUnavailable)
	}
	if header == "Authorization" {
		req.Header.Set(header, "Bearer "+key)
	} else {
		req.Header.Set(header, key)
	}
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The cause of a url.Error: its URL is not secret, but it is not
		// worth carrying either.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%w: %s did not answer: %v", ErrUnavailable, providerName(provider), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxCheckBody))
	err = verdict(provider, resp.StatusCode, body)
	if err == nil && c.listed != nil && provider == catalog.OpenAI {
		if models := openAIModels(body); len(models) > 0 {
			c.listed(provider, models)
		}
	}
	return err
}

// openAIModels reads the ids and shutdown days of GET /v1/models. The list is
// a few hundred models at most and fits maxCheckBody; one that does not
// parses as nothing, and the check stands without it. The ids are the
// account's own and go to the operator chat, so only those that look like a
// model id are kept.
func openAIModels(body []byte) []catalog.Listed {
	var list struct {
		Data []struct {
			ID           string  `json:"id"`
			ShutdownDate *string `json:"shutdown_date"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	var out []catalog.Listed
	for _, m := range list.Data {
		if len(m.ID) == 0 || len(m.ID) > 80 || strings.ContainsAny(m.ID, " \t\r\n/") {
			continue
		}
		l := catalog.Listed{ID: m.ID}
		if m.ShutdownDate != nil {
			l.ShutsDown = *m.ShutdownDate
		}
		out = append(out, l)
	}
	return out
}

// verdict turns a status into the outcome of a check.
func verdict(provider string, status int, body []byte) error {
	name := providerName(provider)
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized:
		return ErrRejected
	case status == http.StatusBadRequest && provider == catalog.Gemini && geminiKeyInvalid(body):
		// Google answers a bad key with a 400, not a 401.
		return ErrRejected
	case regionRefused(provider, status, body):
		// Not the key: framy's server, which the person cannot change.
		return ErrRegion
	case status == http.StatusForbidden:
		return ErrForbidden
	}
	// A 429, a 5xx, or something this code does not expect: it says nothing
	// about the key.
	return fmt.Errorf("%w: %s answered %d", ErrUnavailable, name, status)
}

// locationWords are what Google's 400 says when it does not serve the
// country a request came from, as the Gemini adapter reads it.
var locationWords = regexp.MustCompile(`(?i)\blocation is not supported\b`)

// regionRefused reads the answer of a provider that does not serve the
// country framy's server is in: Gemini's 400 FAILED_PRECONDITION "User
// location is not supported for the API use", OpenAI's 403 with the code
// unsupported_country_region_territory. Only the code and the message are
// looked at.
func regionRefused(provider string, status int, body []byte) bool {
	var e struct {
		Error struct {
			// A number in Google's RPC form, a string at OpenAI.
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if json.NewDecoder(bytes.NewReader(body)).Decode(&e) != nil {
		return false
	}
	switch {
	case provider == catalog.Gemini && status == http.StatusBadRequest:
		return locationWords.MatchString(e.Error.Message)
	case provider == catalog.OpenAI && status == http.StatusForbidden:
		var code string
		return json.Unmarshal(e.Error.Code, &code) == nil && code == "unsupported_country_region_territory"
	}
	return false
}

// geminiKeyInvalid reads Google's error: reason API_KEY_INVALID in the
// details, or the message that says so. Only these two fields are looked at.
func geminiKeyInvalid(body []byte) bool {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.NewDecoder(bytes.NewReader(body)).Decode(&e) != nil {
		return false
	}
	for _, d := range e.Error.Details {
		if d.Reason == "API_KEY_INVALID" {
			return true
		}
	}
	return strings.Contains(e.Error.Message, "API key not valid")
}
