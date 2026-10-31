// SPDX-License-Identifier: Apache-2.0

// Package metrics exposes framy's counters in the Prometheus text exposition
// format, without a client library: what framy needs for alerting is a few
// counters, two gauges and one histogram. The counters of jobs start from
// zero with the process, as a scraper expects of a counter; the database
// keeps the whole history. Add one with a register function below.
package metrics

import (
	"context"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a monotonically increasing, lock-free metric.
type Counter struct{ n atomic.Int64 }

func (c *Counter) Inc()         { c.n.Add(1) }
func (c *Counter) Add(d int64)  { c.n.Add(d) }
func (c *Counter) value() int64 { return c.n.Load() }

// CounterVec is a counter partitioned by a single label (for example "reason").
// Cells are created lazily on first use.
type CounterVec struct {
	label string
	mu    sync.Mutex
	cells map[string]*atomic.Int64
}

func (c *CounterVec) Inc(labelValue string) {
	c.mu.Lock()
	cell := c.cells[labelValue]
	if cell == nil {
		cell = new(atomic.Int64)
		c.cells[labelValue] = cell
	}
	c.mu.Unlock()
	cell.Add(1)
}

func (c *CounterVec) snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.cells))
	for k, v := range c.cells {
		out[k] = v.Load()
	}
	return out
}

// Gauge is a value that goes up and down, such as what is open now.
type Gauge struct{ n atomic.Int64 }

func (g *Gauge) Add(d int64) { g.n.Add(d) }

// AmountVec counts an amount that is not a whole number, such as dollars,
// partitioned by several labels.
type AmountVec struct {
	labels []string
	mu     sync.Mutex
	cells  map[string]float64 // label values joined by "\xff"
}

// Add adds x to the cell of the label values, one for each label in order.
// A negative or not finite x adds nothing: a counter never goes down.
func (v *AmountVec) Add(x float64, values ...string) {
	if len(values) != len(v.labels) || !(x >= 0) || math.IsInf(x, 1) {
		return
	}
	k := strings.Join(values, "\xff")
	v.mu.Lock()
	v.cells[k] += x
	v.mu.Unlock()
}

// HistogramVec counts observations into buckets, partitioned by one label.
type HistogramVec struct {
	label  string
	bounds []float64
	mu     sync.Mutex
	cells  map[string]*histogram
}

type histogram struct {
	counts []int64 // per bound, not cumulative; the last is above every bound
	sum    float64
}

// Observe counts x under the label value.
func (h *HistogramVec) Observe(labelValue string, x float64) {
	if math.IsNaN(x) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.cells[labelValue]
	if c == nil {
		c = &histogram{counts: make([]int64, len(h.bounds)+1)}
		h.cells[labelValue] = c
	}
	i := sort.SearchFloat64s(h.bounds, x)
	c.counts[i]++
	c.sum += x
}

// GaugeFunc is a gauge read when /metrics is: a number framy keeps
// elsewhere, such as the jobs waiting in the database. Until Set gives it a
// way to read, it has no samples.
type GaugeFunc struct {
	label string
	read  atomic.Pointer[func(context.Context) (map[string]float64, error)]
}

// Set gives the gauge its way to read.
func (g *GaugeFunc) Set(read func(context.Context) (map[string]float64, error)) { g.read.Store(&read) }

type kind string

const (
	counter       kind = "counter"
	gauge         kind = "gauge"
	histogramKind kind = "histogram"
)

type entry struct {
	name    string
	help    string
	kind    kind
	counter *Counter
	vec     *CounterVec
	gauge   *Gauge
	amounts *AmountVec
	hist    *HistogramVec
	fn      *GaugeFunc
}

var registry []entry

func registerCounter(name, help string) *Counter {
	c := &Counter{}
	registry = append(registry, entry{name: name, help: help, kind: counter, counter: c})
	return c
}

func registerVec(name, help, label string) *CounterVec {
	v := &CounterVec{label: label, cells: map[string]*atomic.Int64{}}
	registry = append(registry, entry{name: name, help: help, kind: counter, vec: v})
	return v
}

func registerAmounts(name, help string, labels ...string) *AmountVec {
	v := &AmountVec{labels: labels, cells: map[string]float64{}}
	registry = append(registry, entry{name: name, help: help, kind: counter, amounts: v})
	return v
}

func registerGauge(name, help string) *Gauge {
	g := &Gauge{}
	registry = append(registry, entry{name: name, help: help, kind: gauge, gauge: g})
	return g
}

func registerGaugeFunc(name, help, label string) *GaugeFunc {
	g := &GaugeFunc{label: label}
	registry = append(registry, entry{name: name, help: help, kind: gauge, fn: g})
	return g
}

func registerHistogram(name, help, label string, bounds ...float64) *HistogramVec {
	h := &HistogramVec{label: label, bounds: bounds, cells: map[string]*histogram{}}
	registry = append(registry, entry{name: name, help: help, kind: histogramKind, hist: h})
	return h
}

// Process counters. Names follow Prometheus convention: snake_case with a
// _total suffix, and the bot's name in front, like every bot of the family.
var (
	UpdatesHandled = registerCounter("framy_updates_handled_total", "Telegram updates handled to completion.")
	UpdatesFailed  = registerCounter("framy_updates_failed_total", "Telegram updates whose handler returned an error.")
	PollFailures   = registerCounter("framy_poll_failures_total", "getUpdates calls that failed.")
	TouchFailures  = registerCounter("framy_core_touch_failures_total", "core.touch calls that failed.")
	AlbumFailures  = registerCounter("framy_album_note_failures_total", "Album items that could not be saved, so /img may take fewer photos than the album has.")

	CatalogSyncs        = registerCounter("framy_catalog_syncs_total", "Catalog syncs saved.")
	CatalogSyncFailures = registerCounter("framy_catalog_sync_failures_total", "Catalog syncs that failed and changed nothing.")

	FXSyncs        = registerCounter("framy_fx_syncs_total", "Currency rate syncs saved.")
	FXFallbacks    = registerCounter("framy_fx_fallbacks_total", "Rate syncs that took currency-api because the NBU failed.")
	FXSyncFailures = registerCounter("framy_fx_sync_failures_total", "Rate syncs that saved nothing; the last saved rates stay.")

	FreeRefused        = registerVec("framy_free_refused_total", "Free runs refused, by reason: image_limit, video_limit, budget, key, not_free.", "reason")
	FreeClosed         = registerVec("framy_free_closed_total", "Times a fuse closed the free tier for a UTC day, by fuse: budget, key.", "fuse")
	KeyDecryptFailures = registerCounter("framy_key_decrypt_failures_total", "Stored keys that could not be decrypted: a lost or wrong KEK, or a row that was tampered with.")
	KeysInChat         = registerCounter("framy_keys_in_chat_total", "Direct messages that looked like a key and were deleted.")

	TelegramErrors  = registerVec("framy_telegram_errors_total", "Telegram API and transport errors, by method.", "method")
	TelegramLimited = registerVec("framy_telegram_rate_limited_total", "Telegram 429 responses observed, by method.", "method")

	HTTPRateLimited        = registerVec("framy_http_rate_limited_total", "Public HTTP requests answered 429, by rate class.", "class")
	HTTPCrossOriginRefused = registerCounter("framy_http_cross_origin_refused_total", "Public HTTP requests refused as cross-origin.")

	AuthResults      = registerVec("framy_auth_total", "Sign-in outcomes, by result: site_ok, site_denied, site_no_state, site_state, site_exchange, site_token, site_nonce, site_claims, site_provider, site_unavailable, site_error, miniapp_ok, miniapp_stale, miniapp_bad, miniapp_error.", "result")
	OAuthStarted     = registerCounter("framy_oauth_started_total", "OpenRouter connect flows started: people sent to OpenRouter.")
	OAuthCallbacks   = registerVec("framy_oauth_callbacks_total", "OpenRouter connect callbacks, by result: connected, cancelled, expired, signin, refused, unavailable.", "result")
	StreamsOpened    = registerCounter("framy_sse_streams_total", "Job event streams opened.")
	StreamsRefused   = registerCounter("framy_sse_streams_refused_total", "Job event streams refused because the person held the most allowed.")
	EventsReconnects = registerCounter("framy_sse_listener_reconnects_total", "Times the job event listener lost its connection and made it again.")
	OpenRouterHooks  = registerVec("framy_openrouter_hooks_total", "OpenRouter video webhooks, by result: woken, duplicate, ignored, bad_path, bad_signature, error.", "result")
	StreamsOpen      = registerGauge("framy_sse_streams_open", "Job event streams open now.")

	// The jobs: what they cost, how long they took, why they failed, and
	// how many wait.
	JobCost     = registerAmounts("framy_job_cost_usd_total", "What the providers billed for the jobs that ended, in USD, by provider, model and payer: free (framy's key) or key (the person's).", "provider", "model", "payer")
	JobDuration = registerHistogram("framy_job_duration_seconds", "Seconds from the queue to the result of the jobs done, by kind: image or video.", "kind",
		5, 10, 20, 30, 60, 120, 300, 600, 1200, 3600)
	JobFailures = registerVec("framy_job_failures_total", "Jobs that ended without a result, by reason: the error class, such as moderation, provider, timeout or money.", "reason")
	JobsWaiting = registerGaugeFunc("framy_jobs", "Jobs not ended yet, by status: queued or running.", "status")
)

// readWithin is how long a gauge read from elsewhere may take; past it the
// gauge has no samples this time.
const readWithin = 3 * time.Second

// Handler serves the registry in Prometheus text exposition format.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		write(r.Context(), w)
	})
}

// Write renders the registry. Exported for tests.
func Write(w io.Writer) { write(context.Background(), w) }

func write(ctx context.Context, w io.Writer) {
	for _, e := range registry {
		fmt.Fprintf(w, "# HELP %s %s\n", e.name, e.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", e.name, e.kind)
		switch {
		case e.counter != nil:
			fmt.Fprintf(w, "%s %d\n", e.name, e.counter.value())
		case e.gauge != nil:
			fmt.Fprintf(w, "%s %d\n", e.name, e.gauge.n.Load())
		case e.vec != nil:
			snap := e.vec.snapshot()
			for _, k := range sortedKeys(snap) {
				fmt.Fprintf(w, "%s{%s} %d\n", e.name, labels([]string{e.vec.label}, k), snap[k])
			}
		case e.amounts != nil:
			e.amounts.mu.Lock()
			snap := maps.Clone(e.amounts.cells)
			e.amounts.mu.Unlock()
			for _, k := range sortedKeys(snap) {
				fmt.Fprintf(w, "%s{%s} %s\n", e.name, labels(e.amounts.labels, k), number(snap[k]))
			}
		case e.hist != nil:
			writeHistogram(w, e.name, e.hist)
		case e.fn != nil:
			read := e.fn.read.Load()
			if read == nil {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, readWithin)
			snap, err := (*read)(rctx)
			cancel()
			if err != nil {
				continue
			}
			for _, k := range sortedKeys(snap) {
				fmt.Fprintf(w, "%s{%s} %s\n", e.name, labels([]string{e.fn.label}, k), number(snap[k]))
			}
		}
	}
}

func writeHistogram(w io.Writer, name string, h *HistogramVec) {
	h.mu.Lock()
	type row struct {
		counts []int64
		sum    float64
	}
	snap := make(map[string]row, len(h.cells))
	for k, c := range h.cells {
		snap[k] = row{counts: append([]int64(nil), c.counts...), sum: c.sum}
	}
	h.mu.Unlock()
	for _, k := range sortedKeys(snap) {
		c, lv := snap[k], h.label+`="`+escapeLabelValue(k)+`"`
		var total int64
		for i, b := range h.bounds {
			total += c.counts[i]
			fmt.Fprintf(w, "%s_bucket{%s,le=\"%s\"} %d\n", name, lv, number(b), total)
		}
		total += c.counts[len(h.bounds)]
		fmt.Fprintf(w, "%s_bucket{%s,le=\"+Inf\"} %d\n", name, lv, total)
		fmt.Fprintf(w, "%s_sum{%s} %s\n", name, lv, number(c.sum))
		fmt.Fprintf(w, "%s_count{%s} %d\n", name, lv, total)
	}
}

// labels writes the label pairs of a cell whose values are joined by "\xff".
func labels(names []string, joined string) string {
	values := strings.Split(joined, "\xff")
	pairs := make([]string, len(names))
	for i, n := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		pairs[i] = n + `="` + escapeLabelValue(v) + `"`
	}
	return strings.Join(pairs, ",")
}

func number(x float64) string { return strconv.FormatFloat(x, 'g', -1, 64) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// escapeLabelValue escapes a label value per the Prometheus text format:
// backslash, double quote, and newline. Go's %q is close but not identical.
func escapeLabelValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}
