// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"testing"
)

// TestBrowserSliderSaysTheValue checks a setting of many values on a slider,
// a video's length: the slider goes by a value's place in the list, and a
// screen reader is told the value, "6 s", not the place, "4".
func TestBrowserSliderSaysTheValue(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+pickerHelpers+`
	await toVideo();
	await chooseFam("Kling Video");
	const row = () => document.querySelector("#how [data-k='h-f-duration']");
	const r = row().querySelector("input[type=range]"), said = () => [r.getAttribute("aria-valuetext"), row().querySelector(".rline b").textContent, r.value];
	const before = said();
	r.value = String(+r.value + 1);
	r.dispatchEvent(new Event("input", { bubbles: true }));
	return { before, after: said() };`)
	for _, k := range []string{"before", "after"} {
		s, _ := got[k].([]any)
		if len(s) != 3 || s[0] == nil || s[0] != s[1] || s[0] == s[2] {
			t.Errorf("%s: the slider says %v, the value shown %v at place %v", k, s[0], s[1], s[2])
		}
	}
	if got["before"].([]any)[0] == got["after"].([]any)[0] {
		t.Errorf("the slider says %v after a move as before it", got["after"])
	}
	noErrors(t, ctx)
}
