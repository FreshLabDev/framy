// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// pathID reads the job id of a /api/jobs/{id} route. A malformed one is a
// job that does not exist.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// get answers GET /api/jobs/{id}: the polling fallback of the stream, with
// the same body as its events.
func (a *API) get(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		notFound(w)
		return
	}
	st, err := a.data.State(r.Context(), user, id)
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a.render(r.Context(), user, st, a.language(r.Context(), r, user)))
}

// lastEventID reads Last-Event-ID: the id of the last event the client got,
// or 0 when it sends none or something else.
func lastEventID(r *http.Request) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("Last-Event-ID")), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// events answers GET /api/jobs/{id}/events: the job's state as the first
// event, then the state again each time it changes, and the stream ends
// after the final one.
//
// Every event is the whole state, not a difference, and its id is the job's
// event_seq. A client that is told twice about a state, or never about one
// that came and went between two looks of the server, still ends with the
// truth. A client that reconnects with Last-Event-ID gets the state only if
// it is newer than that id; if the job ended and the client has the end,
// the answer is 204, which tells EventSource to stop reconnecting.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		notFound(w)
		return
	}
	ctx := r.Context()
	// Subscribe before the first read, so a change in between wakes the loop.
	s := a.hub.subscribe(id)
	defer s.close()

	st, err := a.data.State(ctx, user, id)
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}
	last := lastEventID(r)
	if last > st.Seq {
		// An id the job has not reached is not one this client was given
		// (a restored database, a guess): it has seen nothing of this job.
		last = 0
	}
	if st.Status.Final() && st.Seq <= last {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	select {
	case <-a.hub.closed:
		e := newError(http.StatusServiceUnavailable, codeShuttingDown, "framy is restarting; reconnect")
		e.retryAfter = time.Second
		e.write(w)
		return
	default:
	}
	release, ok := a.streams.acquire(user)
	if !ok {
		metrics.StreamsRefused.Inc()
		e := newError(http.StatusTooManyRequests, codeTooManyStreams, fmt.Sprintf("at most %d event streams at once", a.cfg.MaxStreams))
		e.retryAfter = 5 * time.Second
		e.with("limit", a.cfg.MaxStreams).write(w)
		return
	}
	defer release()
	metrics.StreamsOpened.Inc()

	lang := a.language(ctx, r, user)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	// Some proxies in front of an origin buffer a response unless told not to.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := &eventWriter{w: w, rc: http.NewResponseController(w), timeout: a.cfg.WriteTimeout}

	// The headers go out now, with the reconnect delay, before anything is
	// read or waited for.
	first := fmt.Sprintf("retry: %d\n\n", retryMillis)
	sent := last
	if st.Seq > sent {
		first += a.frame(ctx, user, st, lang)
		sent = st.Seq
	}
	if err := out.write(first); err != nil {
		a.streamEnded(user, id, err)
		return
	}
	done := st.Status.Final()

	ping := time.NewTicker(a.cfg.Ping)
	defer ping.Stop()
	for !done {
		quiet := false
		select {
		case <-ctx.Done():
			return
		case <-a.hub.closed:
			return
		case <-s.wake:
		case <-ping.C:
			// The notices are the way a change is heard, and this is the way
			// to hear one the notices missed.
			quiet = true
		}
		st, err := a.data.State(ctx, user, id)
		switch {
		case errors.Is(err, ErrNotFound):
			// The job went with its owner's account.
			return
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("read a job for its stream", "job", id, "error", err)
			st = State{Seq: sent}
		}
		var frame string
		switch {
		case st.Seq > sent:
			frame, sent, done = a.frame(ctx, user, st, lang), st.Seq, st.Status.Final()
		case quiet:
			frame = ": ping\n\n"
		default:
			continue
		}
		if err := out.write(frame); err != nil {
			a.streamEnded(user, id, err)
			return
		}
	}
}

// frame is one event: the state, with its seq as the id.
func (a *API) frame(ctx context.Context, user int64, st State, lang string) string {
	b, err := json.Marshal(a.render(ctx, user, st, lang))
	if err != nil {
		// A struct of strings and numbers; this cannot happen.
		b = []byte(`{}`)
	}
	return fmt.Sprintf("id: %d\nevent: job\ndata: %s\n\n", st.Seq, b)
}

func (a *API) streamEnded(user, job int64, err error) {
	// A client that went away is not worth a line; one that stopped reading
	// is the write deadline's work.
	if errors.Is(err, context.Canceled) {
		return
	}
	a.log.Debug("event stream ended", "job", job, "error", err)
}

// eventWriter writes to a stream with a deadline on every write. The
// server's own write deadline is off for streams (web.Stream); this is the
// replacement, and it is what keeps a client that stops reading from holding
// the handler for good: its socket buffer fills, the write blocks, and the
// deadline ends it.
type eventWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

func (e *eventWriter) write(s string) error {
	if err := e.rc.SetWriteDeadline(time.Now().Add(e.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := e.w.Write([]byte(s)); err != nil {
		return err
	}
	if err := e.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
