// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// hinted is an adapter's error that knows what it shows about the key.
type hinted struct{ status string }

func (e hinted) Error() string     { return "provider says: " + e.status }
func (e hinted) KeyStatus() string { return e.status }

func TestStatusOf(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provider   string
		err        error
		wantStatus Status
		wantClass  jobs.Class
	}{
		{"a hint from the adapter", catalog.OpenAI, &jobs.Error{Class: jobs.Key, Err: hinted{"needs_verification"}}, NeedsVerification, jobs.Key},
		{"a revoked key", catalog.OpenAI, &jobs.Error{Class: jobs.Key, Err: hinted{"invalid"}}, Invalid, jobs.Key},
		{"no billing at Gemini", catalog.Gemini, &jobs.Error{Class: jobs.Money, Err: hinted{"needs_billing"}}, NeedsBilling, jobs.Money},
		{"a hint through wrapping", catalog.OpenAI, fmt.Errorf("submit: %w", &jobs.Error{Class: jobs.Key, Err: hinted{"invalid"}}), Invalid, jobs.Key},
		{"a hint that is not a problem", catalog.OpenAI, &jobs.Error{Class: jobs.Key, Err: hinted{"verified"}}, "", jobs.Key},
		{"a hint that is not a status", catalog.OpenAI, &jobs.Error{Class: jobs.Key, Err: hinted{"banned"}}, "", jobs.Key},
		{"money at OpenRouter", catalog.OpenRouter, &jobs.Error{Class: jobs.Money, Err: errors.New("402")}, NoCredits, jobs.Money},
		{"money at OpenAI", catalog.OpenAI, &jobs.Error{Class: jobs.Money, Err: errors.New("insufficient_quota")}, NoCredits, jobs.Money},
		// A 429 at Gemini is a quota as often as a missing billing account.
		{"money at Gemini without a hint", catalog.Gemini, &jobs.Error{Class: jobs.Money, Err: errors.New("429")}, "", jobs.Money},
		// A Key error cannot tell a revoked key from a model it may not use.
		{"a key error without a hint", catalog.OpenAI, &jobs.Error{Class: jobs.Key, Err: errors.New("403")}, "", jobs.Key},
		{"a rate limit", catalog.OpenAI, &jobs.Error{Class: jobs.RateLimited}, "", jobs.RateLimited},
		{"not a jobs error", catalog.OpenAI, errors.New("boom"), "", jobs.ProviderFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, class := statusOf(tc.provider, tc.err)
			if status != tc.wantStatus || class != tc.wantClass {
				t.Errorf("status %q class %q, want %q, %q", status, class, tc.wantStatus, tc.wantClass)
			}
		})
	}
}

// note is one write the recorder was asked for.
type note struct {
	id     string
	ok     bool
	status Status
	class  jobs.Class
}

type fakeRecorder struct {
	mu      sync.Mutex
	notes   []note
	err     error
	ctxLive bool
}

func (f *fakeRecorder) markSuccess(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, note{id: id, ok: true})
	f.ctxLive = ctx.Err() == nil
	return f.err
}

func (f *fakeRecorder) markFailure(ctx context.Context, id string, status Status, class jobs.Class) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, note{id: id, status: status, class: class})
	f.ctxLive = ctx.Err() == nil
	return f.err
}

// adapter stands in for an adapter: it asks for its key the way a real one
// does, through JobKey, which notes the connection in the context; here the
// note is made by hand.
type adapter struct {
	id       string // the connection the call uses; empty: JobKey was not reached
	sub      jobs.Submission
	err      error
	poll     jobs.Outcome
	pollErr  error
	sawUse   bool
	callback func(ctx context.Context)
}

func (a *adapter) Submit(ctx context.Context, _ jobs.Job) (jobs.Submission, error) {
	_, a.sawUse = ctx.Value(useKey{}).(*use)
	if a.id != "" {
		noteUse(ctx, a.id)
	}
	if a.callback != nil {
		a.callback(ctx)
	}
	return a.sub, a.err
}

func (a *adapter) Poll(ctx context.Context, _ jobs.Job) (jobs.Outcome, error) {
	_, a.sawUse = ctx.Value(useKey{}).(*use)
	if a.id != "" {
		noteUse(ctx, a.id)
	}
	return a.poll, a.pollErr
}

func tracking(a *adapter) (*tracked, *fakeRecorder, *bytes.Buffer) {
	rec := &fakeRecorder{}
	var logs bytes.Buffer
	return &tracked{rec: rec, log: slog.New(slog.NewTextHandler(&logs, nil)), inner: a}, rec, &logs
}

var paidJob = jobs.Job{ID: 1, UserID: 4242, Provider: catalog.OpenAI}

func okResult() *jobs.Outcome {
	return &jobs.Outcome{Results: []jobs.Result{{ContentType: "image/png", Data: []byte("x")}}}
}

func TestTrackSubmit(t *testing.T) {
	keyErr := &jobs.Error{Class: jobs.Key, Err: hinted{"invalid"}}
	for _, tc := range []struct {
		name string
		a    *adapter
		want []note
	}{
		{"a result", &adapter{id: "c1", sub: jobs.Submission{Outcome: okResult()}}, []note{{id: "c1", ok: true}}},
		{"a job id of a background run", &adapter{id: "c1", sub: jobs.Submission{ProviderJobID: "op-1"}}, []note{{id: "c1", ok: true}}},
		{"a refusal of the key", &adapter{id: "c1", err: keyErr}, []note{{id: "c1", status: Invalid, class: jobs.Key}}},
		{"an error in the outcome", &adapter{id: "c1", sub: jobs.Submission{Outcome: &jobs.Outcome{Err: &jobs.Error{Class: jobs.Money}}}}, []note{{id: "c1", status: NoCredits, class: jobs.Money}}},
		{"a rate limit", &adapter{id: "c1", err: &jobs.Error{Class: jobs.RateLimited}}, []note{{id: "c1", class: jobs.RateLimited}}},
		{"a moderation block says nothing of the key", &adapter{id: "c1", err: &jobs.Error{Class: jobs.Moderation}}, nil},
		{"a refused setting says nothing of the key", &adapter{id: "c1", err: &jobs.Error{Class: jobs.Refused}}, nil},
		{"a run framy did not send says nothing of the key", &adapter{id: "c1", err: &jobs.Error{Class: jobs.Invalid}}, nil},
		{"a run still going", &adapter{id: "c1", sub: jobs.Submission{Outcome: &jobs.Outcome{Running: true}}}, nil},
		{"no key was taken", &adapter{err: keyErr}, nil},
		{"no key was taken, a result", &adapter{sub: jobs.Submission{Outcome: okResult()}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, rec, _ := tracking(tc.a)
			sub, err := tr.Submit(context.Background(), paidJob)
			if !tc.a.sawUse {
				t.Error("the adapter's context carried no use")
			}
			if fmt.Sprint(err) != fmt.Sprint(tc.a.err) || fmt.Sprint(sub) != fmt.Sprint(tc.a.sub) {
				t.Errorf("the answer was changed: %v, %v", sub, err)
			}
			if fmt.Sprint(rec.notes) != fmt.Sprint(tc.want) {
				t.Errorf("notes = %+v, want %+v", rec.notes, tc.want)
			}
		})
	}
}

func TestTrackPoll(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    *adapter
		want []note
	}{
		{"running", &adapter{id: "c1", poll: jobs.Outcome{Running: true}}, nil},
		{"a result", &adapter{id: "c1", poll: *okResult()}, []note{{id: "c1", ok: true}}},
		{"a failure", &adapter{id: "c1", poll: jobs.Outcome{Err: &jobs.Error{Class: jobs.Key, Err: hinted{"invalid"}}}}, []note{{id: "c1", status: Invalid, class: jobs.Key}}},
		{"an error", &adapter{id: "c1", pollErr: &jobs.Error{Class: jobs.Unavailable}}, []note{{id: "c1", class: jobs.Unavailable}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, rec, _ := tracking(tc.a)
			if _, err := tr.Poll(context.Background(), paidJob); fmt.Sprint(err) != fmt.Sprint(tc.a.pollErr) {
				t.Fatal(err)
			}
			if fmt.Sprint(rec.notes) != fmt.Sprint(tc.want) {
				t.Errorf("notes = %+v, want %+v", rec.notes, tc.want)
			}
		})
	}
}

// A free run is paid by framy; whatever it shows is not about a person's key.
func TestTrackLeavesFreeRunsAlone(t *testing.T) {
	a := &adapter{id: "c1", err: &jobs.Error{Class: jobs.Key, Err: hinted{"invalid"}}}
	tr, rec, _ := tracking(a)
	free := paidJob
	free.Free = true
	_, _ = tr.Submit(context.Background(), free)
	_, _ = tr.Poll(context.Background(), free)
	if a.sawUse || len(rec.notes) != 0 {
		t.Errorf("a free run was tracked: use %v, notes %+v", a.sawUse, rec.notes)
	}
}

// The write goes in when the run was cut short by a stop.
func TestTrackWritesAfterTheRunIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &adapter{id: "c1", sub: jobs.Submission{Outcome: okResult()}, callback: func(context.Context) { cancel() }}
	tr, rec, _ := tracking(a)
	_, _ = tr.Submit(ctx, paidJob)
	if len(rec.notes) != 1 || !rec.ctxLive {
		t.Errorf("notes = %+v, context live = %v", rec.notes, rec.ctxLive)
	}
}

// A status that cannot be saved fails nothing and leaks nothing.
func TestTrackSurvivesARecorderError(t *testing.T) {
	a := &adapter{id: "c1", sub: jobs.Submission{Outcome: okResult()}}
	tr, rec, logs := tracking(a)
	rec.err = errors.New("database is down")
	if _, err := tr.Submit(context.Background(), paidJob); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "connection status not saved") {
		t.Errorf("not logged: %q", logs.String())
	}
}

// Two runs at once each report on the connection they used.
func TestTrackKeepsRunsApart(t *testing.T) {
	rec := &fakeRecorder{}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			id := fmt.Sprintf("c%d", i)
			tr := &tracked{rec: rec, log: slog.New(slog.DiscardHandler), inner: &adapter{id: id, sub: jobs.Submission{Outcome: okResult()}}}
			_, _ = tr.Submit(context.Background(), paidJob)
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, n := range rec.notes {
		seen[n.id] = true
	}
	if len(rec.notes) != 20 || len(seen) != 20 {
		t.Errorf("%d notes for %d connections", len(rec.notes), len(seen))
	}
}

func TestNilServiceTracksNothing(t *testing.T) {
	inner := &adapter{}
	var s *Service
	if got := s.Track(inner); got != jobs.Provider(inner) {
		t.Error("a nil service wrapped the adapter")
	}
}
