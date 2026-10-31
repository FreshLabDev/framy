// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// fakeLibrary is a signed-in person with 70 runs, newest first: the newest
// is running (it ends on its second read), then one queued, one failed, one
// cancelled, and the rest done, every third a video. Five are from today,
// five from yesterday, the others a day apart before. Run 64 cannot be
// deleted just now (storage_unavailable). Again makes a new run that is done
// at once.
type fakeLibrary struct {
	mu      sync.Mutex
	jobs    []map[string]any // newest first
	reads   map[int64]int    // GET /api/jobs/{id} per job
	lists   []string         // the query of every GET /api/jobs
	created []map[string]any // the bodies of POST /api/jobs
	deleted []int64
}

const libTotal = 70

func newFakeLibrary() *fakeLibrary {
	f := &fakeLibrary{reads: map[int64]int{}}
	now := time.Now()
	words := []string{"a fox in the snow", "the old harbor at dusk", "a red bicycle", "Harbor lights", "a glass city"}
	for i := range libTotal {
		id := int64(libTotal - i)
		day := 0
		switch {
		case i < 5:
		case i < 10:
			day = 1
		default:
			day = 2 + (i-10)/4
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location()).AddDate(0, 0, -day).Add(-time.Duration(i) * time.Minute)
		if day == 0 && at.After(now) {
			at = now.Add(-time.Duration(i+1) * time.Minute)
		}
		j := map[string]any{"id": id, "seq": 1, "kind": "image", "offer": "openrouter:black-forest-labs/flux-3-image", "model": "black-forest-labs/flux-3-image",
			"free": false, "prompt": fmt.Sprintf("%s %d", words[i%len(words)], id), "settings": map[string]any{"aspect_ratio": "1:1"}, "surface": "site",
			"inputs": []any{}, "created_at": at.UTC(), "status": "done",
			"estimate": map[string]any{"kind": "about", "usd": 0.04, "max_usd": 0.04, "currency": "USD", "label": "$0.040"}}
		if i%3 == 2 {
			j["kind"], j["offer"], j["model"] = "video", "openrouter:google/veo-3.1-fast", "google/veo-3.1-fast"
			j["settings"] = map[string]any{"aspect_ratio": "16:9", "duration": 6}
		}
		if i == 7 {
			// A run on a photo framy keeps: Again sends it by id.
			j["inputs"] = []any{map[string]any{"id": 900, "content_type": "image/png", "bytes": 2000, "width": 64, "height": 64, "url": "/test/result.png",
				"thumb": map[string]any{"url": "/test/result.png", "width": 64, "height": 64}}}
		}
		switch i {
		case 0:
			j["status"] = "running"
		case 1:
			j["status"] = "queued"
		case 2:
			j["status"], j["error"] = "failed", map[string]any{"class": "money", "message": fakeMessage}
		case 3:
			j["status"], j["free"] = "cancelled", true
		default:
			res := map[string]any{"id": 1000 + id, "content_type": "image/png", "bytes": 4000, "width": 1024, "height": 1024, "url": "/test/result.png",
				"download": "/test/result.png?download=1", "thumb": map[string]any{"url": "/test/result.png", "width": 480, "height": 480}}
			if j["kind"] == "video" {
				res["content_type"], res["seconds"], res["width"], res["height"] = "video/mp4", 6, 1280, 720
				res["url"], res["download"] = "/test/clip.mp4", "/test/clip.mp4?download=1"
			}
			j["results"] = []any{res}
			j["cost"] = map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"}
		}
		switch i {
		case 5: // 65, a free video
			j["free"] = true
		case 6: // 64, made in the direct chat
			j["surface"] = "dm"
		case 8: // 62, a video that went on from a clip, joined to it
			j["joined"] = map[string]any{"id": 3062, "content_type": "video/mp4", "bytes": 9000, "width": 1280, "height": 720, "seconds": 11,
				"url": "/test/clip.mp4?joined", "download": "/test/clip.mp4?download=joined", "thumb": map[string]any{"url": "/test/result.png?joined", "width": 480, "height": 270}}
		case 9: // 61, made by guest mode
			j["surface"] = "guest"
		case 10: // 60, two pictures
			j["results"] = append(j["results"].([]any), map[string]any{"id": 3060, "content_type": "image/png", "bytes": 4000, "width": 1024, "height": 1024,
				"url": "/test/result.png?b", "download": "/test/result.png?download=2", "thumb": map[string]any{"url": "/test/result.png?b", "width": 480, "height": 480}})
		}
		f.jobs = append(f.jobs, j)
	}
	return f
}

func (f *fakeLibrary) find(id int64) (int, map[string]any) {
	for i, j := range f.jobs {
		if j["id"].(int64) == id {
			return i, j
		}
	}
	return -1, nil
}

func (f *fakeLibrary) mount(srv *Server) {
	srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, http.StatusOK, map[string]any{
			"user_id": 1, "name": "Ada", "language": "en", "currency": "USD",
			"connections": []map[string]any{{"id": "c1", "provider": "openrouter", "last4": "abcd", "status": "ok"}},
			"free": map[string]any{
				"images": map[string]any{"used": 2, "limit": 5, "left": 3, "window_seconds": 86400},
				"videos": map[string]any{"used": 0, "limit": 1, "left": 1, "window_seconds": 604800},
			},
		})
	})
	srv.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lists = append(f.lists, r.URL.RawQuery)
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit < 1 {
			limit = 30
		}
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		var out []map[string]any
		for _, j := range f.jobs {
			if k := q.Get("kind"); k != "" && j["kind"] != k {
				continue
			}
			if s := q.Get("q"); s != "" && !strings.Contains(strings.ToLower(j["prompt"].(string)), strings.ToLower(s)) {
				continue
			}
			if before > 0 && j["id"].(int64) >= before {
				continue
			}
			out = append(out, j)
		}
		page := map[string]any{"jobs": out}
		if len(out) > limit {
			page["jobs"], page["next"] = out[:limit], out[limit-1]["id"]
		}
		writeTestJSON(w, http.StatusOK, page)
	})
	srv.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		f.mu.Lock()
		defer f.mu.Unlock()
		_, j := f.find(id)
		if j == nil {
			writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
			return
		}
		f.reads[id]++
		if j["status"] == "running" && f.reads[id] >= 2 {
			j["status"], j["seq"] = "done", 2
			j["cost"] = map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"}
			j["results"] = []any{map[string]any{"id": 1000 + id, "content_type": "image/png", "bytes": 4000, "width": 1024, "height": 1024,
				"url": "/test/result.png", "download": "/test/result.png?download=1"}}
		}
		writeTestJSON(w, http.StatusOK, j)
	})
	srv.HandleFunc("GET /api/jobs/{id}/events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv.HandleFunc("DELETE /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		f.mu.Lock()
		defer f.mu.Unlock()
		i, j := f.find(id)
		switch {
		case j == nil:
			writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		case j["status"] == "running" || j["status"] == "queued":
			writeTestJSON(w, http.StatusConflict, map[string]any{"error": "job_active"})
		case id == 64:
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "storage_unavailable"})
		default:
			f.jobs = slices.Delete(f.jobs, i, i+1)
			f.deleted = append(f.deleted, id)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.created = append(f.created, in)
		offer, _ := in["offer"].(string)
		j := map[string]any{"id": int64(500 + len(f.created)), "seq": 1, "status": "done", "kind": "image", "offer": offer, "model": strings.SplitN(offer, ":", 2)[1],
			"free": in["pay"] == "free", "prompt": in["prompt"], "settings": in["settings"], "surface": "site", "inputs": []any{}, "created_at": time.Now().UTC(),
			"cost":    map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"},
			"results": []any{map[string]any{"id": 2000 + len(f.created), "content_type": "image/png", "width": 1024, "height": 1024, "url": "/test/result.png", "download": "/test/result.png?download=1"}}}
		f.jobs = append([]map[string]any{j}, f.jobs...)
		writeTestJSON(w, http.StatusAccepted, j)
	})
	srv.HandleFunc("POST /auth/miniapp", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, http.StatusOK, map[string]any{"token": "test-token", "expires_in": 3600})
	})
	srv.HandleFunc("GET /test/result.png", func(w http.ResponseWriter, _ *http.Request) {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for i := range img.Pix {
			img.Pix[i] = 0xC0
		}
		img.Set(1, 1, color.Black)
		var b bytes.Buffer
		_ = png.Encode(&b, img)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b.Bytes())
	})
	// Not a playable clip: the checks look at the page around it.
	srv.HandleFunc("GET /test/clip.mp4", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "video/mp4") })
}

func (f *fakeLibrary) state() (lists []string, created []map[string]any, deleted []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.lists), slices.Clone(f.created), slices.Clone(f.deleted)
}

// libHelpers wait for the library and read what it shows.
const libHelpers = pageHelpers + `
const until = async (fn, ms = 6000) => { const end = Date.now() + ms; while (Date.now() < end) { const v = fn(); if (v) return v; await wait(50); } return fn(); };
const tiles = () => [...document.querySelectorAll("#library .li")].filter((e) => !e.closest("[aria-hidden=true]"));
const ids = () => tiles().map((e) => +e.getAttribute("href").split("/").pop());
const settled = () => until(() => tiles().length && !document.querySelector("#library .lspin"));
`

// TestBrowserLibrary checks the library: the runs by day with their covers
// and states, the next pages as the end comes near, the filter and the search.
func TestBrowserLibrary(t *testing.T) {
	f := newFakeLibrary()
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL+"/library", 1440, 900, false)
	got := js[map[string]any](t, ctx, libHelpers+`
	await settled();
	const tab = document.querySelector("#top .tab[aria-current=page]");
	const run = tiles()[0], fail = tiles()[2];
	return {
	  create: vis(document.querySelector("#create")), tab: tab && tab.dataset.v, n: tiles().length,
	  days: [...document.querySelectorAll("#library .lday > h2")].slice(0, 3).map((e) => e.textContent),
	  covers: tiles().slice(4).every((e) => !!e.querySelector(".cover img")),
	  run: run.querySelector(".cover.none b")?.textContent, fail: fail.querySelector(".m")?.textContent,
	  video: tiles()[5].querySelector(".badge")?.textContent, price: tiles()[4].querySelector(".m").textContent,
	  guest: tiles()[9].querySelector(".m").textContent, two: tiles()[10].querySelector(".badge.n")?.textContent,
	  cv: getComputedStyle(tiles()[0]).contentVisibility,
	  overflow: document.documentElement.scrollWidth - innerWidth,
	};`)
	if got["create"] != false || got["tab"] != "library" || got["n"].(float64) != 30 || got["covers"] != true || got["run"] != "Making it" ||
		got["fail"] != "Veo 3.1 Fast · No money on the key" || got["video"] != "6 s" || !strings.HasSuffix(got["price"].(string), "$0.040") || got["overflow"].(float64) > 0 {
		t.Errorf("library: %v", got)
	}
	if got["guest"] != "FLUX 3 · $0.040 · guest mode" || got["two"] != "×2" || got["cv"] != "auto" {
		t.Errorf("tiles: %v", got)
	}
	if d := got["days"].([]any); len(d) != 3 || d[0] != "Today" || d[1] != "Yesterday" {
		t.Errorf("days: %v", d)
	}

	// Scrolled to the end, the next pages come, until there are no more.
	got = js[map[string]any](t, ctx, libHelpers+`
	for (let i = 0; i < 6 && tiles().length < `+strconv.Itoa(libTotal)+`; i++) { scrollTo(0, document.body.scrollHeight); await wait(500); }
	await until(() => /every run/.test(document.querySelector("#library .libmore")?.textContent || ""));
	const ns = ids();
	return { n: ns.length, sorted: ns.every((x, i) => i === 0 || x < ns[i - 1]), end: document.querySelector("#library .libmore")?.textContent };`)
	if got["n"].(float64) != libTotal || got["sorted"] != true || got["end"] != "That is every run" {
		t.Errorf("pages: %v", got)
	}
	lists, _, _ := f.state()
	if !slices.ContainsFunc(lists, func(q string) bool { return strings.Contains(q, "before=41") }) {
		t.Errorf("the second page was not asked with before: %v", lists)
	}

	// Videos only, then a search, then a search with nothing, then all again.
	got = js[map[string]any](t, ctx, libHelpers+`
	scrollTo(0, 0);
	document.querySelector("#library [data-seg=lib] [data-v=video]").click();
	await wait(900); await settled();
	const vids = tiles().length, allVid = tiles().every((e) => /\d s/.test(e.querySelector(".badge")?.textContent || ""));
	document.querySelector("#library [data-seg=lib] [data-v=all]").click();
	await wait(900); await settled();
	const q = document.querySelector("#library .lsearch input");
	q.focus(); q.value = "HARBOR"; q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(900); await settled();
	const found = tiles().map((e) => e.querySelector(".p").textContent), kept = document.activeElement === q;
	q.value = "zzz"; q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(900);
	const none = await until(() => document.querySelector("#library .lempty.slim p")?.textContent);
	document.querySelector("#library .lempty.slim .x").click();
	await wait(900); await settled();
	return { vids, allVid, found: found.length, harbor: found.every((p) => /harbor/i.test(p)), kept, none, back: tiles().length, cleared: q.value };`)
	if got["vids"].(float64) != 23 || got["allVid"] == true || got["found"].(float64) != 28 || got["harbor"] != true || got["kept"] != true ||
		got["none"] != "Nothing found for “zzz”." || got["back"].(float64) != 30 || got["cleared"] != "" {
		t.Errorf("filter and search: %v", got)
	}
	lists, _, _ = f.state()
	if !slices.ContainsFunc(lists, func(q string) bool { return strings.Contains(q, "kind=video") }) ||
		!slices.ContainsFunc(lists, func(q string) bool { return strings.Contains(q, "q=HARBOR") }) {
		t.Errorf("the filter and the search did not reach the server: %v", lists)
	}

	// The running run is read again and shows its result.
	got = js[map[string]any](t, ctx, libHelpers+`
	const done = await until(() => tiles()[0]?.querySelector(".cover img"), 12000);
	return { done: !!done, line: tiles()[0].querySelector(".m").textContent };`)
	if got["done"] != true || !strings.HasSuffix(got["line"].(string), "$0.040") {
		t.Errorf("running run: %v", got)
	}

	// The tab goes back to the composer, and the address with it.
	got = js[map[string]any](t, ctx, libHelpers+`
	document.querySelector("#top .tab[data-v=create]").click();
	await wait(600);
	const there = { path: location.pathname, create: vis(document.querySelector("#create")), lib: vis(document.querySelector("#library")) };
	history.back();
	await wait(600);
	return { ...there, backPath: location.pathname, backLib: vis(document.querySelector("#library")) };`)
	if got["path"] != "/" || got["create"] != true || got["lib"] != false || got["backPath"] != "/library" || got["backLib"] != true {
		t.Errorf("tabs: %v", got)
	}
	noErrors(t, ctx)
}

// TestBrowserViewer checks one run on the viewer: at its own address, the
// arrows, closing, deleting and its refusals, and Again at today's price.
func TestBrowserViewer(t *testing.T) {
	f := newFakeLibrary()
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL+"/r/66", 1440, 900, false)
	got := js[map[string]any](t, ctx, libHelpers+`
	await until(() => vis(document.querySelector("#viewer")) && document.querySelector("#vnav span"));
	const v = document.querySelector("#viewer");
	const r = { title: v.querySelector("h3").textContent, img: !!v.querySelector(".vbox img:not(.ph)"), nav: v.querySelector("#vnav span").textContent,
	  line: v.querySelector(".jline").textContent, facts: [...v.querySelectorAll(".vfacts .fact")].map((e) => e.textContent),
	  dl: v.querySelector(".jacts a.primary")?.getAttribute("href"), del: !!v.querySelector(".jacts button[aria-label=Delete]"),
	  fits: v.querySelector(".vbox").getBoundingClientRect().bottom <= innerHeight };
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
	await wait(400);
	r.next = location.pathname; r.nav2 = v.querySelector("#vnav span").textContent;
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
	await wait(600);
	r.closed = v.hidden; r.after = location.pathname;
	return r;`)
	if got["title"] != "Image" || got["img"] != true || got["nav"] != "5 of 30" || got["line"] != "FLUX.3 · $0.040" && !strings.HasSuffix(got["line"].(string), "$0.040") ||
		got["dl"] != "/test/result.png?download=1" || got["del"] != true || got["fits"] != true || got["next"] != "/r/65" || got["nav2"] != "6 of 30" ||
		got["closed"] != true || got["after"] != "/library" {
		t.Errorf("viewer: %v", got)
	}
	if fs := got["facts"].([]any); len(fs) != 1 || fs[0] != "1:1" {
		t.Errorf("facts: %v", fs)
	}

	// A tile opens it at its address; back closes it.
	got = js[map[string]any](t, ctx, libHelpers+`
	tiles()[6].click();
	await wait(600);
	const r = { path: location.pathname, open: vis(document.querySelector("#viewer")), where: document.querySelector("#viewer .vwhen").innerText.split("\n").pop() };
	history.back();
	await wait(700);
	return { ...r, back: location.pathname, closed: document.querySelector("#viewer").hidden };`)
	if got["path"] != "/r/64" || got["open"] != true || got["where"] != "Made in the chat with framy" || got["back"] != "/library" || got["closed"] != true {
		t.Errorf("open and back: %v", got)
	}

	// Two pictures on the whole screen: the arrows go between them, Escape goes back to the viewer, then to the library.
	got = js[map[string]any](t, ctx, libHelpers+`
	const esc = () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
	const right = () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
	tiles()[10].click(); await wait(600);
	document.querySelector("#viewer .vexp").click(); await wait(400);
	const f = document.querySelector("#full");
	const r = { full: vis(f), cnt: f.querySelector(".cnt").textContent, dl: f.querySelector(".jacts a.primary")?.getAttribute("href"), again: !!f.querySelector("[data-again]") };
	right(); await wait(900);
	r.cnt2 = f.querySelector(".cnt").textContent; r.dl2 = f.querySelector(".jacts a.primary")?.getAttribute("href"); r.path = location.pathname;
	r.overflow = document.documentElement.scrollWidth - innerWidth;
	esc(); await wait(400);
	r.closed = f.hidden; r.viewer = vis(document.querySelector("#viewer")); r.path2 = location.pathname;
	esc(); await wait(700);
	r.viewer2 = vis(document.querySelector("#viewer")); r.path3 = location.pathname;
	return r;`)
	if got["full"] != true || got["cnt"] != "1 of 2" || got["dl"] != "/test/result.png?download=1" || got["again"] != true || got["cnt2"] != "2 of 2" ||
		got["dl2"] != "/test/result.png?download=2" || got["path"] != "/r/60" || got["overflow"].(float64) > 0 || got["closed"] != true || got["viewer"] != true ||
		got["path2"] != "/r/60" || got["viewer2"] != false || got["path3"] != "/library" {
		t.Errorf("whole screen: %v", got)
	}

	// Deleting: one framy cannot delete just now stays, one it can goes, and the viewer goes on to the next.
	got = js[map[string]any](t, ctx, libHelpers+`
	const del = async () => {
	  document.querySelector("#viewer .jacts button[aria-label=Delete]").click();
	  await wait(400);
	  const q = document.querySelector("#ask h2")?.textContent + " " + document.querySelector("#ask p")?.textContent;
	  document.querySelector("#ask .yes").click();
	  await wait(600);
	  return q;
	};
	tiles()[6].click(); await wait(600);
	const q = await del(), toast1 = document.querySelector(".toast")?.textContent, still = ids().includes(64);
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true })); await wait(400);
	const q2 = await del();
	const toast2 = document.querySelector(".toast")?.textContent;
	return { q, q2, toast1, still, toast2, gone: !ids().includes(65), at: location.pathname, open: vis(document.querySelector("#viewer")) };`)
	const delText = "Delete this run? The result and its files go from framy for good, on the site and in Telegram. What the bot already sent to chats stays there."
	if got["q"] != delText || got["q2"] != delText+" The free run does not come back." ||
		got["toast1"] != "The files were not deleted. The run is still here, try again." || got["still"] != true ||
		got["toast2"] != "Deleted" || got["gone"] != true || got["at"] != "/r/64" || got["open"] != true {
		t.Errorf("delete: %v", got)
	}
	if _, _, deleted := f.state(); !slices.Equal(deleted, []int64{65}) {
		t.Errorf("deleted %v, want [65]", deleted)
	}

	// A run still going cannot be deleted; it says so, and the viewer offers to stop a queued one instead.
	got = js[map[string]any](t, ctx, libHelpers+`
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); await wait(600);
	tiles()[1].click(); await wait(600);
	const v = document.querySelector("#viewer");
	return { del: !!v.querySelector(".jacts button[aria-label=Delete]"), stop: v.querySelector(".jacts button")?.textContent, status: v.querySelector(".jbox .c b")?.textContent };`)
	if got["del"] != false || got["stop"] != "Cancel" || got["status"] != "In the queue" {
		t.Errorf("queued run: %v", got)
	}

	// Again on a run with a photo framy keeps: the price of now on the button, the run back on the composer.
	got = js[map[string]any](t, ctx, libHelpers+`
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); await wait(600);
	const t7 = tiles().find((e) => e.getAttribute("href") === "/r/63");
	t7.click(); await wait(600);
	const b = await until(() => { const x = document.querySelector("#viewer [data-again]"); return x && !x.disabled && !/…/.test(x.textContent) && x; });
	const label = b.textContent, ins = document.querySelectorAll("#viewer .vins .thumb").length;
	b.click();
	await wait(900);
	return { label, ins, path: location.pathname, create: vis(document.querySelector("#create")), viewer: document.querySelector("#viewer").hidden,
	  prompt: document.querySelector("#prompt").value, stage: !!document.querySelector("#stage .jbox"), files: document.querySelectorAll("#files .thumb").length };`)
	if !regexp.MustCompile(`^Again · ≈ \$\d+\.\d+$`).MatchString(got["label"].(string)) || got["ins"].(float64) != 1 || got["path"] != "/" || got["create"] != true ||
		got["viewer"] != true || got["prompt"] != "a red bicycle 63" || got["stage"] != true || got["files"].(float64) != 1 {
		t.Errorf("again: %v", got)
	}
	_, created, _ := f.state()
	if len(created) != 1 {
		t.Fatalf("created %v", created)
	}
	c := created[0]
	if c["offer"] != "openrouter:black-forest-labs/flux-3-image" || c["prompt"] != "a red bicycle 63" || c["pay"] != "key" ||
		fmt.Sprint(c["inputs"]) != "[900]" || c["shown_usd"] == nil {
		t.Errorf("again sent %v", c)
	}

	// A run that is not there: said, and the library instead.
	got = js[map[string]any](t, ctx, libHelpers+`
	history.pushState(null, "", "/r/9999"); dispatchEvent(new PopStateEvent("popstate"));
	const msg = await until(() => document.querySelector(".toast")?.textContent === "There is no such run in your library." && document.querySelector(".toast").textContent);
	await wait(100);
	return { msg, path: location.pathname, open: vis(document.querySelector("#viewer")) };`)
	if got["msg"] != "There is no such run in your library." || got["path"] != "/library" || got["open"] != false {
		t.Errorf("missing run: %v", got)
	}
	noErrors(t, ctx)
}

// TestBrowserLibraryPhone checks the library and the viewer at phone widths:
// nothing wider than the screen, the tabs as icons, the actions on the sheet.
func TestBrowserLibraryPhone(t *testing.T) {
	for _, size := range [][2]int64{{375, 812}, {320, 568}} {
		f := newFakeLibrary()
		ts := fakeSite(t, f.mount)
		ctx := browser(t, ts.URL+"/library", size[0], size[1], true)
		got := js[map[string]any](t, ctx, libHelpers+`
		await settled();
		const r = { overflow: document.documentElement.scrollWidth - innerWidth, cols: getComputedStyle(document.querySelector("#library .libgrid")).gridTemplateColumns.split(" ").length,
		  tabText: vis(document.querySelector("#top .tab .t")), dock: vis(document.querySelector("#composer")) };
		tiles()[6].click();
		await wait(700);
		const v = document.querySelector("#viewer"), acts = [...v.querySelectorAll(".jacts > *")];
		r.sheet = vis(v);
		r.actsIn = acts.every((a) => { const b = a.getBoundingClientRect(); return b.left >= 0 && b.right <= innerWidth + 0.5; });
		r.box = Math.round(v.querySelector(".vbox").getBoundingClientRect().width);
		r.wide = v.scrollWidth - v.clientWidth;
		return r;`)
		if got["overflow"].(float64) > 0 || got["cols"].(float64) != 2 || got["tabText"] != false || got["dock"] != false ||
			got["sheet"] != true || got["actsIn"] != true || got["box"].(float64) > float64(size[0]) || got["wide"].(float64) > 0 {
			t.Errorf("%dx%d: %v", size[0], size[1], got)
		}
		noErrors(t, ctx)
	}
}

// TestBrowserLibrarySignedOut checks the library of nobody: it asks to sign
// in, and signing in comes back to it.
func TestBrowserLibrarySignedOut(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL+"/library", 1440, 900, false)
	got := js[map[string]any](t, ctx, libHelpers+`
	const e = await until(() => document.querySelector("#library .lempty h2"));
	return { h: e.textContent, tabs: !!document.querySelector("#top .tabs"), btn: document.querySelector("#library .lempty .go")?.textContent };`)
	if got["h"] != "Your library is in your account" || got["tabs"] != false || got["btn"] != "Sign in with Telegram" {
		t.Errorf("signed out: %v", got)
	}
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[any](`document.querySelector("#library .lempty .go").click()`)); err != nil {
		t.Fatal(err)
	}
	// The page goes away under the check, so it asks again until the new one answers.
	var url string
	for range 40 {
		time.Sleep(250 * time.Millisecond)
		if url, _ = chromedp.Run(ctx, chromedp.Evaluate[string](`location.pathname + location.search`)); url == "/auth/login?return_to=%2Flibrary" {
			break
		}
	}
	if url != "/auth/login?return_to=%2Flibrary" {
		t.Errorf("sign in went to %q", url)
	}
}

// TestBrowserLibraryMini checks the library in the Mini App: no main button
// there, Telegram's back button to the composer, and the address untouched.
func TestBrowserLibraryMini(t *testing.T) {
	f := newFakeLibrary()
	ts := fakeSite(t, f.mount)
	ctx := miniBrowser(t, ts.URL)
	got := js[map[string]any](t, ctx, libHelpers+`
	const r = { main0: __tg.params?.is_visible, back0: __tg.back };
	document.querySelector("#top .tab[data-v=library]").click();
	await wait(500); await settled();
	r.main1 = __tg.params?.is_visible; r.back1 = __tg.back; r.path = location.pathname;
	tiles()[6].click(); await wait(600);
	r.viewer = vis(document.querySelector("#viewer")); r.path2 = location.pathname;
	document.querySelector("#viewer .vexp").click(); await wait(400);
	r.full = vis(document.querySelector("#full")); r.cnt = document.querySelector("#full .cnt").textContent;
	__tg.backClick(); await wait(400);
	r.fullClosed = document.querySelector("#full").hidden; r.viewerKept = vis(document.querySelector("#viewer"));
	__tg.backClick(); await wait(600);
	r.closed = document.querySelector("#viewer").hidden; r.lib = vis(document.querySelector("#library")); r.back3 = __tg.back;
	__tg.backClick(); await wait(600);
	r.create = vis(document.querySelector("#create")); r.main2 = __tg.params?.is_visible; r.back2 = __tg.back;
	return r;`)
	if got["main0"] != true || got["back0"] != false || got["main1"] != false || got["back1"] != true || got["path"] != "/" ||
		got["viewer"] != true || got["path2"] != "/" || got["full"] != true || got["cnt"] != "Image" || got["fullClosed"] != true || got["viewerKept"] != true ||
		got["closed"] != true || got["lib"] != true || got["create"] != true || got["main2"] != true || got["back2"] != false {
		t.Errorf("mini app: %v", got)
	}
	noErrors(t, ctx)
}

// TestBrowserLibrarySmooth walks the library as TestBrowserSmooth walks the
// composer, on the same slowed CPU: the tab, the filter, a search, the next
// page, the viewer with its arrows, the whole screen and back, on a phone and
// on a desktop. A step that held the main thread over 50 ms fails it; each
// step counts its better run of two.
func TestBrowserLibrarySmooth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		w, h  int64
		phone bool
	}{{"phone", 390, 844, true}, {"desktop", 1440, 900, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLibrary()
			ts := fakeSite(t, f.mount)
			ctx := browser(t, ts.URL, tc.w, tc.h, tc.phone)
			scale := slowCPU(t, ctx)
			js[any](t, ctx, pageHelpers+`await wait(1500); return 1;`)
			// One run per call: a run takes longer than js allows a call.
			var runs []map[string]float64
			for range 2 {
				runs = append(runs, js[map[string]float64](t, ctx, libHelpers+`
	let marks = [];
	const step = async (name, fn, ms = 900) => {
	  marks.push([name, performance.now()]);
	  await new Promise((r) => setTimeout(r));
	  fn();
	  await wait(ms);
	};
	const key = (k) => () => document.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));
	const kind = (v) => () => document.querySelector("#library [data-seg=lib] [data-v=" + v + "]").click();
	const type = (v) => () => { const q = document.querySelector("#library .lsearch input"); q.value = v; q.dispatchEvent(new Event("input", { bubbles: true })); };
	  window.__long = [];
	  await step("library", () => document.querySelector("#top .tab[data-v=library]").click(), 1500);
	  await step("videos", kind("video"), 1200);
	  await step("all", kind("all"), 1200);
	  await step("search", type("harbor"), 1200);
	  await step("clear", type(""), 1200);
	  await step("next page", () => scrollTo(0, document.body.scrollHeight), 1500);
	  await step("top", () => scrollTo(0, 0), 400);
	  await step("open", () => tiles()[10].click(), 1000);
	  await step("next run", key("ArrowRight"));
	  await step("back a run", key("ArrowLeft"));
	  await step("whole screen", () => document.querySelector("#viewer .vexp").click());
	  await step("next result", key("ArrowRight"));
	  await step("close whole screen", key("Escape"));
	  await step("close viewer", key("Escape"), 1000);
	  await step("create", () => document.querySelector("#top .tab[data-v=create]").click(), 1200);
	  marks.push(["end", performance.now()]);
	  const run = {};
	  for (const [name] of marks.slice(0, -1)) run[name] = 0;
	  for (const [at, ms] of window.__long) {
	    const m = marks.filter((x) => x[1] <= at).pop();
	    if (m && m[0] !== "end") run[m[0]] = Math.max(run[m[0]], ms);
	  }
	  return run;`))
			}
			// A task known to be long, to show the observer works here.
			if !js[bool](t, ctx, pageHelpers+`window.__long = [];
	await new Promise((r) => setTimeout(() => { const t = performance.now(); while (performance.now() - t < 120); r(); }));
	await wait(300);
	return window.__long.some(([, ms]) => ms >= 100);`) {
				t.Fatal("the browser reports no long tasks, so the check cannot work")
			}
			type task struct {
				Step string  `json:"step"`
				MS   float64 `json:"ms"`
			}
			var worst []task
			for name, ms := range runs[0] {
				best := ms
				for _, r := range runs[1:] {
					if v, ok := r[name]; ok {
						best = min(best, v)
					}
				}
				if best > 0 {
					worst = append(worst, task{name, math.Round(best * scale)})
				}
			}
			for _, l := range worst {
				if l.MS > 50 {
					t.Errorf("steps over 50 ms on the laptop's slow CPU: %v (each run: %v)", worst, runs)
					break
				}
			}
			t.Logf("long tasks, laptop ms: %v", worst)
			noErrors(t, ctx)
		})
	}
}

// TestBrowserViewerJoined checks a run of the library joined to the clip it
// went on from: its tile and the viewer show the joined clip, which Download
// saves, and the new part saves on its own.
func TestBrowserViewerJoined(t *testing.T) {
	f := newFakeLibrary()
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL+"/library", 1440, 900, false)
	got := js[map[string]any](t, ctx, libHelpers+`
	await settled();
	const tile = tiles().find((e) => e.getAttribute("href") === "/r/62");
	const r = { cover: tile?.querySelector(".cover img")?.getAttribute("src"), badge: tile?.querySelector(".badge")?.textContent };
	tile?.click();
	await until(() => vis(document.querySelector("#viewer")) && document.querySelector("#viewBody .vbox video"));
	const v = document.querySelector("#viewer");
	r.video = v.querySelector(".vbox video")?.getAttribute("src");
	r.links = [...v.querySelectorAll(".jacts > a")].map((a) => a.getAttribute("href") + " | " + a.textContent.trim());
	return r;`)
	want := []any{"/test/clip.mp4?download=joined | Download", "/test/clip.mp4?download=1 | New part"}
	if got["cover"] != "/test/result.png?joined" || got["badge"] != "11 s" || got["video"] != "/test/clip.mp4?joined" || fmt.Sprint(got["links"]) != fmt.Sprint(want) {
		t.Errorf("joined in the library: %v", got)
	}
	noErrors(t, ctx)
}
