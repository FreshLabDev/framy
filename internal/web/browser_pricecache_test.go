// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
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

// TestBrowserPriceRetry: a price that did not come because the network was
// down or the server busy is asked again by itself; a refusal is not.
func TestBrowserPriceRetry(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, priceBundle(t)+"; return true;")
	got := js[map[string]any](t, ctx, priceKit+`
	const fetch0 = window.fetch, st = window.setTimeout;
	// The pauses between tries are seconds; the test lives them in milliseconds.
	window.setTimeout = (f, ms, ...a) => st(f, ms >= 1000 ? 15 : ms, ...a);
	let calls = 0, mode = "network";
	window.fetch = async (u, o) => {
	  if (!String(u).endsWith("/api/quote")) return fetch0(u, o);
	  calls++;
	  if (mode === "network") throw new TypeError("offline");
	  if (mode === "busy") return new Response(JSON.stringify({ error: "unavailable" }), { status: 503, headers: { "Content-Type": "application/json" } });
	  if (mode === "refused") return new Response(JSON.stringify({ error: "bad_request" }), { status: 400, headers: { "Content-Type": "application/json" } });
	  return answer(JSON.parse(o.body));
	};
	const out = {};
	// Down for the first two tries, then back.
	const a = item(1);
	P.need(a);
	await new Promise((r) => st(r, 5));
	mode = "busy";
	await new Promise((r) => st(r, 60));
	mode = "ok";
	await new Promise((r) => st(r, 200));
	await settle();
	out.recovered = P.known(a) !== undefined; out.callsRecovered = calls;
	// Down for good: it tries a few times and gives up, no loop.
	calls = 0; mode = "network";
	P.need(item(2));
	await new Promise((r) => st(r, 600));
	await settle();
	out.gaveUp = calls; out.stillBusy = P.busy();
	// A refusal is asked once.
	calls = 0; mode = "refused";
	P.need(item(3));
	await new Promise((r) => st(r, 200));
	out.refused = calls;
	// Asking again after giving up works once the network is back.
	mode = "ok";
	P.need(item(2));
	await new Promise((r) => st(r, 200));
	out.later = P.known(item(2)) !== undefined;
	window.fetch = fetch0; window.setTimeout = st;
	return out;`)
	if got["recovered"] != true {
		t.Errorf("the price did not come back after the network did: %v", got)
	}
	if n := got["gaveUp"].(float64); n < 3 || n > 7 || got["stillBusy"] != false {
		t.Errorf("a quote that stays down was asked %v times (busy: %v), want a few and then stop", got["gaveUp"], got["stillBusy"])
	}
	if got["refused"] != float64(1) {
		t.Errorf("a refusal was asked %v times, want once", got["refused"])
	}
	if got["later"] != true {
		t.Errorf("asking again after giving up did not work: %v", got)
	}
}
