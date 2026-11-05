// SPDX-License-Identifier: Apache-2.0
package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestExpositionFormat(t *testing.T) {
	UpdatesHandled.Inc()
	TelegramErrors.Inc("sendMessage")
	TelegramErrors.Inc("sendMessage")
	TelegramErrors.Inc("getUpdates")

	rr := httptest.NewRecorder()
	Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type = %q", ct)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"# TYPE framy_updates_handled_total counter",
		"framy_updates_handled_total 1",
		`framy_telegram_errors_total{method="sendMessage"} 2`,
		`framy_telegram_errors_total{method="getUpdates"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	// Every metric needs its HELP and TYPE lines or a scraper rejects the page.
	for _, e := range registry {
		if !strings.Contains(body, "# HELP "+e.name+" ") || !strings.Contains(body, "# TYPE "+e.name+" "+string(e.kind)+"\n") {
			t.Fatalf("metric %s is missing HELP or TYPE", e.name)
		}
	}
}

// What the jobs cost adds up by provider, model and payer, in dollars.
func TestAmountsAddUp(t *testing.T) {
	v := registerAmounts("framy_amount_probe_usd_total", "Probe.", "provider", "payer")
	v.Add(0.034, "openrouter", "free")
	v.Add(0.016, "openrouter", "free")
	v.Add(0.25, "openai", "key")
	v.Add(-1, "openai", "key")
	v.Add(1, "openai")
	var sb strings.Builder
	Write(&sb)
	for _, want := range []string{
		`framy_amount_probe_usd_total{provider="openrouter",payer="free"} 0.05`,
		`framy_amount_probe_usd_total{provider="openai",payer="key"} 0.25`,
	} {
		if !strings.Contains(sb.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, sb.String())
		}
	}
}

// A histogram's buckets count every observation at or under their bound.
func TestTheHistogramIsCumulative(t *testing.T) {
	h := registerHistogram("framy_probe_seconds", "Probe.", "kind", 10, 60)
	h.Observe("image", 4)
	h.Observe("image", 10)
	h.Observe("image", 45)
	h.Observe("image", 400)
	var sb strings.Builder
	Write(&sb)
	for _, want := range []string{
		"# TYPE framy_probe_seconds histogram",
		`framy_probe_seconds_bucket{kind="image",le="10"} 2`,
		`framy_probe_seconds_bucket{kind="image",le="60"} 3`,
		`framy_probe_seconds_bucket{kind="image",le="+Inf"} 4`,
		`framy_probe_seconds_sum{kind="image"} 459`,
		`framy_probe_seconds_count{kind="image"} 4`,
	} {
		if !strings.Contains(sb.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, sb.String())
		}
	}
}

// A gauge read from elsewhere has samples once it can be read, and none
// when the read fails.
func TestAGaugeIsReadWhenScraped(t *testing.T) {
	g := registerGaugeFunc("framy_probe_jobs", "Probe.", "status")
	var sb strings.Builder
	Write(&sb)
	if strings.Contains(sb.String(), "framy_probe_jobs{") {
		t.Fatalf("samples before Set:\n%s", sb.String())
	}
	g.Set(func(context.Context) (map[string]float64, error) {
		return map[string]float64{"queued": 3, "running": 1}, nil
	})
	sb.Reset()
	Write(&sb)
	if !strings.Contains(sb.String(), `framy_probe_jobs{status="queued"} 3`) || !strings.Contains(sb.String(), `framy_probe_jobs{status="running"} 1`) {
		t.Fatalf("gauge:\n%s", sb.String())
	}
	g.Set(func(context.Context) (map[string]float64, error) { return nil, errors.New("down") })
	sb.Reset()
	Write(&sb)
	if strings.Contains(sb.String(), "framy_probe_jobs{") || !strings.Contains(sb.String(), "# TYPE framy_probe_jobs gauge") {
		t.Fatalf("a failed read:\n%s", sb.String())
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	vec := registerVec("framy_escaping_probe_total", "Probe.", "reason")
	vec.Inc(`we"ird\value` + "\n")
	var sb strings.Builder
	Write(&sb)
	if !strings.Contains(sb.String(), `framy_escaping_probe_total{reason="we\"ird\\value\n"} 1`) {
		t.Fatalf("label was not escaped:\n%s", sb.String())
	}
}

// Names carry the bot prefix; a counter ends in _total, as a scraper
// expects, and a gauge does not.
func TestNamingConvention(t *testing.T) {
	for _, e := range registry {
		if !strings.HasPrefix(e.name, "framy_") || (e.kind == counter) != strings.HasSuffix(e.name, "_total") {
			t.Fatalf("metric %q breaks the naming convention", e.name)
		}
		if e.help == "" || !strings.HasSuffix(e.help, ".") {
			t.Fatalf("metric %q needs a sentence of help text", e.name)
		}
	}
}

// The page carries the process's and the Go runtime's figures under the
// names any Go service's dashboard knows; the process's own, read from
// /proc, on Linux.
func TestTheProcessIsOnThePage(t *testing.T) {
	var sb strings.Builder
	Write(&sb)
	body := sb.String()
	want := []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "go_memstats_sys_bytes", "process_start_time_seconds"}
	if runtime.GOOS == "linux" {
		want = append(want, "process_resident_memory_bytes", "process_open_fds", "process_cpu_seconds_total")
	}
	for _, name := range want {
		kind := "gauge"
		if strings.HasSuffix(name, "_total") {
			kind = "counter"
		}
		if !strings.Contains(body, "# HELP "+name+" ") || !strings.Contains(body, "# TYPE "+name+" "+kind+"\n") {
			t.Fatalf("%s is missing its HELP or TYPE in:\n%s", name, body)
		}
		var v float64
		found := false
		for _, line := range strings.Split(body, "\n") {
			if rest, ok := strings.CutPrefix(line, name+" "); ok {
				x, err := strconv.ParseFloat(rest, 64)
				if err != nil {
					t.Fatalf("%s: %v", line, err)
				}
				v, found = x, true
			}
		}
		if !found || !(v > 0) {
			t.Errorf("%s = %v, found %v; want a figure above 0", name, v, found)
		}
	}
}

func TestCPUSecondsReadPastACommandWithSpaces(t *testing.T) {
	// utime 250 and stime 50 ticks: 3 s.
	stat := "4242 (framy (a b)) S 1 4242 4242 0 -1 4194560 1200 0 0 0 250 50 0 0 20 0 12 0 1500 1300000000 9000 18446744073709551615\n"
	if cpu, ok := cpuOfStat(stat); !ok || cpu != 3 {
		t.Errorf("cpu = %v, %v; want 3 s", cpu, ok)
	}
	if _, ok := cpuOfStat("4242 (framy) S 1"); ok {
		t.Error("a short line read as CPU time")
	}
}
