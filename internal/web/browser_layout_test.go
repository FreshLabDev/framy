// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
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
	err := chromedp.Do(ctx,
		chromedp.EmulateViewport(w, h, vp...),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
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
