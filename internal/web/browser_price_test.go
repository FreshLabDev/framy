// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

// priceInit opens the page in a language and changes every estimate the
// quote answers with shape, a page function of the estimate: another kind,
// another currency's label.
func priceInit(lang, shape string) string {
	return `
try { localStorage.setItem("framy.lang", ` + jsString(lang) + `); } catch (e) {}
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
// A variant that is not free, so the bar shows its price.
const paid = async () => {
  const v = [...document.querySelectorAll("#how [data-seg=var] > button")].find((b) => b.getAttribute("aria-checked") !== "true");
  v.click();
  await wait(900);
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
			if !regexp.MustCompile(tc.want).MatchString(paid["text"].(string)) {
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
