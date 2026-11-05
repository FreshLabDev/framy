// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"math"
	"regexp"
	"testing"
)

// priceInit opens the page in a language and changes every estimate the
// quote answers with shape, a page function of the estimate: another kind,
// another currency's label.
func priceInit(lang, shape string) string {
	return `
try { localStorage.setItem("framy.lang", ` + jsString(lang) + `); } catch (e) {}
window.__shape = ` + shape + `;
{
  const fetch0 = window.fetch;
  window.fetch = async (u, o) => {
    const r = await fetch0(u, o);
    if (!String(u).endsWith("/api/quote")) return r;
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
  const pn = document.querySelector("#pnum"), cs = getComputedStyle(pn);
  const s = document.createElement("span");
  s.style.cssText = "position:absolute;white-space:pre";
  for (const k of ["fontFamily", "fontSize", "fontWeight", "fontStyle", "fontStretch", "fontVariantNumeric", "lineHeight"]) s.style[k] = cs[k];
  s.textContent = text;
  pn.parentElement.append(s);
  const out = { seen: seen(s), width: s.getBoundingClientRect().width };
  s.remove();
  return out;
};
const figure = () => {
  const pn = document.querySelector("#pnum"), text = pn.dataset.t, p = plain(text);
  const parts = [...pn.children].filter((x) => !x.matches(".whole, .sr"));
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
