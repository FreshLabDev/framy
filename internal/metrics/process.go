// SPDX-License-Identifier: Apache-2.0
package metrics

import (
	"fmt"
	"io"
	"os"
	"runtime"
	rtmetrics "runtime/metrics"
	"strconv"
	"strings"
	"time"
)

// The process and the Go runtime, under the names Prometheus's Go client
// gives them, so a dashboard or an alert written for any Go service reads
// them: memory on its way to the container's limit, goroutines piling up,
// file descriptors, CPU, and when the process started. They are read when
// the page is asked for. The process's own figures come from /proc, so
// they are there on Linux, where framy runs, and absent elsewhere.

// started stands for the process's start: the moment this package was
// initialised, a few milliseconds after it.
var started = time.Now()

type processSample struct {
	name, help string
	kind       kind
	value      float64
}

// userHZ is the clock ticks a second of /proc/self/stat's CPU times, 100 on
// every Linux framy runs on, as Prometheus's own client assumes.
const userHZ = 100

func processSamples() []processSample {
	out := []processSample{
		{"go_goroutines", "Goroutines that currently exist.", gauge, float64(runtime.NumGoroutine())},
	}
	rt := []rtmetrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/total:bytes"},
	}
	rtmetrics.Read(rt)
	if rt[0].Value.Kind() == rtmetrics.KindUint64 {
		out = append(out, processSample{"go_memstats_heap_alloc_bytes", "Heap bytes allocated and still in use.", gauge, float64(rt[0].Value.Uint64())})
	}
	if rt[1].Value.Kind() == rtmetrics.KindUint64 {
		out = append(out, processSample{"go_memstats_sys_bytes", "Bytes of memory the Go runtime obtained from the system.", gauge, float64(rt[1].Value.Uint64())})
	}
	if rss, ok := residentBytes(); ok {
		out = append(out, processSample{"process_resident_memory_bytes", "Resident memory size in bytes.", gauge, rss})
	}
	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		out = append(out, processSample{"process_open_fds", "Open file descriptors.", gauge, float64(len(fds))})
	}
	if cpu, ok := cpuSeconds(); ok {
		out = append(out, processSample{"process_cpu_seconds_total", "User and system CPU time spent, in seconds.", counter, cpu})
	}
	out = append(out, processSample{"process_start_time_seconds", "When the process started, in seconds since the Unix epoch.", gauge, float64(started.Unix())})
	return out
}

func writeProcess(w io.Writer) {
	for _, s := range processSamples() {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", s.name, s.help, s.name, s.kind, s.name, number(s.value))
	}
}

// residentBytes reads the second field of /proc/self/statm, in pages.
func residentBytes() (float64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return float64(pages) * float64(os.Getpagesize()), true
}

// cpuSeconds reads utime and stime, the 14th and 15th fields of
// /proc/self/stat. The second field, the command, is in parentheses and
// may hold spaces, so the fields are counted after its closing one.
func cpuSeconds() (float64, bool) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	return cpuOfStat(string(data))
}

func cpuOfStat(s string) (float64, bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:]) // from the 3rd field on
	if len(f) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(f[11], 10, 64)
	stime, err2 := strconv.ParseUint(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return float64(utime+stime) / userHZ, true
}
