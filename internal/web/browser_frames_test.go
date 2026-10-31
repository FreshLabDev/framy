// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// framesHelpers read the frames on the stage and in the form.
const framesHelpers = `
const cells = () => [...document.querySelectorAll("#stage .cell")];
async function pickModel(name) {
  document.querySelector("#how .mhead").click();
  await wait(600);
  [...document.querySelectorAll("#picker button")].find((b) => b.textContent.trim().startsWith(name)).click();
  for (let i = 0; i < 20 && !document.querySelector("#picker").hidden; i++) await wait(100);
  await wait(300);
}
`

// TestBrowserFrames checks a press of several frames on a computer: frames
// on a board, one set up on its own, one POST /api/batches for all of them,
// and the results in place of the frames, a failed one gone.
func TestBrowserFrames(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)

	// Two more frames, the second with a model of its own.
	s := js[map[string]any](t, ctx, pageHelpers+framesHelpers+`
	document.querySelector("#stage .addf").click(); await wait(500);
	document.querySelector("#stage .addf").click(); await wait(500);
	const n = cells().length;
	cells()[1].click(); await wait(500);
	const pressed = cells().map((c) => c.getAttribute("aria-pressed")).join(",");
	const tip = document.querySelector("#stage .scopetip")?.textContent || "";
	await pickModel("Nano Banana 2.1");
	const own = document.querySelector("#how .ownmark")?.textContent || "";
	const chips = cells().map((c) => !!c.querySelector(".chip.own")).join(",");
	document.querySelector("#stage .scopetip button").click(); await wait(500);
	for (let i = 0; i < 30 && /…/.test(document.querySelector("#bar").textContent); i++) await wait(100);
	return { n, pressed, tip, own, chips, note: document.querySelector("#stage .fnote")?.textContent || "", bar: document.querySelector("#bar").textContent,
	  scoped: !!document.querySelector("#stage .scopetip") };`)
	if s["n"] != float64(3) || s["pressed"] != "false,true,false" || !strings.HasPrefix(s["tip"].(string), "Setting up frame 2") ||
		s["own"] != "only frame 2" || s["chips"] != "false,true,false" || !strings.HasPrefix(s["note"].(string), "Frames with their own settings: 2.") ||
		s["scoped"] != false || !strings.Contains(s["bar"].(string), "$") {
		t.Errorf("setting up: %v", s)
	}

	// Frames 1 and 3 are the same run and go as one job; frame 2 fails.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+framesHelpers+`
	const s = await press("frames:done,fail:moderation", () => document.querySelectorAll("#stage .jbox.cell.done img").length === 2 && !!document.querySelector("#stage .jwhy"));
	return { ...s, nums: [...document.querySelectorAll("#stage .jbox.cell .num")].map((e) => e.textContent).join(","),
	  models: [...document.querySelectorAll("#stage .jbox.cell .tm")].length, tiles: document.querySelectorAll("#stage .jbox.cell").length };`)
	if s["timeout"] == true || s["nums"] != "1,3" || s["models"] != float64(2) || s["tiles"] != float64(2) ||
		s["why"] != "Frame 2: Refused by the rules." || s["line"] != "MAI-Image 2.6 Flash, Nano Banana 2.1 · $0.08" ||
		!strings.HasPrefix(fmt.Sprint(s["acts"]), "[Download all Again Edit the prompt") {
		t.Errorf("press: %v", s)
	}
	runs.mu.Lock()
	if len(runs.batches) != 1 {
		t.Fatalf("batches sent: %d", len(runs.batches))
	}
	b := runs.batches[0]
	runs.mu.Unlock()
	frames, _ := b["frames"].([]any)
	if len(frames) != 3 || b["shown_usd"] == nil || b["prompt"] != "frames:done,fail:moderation" {
		t.Fatalf("the batch: %v", b)
	}
	for i, f := range frames {
		fr := f.(map[string]any)
		set, _ := fr["settings"].(map[string]any)
		if _, ok := set["n"]; ok || fr["offer"] == nil || fr["pay"] != "key" {
			t.Errorf("frame %d: %v", i, fr)
		}
		if nano := strings.Contains(fr["offer"].(string), "nano-banana"); nano != (i == 1) {
			t.Errorf("frame %d offer: %v", i, fr["offer"])
		}
	}

	// Edit puts every frame back, the second with its own model.
	s = js[map[string]any](t, ctx, pageHelpers+framesHelpers+`
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Edit the prompt").click();
	await wait(700);
	return { n: cells().length, chips: cells().map((c) => !!c.querySelector(".chip.own")).join(","), prompt: document.querySelector("#prompt").value };`)
	if s["n"] != float64(3) || s["chips"] != "false,true,false" || s["prompt"] != "frames:done,fail:moderation" {
		t.Errorf("edit: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserFramesPhone checks the frames on a phone: a strip under the
// button that adds one, clear of the dock, and a swipe in the sheet that goes
// to the next frame and rolls the strip with it.
func TestBrowserFramesPhone(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	for _, size := range [][2]int64{{375, 812}, {320, 568}} {
		ctx := browser(t, ts.URL, size[0], size[1], true)
		s := js[map[string]any](t, ctx, pageHelpers+framesHelpers+`
	// The card that stands whole in the strip: at its start, or the last one at its end.
	const at = () => { const s = document.querySelector("#stage .strip"), cs = [...s.querySelectorAll(":scope > .cell")], r = s.getBoundingClientRect();
	  return cs.findIndex((c, i) => { const b = c.getBoundingClientRect(); return Math.abs(b.left - r.left - 16) < 2 || i === cs.length - 1 && Math.abs(r.right - 16 - b.right) < 2; }); };
	const dot = () => [...document.querySelectorAll("#stage .dots i")].findIndex((i) => i.classList.contains("on"));
	const settle = async (k) => { for (let i = 0; i < 25 && at() !== k; i++) await wait(100); await wait(200); };
	document.querySelector("#stage .addf").click(); await wait(900);
	document.querySelector("#stage .addf").click(); await settle(2);
	const plus = document.querySelector("#stage .shead .addf").getBoundingClientRect(), strip = document.querySelector("#stage .strip").getBoundingClientRect();
	const added = { n: cells().length, at: at(), dot: dot(), plus: Math.round(plus.height), over: plus.bottom <= strip.top,
	  dock: Math.round(document.querySelector("#composer").getBoundingClientRect().top - strip.bottom) };
	cells()[2].click(); await wait(800);
	const title = document.querySelector("#tuneTitle").textContent;
	async function swipe(dx, k) {
	  const tg = document.querySelector("#tuneBody .mcard .facts");
	  const touch = (x) => new Touch({ identifier: 1, target: tg, clientX: x, clientY: 300 });
	  tg.dispatchEvent(new TouchEvent("touchstart", { touches: [touch(180)], changedTouches: [touch(180)], bubbles: true }));
	  await wait(100);
	  tg.dispatchEvent(new TouchEvent("touchend", { touches: [], changedTouches: [touch(180 + dx)], bubbles: true }));
	  await wait(300); await settle(k);
	  return { title: document.querySelector("#tuneTitle").textContent, at: at(), dot: dot() };
	}
	const back = await swipe(120, 1), next = await swipe(-120, 2);
	return { added, title, back, next, overflow: document.documentElement.scrollWidth - innerWidth };`)
		added, _ := s["added"].(map[string]any)
		back, _ := s["back"].(map[string]any)
		next, _ := s["next"].(map[string]any)
		if added["n"] != float64(3) || added["at"] != float64(2) || added["dot"] != float64(2) || added["plus"].(float64) < 44 || added["over"] != true || added["dock"].(float64) < 0 ||
			s["title"] != "Frame 3" || back["title"] != "Frame 2" || back["at"] != float64(1) || back["dot"] != float64(1) ||
			next["title"] != "Frame 3" || next["at"] != float64(2) || s["overflow"].(float64) > 0 {
			t.Errorf("%dx%d: %v", size[0], size[1], s)
		}
		noErrors(t, ctx)
	}
}

// TestBrowserLibraryPress checks a press of three frames in the library: one
// card for its two jobs, the frames on the viewer, and Edit and Again for all
// of them. Job 66 made frames 1 and 3 on FLUX, job 65 frame 2 on Nano Banana.
func TestBrowserLibraryPress(t *testing.T) {
	f := newFakeLibrary()
	img := func(id int, url string) map[string]any {
		return map[string]any{"id": id, "content_type": "image/png", "bytes": 4000, "width": 1024, "height": 1024, "url": url,
			"download": url + "&download=1", "thumb": map[string]any{"url": url, "width": 480, "height": 480}}
	}
	cost := func(usd float64) map[string]any {
		return map[string]any{"usd": usd, "amount": usd, "currency": "USD", "label": fmt.Sprintf("$%.3f", usd)}
	}
	j66, j65 := f.jobs[4], f.jobs[5]
	j66["batch"], j66["frames"], j66["settings"] = "b1", []int{0, 2}, map[string]any{"aspect_ratio": "1:1", "n": 2}
	j66["results"], j66["cost"] = []any{img(1066, "/test/result.png?a"), img(3066, "/test/result.png?c")}, cost(0.08)
	j65["batch"], j65["frames"], j65["kind"], j65["free"] = "b1", []int{1}, "image", false
	j65["offer"], j65["model"], j65["settings"] = "openrouter:google/gemini-nano-banana-2.1", "google/gemini-nano-banana-2.1", map[string]any{"aspect_ratio": "1:1", "resolution": "1K"}
	j65["results"], j65["cost"] = []any{img(1065, "/test/result.png?b")}, cost(0.04)

	var sent []map[string]any
	ts := fakeSite(t, func(srv *Server) {
		f.mount(srv)
		srv.HandleFunc("POST /api/batches", func(w http.ResponseWriter, r *http.Request) {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			defer f.mu.Unlock()
			sent = append(sent, in)
			var out []map[string]any
			for k, fr := range [][]int{{0, 2}, {1}} {
				frame := in["frames"].([]any)[fr[0]].(map[string]any)
				offer := frame["offer"].(string)
				j := map[string]any{"id": int64(801 + k), "seq": 1, "status": "done", "kind": "image", "offer": offer, "model": strings.SplitN(offer, ":", 2)[1],
					"free": false, "prompt": in["prompt"], "settings": frame["settings"], "surface": "site", "inputs": []any{}, "created_at": time.Now().UTC(),
					"batch": "b2", "frames": fr, "cost": cost(0.04)}
				var rs []any
				for i := range fr {
					rs = append(rs, img(4000+10*k+i, "/test/result.png?n"))
				}
				j["results"] = rs
				f.jobs = append([]map[string]any{j}, f.jobs...)
				out = append(out, j)
			}
			writeTestJSON(w, http.StatusAccepted, map[string]any{"batch": "b2", "jobs": out})
		})
	})
	ctx := browser(t, ts.URL+"/library", 1440, 900, false)

	got := js[map[string]any](t, ctx, libHelpers+`
	await settled();
	const card = tiles().find((e) => e.getAttribute("href") === "/r/66");
	const r = { has65: ids().includes(65), cover: card.querySelector(".cover").className, imgs: card.querySelectorAll(".cover img").length,
	  badge: card.querySelector(".badge.n")?.textContent, m: card.querySelector(".m").textContent };
	card.click(); await wait(700);
	const v = document.querySelector("#viewer"), picks = () => [...v.querySelectorAll(".vpicks button")];
	r.path = location.pathname; r.picks = picks().length; r.pressed = picks().map((b) => b.getAttribute("aria-pressed")).join(",");
	r.nav = v.querySelector("#vnav span")?.textContent;
	picks()[1].click(); await wait(400);
	r.path2 = location.pathname; r.line2 = v.querySelector(".jline").textContent;
	picks()[2].click(); await wait(400);
	r.path3 = location.pathname; r.src3 = v.querySelector(".vbox img:not(.ph)")?.getAttribute("src");
	[...v.querySelectorAll(".jacts > button")].find((b) => b.textContent.trim() === "Edit the prompt").click();
	await wait(900);
	r.where = location.pathname; r.cells = document.querySelectorAll("#stage .cell").length;
	r.own = [...document.querySelectorAll("#stage .cell")].map((c) => !!c.querySelector(".chip.own")).join(",");
	r.prompt = document.querySelector("#prompt").value;
	return r;`)
	if got["has65"] != false || got["cover"] != "cover mosaic m3" || got["imgs"] != float64(3) || got["badge"] != "×3" ||
		!strings.Contains(got["m"].(string), "+1 · $0.12") || got["path"] != "/r/66" || got["picks"] != float64(3) || got["pressed"] != "true,false,false" ||
		got["nav"] != "5 of 29" || got["path2"] != "/r/65" || !strings.HasPrefix(got["line2"].(string), "Nano Banana 2.1") || got["path3"] != "/r/66" ||
		got["src3"] != "/test/result.png?c" || got["where"] != "/" || got["cells"] != float64(3) || got["own"] != "false,true,false" || got["prompt"] != "a glass city 66" {
		t.Errorf("press in the library: %v", got)
	}

	// Again runs every frame as one press, at the price of now.
	got = js[map[string]any](t, ctx, libHelpers+`
	document.querySelector(".tab[data-v=library]").click(); await wait(700);
	tiles().find((e) => e.getAttribute("href") === "/r/66").click(); await wait(700);
	const b = await until(() => { const x = document.querySelector("#viewer [data-again]"); return x && !x.disabled && !/…/.test(x.textContent) && x; });
	const label = b.textContent;
	b.click();
	const done = await until(() => document.querySelectorAll("#stage .jbox.cell.done img").length === 3, 8000);
	return { label, path: location.pathname, done: !!done };`)
	if !regexp.MustCompile(`^Again · ≈ \$\d+\.\d+$`).MatchString(got["label"].(string)) || got["path"] != "/" || got["done"] != true {
		t.Errorf("again: %v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("batches sent: %v", sent)
	}
	frames, _ := sent[0]["frames"].([]any)
	var offers []string
	for _, x := range frames {
		fr := x.(map[string]any)
		offers = append(offers, fr["offer"].(string))
		if _, ok := fr["settings"].(map[string]any)["n"]; ok {
			t.Errorf("a frame with n: %v", fr)
		}
	}
	if strings.Join(offers, " ") != "openrouter:black-forest-labs/flux-3-image openrouter:google/gemini-nano-banana-2.1 openrouter:black-forest-labs/flux-3-image" ||
		sent[0]["shown_usd"] == nil || sent[0]["prompt"] != "a glass city 66" {
		t.Errorf("again sent %v", sent[0])
	}
	noErrors(t, ctx)
}
