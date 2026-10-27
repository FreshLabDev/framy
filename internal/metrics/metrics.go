// SPDX-License-Identifier: Apache-2.0

// Package metrics exposes a handful of process counters in the Prometheus text
// exposition format, without a client library: what framy needs for alerting
// is a few monotonic counters. Jobs, spending and the free allowance are
// counted from the database, which survives restarts; these cover what never
// becomes a row. Add one with registerCounter or registerVec below.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

type entry struct {
	name    string
	help    string
	counter *Counter
	vec     *CounterVec
}

var registry []entry

func registerCounter(name, help string) *Counter {
	c := &Counter{}
	registry = append(registry, entry{name: name, help: help, counter: c})
	return c
}

func registerVec(name, help, label string) *CounterVec {
	v := &CounterVec{label: label, cells: map[string]*atomic.Int64{}}
	registry = append(registry, entry{name: name, help: help, vec: v})
	return v
}

// Process counters. Names follow Prometheus convention: snake_case with a
// _total suffix, and the bot's name in front, like every bot of the family.
var (
	UpdatesHandled = registerCounter("framy_updates_handled_total", "Telegram updates handled to completion.")
	UpdatesFailed  = registerCounter("framy_updates_failed_total", "Telegram updates whose handler returned an error.")
	PollFailures   = registerCounter("framy_poll_failures_total", "getUpdates calls that failed.")
	TouchFailures  = registerCounter("framy_core_touch_failures_total", "core.touch calls that failed.")

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
)

// Handler serves the registry in Prometheus text exposition format.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		Write(w)
	})
}

// Write renders the registry. Exported for tests.
func Write(w io.Writer) {
	for _, e := range registry {
		fmt.Fprintf(w, "# HELP %s %s\n", e.name, e.help)
		fmt.Fprintf(w, "# TYPE %s counter\n", e.name)
		if e.counter != nil {
			fmt.Fprintf(w, "%s %d\n", e.name, e.counter.value())
			continue
		}
		snap := e.vec.snapshot()
		keys := make([]string, 0, len(snap))
		for k := range snap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s{%s=\"%s\"} %d\n", e.name, e.vec.label, escapeLabelValue(k), snap[k])
		}
	}
}

// escapeLabelValue escapes a label value per the Prometheus text format:
// backslash, double quote, and newline. Go's %q is close but not identical.
func escapeLabelValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}
