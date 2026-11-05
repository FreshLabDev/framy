// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// opened is how a page of the layout checks opens: in a language, and with
// the media features a person's system can ask for.
type opened struct {
	lang  string            // the language picked on the site, "" for the browser's
	phone bool              // a touch phone at twice the pixels
	media map[string]string // emulated media features, such as forced-colors
}

// browserAs opens the page in a fresh headless Chrome of the given size, as
// browser does, in the language and with the media features of o.
func browserAs(t *testing.T, url string, w, h int64, o opened) context.Context {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(int(w), int(h)), chromedp.NoSandbox)
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelC := chromedp.NewContext(actx)
	t.Cleanup(func() { cancelC(); cancelA() })
	vp := []chromedp.EmulateViewportOption{}
	if o.phone {
		vp = append(vp, chromedp.EmulateMobile, chromedp.EmulateTouch, chromedp.EmulateScale(2))
	}
	pre := watch
	if o.lang != "" {
		pre += fmt.Sprintf("try { localStorage.setItem(\"framy.lang\", %s); } catch (e) {}\n", jsString(o.lang))
	}
	media := []*emulation.MediaFeature{}
	for k, v := range o.media {
		media = append(media, &emulation.MediaFeature{Name: k, Value: v})
	}
	err := chromedp.Do(ctx,
		chromedp.EmulateViewport(w, h, vp...),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			if len(media) > 0 {
				if _, err := cdp.Call(ctx, tg, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Features: media}); err != nil {
					return err
				}
			}
			_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: pre})
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

// TestBrowserStepsRTL checks the slider of named steps in Arabic, where the
// range runs from the right: the step in bold stands over the thumb, and the
// track is filled from the side of the least.
func TestBrowserStepsRTL(t *testing.T) {
	ts := fakeAPI(t)
	for _, lang := range []string{"en", "ar"} {
		ctx := browserAs(t, ts.URL, 1440, 900, opened{lang: lang})
		got := js[map[string]any](t, ctx, pageHelpers+`
		document.querySelector("#how .mhead").click();
		await wait(700);
		const q = document.querySelector("#pksearch");
		q.value = "FLUX";
		q.dispatchEvent(new Event("input", { bubbles: true }));
		await wait(800);
		const row = [...document.querySelectorAll("#pkrows .frow")].find((r) => /FLUX/.test(r.textContent) && [...r.querySelectorAll(".pill")].some((b) => b.textContent.trim() === "3"));
		[...row.querySelectorAll(".pill")].find((b) => b.textContent.trim() === "3").click();
		await wait(900);
		const s = [...document.querySelectorAll("#how .steps")].find(vis);
		if (!s) return { none: true };
		const r = s.querySelector("input[type=range]");
		const out = [];
		for (const v of [1, 2, 3]) {
		  r.value = v; r.dispatchEvent(new Event("input", { bubbles: true }));
		  await wait(100);
		  const rr = r.getBoundingClientRect(), f = +r.value / +r.max, rtl = getComputedStyle(r).direction === "rtl";
		  // Where the browser draws the 20 px thumb of the value.
		  const thumb = rtl ? rr.right - 10 - (rr.width - 20) * f : rr.left + 10 + (rr.width - 20) * f;
		  const on = s.querySelector(".ticks button.on").getBoundingClientRect();
		  out.push(Math.round(Math.abs(on.left + on.width / 2 - thumb)));
		}
		const fill = getComputedStyle(r).getPropertyValue("--fill");
		return { dir: document.dir, off: out, fill: fill.includes("to left") ? "left" : fill.includes("to right") ? "right" : fill };`)
		if got["none"] == true {
			t.Fatalf("%s: FLUX 3 shows no slider of steps", lang)
		}
		for _, d := range got["off"].([]any) {
			if d.(float64) > 2 {
				t.Errorf("%s: the step in bold is %v px off the thumb: %v", lang, d, got)
			}
		}
		if want := map[string]string{"en": "right", "ar": "left"}[lang]; got["fill"] != want {
			t.Errorf("%s: the track fills to the %v, want %s", lang, got["fill"], want)
		}
		noErrors(t, ctx)
	}
}

// barWhole is how the bar of a phone stands: the line under the price is
// whole, inside the dock, and the price and the button do not cover each other.
const barWhole = `
const cap = document.querySelector("#pcap"), c = cap.getBoundingClientRect(), d = document.querySelector("#composer").getBoundingClientRect();
const n = document.querySelector("#pnum").getBoundingClientRect(), g = document.querySelector("#go").getBoundingClientRect();
return { text: cap.textContent, cut: cap.scrollWidth > cap.clientWidth + 1, inside: c.left >= d.left && c.right <= d.right && c.bottom <= d.bottom,
  apart: document.dir === "rtl" ? g.right <= n.left : n.right <= g.left, page: document.documentElement.scrollWidth - innerWidth };`

// TestBrowserPriceLine checks the line under the price on a narrow phone: how
// many free runs a sign-in gives, in a long language, and when the next free
// run comes, which is only the time when it is today.
func TestBrowserPriceLine(t *testing.T) {
	ts := fakeAPI(t)
	for _, lang := range []string{"de", "uk", "en"} {
		ctx := browserAs(t, ts.URL, 320, 568, opened{lang: lang, phone: true})
		got := js[map[string]any](t, ctx, pageHelpers+`await wait(400);`+barWhole)
		if got["text"] == "" || got["cut"] != false || got["inside"] != true || got["apart"] != true || got["page"].(float64) > 0 {
			t.Errorf("%s, signed out: %v", lang, got)
		}
		noErrors(t, ctx)
	}
	runs := &fakeRuns{}
	ts = fakeSite(t, runs.mount)
	ctx := browserAs(t, ts.URL, 320, 568, opened{phone: true})
	got := js[map[string]any](t, ctx, pageHelpers+`
	document.querySelector("#dtools .chipbtn.set").click();
	await wait(600);
	document.querySelector("#tune [data-seg=pay] > button[data-v=free]").click();
	await wait(600);
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	await wait(600);
	const at = Date.parse((await (await fetch("/api/me")).json()).free.images.frees_at);
	const today = new Date(at).toDateString() === new Date().toDateString();
	const want = new Intl.DateTimeFormat("en", today ? { hour: "numeric", minute: "2-digit" } : { day: "numeric", month: "long", hour: "numeric", minute: "2-digit" }).format(at);
	const r = (() => {`+barWhole+`})();
	return { ...r, want: "Next free run: " + want };`)
	if got["text"] != got["want"] || got["cut"] != false || got["inside"] != true || got["apart"] != true {
		t.Errorf("free runs used up: %v", got)
	}
	noErrors(t, ctx)
}

// TestBrowserAddTile checks the tile that adds files on a phone: its word
// stays inside it in every language, and the empty slots of a video stay
// squares of a thumbnail's size.
func TestBrowserAddTile(t *testing.T) {
	ts := fakeAPI(t)
	for _, lang := range []string{"de", "ru", "en"} {
		ctx := browserAs(t, ts.URL, 360, 780, opened{lang: lang, phone: true})
		got := js[map[string]any](t, ctx, pageHelpers+`
		const tile = () => {
		  const b = document.querySelector("#files .addfile:not(.slotadd)"), r = b.getBoundingClientRect(), rg = document.createRange();
		  rg.selectNodeContents(b);
		  const w = rg.getBoundingClientRect();
		  return { inside: w.left >= r.left && w.right <= r.right, w: Math.round(r.width), h: Math.round(r.height), onScreen: r.right <= innerWidth };
		};
		const image = tile();
		document.querySelectorAll("#kind > button")[1].click();
		await wait(700);
		const slots = [...document.querySelectorAll("#files .slotadd")].map((b) => { const r = b.getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; });
		return { image, slots };`)
		img := got["image"].(map[string]any)
		if img["inside"] != true || img["onScreen"] != true || img["w"].(float64) < 52 || img["h"] != float64(52) {
			t.Errorf("%s: the add tile: %v", lang, img)
		}
		slots := got["slots"].([]any)
		if len(slots) == 0 {
			t.Errorf("%s: a video shows no slot to add to", lang)
		}
		for _, s := range slots {
			if fmt.Sprint(s) != "[52 52]" {
				t.Errorf("%s: a slot's add tile is %v, want 52 by 52", lang, s)
			}
		}
		noErrors(t, ctx)
	}
}

// TestBrowserSignInButton checks the sign-in button in the header of a phone:
// its line is whole in every language from 320 px, its name is the whole
// line, and from 360 px the name framy stands beside the mark.
func TestBrowserSignInButton(t *testing.T) {
	ts := fakeAPI(t)
	for _, c := range []struct {
		lang string
		w    int64
	}{{"en", 360}, {"de", 360}, {"ca", 360}, {"ar", 360}, {"es", 320}, {"ca", 320}, {"ca", 430}, {"en", 1440}} {
		ctx := browserAs(t, ts.URL, c.w, 780, opened{lang: c.lang, phone: c.w < 960})
		got := js[map[string]any](t, ctx, pageHelpers+`
		const b = document.querySelector("#top .signin"), shown = [...b.querySelectorAll(":scope > span")].filter(vis);
		const name = document.querySelector("#top .logo span");
		return { shown: shown.map((s) => s.textContent), cut: shown.some((s) => s.scrollWidth > s.clientWidth), label: b.getAttribute("aria-label"),
		  framy: vis(name), right: Math.round(b.getBoundingClientRect().right), page: document.documentElement.scrollWidth - innerWidth };`)
		shown := got["shown"].([]any)
		if len(shown) != 1 || got["cut"] != false || got["label"] == "" || got["page"].(float64) > 0 || got["right"].(float64) > float64(c.w) {
			t.Errorf("%s at %d: %v", c.lang, c.w, got)
		}
		if want := c.w >= 360; got["framy"] != want {
			t.Errorf("%s at %d: the name beside the mark shown %v, want %v", c.lang, c.w, got["framy"], want)
		}
		// The short line is on a phone only; the whole one is the name everywhere.
		if (c.w >= 480) != (shown[0] == got["label"]) {
			t.Errorf("%s at %d: the button says %q, named %q", c.lang, c.w, shown[0], got["label"])
		}
		noErrors(t, ctx)
	}
}

// axNames gives the names Chrome's accessibility tree has for the nodes of a
// role, as a screen reader reads them.
func axNames(t *testing.T, ctx context.Context, role string) []string {
	t.Helper()
	var names []string
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		res, err := cdp.Call(ctx, tg, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
		if err != nil {
			return err
		}
		for _, n := range res.Nodes {
			var r, name string
			if n.Ignored || n.Role == nil || json.Unmarshal(n.Role.Value, &r) != nil || r != role {
				continue
			}
			if n.Name != nil {
				_ = json.Unmarshal(n.Name.Value, &name)
			}
			names = append(names, name)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestBrowserTabNames checks that the tabs of a phone, icons only, keep
// their words as their names, as they have them on a wide screen.
func TestBrowserTabNames(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	for _, w := range []int64{390, 1440} {
		ctx := browserAs(t, ts.URL, w, 844, opened{phone: w < 960})
		if got := fmt.Sprint(axNames(t, ctx, "link")); got != "[Create Library]" {
			t.Errorf("at %d px the tabs are named %s", w, got)
		}
		wide := js[bool](t, ctx, `return document.documentElement.scrollWidth <= innerWidth;`)
		if !wide {
			t.Errorf("at %d px the page scrolls sideways", w)
		}
		noErrors(t, ctx)
	}
}

// forcedHelpers reads colours under the system's contrast colours: sys gives
// a system colour as the page resolves it, and words lists the colours of the
// words inside an element.
const forcedHelpers = `
const sys = (c) => { const d = document.createElement("i"); d.style.cssText = "position:absolute;forced-color-adjust:none;color:" + c; document.body.append(d); const v = getComputedStyle(d).color; d.remove(); return v; };
const cs = (el) => getComputedStyle(el);
const words = (el) => [...el.querySelectorAll("*")].filter((e) => [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())).map((e) => cs(e).color);
`

// TestBrowserForcedColors checks the page under the system's contrast colours
// (Windows contrast themes), which paint every fill with the page colour: the
// option chosen in each choice has the selection colour with its words in the
// selection's text colour, a control has a frame, a maker's mark and an icon
// have the colour of the words, and a slider keeps its thumb and its fill.
func TestBrowserForcedColors(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browserAs(t, ts.URL, 1440, 900, opened{media: map[string]string{"forced-colors": "active"}})
	got := js[map[string]any](t, ctx, pageHelpers+forcedHelpers+`
	await wait(500);
	const hi = sys("Highlight"), hiText = sys("HighlightText"), canvas = sys("Canvas"), text = [sys("CanvasText"), sys("ButtonText")];
	const bad = [];
	if (!matchMedia("(forced-colors: active)").matches) bad.push("no forced colours");
	const inds = [...document.querySelectorAll("[data-seg] > .ind")];
	if (!inds.length || inds.some((e) => cs(e).display !== "none")) bad.push("the sliding block shows: " + inds.length);
	const groups = { kind: '[data-seg="kind"]', variant: '[data-seg="var"]', pay: '[data-seg="pay"]', ratio: ".ratios[data-seg]", tab: '[data-seg="view"]' };
	for (const [k, q] of Object.entries(groups)) {
	  const g = document.querySelector(q);
	  if (!g) { bad.push(k + ": no group"); continue; }
	  const on = g.querySelector(':scope > [aria-checked="true"], :scope > [aria-current="page"]'), off = g.querySelector(':scope > [aria-checked="false"]:not(:disabled), :scope > a:not([aria-current])');
	  if (cs(on).backgroundColor !== hi) bad.push(k + ": chosen on " + cs(on).backgroundColor);
	  if (off && cs(off).backgroundColor === hi) bad.push(k + ": not chosen on the selection colour");
	  const ws = [cs(on).color, ...words(on)].filter((c) => c !== hiText);
	  if (ws.length) bad.push(k + ": words of the chosen in " + ws.join(", "));
	  if (!off || parseFloat(cs(off).borderTopWidth) < 1 || cs(off).borderTopColor === canvas) bad.push(k + ": an option has no frame");
	}
	const mark = document.querySelector('[data-seg="pay"] > [aria-checked="true"] .lgi');
	if (!mark || cs(mark).backgroundColor !== hiText) bad.push("the mark of the chosen key: " + (mark && cs(mark).backgroundColor));
	const lg = document.querySelector("#how .mhead .lg > i");
	if (!lg || !text.includes(cs(lg).backgroundColor)) bad.push("the maker's mark: " + (lg && cs(lg).backgroundColor));
	const chev = document.querySelector("#how .mhead > .ic");
	if (!chev || !text.includes(cs(chev).color)) bad.push("the icon of the model: " + (chev && cs(chev).color));
	for (const sel of ["#prompt", "#go", "#how .mhead"]) {
	  const el = document.querySelector(sel);
	  if (!el || parseFloat(cs(el).borderTopWidth) < 1 || cs(el).borderTopColor === canvas) bad.push(sel + " has no frame");
	}
	// FLUX 3 has a slider of resolutions.
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "FLUX";
	q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	const cur = document.querySelector('#pkrows .frow[aria-current="true"] > .fhit');
	const row = [...document.querySelectorAll("#pkrows .frow")].find((r) => /FLUX/.test(r.textContent) && [...r.querySelectorAll(".pill")].some((b) => b.textContent.trim() === "3"));
	[...row.querySelectorAll(".pill")].find((b) => b.textContent.trim() === "3").click();
	await wait(900);
	const s = [...document.querySelectorAll("#how .steps")].find(vis);
	if (!s) return { bad: [...bad, "FLUX 3 shows no slider"] };
	const r = s.querySelector("input[type=range]");
	r.value = 1;
	r.dispatchEvent(new Event("input", { bubbles: true }));
	s.scrollIntoView({ block: "center" });
	await wait(300);
	if (cs(s.querySelector(".ticks button.on")).color !== hi) bad.push("the step chosen: " + cs(s.querySelector(".ticks button.on")).color);
	// Where the thumb, the filled track and the rest of it are drawn, to look at on the screen.
	const rr = r.getBoundingClientRect(), y = Math.round(rr.top + rr.height / 2);
	const pts = { thumb: [Math.round(rr.left + 10 + (rr.width - 20) * r.value / r.max), y], fill: [Math.round(rr.left + 5), y], rest: [Math.round(rr.right - 5), y] };
	return { bad, hi, canvas, pts, thumb: sys("ButtonText") };`)
	if bad := got["bad"].([]any); len(bad) > 0 {
		t.Errorf("forced colours (selection %v, page %v):\n%v", got["hi"], got["canvas"], bad)
	}
	buf, err := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	at := func(k string) [3]int {
		p := got["pts"].(map[string]any)[k].([]any)
		r, g, b, _ := img.At(int(p[0].(float64)), int(p[1].(float64))).RGBA()
		return [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
	}
	far := func(a, b [3]int) int { return abs(a[0]-b[0]) + abs(a[1]-b[1]) + abs(a[2]-b[2]) }
	canvas, thumb := rgbOf(got["canvas"].(string)), rgbOf(got["thumb"].(string))
	if d := far(at("thumb"), thumb); d > 60 {
		t.Errorf("the thumb is drawn in %v, want the button text %v", at("thumb"), thumb)
	}
	if far(at("fill"), canvas) < 90 || far(at("rest"), canvas) < 90 || far(at("fill"), at("rest")) < 90 {
		t.Errorf("the track is lost: filled %v, the rest %v, the page %v", at("fill"), at("rest"), canvas)
	}
	noErrors(t, ctx)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// rgbOf reads a colour as getComputedStyle gives it, "rgb(r, g, b)" or "rgba(r, g, b, a)".
func rgbOf(s string) [3]int {
	var c [3]int
	var a float64
	if _, err := fmt.Sscanf(s, "rgb(%d, %d, %d)", &c[0], &c[1], &c[2]); err != nil {
		fmt.Sscanf(s, "rgba(%d, %d, %d, %g)", &c[0], &c[1], &c[2], &a)
	}
	return c
}

// lowHelpers measures a low window: where a box stands against the window and
// against the page, which scrolls there.
const lowHelpers = `
const spot = (el) => { const b = el.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, abs: b.top + scrollY }; };
const flow = () => document.documentElement.classList.contains("flow");
const fixed = () => getComputedStyle(document.querySelector("#composer")).position === "fixed";
// In sight: inside the window, and not under a dock.
const inView = (el) => { const b = spot(el), c = document.querySelector("#composer");
  const floor = fixed() && !c.contains(el) ? c.getBoundingClientRect().top : innerHeight; return b.top >= -1 && b.bottom <= floor + 1; };
`

// TestBrowserLowWindow checks a window too low for the dock: a phone on its
// side, and a page zoomed to 400% (320 by 256). There the composer stands in
// the page over the stage and the page scrolls: all of the composer can be
// reached, a run brings the stage into view with the frame, the reason and
// what to do, and Edit the prompt brings the composer back. A phone held
// upright keeps the dock; with a mouse a page zoomed in takes the page
// whichever way the screen stands.
func TestBrowserLowWindow(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)

	ctx := browserAs(t, ts.URL, 844, 390, opened{phone: true})
	got := js[map[string]any](t, ctx, pageHelpers+runHelpers+lowHelpers+`
	await wait(400);
	const bad = [];
	if (!flow() || fixed()) bad.push("the composer is a dock");
	if (!inView(document.querySelector("#composer")) || !inView(document.querySelector("#go"))) bad.push("the composer is cut: " + JSON.stringify(spot(document.querySelector("#composer"))));
	const s = await press("fail:money", (s) => s.failed);
	await wait(900);
	if (s.label !== "No money on the key") bad.push("the run: " + s.label);
	for (const q of ["#stage [data-k=pv]", "#stage .jbox .c b", "#stage .jacts"]) if (!inView(document.querySelector(q))) bad.push(q + " out of sight: " + JSON.stringify(spot(document.querySelector(q))));
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Edit the prompt").click();
	await wait(900);
	if (document.activeElement !== document.querySelector("#prompt") || !inView(document.querySelector("#prompt"))) bad.push("Edit the prompt leaves the prompt out of sight");
	return { bad, page: document.documentElement.scrollWidth - innerWidth };`)
	if bad := got["bad"].([]any); len(bad) > 0 || got["page"].(float64) > 0 {
		t.Errorf("a phone on its side: %v, %v px wider than the window", bad, got["page"])
	}
	noErrors(t, ctx)

	// The keyboard of a field being typed in does not switch the layout: it
	// changes when the field lets go.
	got = js[map[string]any](t, ctx, lowHelpers+`document.querySelector("#prompt").focus(); return { flow: flow() };`)
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(390, 844, chromedp.EmulateMobile, chromedp.EmulateTouch, chromedp.EmulateScale(2), chromedp.EmulatePortrait)); err != nil {
		t.Fatal(err)
	}
	got = js[map[string]any](t, ctx, pageHelpers+lowHelpers+`
	await wait(400);
	const typing = flow();
	document.querySelector("#prompt").blur();
	await wait(700);
	return { typing, after: flow(), fixed: fixed() };`)
	if got["typing"] != true || got["after"] != false || got["fixed"] != true {
		t.Errorf("turned upright while typing: %v", got)
	}

	// 400% zoom: the composer does not run off the top of the page, and every
	// button of the run can be scrolled to.
	ctx = browserAs(t, ts.URL, 320, 256, opened{})
	got = js[map[string]any](t, ctx, pageHelpers+runHelpers+lowHelpers+`
	await wait(400);
	const bad = [];
	if (!flow() || fixed()) bad.push("the composer is a dock");
	if (spot(document.querySelector("#composer")).abs < 0) bad.push("the composer starts over the page");
	const s = await press("fail:money", (s) => s.failed);
	await wait(900);
	if (!inView(document.querySelector("#stage .jbox .c b"))) bad.push("the reason is out of sight");
	const end = document.documentElement.scrollHeight;
	for (const b of document.querySelectorAll("#stage .jacts > button")) if (spot(b).abs + b.offsetHeight > end) bad.push(b.textContent.trim() + " cannot be reached");
	return { bad, page: document.documentElement.scrollWidth - innerWidth };`)
	if bad := got["bad"].([]any); len(bad) > 0 || got["page"].(float64) > 0 {
		t.Errorf("400%% zoom: %v, %v px wider than the window", bad, got["page"])
	}
	noErrors(t, ctx)

	// A phone held upright keeps the dock, also in a window as low as its
	// keyboard leaves it; with a mouse, a page zoomed to 400% on a screen
	// that stands upright (270 by 480) has the composer in the page.
	for _, c := range []struct {
		name  string
		w, h  int64
		phone bool
		flow  bool
	}{{"a phone held upright", 390, 844, true, false}, {"a low window of a phone held upright", 390, 500, true, false}, {"400% zoom on an upright screen", 270, 480, false, true}} {
		ctx = browserAs(t, ts.URL, c.w, c.h, opened{phone: c.phone})
		got = js[map[string]any](t, ctx, lowHelpers+`return { flow: flow(), fixed: fixed(), sideways: screen.orientation.type };`)
		if got["flow"] != c.flow || got["fixed"] == c.flow {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// TestBrowserReducedMotion checks the style sheet against the rule of the
// site's motion: what moves (a transition of transform, or of all) is
// declared only under prefers-reduced-motion: no-preference, so a person who
// asked for less motion sees nothing slide, the switches' knobs included.
func TestBrowserReducedMotion(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browserAs(t, ts.URL, 390, 844, opened{phone: true, media: map[string]string{"prefers-reduced-motion": "reduce"}})
	got := js[[]string](t, ctx, `
	const bad = [], moves = /(^|,)\s*(all|transform|translate|scale|rotate)\s*(,|$)/;
	const walk = (rules, quiet) => {
	  for (const r of rules) {
	    if (r instanceof CSSImportRule) { if (r.styleSheet) walk(r.styleSheet.cssRules, quiet); continue; }
	    if (r instanceof CSSStyleRule && quiet && moves.test(r.style.transitionProperty || "")) bad.push(r.selectorText + " {transition: " + r.style.transition + "}");
	    if (r.cssRules) walk(r.cssRules, quiet && !(r instanceof CSSMediaRule && /prefers-reduced-motion:\s*no-preference/.test(r.media.mediaText)));
	  }
	};
	for (const s of document.styleSheets) walk(s.cssRules, true);
	return bad;`)
	if len(got) > 0 {
		t.Errorf("moves outside prefers-reduced-motion: no-preference:\n%s", strings.Join(got, "\n"))
	}
	noErrors(t, ctx)
}
