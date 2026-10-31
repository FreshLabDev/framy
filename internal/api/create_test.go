// SPDX-License-Identifier: Apache-2.0
package api

import (
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/price"
)

const testOffer = "openrouter:test/image"

var keyCounter atomic.Int64

// nextKey is an Idempotency-Key no other request of the test used.
func nextKey() string { return fmt.Sprintf("test-key-%d", keyCounter.Add(1)) }

// jobRig is a rig with an offer, a connection for it, and one file of
// alice's.
func jobRig(t *testing.T, opts ...func(*rigOptions)) *rig {
	t.Helper()
	r := newRig(t, opts...)
	r.store.offers[testOffer] = image(testOffer, catalog.OpenRouter)
	r.store.offers["openrouter:microsoft/mai-image-2.6-flash"] = image("openrouter:microsoft/mai-image-2.6-flash", catalog.OpenRouter)
	r.conns.list = []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}
	r.data.addAsset(alice, InputAsset{ID: 50, ContentType: "image/jpeg", Width: 1024, Height: 1024})
	r.data.addAsset(bob, InputAsset{ID: 51, ContentType: "image/jpeg", Width: 1024, Height: 1024})
	return r
}

func body(offer, pay string, extra ...string) string {
	s := fmt.Sprintf(`{"offer":%q,"pay":%q,"prompt":"a fox in the snow"`, offer, pay)
	for _, e := range extra {
		s += "," + e
	}
	return s + "}"
}

func TestACreateRequestIsChecked(t *testing.T) {
	r := jobRig(t)
	long := strings.Repeat("я", maxPrompt+1)
	for _, c := range []struct {
		name, body, field string
	}{
		{"not JSON", `nope`, ""},
		{"empty", ``, ""},
		{"two objects", body(testOffer, "key") + body(testOffer, "key"), ""},
		{"unknown field", body(testOffer, "key", `"free":true`), ""},
		{"no offer", `{"pay":"key","prompt":"x"}`, "offer"},
		{"no pay", `{"offer":"` + testOffer + `","prompt":"x"}`, "pay"},
		{"pay by something else", body(testOffer, "card"), "pay"},
		{"surface", body(testOffer, "key", `"surface":"group"`), "surface"},
		{"nothing to run", `{"offer":"` + testOffer + `","pay":"key","prompt":"  "}`, "prompt"},
		{"prompt too long", `{"offer":"` + testOffer + `","pay":"key","prompt":"` + long + `"}`, "prompt"},
		{"NUL in the prompt", `{"offer":"` + testOffer + `","pay":"key","prompt":"a\u0000b"}`, "prompt"},
		{"too many inputs", body(testOffer, "key", `"inputs":[`+strings.TrimSuffix(strings.Repeat("1,", maxInputs+1), ",")+`]`), "inputs"},
		{"input zero", body(testOffer, "key", `"inputs":[0]`), "inputs"},
		{"input a string", body(testOffer, "key", `"inputs":["50"]`), ""},
		{"setting an object", body(testOffer, "key", `"settings":{"size":{"w":1}}`), "settings.size"},
		{"setting a list", body(testOffer, "key", `"settings":{"size":[1]}`), "settings.size"},
		{"setting with NUL", body(testOffer, "key", `"settings":{"size":"a\u0000"}`), "settings.size"},
	} {
		resp := r.post(alice, "/api/jobs", nextKey(), c.body)
		m := jsonOf(t, resp)
		if resp.StatusCode != http.StatusBadRequest || m["error"] != "bad_request" {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
			continue
		}
		if c.field != "" && m["field"] != c.field {
			t.Errorf("%s: field %v, want %s", c.name, m["field"], c.field)
		}
		if msg, _ := m["message"].(string); strings.Contains(msg, "nope") {
			t.Errorf("%s: the answer echoes the body: %q", c.name, msg)
		}
	}
	if r.queue.calls() != 0 {
		t.Errorf("a bad request reached the queue %d times", r.queue.calls())
	}
}

func TestACreateRequestNeedsAnIdempotencyKey(t *testing.T) {
	r := jobRig(t)
	for _, key := range []string{"", "has space", strings.Repeat("a", 129), "ключ"} {
		resp := r.post(alice, "/api/jobs", "", body(testOffer, "key"))
		if key != "" {
			resp = r.do(alice, "POST", "/api/jobs", strings.NewReader(body(testOffer, "key")), "Idempotency-Key", key)
		}
		m := jsonOf(t, resp)
		if resp.StatusCode != http.StatusBadRequest || m["field"] != "Idempotency-Key" {
			t.Errorf("key %q: %d %v", key, resp.StatusCode, m)
		}
	}
	if r.queue.calls() != 0 {
		t.Error("a job was made without a key")
	}
}

func TestAnUnknownOfferIsRefused(t *testing.T) {
	r := jobRig(t)
	resp := r.post(alice, "/api/jobs", "k1", body("openrouter:nobody/nothing", "key"))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "unknown_offer" {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
}

func TestAPaidRunNeedsTheProvidersConnection(t *testing.T) {
	r := jobRig(t)
	for _, c := range []struct {
		name   string
		conns  []keys.Connection
		err    error
		status int
		code   string
	}{
		{"none", nil, nil, http.StatusPaymentRequired, "no_connection"},
		{"another provider's", []keys.Connection{{Provider: catalog.OpenAI, Status: keys.Valid}}, nil, http.StatusPaymentRequired, "no_connection"},
		{"no KEK file", nil, keys.ErrDisabled, http.StatusPaymentRequired, "no_connection"},
		{"refused by the provider", []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Invalid}}, nil, http.StatusPaymentRequired, "connection_invalid"},
		{"short of credit still goes", []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.NoCredits}}, nil, http.StatusAccepted, ""},
		{"valid", []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}, nil, http.StatusAccepted, ""},
	} {
		r.conns.list, r.conns.err = c.conns, c.err
		resp := r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key"))
		m := jsonOf(t, resp)
		if resp.StatusCode != c.status || (c.code != "" && m["error"] != c.code) {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
		if c.code != "" && m["provider"] != "openrouter" {
			t.Errorf("%s: the refusal names the provider: %v", c.name, m)
		}
	}
	// A free run needs none.
	r.conns.list, r.conns.err = nil, keys.ErrDisabled
	resp := r.post(alice, "/api/jobs", nextKey(), body("openrouter:microsoft/mai-image-2.6-flash", "free"))
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("free without a connection: %d", resp.StatusCode)
	}
}

func TestAFreeRunGetsTheAllowancesSettings(t *testing.T) {
	r := jobRig(t)
	const mai = "openrouter:microsoft/mai-image-2.6-flash"
	resp := r.post(alice, "/api/jobs", "k1", body(mai, "free"))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d", resp.StatusCode)
	}
	n := r.queue.last()
	if !n.Free || n.Settings["n"] != 1 {
		t.Errorf("%+v", n)
	}
	// What the person set stays, for the allowance to judge.
	withN := image(mai, catalog.OpenRouter)
	one, four := 1.0, 4.0
	withN.Offers[0].Params["n"] = catalog.Param{Type: catalog.Range, Min: &one, Max: &four}
	r.store.offers[mai] = withN
	r.post(alice, "/api/jobs", "k2", body(mai, "free", `"settings":{"n":4,"quality":null}`))
	if n := r.queue.last(); n.Settings["n"] != float64(4) {
		t.Errorf("a setting of the person's is overwritten: %v", n.Settings)
	}
	r.store.offers[mai] = image(mai, catalog.OpenRouter)
	// An offer the allowance does not run is not free, said before the queue.
	before := r.queue.calls()
	resp = r.post(alice, "/api/jobs", "k3", body(testOffer, "free"))
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "not_free" || r.queue.calls() != before {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
	// The person's own key is not touched by a free run, and a paid run does
	// not get the allowance's settings.
	r.post(alice, "/api/jobs", "k4", body(mai, "key"))
	if n := r.queue.last(); n.Free || len(n.Settings) != 0 {
		t.Errorf("%+v", n)
	}
}

func TestEachRefusalOfTheQueueHasItsCode(t *testing.T) {
	r := jobRig(t)
	freesAt := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	until := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	for _, c := range []struct {
		name      string
		err       error
		status    int
		code      string
		retry     bool
		fieldKeys []string
	}{
		{"limit", &free.LimitError{Kind: catalog.Image, Limit: 5, Window: 24 * time.Hour, FreesAt: freesAt}, 429, "free_limit", true, []string{"frees_at", "kind", "limit", "window_seconds"}},
		{"closed until", &free.ClosedError{Fuse: free.FuseBudget, Until: until, Why: "internal words"}, 503, "free_closed", true, []string{"until"}},
		{"closed, no time", &free.ClosedError{Fuse: free.FuseKey, Why: "internal words"}, 503, "free_closed", false, nil},
		{"not free", &free.NotFreeError{Reason: "n is locked to 1 on the free allowance"}, 422, "not_free", false, []string{"reason"}},
		{"wrapped no input", fmt.Errorf("enqueue: %w", jobs.ErrNoInput), 422, "unknown_input", false, nil},
		{"anything else", fmt.Errorf("%w: password=hunter2", errBoom), 500, "internal", false, nil},
	} {
		r.queue.err = c.err
		resp := r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key"))
		raw := resp.Header.Get("Retry-After")
		m := jsonOf(t, resp)
		if resp.StatusCode != c.status || m["error"] != c.code {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
		if c.retry != (raw != "") {
			t.Errorf("%s: Retry-After %q", c.name, raw)
		}
		for _, k := range c.fieldKeys {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: no %s in %v", c.name, k, m)
			}
		}
		if s := fmt.Sprint(m); strings.Contains(s, "hunter2") || strings.Contains(s, "internal words") {
			t.Errorf("%s: the answer shows what only logs should: %v", c.name, m)
		}
	}
	// The window and the time are the allowance's, in the answer.
	r.queue.err = &free.LimitError{Kind: catalog.Video, Limit: 1, Window: 7 * 24 * time.Hour, FreesAt: freesAt}
	m := jsonOf(t, r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key")))
	if m["kind"] != "video" || m["limit"] != float64(1) || m["window_seconds"] != float64(7*24*3600) || m["frees_at"] != freesAt.Format(time.RFC3339) {
		t.Errorf("%v", m)
	}
}

func TestARepeatedRequestOfADeletedJobStartsNoOther(t *testing.T) {
	r := jobRig(t)
	m := jsonOf(t, r.post(alice, "/api/jobs", "gone-key", body(testOffer, "key")))
	id := int64(m["id"].(float64))
	r.data.mu.Lock()
	delete(r.data.states, id)
	r.data.mu.Unlock()
	again := r.post(alice, "/api/jobs", "gone-key", body(testOffer, "key"))
	if m := jsonOf(t, again); again.StatusCode != http.StatusNotFound || m["error"] != "not_found" {
		t.Fatalf("%d %v", again.StatusCode, m)
	}
	if r.queue.calls() != 1 {
		t.Errorf("the queue was asked %d times", r.queue.calls())
	}
}

func TestARepeatedRequestIsTheSameJob(t *testing.T) {
	r := jobRig(t)
	first := r.post(alice, "/api/jobs", "same-key", body(testOffer, "key"))
	m1 := jsonOf(t, first)
	if first.StatusCode != http.StatusAccepted || first.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("%d %v", first.StatusCode, m1)
	}
	if got := r.queue.last().DedupeKey; got != "web:1001:same-key" {
		t.Errorf("the dedupe key is %q", got)
	}
	if first.Header.Get("Location") != fmt.Sprintf("/api/jobs/%.0f", m1["id"]) {
		t.Errorf("location %q", first.Header.Get("Location"))
	}

	// The same key again, even with another body, even if the offer is gone
	// by now: the job that key started.
	delete(r.store.offers, testOffer)
	again := r.post(alice, "/api/jobs", "same-key", body("openrouter:other/thing", "free"))
	m2 := jsonOf(t, again)
	if again.StatusCode != http.StatusAccepted || again.Header.Get("Idempotent-Replayed") != "true" || m2["id"] != m1["id"] {
		t.Errorf("%d %v", again.StatusCode, m2)
	}
	if r.queue.calls() != 1 {
		t.Errorf("the queue was asked %d times", r.queue.calls())
	}

	// The key is the person's own: another person with it starts a job of
	// their own.
	r.store.offers[testOffer] = image(testOffer, catalog.OpenRouter)
	r.conns.list = []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}
	other := jsonOf(t, r.post(bob, "/api/jobs", "same-key", body(testOffer, "key")))
	if other["id"] == m1["id"] || r.queue.last().DedupeKey != "web:1002:same-key" {
		t.Errorf("%v %q", other, r.queue.last().DedupeKey)
	}
	// And cannot read the first one.
	if resp := r.do(bob, "GET", fmt.Sprintf("/api/jobs/%.0f", m1["id"]), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("%d", resp.StatusCode)
	}
}

func TestAnInputMustBeTheirsAndItsPriceCounts(t *testing.T) {
	r := jobRig(t)
	resp := r.post(alice, "/api/jobs", "k1", body(testOffer, "key", `"inputs":[51]`))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "unknown_input" {
		t.Errorf("another person's file: %d %v", resp.StatusCode, m)
	}
	resp = r.post(alice, "/api/jobs", "k2", body(testOffer, "key", `"inputs":[999]`))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "unknown_input" {
		t.Errorf("a file that is not there: %d %v", resp.StatusCode, m)
	}
	resp = r.post(alice, "/api/jobs", "k3", body(testOffer, "key", `"inputs":[50]`))
	if resp.StatusCode != http.StatusAccepted || len(r.queue.last().Inputs) != 1 || r.queue.last().Inputs[0] != 50 {
		t.Errorf("%d %+v", resp.StatusCode, r.queue.last())
	}
}

func TestTheCurrencyFollowsTheRules(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	for _, c := range []struct {
		name    string
		proxies []netip.Prefix
		chosen  string
		country string
		coreLng string
		accept  string
		want    string
	}{
		{"the person's choice beats the country", trusted, "EUR", "UA", "uk", "", "EUR"},
		{"the country, from the proxy", trusted, "", "UA", "en", "", "UAH"},
		{"a country that pays in euro", trusted, "", "DE", "", "", "EUR"},
		{"poland", trusted, "", "PL", "", "", "PLN"},
		{"a country with no currency of ours is USD, not the language", trusted, "", "US", "uk", "", "USD"},
		{"XX is no country: the language", trusted, "", "XX", "pl", "", "PLN"},
		{"Tor is no country: the language", trusted, "", "T1", "be", "", "BYN"},
		{"a header that is not a country: the language", trusted, "", "UAH", "uk", "", "UAH"},
		{"nothing known is USD", trusted, "", "", "", "", "USD"},
		{"Russian says nothing of the country", trusted, "", "", "ru", "", "USD"},
		{"the header of a stranger is not believed", nil, "", "UA", "", "", "USD"},
		{"and the language decides instead", nil, "", "DE", "uk", "", "UAH"},
		{"the choice stands without the proxy too", nil, "BYN", "UA", "", "", "BYN"},
		{"Accept-Language when core knows none", trusted, "", "", "", "be-BY,be;q=0.9,en;q=0.8", "BYN"},
		{"core's language over Accept-Language", trusted, "", "", "pl", "uk", "PLN"},
		{"a choice that is not a currency is no choice", trusted, "GBP", "UA", "", "", "UAH"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := jobRig(t, func(o *rigOptions) { o.proxies = c.proxies })
			r.store.chosen[alice], r.store.lang[alice] = c.chosen, c.coreLng
			hdr := []string{"Content-Type", "application/json", "Idempotency-Key", "k"}
			if c.country != "" {
				hdr = append(hdr, "CF-IPCountry", c.country)
			}
			if c.accept != "" {
				hdr = append(hdr, "Accept-Language", c.accept)
			}
			resp := r.do(alice, "POST", "/api/jobs", strings.NewReader(body(testOffer, "key")), hdr...)
			m := jsonOf(t, resp)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("%d %v", resp.StatusCode, m)
			}
			est := m["estimate"].(map[string]any)
			if est["currency"] != c.want {
				t.Errorf("the price is in %v, want %s", est["currency"], c.want)
			}
			n := r.queue.last()
			switch {
			case c.want == fx.USD && n.FX != nil:
				t.Errorf("a price in dollars alone keeps no rate: %+v", n.FX)
			case c.want != fx.USD && (n.FX == nil || n.FX.Code != c.want || n.FX.PerUSD != r.store.rates[c.want]):
				t.Errorf("the job keeps the rate it was shown with: %+v", n.FX)
			}
		})
	}
}

func TestTheEstimateIsQuotedAndWritten(t *testing.T) {
	r := jobRig(t)
	r.store.chosen[alice], r.store.lang[alice] = "UAH", "uk"
	resp := r.post(alice, "/api/jobs", "k1", body(testOffer, "key"))
	m := jsonOf(t, resp)
	est := m["estimate"].(map[string]any)
	if est["usd"] != 0.04 || est["currency"] != "UAH" || est["label"] != "1,66 ₴" || est["kind"] != "exact" {
		t.Errorf("%v", est)
	}
	if got, want := est["amount"].(float64), 1.66; math.Abs(got-want) > 1e-9 {
		t.Errorf("amount %v, want %v", got, want)
	}
	if est["rate_date"] == "" || est["rate_date"] == nil {
		t.Errorf("the rate's day is shown: %v", est)
	}
	if m["status"] != "queued" || m["free"] != false || m["kind"] != "image" || m["offer"] != testOffer {
		t.Errorf("%v", m)
	}

	// In dollars, and in the person's language.
	r.store.chosen[alice], r.store.lang[alice] = "", "en"
	m = jsonOf(t, r.post(alice, "/api/jobs", "k2", body(testOffer, "key")))
	est = m["estimate"].(map[string]any)
	if est["currency"] != "USD" || est["label"] != "$0.04" || est["amount"] != 0.04 || est["rate_date"] != nil {
		t.Errorf("%v", est)
	}

	// No rate saved yet: dollars, not an error.
	r.store.chosen[alice], r.store.noRate = "PLN", true
	m = jsonOf(t, r.post(alice, "/api/jobs", "k3", body(testOffer, "key")))
	if est := m["estimate"].(map[string]any); est["currency"] != "USD" {
		t.Errorf("%v", est)
	}
	if r.queue.last().FX != nil {
		t.Error("a price shown in dollars keeps no rate")
	}

	// A broken rate lookup is not hidden as dollars.
	r.store.noRate, r.store.quoteErr = false, errBoom
	if resp := r.post(alice, "/api/jobs", "k4", body(testOffer, "key")); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("%d", resp.StatusCode)
	}
}

func TestAnEstimateSaysHowFarToTrustIt(t *testing.T) {
	for _, c := range []struct {
		kind     price.Kind
		usd, max float64
		label    string
	}{
		{price.Exact, 0.5, 0.5, "$0.50"},
		{price.About, 0.5, 0.5, "$0.50"},
		{price.From, 0.5, 0.5, "$0.50"},
		{price.UpTo, 0.5, 1.2, "$1.20"},
		{price.Unknown, 0, 0, ""},
	} {
		usd := c.usd
		st := State{EstimateUSD: &usd, Estimate: &price.Estimate{Kind: c.kind, USD: c.usd, MaxUSD: c.max}}
		est := estimateOf(st, "USD", 1, "en")
		if price.Kind(est.Kind) != c.kind || est.Label != c.label || est.MaxUSD != c.max || est.USD != c.usd {
			t.Errorf("%s: %+v", c.kind, est)
		}
	}
	// In another currency the figure still never rounds down: 0.0191 USD is
	// 0.79265 hryvnias, shown as 0,80.
	usd := 0.0191
	st := State{EstimateUSD: &usd, Estimate: &price.Estimate{Kind: price.About, USD: usd, MaxUSD: usd}}
	if est := estimateOf(st, "UAH", 41.5, "uk"); est.Label != "0,80\u00a0₴" {
		t.Errorf("%+v", est)
	}
}

func TestAHigherPriceThanShownStopsTheRun(t *testing.T) {
	r := jobRig(t)
	r.store.lang[alice] = "en"
	post := func(shown string) (int, map[string]any) {
		resp := r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key", `"shown_usd":`+shown))
		return resp.StatusCode, jsonOf(t, resp)
	}
	before := r.queue.calls()
	// $0.04 now, $0.03 shown: the person has not seen this price.
	code, m := post("0.03")
	if code != http.StatusConflict || m["error"] != codePriceChanged || r.queue.calls() != before {
		t.Fatalf("%d %v", code, m)
	}
	if e := m["estimate"].(map[string]any); e["usd"] != 0.04 || e["label"] != "$0.04" {
		t.Errorf("the new price = %v", e)
	}
	// The price shown, a lower one, and a change too small to show all run.
	for _, shown := range []string{"0.04", "0.05", "0.0399999"} {
		if code, m := post(shown); code != http.StatusAccepted {
			t.Errorf("shown %s: %d %v", shown, code, m)
		}
	}
	// No figure sent: no check, as before the composer.
	if resp := r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key")); resp.StatusCode != http.StatusAccepted {
		t.Errorf("%d", resp.StatusCode)
	}
	// A free run costs the person nothing: what was shown is not checked.
	if resp := r.post(alice, "/api/jobs", nextKey(), body("openrouter:microsoft/mai-image-2.6-flash", "free", `"shown_usd":0`)); resp.StatusCode != http.StatusAccepted {
		t.Errorf("free: %d", resp.StatusCode)
	}
	if resp := r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key", `"shown_usd":-1`)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a negative price: %d", resp.StatusCode)
	}
}

func TestAPriceShownInHryvniasIsComparedInHryvnias(t *testing.T) {
	r := jobRig(t)
	r.store.chosen[alice], r.store.lang[alice] = "UAH", "uk"
	// $0.04 is 1,66 ₴; $0.0399 is 1,6559 ₴, shown as 1,66 ₴ too.
	if resp := r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key", `"shown_usd":0.0399`)); resp.StatusCode != http.StatusAccepted {
		t.Errorf("%d", resp.StatusCode)
	}
	resp := r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key", `"shown_usd":0.039`))
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if e := m["estimate"].(map[string]any); e["currency"] != "UAH" || e["rate_date"] == nil {
		t.Errorf("%v", e)
	}
}

func TestAFreeVideoFromAStartFrameSaysSo(t *testing.T) {
	r := jobRig(t)
	r.store.offers[heygenOff] = heygen()
	resp := r.post(alice, "/api/jobs", nextKey(), body(heygenOff, "free", `"inputs":[50]`))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, jsonOf(t, resp))
	}
	n := r.queue.last()
	if fmt.Sprint(n.Settings["frame_images"]) != "[first_frame]" || n.Settings["resolution"] != "768p" {
		t.Errorf("settings = %v", n.Settings)
	}
	// Without a photo there is no frame to name.
	r.post(alice, "/api/jobs", nextKey(), body(heygenOff, "free"))
	if _, ok := r.queue.last().Settings["frame_images"]; ok {
		t.Errorf("settings = %v", r.queue.last().Settings)
	}
	// A paid run names its frames itself, as a list.
	resp = r.post(alice, "/api/jobs", nextKey(), body(heygenOff, "key", `"inputs":[50]`, `"settings":{"frame_images":["first_frame"]}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, jsonOf(t, resp))
	}
	for name, s := range map[string]string{
		"a list of numbers": `{"frame_images":[1]}`,
		"a long list":       `{"frame_images":["a","b","c","d","e","f","g","h","i"]}`,
	} {
		if resp := r.post(alice, "/api/jobs", nextKey(), body(heygenOff, "key", `"settings":`+s)); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
}

// A video model that takes no reference images gets its pictures as frames;
// too many files, or frames with references, are refused before the queue.
func TestTheFilesOfARunAreChecked(t *testing.T) {
	r := jobRig(t)
	r.conns.list = []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}
	const grok = "openrouter:x-ai/grok-imagine-video-1.5"
	g := heygen()
	g.ID, g.Offers[0].ID, g.Offers[0].ProviderModelID, g.Traits.References, g.Free = "x-ai/grok-imagine-video-1.5", grok, "x-ai/grok-imagine-video-1.5", nil, false
	r.store.offers[grok] = g
	r.store.offers[heygenOff] = heygen()
	for id := int64(60); id < 70; id++ {
		r.data.addAsset(alice, InputAsset{ID: id, ContentType: "image/jpeg", Width: 800, Height: 600})
	}

	resp := r.post(alice, "/api/jobs", nextKey(), body(grok, "key", `"inputs":[60]`))
	if resp.StatusCode != http.StatusAccepted || fmt.Sprint(r.queue.last().Settings["frame_images"]) != "[first_frame]" {
		t.Errorf("a photo to a frame-only model: %d %v", resp.StatusCode, r.queue.last().Settings)
	}
	calls := r.queue.calls()
	for name, tc := range map[string]struct{ offer, extra, want string }{
		"two photos to one frame":    {grok, `"inputs":[60,61]`, "at most 1 images"},
		"ten references":             {heygenOff, `"inputs":[60,61,62,63,64,65,66,67,68,69]`, "at most 9 images"},
		"a frame and two references": {heygenOff, `"inputs":[60,61,62],"settings":{"frame_images":["first_frame"]}`, "do not go together"},
	} {
		resp := r.post(alice, "/api/jobs", nextKey(), body(tc.offer, "key", tc.extra))
		m := jsonOf(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "wrong_inputs" || !strings.Contains(fmt.Sprint(m["message"]), tc.want) {
			t.Errorf("%s: %d %v", name, resp.StatusCode, m)
		}
	}
	if r.queue.calls() != calls {
		t.Errorf("a refused run reached the queue")
	}
}

// A kept sound goes as a reference where the offer takes sounds, priced as
// "about"; one of no known length, or to a model without sounds, is refused
// before the queue.
func TestASoundGoesAsAReference(t *testing.T) {
	r := jobRig(t)
	r.conns.list = []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}
	r.store.offers[heygenOff] = heygen()
	r.data.addAsset(alice, InputAsset{ID: 70, ContentType: "audio/wav", Bytes: 1 << 20, Seconds: 4})
	r.data.addAsset(alice, InputAsset{ID: 71, ContentType: "audio/mpeg", Bytes: 1 << 20})

	resp := r.post(alice, "/api/jobs", nextKey(), body(heygenOff, "key", `"inputs":[70]`, `"settings":{"resolution":"480p","duration":5}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a sound to heygen: %d %v", resp.StatusCode, jsonOf(t, resp))
	}
	if est := r.queue.last().Estimate; est == nil || est.Kind != price.About {
		t.Errorf("the estimate: %+v", est)
	}
	calls := r.queue.calls()
	for name, tc := range map[string]struct{ offer, extra, want string }{
		"of no known length": {heygenOff, `"inputs":[71]`, "not known"},
		"to an image model":  {testOffer, `"inputs":[70]`, "no sounds"},
	} {
		resp := r.post(alice, "/api/jobs", nextKey(), body(tc.offer, "key", tc.extra))
		m := jsonOf(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "wrong_inputs" || !strings.Contains(fmt.Sprint(m["message"]), tc.want) {
			t.Errorf("%s: %d %v", name, resp.StatusCode, m)
		}
	}
	if r.queue.calls() != calls {
		t.Errorf("a refused run reached the queue")
	}
}

// A setting the offer has no parameter for is refused with its name before
// the queue, on a key and on the allowance alike: fast on Wan 3.0 once made
// a job that only the adapter refused.
func TestASettingTheOfferLacksIsRefused(t *testing.T) {
	r := jobRig(t)
	r.store.offers[heygenOff] = heygen()
	const routes = "openrouter:test/routes"
	byRoute := image(routes, catalog.OpenRouter)
	byRoute.Offers[0].Routes[0].Params = map[string]catalog.Param{"quality": {Type: catalog.Enum, Values: []any{"low", "high"}}}
	r.store.offers[routes] = byRoute

	calls := r.queue.calls()
	for name, tc := range map[string]struct{ offer, pay, settings, setting string }{
		"fast on a video":            {heygenOff, "key", `{"fast":false}`, "fast"},
		"one the allowance lacks":    {heygenOff, "free", `{"quality":"high"}`, "quality"},
		"frames on an image":         {testOffer, "key", `{"frame_images":["first_frame"]}`, "frame_images"},
		"a known and an unknown one": {heygenOff, "key", `{"duration":5,"size":"1280x720"}`, "size"},
	} {
		resp := r.post(alice, "/api/jobs", nextKey(), body(tc.offer, tc.pay, `"settings":`+tc.settings))
		m := jsonOf(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "unknown_setting" || m["setting"] != tc.setting ||
			!strings.Contains(fmt.Sprint(m["message"]), tc.setting+" is not a setting of "+tc.offer) {
			t.Errorf("%s: %d %v", name, resp.StatusCode, m)
		}
	}
	if r.queue.calls() != calls {
		t.Errorf("a refused run reached the queue")
	}

	for name, tc := range map[string]struct{ offer, extra string }{
		"one left empty":     {heygenOff, `"settings":{"fast":null}`},
		"frames on a video":  {heygenOff, `"settings":{"frame_images":["first_frame"]},"inputs":[50]`},
		"a route's own":      {routes, `"settings":{"quality":"high"}`},
		"one of the offer's": {heygenOff, `"settings":{"resolution":"480p"}`},
	} {
		if resp := r.post(alice, "/api/jobs", nextKey(), body(tc.offer, "key", tc.extra)); resp.StatusCode != http.StatusAccepted {
			t.Errorf("%s: %d %v", name, resp.StatusCode, jsonOf(t, resp))
		}
	}
}

// withParams gives the offer of model m an aspect ratio and a resolution.
func withParams(m catalog.Model) catalog.Model {
	params := maps.Clone(m.Offers[0].Params)
	if params == nil {
		params = map[string]catalog.Param{}
	}
	params["aspect_ratio"] = catalog.Param{Type: catalog.Enum, Values: []any{"1:1", "4:3", "16:9", "9:16"}}
	params["resolution"] = catalog.Param{Type: catalog.Enum, Values: []any{"1K", "2K"}}
	m.Offers[0].Params = params
	return m
}

// A run on a person's key goes with framy's value of every setting it
// leaves out, so the run is the one priced; the photo gives the shape.
func TestARunOnAKeyGoesWithTheDefaults(t *testing.T) {
	r := jobRig(t)
	r.store.offers[testOffer] = withParams(r.store.offers[testOffer])
	r.data.addAsset(alice, InputAsset{ID: 52, ContentType: "image/jpeg", Width: 1600, Height: 1200})

	r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key"))
	if got := r.queue.last().Settings; fmt.Sprint(got) != "map[aspect_ratio:1:1 resolution:1K]" {
		t.Errorf("from text: %v", got)
	}
	r.post(alice, "/api/jobs", nextKey(), body(testOffer, "key", `"inputs":[52]`, `"settings":{"resolution":"2K"}`))
	if got := r.queue.last().Settings; fmt.Sprint(got) != "map[aspect_ratio:4:3 resolution:2K]" {
		t.Errorf("from a 4:3 photo: %v", got)
	}
	// A free run goes with its plan's settings and nothing else.
	const mai = "openrouter:microsoft/mai-image-2.6-flash"
	r.store.offers[mai] = withParams(r.store.offers[mai])
	r.post(alice, "/api/jobs", nextKey(), body(mai, "free"))
	if got := r.queue.last().Settings; fmt.Sprint(got) != "map[n:1]" {
		t.Errorf("free: %v", got)
	}
}
