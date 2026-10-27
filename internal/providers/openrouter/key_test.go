// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
)

func keyServer(t *testing.T, answer func(http.ResponseWriter)) (*Provider, *http.Header) {
	t.Helper()
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/key" {
			t.Errorf("%s %s, want GET /api/v1/key", r.Method, r.URL.Path)
		}
		seen = r.Header.Clone()
		answer(w)
	}))
	t.Cleanup(srv.Close)
	return New(Config{Base: srv.URL + "/api/v1", HTTP: srv.Client(), UserAgent: "framy-test"}), &seen
}

func TestKeyInfoReadsTheLimit(t *testing.T) {
	p, seen := keyServer(t, reply(200, `{"data":{"label":"sk-or-v1-abc...xyz","limit":2,"limit_remaining":1.25,"limit_reset":"daily","usage":10.5,"usage_daily":0.75,"is_free_tier":false}}`))
	info, err := p.KeyInfo(context.Background(), "sk-or-v1-secret")
	if err != nil {
		t.Fatal(err)
	}
	if info.Limit == nil || *info.Limit != 2 || info.Remaining == nil || *info.Remaining != 1.25 || info.Reset != "daily" || info.Usage != 10.5 || info.UsageDaily != 0.75 {
		t.Fatalf("info = %+v", info)
	}
	if got := seen.Get("Authorization"); got != "Bearer sk-or-v1-secret" || seen.Get("User-Agent") != "framy-test" {
		t.Fatalf("headers = %v", *seen)
	}
}

func TestAKeyWithoutALimitSaysSo(t *testing.T) {
	p, _ := keyServer(t, reply(200, `{"data":{"limit":null,"limit_remaining":null,"limit_reset":null,"usage":3,"usage_daily":0}}`))
	info, err := p.KeyInfo(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if info.Limit != nil || info.Remaining != nil || info.Reset != "" || info.Usage != 3 {
		t.Fatalf("info = %+v; an unlimited key has no limit to run out of", info)
	}
}

func TestKeyInfoErrorsAreClassifiedAndNeverNameTheKey(t *testing.T) {
	const secret = "sk-or-v1-do-not-print"
	for name, c := range map[string]struct {
		answer func(http.ResponseWriter)
		class  jobs.Class
	}{
		"a key OpenRouter does not know": {reply(401, `{"error":{"message":"User not found.","code":401}}`), jobs.Key},
		"OpenRouter being down":          {reply(503, `{"error":{"message":"down","code":503}}`, "Retry-After", "7"), jobs.Unavailable},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := keyServer(t, c.answer)
			_, err := p.KeyInfo(context.Background(), secret)
			if pe := classOf(t, err); pe.Class != c.class {
				t.Fatalf("class = %s, want %s", pe.Class, c.class)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("the error names the key: %v", err)
			}
		})
	}
	t.Run("an answer that is not the key's", func(t *testing.T) {
		p, _ := keyServer(t, reply(200, `<html>hi</html>`))
		if _, err := p.KeyInfo(context.Background(), secret); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("err = %v", err)
		}
	})
}
