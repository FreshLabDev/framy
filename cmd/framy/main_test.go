// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
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

type fakeKEK struct {
	rotated, inspected int
	rotate             keys.RotateResult
	state              keys.KEKState
}

func (f *fakeKEK) Rotate(context.Context) (keys.RotateResult, error) {
	f.rotated++
	return f.rotate, nil
}

func (f *fakeKEK) Inspect(context.Context) (keys.KEKState, error) {
	f.inspected++
	return f.state, nil
}

// Only production rewraps the data keys: the test bot has a copy of its KEK
// file, and the table is shared, so a rotation there could move every key to
// a version production lacks. The test bot says what it sees instead.
func TestOnlyProductionRotatesTheKEK(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	prod := &fakeKEK{rotate: keys.RotateResult{Current: 2, Rewrapped: 3}}
	checkKEK(ctx, prod, true, []int{1, 2}, log)
	if prod.rotated != 1 || prod.inspected != 0 {
		t.Errorf("production: rotated %d, inspected %d", prod.rotated, prod.inspected)
	}

	for _, c := range []struct {
		state keys.KEKState
		want  string
	}{
		{keys.KEKState{Current: 2}, "level=INFO msg=\"keys ready\""},
		{keys.KEKState{Current: 2, Older: 4}, "level=WARN msg=\"stored keys are on an older KEK version"},
		{keys.KEKState{Current: 1, Unreadable: 4}, "level=ERROR msg=\"stored keys are wrapped under a KEK version this file lacks"},
	} {
		logs.Reset()
		test := &fakeKEK{state: c.state}
		checkKEK(ctx, test, false, []int{1, 2}, log)
		if test.rotated != 0 || test.inspected != 1 {
			t.Errorf("%+v: the test stack rotated %d times, inspected %d", c.state, test.rotated, test.inspected)
		}
		if !strings.Contains(logs.String(), c.want) {
			t.Errorf("%+v: logged %q, want %q", c.state, logs.String(), c.want)
		}
	}
}
