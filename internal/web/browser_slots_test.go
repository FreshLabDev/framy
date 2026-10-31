// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"fmt"
	"image"
	_ "image/png"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestBrowserVideoSlots checks the slots of a video (docs/design.md, Video):
// nothing, Start, Start and End, and References each run as that mode, the
// frames going first with frame_images saying which they are; a last frame
// hides the models that take none, references hide the models that take
// fewer, and a model that cannot take what is attached gives way to one
// that can. Omni says the model picks the length, and the wait shows the
// time that went by.
func TestBrowserVideoSlots(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, func(srv *Server) {
		runs.mount(srv)
		// A photo framy keeps gets its width as its id, so the test can tell
		// the order the run sends them in.
		srv.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, r *http.Request) {
			c, _, err := image.DecodeConfig(r.Body)
			if err != nil {
				writeTestJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "unreadable"})
				return
			}
			writeTestJSON(w, http.StatusCreated, map[string]any{"id": c.Width, "content_type": "image/png", "bytes": 100, "width": c.Width, "height": c.Height})
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	// The steps go in a few calls, each under the helper's 30 seconds; what
	// the page sent is kept on window between them.
	step := func(body string) map[string]any {
		return js[map[string]any](t, ctx, pageHelpers+runHelpers+slotHelpers+body)
	}
	js[bool](t, ctx, `
	window.__quotes = []; window.__sent = [];
	const fetch0 = window.fetch;
	window.fetch = (u, o) => {
	  const s = String(u);
	  if (s.endsWith("/api/quote")) __quotes.push(JSON.parse(o.body));
	  if (s.endsWith("/api/jobs") && o?.method === "POST") __sent.push(JSON.parse(o.body));
	  return fetch0(u, o);
	};
	return true;`)
	s := step(`
	const out = {};
	document.querySelectorAll("#kind > button")[1].click();
	// The empty row of files was hidden: nothing of it leaves on the switch,
	// least of all its hint squeezed into a column at the page's edge.
	out.ghost = [...document.querySelectorAll("[aria-hidden='true']")].some((e) => e.textContent.includes("or a clip to change"));
	await wait(800);
	out.empty = groups();
	out.loose = !!document.querySelector("#files .addfile:not(.slotadd)");
	await dropOn("start", [await png(64, 36)]);
	out.start = groups(); out.m1 = model(); out.pay1 = own()?.pay;
	await dropOn("end", [await png(48, 48)]);
	out.both = groups(); out.m2 = model(); out.f2 = frames();
	out.list2 = await listed();
	return out;`)
	merge := func(m map[string]any) {
		for k, v := range m {
			s[k] = v
		}
	}
	merge(step(`
	const out = {};
	// A new start frame takes the old one's place; the run still sends it first.
	await dropOn("start", [await png(80, 45)]);
	out.swap = groups();
	const p = document.querySelector("#prompt");
	p.value = "done"; p.dispatchEvent(new Event("input"));
	await wait(400);
	document.querySelector("#go").click();
	for (let i = 0; i < 60 && !__sent.length; i++) await wait(100);
	const j = __sent[0];
	out.job = j ? { inputs: j.inputs, frames: j.settings.frame_images, offer: j.offer } : null;
	await wait(1500);
	// Back to the composer: without the start frame, the end frame becomes the start.
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Edit")?.click();
	await wait(900);
	document.querySelector('#files [data-k="g-start"] .thumb .rm').click();
	await wait(900);
	out.promoted = groups();
	document.querySelector('#files [data-k="g-start"] .thumb .rm').click();
	await wait(900);
	out.cleared = groups();
	return out;`))
	merge(step(`
	const out = {};
	await dropOn("ref", [await png(40, 40), await png(41, 40), await png(42, 40), await png(43, 40)]);
	out.refs = groups(); out.m3 = model(); out.f3 = frames(); out.n3 = (own()?.files || []).length;
	out.list3 = await listed();
	for (const b of [...document.querySelectorAll("#files .thumb .rm")]) { b.click(); await wait(300); }
	await wait(600);
	// Omni picks the length itself.
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "Omni"; q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	document.querySelector("#pkrows .frow .fhit").click();
	await wait(900);
	out.omni = document.querySelector("#how [data-k='h-f-duration']")?.textContent || "";
	return out;`))
	// Omni runs on an AI Studio key, which this person has not connected: the
	// wait is checked on a model of their OpenRouter key.
	merge(step(`
	document.querySelectorAll("#kind > button")[0].click();
	await wait(800);
	const r = await press("running", (s) => s.label === "Making it");
	return { took: r.sub };`))
	if s["empty"] != "start:0+ ref:0+" || s["loose"] != false || s["ghost"] != false {
		t.Errorf("empty slots: %v, plain add button %v, the hidden hint left on the switch %v", s["empty"], s["loose"], s["ghost"])
	}
	// One picture in Start runs free on HeyGen: its plan sets frame_images on the server.
	if s["start"] != "start:1 end:0+" || s["m1"] != "HeyGen Video" || s["pay1"] != "free" {
		t.Errorf("Start: %v on %v paid %v", s["start"], s["m1"], s["pay1"])
	}
	// HeyGen takes no last frame: the composer moves to a model that does.
	if s["both"] != "start:1 end:1" || s["m2"] != "Seedance 2.0 Mini" || s["f2"] != `["first_frame","last_frame"]` {
		t.Errorf("Start and End: %v on %v, frames %v", s["both"], s["m2"], s["f2"])
	}
	has := func(list any, word string) bool { return strings.Contains(fmt.Sprint(list), word) }
	if l := s["list2"]; has(l, "HeyGen") || has(l, "Gen-4.5") || has(l, "Hailuo 2.3") || has(l, "Grok") || has(l, "Aleph") || !has(l, "Kling") || !has(l, "Veo") {
		t.Errorf("models offered for a first and a last frame: %v", l)
	}
	if s["swap"] != "start:1 end:1" {
		t.Errorf("a new start frame: %v", s["swap"])
	}
	job, _ := s["job"].(map[string]any)
	if fmt.Sprint(job["inputs"]) != "[80 48]" || fmt.Sprint(job["frames"]) != "[first_frame last_frame]" || job["offer"] != "openrouter:bytedance/seedance-2.0-mini" {
		t.Errorf("the run of two frames: %v", job)
	}
	if s["promoted"] != "start:1 end:0+" || s["cleared"] != "start:0+ ref:0+" {
		t.Errorf("removing frames: %v, then %v", s["promoted"], s["cleared"])
	}
	if s["refs"] != "ref:4+" || s["m3"] != "Seedance 2.0 Mini" || s["f3"] != "null" || s["n3"] != float64(4) {
		t.Errorf("References: %v on %v, frames %v, %v files", s["refs"], s["m3"], s["f3"], s["n3"])
	}
	if l := s["list3"]; has(l, "Kling") || has(l, "Veo") || has(l, "Gen-4.5") || !has(l, "HeyGen") || !has(l, "Seedance") || !has(l, "Wan") {
		t.Errorf("models offered for four references: %v", l)
	}
	if !strings.Contains(fmt.Sprint(s["omni"]), "The model picks it, 3–10 s") {
		t.Errorf("Omni's length: %q", s["omni"])
	}
	if took := fmt.Sprint(s["took"]); len(took) != 4 || !strings.HasPrefix(took, "0:0") {
		t.Errorf("the wait shows %q, want the time since the press", took)
	}
	noErrors(t, ctx)
}

// TestBrowserVideoSounds checks a sound in References: it turns a start
// frame into a reference, goes on a key (a free run takes none), hides the
// models that take no sound, and goes up as a clip does, by the bucket and
// POST /api/videos, its id joining the run's inputs.
func TestBrowserVideoSounds(t *testing.T) {
	runs := &fakeRuns{}
	var put, took atomic.Int32
	ts := fakeSite(t, func(srv *Server) {
		runs.mount(srv)
		srv.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, r *http.Request) {
			c, _, err := image.DecodeConfig(r.Body)
			if err != nil {
				writeTestJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "unreadable"})
				return
			}
			writeTestJSON(w, http.StatusCreated, map[string]any{"id": c.Width, "content_type": "image/png", "bytes": 100, "width": c.Width, "height": c.Height})
		})
		srv.HandleFunc("POST /api/videos/uploads", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(w, http.StatusCreated, map[string]any{"url": "/bucket/sound", "token": "tok", "max_bytes": 100 << 20})
		})
		srv.HandleFunc("PUT /bucket/sound", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "audio/") {
				put.Add(1)
			}
			w.WriteHeader(http.StatusOK)
		})
		srv.HandleFunc("POST /api/videos", func(w http.ResponseWriter, r *http.Request) {
			took.Add(1)
			writeTestJSON(w, http.StatusAccepted, map[string]any{"task": "snd"})
		})
		srv.HandleFunc("GET /api/videos/snd", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(w, http.StatusOK, map[string]any{"status": "done", "file": map[string]any{"id": 900, "content_type": "audio/wav", "bytes": 48044, "seconds": 3, "audio": true}})
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	step := func(body string) map[string]any {
		return js[map[string]any](t, ctx, pageHelpers+runHelpers+slotHelpers+soundHelpers+body)
	}
	js[bool](t, ctx, `
	window.__quotes = []; window.__sent = [];
	const fetch0 = window.fetch;
	window.fetch = (u, o) => {
	  const s = String(u);
	  if (s.endsWith("/api/quote")) __quotes.push(JSON.parse(o.body));
	  if (s.endsWith("/api/jobs") && o?.method === "POST") __sent.push(JSON.parse(o.body));
	  return fetch0(u, o);
	};
	return true;`)
	s := step(`
	const out = {};
	document.querySelectorAll("#kind > button")[1].click();
	await wait(800);
	await dropOn("start", [await png(64, 36)]);
	// A sound dropped anywhere is a reference, and the start frame becomes one too.
	const dt = new DataTransfer();
	dt.items.add(wav(3));
	document.body.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
	await wait(1300);
	out.groups = groups(); out.model = model();
	const it = own();
	out.pay = it?.pay; out.frames = frames();
	out.sound = (it?.files || []).find((f) => f.content_type?.startsWith("audio/")) || null;
	out.tag = [...document.querySelectorAll('#files [data-k="g-ref"] .thumb .tag')].map((x) => x.textContent).join(",");
	out.list = await listed();
	return out;`)
	merge := func(m map[string]any) {
		for k, v := range m {
			s[k] = v
		}
	}
	merge(step(`
	const p = document.querySelector("#prompt");
	p.value = "with a sound"; p.dispatchEvent(new Event("input"));
	await wait(400);
	document.querySelector("#go").click();
	for (let i = 0; i < 80 && !__sent.length; i++) await wait(100);
	const j = __sent[0];
	return { job: j ? { inputs: j.inputs, offer: j.offer, frames: j.settings.frame_images || null } : null };`))
	if s["groups"] != "ref:2+" || s["model"] != "HeyGen Video" || s["pay"] != "key" || s["frames"] != "null" {
		t.Errorf("a sound beside a start frame: %v on %v paid %v, frames %v", s["groups"], s["model"], s["pay"], s["frames"])
	}
	if snd, _ := s["sound"].(map[string]any); snd == nil || snd["content_type"] != "audio/wav" || snd["seconds"] != float64(3) {
		t.Errorf("the sound in the quote: %v", s["sound"])
	}
	if !strings.Contains(fmt.Sprint(s["tag"]), "3 s") {
		t.Errorf("the sound's length on its cell: %q", s["tag"])
	}
	has := func(list any, word string) bool { return strings.Contains(fmt.Sprint(list), word) }
	if l := s["list"]; has(l, "Kling") || has(l, "Veo") || has(l, "Grok") || !has(l, "HeyGen") || !has(l, "Seedance") || !has(l, "Wan") {
		t.Errorf("models offered for a sound: %v", l)
	}
	job, _ := s["job"].(map[string]any)
	if fmt.Sprint(job["inputs"]) != "[64 900]" || job["offer"] != "openrouter:heygen/heygen-video-1" || job["frames"] != nil || put.Load() != 1 || took.Load() != 1 {
		t.Errorf("the run with a sound: %v, put %d, taken %d", job, put.Load(), took.Load())
	}
	noErrors(t, ctx)
}

// soundHelpers make a WAV of a few seconds of silence.
const soundHelpers = `
const wav = (secs) => {
  const rate = 8000, n = rate * secs, b = new ArrayBuffer(44 + n * 2), v = new DataView(b);
  const s = (o, x) => [...x].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
  s(0, "RIFF"); v.setUint32(4, 36 + n * 2, true); s(8, "WAVE"); s(12, "fmt "); v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true);
  v.setUint32(24, rate, true); v.setUint32(28, rate * 2, true); v.setUint16(32, 2, true); v.setUint16(34, 16, true); s(36, "data"); v.setUint32(40, n * 2, true);
  return new File([b], "s" + secs + ".wav", { type: "audio/wav" });
};
`

// slotHelpers drop pictures on a slot and read the slots, the model and the
// composer's own quote.
const slotHelpers = `
const png = async (w, h) => { const cv = document.createElement("canvas"); cv.width = w; cv.height = h; return new File([await new Promise((r) => cv.toBlob(r, "image/png"))], "p" + w + ".png", { type: "image/png" }); };
async function dropOn(slot, files) {
  const g = document.querySelector('#files [data-k="g-' + slot + '"]');
  const dt = new DataTransfer();
  for (const f of files) dt.items.add(f);
  g.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
  await wait(1100);
}
const groups = () => [...document.querySelectorAll("#files .fgroup.slot")].map((g) => g.dataset.k.slice(2) + ":" + g.querySelectorAll(".thumb").length + (g.querySelector(".slotadd") ? "+" : "")).join(" ");
const model = () => document.querySelector("#how .mname").textContent;
// The composer's own run is the item with a payment; the prices beside a choice have none.
const own = () => { for (let i = __quotes.length - 1; i >= 0; i--) { const it = __quotes[i].items.find((x) => x.pay); if (it) return it; } return null; };
const frames = () => JSON.stringify(own()?.settings?.frame_images || null);
async function listed() {
  document.querySelector("#how .mhead").click();
  await wait(800);
  const names = [...document.querySelectorAll("#pkrows .frow .nm, #pkrows .rcard .nm")].map((x) => x.textContent);
  document.querySelector("#picker [data-close]").click();
  await wait(600);
  return names;
}
`
