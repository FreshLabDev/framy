// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The rhythm of the interface is four durations and four curves, written once
// in motion.js and mirrored as tokens in app.css. These tests keep every
// other file from moving by a number of its own, and the two sheets together.

func motionDur(t *testing.T) map[string]int {
	t.Helper()
	src, err := os.ReadFile("app/src/motion.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export const DUR = \{([^}]*)\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no DUR in motion.js")
	}
	dur := map[string]int{}
	for _, kv := range regexp.MustCompile(`(\w+): (\d+)`).FindAllSubmatch(m[1], -1) {
		dur[string(kv[1])], _ = strconv.Atoi(string(kv[2]))
	}
	if len(dur) != 4 {
		t.Fatalf("DUR has %d steps: %v", len(dur), dur)
	}
	return dur
}

func TestMotionTokens(t *testing.T) {
	dur := motionDur(t)
	css, err := os.ReadFile("app/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for name, ms := range dur {
		want := "--dur-" + name + ": " + strconv.Itoa(ms) + "ms"
		if !strings.Contains(string(css), want) {
			t.Errorf("app.css lacks %q, the step motion.js calls %s", want, name)
		}
	}
	js, _ := os.ReadFile("app/src/motion.js")
	spring := regexp.MustCompile(`export const SPRING = "(cubic-bezier\([^)]*\))"`).FindSubmatch(js)
	if spring == nil || !strings.Contains(string(css), "--spring: "+string(spring[1])+";") {
		t.Errorf("app.css --spring is not motion.js's SPRING")
	}
}

// TestMotionNoStrayNumbers: a duration is a step of DUR (or LOOP for a
// repeating motion), a curve is one of the four of motion.js, and a CSS
// transition names its duration by a token.
func TestMotionNoStrayNumbers(t *testing.T) {
	files, err := filepath.Glob("app/src/*.js")
	if err != nil || len(files) == 0 {
		t.Fatal("no sources", err)
	}
	number := regexp.MustCompile(`\b(?:duration|dur)\b["']?\s*[:=]\s*(?:\w+\.dur\s*\|\|\s*)?\d`)
	dataDur := regexp.MustCompile(`data\.?(?:set\.)?dur\s*\|\|\s*\d|"data-dur":\s*\d`)
	for _, f := range files {
		if strings.HasSuffix(f, "motion.js") {
			continue
		}
		src, _ := os.ReadFile(f)
		for i, line := range strings.Split(string(src), "\n") {
			if number.MatchString(line) || dataDur.MatchString(line) {
				t.Errorf("%s:%d: a duration that is not a step of DUR: %s", f, i+1, strings.TrimSpace(line))
			}
			if strings.Contains(line, "cubic-bezier") {
				t.Errorf("%s:%d: a curve of its own; use EASE, OUT, SPRING or SNAP", f, i+1)
			}
		}
	}
	css, _ := os.ReadFile("app/app.css")
	for i, line := range strings.Split(string(css), "\n") {
		for _, tr := range regexp.MustCompile(`(?:transition|animation):[^;}]*`).FindAllString(line, -1) {
			if regexp.MustCompile(`(?:^|[\s,])\.?\d+(?:\.\d+)?m?s\b`).MatchString(tr) || (strings.Contains(tr, "cubic-bezier") && !strings.Contains(line, "--spring:")) {
				t.Errorf("app.css:%d: %q should use the --dur-* and --spring tokens", i+1, tr)
			}
		}
	}
}
