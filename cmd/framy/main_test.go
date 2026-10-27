// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
)

// A run is paid by one key and never falls back to the other: framy's own
// for the free allowance, the person's for everything else.
func TestOpenRouterKeyNeverFallsBack(t *testing.T) {
	var asked []jobs.Job
	people := func(_ context.Context, j jobs.Job) (string, error) {
		asked = append(asked, j)
		if j.UserID == 1 {
			return "the-persons-key", nil
		}
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("the person has no OpenRouter key")}
	}
	ctx := context.Background()

	key := openRouterKey("framys-own-key", people)
	free := jobs.Job{UserID: 1, Free: true}
	if got, err := key(ctx, free); err != nil || got != "framys-own-key" {
		t.Errorf("a free run: %q, %v", got, err)
	}
	if len(asked) != 0 {
		t.Error("a free run looked for the person's key")
	}
	if got, err := key(ctx, jobs.Job{UserID: 1}); err != nil || got != "the-persons-key" {
		t.Errorf("a paid run: %q, %v", got, err)
	}
	// A person without a connection does not get framy's key.
	got, err := key(ctx, jobs.Job{UserID: 2})
	var pe *jobs.Error
	if !errors.As(err, &pe) || pe.Class != jobs.Key || got != "" {
		t.Errorf("a paid run with no connection: %q, %v", got, err)
	}
	// And a free run without framy's key does not get the person's.
	got, err = openRouterKey("", people)(ctx, free)
	if !errors.As(err, &pe) || pe.Class != jobs.Key || got != "" {
		t.Errorf("a free run with no key of framy's: %q, %v", got, err)
	}
	if len(asked) != 2 {
		t.Errorf("the person's key was asked for %d times", len(asked))
	}
}
