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

// inLang stops the test unless the page speaks lang: the person's language
// from /api/me wins over the one picked on the site.
func inLang(t *testing.T, ctx context.Context, lang string) {
	t.Helper()
	if got := js[string](t, ctx, `return document.documentElement.lang;`); got != lang {
		t.Fatalf("the page is in %q, not %q", got, lang)
	}
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

// TestBrowserArabicSides checks that in Arabic the whole page reads from the
// right: the picker stands at the left, the end of the line, and comes in from
// there; its heading stands at the start; the chevron of the model points
// onward, to the left; a version's name and a price keep their own order
// ("2.6 Flash", "من $0.282"); and in the settings the mark of a dead key is at
// the start of its row and a switch that is on has its knob at the end.
func TestBrowserArabicSides(t *testing.T) {
	ts := fakeAPI(t)
	for _, c := range []struct {
		lang string
		w, h int64
	}{{"en", 1440, 900}, {"ar", 1440, 900}, {"ar", 390, 844}} {
		ctx := browserAs(t, ts.URL, c.w, c.h, opened{lang: c.lang, phone: c.w < 960})
		got := js[map[string]any](t, ctx, pageHelpers+`
		const head = [...document.querySelectorAll("#how .mhead, #dtools .chipbtn:not(.set)")].find(vis), chev = head.querySelector(".ic.chev");
		const flip = chev ? new DOMMatrix(getComputedStyle(chev).transform).a : 0;
		head.click();
		const pk = document.querySelector("#picker");
		await wait(30);
		const from = pk.getAnimations().map((a) => a.effect.getKeyframes()[0].transform).find((x) => x && x !== "none") || "";
		await wait(900);
		const r = pk.getBoundingClientRect(), h3 = pk.querySelector("h3").getBoundingClientRect(), done = pk.querySelector("header .x").getBoundingClientRect();
		// Where a letter of a text stands on the screen.
		const at = (node, i) => { const g = document.createRange(); g.setStart(node, i); g.setEnd(node, i + 1); return g.getBoundingClientRect().left; };
		const texts = (sel, re) => [...pk.querySelectorAll(sel)].flatMap((el) => { const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT); const out = []; while (w.nextNode()) if (re.test(w.currentNode.data)) out.push(w.currentNode); return out; });
		const names = texts(".pill", /^\d[\d.]* [A-Za-z]/).map((n) => ({ text: n.data, ok: at(n, 0) < at(n, n.data.indexOf(" ") + 1) }));
		const prices = texts(".usd", /\$\d/).map((n) => { const i = n.data.indexOf("$"); return { text: n.data, ok: at(n, i) < at(n, i + 1) }; });
		return { dir: document.dir, flip, from, left: Math.round(r.left), right: Math.round(innerWidth - r.right), headAtStart: (document.dir === "rtl" ? r.right - h3.right : h3.left - r.left) < 40 && Math.abs(h3.left - done.left) > 100,
		  names, prices, worded: prices.filter((p) => /[؀-ۿ]/.test(p.text)).length };`)
		rtl := c.lang == "ar"
		if want := map[bool]string{false: "ltr", true: "rtl"}[rtl]; got["dir"] != want {
			t.Fatalf("%s: the page is %v, want %s", c.lang, got["dir"], want)
		}
		if c.w >= 960 {
			// A phone's model button opens downwards, and its chevron points down in every language.
			if want := map[bool]float64{false: 1, true: -1}[rtl]; got["flip"] != want {
				t.Errorf("%s: the chevron of the model is scaled %v, want %v", c.lang, got["flip"], want)
			}
			side, other, from := "right", "left", "translateX(64px)"
			if rtl {
				side, other, from = "left", "right", "translateX(-64px)"
			}
			if got[side].(float64) != 0 || got[other].(float64) < 300 {
				t.Errorf("%s: the picker does not stand at the %s: %v", c.lang, side, got)
			}
			if got["from"] != from {
				t.Errorf("%s: the picker comes in from %v, want %s", c.lang, got["from"], from)
			}
		}
		if got["headAtStart"] != true {
			t.Errorf("%s %d: the heading of the picker is not at the start of its line: %v", c.lang, c.w, got)
		}
		for _, k := range []string{"names", "prices"} {
			list, _ := got[k].([]any)
			if len(list) == 0 {
				t.Errorf("%s %d: the picker shows no %s to check", c.lang, c.w, k)
			}
			for _, x := range list {
				if x.(map[string]any)["ok"] != true {
					t.Errorf("%s %d: %q does not keep its order", c.lang, c.w, x.(map[string]any)["text"])
				}
			}
		}
		if rtl && got["worded"].(float64) == 0 {
			t.Errorf("%s %d: no price stands after an Arabic word: %v", c.lang, c.w, got["prices"])
		}
		noErrors(t, ctx)

		f := &fakePrefs{}
		ps := fakeSite(t, func(srv *Server) {
			f.mount(srv)
			f.language = c.lang
			f.connect("openai", "dead", "invalid")
		})
		ctx = browserAs(t, ps.URL, c.w, c.h, opened{lang: c.lang, phone: c.w < 960})
		s := js[map[string]any](t, ctx, pageHelpers+`
		document.querySelector("#meBtn").click();
		for (let i = 0; i < 40 && !document.querySelector("#prefsBody .srow.bad"); i++) await wait(100);
		await wait(600);
		const bad = document.querySelector("#prefsBody .srow.bad"), tog = document.querySelector("#prefsBody .tog[aria-checked=true] .knob");
		const k = tog && getComputedStyle(tog, "::after");
		return { stripe: bad ? getComputedStyle(bad).boxShadow : "", knob: k ? new DOMMatrix(k.transform).e : 0 };`)
		stripe, knob := "3px 0px 0px 0px", 16.0
		if rtl {
			stripe, knob = "-3px 0px 0px 0px", -16
		}
		if !strings.Contains(fmt.Sprint(s["stripe"]), stripe) {
			t.Errorf("%s %d: the mark of a dead key is %v, want an inset %s", c.lang, c.w, s["stripe"], stripe)
		}
		if s["knob"] != knob {
			t.Errorf("%s %d: the knob of a switch that is on is moved %v px, want %v", c.lang, c.w, s["knob"], knob)
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
		inLang(t, ctx, lang)
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
	for (const q of ["#stage [data-k=pv]", "#stage .c b", "#stage .jacts"]) if (!inView(document.querySelector(q))) bad.push(q + " out of sight: " + JSON.stringify(spot(document.querySelector(q))));
	[...document.querySelectorAll("#stage .jacts button")].find((b) => b.textContent.trim() === "Edit the prompt").click();
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
	if (!inView(document.querySelector("#stage .c b"))) bad.push("the reason is out of sight");
	const end = document.documentElement.scrollHeight;
	for (const b of document.querySelectorAll("#stage .jacts button")) if (spot(b).abs + b.offsetHeight > end) bad.push(b.textContent.trim() + " cannot be reached");
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

// TestBrowserDropHidden checks that the words of the layer shown while files
// are dragged over the page stay out of the accessibility tree, where a
// screen reader read them after Create as a line of the form, and that the
// layer still shows them during a drag.
func TestBrowserDropHidden(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browserAs(t, ts.URL, 1440, 900, opened{})
	for _, role := range []string{"StaticText", "generic", "paragraph"} {
		for _, n := range axNames(t, ctx, role) {
			if strings.Contains(n, "Drop to add") {
				t.Errorf("a screen reader reads %q (%s)", n, role)
			}
		}
	}
	got := js[string](t, ctx, `
	document.querySelector("#composer").classList.add("drag");
	return getComputedStyle(document.querySelector("#composer"), "::after").content;`)
	if !strings.Contains(got, "Drop to add") {
		t.Errorf("the layer of a drag says %s", got)
	}
	noErrors(t, ctx)
}

// contrastHelpers lists the words on the screen that stand under WCAG AA
// against the first colour painted behind them: 4.5 : 1, or 3 : 1 for large
// text, with the opacity of what holds them. A disabled button is left out.
const contrastHelpers = `
function lowContrast() {
  const bad = [];
  const vis = (el) => el.getClientRects().length && getComputedStyle(el).visibility !== "hidden" && !el.closest("[hidden],[aria-hidden=true],.scrim");
  const P = (c) => c.match(/[\d.]+/g).map(Number);
  const Lum = (c) => { const v = c.map((x) => { x /= 255; return x <= .03928 ? x / 12.92 : ((x + .055) / 1.055) ** 2.4; }); return .2126 * v[0] + .7152 * v[1] + .0722 * v[2]; };
  for (const el of document.querySelectorAll("body *")) {
    if (!vis(el) || ![...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())) continue;
    const r = el.getBoundingClientRect(); if (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) continue;
    if (el.closest("button:disabled, .ind")) continue;
    const cs = getComputedStyle(el); let bg = null, op = 1;
    for (let a = el; a; a = a.parentElement) { const ac = getComputedStyle(a); op *= +ac.opacity; const b = ac.backgroundColor; if (b && !/rgba\(0, 0, 0, 0\)|transparent/.test(b)) { bg = P(b); break; } }
    if (!bg) continue;
    const f = P(cs.color), fa = (f[3] ?? 1) * op, mix = f.slice(0, 3).map((x, i) => x * fa + bg[i] * (1 - fa));
    const l1 = Lum(mix), l2 = Lum(bg.slice(0, 3)), cr = (Math.max(l1, l2) + .05) / (Math.min(l1, l2) + .05);
    const fs = parseFloat(cs.fontSize), fw = +cs.fontWeight || 400, large = fs >= 24 || (fs >= 18.66 && fw >= 700);
    if (cr < (large ? 3 : 4.5)) bad.push((el.className || el.tagName) + " '" + el.textContent.trim().slice(0, 24) + "' " + cr.toFixed(2) + " " + fs);
  }
  return bad;
}
`

// TestBrowserContrast checks the contrast of every line in sight in the
// composer, the picker (a dimmed model among them), the settings sheet of a
// phone, the empty frame and the library: the secondary lines, the prices of
// the variants, the labels of the settings and the ratio in the empty frame
// read on a phone in the sun too.
func TestBrowserContrast(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	check := func(name string, ctx context.Context, body string) {
		t.Helper()
		got := js[[]string](t, ctx, pageHelpers+contrastHelpers+body+`
		await wait(700);
		return lowContrast();`)
		if len(got) > 0 {
			t.Errorf("%s: too little contrast:\n%s", name, strings.Join(got, "\n"))
		}
	}
	ctx := browserAs(t, ts.URL, 1440, 900, opened{})
	check("composer", ctx, ``)
	check("picker", ctx, `
	document.querySelector("#how .mhead").click();
	await wait(900);
	if (!document.querySelector("#pkrows .frow.dim")) throw new Error("no dimmed model in the picker");`)
	noErrors(t, ctx)

	ctx = browserAs(t, ts.URL, 390, 844, opened{phone: true})
	check("phone", ctx, ``)
	check("phone settings", ctx, `document.querySelector("#dtools .chipbtn.set").click();`)
	noErrors(t, ctx)

	lib := newFakeLibrary()
	ctx = browserAs(t, fakeSite(t, lib.mount).URL+"/library", 1440, 900, opened{})
	check("library", ctx, `for (let i = 0; i < 40 && !document.querySelector("#library .li"); i++) await wait(100);`)
	noErrors(t, ctx)
}

// dropHelpers drop made-up photos on the composer, as a person drops files.
const dropHelpers = `
const photo = async (hue) => { const cv = document.createElement("canvas"); cv.width = 64; cv.height = 48; const g = cv.getContext("2d");
  g.fillStyle = "hsl(" + hue + ",60%,55%)"; g.fillRect(0, 0, 64, 48);
  return new File([await new Promise((r) => cv.toBlob(r, "image/png"))], "p" + hue + ".png", { type: "image/png" }); };
async function dropPhotos(n, on = "#composer") {
  const dt = new DataTransfer();
  for (let i = 0; i < n; i++) dt.items.add(await photo(i * 40));
  document.querySelector(on).dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
  await wait(1000);
}
`

// TestBrowserFilesHint checks the row of files on a phone: the line about
// the files (that they stay on the device, why one is not used) is never
// cut by the edge of the screen or by the row's scroll, beside the tiles
// while it fits and under them otherwise, and the tiles alone scroll.
func TestBrowserFilesHint(t *testing.T) {
	for _, c := range []struct {
		lang string
		w    int64
	}{{"en", 390}, {"de", 360}, {"ru", 360}, {"zh", 360}} {
		runs := &fakeRuns{lang: c.lang}
		ts := fakeSite(t, runs.mount)
		ctx := browserAs(t, ts.URL, c.w, 800, opened{lang: c.lang, phone: true})
		inLang(t, ctx, c.lang)
		got := js[[]string](t, ctx, pageHelpers+dropHelpers+`
		const bad = [], files = document.querySelector("#files"), right = document.querySelector("#prompt").getBoundingClientRect().right;
		for (const n of [1, 2, 3, 4, 3]) {
		  await dropPhotos(n - document.querySelectorAll("#files .thumb").length);
		  const hints = [...files.querySelectorAll(".fhint")].filter(vis);
		  if (!hints.length) bad.push(n + " files: no line about them");
		  for (const h of hints) {
		    const r = h.getBoundingClientRect();
		    if (r.right > right + 1 || h.scrollWidth > h.clientWidth + 1 || h.closest(".ftiles")) bad.push(n + " files: cut " + JSON.stringify([r.left, r.right, right]) + " " + h.textContent);
		  }
		}
		await dropPhotos(5);
		const tiles = files.querySelector(".ftiles") || files;
		if (tiles.scrollWidth <= tiles.clientWidth) bad.push("8 files do not scroll");
		if (document.documentElement.scrollWidth > innerWidth) bad.push("the page scrolls sideways");
		return bad;`)
		if len(got) > 0 {
			t.Errorf("%s at %d: %v", c.lang, c.w, got)
		}
		noErrors(t, ctx)
	}
}

// TestBrowserDockFocus walks the dock of a phone with Tab, three photos in
// it: focus goes in the order the dock reads (the kind, the model, the
// settings, the prompt, the files, Create) and the ring of every control is
// whole, the tiles in their scrolling row included.
func TestBrowserDockFocus(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browserAs(t, ts.URL, 390, 844, opened{phone: true})
	js[bool](t, ctx, pageHelpers+dropHelpers+`await dropPhotos(3); document.activeElement?.blur(); return true;`)
	var seen []map[string]any
	for i := 0; i < 40 && len(seen) < 12; i++ {
		if err := chromedp.Do(ctx, chromedp.KeyEvent("\t")); err != nil {
			t.Fatal(err)
		}
		got := js[map[string]any](t, ctx, `
		const e = document.activeElement;
		if (!e || !document.querySelector("#composer").contains(e)) return { out: true };
		const cs = getComputedStyle(e), ext = (parseFloat(cs.outlineOffset) || 0) + (parseFloat(cs.outlineWidth) || 0), r = e.getBoundingClientRect();
		let cut = "";
		for (let p = e.parentElement; p && p !== document.body; p = p.parentElement) {
		  if (!/(hidden|auto|scroll|clip)/.test(getComputedStyle(p).overflowX + getComputedStyle(p).overflowY)) continue;
		  const q = p.getBoundingClientRect();
		  if (Math.max(q.left - (r.left - ext), q.top - (r.top - ext), r.right + ext - q.right, r.bottom + ext - q.bottom) > 0.5) { cut = p.className; break; }
		}
		return { name: e.getAttribute("aria-label") || e.id || e.textContent.trim() || e.className, x: Math.round(r.left), y: Math.round(r.top), h: Math.round(r.height), cut };`)
		if got["out"] == true {
			if len(seen) > 0 {
				break
			}
			continue
		}
		seen = append(seen, got)
	}
	if len(seen) < 6 {
		t.Fatalf("Tab reached %d controls of the dock: %v", len(seen), seen)
	}
	for i, s := range seen {
		if s["cut"] != "" {
			t.Errorf("the ring of %v is cut by %v", s["name"], s["cut"])
		}
		if i == 0 {
			continue
		}
		p := seen[i-1]
		// The next control stands lower, or on the same line further on.
		sameLine := s["y"].(float64) < p["y"].(float64)+p["h"].(float64)/2 && s["y"].(float64) > p["y"].(float64)-p["h"].(float64)/2
		if s["y"].(float64) < p["y"].(float64)-1 && !sameLine || sameLine && s["x"].(float64) <= p["x"].(float64) {
			t.Errorf("Tab goes from %v (%v, %v) back to %v (%v, %v)", p["name"], p["x"], p["y"], s["name"], s["x"], s["y"])
		}
	}
	noErrors(t, ctx)
}

// touchHelpers measure what a finger can press on a touch screen. reach is
// the box a control answers to, its own or its ::after's when that reaches
// further; small lists the controls in sight that answer to less than 44 by
// 44 px; stolen lists the controls whose own drawing answers for another.
const touchHelpers = `
const CTL = "button, a[href], input:not([type=hidden]):not([type=file]), select, textarea, [role=button]";
const live = (el) => el.getClientRects().length && getComputedStyle(el).visibility !== "hidden" && +getComputedStyle(el).opacity > 0 &&
  getComputedStyle(el).pointerEvents !== "none" && !el.disabled && !el.closest("[hidden], [aria-hidden=true], .ind");
const label = (el) => (el.getAttribute("aria-label") || el.textContent || el.placeholder || "").trim().slice(0, 24) + " (" + el.className + ")";
const layer = (el) => el.closest(".sheet:not([hidden]), .full, .ask") || document.body;
const top = () => { const s = [...document.querySelectorAll(".sheet:not([hidden]), .full:not([hidden]), .ask")].filter(live).pop(); return s || document.body; };
function reach(el) {
  const r = el.getBoundingClientRect(), a = getComputedStyle(el, "::after");
  let w = r.width, h = r.height;
  if (a.content !== "none" && a.position === "absolute") { w = Math.max(w, r.width - parseFloat(a.left) - parseFloat(a.right)); h = Math.max(h, r.height - parseFloat(a.top) - parseFloat(a.bottom)); }
  return [Math.round(w), Math.round(h)];
}
function touch() {
  const small = [], stolen = [], up = top();
  for (const el of document.querySelectorAll(CTL)) {
    if (!live(el) || layer(el) !== up) continue;
    const r = el.getBoundingClientRect();
    if (r.top < 0 || r.bottom > innerHeight || r.left < 0 || r.right > innerWidth) continue;
    const [w, h] = reach(el), cross = el.matches(".thumb .rm");
    // A cross on a tile reaches 28 by 33 px: farther in, a press on the photo would remove it.
    if (cross ? w < 28 || h < 33 : w < 44 || h < 44) small.push(label(el) + " " + w + "x" + h);
    for (const [x, y] of [[r.left + 2, r.top + 2], [r.right - 2, r.top + 2], [r.left + 2, r.bottom - 2], [r.right - 2, r.bottom - 2]]) {
      const c = document.elementFromPoint(x, y)?.closest(CTL);
      // The clear button inside the search field stands over the field's end.
      if (c && c !== el && !el.contains(c) && !c.contains(el) && !(el.matches(".lsearch input") && c.matches(".lsearch .clr"))) { stolen.push(label(el) + " <- " + label(c)); break; }
    }
  }
  return { small, stolen };
}
`

// TestBrowserTouchTargets checks a phone's controls against a finger: every
// control in sight answers to at least 44 by 44 px, drawn that big or
// reaching that far around its drawing, and no reach takes a press from the
// control beside it. It goes through the composer with photos, the picker,
// the settings sheet, a result, a failed run, a strip of frames and their
// results, the library with a search, the viewer and the settings.
func TestBrowserTouchTargets(t *testing.T) {
	report := func(name string, got map[string]any) {
		t.Helper()
		if s := got["small"].([]any); len(s) > 0 {
			t.Errorf("%s: under a finger's size: %v", name, s)
		}
		if s := got["stolen"].([]any); len(s) > 0 {
			t.Errorf("%s: a press goes to the neighbour: %v", name, s)
		}
	}
	for _, c := range []struct {
		lang string
		w    int64
	}{{"en", 390}, {"de", 360}} {
		runs := &fakeRuns{lang: c.lang}
		ts := fakeSite(t, runs.mount)
		ctx := browserAs(t, ts.URL, c.w, 844, opened{lang: c.lang, phone: true})
		inLang(t, ctx, c.lang)
		at := func(name, body string) {
			t.Helper()
			report(fmt.Sprintf("%s at %d, %s", c.lang, c.w, name), js[map[string]any](t, ctx, pageHelpers+runHelpers+dropHelpers+touchHelpers+body+`
			await wait(700);
			return touch();`))
		}
		at("composer", `await dropPhotos(2);`)
		at("picker", `document.querySelector("#dtools .chipbtn:not(.set)").click();`)
		at("settings sheet", `document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); await wait(600); document.querySelector("#dtools .chipbtn.set").click();`)
		at("result", `document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); await wait(600);
		await press("done", (s) => s.done); await wait(900); document.querySelector("#prompt").value = "";`)
		at("failed", `await press("fail:money", (s) => s.failed); document.querySelector("#prompt").value = "";`)
		noErrors(t, ctx)

		// Frames in a strip, then their results.
		ctx = browserAs(t, ts.URL, c.w, 844, opened{lang: c.lang, phone: true})
		at("board", `for (let i = 0; i < 2; i++) { document.querySelector("#stage .addf").click(); await wait(700); }
		if (document.querySelectorAll("#stage .strip > .cell").length !== 3) throw new Error("no frames to press");`)
		at("frames", `const s = await press("frames:done", () => document.querySelectorAll("#stage .jbox.cell.done img").length === 3);
		if (s.timeout) throw new Error("no frames");
		await wait(1500); document.querySelector("#prompt").value = "";`)
		noErrors(t, ctx)
	}

	lib := newFakeLibrary()
	ls := fakeSite(t, lib.mount)
	ctx := browserAs(t, ls.URL+"/library", 390, 844, opened{phone: true})
	report("library", js[map[string]any](t, ctx, pageHelpers+touchHelpers+`
	for (let i = 0; i < 40 && !document.querySelector("#library .li"); i++) await wait(100);
	const q = document.querySelector(".lsearch input"); q.value = "a"; q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	return touch();`))
	noErrors(t, ctx)
	ctx = browserAs(t, ls.URL+"/r/60", 390, 844, opened{phone: true})
	report("viewer", js[map[string]any](t, ctx, pageHelpers+touchHelpers+`
	for (let i = 0; i < 40 && !document.querySelector("#viewer:not([hidden]) .vrun"); i++) await wait(100);
	await wait(1200);
	return touch();`))
	noErrors(t, ctx)

	prefs := &fakePrefs{}
	ps := fakeSite(t, func(srv *Server) {
		prefs.mount(srv)
		prefs.connect("openrouter", "or42", "valid")
		prefs.connect("openai", "dead", "invalid")
	})
	ctx = browserAs(t, ps.URL, 390, 844, opened{phone: true})
	report("settings", js[map[string]any](t, ctx, pageHelpers+touchHelpers+`
	document.querySelector("#meBtn").click();
	await wait(1200);
	return touch();`))
	noErrors(t, ctx)
}

// TestBrowserPhoneNames checks the names a phone shows in its narrowest
// rows. The chip of the dock names the model whole, every model of the
// catalog, also beside the count of the other frames' models, while the
// settings beside it stay a square with the count of frames in its corner
// and in its name. The viewer's title is the kind's word while it fits whole
// beside the arrows and Close, and its icon when not, never a cut word.
func TestBrowserPhoneNames(t *testing.T) {
	for _, lang := range []string{"en", "de"} {
		runs := &fakeRuns{lang: lang}
		ts := fakeSite(t, runs.mount)
		ctx := browserAs(t, ts.URL, 360, 780, opened{lang: lang, phone: true})
		inLang(t, ctx, lang)
		got := js[map[string]any](t, ctx, pageHelpers+`
		for (let i = 0; i < 3; i++) { document.querySelector("#stage .addf").click(); await wait(600); }
		const set = document.querySelector("#dtools .chipbtn.set"), cnt = set.querySelector(".cnt");
		// Every model's name as the chip writes it: the family's and the variant's where the family has several.
		const cat = await (await fetch("/api/catalog")).json(), fams = {}, names = [];
		for (const m of cat.models) { if (m.fast_of) continue; const f = m.family || {}; (fams[f.id || m.id] ||= { name: f.name || m.name, n: 0 }).n++; }
		for (const m of cat.models) { if (m.fast_of) continue; const f = m.family || {}, fam = fams[f.id || m.id]; names.push(fam.n > 1 ? fam.name + " " + (f.label || m.name) : m.name); }
		const chip = document.querySelector("#dtools .chipbtn:not(.set)"), sp = chip.querySelector(".nm > span"), cut = [];
		const check = () => { for (const n of names) { sp.textContent = n; if (sp.scrollWidth > sp.clientWidth + 0.5 || sp.scrollHeight > sp.clientHeight + 0.5) cut.push(n); } };
		check();
		const more = document.createElement("small"); more.className = "more"; more.textContent = "+3"; chip.querySelector(".nm").after(more);
		check();
		const b = set.getBoundingClientRect(), c = cnt.getBoundingClientRect();
		return { n: names.length, cut, set: Math.round(b.width), label: set.getAttribute("aria-label"),
		  corner: c.top >= b.top && c.right <= b.right + 0.5 && c.bottom <= b.top + 22 && c.left >= b.right - 30 };`)
		if got["n"].(float64) < 20 || len(got["cut"].([]any)) > 0 || got["set"] != float64(46) || got["corner"] != true ||
			!strings.HasSuffix(got["label"].(string), ": 4") {
			t.Errorf("%s: the dock's chip: %v", lang, got)
		}
		noErrors(t, ctx)
	}

	for _, c := range []struct {
		lang string
		w    int64
		word bool
	}{{"en", 360, true}, {"ru", 360, false}, {"ru", 390, true}, {"uk", 390, false}, {"ar", 360, true}} {
		lib := newFakeLibrary()
		lib.lang = c.lang
		ls := fakeSite(t, lib.mount)
		ctx := browserAs(t, ls.URL+"/r/60", c.w, 780, opened{lang: c.lang, phone: true})
		inLang(t, ctx, c.lang)
		got := js[map[string]any](t, ctx, pageHelpers+`
		for (let i = 0; i < 40 && !document.querySelector("#viewer:not([hidden]) .vrun"); i++) await wait(100);
		await wait(800);
		const h = document.querySelector("#viewTitle"), w = h.querySelector(".t"), ic = h.querySelector(".ic");
		const hr = h.getBoundingClientRect(), wr = w.getBoundingClientRect(), at = (r) => document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
		const word = wr.top < hr.bottom, whole = wr.left >= hr.left - 0.5 && wr.right <= hr.right + 0.5;
		return { word, whole, icon: at(ic.getBoundingClientRect())?.closest("svg") === ic, name: document.querySelector("#viewer").getAttribute("aria-labelledby") === h.id && h.textContent.trim() !== "" };`)
		if got["word"] != c.word || (c.word && got["whole"] != true) || got["icon"] == c.word || got["name"] != true {
			t.Errorf("%s at %d: the viewer's title: %v", c.lang, c.w, got)
		}
		noErrors(t, ctx)
	}
}

// rowHelpers read a row of actions as a person sees it: lines() groups its
// buttons by the line they stand on, and rowFaults says what is wrong with
// it: a line that does not reach both ends of the row, a group of squares
// split over two lines, a square alone on a line, a button whose words are
// cut.
const rowHelpers = `
function rowFaults() {
  const row = [...document.querySelectorAll("#viewBody .jacts, #stage .jacts")].find((e) => e.offsetParent);
  if (!row) return ["no row of actions"];
  const r = row.getBoundingClientRect(), bad = [], lines = [];
  const items = [...row.querySelectorAll(":scope > :is(button, a), :scope > .sqs > *")];
  for (const b of items) {
    const br = b.getBoundingClientRect();
    let l = lines.find((x) => Math.abs(x.top - br.top) < 4);
    if (!l) lines.push((l = { top: br.top, items: [] }));
    l.items.push(b);
    const t = b.querySelector(".t");
    if (t && t.offsetParent && t.scrollWidth > t.clientWidth + 1) bad.push("cut: " + t.textContent);
  }
  const name = (b) => b.getAttribute("aria-label") || b.textContent.trim();
  for (const l of lines) {
    const left = Math.min(...l.items.map((b) => b.getBoundingClientRect().left)), right = Math.max(...l.items.map((b) => b.getBoundingClientRect().right));
    if (left > r.left + 1 || right < r.right - 1) bad.push("not flush: " + l.items.map(name).join(", "));
    if (l.items.length === 1 && l.items[0].matches(".sqs:not(.pair) > *") && lines.length > 1) bad.push("alone: " + name(l.items[0]));
  }
  for (const g of row.querySelectorAll(":scope > .sqs")) {
    const tops = new Set([...g.children].map((b) => Math.round(b.getBoundingClientRect().top)));
    if (tops.size > 1) bad.push("split: " + [...g.children].map(name).join(", "));
  }
  return bad;
}
`

// TestBrowserActionRows checks the rows of actions on a phone, under a
// result, a failed run and a refused one on the stage and in the viewer:
// every line reaches both ends of the row, the squares stand together, on
// the line of the buttons with words while they fit and on their own line
// when not, a square is never alone on a line, and no words are cut.
func TestBrowserActionRows(t *testing.T) {
	for _, c := range []struct {
		lang string
		w    int64
	}{{"de", 360}, {"ar", 360}, {"en", 390}} {
		runs := &fakeRuns{lang: c.lang}
		ts := fakeSite(t, runs.mount)
		ctx := browserAs(t, ts.URL, c.w, 844, opened{lang: c.lang, phone: true})
		inLang(t, ctx, c.lang)
		for _, p := range []string{"done", "done:joined", "fail:money", "refuse:free_limit", "fail:key"} {
			bad := js[[]string](t, ctx, pageHelpers+runHelpers+rowHelpers+`
			const s = await press(`+jsString(p)+`, () => document.querySelector("#stage .jbox.done, #stage .jbox.failed"));
			if (s.timeout) return ["no end"];
			await wait(900);
			return rowFaults();`)
			if len(bad) > 0 {
				t.Errorf("%s at %d, the stage after %s: %v", c.lang, c.w, p, bad)
			}
		}
		noErrors(t, ctx)

		lib := newFakeLibrary()
		lib.lang = c.lang
		ls := fakeSite(t, lib.mount)
		for _, id := range []string{"60", "62", "68"} {
			ctx := browserAs(t, ls.URL+"/r/"+id, c.w, 844, opened{lang: c.lang, phone: true})
			bad := js[[]string](t, ctx, pageHelpers+rowHelpers+`
			for (let i = 0; i < 40 && !document.querySelector("#viewer:not([hidden]) .vrun"); i++) await wait(100);
			await wait(1000);
			return rowFaults();`)
			if len(bad) > 0 {
				t.Errorf("%s at %d, the viewer of %s: %v", c.lang, c.w, id, bad)
			}
			noErrors(t, ctx)
		}
	}
}
