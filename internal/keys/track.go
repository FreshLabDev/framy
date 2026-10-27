// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
)

// Track wraps an adapter so that what a run shows about the person's key is
// written down: a generation that succeeded makes the connection Verified; a
// failure sets the connection's last error class, and its status when the
// failure proves something (StatusHinter, or a Money error). Free runs use no
// key of the person's and are passed through. A nil *Service returns the
// adapter as it is.
//
// Which connection a run used is noted by JobKey in the call's context, so a
// report lands on the key the run used, not on one that replaced it since.
func (s *Service) Track(p jobs.Provider) jobs.Provider {
	if s == nil {
		return p
	}
	return &tracked{rec: s, log: s.log, inner: p}
}

// recorder writes down what a run showed about a connection.
type recorder interface {
	markSuccess(ctx context.Context, id string) error
	markFailure(ctx context.Context, id string, status Status, class jobs.Class) error
}

type tracked struct {
	rec   recorder
	log   *slog.Logger
	inner jobs.Provider
}

func (t *tracked) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	if j.Free {
		return t.inner.Submit(ctx, j)
	}
	ctx, u := withUse(ctx)
	sub, err := t.inner.Submit(ctx, j)
	switch {
	case err != nil:
		t.report(ctx, u, j, err)
	case sub.Outcome != nil && sub.Outcome.Err != nil:
		t.report(ctx, u, j, sub.Outcome.Err)
	case sub.ProviderJobID != "" || (sub.Outcome != nil && len(sub.Outcome.Results) > 0):
		// The provider took the request, or answered with the result.
		t.report(ctx, u, j, nil)
	}
	return sub, err
}

func (t *tracked) Poll(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	if j.Free {
		return t.inner.Poll(ctx, j)
	}
	ctx, u := withUse(ctx)
	out, err := t.inner.Poll(ctx, j)
	switch {
	case err != nil:
		t.report(ctx, u, j, err)
	case out.Err != nil:
		t.report(ctx, u, j, out.Err)
	case len(out.Results) > 0:
		t.report(ctx, u, j, nil)
	}
	return out, err
}

// use is the connection a call took its key from.
type use struct {
	mu sync.Mutex
	id string
}

type useKey struct{}

func withUse(ctx context.Context) (context.Context, *use) {
	u := &use{}
	return context.WithValue(ctx, useKey{}, u), u
}

// noteUse is called by JobKey; outside a tracked call it does nothing.
func noteUse(ctx context.Context, id string) {
	if u, ok := ctx.Value(useKey{}).(*use); ok {
		u.mu.Lock()
		u.id = id
		u.mu.Unlock()
	}
}

func (u *use) connection() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.id
}

// report writes down what a call showed: a success when err is nil, a
// failure otherwise. It never fails a run; a status that could not be saved
// is logged.
func (t *tracked) report(ctx context.Context, u *use, j jobs.Job, err error) {
	id := u.connection()
	if id == "" {
		return
	}
	// The write goes in even when the run was cut short.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil {
		if e := t.rec.markSuccess(ctx, id); e != nil {
			t.log.Warn("connection status not saved", "connection", id, "provider", j.Provider, "error", e)
		}
		return
	}
	status, class := statusOf(j.Provider, err)
	if !keyRelated(class) && status == "" {
		return
	}
	if e := t.rec.markFailure(ctx, id, status, class); e != nil {
		t.log.Warn("connection status not saved", "connection", id, "provider", j.Provider, "error", e)
	}
}

func (s *Service) markSuccess(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE connection SET status = 'verified', last_success_at = now(), last_error_class = NULL, updated_at = now()
		WHERE id = $1::uuid`, id)
	return err
}

// markFailure sets the status only when the failure proved one, and the class
// only when it is about the key (keyRelated).
func (s *Service) markFailure(ctx context.Context, id string, status Status, class jobs.Class) error {
	if !keyRelated(class) {
		class = ""
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE connection SET status = COALESCE(NULLIF($2, ''), status),
		  last_error_class = COALESCE(NULLIF($3, ''), last_error_class), updated_at = now()
		WHERE id = $1::uuid`, id, string(status), string(class))
	return err
}
