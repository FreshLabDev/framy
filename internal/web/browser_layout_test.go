// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"fmt"
	"os"
	"testing"

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
