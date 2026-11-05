// SPDX-License-Identifier: Apache-2.0
package main

import (
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// cgroupMemoryMax is where cgroup v2 says how much memory the container may
// use: a number of bytes, or "max".
const cgroupMemoryMax = "/sys/fs/cgroup/memory.max"

// memoryShare is the part of the container's memory the Go heap is held to.
// The rest is for FFmpeg, which runs in the same cgroup, and for what the
// runtime does not count.
const memoryShare = 0.75

// limitMemory holds the Go heap to three quarters of the container's memory,
// as GOMEMLIMIT would: the collector then works harder as the heap nears it,
// and the garbage of large answers is freed before the kernel kills the
// whole process, the bot and the site with it. GOMEMLIMIT, when set, wins.
// It returns the limit set, or 0.
func limitMemory(getenv func(string) string, read func(string) ([]byte, error)) int64 {
	if getenv("GOMEMLIMIT") != "" {
		return 0
	}
	raw, err := read(cgroupMemoryMax)
	if err != nil {
		return 0
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || total <= 0 || total == math.MaxInt64 {
		// "max": no limit to keep under.
		return 0
	}
	limit := int64(float64(total) * memoryShare)
	debug.SetMemoryLimit(limit)
	return limit
}

// limitMemoryHere reads this process's environment and cgroup.
func limitMemoryHere() int64 { return limitMemory(os.Getenv, os.ReadFile) }
