// SPDX-License-Identifier: Apache-2.0
package main

import (
	"errors"
	"math"
	"runtime/debug"
	"testing"
)

func TestTheHeapIsHeldUnderTheContainersMemory(t *testing.T) {
	t.Cleanup(func() { debug.SetMemoryLimit(math.MaxInt64) })
	env := func(v string) func(string) string { return func(string) string { return v } }
	file := func(s string, err error) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if path != cgroupMemoryMax {
				t.Errorf("read %s", path)
			}
			return []byte(s), err
		}
	}
	if got := limitMemory(env(""), file("1073741824\n", nil)); got != 768<<20 {
		t.Errorf("1 GiB: %d", got)
	}
	if got := debug.SetMemoryLimit(-1); got != 768<<20 {
		t.Errorf("the runtime's limit is %d", got)
	}
	for name, c := range map[string]struct {
		env  string
		file string
		err  error
	}{
		"GOMEMLIMIT wins": {"900MiB", "1073741824", nil},
		"no limit":        {"", "max\n", nil},
		"no cgroup v2":    {"", "", errors.New("no such file")},
		"nonsense":        {"", "lots", nil},
	} {
		if got := limitMemory(env(c.env), file(c.file, c.err)); got != 0 {
			t.Errorf("%s: %d", name, got)
		}
	}
}
