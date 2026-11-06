// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// priceInit opens the page in a language and changes every estimate the
// quote answers with shape, a page function of the estimate: another kind,
// another currency's label.
func priceInit(lang, shape string) string {
	return `
try { localStorage.setItem("framy.language", ` + jsString(lang) + `); } catch (e) {}
window.__shape = ` + shape + `;
// A promise in __hold keeps the answers of the quote until it settles.
window.__hold = null;
{
  const fetch0 = window.fetch;
  window.fetch = async (u, o) => {
    const r = await fetch0(u, o);
    if (!String(u).endsWith("/api/quote")) return r;
    if (window.__hold) await window.__hold;
    const b = await r.json();
    for (const it of b.items || []) it.estimate = __shape(it.estimate);
    if (b.total) b.total = __shape(b.total);
    return new Response(JSON.stringify(b), { status: r.status, headers: { "Content-Type": "application/json" } });
  };
}
`
}

// priceHelpers read the price as it stands on the screen.
const priceHelpers = `
// The characters under root in the order they stand on the screen, left to
// right, leaving out what is on its way out.
const seen = (root) => {
  const out = [], w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let n; (n = w.nextNode());) {
    if (n.parentElement.closest(".o, .whole, .sr")) continue;
    for (let i = 0; i < n.length; i++) {
      const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + 1);
      const b = r.getBoundingClientRect();
      if (b.width) out.push([b.left + b.width / 2, n.data[i]]);
    }
  }
  return out.sort((a, b) => a[0] - b[0]).map((x) => x[1]).join("");
};
// The same text as a plain line of the price's font, where the price stands:
// how the browser itself lays it out.
const plain = (text) => {
  const pn = document.querySelector("#podo"), cs = getComputedStyle(pn);
  const s = document.createElement("span");
  s.style.cssText = "position:absolute;white-space:pre";
  for (const k of ["fontFamily", "fontSize", "fontWeight", "fontStyle", "fontStretch", "fontVariantNumeric", "lineHeight"]) s.style[k] = cs[k];
  s.textContent = text;
  pn.closest(".price").append(s);
  const out = { seen: seen(s), width: s.getBoundingClientRect().width };
  s.remove();
  return out;
};
const figure = () => {
  const pn = document.querySelector("#podo"), text = pn.dataset.t, p = plain(text);
  const parts = [...pn.children].filter((x) => !x.matches(".whole"));
  const width = parts.reduce((a, x) => a + x.getBoundingClientRect().width, 0);
  return { text, seen: seen(pn), plain: p.seen, width: Math.round(width), plainWidth: Math.round(p.width), split: pn.querySelectorAll(".ch").length };
};
// A variant that is not free, so the bar shows its price; on a phone it is
// picked in the settings sheet.
const paid = async () => {
  const sheet = document.querySelector("#dtools .chipbtn.set");
  const phone = sheet && vis(sheet);
  if (phone) { sheet.click(); await wait(600); }
  const v = [...document.querySelectorAll("#how [data-seg=var] > button")].find((b) => b.getAttribute("aria-checked") !== "true");
  v.click();
  await wait(900);
  if (phone) { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); await wait(700); }
};
// Where the price stands, and its hint box if it shows.
const tipNow = () => {
  const tip = document.querySelector(".tip"), pn = document.querySelector("#pnum").getBoundingClientRect();
  const r = tip && !tip.hidden ? tip.getBoundingClientRect() : null;
  return { shown: !!r, tip: r ? tip.textContent : "", inside: !!r && r.left >= 0 && r.right <= innerWidth && r.top >= 0, above: !!r && r.bottom <= pn.top };
};
`

// TestBrowserPriceOrder checks that the odometer shows a price as the
// browser would write the same text: in Arabic the figures stay left to
// right with the sign before them, "≈" and "حتى" stand on the right, and a
// word with no figures ("مجاني") keeps its letters joined.
func TestBrowserPriceOrder(t *testing.T) {
	ts := fakeAPI(t)
	for _, tc := range []struct {
		name, lang, shape string
		want              string // the text of the price, as the page reads it
	}{
		{"en about", "en", `(e) => e`, `^≈ \$0\.\d+$`},
		{"ar about", "ar", `(e) => e`, `^≈ \$0\.\d+$`},
		{"ar up to", "ar", `(e) => ({ ...e, kind: "up_to", max_usd: e.usd })`, `^حتى \$0\.\d+$`},
		{"ar zloty", "ar", `(e) => ({ ...e, currency: "PLN", label: e.usd.toFixed(3) + " zł" })`, `^≈ 0\.\d+\x{a0}zł$`},
		{"tr from", "tr", `(e) => ({ ...e, kind: "from" })`, `^\$0\.\d+ ve üzeri$`},
		{"de euro", "de", `(e) => ({ ...e, kind: "up_to", currency: "EUR", label: e.usd.toFixed(3).replace(".", ",") + " €" })`, `^bis zu 0,\d+\x{a0}€$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := browserInit(t, ts.URL, 1440, 900, false, priceInit(tc.lang, tc.shape))
			got := js[map[string]any](t, ctx, pageHelpers+priceHelpers+`
			const free = figure();
			await paid();
			return { free, paid: figure(), dir: document.dir };`)
			free, paid := got["free"].(map[string]any), got["paid"].(map[string]any)
			if want := map[bool]string{true: "rtl", false: "ltr"}[tc.lang == "ar"]; got["dir"] != want {
				t.Fatalf("dir %v, want %s", got["dir"], want)
			}
			// The figures of a right-to-left page stand in isolates, which show and read nothing.
			if !regexp.MustCompile(tc.want).MatchString(strings.NewReplacer("\u2068", "", "\u2069", "").Replace(paid["text"].(string))) {
				t.Fatalf("price %q, want %s", paid["text"], tc.want)
			}
			for name, f := range map[string]map[string]any{"free": free, "paid": paid} {
				if f["seen"] != f["plain"] {
					t.Errorf("%s: the odometer shows %q where the line reads %q", name, f["seen"], f["plain"])
				}
			}
			// A word is never cut into letters: in Arabic they would stop joining.
			if free["split"].(float64) != 0 || math.Abs(free["width"].(float64)-free["plainWidth"].(float64)) > 1 {
				t.Errorf("free: %v", free)
			}
			noErrors(t, ctx)
		})
	}
}

// spoken is what a screen reader gets of the bar's live region: the text of
// every node it reads there, in order.
func spoken(t *testing.T, ctx context.Context) []string {
	t.Helper()
	var nodes []*accessibility.Node
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		r, err := cdp.Call(ctx, tg, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
		nodes = r.Nodes
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	str := func(v *accessibility.Value) string {
		var s string
		if v != nil {
			_ = json.Unmarshal(v.Value, &s)
		}
		return s
	}
	by := map[accessibility.NodeID]*accessibility.Node{}
	var region *accessibility.Node
	for _, n := range nodes {
		by[n.NodeID] = n
		for _, p := range n.Properties {
			if p.Name == "live" && region == nil && str(p.Value) == "polite" {
				region = n
			}
		}
	}
	if region == nil {
		t.Fatal("no live region")
	}
	var out []string
	var walk func(*accessibility.Node)
	walk = func(n *accessibility.Node) {
		if n.Ignored {
			return
		}
		// Only text is read out as it is; a name on a box without a role
		// is not read at all.
		if str(n.Role) == "StaticText" {
			out = append(out, str(n.Name))
		}
		for _, c := range n.ChildIDs {
			if k := by[c]; k != nil {
				walk(k)
			}
		}
	}
	walk(region)
	return out
}

// TestBrowserPriceSpoken checks what a screen reader gets of the price: the
// figure as text in the bar's live region, the same as on the screen, so a
// change is announced; nothing while a price is on its way; and none of the
// odometer's characters.
func TestBrowserPriceSpoken(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browserInit(t, ts.URL, 1440, 900, false, priceInit("en", `(e) => e`))
	// The price is the first line the region reads, the caption under it the next.
	read := func(name, want string) {
		t.Helper()
		got := spoken(t, ctx)
		if len(got) == 0 || got[0] != want {
			t.Errorf("%s: read %q, want %q first", name, got, want)
		}
		for _, s := range got {
			if len([]rune(s)) == 1 {
				t.Errorf("%s: a character read on its own: %q", name, got)
			}
		}
	}
	dom := js[map[string]any](t, ctx, `
	const pn = document.querySelector("#pnum");
	return { label: pn.getAttribute("aria-label"), hidden: document.querySelector("#podo").getAttribute("aria-hidden"), text: document.querySelector("#podo").dataset.t };`)
	if dom["label"] != nil || dom["hidden"] != "true" {
		t.Errorf("the price box: %v", dom)
	}
	read("free", dom["text"].(string))
	paid := js[string](t, ctx, pageHelpers+priceHelpers+`await paid(); return document.querySelector("#podo").dataset.t;`)
	if !strings.HasPrefix(paid, "≈ $") {
		t.Fatalf("paid: %q", paid)
	}
	read("paid", paid)
	// Another shape is a price nobody asked for yet: held, it is on its way.
	held := js[map[string]any](t, ctx, pageHelpers+`
	window.__hold = new Promise((r) => (window.__go = r));
	const r = [...document.querySelectorAll("#how .ratios > button.rt")].find((b) => b.getAttribute("aria-checked") !== "true" && !b.disabled);
	r.click();
	await wait(500);
	return { shown: document.querySelector("#podo").dataset.t, said: document.querySelector("#psr").textContent };`)
	if held["shown"] != "…" || held["said"] != "" {
		t.Errorf("on its way: %v", held)
	}
	if got := spoken(t, ctx); len(got) > 0 && strings.HasPrefix(got[0], "≈") {
		t.Errorf("on its way: read %q", got)
	}
	next := js[string](t, ctx, pageHelpers+`
	window.__go(); window.__hold = null;
	await wait(900);
	return document.querySelector("#podo").dataset.t;`)
	if next == paid || !strings.HasPrefix(next, "≈ $") {
		t.Errorf("after: %q, before %q", next, paid)
	}
	read("after", next)
	noErrors(t, ctx)
}

// pointAt is the middle of what sel finds, in CSS pixels of the viewport.
func pointAt(t *testing.T, ctx context.Context, sel string) (x, y float64) {
	t.Helper()
	p := js[[]float64](t, ctx, `const r = document.querySelector(`+jsString(sel)+`).getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2];`)
	return p[0], p[1]
}

// tap touches the screen at the middle of what sel finds, as a finger does.
func tap(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	x, y := pointAt(t, ctx, sel)
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		if _, err := cdp.Call(ctx, tg, input.DispatchTouchEvent, input.DispatchTouchEventParams{Type: input.DispatchTouchEventTypeTouchStart, TouchPoints: []*input.TouchPoint{{X: x, Y: y}}}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, tg, input.DispatchTouchEvent, input.DispatchTouchEventParams{Type: input.DispatchTouchEventTypeTouchEnd, TouchPoints: []*input.TouchPoint{}})
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
}

// mouseTo moves the mouse to the middle of what sel finds.
func mouseTo(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	x, y := pointAt(t, ctx, sel)
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseMoved, X: x, Y: y})
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
}

// press presses a key on the keyboard.
func press(t *testing.T, ctx context.Context, key, code string, vk int64) {
	t.Helper()
	err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		if _, err := cdp.Call(ctx, tg, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeRawKeyDown, Key: key, Code: code, WindowsVirtualKeyCode: vk}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, tg, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: key, Code: code, WindowsVirtualKeyCode: vk})
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
}

// euro prices every run in euros, its dollars behind it.
const euro = `(e) => ({ ...e, currency: "EUR", label: "€" + (e.usd * 0.9).toFixed(2) })`

// TestBrowserPriceHint checks the dollars behind a price in another
// currency: a tap shows them on a phone, hovering does with a mouse, and
// focus from the keyboard; a screen reader has them as the price's
// description; and a price in dollars promises no hint it does not have.
func TestBrowserPriceHint(t *testing.T) {
	ts := fakeAPI(t)
	t.Run("dollars", func(t *testing.T) {
		ctx := browserInit(t, ts.URL, 1440, 900, false, priceInit("en", `(e) => e`))
		got := js[map[string]any](t, ctx, pageHelpers+priceHelpers+`
		await paid();
		const pn = document.querySelector("#pnum"), cs = getComputedStyle(pn);
		return { text: document.querySelector("#podo").dataset.t, cursor: cs.cursor, line: cs.borderBottomStyle, role: pn.getAttribute("role"), tab: pn.getAttribute("tabindex"), title: pn.getAttribute("title") };`)
		if got["cursor"] == "help" || got["line"] != "none" || got["role"] != nil || got["tab"] != nil || got["title"] != nil {
			t.Errorf("a price in dollars: %v", got)
		}
		noErrors(t, ctx)
	})
	t.Run("phone", func(t *testing.T) {
		ctx := browserInit(t, ts.URL, 390, 844, true, priceInit("en", euro))
		got := js[map[string]any](t, ctx, pageHelpers+priceHelpers+`
		await paid();
		const pn = document.querySelector("#pnum"), cs = getComputedStyle(pn);
		return { text: document.querySelector("#podo").dataset.t, line: cs.borderBottomStyle, role: pn.getAttribute("role"), tab: pn.getAttribute("tabindex"),
		  hint: document.getElementById(pn.getAttribute("aria-describedby"))?.textContent, ...tipNow() };`)
		usd, _ := got["hint"].(string)
		if !regexp.MustCompile(`^≈ €0\.\d\d$`).MatchString(got["text"].(string)) || !regexp.MustCompile(`^\$0\.\d{2,4}$`).MatchString(usd) ||
			got["line"] != "dotted" || got["role"] != "button" || got["tab"] != "0" || got["shown"] != false {
			t.Fatalf("a price in euros: %v", got)
		}
		tipAfter := func(name string, shown bool) {
			t.Helper()
			s := js[map[string]any](t, ctx, pageHelpers+priceHelpers+`await wait(300); return tipNow();`)
			if s["shown"] != shown || (shown && (s["tip"] != usd || s["inside"] != true || s["above"] != true)) {
				t.Errorf("%s: %v, want shown %v with %q", name, s, shown, usd)
			}
		}
		tap(t, ctx, "#pnum")
		tipAfter("tap", true)
		tap(t, ctx, "#pnum")
		tipAfter("tap again", false)
		tap(t, ctx, "#pnum")
		tipAfter("tap once more", true)
		tap(t, ctx, "#prompt")
		tipAfter("tap elsewhere", false)
		noErrors(t, ctx)
	})
	t.Run("desktop", func(t *testing.T) {
		ctx := browserInit(t, ts.URL, 1440, 900, false, priceInit("en", euro))
		usd := js[string](t, ctx, pageHelpers+priceHelpers+`
		await paid();
		const pn = document.querySelector("#pnum");
		return document.getElementById(pn.getAttribute("aria-describedby")).textContent;`)
		tipAfter := func(name string, shown bool) {
			t.Helper()
			s := js[map[string]any](t, ctx, pageHelpers+priceHelpers+`await wait(450); return tipNow();`)
			if s["shown"] != shown || (shown && (s["tip"] != usd || s["inside"] != true)) {
				t.Errorf("%s: %v, want shown %v with %q", name, s, shown, usd)
			}
		}
		mouseTo(t, ctx, "#pnum")
		tipAfter("hover", true)
		mouseTo(t, ctx, "#prompt")
		tipAfter("away", false)
		// From the keyboard: focus shows it, Enter and Escape hide it.
		press(t, ctx, "Shift", "ShiftLeft", 16)
		js[bool](t, ctx, `document.querySelector("#pnum").focus(); return true;`)
		tipAfter("focus", true)
		press(t, ctx, "Enter", "Enter", 13)
		tipAfter("enter", false)
		press(t, ctx, "Enter", "Enter", 13)
		tipAfter("enter again", true)
		press(t, ctx, "Escape", "Escape", 27)
		tipAfter("escape", false)
		// A screen reader reads the price, a button, with its dollars as its description.
		var found bool
		err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			r, err := cdp.Call(ctx, tg, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
			for _, n := range r.Nodes {
				var role, name, desc string
				if n.Role != nil {
					_ = json.Unmarshal(n.Role.Value, &role)
				}
				if n.Name != nil {
					_ = json.Unmarshal(n.Name.Value, &name)
				}
				if n.Description != nil {
					_ = json.Unmarshal(n.Description.Value, &desc)
				}
				if role == "button" && strings.Contains(name, "€") {
					found = desc == usd
					if !found {
						t.Errorf("the price as a screen reader has it: %q, %q", name, desc)
					}
				}
			}
			return err
		}))
		if err != nil || !found {
			t.Errorf("no price button with its dollars: %v", err)
		}
		// A button still has its price read out in the live region.
		shown := js[string](t, ctx, `return document.querySelector("#podo").dataset.t;`)
		if got := spoken(t, ctx); len(got) == 0 || got[0] != shown {
			t.Errorf("the live region reads %q, the price is %q", got, shown)
		}
		noErrors(t, ctx)
	})
}

// TestBrowserRatios checks the row of aspect ratios with a photo attached:
// "As photo" is a word among figures, its column as wide as the word in the
// longest languages too, every tile keeps its whole label on one line, and
// the other columns are as wide as each other, at 1440 px and on a phone of
// 320 px. The ratios past the common ones open with "+N" and close with
// "−N", which fit as well.
func TestBrowserRatios(t *testing.T) {
	ts := fakeAPI(t)
	for _, tc := range []struct {
		lang  string
		w, h  int64
		phone bool
	}{
		{"en", 1440, 900, false}, {"de", 1440, 900, false}, {"fr", 1440, 900, false}, {"fr", 320, 568, true}, {"ru", 320, 568, true},
	} {
		t.Run(fmt.Sprintf("%s-%d", tc.lang, tc.w), func(t *testing.T) {
			ctx := browserInit(t, ts.URL, tc.w, tc.h, tc.phone, priceInit(tc.lang, `(e) => e`))
			got := js[map[string]any](t, ctx, pageHelpers+priceHelpers+`
			const cv = document.createElement("canvas"); cv.width = 64; cv.height = 36;
			const dt = new DataTransfer(); dt.items.add(new File([await new Promise((r) => cv.toBlob(r, "image/png"))], "p.png", { type: "image/png" }));
			const inp = document.querySelector("#file"); inp.files = dt.files; inp.dispatchEvent(new Event("change", { bubbles: true }));
			await wait(1200);
			await paid();
			const sheet = document.querySelector("#dtools .chipbtn.set");
			if (sheet && vis(sheet)) { sheet.click(); await wait(700); }
			const row = () => [...document.querySelectorAll("#how .ratios")].find(vis);
			const look = () => {
			  const rb = row(), r = rb.getBoundingClientRect(), tiles = [...rb.querySelectorAll(":scope > button")];
			  const cut = tiles.filter((b) => b.scrollWidth > b.clientWidth + 1 || b.scrollHeight > b.clientHeight + 1 || [...b.querySelectorAll(".rl")].some((l) => l.scrollWidth > l.clientWidth + 1));
			  const lines = tiles.map((b) => b.querySelector(".rl")).filter(Boolean).map((l) => Math.round(l.getBoundingClientRect().height));
			  // The ratios share the room the word leaves: past its column they are all as wide.
			  const widths = [...new Set(tiles.filter((b) => b.getBoundingClientRect().left > r.left + 1).map((b) => Math.round(b.getBoundingClientRect().width)))];
			  return { first: tiles[0].textContent, cut: cut.map((b) => b.textContent), lines, widths, out: Math.round(Math.max(0, r.right - rb.parentElement.getBoundingClientRect().right)), more: tiles.find((b) => b.classList.contains("more"))?.textContent || "" };
			};
			const out = { common: look() };
			// FLUX 3 has more ratios than the common ones.
			if (sheet && vis(sheet)) { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); await wait(700); }
			const opener = [...document.querySelectorAll("#how .mhead, #dtools .chipbtn:not(.set)")].find(vis);
			opener.click();
			await wait(700);
			const q = document.querySelector("#pksearch");
			q.value = "FLUX"; q.dispatchEvent(new Event("input", { bubbles: true }));
			await wait(800);
			const fam = [...document.querySelectorAll("#pkrows .frow")].find((r) => /^FLUX/.test(r.textContent) && !/Klein|Flex|Max|Pro|2/.test(r.querySelector(".fhit")?.textContent || ""));
			fam.querySelector(".fhit").click();
			await wait(500);
			const choose = [...document.querySelectorAll("#picker .det .choose")].find(vis);
			if (choose && !choose.disabled) choose.click();
			await wait(900);
			const again = document.querySelector("#dtools .chipbtn.set");
			if (again && vis(again)) { again.click(); await wait(700); }
			out.closed = look();
			row().querySelector(".rt.more").click();
			await wait(700);
			out.open = look();
			return out;`)
			for name, v := range got {
				r := v.(map[string]any)
				if r["first"] == "" || len(r["cut"].([]any)) > 0 || r["out"].(float64) > 0 {
					t.Errorf("%s: %v", name, r)
				}
				for _, h := range r["lines"].([]any) {
					if h.(float64) > 14 {
						t.Errorf("%s: a label on two lines: %v", name, r)
					}
				}
				if w := r["widths"].([]any); len(w) > 2 || len(w) == 2 && math.Abs(w[0].(float64)-w[1].(float64)) > 1 {
					t.Errorf("%s: the ratios differ in width: %v", name, r)
				}
			}
			closed, open := got["closed"].(map[string]any)["more"].(string), got["open"].(map[string]any)["more"].(string)
			if !strings.HasPrefix(closed, "+") || "−"+closed[1:] != open {
				t.Errorf("more and fewer: %q, then %q", closed, open)
			}
			noErrors(t, ctx)
		})
	}
}
