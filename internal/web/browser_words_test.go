// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"testing"
)

// wordsPlan lists Recraft V4.1 Flash as the free plan of images from words,
// as /api/catalog does while its free variant is open.
func wordsPlan(cat map[string]any) {
	free := cat["free"].(map[string]any)
	free["plans"] = append([]any{map[string]any{
		"kind": "image", "max_inputs": 0, "min_inputs": 0, "offer": "openrouter:recraft/recraft-v4.1-flash",
		"settings": map[string]any{"n": 1}, "unmetered": true, "open": []any{"aspect_ratio"},
	}}, free["plans"].([]any)...)
}

// TestBrowserWordsOpenOnTheFreeModel checks that the composer opens on the
// free model from words, says it is not counted, goes to framy's choice when
// a photo comes and back when it leaves, and that a model the person picked
// stays whatever the photos.
func TestBrowserWordsOpenOnTheFreeModel(t *testing.T) {
	ts := fakeSiteWith(t, nil, wordsPlan)
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+slotHelpers+filesHelpers+`
	const out = {};
	const pay = () => document.querySelector("#how .paysec")?.textContent || "";
	out.start = model();
	out.cap = document.querySelector("#pcap").textContent;
	out.pay = pay();
	out.ratio = !!document.querySelector("#how .ratios");
	drop([await png(60, 40)]);
	await wait(1500);
	out.photo = model();
	document.querySelector("#files .thumb .rm").click();
	await wait(1200);
	out.back = model();
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "Nano Banana"; q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	document.querySelector("#pkrows .frow .fhit").click();
	await wait(1200);
	out.picked = model();
	drop([await png(61, 40)]);
	await wait(1500);
	document.querySelector("#files .thumb .rm").click();
	await wait(1200);
	out.kept = model();
	return out;`)
	if got["start"] != "Recraft V4.1 Flash" || got["back"] != "Recraft V4.1 Flash" {
		t.Errorf("no photo: %v, want Recraft V4.1 Flash", got)
	}
	if got["photo"] != "MAI-Image 2.6 Flash" {
		t.Errorf("with a photo: %v, want MAI 2.6 Flash", got)
	}
	if got["cap"] != "Free, not counted in your free runs" {
		t.Errorf("caption %q", got["cap"])
	}
	if got["ratio"] != true {
		t.Errorf("no aspect ratio to choose: %v", got)
	}
	if got["picked"] == "Recraft V4.1 Flash" || got["kept"] != got["picked"] {
		t.Errorf("a picked model gave way: %v", got)
	}
	noErrors(t, ctx)
}
