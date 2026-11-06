// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"fmt"
	"strings"
	"testing"
)

// gifOffers gives the direct OpenAI offers of GPT Image 2.5 what 2.6 has
// (catalog/openai.yaml): a GIF among the formats, its frames and their
// length only with it, and a background only without it. The fixture has
// no 2.6.
func gifOffers(cat map[string]any) {
	only := func(v ...any) map[string]any { return map[string]any{"output_format": v} }
	for _, m := range cat["models"].([]any) {
		if !strings.HasPrefix(m.(map[string]any)["id"].(string), "openai/gpt-image-2.5") {
			continue
		}
		for _, o := range m.(map[string]any)["offers"].([]any) {
			o := o.(map[string]any)
			if o["provider"] != "openai" {
				continue
			}
			p := o["params"].(map[string]any)
			p["output_format"] = map[string]any{"type": "enum", "values": []any{"png", "jpeg", "webp", "gif"}, "default": "png"}
			p["background"].(map[string]any)["only"] = only("png", "jpeg", "webp")
			p["frame_count"] = map[string]any{"type": "range", "min": 2, "max": 16, "default": 9, "only": only("gif")}
			p["frame_duration_ms"] = map[string]any{"type": "range", "min": 20, "max": 2000, "default": 200, "only": only("gif")}
		}
	}
}

// TestBrowserGIF checks GPT Image's GIF on a direct OpenAI offer: Format
// switches the form at once to frames and their length, with no
// background; a photo attached is offered to be animated; the loop's length
// follows both; a big size comes down to 1K; the quote asks for what the
// form shows; back to Image the frames go. A GIF result then plays on the
// stage and stops on its first frame.
func TestBrowserGIF(t *testing.T) {
	runs := &fakeRuns{openai: true}
	ts := fakeSiteWith(t, runs.mount, gifOffers)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const quotes = [], jobs = [], fetch0 = window.fetch;
	window.fetch = (u, o) => {
	  if (String(u).endsWith("/api/quote")) quotes.push(JSON.parse(o.body));
	  if (String(u).endsWith("/api/jobs") && o?.method === "POST") jobs.push(JSON.parse(o.body));
	  return fetch0(u, o);
	};
	const rows = () => [...document.querySelectorAll("#how [data-k^='h-f-']")].filter(vis).map((r) => r.dataset.k.slice(4));
	const last = () => quotes.flatMap((q) => q.items).filter((i) => i.offer.startsWith("openai:")).at(-1)?.settings || {};
	const seg = () => document.querySelector('#how [data-seg="set-output_format"]');
	const sec = (k) => document.querySelector('#how [data-k="h-f-' + k + '"]');
	const tick = (k, text) => [...sec(k).querySelectorAll(".ticks > button")].find((b) => b.querySelector("span").textContent === text);
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "GPT Image";
	q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	const row = document.querySelector("#pkrows .frow");
	([...row.querySelectorAll(".pill:not(.old)")].find((b) => b.textContent.trim() === "2.5 Sunburst") || row.querySelector(".fhit")).click();
	await wait(900);
	document.querySelector('#how [data-seg="pay"] [data-v="openrouter"]').click();
	await wait(700);
	const openrouter = rows();
	document.querySelector('#how [data-seg="pay"] [data-v="openai"]').click();
	await wait(900);
	const out = { openrouter, image: rows(), values: [...seg().querySelectorAll(":scope > button")].map((b) => b.querySelector(".row").textContent + (b.getAttribute("aria-checked") === "true" ? "*" : "")),
	  prices: [...seg().querySelectorAll(":scope > button")].filter((b) => /\$/.test(b.textContent)).length };
	// 2K first: a GIF is made of 1K frames.
	tick("size-k", "2K").click();
	await wait(700);
	seg().querySelector('[data-v="gif"]').click();
	await wait(900);
	const strip = () => sec("frame_count").querySelectorAll(".gloop > i").length;
	Object.assign(out, { gif: rows(), sent: last(), cells: strip(), loop: document.querySelector("#how [data-gl]").textContent });
	// Twelve frames: the loop grows as the slider moves, before any render.
	const r = sec("frame_count").querySelector("input[type=range]");
	r.value = 12;
	r.dispatchEvent(new Event("input", { bubbles: true }));
	out.moving = [strip(), document.querySelector("#how [data-gl]").textContent, sec("frame_count").querySelector(".rline b").textContent];
	r.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(900);
	// 100 ms a frame: the loop's length follows the slider as it moves, too.
	const d = sec("frame_duration_ms").querySelector("input[type=range]");
	d.value = 1;
	d.dispatchEvent(new Event("input", { bubbles: true }));
	out.sliding = document.querySelector("#how [data-gl]").textContent;
	d.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(900);
	Object.assign(out, { faster: last(), loop2: document.querySelector("#how [data-gl]").textContent });
	// Back to Image, a run goes as a picture: the price of that was known, so the job tells.
	seg().querySelector('[data-v="png"]').click();
	await wait(900);
	out.back = rows();
	// A photo attached: one line offers to animate it, and once it is a GIF says so.
	const cv = document.createElement("canvas"); cv.width = 64; cv.height = 64;
	const png = await new Promise((r) => cv.toBlob(r, "image/png"));
	const dt = new DataTransfer(); dt.items.add(new File([png], "p.png", { type: "image/png" }));
	const inp = document.querySelector("#file"); inp.files = dt.files; inp.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(1200);
	const note = () => document.querySelector('#how [data-k="h-gifnote"]');
	out.offer = note()?.textContent || "";
	note().querySelector("button").click();
	await wait(900);
	out.animated = [note()?.textContent || "", seg().querySelector('[aria-checked="true"]')?.dataset.v];
	document.querySelector("#files .thumb .rm").click();
	await wait(700);
	out.gone = !note();
	seg().querySelector('[data-v="png"]').click();
	await wait(900);
	await press("done", (s) => s.done);
	seg().querySelector('[data-v="gif"]').click();
	await wait(900);
	// A GIF result: it plays, Pause holds it on a canvas of its first frame, Play lets it go.
	const s = await press("done:gif", (s) => s.done);
	Object.assign(out, { still: jobs[0]?.settings || {}, made: jobs[1]?.settings || {} });
	const gv = () => document.querySelector("#stage .jbox.done .gifv");
	for (let i = 0; i < 30 && !gv()?._img.complete; i++) await wait(100);
	const pp = document.querySelector("#stage .jbox.done > .tpp");
	out.result = { timeout: !!s.timeout, acts: s.acts, playing: pp?.getAttribute("aria-label"), canvas: !!gv()?.querySelector("canvas") };
	pp.click();
	await wait(200);
	const c = gv().querySelector("canvas");
	out.held = { label: pp.getAttribute("aria-label"), canvas: !!c, w: c?.width, hidden: gv()._img.style.visibility };
	pp.click();
	await wait(200);
	out.again = { label: pp.getAttribute("aria-label"), canvas: !!gv().querySelector("canvas"), hidden: gv()._img.style.visibility };
	return out;`)

	if got := fmt.Sprint(s["openrouter"]); strings.Contains(got, "output_format") || strings.Contains(got, "frame_count") {
		t.Errorf("OpenRouter's offer has no GIF: rows %v", got)
	}
	if got := fmt.Sprint(s["image"]); !strings.Contains(got, "output_format") || !strings.Contains(got, "background") || strings.Contains(got, "frame_count") {
		t.Errorf("Image on OpenAI: rows %v", got)
	}
	if fmt.Sprint(s["values"]) != "[Image* GIF]" || s["prices"] != float64(2) {
		t.Errorf("Format: %v, %v prices", s["values"], s["prices"])
	}
	if s["offer"] != "Animate this photo as a GIF" || fmt.Sprint(s["animated"]) != "[The GIF brings your photo to life. gif]" || s["gone"] != true {
		t.Errorf("with a photo: offered %q, then %v, gone with it %v", s["offer"], s["animated"], s["gone"])
	}
	if got := fmt.Sprint(s["gif"]); !strings.Contains(got, "frame_count") || !strings.Contains(got, "frame_duration_ms") || strings.Contains(got, "background") {
		t.Errorf("GIF: rows %v", got)
	}
	sent := s["sent"].(map[string]any)
	if sent["output_format"] != "gif" || sent["frame_count"] != float64(9) || sent["frame_duration_ms"] != float64(200) || sent["size"] != "1024x1024" || sent["background"] != nil {
		t.Errorf("GIF quoted with %v", sent)
	}
	if s["cells"] != float64(9) || s["loop"] != "1.8 s loop" {
		t.Errorf("9 frames of 200 ms: %v cells, %q", s["cells"], s["loop"])
	}
	if fmt.Sprint(s["moving"]) != "[12 2.4 s loop 12 frames]" {
		t.Errorf("sliding to 12 frames: %v", s["moving"])
	}
	faster := s["faster"].(map[string]any)
	if faster["frame_count"] != float64(12) || faster["frame_duration_ms"] != float64(100) || s["sliding"] != "1.2 s loop" || s["loop2"] != "1.2 s loop" {
		t.Errorf("12 frames of 100 ms: quoted %v, %q while sliding, %q after", faster, s["sliding"], s["loop2"])
	}
	still := s["still"].(map[string]any)
	if got := fmt.Sprint(s["back"]); strings.Contains(got, "frame_count") || !strings.Contains(got, "background") ||
		still["output_format"] != "png" || still["frame_count"] != nil || still["frame_duration_ms"] != nil || still["background"] != "auto" {
		t.Errorf("back to Image: rows %v, sent %v", got, still)
	}
	// Back to GIF, the frames chosen before come back with it.
	if made := s["made"].(map[string]any); made["output_format"] != "gif" || made["frame_count"] != float64(12) || made["frame_duration_ms"] != float64(100) || made["background"] != nil {
		t.Errorf("GIF sent with %v", made)
	}
	res := s["result"].(map[string]any)
	if acts := fmt.Sprint(res["acts"]); res["timeout"] == true || res["playing"] != "Pause" || res["canvas"] != false || strings.Contains(acts, "Animate") {
		t.Errorf("GIF result: %v", res)
	}
	if held := s["held"].(map[string]any); held["label"] != "Play" || held["canvas"] != true || held["w"] != float64(64) || held["hidden"] != "hidden" {
		t.Errorf("paused: %v", held)
	}
	if again := s["again"].(map[string]any); again["label"] != "Pause" || again["canvas"] != false || again["hidden"] != "" {
		t.Errorf("playing again: %v", again)
	}
	noErrors(t, ctx)
}
