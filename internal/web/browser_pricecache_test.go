// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"fmt"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

// priceBundle is price.js on its own, built the way the page is, as a script
// that leaves its exports in window.__price. The page under test runs its own
// copy; this one has a fetch and a clock the test controls.
func priceBundle(t *testing.T) string {
	t.Helper()
	res := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   `import * as P from "./price.js"; window.__price = P;`,
			ResolveDir: "app/src",
			Loader:     api.LoaderJS,
		},
		Bundle: true,
		Format: api.FormatIIFE,
		Write:  false,
	})
	if len(res.Errors) > 0 {
		t.Fatalf("bundling price.js: %v", res.Errors[0].Text)
	}
	return string(res.OutputFiles[0].Contents)
}

// priceKit is what the tests of the quote share: an item per number, a quote
// that answers with a price, and a wait for the page to stop asking.
const priceKit = `
const P = window.__price;
const item = (i) => ({ offer: "test/model", settings: { n: i } });
const answer = (b) => new Response(JSON.stringify({ items: b.items.map(() => ({ estimate: { kind: "exact", usd: 0.01, currency: "USD", label: "$0.01" } })) }), { headers: { "Content-Type": "application/json" } });
const settle = async () => { for (let i = 0; i < 400 && P.busy(); i++) await new Promise((r) => setTimeout(r, 10)); };
`

// TestBrowserPriceCacheBounded: a long session keeps the latest answers of the
// quote and no more, and what is read stays among them.
func TestBrowserPriceCacheBounded(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, priceBundle(t)+"; return true;")
	got := js[map[string]any](t, ctx, priceKit+`
	const fetch0 = window.fetch;
	window.fetch = async (u, o) => (String(u).endsWith("/api/quote") ? answer(JSON.parse(o.body)) : fetch0(u, o));
	const max = P.CACHE_MAX;
	for (let i = 0; i < max - 100; i++) P.need(item(i));
	await settle();
	const before = [P.known(item(0)) !== undefined, P.known(item(max - 101)) !== undefined];
	P.known(item(0)); // read: it moves to the back
	for (let i = max - 100; i < max + 300; i++) P.need(item(i));
	await settle();
	window.fetch = fetch0;
	return { before, first: P.known(item(0)) !== undefined, second: P.known(item(1)) !== undefined, last: P.known(item(max + 299)) !== undefined };`)
	if b := got["before"].([]any); b[0] != true || b[1] != true {
		t.Errorf("answers under the limit were lost: %v", got)
	}
	if got["first"] != true {
		t.Errorf("an answer that was read was dropped with the oldest: %v", got)
	}
	if got["second"] != false {
		t.Errorf("the oldest answer is still kept past the limit: %v", got)
	}
	if got["last"] != true {
		t.Errorf("the newest answer is missing: %v", got)
	}
}

// TestBrowserPriceRetry: a price on the page that did not come because the
// network was down or the server busy is asked again by itself, each wait
// twice the last and 30 s at most, for as long as the page shows it (Create
// waits for it, so giving up would leave the person stuck); one framy refused
// is asked once; back online, a price that did not come is asked at once.
func TestBrowserPriceRetry(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, priceBundle(t)+"; return true;")
	got := js[map[string]any](t, ctx, priceKit+`
	const fetch0 = window.fetch, st = window.setTimeout, waits = [];
	// The waits between tries are seconds; the test lives them in milliseconds.
	window.setTimeout = (f, ms, ...a) => { if (ms >= 500) waits.push(Math.round(ms / 100) * 100); return st(f, ms >= 500 ? 15 : ms, ...a); };
	const pause = (ms) => new Promise((r) => st(r, ms));
	let calls = 0, mode = "network";
	window.fetch = async (u, o) => {
	  if (!String(u).endsWith("/api/quote")) return fetch0(u, o);
	  calls++;
	  if (mode === "network") throw new TypeError("offline");
	  if (mode === "busy") return new Response(JSON.stringify({ error: "unavailable" }), { status: 503, headers: { "Content-Type": "application/json" } });
	  if (mode === "refused") return new Response(JSON.stringify({ error: "bad_request" }), { status: 400, headers: { "Content-Type": "application/json" } });
	  return answer(JSON.parse(o.body));
	};
	const shown = (it) => { const el = P.priceEl(it); document.body.append(el); return el; };
	const out = {};
	// Down for the first two tries, then back: the price on the page fills in.
	const a = shown(item(1));
	await pause(45);
	mode = "busy";
	await pause(60);
	mode = "ok";
	await pause(200);
	await settle();
	out.recovered = [P.known(item(1)) !== undefined, a.textContent, calls >= 3];
	// Down for good: asked again while it is on the page, the waits growing.
	calls = 0; waits.length = 0; mode = "network";
	const b = shown(item(2));
	await pause(500);
	out.waits = waits.slice(0, 7);
	// Off the page, nobody asks.
	b.remove();
	await pause(150);
	const n0 = calls;
	await pause(300);
	out.offPage = calls - n0;
	// A refusal is asked once, on the page or not.
	calls = 0; mode = "refused";
	shown(item(3));
	await pause(300);
	out.refused = calls;
	// Back online, a price that did not come is asked at once, not after its wait.
	window.setTimeout = st;
	calls = 0; mode = "network";
	shown(item(4));
	await pause(150);
	mode = "ok";
	dispatchEvent(new Event("online"));
	await pause(150);
	// Long before its wait of a second is over. (The page's own prices hear the event too.)
	out.online = P.known(item(4)) !== undefined;
	window.fetch = fetch0;
	return out;`)
	want := map[string]string{
		"recovered": "[true $0.01 true]", "waits": "[1000 2000 4000 8000 16000 30000 30000]",
		"offPage": "0", "refused": "1", "online": "true",
	}
	for k, w := range want {
		if g := fmt.Sprint(got[k]); g != w {
			t.Errorf("%s = %s, want %s", k, g, w)
		}
	}
}
