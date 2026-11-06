// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// TestBrowserSliderSaysTheValue checks a setting of many values on a slider,
// a video's length: the slider goes by a value's place in the list, and a
// screen reader is told the value, "6 s", not the place, "4".
func TestBrowserSliderSaysTheValue(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+pickerHelpers+`
	await toVideo();
	await chooseFam("Kling Video");
	const row = () => document.querySelector("#how [data-k='h-f-duration']");
	const r = row().querySelector("input[type=range]"), said = () => [r.getAttribute("aria-valuetext"), row().querySelector(".rline b").textContent, r.value];
	const before = said();
	r.value = String(+r.value + 1);
	r.dispatchEvent(new Event("input", { bubbles: true }));
	return { before, after: said() };`)
	for _, k := range []string{"before", "after"} {
		s, _ := got[k].([]any)
		if len(s) != 3 || s[0] == nil || s[0] != s[1] || s[0] == s[2] {
			t.Errorf("%s: the slider says %v, the value shown %v at place %v", k, s[0], s[1], s[2])
		}
	}
	if got["before"].([]any)[0] == got["after"].([]any)[0] {
		t.Errorf("the slider says %v after a move as before it", got["after"])
	}
	noErrors(t, ctx)
}

// focused tells what has the keyboard's focus: its id, the group of choices
// it is an option of and whether it is the chosen one.
func focused(t *testing.T, ctx context.Context) map[string]any {
	t.Helper()
	return js[map[string]any](t, ctx, `
	const a = document.activeElement, g = a?.getAttribute("role") === "radio" ? a.closest("[data-seg]") : null;
	return { id: a?.id || "", tag: a?.tagName || "", seg: g?.dataset.seg || "", v: a?.dataset?.v || "", on: a?.getAttribute("aria-checked") === "true",
	  live: !!a && a.isConnected && !a.closest(".mirror, [inert]"), k: a?.closest("[data-k]")?.dataset.k || "", value: a?.value ?? "" };`)
}

// TestBrowserChoiceIsOneTabStop checks a group of choices is one stop for
// Tab, as a radio group is, so the keyboard gets from the prompt to Create
// without a stop at every option.
func TestBrowserChoiceIsOneTabStop(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, pageHelpers+pickerHelpers+`await wait(600); await chooseFam("FLUX"); document.querySelector("#prompt").focus(); return true;`)
	var stops []map[string]any
	for i := 0; i < 60; i++ {
		press(t, ctx, "Tab", "Tab", 9)
		f := focused(t, ctx)
		if f["id"] == "go" || f["tag"] == "BODY" {
			break
		}
		stops = append(stops, f)
	}
	seen := map[string]int{}
	for _, f := range stops {
		if s, _ := f["seg"].(string); s != "" {
			seen[s]++
			if !f["on"].(bool) {
				t.Errorf("Tab stops at %s %v, an option not chosen", s, f["v"])
			}
		}
	}
	for s, n := range seen {
		if n > 1 {
			t.Errorf("Tab stops %d times in the choice %s", n, s)
		}
	}
	if seen["var"] != 1 || seen["set-aspect_ratio"] != 1 {
		t.Errorf("Tab passed the choices %v on its way to Create, want the variant and the ratio once each", seen)
	}
	noErrors(t, ctx)
}

// TestBrowserArrowsChoose checks the arrows choose in a group of choices:
// the next option is chosen and keeps the focus, though the form is drawn
// again; Home and End go to the ends; in Arabic the left arrow is the next.
func TestBrowserArrowsChoose(t *testing.T) {
	ts := fakeAPI(t)
	for _, tc := range []struct {
		lang, next, nextCode string
		vk                   int64
	}{
		{"en", "ArrowRight", "ArrowRight", 39},
		{"ar", "ArrowLeft", "ArrowLeft", 37},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			ctx := browserInit(t, ts.URL, 1440, 900, false, catalogInit(tc.lang, `(c) => c`))
			js[bool](t, ctx, pageHelpers+`await wait(600); document.querySelector("#kind [aria-checked=true]").focus(); return true;`)
			press(t, ctx, tc.next, tc.nextCode, tc.vk)
			js[bool](t, ctx, pageHelpers+`await wait(700); return true;`)
			if f := focused(t, ctx); f["seg"] != "kind" || f["v"] != "video" || !f["on"].(bool) || !f["live"].(bool) {
				t.Fatalf("after the next arrow on Image the focus is on %v", f)
			}
			// A row of tiles: the ratio of a picture.
			ok := js[bool](t, ctx, pageHelpers+pickerHelpers+`
			document.querySelector("#kind [data-v=image]").click();
			await wait(800);
			await chooseFam("FLUX");
			const g = [...document.querySelectorAll("#how .ratios[data-seg]")].find(vis);
			if (!g) return false;
			g.querySelector(":scope > [aria-checked=true]").focus();
			return true;`)
			if !ok {
				t.Fatal("no row of ratios in the form")
			}
			before := js[[]any](t, ctx, `return [...document.querySelector("#how .ratios[data-seg]").querySelectorAll(":scope > [role=radio]")].map((r) => r.dataset.v);`)
			start := focused(t, ctx)
			press(t, ctx, tc.next, tc.nextCode, tc.vk)
			js[bool](t, ctx, pageHelpers+`await wait(700); return true;`)
			f := focused(t, ctx)
			at := -1
			for i, v := range before {
				if v == start["v"] {
					at = i
				}
			}
			if at < 0 || f["seg"] != start["seg"] || f["v"] != before[(at+1)%len(before)] || !f["on"].(bool) || !f["live"].(bool) {
				t.Errorf("after the next arrow on %v the focus is on %v, want %v chosen", start["v"], f, before[(at+1)%len(before)])
			}
			press(t, ctx, "End", "End", 35)
			js[bool](t, ctx, pageHelpers+`await wait(700); return true;`)
			if f := focused(t, ctx); f["v"] != before[len(before)-1] || !f["on"].(bool) {
				t.Errorf("after End the focus is on %v, want the last ratio %v chosen", f, before[len(before)-1])
			}
			press(t, ctx, "Home", "Home", 36)
			js[bool](t, ctx, pageHelpers+`await wait(700); return true;`)
			if f := focused(t, ctx); f["v"] != before[0] || !f["on"].(bool) {
				t.Errorf("after Home the focus is on %v, want the first ratio %v chosen", f, before[0])
			}
			noErrors(t, ctx)
		})
	}
}

// TestBrowserSliderKeepsFocus checks a slider moved by the keyboard keeps
// the focus: each step sets the video's length, and the form drawn again
// after it gave the focus back to the top of the page.
func TestBrowserSliderKeepsFocus(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, pageHelpers+pickerHelpers+`
	await toVideo();
	await chooseFam("Kling Video");
	document.querySelector("#how [data-k='h-f-duration'] input[type=range]").focus();
	return true;`)
	last := focused(t, ctx)["value"]
	for i := 0; i < 2; i++ {
		press(t, ctx, "ArrowRight", "ArrowRight", 39)
		js[bool](t, ctx, pageHelpers+`await wait(700); return true;`)
		f := focused(t, ctx)
		if f["tag"] != "INPUT" || f["k"] != "h-f-duration" || !f["live"].(bool) {
			t.Fatalf("after step %d the focus is on %v, not the slider", i+1, f)
		}
		if f["value"] == last {
			t.Errorf("step %d left the slider at %v", i+1, last)
		}
		last = f["value"]
	}
	noErrors(t, ctx)
}

// TestBrowserMoreRatiosKeepsFocus checks the button that shows every ratio
// keeps the focus when the form is drawn again with them: it is the button
// that shows fewer then, in the same place.
func TestBrowserMoreRatiosKeepsFocus(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, pageHelpers+pickerHelpers+`
	await chooseFam("FLUX");
	document.querySelector("#how .ratios .rt.more").focus();
	return true;`)
	enter(t, ctx)
	got := js[map[string]any](t, ctx, pageHelpers+`
	await wait(700);
	const a = document.activeElement;
	return { more: a.matches("#how .ratios .rt.more"), text: a.textContent, all: document.querySelectorAll("#how .ratios[data-seg] > [role=radio]").length };`)
	if text, _ := got["text"].(string); !got["more"].(bool) || !strings.HasPrefix(text, "−") || got["all"].(float64) <= 8 {
		t.Errorf("after Enter on the button for more ratios the focus is on %v", got)
	}
	noErrors(t, ctx)
}

// TestBrowserArrowsSaveTheLastCurrency checks the arrows through the
// currencies, a choice at every press, end saved on the one the person
// stopped at. The saves go one at a time and the newest last: sent together
// they could be kept out of order, the account on one currency and the page
// on another.
func TestBrowserArrowsSaveTheLastCurrency(t *testing.T) {
	f := &fakePrefs{patchLate: 300 * time.Millisecond}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && sheet().querySelector('[data-seg="cur"] [aria-checked="true"]'));
	await wait(500);
	sheet().querySelector('[data-seg="cur"] [aria-checked="true"]').focus();
	return {};`)
	for i := 0; i < 3; i++ {
		press(t, ctx, "ArrowRight", "ArrowRight", 39)
	}
	s := prefs(t, ctx, `
	await wait(1800);
	const a = document.activeElement;
	return { on: sheet().querySelector('[data-seg="cur"] [aria-checked="true"]')?.dataset.v || "", focus: a?.dataset?.v || a?.tagName || "" };`)
	f.with(func() {
		last := ""
		if len(f.patches) > 0 {
			last, _ = f.patches[len(f.patches)-1]["currency"].(string)
		}
		if f.mostPatching != 1 || f.currency != "PLN" || last != "PLN" {
			t.Errorf("three presses from USD: %d saves at once at most, the account on %s, the last save %q; want one at a time and PLN", f.mostPatching, f.currency, last)
		}
	})
	if s["on"] != "PLN" || s["focus"] != "PLN" {
		t.Errorf("after three presses from USD the page shows %v, want PLN chosen and in focus", s)
	}
	noErrors(t, ctx)
}

// TestBrowserClipEdgesKeepTheirArrows checks the clip editor, where ← and →
// move the chosen edge by a frame wherever the focus is, as its line says.
// On the edges themselves the arrows once chose the other edge as well and
// moved it, so a press changed the part from the wrong end. Each edge is a
// stop for Tab instead.
func TestBrowserClipEdgesKeepTheirArrows(t *testing.T) {
	// Signed in with a key: a free run takes no clip.
	f := &fakePrefs{}
	ts := fakeSite(t, func(srv *Server) {
		f.mount(srv)
		f.connect("openrouter", "or42", "valid")
		srv.HandleFunc("GET /test/clip.webm", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "video/webm")
			http.ServeFile(w, r, "testdata/clip.webm")
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+pickerHelpers+filesHelpers+`
	await toVideo();
	const b = await (await fetch("/test/clip.webm")).blob();
	drop([new File([b], "clip.webm", { type: "video/webm" })]);
	let pick = null;
	for (let i = 0; i < 50 && !pick; i++) { await wait(100); pick = [...document.querySelectorAll("#files .fhint.cut button")].find(vis); }
	if (!pick) return { error: "no button to pick a part: " + JSON.stringify({ thumbs: thumbs(), files: document.querySelector("#files")?.textContent, model: document.querySelector("#how .mname")?.textContent, kind: document.querySelector("#kind [aria-checked=true]")?.dataset.v }) };
	pick.click();
	await wait(900);
	const edges = [...document.querySelectorAll("#cutter .edges:not(.mirror) > [role=radio]")];
	edges.find((r) => r.dataset.v === "start").focus();
	return { tabs: edges.map((r) => r.tabIndex), at: edges.map((r) => r.querySelector("small").textContent) };`)
	if e, _ := got["error"].(string); e != "" {
		t.Fatal(e)
	}
	press(t, ctx, "ArrowRight", "ArrowRight", 39)
	after := js[map[string]any](t, ctx, pageHelpers+`
	await wait(500);
	const edges = [...document.querySelectorAll("#cutter .edges:not(.mirror) > [role=radio]")], a = document.activeElement;
	return { on: edges.find((r) => r.getAttribute("aria-checked") === "true")?.dataset.v || "", focus: a?.dataset?.v || a?.tagName || "", at: edges.map((r) => r.querySelector("small").textContent) };`)
	if tabs, _ := got["tabs"].([]any); len(tabs) != 2 || tabs[0] != 0.0 || tabs[1] != 0.0 {
		t.Errorf("the edges are stops for Tab %v, want both", got["tabs"])
	}
	if after["on"] != "start" || after["focus"] != "start" {
		t.Errorf("after → on the start the chosen edge is %q and the focus on %q, want the start", after["on"], after["focus"])
	}
	// A frame of 30 a second, the rate when the browser does not say one.
	if fmt.Sprint(got["at"]) != "[0.00 s 3.00 s]" || fmt.Sprint(after["at"]) != "[0.03 s 3.00 s]" {
		t.Errorf("the edges were at %v and are at %v after →, want the start a frame later and the end where it was", got["at"], after["at"])
	}
	noErrors(t, ctx)
}

// enter presses Enter, which a button takes as a click: unlike the keys press
// sends, it types a character.
func enter(t *testing.T, ctx context.Context) {
	t.Helper()
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		if _, err := cdp.Call(ctx, tg, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13, Text: "\r"}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, tg, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 13})
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
}

// keys focuses the element sel names and presses the keys on it, as a
// keyboard does, a moment apart; a sel of "" presses them where the focus is.
func keys(t *testing.T, ctx context.Context, sel string, ks ...string) {
	t.Helper()
	if sel != "" && !js[bool](t, ctx, `const el = document.querySelector(`+jsString(sel)+`); if (!el) return false; el.focus(); return document.activeElement === el;`) {
		t.Fatalf("cannot focus %s", sel)
	}
	for _, k := range ks {
		if err := chromedp.Do(ctx, chromedp.KeyEvent(k), chromedp.Sleep(450*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
}

// focusNow says what has the focus now: its group and value for an option,
// else its id or key, else its tag and label.
const focusNow = `const a = document.activeElement;
if (!a || a === document.body) return "body";
const seg = a.parentElement && a.parentElement.dataset.seg;
if (seg && a.dataset.v != null) return seg + "=" + a.dataset.v + (a.getAttribute("aria-checked") === "true" || a.getAttribute("aria-current") === "page" ? " on" : "");
if (a.id) return "#" + a.id;
if (a.dataset.k) return "k=" + a.dataset.k;
return a.localName + ":" + (a.getAttribute("aria-label") || a.textContent.trim());`

// TestBrowserKeyboardChoices checks that a choice made with the keyboard
// leaves the focus on the control it was made with, though the blocks around
// it are drawn anew: Image and Video, how to pay, the shape, a step of the
// resolution slider after another, a model chosen in the picker (back on the
// model's card), a tab of the header and a filter of the library.
func TestBrowserKeyboardChoices(t *testing.T) {
	f := newFakeLibrary()
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	at := func(want string) {
		t.Helper()
		if got := js[string](t, ctx, focusNow); got != want {
			t.Errorf("focus on %q, want %q", got, want)
		}
	}

	keys(t, ctx, `#kind [data-v=video]`, " ")
	at("kind=video on")
	keys(t, ctx, `#kind [data-v=image]`, kb.Enter)
	at("kind=image on")

	// The free run takes no settings; on the key they show, and the focus stays on the key.
	keys(t, ctx, `#how [data-seg=pay] [data-v=openrouter]`, " ")
	at("pay=openrouter on")
	tile := js[string](t, ctx, `const b = [...document.querySelectorAll("#how .ratios > .rt[data-v]")].find((b) => b.getAttribute("aria-checked") !== "true"); return b ? b.dataset.v : "";`)
	if tile == "" {
		t.Fatal("no shape to choose")
	}
	keys(t, ctx, `#how .ratios > .rt[data-v="`+tile+`"]`, " ")
	at("set-aspect_ratio=" + tile + " on")

	// A model chosen in the picker with Enter: the picker closes and the focus is on the model's card again.
	keys(t, ctx, "#mhead", kb.Enter)
	js[bool](t, ctx, pageHelpers+`const q = document.querySelector("#pksearch"); q.value = "klein"; q.dispatchEvent(new Event("input")); await wait(500); return true;`)
	keys(t, ctx, `#pkrows .frow .fhit`, kb.Enter)
	at("#mhead")
	if js[bool](t, ctx, `return !document.querySelector("#picker").hidden;`) {
		t.Error("the picker did not close")
	}

	// The resolution slider goes on step by step from the keyboard.
	v0 := js[float64](t, ctx, `const r = document.querySelector("#how .steps input[type=range]"); return r ? +r.value : -1;`)
	if v0 < 0 {
		t.Fatal("no resolution slider on FLUX.3 Klein")
	}
	keys(t, ctx, `#how .steps input[type=range]`, kb.ArrowRight, kb.ArrowRight)
	if v := js[float64](t, ctx, `return +document.activeElement.value;`); v != v0+2 {
		t.Errorf("two steps right of %v went to %v", v0, v)
	}
	at("input:Resolution")

	keys(t, ctx, `#top [data-v=library]`, kb.Enter)
	at("view=library on")
	js[bool](t, ctx, libHelpers+`return !!(await settled());`)
	keys(t, ctx, `#library [data-seg=lib] [data-v=image]`, " ")
	at("lib=image on")
	keys(t, ctx, `#library [data-seg=lib] [data-v=video]`, kb.Enter)
	at("lib=video on")
	noErrors(t, ctx)
}

// tabRound presses Tab n times and returns where the focus went that is
// not inside the element within names (nor the page itself, between the
// last control and the first again).
func tabRound(t *testing.T, ctx context.Context, within string, n int) []string {
	t.Helper()
	var out []string
	for range n {
		if err := chromedp.Do(ctx, chromedp.KeyEvent(kb.Tab), chromedp.Sleep(15*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		where := js[string](t, ctx, `const a = document.activeElement;
	if (!a || a === document.body || a.closest(`+jsString(within)+`)) return "";
	return a.localName + "." + a.className + ":" + (a.getAttribute("aria-label") || a.textContent.trim()).slice(0, 30);`)
		if where != "" {
			out = append(out, where)
		}
	}
	return out
}

// TestBrowserKeyboardDialogs checks that what is open over the page keeps
// the keyboard: Tab goes round inside the picker and the viewer and never
// to the page under them; the question before a deletion starts on Cancel,
// says what it is about to a screen reader, keeps Tab within its two
// answers, and on Escape gives the focus back to Delete; closing the viewer
// gives it back to the run's tile.
func TestBrowserKeyboardDialogs(t *testing.T) {
	f := newFakeLibrary()
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	at := func(want string) {
		t.Helper()
		if got := js[string](t, ctx, focusNow); got != want {
			t.Errorf("focus on %q, want %q", got, want)
		}
	}

	keys(t, ctx, "#mhead", kb.Enter)
	at("#pksearch")
	if out := tabRound(t, ctx, "#picker", 120); len(out) > 0 {
		t.Errorf("Tab left the picker for %v", out)
	}
	keys(t, ctx, "", kb.Escape)
	at("#mhead")

	keys(t, ctx, `#top [data-v=library]`, kb.Enter)
	js[bool](t, ctx, libHelpers+`return !!(await settled());`)
	tile := js[string](t, ctx, libHelpers+`return tiles()[6].dataset.k;`)
	keys(t, ctx, `#library [data-k="`+tile+`"]`, kb.Enter)
	if !js[bool](t, ctx, `return !document.querySelector("#viewer").hidden;`) {
		t.Fatal("the viewer did not open")
	}
	if out := tabRound(t, ctx, "#viewer", 40); len(out) > 0 {
		t.Errorf("Tab left the viewer for %v", out)
	}

	keys(t, ctx, `#viewer .jacts button[aria-label=Delete]`, kb.Enter)
	at("button:Cancel")
	if d := js[string](t, ctx, `const b = document.querySelector("#ask"); return b.getAttribute("aria-describedby") === "askD" ? document.getElementById("askD").textContent : "";`); d == "" {
		t.Error("the question is not described by its text")
	}
	if out := tabRound(t, ctx, "#ask", 6); len(out) > 0 {
		t.Errorf("Tab left the question for %v", out)
	}
	keys(t, ctx, "", kb.Escape)
	at("button:Delete")
	if js[bool](t, ctx, `return !document.querySelector("#ask").hidden || document.querySelector("#viewer").hidden;`) {
		t.Error("Escape did more than answer the question")
	}
	keys(t, ctx, "", kb.Escape)
	at("k=" + tile)
	noErrors(t, ctx)
}
