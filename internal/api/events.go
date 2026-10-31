// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// channel is the PostgreSQL channel migration 009 notifies on.
const channel = "framy_events"

// hub wakes the streams that follow a job. A wake carries nothing: a stream
// that is woken rereads its job, so what it sends is the row as it is, and a
// burst of changes is one reread. That is also why the hub can never block
// or fall behind: a stream has one wake slot, and a second wake into a full
// slot is the same wake.
type hub struct {
	mu     sync.Mutex
	subs   map[int64]map[*sub]struct{}
	closed chan struct{}
	once   sync.Once
}

func newHub() *hub {
	return &hub{subs: map[int64]map[*sub]struct{}{}, closed: make(chan struct{})}
}

// sub is one stream's place in the hub.
type sub struct {
	// wake has one slot.
	wake chan struct{}
	job  int64
	hub  *hub
}

// subscribe registers a stream that follows job. Subscribe before the first
// read of the job: a change between the two then wakes the stream instead of
// slipping past it.
func (h *hub) subscribe(job int64) *sub {
	s := &sub{wake: make(chan struct{}, 1), job: job, hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[job] == nil {
		h.subs[job] = map[*sub]struct{}{}
	}
	h.subs[job][s] = struct{}{}
	return s
}

func (s *sub) close() {
	h := s.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[s.job], s)
	if len(h.subs[s.job]) == 0 {
		delete(h.subs, s.job)
	}
}

func (s *sub) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// notify wakes the streams of one job.
func (h *hub) notify(job int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[job] {
		s.poke()
	}
}

// notifyAll wakes every stream: after the listener's connection was made
// again, when any notice may have been missed.
func (h *hub) notifyAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.subs {
		for s := range set {
			s.poke()
		}
	}
}

// close ends every stream, now and for good: the process is stopping, and a
// stream that stayed open would hold up the server's shutdown. The clients
// reconnect to the next process with Last-Event-ID.
func (h *hub) close() { h.once.Do(func() { close(h.closed) }) }

// open counts the streams, for tests.
func (h *hub) open() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.subs {
		n += len(set)
	}
	return n
}

// event is the payload of a notice, as migration 009 writes it.
type event struct {
	Job    int64  `json:"job"`
	User   int64  `json:"user"`
	Seq    int64  `json:"seq"`
	Status string `json:"status"`
}

// listener holds the process's one connection that LISTENs, outside the
// pool: a pooled connection would be lent to a query, and the notices that
// arrive while it is lent would wait for it.
type listener struct {
	cfg       *pgx.ConnConfig
	hub       *hub
	log       *slog.Logger
	keepalive time.Duration
	backoff   time.Duration
	// connected, when set, is told each time the connection is up and
	// listening; tests wait on it.
	connected func()
}

const maxBackoff = 30 * time.Second

// run keeps the connection until ctx ends. A connection that is lost is
// made again after a pause that doubles up to 30 s and starts over once a
// connection has lasted. Each new connection wakes every stream, so a notice
// that fell in the gap costs a reread and nothing more.
func (l *listener) run(ctx context.Context) {
	delay := l.backoff
	for ctx.Err() == nil {
		started := time.Now()
		err := l.session(ctx)
		if ctx.Err() != nil {
			return
		}
		metrics.EventsReconnects.Inc()
		l.log.Warn("the event listener lost its connection; connecting again", "error", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if time.Since(started) > 2*maxBackoff {
			delay = l.backoff
		} else {
			delay = min(delay*2, maxBackoff)
		}
	}
}

// session is one connection's life: it ends with the error that ended it.
func (l *listener) session(ctx context.Context) error {
	cfg := l.cfg.Copy()
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = "framy-events"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		// pgx can quote the URL of a failed connection, password included.
		return errors.New("connect: " + redactConnErr(err))
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	// Anything may have changed since the last connection (or since the
	// process started).
	l.hub.notifyAll()
	if l.connected != nil {
		l.connected()
	}
	for {
		wait, cancel := context.WithTimeout(ctx, l.keepalive)
		n, err := conn.WaitForNotification(wait)
		quiet := errors.Is(wait.Err(), context.DeadlineExceeded)
		cancel()
		switch {
		case err == nil:
			var e event
			if json.Unmarshal([]byte(n.Payload), &e) != nil || e.Job == 0 {
				l.log.Warn("an event notice was not understood; waking every stream")
				l.hub.notifyAll()
				continue
			}
			l.hub.notify(e.Job)
		case ctx.Err() != nil:
			return ctx.Err()
		case quiet:
			// Quiet for a keepalive: a connection that died without a word
			// (a firewall, a failover) is found out by asking it something.
			if err := conn.Ping(ctx); err != nil {
				return fmt.Errorf("ping: %w", err)
			}
		default:
			return fmt.Errorf("wait for a notice: %w", err)
		}
	}
}

// redactConnErr is the message of a connection error without anything that
// could be a URL or a password: only the kind of failure.
func redactConnErr(err error) string {
	var pgErr interface{ SQLState() string }
	switch {
	case errors.As(err, &pgErr):
		return "the server refused the connection (" + pgErr.SQLState() + ")"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	return "the server cannot be reached"
}
