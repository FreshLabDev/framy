// SPDX-License-Identifier: Apache-2.0

//go:build browser

// The browser tests run the built page in headless Chrome, under the real CSP,
// against a fake API: the catalog from testdata, nobody signed in, and made-up
// prices. They check the layout on a phone and on a desktop, that choosing
// never moves the controls around, and that the motion leaves the main thread
// free on a slow CPU.
//
//	go test -tags=browser ./internal/web/
//
// CHROME_PATH names the browser when it is not on the PATH, and
// FRAMY_BROWSER_ARTIFACTS a directory to keep the page of a failed test in.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// fakeAPI is the site with the routes the page calls before anyone signs in.
func fakeAPI(t *testing.T) *httptest.Server { t.Helper(); return fakeSite(t, nil) }

// fakeSite is the site with the catalog and the prices; signed, when there is
// one, registers who is signed in and the routes of their runs.
func fakeSite(t *testing.T, signed func(*Server)) *httptest.Server {
	t.Helper()
	return fakeSiteWith(t, signed, nil)
}

// fakeSiteWith is fakeSite with the catalog changed by edit first.
func fakeSiteWith(t *testing.T, signed func(*Server), edit func(cat map[string]any)) *httptest.Server {
	t.Helper()
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{R2Endpoint: testR2, CSPExtra: app.CSP()})
	if err != nil {
		t.Fatal(err)
	}
	app.Mount(srv)
	cat, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		var c map[string]any
		if err := json.Unmarshal(cat, &c); err != nil {
			t.Fatal(err)
		}
		edit(c)
		if cat, err = json.Marshal(c); err != nil {
			t.Fatal(err)
		}
	}
	srv.HandleFunc("GET /api/catalog", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cat)
	})
	if signed != nil {
		signed(srv)
	} else {
		srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"sign_in","message":"sign in first"}`))
		})
	}
	srv.HandleFunc("POST /api/quote", fakeQuote)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// fakeQuote prices every run by a hash of what it asks, so each choice has its
// own price and the same choice always the same one.
func fakeQuote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []struct {
			Offer    string         `json:"offer"`
			Pay      string         `json:"pay"`
			Settings map[string]any `json:"settings"`
		} `json:"items"`
		Batch bool `json:"batch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	type estimate struct {
		Kind     string  `json:"kind"`
		USD      float64 `json:"usd"`
		MaxUSD   float64 `json:"max_usd"`
		Currency string  `json:"currency"`
		Label    string  `json:"label"`
	}
	type item struct {
		Offer    string         `json:"offer"`
		Estimate estimate       `json:"estimate"`
		Settings map[string]any `json:"settings"`
	}
	out := struct {
		Currency string    `json:"currency"`
		Items    []item    `json:"items"`
		Jobs     [][]int   `json:"jobs,omitempty"`
		Total    *estimate `json:"total,omitempty"`
	}{Currency: "USD"}
	// For a batch, the items with the same offer, payer and settings go as
	// one job, as the server sends them, and the total is of the paid ones.
	group, sum := map[string]int{}, 0.0
	for i, it := range in.Items {
		h := fnv.New32a()
		b, _ := json.Marshal(it.Settings)
		_, _ = h.Write([]byte(it.Offer))
		_, _ = h.Write(b)
		usd := 0.01 + float64(h.Sum32()%400)/1000
		out.Items = append(out.Items, item{Offer: it.Offer, Settings: it.Settings,
			Estimate: estimate{Kind: "about", USD: usd, MaxUSD: usd, Currency: "USD", Label: fmt.Sprintf("$%.3f", usd)}})
		if !in.Batch {
			continue
		}
		key := it.Offer + "\x00" + it.Pay + "\x00" + string(b)
		if g, ok := group[key]; ok {
			out.Jobs[g] = append(out.Jobs[g], i)
		} else {
			group[key] = len(out.Jobs)
			out.Jobs = append(out.Jobs, []int{i})
		}
		if it.Pay != "free" {
			sum += usd
			out.Total = &estimate{Kind: "about", USD: sum, MaxUSD: sum, Currency: "USD", Label: fmt.Sprintf("$%.3f", sum)}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// watch collects CSP violations, errors and long tasks from the first line of
// every document; the page reads them back as window.__errs and __long.
//
// It also counts what the page is still doing: requests and their bodies in
// flight, timers of a second or less, and animations that end. window.__idle
// is true when there is none, which is how settle (pageHelpers) knows the page
// is done rather than guessing how long that takes on this machine. The
// checks' own pauses go through __sleep, the timer from before, so they do
// not count.
const watch = `
window.__errs = []; window.__long = [];
addEventListener("securitypolicyviolation", (e) => __errs.push("csp: " + e.violatedDirective + " " + e.blockedURI));
addEventListener("error", (e) => __errs.push("error: " + e.message));
addEventListener("unhandledrejection", (e) => __errs.push("rejection: " + (e.reason && e.reason.message || e.reason)));
try { new PerformanceObserver((l) => { for (const e of l.getEntries()) __long.push([e.startTime, Math.round(e.duration)]); }).observe({ type: "longtask", buffered: true }); } catch (e) {}
(() => {
  const st = window.setTimeout, ct = window.clearTimeout, timers = new Set();
  let busy = 0;
  window.__sleep = (ms) => new Promise((r) => st(r, ms));
  window.setTimeout = function (fn, ms, ...args) {
    if (typeof fn !== "function" || (ms || 0) > 1000) return st(fn, ms, ...args);
    const id = st(() => { timers.delete(id); fn(...args); }, ms);
    timers.add(id);
    return id;
  };
  window.clearTimeout = (id) => { timers.delete(id); ct(id); };
  const counted = (p) => { busy++; return p.finally(() => busy--); };
  const fetch0 = window.fetch;
  window.fetch = (...a) => counted(fetch0.apply(window, a));
  for (const m of ["json", "text", "blob", "arrayBuffer"]) {
    const read = Response.prototype[m];
    Response.prototype[m] = function () { return counted(read.call(this)); };
  }
  const moving = (a) => a.playState === "running" && a.effect && a.effect.getComputedTiming().endTime !== Infinity;
  window.__idle = () => busy === 0 && timers.size === 0 && !document.getAnimations().some(moving);
})();
`

// browser opens the page in a fresh headless Chrome of the given size.
func browser(t *testing.T, url string, w, h int64, phone bool) context.Context {
	t.Helper()
	return browserInit(t, url, w, h, phone, "")
}

// browserInit is browser with a script of the test's own that runs before
// the page's first line, as watch does: a language to open in, a fetch to
// change.
func browserInit(t *testing.T, url string, w, h int64, phone bool, init string) context.Context {
	t.Helper()
	// No sandbox: the runners of CI do not allow Chrome's, and the page is our own.
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(int(w), int(h)), chromedp.NoSandbox)
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelC := chromedp.NewContext(actx)
	t.Cleanup(func() { cancelC(); cancelA() })
	keepOnFailure(t, ctx)
	vp := []chromedp.EmulateViewportOption{}
	if phone {
		vp = append(vp, chromedp.EmulateMobile, chromedp.EmulateTouch, chromedp.EmulateScale(2))
	}
	err := chromedp.Do(ctx,
		chromedp.EmulateViewport(w, h, vp...),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: watch + init})
			return err
		}),
		chromedp.Navigate(url),
	)
	if err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	return ctx
}

// kept numbers the pages keepOnFailure saved, for a test that opens several.
var kept atomic.Int32

// keepOnFailure saves the page as it was when the test failed, into the
// directory FRAMY_BROWSER_ARTIFACTS names: a screenshot of the whole page,
// its document, and what it reported. On a runner the page is gone by the
// time anyone reads the log, and a line saying a bar is 3 px off says
// nothing of why. Without the variable it does nothing.
func keepOnFailure(t *testing.T, ctx context.Context) {
	dir := os.Getenv("FRAMY_BROWSER_ARTIFACTS")
	if dir == "" {
		return
	}
	// Registered after the browser's own cleanup, so it runs before it.
	t.Cleanup(func() {
		if !t.Failed() || ctx.Err() != nil {
			return
		}
		name := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
				return r
			}
			return '_'
		}, t.Name())
		base := filepath.Join(dir, fmt.Sprintf("%s-%d", name, kept.Add(1)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Logf("keep the failed page: %v", err)
			return
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if png, err := chromedp.Run(c, chromedp.FullScreenshot(100)); err == nil {
			_ = os.WriteFile(base+".png", png, 0o644)
		} else {
			t.Logf("keep the failed page's screenshot: %v", err)
		}
		state, err := chromedp.Run(c, chromedp.Evaluate[map[string]any](`({url: location.href, errs: window.__errs || [], html: document.documentElement.outerHTML})`))
		if err != nil {
			t.Logf("keep the failed page's document: %v", err)
			return
		}
		html, _ := state["html"].(string)
		_ = os.WriteFile(base+".html", []byte(html), 0o644)
		delete(state, "html")
		if b, err := json.MarshalIndent(state, "", "  "); err == nil {
			_ = os.WriteFile(base+".json", b, 0o644)
		}
		t.Logf("the failed page is kept as %s.{png,html,json}", base)
	})
}

// TestBrowserNotesInPageLanguage opens the page in Russian over a catalog
// whose notes came in English, as the server writes them for a request
// without the person and a browser whose first language framy does not
// speak: the notes under the models are in the page's language anyway.
func TestBrowserNotesInPageLanguage(t *testing.T) {
	ts := fakeSite(t, nil)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, `localStorage.setItem("framy.language", "ru"); return true;`)
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	s := js[map[string]any](t, ctx, `return { lang: document.documentElement.lang, note: document.querySelector("#how .mnote.info")?.textContent || "",
		titles: [...document.querySelectorAll('#how [data-seg="var"] [title]')].map((b) => b.title).join(" | ") };`)
	if s["lang"] != "ru" || s["note"] != "Быстрее и дешевле, чем MAI-Image 2.6, качество близко" || !strings.Contains(s["titles"].(string), "2.6: Точнее, чем 2.6 Flash, для дизайна") {
		t.Errorf("notes of a Russian page: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserFatalInPageLanguage opens a German page whose lines and catalog
// do not come: the page says so in German all the same, from lines inside
// app.js. Loaded, the page prints Ctrl as a German keyboard does, Strg, and
// tells a screen reader the shortcut.
func TestBrowserFatalInPageLanguage(t *testing.T) {
	ts := fakeSite(t, nil)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, `localStorage.setItem("framy.language", "de"); return true;`)
	if _, err := chromedp.Call(ctx, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*/api/catalog*"}, {URLPattern: "*/texts-*"}}}); err != nil {
		t.Fatal(err)
	}
	paused := chromedp.Events(ctx, fetch.RequestPaused)
	go func() {
		for ev, err := range paused {
			if err != nil {
				return
			}
			_, _ = chromedp.Call(ctx, fetch.FulfillRequest, fetch.FulfillRequestParams{RequestID: ev.RequestID, ResponseCode: 503, Body: []byte("down")})
		}
	}()
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	got := js[map[string]any](t, ctx, pageHelpers+`
	for (let i = 0; i < 100 && !document.querySelector(".fatal"); i++) await wait(100);
	const f = document.querySelector(".fatal");
	return { lang: document.documentElement.lang, text: f?.querySelector("p")?.textContent || "", again: f?.querySelector("button")?.textContent || "", mark: (f?.querySelector("img")?.getBoundingClientRect().width || 0) === 48 };`)
	if got["lang"] != "de" || got["text"] != "framy konnte nicht geladen werden. Prüfe die Verbindung und versuche es noch einmal." || got["again"] != "Erneut versuchen" || got["mark"] != true {
		t.Errorf("the page that could not load, in German: %v", got)
	}

	if _, err := chromedp.Call(ctx, fetch.Disable, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: `Object.defineProperty(navigator, "platform", { get: () => "Win32" });`})
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	kbd := js[map[string]any](t, ctx, `const go = document.querySelector("#go");
	return { kbd: go.querySelector(".kbd")?.textContent || "", hidden: go.querySelector(".kbd")?.getAttribute("aria-hidden"), keys: go.getAttribute("aria-keyshortcuts"), title: go.title };`)
	if kbd["kbd"] != "Strg ↵" || kbd["hidden"] != "true" || kbd["keys"] != "Control+Enter" || !strings.HasSuffix(kbd["title"].(string), "(Strg ↵)") {
		t.Errorf("the shortcut on a German page: %v", kbd)
	}
	noErrors(t, ctx)
}

// TestBrowserCyrillicForms opens the page in Ukrainian: its Cyrillic has the
// forms Russian has, not Sofia Sans's Bulgarian ones (a т like an m is twice
// as wide), in both faces and whatever language an element is in.
func TestBrowserCyrillicForms(t *testing.T) {
	ts := fakeSite(t, nil)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, `localStorage.setItem("framy.language", "uk"); return true;`)
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	s := js[map[string]any](t, ctx, `
	const text = "Додати твій ґудзик", out = { lang: document.documentElement.lang };
	for (const font of ["400 40px 'Sofia Sans'", "700 40px 'Sofia Sans Extra Condensed'"]) {
	  await document.fonts.load(font, text);
	  const w = {};
	  for (const lang of ["ru", "uk", "be", "en"]) {
	    const el = document.createElement("span");
	    Object.assign(el.style, { font, position: "absolute", whiteSpace: "nowrap" });
	    el.lang = lang;
	    el.textContent = text;
	    document.body.append(el);
	    w[lang] = el.getBoundingClientRect().width;
	    el.remove();
	  }
	  out[font] = w;
	}
	return out;`)
	if s["lang"] != "uk" {
		t.Fatalf("the page is in %v", s["lang"])
	}
	for _, font := range []string{"400 40px 'Sofia Sans'", "700 40px 'Sofia Sans Extra Condensed'"} {
		w, _ := s[font].(map[string]any)
		if ru, _ := w["ru"].(float64); ru == 0 || w["uk"] != ru || w["be"] != ru || w["en"] != ru {
			t.Errorf("%s: the width of the line in each language %v, want the Russian one in all", font, w)
		}
	}
	noErrors(t, ctx)
}

// ready waits until the app has replaced the skeleton and the prices are in.
func ready(t *testing.T, ctx context.Context) {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := chromedp.Run(c, chromedp.Poll[bool](`!document.querySelector("#app").hidden && document.querySelector("#skel").hidden &&
		!!document.querySelector("#go").textContent && !document.querySelector("#composer .q.wait") && /\S/.test(document.querySelector("#pnum").textContent)`,
		chromedp.WithPollingInterval(50*time.Millisecond)))
	if err != nil {
		t.Fatalf("the app did not come up: %v", err)
	}
}

// leave marks the document about to go, for reloaded: put it in the same
// step as what makes the page load again.
const leave = `window.__left = true;`

// reloaded waits until a document marked with leave has been replaced and the
// app on the new one is up. A fixed pause is a guess: on a slow machine the
// old page is still there after it, up and looking the same.
func reloaded(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		// Between two documents an evaluation can fail; it is asked again.
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		gone, err := chromedp.Run(c, chromedp.Evaluate[bool](`!window.__left && document.readyState !== "loading"`))
		cancel()
		if err == nil && gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the page did not load again: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	ready(t, ctx)
}

// js runs an async page function and decodes its result.
func js[T any](t *testing.T, ctx context.Context, body string) T {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v, err := chromedp.Run(c, chromedp.Evaluate[T]("(async () => {"+body+"})()", chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	return v
}

// noErrors fails on anything the page reported.
func noErrors(t *testing.T, ctx context.Context) {
	t.Helper()
	if errs := js[[]string](t, ctx, `return window.__errs;`); len(errs) > 0 {
		t.Errorf("the page reported:\n%s", strings.Join(errs, "\n"))
	}
}

// pageHelpers are shared by the checks below.
//
// wait(ms) gives the page at least ms and then as long as it is still busy
// (settle), so a pause tuned on a laptop is not too short on a runner four
// times slower; it never returns sooner than ms, so a check that nothing
// happens in that time still waits it out. until polls a condition.
const pageHelpers = `
const sleep = window.__sleep || ((ms) => new Promise((r) => setTimeout(r, ms)));
// settle and until are set on the page, not declared, so a test with its own of the same name keeps it.
globalThis.settle = async (most = 4000) => {
  const end = Date.now() + most;
  for (let calm = 0; Date.now() < end; await sleep(20)) {
    calm = !window.__idle || window.__idle() ? calm + 1 : 0;
    if (calm >= 2) return true;
  }
  return false;
};
const wait = async (ms) => { await sleep(ms); await settle(); };
globalThis.until = async (fn, ms = 8000) => { const end = Date.now() + ms; while (Date.now() < end) { const v = fn(); if (v) return v; await sleep(50); } return fn(); };
const vis = (el) => !!el && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== "hidden";
// Where a button starts across and how big it is. Its top may move when a
// line above it changes, as the facts of another variant do.
const box = (el) => { const r = el.getBoundingClientRect(); return [r.left, r.width, r.height].map((x) => Math.round(x * 2) / 2); };
// Choosing in a group of choices: the buttons keep their places and sizes.
async function steady() {
  const bad = [];
  const groups = [...document.querySelectorAll("#how [data-seg]")].filter(vis);
  for (const g of groups) {
    const key = g.dataset.seg, bs = [...g.querySelectorAll(":scope > button")];
    const other = bs.find((b) => b.getAttribute("aria-checked") !== "true" && !b.disabled);
    if (!other) continue;
    const before = bs.map(box);
    other.click();
    await wait(700);
    const now = document.querySelector("#how [data-seg='" + CSS.escape(key) + "']");
    if (!now) continue;
    const after = [...now.querySelectorAll(":scope > button")].map(box);
    if (after.length !== before.length) continue;
    if (JSON.stringify(after) !== JSON.stringify(before)) bad.push(key + ": " + JSON.stringify(before) + " -> " + JSON.stringify(after));
  }
  return { groups: groups.length, bad };
}
`

func TestBrowserPhone(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 390, 844, true)
	got := js[map[string]any](t, ctx, pageHelpers+`
	const c = document.querySelector("#composer").getBoundingClientRect(), pv = document.querySelector("#stage [data-k=pv]").getBoundingClientRect();
	return {
	  overflow: document.documentElement.scrollWidth - innerWidth,
	  dockBottom: Math.round(innerHeight - c.bottom), dockFixed: getComputedStyle(document.querySelector("#composer")).position,
	  stageAbove: Math.round(c.top - pv.bottom), chip: vis(document.querySelector("#dtools .chipbtn")), how: vis(document.querySelector("#composer > #how")),
	  top: !!document.querySelector("#top .signin"),
	  cap: document.querySelector("#pcap").textContent, capCut: document.querySelector("#pcap").scrollWidth - document.querySelector("#pcap").clientWidth,
	};`)
	if got["overflow"].(float64) > 0 || got["dockBottom"].(float64) != 0 || got["dockFixed"] != "fixed" || got["stageAbove"].(float64) < 0 ||
		got["chip"] != true || got["how"] != false || got["top"] != true {
		t.Errorf("phone layout: %v", got)
	}
	// Signed out, the line under the price says the allowance whole, its
	// window too: the button beside it already says to sign in.
	if got["cap"] != "5 free in any 24 hours" || got["capCut"].(float64) > 0 {
		t.Errorf("the line under the price: %q, cut by %v px", got["cap"], got["capCut"])
	}
	// The settings open in a sheet, and choosing there keeps every control in place.
	res := js[map[string]any](t, ctx, pageHelpers+`
	document.querySelector("#dtools .chipbtn.set").click();
	await wait(600);
	const open = vis(document.querySelector("#tune")) && !!document.querySelector("#tuneBody #how");
	const s = await steady();
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	await wait(600);
	return { open, back: !!document.querySelector("#composer > #how"), closed: document.querySelector("#tune").hidden, ...s };`)
	if res["open"] != true || res["back"] != true || res["closed"] != true || len(res["bad"].([]any)) > 0 {
		t.Errorf("settings sheet: %v", res)
	}
	pickAnother(t, ctx, "#dtools .chipbtn:not(.set)")
	noErrors(t, ctx)
}

// TestBrowserSmallPhone checks the composer on the smallest phone (320 by
// 568, an iPhone SE): the model's whole name shows in its chip, the empty
// frame keeps what it says inside it, and the line under the price is read
// to its end, signed in or not.
func TestBrowserSmallPhone(t *testing.T) {
	runs := &fakeRuns{}
	for name, ts := range map[string]*httptest.Server{"signed out": fakeAPI(t), "signed in": fakeSite(t, runs.mount)} {
		ctx := browser(t, ts.URL, 320, 568, true)
		got := js[map[string]any](t, ctx, pageHelpers+`
	await wait(400);
	const clipped = (el) => !!el && (el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1);
	const g = document.querySelector("#stage .ghost"), gr = g.getBoundingClientRect();
	const inside = [...g.querySelectorAll(".c > *")].filter(vis).every((el) => { const r = el.getBoundingClientRect(); return r.top >= gr.top && r.bottom <= gr.bottom && r.left >= gr.left && r.right <= gr.right; });
	const dock = document.querySelector("#composer").getBoundingClientRect();
	return { name: clipped(document.querySelector("#dtools .chipbtn .nm > span")), cap: clipped(document.querySelector("#pcap")), inside,
	  size: Math.round(gr.height), above: Math.round(dock.top - gr.bottom), overflow: document.documentElement.scrollWidth - innerWidth };`)
		if got["name"] != false || got["cap"] != false || got["inside"] != true || got["size"].(float64) < 110 || got["above"].(float64) < 0 || got["overflow"].(float64) > 0 {
			t.Errorf("%s at 320x568: %v", name, got)
		}
		noErrors(t, ctx)
	}
}

func TestBrowserDesktop(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+`
	const c = document.querySelector("#composer").getBoundingClientRect(), st = document.querySelector("#stage").getBoundingClientRect();
	const pv = document.querySelector("#stage [data-k=pv]").getBoundingClientRect();
	return {
	  overflow: document.documentElement.scrollWidth - innerWidth, side: Math.round(st.left - c.right),
	  fits: pv.bottom <= innerHeight, how: vis(document.querySelector("#composer > #how")), chip: vis(document.querySelector("#dtools .chipbtn")),
	  font: document.fonts.check("700 20px 'Sofia Sans Extra Condensed'"),
	};`)
	if got["overflow"].(float64) > 0 || got["side"].(float64) < 24 || got["fits"] != true || got["how"] != true || got["chip"] != false || got["font"] != true {
		t.Errorf("desktop layout: %v", got)
	}
	// Nobody signed in and nothing typed: the button signs in at once, and the line under the price says what that gives.
	// The language is guessed from the browser, and a guess is not kept.
	bar := js[map[string]any](t, ctx, `const go = document.querySelector("#go");
	return { go: go.textContent, on: !go.disabled, cap: document.querySelector("#pcap").textContent, prompt: document.querySelector("#prompt").value,
	  kept: localStorage.getItem("framy.language") };`)
	if bar["prompt"] != "" || bar["on"] != true || !strings.HasPrefix(bar["go"].(string), "Sign in to create") || bar["cap"] != "5 free in any 24 hours" || bar["kept"] != nil {
		t.Errorf("signed out, nothing typed: %v", bar)
	}
	res := js[map[string]any](t, ctx, pageHelpers+`return await steady();`)
	if res["groups"].(float64) == 0 || len(res["bad"].([]any)) > 0 {
		t.Errorf("choosing moved controls: %v", res)
	}
	pickAnother(t, ctx, "#how .mhead")
	noErrors(t, ctx)
}

// pickAnother opens the picker from the opener, chooses another family and
// checks that the composer shows it.
func pickAnother(t *testing.T, ctx context.Context, opener string) {
	t.Helper()
	res := js[map[string]any](t, ctx, pageHelpers+`
	// The name the person sees: the chip on a phone, the card on a desktop.
	const name = () => [...document.querySelectorAll("#how .mname > span, #dtools .chipbtn .nm > span")].find(vis).textContent;
	const before = name();
	document.querySelector(`+jsString(opener)+`).click();
	await wait(700);
	const rows = [...document.querySelectorAll("#pkrows .frow")];
	const row = rows.find((r) => r.getAttribute("aria-current") !== "true" && !r.classList.contains("dim"));
	const shown = vis(document.querySelector("#picker"));
	row.querySelector(".fhit").click();
	await wait(300);
	// A phone shows the detail under the row; a desktop beside the list.
	const choose = [...document.querySelectorAll("#picker .det .choose")].find(vis);
	if (choose && !choose.disabled) choose.click();
	await wait(900);
	return { shown, rows: rows.length, closed: document.querySelector("#picker").hidden, before, after: name() };`)
	if res["shown"] != true || res["rows"].(float64) < 3 || res["closed"] != true || res["before"] == res["after"] {
		t.Errorf("picker: %v", res)
	}
}

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }

// cpuRef is how long cpuWork takes on the machine the budget was set on, an
// Apple M-series laptop, in ms.
const cpuRef = 6.5

// slowCPU slows the page's CPU until cpuWork takes four times cpuRef, and
// returns what a time measured now is multiplied by to count on the laptop.
func slowCPU(t *testing.T, ctx context.Context) float64 {
	t.Helper()
	target := 4 * cpuRef
	fast := js[float64](t, ctx, cpuWork)
	rate := max(1, target/fast)
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, emulation.SetCPUThrottlingRate, emulation.SetCPUThrottlingRateParams{Rate: rate})
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	// The same work again, to see the slowdown take hold.
	slow := js[float64](t, ctx, cpuWork)
	if rate >= 2 && slow < rate/2*fast {
		t.Fatalf("the CPU is not slower: %.1f ms, then %.1f ms", fast, slow)
	}
	// Slowed to the laptop's speed, a time counts as it is; on a machine
	// slower than that, it is brought down by how much slower.
	scale := 1.0
	if rate == 1 {
		scale = min(1, target/slow)
	}
	t.Logf("work: %.1f ms here, %.1f ms on the laptop; slowed %.1f times to %.1f ms, times count %.2f", fast, cpuRef, rate, slow, scale)
	return scale
}

// pace says what a time measured now is multiplied by to count on the
// laptop, once slowCPU has set the pace. A machine that got busy with
// something else since (another test binary, a build) does the same work
// slower than slowCPU saw, and its times count down by as much.
func pace(t *testing.T, ctx context.Context) float64 {
	t.Helper()
	return min(1, 4*cpuRef/js[float64](t, ctx, cpuWork))
}

// paced multiplies each time of run by the slower pace of the two around it.
func paced(run map[string]float64, before, after float64) map[string]float64 {
	f := min(before, after)
	for k, v := range run {
		run[k] = math.Round(v * f)
	}
	return run
}

// cpuWork is the kind of work the steps do: it builds rows of text, has the
// browser style and lay them out, and reads them back. The best of three, in
// ms, so a cold start does not count. A CI runner is slower at this than at
// plain arithmetic, so arithmetic would not measure it.
const cpuWork = `let best = Infinity;
for (let n = 0; n < 3; n++) {
  const t = performance.now();
  const box = document.createElement("div");
  box.style.cssText = "position:absolute;left:0;top:0;width:360px;display:grid;gap:4px;visibility:hidden";
  for (let i = 0; i < 300; i++) {
    const r = document.createElement("div");
    r.style.cssText = "display:flex;gap:8px;align-items:center;padding:6px 10px";
    r.innerHTML = "<b>Model " + i + "</b><span>Up to 4K, up to 10 photos</span><small>$0." + i + "</small>";
    box.append(r);
  }
  document.body.append(box);
  let y = 0;
  for (const r of box.children) y += r.lastChild.getBoundingClientRect().width;
  box.remove();
  best = Math.min(best, performance.now() - t);
}
return best;`

// TestBrowserSmooth switches the kind, a variant, the settings sheet and the
// picker on a phone and on a desktop, as on a CPU four times slower than that
// laptop, and fails on a step that held the main thread over 50 ms.
//
// The CPU is slowed until cpuWork takes four times cpuRef. A machine that is
// slower than that already, like a CI runner, is not slowed, and its times
// are brought down to the laptop's by the same ratio. The pace is measured
// again around each run, so a machine that got busy with something else in
// the meantime (another test binary, a build) counts its slower steps at the
// laptop's speed too. The steps run three times, each time on the page
// loaded afresh, so the first, cold opening of the picker (its rows built,
// its marks drawn for the first time) counts as a person meets it; each step
// counts its best run, so one hiccup of a shared runner does not fail it.
func TestBrowserSmooth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		w, h  int64
		phone bool
		// steps opens and closes what this size has.
		steps string
	}{
		{"phone", 390, 844, true, `
	await step("settings open", () => document.querySelector("#dtools .chipbtn.set").click());
	await variant();
	await step("settings close", esc);
	await step("picker open", () => document.querySelector("#dtools .chipbtn").click(), 1200);
	await step("picker close", esc);`},
		{"desktop", 1440, 900, false, `
	await variant();
	await step("picker open", () => document.querySelector("#how .mhead").click(), 1200);
	await step("picker close", esc);`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := fakeAPI(t)
			ctx := browser(t, ts.URL, tc.w, tc.h, tc.phone)
			scale := slowCPU(t, ctx)
			type task struct {
				Step string  `json:"step"`
				MS   float64 `json:"ms"`
			}
			// For each step, the longest task of each run, in the laptop's ms.
			var runs []map[string]float64
			for n := range 3 {
				if n > 0 {
					if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
						t.Fatal(err)
					}
					ready(t, ctx)
				}
				before := pace(t, ctx)
				run := js[map[string]float64](t, ctx, pageHelpers+`
	// Each step starts in a task of its own, so every task that begins after
	// its mark and before the next one is that step's.
	let marks = [];
	const step = async (name, fn, ms = 900) => {
	  marks.push([name, performance.now()]);
	  await new Promise((r) => setTimeout(r));
	  fn();
	  await wait(ms);
	};
	const esc = () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	const variant = async () => {
	  const v = [...document.querySelectorAll("#how [data-seg=var] > button")].find((b) => b.getAttribute("aria-checked") !== "true");
	  if (v) await step("variant", () => v.click());
	};
	await wait(2500); // the idle prefetch of the picker's prices
	window.__long = [];
	const kind = [...document.querySelectorAll("#kind > button")];
	await step("video", () => kind[1].click());
	await step("image", () => kind[0].click());
	`+tc.steps+`
	marks.push(["end", performance.now()]);
	const run = {};
	for (const [name] of marks.slice(0, -1)) run[name] = 0;
	// A task from before the first step can be reported late.
	for (const [at, ms] of window.__long) {
	  const m = marks.filter((x) => x[1] <= at).pop();
	  if (m && m[0] !== "end") run[m[0]] = Math.max(run[m[0]], ms);
	}
	return run;`)
				runs = append(runs, paced(run, before, pace(t, ctx)))
			}
			// A task known to be long, to show the observer works here, and
			// that the page loaded again is as slow as the first.
			if !js[bool](t, ctx, pageHelpers+`window.__long = [];
	await new Promise((r) => setTimeout(() => { const t = performance.now(); while (performance.now() - t < 120); r(); }));
	await wait(300);
	return window.__long.some(([, ms]) => ms >= 100);`) {
				t.Fatal("the browser reports no long tasks, so the check cannot work")
			}
			if slow := js[float64](t, ctx, cpuWork); slow*scale < 2*cpuRef {
				t.Fatalf("the page loaded again is not slowed: %.1f ms", slow)
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
					worst = append(worst, task{name, best})
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

// moves records, from the first line of every document, what moves on the
// page: each call of animate(), each CSS animation and each transition of
// where or how big a thing is, and each smooth scroll. A change of colour or
// of opacity is no move.
const moves = `
window.__moves = [];
const who = (el) => el ? (el.id ? "#" + el.id : el.tagName.toLowerCase() + (typeof el.className === "string" && el.className ? "." + el.className.split(" ")[0] : "")) : "";
const anim0 = Element.prototype.animate;
Element.prototype.animate = function (kf, o) { __moves.push("animate " + who(this)); return anim0.call(this, kf, o); };
addEventListener("animationstart", (e) => __moves.push("css " + e.animationName + " " + who(e.target)), true);
addEventListener("transitionrun", (e) => { if (!/color|opacity|shadow|outline/.test(e.propertyName)) __moves.push("transition " + e.propertyName + " " + who(e.target)); }, true);
for (const o of [Element.prototype, window]) for (const k of ["scrollTo", "scrollBy", "scrollIntoView"]) {
  const f0 = o[k];
  if (f0) o[k] = function (a, ...r) { if (a && a.behavior === "smooth") __moves.push(k + " smooth " + who(this === window ? null : this)); return f0.call(this, a, ...r); };
}
`

// TestBrowserReducedMotionWalk walks the site with "reduce motion" asked, on a
// phone and on a desktop: the kind, a variant, the sheets, the picker, the
// person's settings, a run that comes back done, the library, the viewer and
// the whole screen. Nothing may move on the way, as design.md says of the
// motion kit: every animation goes through motion(), off when less motion is
// asked.
func TestBrowserReducedMotionWalk(t *testing.T) {
	for _, tc := range []struct {
		name  string
		w, h  int64
		phone bool
		// sheets opens and closes what this size has.
		sheets string
	}{
		{"phone", 390, 844, true, `
	await step("settings", () => document.querySelector("#dtools .chipbtn.set").click());
	await step("settings closed", esc);
	await step("picker", () => document.querySelector("#dtools .chipbtn").click(), 1200);
	await step("picker closed", esc);`},
		{"desktop", 1440, 900, false, `
	await step("picker", () => document.querySelector("#how .mhead").click(), 1200);
	await step("picker closed", esc);`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLibrary()
			ts := fakeSite(t, f.mount)
			ctx := browser(t, ts.URL, tc.w, tc.h, tc.phone)
			// Asked before the page loads again, so its first frame is counted too.
			err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
				if _, err := cdp.Call(ctx, tg, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Features: []*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}}}); err != nil {
					return err
				}
				_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: moves})
				return err
			}), chromedp.Reload())
			if err != nil {
				t.Fatal(err)
			}
			ready(t, ctx)
			got := js[map[string]any](t, ctx, libHelpers+`
	const seen = {};
	const step = async (name, fn, ms = 700) => {
	  const n = __moves.length;
	  try { fn(); } catch (e) { seen[name] = ["failed: " + e.message]; }
	  await wait(ms);
	  if (__moves.length > n) seen[name] = [...new Set(__moves.slice(n))];
	};
	const esc = () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
	const key = (k) => () => document.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));
	await wait(600);
	if (__moves.length) seen.load = [...new Set(__moves)];
	const kind = [...document.querySelectorAll("#kind > button")];
	await step("video", () => kind[1].click());
	await step("image", () => kind[0].click());
	const v = [...document.querySelectorAll("#how [data-seg=var] > button")].find((b) => b.getAttribute("aria-checked") !== "true");
	if (v) await step("variant", () => v.click());
	`+tc.sheets+`
	await step("account", () => document.querySelector("#meBtn").click(), 900);
	await step("account closed", esc);
	await step("create", () => { const p = document.querySelector("#prompt"); p.value = "a fox"; p.dispatchEvent(new Event("input")); setTimeout(() => document.querySelector("#go").click(), 300); }, 2000);
	const done = !!document.querySelector("#stage .jbox.done img");
	await step("library", () => document.querySelector("#top .tab[data-v=library]").click(), 1200);
	await settled();
	await step("videos", () => document.querySelector("#library [data-seg=lib] [data-v=video]").click(), 1000);
	await step("all", () => document.querySelector("#library [data-seg=lib] [data-v=all]").click(), 1000);
	await settled();
	await step("viewer", () => tiles()[10].click(), 1000);
	await step("next run", key("ArrowRight"));
	await step("whole screen", () => document.querySelector("#viewer .vexp").click());
	await step("next result", key("ArrowRight"));
	await step("whole screen closed", key("Escape"));
	await step("viewer closed", key("Escape"), 900);
	await step("back to create", () => document.querySelector("#top .tab[data-v=create]").click(), 900);
	return { reduced: matchMedia("(prefers-reduced-motion: reduce)").matches, done, seen };`)
			if got["reduced"] != true || got["done"] != true {
				t.Fatalf("the walk did not go as planned: %v", got)
			}
			if seen, _ := got["seen"].(map[string]any); len(seen) > 0 {
				t.Errorf("moves with less motion asked: %v", seen)
			}
			noErrors(t, ctx)
		})
	}
}
