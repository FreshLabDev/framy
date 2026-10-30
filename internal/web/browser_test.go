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
// CHROME_PATH names the browser when it is not on the PATH.
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
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// fakeAPI is the site with the routes the page calls before anyone signs in.
func fakeAPI(t *testing.T) *httptest.Server { t.Helper(); return fakeSite(t, nil) }

// fakeSite is the site with the catalog and the prices; signed, when there is
// one, registers who is signed in and the routes of their runs.
func fakeSite(t *testing.T, signed func(*Server)) *httptest.Server {
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
			Settings map[string]any `json:"settings"`
		} `json:"items"`
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
		Currency string `json:"currency"`
		Items    []item `json:"items"`
	}{Currency: "USD"}
	for _, it := range in.Items {
		h := fnv.New32a()
		b, _ := json.Marshal(it.Settings)
		_, _ = h.Write([]byte(it.Offer))
		_, _ = h.Write(b)
		usd := 0.01 + float64(h.Sum32()%400)/1000
		out.Items = append(out.Items, item{Offer: it.Offer, Settings: it.Settings,
			Estimate: estimate{Kind: "about", USD: usd, MaxUSD: usd, Currency: "USD", Label: fmt.Sprintf("$%.3f", usd)}})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// watch collects CSP violations, errors and long tasks from the first line of
// every document; the page reads them back as window.__errs and __long.
const watch = `
window.__errs = []; window.__long = [];
addEventListener("securitypolicyviolation", (e) => __errs.push("csp: " + e.violatedDirective + " " + e.blockedURI));
addEventListener("error", (e) => __errs.push("error: " + e.message));
addEventListener("unhandledrejection", (e) => __errs.push("rejection: " + (e.reason && e.reason.message || e.reason)));
try { new PerformanceObserver((l) => { for (const e of l.getEntries()) __long.push([e.startTime, Math.round(e.duration)]); }).observe({ type: "longtask", buffered: true }); } catch (e) {}
`

// browser opens the page in a fresh headless Chrome of the given size.
func browser(t *testing.T, url string, w, h int64, phone bool) context.Context {
	t.Helper()
	// No sandbox: the runners of CI do not allow Chrome's, and the page is our own.
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(int(w), int(h)), chromedp.NoSandbox)
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelC := chromedp.NewContext(actx)
	t.Cleanup(func() { cancelC(); cancelA() })
	vp := []chromedp.EmulateViewportOption{}
	if phone {
		vp = append(vp, chromedp.EmulateMobile, chromedp.EmulateTouch, chromedp.EmulateScale(2))
	}
	err := chromedp.Do(ctx,
		chromedp.EmulateViewport(w, h, vp...),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: watch})
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
const pageHelpers = `
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
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
	};`)
	if got["overflow"].(float64) > 0 || got["dockBottom"].(float64) != 0 || got["dockFixed"] != "fixed" || got["stageAbove"].(float64) < 0 ||
		got["chip"] != true || got["how"] != false || got["top"] != true {
		t.Errorf("phone layout: %v", got)
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
	pickAnother(t, ctx, "#dtools .chipbtn:not(.set):not(.att)")
	noErrors(t, ctx)
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
// are brought down to the laptop's by the same ratio. The steps run twice and
// each step counts its better run, so one hiccup of a shared runner does not
// fail it.
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
			type task struct {
				Step string  `json:"step"`
				MS   float64 `json:"ms"`
			}
			// For each step, the longest task of each run.
			runs := js[[]map[string]float64](t, ctx, pageHelpers+`
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
	const runs = [];
	for (let n = 0; n < 2; n++) {
	  marks = [];
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
	  runs.push(run);
	}
	// A task known to be long, to show the observer works here.
	window.__long = [];
	await new Promise((r) => setTimeout(() => { const t = performance.now(); while (performance.now() - t < 120); r(); }));
	await wait(300);
	return window.__long.some(([, ms]) => ms >= 100) ? runs : null;`)
			if runs == nil {
				t.Fatal("the browser reports no long tasks, so the check cannot work")
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
