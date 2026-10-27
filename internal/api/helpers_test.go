// SPDX-License-Identifier: Apache-2.0
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/storage"
	"github.com/FreshLabDev/framy/internal/web"
)

const (
	alice = int64(1001)
	bob   = int64(1002)
)

// ---- fakes of what the API reads and calls ----

// fakeData is the job and asset rows, in memory. A test changes a job with
// set, the way a worker's UPDATE does, and wakes the hub itself when it
// wants a notice to have come.
type fakeData struct {
	mu     sync.Mutex
	states map[int64]State // by job id
	owners map[int64]int64 // job id -> user
	keys   map[string]int64
	assets map[int64]InputAsset
	owner  map[int64]int64 // asset id -> user
	reads  int
	failed error
	// afterRead runs once, right after a read has taken its answer and
	// before the reader has it: a change landing in that gap.
	afterRead func()
}

func newFakeData() *fakeData {
	return &fakeData{states: map[int64]State{}, owners: map[int64]int64{}, keys: map[string]int64{}, assets: map[int64]InputAsset{}, owner: map[int64]int64{}}
}

// set stores a job of user, bumping its seq as the trigger does when the
// status changes.
func (d *fakeData) set(user int64, s State) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.states[s.ID]; ok && s.Seq == 0 {
		s.Seq = old.Seq + 1
	}
	if s.Seq == 0 {
		s.Seq = 1
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	}
	d.states[s.ID], d.owners[s.ID] = s, user
}

func (d *fakeData) State(_ context.Context, userID, id int64) (State, error) {
	d.mu.Lock()
	d.reads++
	if d.failed != nil {
		defer d.mu.Unlock()
		return State{}, d.failed
	}
	s, ok := d.states[id]
	if !ok || d.owners[id] != userID {
		d.mu.Unlock()
		return State{}, ErrNotFound
	}
	hook := d.afterRead
	d.afterRead = nil
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	return s, nil
}

func (d *fakeData) ByKey(_ context.Context, key string) (int64, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, ok := d.keys[key]
	return id, ok, nil
}

func (d *fakeData) Inputs(_ context.Context, userID int64, ids []int64) ([]InputAsset, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []InputAsset
	for _, id := range ids {
		if a, ok := d.assets[id]; ok && d.owner[id] == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

// addAsset gives user a file.
func (d *fakeData) addAsset(user int64, a InputAsset) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.assets[a.ID], d.owner[a.ID] = a, user
}

func (d *fakeData) readCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads
}

// fakeQueue makes a job in fakeData for what it is given.
type fakeQueue struct {
	mu    sync.Mutex
	data  *fakeData
	got   []jobs.New
	err   error
	next  int64
	state State
}

func (q *fakeQueue) Enqueue(_ context.Context, n jobs.New) (int64, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.got = append(q.got, n)
	if q.err != nil {
		return 0, false, q.err
	}
	q.data.mu.Lock()
	id, ok := q.data.keys[n.DedupeKey]
	q.data.mu.Unlock()
	if ok {
		return id, false, nil
	}
	q.next++
	s := q.state
	s.ID, s.Status, s.Kind, s.Offer, s.Free = 100+q.next, jobs.Queued, n.Offer.Kind, n.Offer.ID, n.Free
	if n.Estimate != nil {
		s.EstimateUSD = &n.Estimate.USD
		s.Estimate = n.Estimate
	}
	if n.FX != nil {
		s.Currency, s.PerUSD, s.FXDate = n.FX.Code, n.FX.PerUSD, &n.FX.Date
	}
	q.data.set(n.UserID, s)
	q.data.mu.Lock()
	q.data.keys[n.DedupeKey] = s.ID
	q.data.mu.Unlock()
	return s.ID, true, nil
}

func (q *fakeQueue) last() jobs.New {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.got[len(q.got)-1]
}

func (q *fakeQueue) calls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.got)
}

// fakeStore is the catalog, the rates and what the database knows of a
// person.
type fakeStore struct {
	offers   map[string]catalog.Model
	chosen   map[int64]string
	lang     map[int64]string
	rates    map[string]float64 // units per USD
	noRate   bool
	quoteErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		offers: map[string]catalog.Model{}, chosen: map[int64]string{}, lang: map[int64]string{},
		rates: map[string]float64{fx.UAH: 41.5, fx.EUR: 0.86, fx.PLN: 3.65, fx.BYN: 3.3},
	}
}

func (s *fakeStore) Offer(_ context.Context, id string) (catalog.Model, catalog.Offer, error) {
	m, ok := s.offers[id]
	if !ok {
		return catalog.Model{}, catalog.Offer{}, db.ErrNoOffer
	}
	return m, m.Offers[0], nil
}

func (s *fakeStore) Quote(_ context.Context, code string, day time.Time) (fx.Quote, error) {
	if s.quoteErr != nil {
		return fx.Quote{}, s.quoteErr
	}
	if code == fx.USD {
		return fx.Dollar, nil
	}
	if s.noRate {
		return fx.Quote{}, fx.ErrNoRate
	}
	return fx.Quote{Code: code, PerUSD: s.rates[code], Date: day, Source: fx.NBU}, nil
}

func (s *fakeStore) Currency(_ context.Context, userID int64) (string, error) {
	return s.chosen[userID], nil
}

func (s *fakeStore) UserLang(_ context.Context, userID int64) (string, error) {
	return s.lang[userID], nil
}

// image is an offer of an image model priced $0.04 an image.
func image(id, provider string) catalog.Model {
	of := catalog.Offer{
		ID: id, ModelID: strings.TrimPrefix(id, provider+":"), Provider: provider, ProviderModelID: strings.TrimPrefix(id, provider+":"),
		Kind: catalog.Image, Name: "Test",
		Routes: []catalog.Route{{Prices: []catalog.Price{{Billable: "output_image", Unit: "image", CostUSD: 0.04}}}},
	}
	return catalog.Model{ID: of.ModelID, Kind: catalog.Image, Name: "Test", Offers: []catalog.Offer{of}}
}

type fakeUploads struct {
	mu    sync.Mutex
	err   error
	asset storage.Asset
	got   []byte
	user  int64
	block chan struct{}
	calls int
}

func (u *fakeUploads) Upload(ctx context.Context, userID int64, r io.Reader) (storage.Asset, error) {
	u.mu.Lock()
	block, err, a := u.block, u.err, u.asset
	u.calls++
	u.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return storage.Asset{}, ctx.Err()
		}
	}
	b, rerr := io.ReadAll(r)
	u.mu.Lock()
	u.got, u.user = b, userID
	u.mu.Unlock()
	if rerr != nil {
		return storage.Asset{}, rerr
	}
	return a, err
}

type fakeConns struct {
	list []keys.Connection
	err  error
}

func (c fakeConns) List(context.Context, int64) ([]keys.Connection, error) { return c.list, c.err }

type fakeLinks struct{}

func (fakeLinks) Link(_ context.Context, _, assetID int64, _ string) (string, error) {
	return "https://files.example/obj-" + strconv.FormatInt(assetID, 10) + "?sig=secret", nil
}

// ---- the server ----

// rig is the API on a real listener, behind the real web.Server, with
// fakes behind it. A request names its person in X-Test-User.
type rig struct {
	t     *testing.T
	api   *API
	site  *web.Server
	srv   *httptest.Server
	data  *fakeData
	queue *fakeQueue
	store *fakeStore
	up    *fakeUploads
	conns *fakeConns
	stuck *stuckListener
}

type rigOptions struct {
	cfg     func(*Config)
	proxies []netip.Prefix
	// writeTimeout stands in for the server's WriteTimeout, so a test can
	// show a stream outliving it without waiting ten seconds.
	writeTimeout time.Duration
	readTimeout  time.Duration
	limits       web.Limits
}

func newRig(t *testing.T, opts ...func(*rigOptions)) *rig {
	t.Helper()
	var o rigOptions
	for _, f := range opts {
		f(&o)
	}
	r := &rig{t: t, data: newFakeData(), store: newFakeStore(), up: &fakeUploads{}, conns: &fakeConns{}}
	r.queue = &fakeQueue{data: r.data}
	cfg := Config{
		Queue: r.queue, Store: r.store, Uploads: r.up, Links: fakeLinks{}, Keys: r.conns,
		Person: func(req *http.Request) (int64, bool) {
			id, err := strconv.ParseInt(req.Header.Get("X-Test-User"), 10, 64)
			return id, err == nil
		},
		Ping: time.Hour, WriteTimeout: 500 * time.Millisecond, data: r.data,
	}
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	var err error
	if r.api, err = New(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.api.Close)
	// The limits are the rate limiter's own business and tested there; a
	// test that makes a dozen requests must not run into them.
	wide := web.ClassLimit{IP: web.Bucket{Rate: 1000, Burst: 1000}, Person: web.Bucket{Rate: 1000, Burst: 1000}}
	limits := web.Limits{web.ClassJob: wide, web.ClassUpload: wide}
	if o.limits != nil {
		limits = o.limits
	}
	r.site, err = web.New(web.Config{TrustedProxies: o.proxies, Person: PersonKey(cfg.Person), Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	r.api.Register(r.site)
	r.srv = httptest.NewUnstartedServer(r.site)
	r.stuck = &stuckListener{Listener: r.srv.Listener}
	r.srv.Listener = r.stuck
	hs := r.site.HTTPServer("")
	r.srv.Config.ReadHeaderTimeout, r.srv.Config.ReadTimeout = hs.ReadHeaderTimeout, hs.ReadTimeout
	r.srv.Config.WriteTimeout, r.srv.Config.IdleTimeout = hs.WriteTimeout, hs.IdleTimeout
	if o.writeTimeout > 0 {
		r.srv.Config.WriteTimeout = o.writeTimeout
	}
	if o.readTimeout > 0 {
		r.srv.Config.ReadTimeout = o.readTimeout
	}
	r.srv.Start()
	t.Cleanup(r.srv.Close)
	// Runs before the server is closed: a handler stuck in a write that has
	// no deadline would hold Close, and the test with it, until the timeout.
	t.Cleanup(r.stuck.release)
	return r
}

// stuckListener makes the connections it accepts behave, once stuck is
// set, like those of a client whose receive window is closed: a write blocks
// until its deadline, and if it has none, for good.
type stuckListener struct {
	net.Listener
	stuck atomic.Bool
	mu    sync.Mutex
	conns []*stuckConn
}

// release lets go of every connection, which ends any write held on one.
func (l *stuckListener) release() {
	l.stuck.Store(false)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		c.Close()
	}
}

func (l *stuckListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	sc := &stuckConn{Conn: c, l: l, closed: make(chan struct{})}
	l.mu.Lock()
	l.conns = append(l.conns, sc)
	l.mu.Unlock()
	return sc, nil
}

type stuckConn struct {
	net.Conn
	l         *stuckListener
	mu        sync.Mutex
	deadline  time.Time
	closed    chan struct{}
	closeOnce sync.Once
}

func (c *stuckConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}

func (c *stuckConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

func (c *stuckConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *stuckConn) Write(b []byte) (int, error) {
	if !c.l.stuck.Load() {
		return c.Conn.Write(b)
	}
	c.mu.Lock()
	d := c.deadline
	c.mu.Unlock()
	if d.IsZero() {
		<-c.closed
		return 0, net.ErrClosed
	}
	select {
	case <-time.After(time.Until(d)):
		return 0, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

// do sends a request as a person.
func (r *rig) do(user int64, method, path string, body io.Reader, hdr ...string) *http.Response {
	r.t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, body)
	if err != nil {
		r.t.Fatal(err)
	}
	if user != 0 {
		req.Header.Set("X-Test-User", strconv.FormatInt(user, 10))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// jsonOf reads a JSON answer into a map and closes it.
func jsonOf(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// Reading a stream to its end would hold the test for good.
		t.Fatalf("an event stream (%d) where an answer was expected", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON (%d): %q", resp.StatusCode, b)
	}
	return m
}

func (r *rig) post(user int64, path, key, body string) *http.Response {
	hdr := []string{"Content-Type", "application/json"}
	if key != "" {
		hdr = append(hdr, "Idempotency-Key", key)
	}
	return r.do(user, http.MethodPost, path, strings.NewReader(body), hdr...)
}

// ---- reading a stream ----

// sseEvent is one block of an event stream.
type sseEvent struct {
	ID, Event, Data, Retry string
	Comments               []string
}

// stream is an open event stream read block by block.
type stream struct {
	t      *testing.T
	resp   *http.Response
	blocks chan sseEvent
	cancel context.CancelFunc
}

// openStream opens GET path as a person and returns once the headers are in:
// the server must send them before it has anything to say.
func (r *rig) openStream(user int64, path string, hdr ...string) *stream {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+path, nil)
	if err != nil {
		cancel()
		r.t.Fatal(err)
	}
	req.Header.Set("X-Test-User", strconv.FormatInt(user, 10))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	type result struct {
		resp *http.Response
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		got <- result{resp, err}
	}()
	var resp *http.Response
	select {
	case res := <-got:
		if res.err != nil {
			cancel()
			r.t.Fatal(res.err)
		}
		resp = res.resp
	case <-time.After(3 * time.Second):
		cancel()
		r.t.Fatal("the headers of the stream did not come within 3s: the server holds them back")
	}
	s := &stream{t: r.t, resp: resp, blocks: make(chan sseEvent, 64), cancel: cancel}
	r.t.Cleanup(s.close)
	if resp.StatusCode != http.StatusOK {
		return s
	}
	go func() {
		defer close(s.blocks)
		br := bufio.NewReader(resp.Body)
		var cur sseEvent
		any := false
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if any {
					s.blocks <- cur
				}
				cur, any = sseEvent{}, false
				continue
			}
			any = true
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch {
			case line[0] == ':':
				cur.Comments = append(cur.Comments, strings.TrimPrefix(line[1:], " "))
			case field == "id":
				cur.ID = value
			case field == "event":
				cur.Event = value
			case field == "data":
				cur.Data += value
			case field == "retry":
				cur.Retry = value
			}
		}
	}()
	return s
}

func (s *stream) close() {
	s.cancel()
	s.resp.Body.Close()
}

// next waits for the next block; ok is false if the stream ended first.
func (s *stream) next(within time.Duration) (ev sseEvent, ok bool) {
	s.t.Helper()
	select {
	case ev, ok = <-s.blocks:
		return ev, ok
	case <-time.After(within):
		s.t.Fatalf("nothing came on the stream within %s", within)
		return sseEvent{}, false
	}
}

// event waits for the next block that carries data and decodes it.
func (s *stream) event(within time.Duration) (id int64, job map[string]any) {
	s.t.Helper()
	for {
		ev, ok := s.next(within)
		if !ok {
			s.t.Fatal("the stream ended")
		}
		if ev.Data == "" {
			continue
		}
		if ev.Event != "job" {
			s.t.Fatalf("event %q, want job", ev.Event)
		}
		id, err := strconv.ParseInt(ev.ID, 10, 64)
		if err != nil {
			s.t.Fatalf("event id %q", ev.ID)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ev.Data), &m); err != nil {
			s.t.Fatalf("event data %q", ev.Data)
		}
		return id, m
	}
}

// ended reports whether the stream closes within the time.
func (s *stream) ended(within time.Duration) bool {
	timeout := time.After(within)
	for {
		select {
		case _, ok := <-s.blocks:
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// quiet fails if anything but a comment arrives within the time.
func (s *stream) quiet(within time.Duration) {
	s.t.Helper()
	select {
	case ev, ok := <-s.blocks:
		if ok && ev.Data != "" {
			s.t.Fatalf("an event came that should not have: %+v", ev)
		}
		if !ok {
			s.t.Fatal("the stream ended")
		}
	case <-time.After(within):
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// queued is a job of alice's as a state.
func queuedState(id int64) State {
	return State{ID: id, Status: jobs.Queued, Kind: catalog.Image, Offer: "openrouter:test/image", Free: false}
}

var errBoom = errors.New("boom")

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("an event stream (%d) where an answer was expected", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(bytes.TrimSpace(b))
}

// count is how many streams the person holds.
func (l *streamLimiter) count(user int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n[user]
}
