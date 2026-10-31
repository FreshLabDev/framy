// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
)

const checkedKey = "sk-proj-Zq81nT3vLw5RkE7yUo2Pd9Hs4Bj6Xc0MaGfIt"

// provider serves one canned answer and remembers the request.
type provider struct {
	*httptest.Server
	status int
	body   string
	last   atomic.Pointer[http.Request]
}

func newProvider(t *testing.T, status int, body string) *provider {
	t.Helper()
	p := &provider{status: status, body: body}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.last.Store(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(p.status)
		_, _ = w.Write([]byte(p.body))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *provider) checker() checker {
	return newChecker(Config{
		HTTP:       p.Client(),
		UserAgent:  "framy-test",
		OpenAIBase: p.URL, GeminiBase: p.URL, OpenRouterBase: p.URL + "/api/v1",
	})
}

func TestCheckAsksForTheListOfModels(t *testing.T) {
	for _, tc := range []struct {
		provider, path, query, header, value string
	}{
		{catalog.OpenAI, "/v1/models", "", "Authorization", "Bearer " + checkedKey},
		{catalog.Gemini, "/v1beta/models", "pageSize=1", "x-goog-api-key", checkedKey},
		{catalog.OpenRouter, "/api/v1/key", "", "Authorization", "Bearer " + checkedKey},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			p := newProvider(t, 200, `{"data":[]}`)
			c := p.checker()
			if err := c.check(context.Background(), tc.provider, checkedKey); err != nil {
				t.Fatal(err)
			}
			r := p.last.Load()
			if r.Method != http.MethodGet || r.URL.Path != tc.path || r.URL.RawQuery != tc.query {
				t.Errorf("asked %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			if got := r.Header.Get(tc.header); got != tc.value {
				t.Errorf("%s = %q", tc.header, got)
			}
			// A key in a URL ends up in logs and proxies.
			if strings.Contains(r.URL.String(), checkedKey) || strings.Contains(r.RequestURI, checkedKey[:12]) {
				t.Errorf("the key is in the URL: %s", r.RequestURI)
			}
			if r.Header.Get("User-Agent") != "framy-test" {
				t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
			}
			if tc.provider == catalog.Gemini && r.Header.Get("Authorization") != "" {
				t.Error("Gemini got an Authorization header")
			}
		})
	}
}

const (
	openAIUnauthorized = `{"error":{"message":"Incorrect API key provided: ` + "sk-proj-Zq81********************************IIt" + `. You can find your API key at https://platform.openai.com/account/api-keys.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`
	geminiBadKey       = `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com"}]}}`
	geminiExpired      = `{"error":{"code":400,"message":"API key expired. Please renew the API key.","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`
	geminiMessageOnly  = `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`
	geminiOtherBadArg  = `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`
)

func TestCheckVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		status   int
		body     string
		want     error
	}{
		{"OpenAI accepts", catalog.OpenAI, 200, `{}`, nil},
		{"OpenAI refuses the key", catalog.OpenAI, 401, openAIUnauthorized, ErrRejected},
		{"OpenAI refuses the list", catalog.OpenAI, 403, `{"error":{"message":"You have insufficient permissions for this operation. Missing scopes: api.model.read."}}`, ErrForbidden},
		{"OpenAI is limited", catalog.OpenAI, 429, `{"error":{"code":"rate_limit_exceeded"}}`, ErrUnavailable},
		{"OpenAI is down", catalog.OpenAI, 503, ``, ErrUnavailable},
		{"OpenAI errs", catalog.OpenAI, 500, openAIUnauthorized, ErrUnavailable},
		{"OpenAI is not found", catalog.OpenAI, 404, ``, ErrUnavailable},
		{"OpenAI redirects", catalog.OpenAI, 302, ``, ErrUnavailable},

		{"Gemini accepts", catalog.Gemini, 200, `{}`, nil},
		{"Gemini says the key is invalid", catalog.Gemini, 400, geminiBadKey, ErrRejected},
		{"Gemini says it expired", catalog.Gemini, 400, geminiExpired, ErrRejected},
		{"Gemini says it in words only", catalog.Gemini, 400, geminiMessageOnly, ErrRejected},
		{"Gemini 400 about something else", catalog.Gemini, 400, geminiOtherBadArg, ErrUnavailable},
		{"Gemini 400 that is not JSON", catalog.Gemini, 400, `<html>`, ErrUnavailable},
		{"Gemini 401", catalog.Gemini, 401, `{"error":{"status":"UNAUTHENTICATED"}}`, ErrRejected},
		{"Gemini 403", catalog.Gemini, 403, `{"error":{"status":"PERMISSION_DENIED"}}`, ErrForbidden},
		{"Gemini 429", catalog.Gemini, 429, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`, ErrUnavailable},

		{"OpenRouter accepts", catalog.OpenRouter, 200, `{"data":{}}`, nil},
		{"OpenRouter does not know the key", catalog.OpenRouter, 401, `{"error":{"message":"User not found.","code":401}}`, ErrRejected},
		{"OpenRouter 502", catalog.OpenRouter, 502, ``, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProvider(t, tc.status, tc.body)
			c := p.checker()
			err := c.check(context.Background(), tc.provider, checkedKey)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			// Gemini's 400 about another argument is the only 400 that is
			// not the key's fault, and it is not a verdict on the key.
			if err != nil {
				for _, leak := range []string{checkedKey, checkedKey[:12], "Zq81", "api-keys", "Incorrect"} {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("the error carries the provider's answer or the key: %v", err)
					}
				}
			}
		})
	}
}

func TestCheckUnsupportedProvider(t *testing.T) {
	c := newProvider(t, 200, `{}`).checker()
	if err := c.check(context.Background(), "midjourney", checkedKey); !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("err = %v", err)
	}
}

// A redirect would carry the key's header to another host.
func TestCheckDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(200)
	}))
	defer other.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1beta/models", http.StatusFound)
	}))
	defer redirecting.Close()
	c := newChecker(Config{HTTP: redirecting.Client(), GeminiBase: redirecting.URL})
	err := c.check(context.Background(), catalog.Gemini, checkedKey)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestCheckWhenTheProviderIsUnreachable(t *testing.T) {
	p := newProvider(t, 200, `{}`)
	c := p.checker()
	p.Close()
	err := c.check(context.Background(), catalog.OpenAI, checkedKey)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), checkedKey) {
		t.Errorf("the error holds the key: %v", err)
	}
}

func TestCheckStopsWithItsContext(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	c := newChecker(Config{HTTP: slow.Client(), OpenAIBase: slow.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.check(ctx, catalog.OpenAI, checkedKey); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
