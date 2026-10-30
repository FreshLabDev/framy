// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"errors"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// Status is what framy can back up about a connection (docs/design.md,
// Providers and keys). Nobody can read the balance behind an OpenAI or Gemini
// key, so a status is only ever a fact one of framy's own calls showed.
type Status string

const (
	// Valid: the provider accepted the key when it was checked: its list of
	// models answered. That is all it says; the key may still lack billing,
	// verification, credit or the permission to generate.
	Valid Status = "valid"
	// Verified: a generation succeeded on the key.
	Verified Status = "verified"
	// NeedsBilling: the project behind a Gemini key has no billing (a 429
	// with a limit of 0).
	NeedsBilling Status = "needs_billing"
	// NeedsVerification: OpenAI wants the organization verified before the
	// model runs (a 403 about it).
	NeedsVerification Status = "needs_verification"
	// NoCredits: OpenRouter's 402, or OpenAI's insufficient_quota.
	NoCredits Status = "no_credits"
	// Invalid: the provider refused the key itself: revoked or mistyped.
	Invalid Status = "invalid"
)

// problem: the status says something is wrong with the key, as a run's
// failure can show.
func (s Status) problem() bool {
	switch s {
	case NeedsBilling, NeedsVerification, NoCredits, Invalid:
		return true
	}
	return false
}

// Providers are the services a person can connect a key of.
var Providers = []string{catalog.OpenRouter, catalog.OpenAI, catalog.Gemini}

func supported(provider string) bool {
	for _, p := range Providers {
		if p == provider {
			return true
		}
	}
	return false
}

// providerName is how an error names a provider.
func providerName(provider string) string {
	switch provider {
	case catalog.OpenAI:
		return "OpenAI"
	case catalog.Gemini:
		return "Gemini"
	case catalog.OpenRouter:
		return "OpenRouter"
	}
	return provider
}

// StatusHinter is implemented by an adapter's error that knows more about the
// key than its class does. KeyStatus returns one of needs_billing,
// needs_verification, no_credits or invalid; an adapter needs no import of
// this package to say so. Without a hint only a Money error moves a status,
// to no_credits, since a Key error cannot tell a revoked key (401) from one
// that may not use the model (403).
type StatusHinter interface {
	KeyStatus() string
}

// statusOf reads a failed run: the status it shows, empty when it shows none,
// and its class.
func statusOf(provider string, err error) (Status, jobs.Class) {
	class := jobs.ProviderFailed
	var pe *jobs.Error
	if errors.As(err, &pe) {
		class = pe.Class
	}
	var hint StatusHinter
	if errors.As(err, &hint) {
		if s := Status(hint.KeyStatus()); s.problem() {
			return s, class
		}
	}
	// A Gemini 429 is a quota as often as a missing billing account, and
	// only the adapter can tell the two apart.
	if class == jobs.Money && provider != catalog.Gemini {
		return NoCredits, class
	}
	return "", class
}

// keyRelated: the class says something about the key or the account behind
// it. A moderation block and a refused setting are about the request, and a
// run framy did not send says nothing of the key.
func keyRelated(c jobs.Class) bool {
	return c != jobs.Moderation && c != jobs.Refused && c != jobs.Invalid
}
