// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"errors"
	"testing"
)

// internal/keys reads this from an adapter's error without importing it.
func TestAnErrorTellsKeysWhatItShowsAboutTheKey(t *testing.T) {
	type hinter interface{ KeyStatus() string }
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"a key OpenRouter does not know": {401, `{"error":{"code":401,"message":"User not found."}}`, "invalid"},
		"a guardrail on the model":       {403, `{"error":{"code":403,"message":"No allowed providers are available"}}`, ""},
		"no credit":                      {402, `{"error":{"code":402,"message":"Insufficient credits"}}`, ""},
		"a rate limit":                   {429, `{"error":{"code":429,"message":"slow down"}}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			var h hinter
			if err := statusError(tc.status, nil, []byte(tc.body)); !errors.As(err, &h) || h.KeyStatus() != tc.want {
				t.Errorf("KeyStatus = %v, want %q", err, tc.want)
			}
		})
	}
}
