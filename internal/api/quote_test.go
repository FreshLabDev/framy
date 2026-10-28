// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
)

const (
	maiOffer   = "openrouter:microsoft/mai-image-2.6-flash"
	heygenOff  = "openrouter:heygen/heygen-video-1"
	refsOffer  = "openrouter:test/refs"
	discounted = "openrouter:test/discounted"
)

// heygen is the free video model: 5 s at 768p, $0.036 a second listed.
func heygen() catalog.Model {
	of := catalog.Offer{
		ID: heygenOff, ModelID: "heygen/heygen-video-1", Provider: catalog.OpenRouter, ProviderModelID: "heygen/heygen-video-1",
		Kind: catalog.Video, Name: "HeyGen",
		Params: map[string]catalog.Param{
			"resolution":   {Type: catalog.Enum, Values: []any{"480p", "768p"}},
			"duration":     {Type: catalog.Enum, Values: []any{5.0, 10.0}},
			"frame_images": {Type: catalog.Enum, Values: []any{"first_frame"}},
		},
		Routes: []catalog.Route{{SKUs: map[string]string{"duration_seconds_480p": "0.02", "duration_seconds_768p": "0.036"}, Discount: 0.5}},
	}
	return catalog.Model{ID: of.ModelID, Kind: catalog.Video, Name: "HeyGen Video", Maker: "HeyGen", Free: true, Offers: []catalog.Offer{of}}
}

// quoteRig has an image model, MAI on the allowance, one that charges for
// each reference, one at 15% off and HeyGen.
func quoteRig(t *testing.T, opts ...func(*rigOptions)) *rig {
	t.Helper()
	r := newRig(t, opts...)
	r.store.offers[testOffer] = image(testOffer, catalog.OpenRouter)
	mai := image(maiOffer, catalog.OpenRouter)
	mai.Free = true
	r.store.offers[maiOffer] = mai
	refs := image(refsOffer, catalog.OpenRouter)
	refs.Offers[0].Routes[0].Prices = append(refs.Offers[0].Routes[0].Prices, catalog.Price{Billable: "input_image", Unit: "image", CostUSD: 0.01})
	r.store.offers[refsOffer] = refs
	d := image(discounted, catalog.OpenRouter)
	d.Offers[0].Routes[0].Discount = 0.15
	r.store.offers[discounted] = d
	r.store.offers[heygenOff] = heygen()
	r.data.addAsset(alice, InputAsset{ID: 50, ContentType: "image/jpeg", Width: 1024, Height: 1024})
	r.data.addAsset(bob, InputAsset{ID: 51, ContentType: "image/jpeg", Width: 1024, Height: 1024})
	return r
}

func quoteOf(t *testing.T, r *rig, user int64, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	resp := r.do(user, http.MethodPost, "/api/quote", strings.NewReader(body), append([]string{"Content-Type", "application/json"}, hdr...)...)
	return resp.StatusCode, jsonOf(t, resp)
}

func items(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	list, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("no items: %v", m)
	}
	out := make([]map[string]any, len(list))
	for i, it := range list {
		out[i] = it.(map[string]any)
	}
	return out
}

func estimate(t *testing.T, item map[string]any) map[string]any {
	t.Helper()
	e, ok := item["estimate"].(map[string]any)
	if !ok {
		t.Fatalf("no estimate: %v", item)
	}
	return e
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTheCatalogIsForAnyone(t *testing.T) {
	r := quoteRig(t)
	r.store.syncedAt = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	m := r.store.offers[refsOffer]
	m.Traits = catalog.Traits{
		Effort: &catalog.Effort{Parameter: "quality", Values: []string{"low", "high"}},
		Price:  &catalog.PriceHints{Tokens: map[string]map[string]int{"output_image": {"*": 1000}}},
	}
	r.store.offers[refsOffer] = m

	resp := r.do(0, http.MethodGet, "/api/catalog", nil)
	out := jsonOf(t, resp)
	if resp.StatusCode != http.StatusOK || out["synced_at"] != "2026-10-28T12:00:00Z" {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	models := out["models"].([]any)
	if len(models) != 5 {
		t.Fatalf("%d models", len(models))
	}
	byID := map[string]map[string]any{}
	for _, x := range models {
		mm := x.(map[string]any)
		byID[mm["id"].(string)] = mm
	}
	refs := byID["test/refs"]
	traits := refs["traits"].(map[string]any)
	if traits["effort"] == nil {
		t.Errorf("traits = %v", traits)
	}
	if _, leaked := traits["price"]; leaked {
		t.Errorf("the price hints went out: %v", traits)
	}
	hg := byID["heygen/heygen-video-1"]
	if hg["free"] != true || hg["kind"] != "video" {
		t.Errorf("heygen = %v", hg)
	}
	of := hg["offers"].([]any)[0].(map[string]any)
	if of["id"] != heygenOff || of["discount"] != 0.5 || of["params"].(map[string]any)["resolution"] == nil {
		t.Errorf("heygen offer = %v", of)
	}
	if _, ok := of["routes"]; ok {
		t.Errorf("routes went out: %v", of)
	}
	fr := out["free"].(map[string]any)
	if fr["images"].(map[string]any)["limit"] != 5.0 || fr["videos"].(map[string]any)["window_seconds"] != float64(7*24*3600) {
		t.Errorf("free = %v", fr)
	}
	plans := fr["plans"].([]any)
	if len(plans) != len(free.Plans()) {
		t.Fatalf("plans = %v", plans)
	}
	for _, p := range plans {
		p := p.(map[string]any)
		if p["offer"] == heygenOff && (p["start_frame"] != true || p["settings"].(map[string]any)["resolution"] != "768p") {
			t.Errorf("heygen plan = %v", p)
		}
	}
}

func TestTheCatalogIsKeptForAMinute(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	r := quoteRig(t, func(o *rigOptions) { o.cfg = func(c *Config) { c.Now = clock } })
	for range 3 {
		r.do(0, http.MethodGet, "/api/catalog", nil)
		quoteOf(t, r, 0, `{"items":[{"offer":"`+testOffer+`"}]}`)
	}
	if r.store.catalogReads != 1 {
		t.Fatalf("%d reads within a minute", r.store.catalogReads)
	}
	now = now.Add(DefaultCatalogTTL + time.Second)
	r.do(0, http.MethodGet, "/api/catalog", nil)
	if r.store.catalogReads != 2 {
		t.Fatalf("%d reads after the minute", r.store.catalogReads)
	}
	// A failed read keeps nothing and is an error.
	now = now.Add(DefaultCatalogTTL + time.Second)
	r.store.catalogErr = errors.New("connection reset")
	if resp := r.do(0, http.MethodGet, "/api/catalog", nil); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("%d", resp.StatusCode)
	}
	r.store.catalogErr = nil
	if resp := r.do(0, http.MethodGet, "/api/catalog", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestAQuotePricesEveryItem(t *testing.T) {
	r := quoteRig(t)
	code, m := quoteOf(t, r, 0, fmt.Sprintf(`{"items":[
		{"offer":%q},
		{"offer":%q,"files":[{"content_type":"image/jpeg","bytes":3000000,"width":4032,"height":3024},{"content_type":"image/png","width":800,"height":600}]},
		{"offer":%q},
		{"offer":"openrouter:gone/model"},
		{"offer":%q,"settings":{"resolution":"768p","duration":5}}
	]}`, testOffer, refsOffer, discounted, heygenOff), "Accept-Language", "en")
	if code != http.StatusOK || m["currency"] != "USD" {
		t.Fatalf("%d %v", code, m)
	}
	it := items(t, m)
	if len(it) != 5 {
		t.Fatalf("%d items", len(it))
	}
	if e := estimate(t, it[0]); e["usd"] != 0.04 || e["label"] != "$0.04" || e["kind"] != "exact" || it[0]["refusal"] != nil {
		t.Errorf("plain = %v", it[0])
	}
	// Two references at $0.01 each.
	if e := estimate(t, it[1]); !near(e["usd"].(float64), 0.06) {
		t.Errorf("refs = %v", e)
	}
	// 15% off $0.04, with the listed price beside it.
	e := estimate(t, it[2])
	if !near(e["usd"].(float64), 0.034) || e["discount"] != 0.15 || e["list_usd"] != 0.04 || e["list_label"] != "$0.04" || e["label"] != "$0.034" {
		t.Errorf("discounted = %v", e)
	}
	if it[3]["estimate"] != nil || it[3]["refusal"].(map[string]any)["error"] != codeUnknownOffer {
		t.Errorf("unknown = %v", it[3])
	}
	// HeyGen at half price: $0.09 for 5 s, $0.18 listed.
	if e := estimate(t, it[4]); !near(e["usd"].(float64), 0.09) || !near(e["list_usd"].(float64), 0.18) {
		t.Errorf("heygen = %v", e)
	}
	// Nobody is signed in: no refusals, whatever the payer.
	_, m = quoteOf(t, r, 0, fmt.Sprintf(`{"items":[{"offer":%q,"pay":"key"},{"offer":%q,"pay":"free"}]}`, testOffer, testOffer))
	for _, item := range items(t, m) {
		if item["refusal"] != nil {
			t.Errorf("a refusal for nobody: %v", item)
		}
	}
}

func TestAQuoteIsInThePersonsCurrency(t *testing.T) {
	r := quoteRig(t)
	// Nobody signed in: the browser's language.
	_, m := quoteOf(t, r, 0, `{"items":[{"offer":"`+testOffer+`"}]}`, "Accept-Language", "uk-UA,uk;q=0.9")
	if m["currency"] != "UAH" || m["rate_date"] == nil {
		t.Fatalf("%v", m)
	}
	if e := estimate(t, items(t, m)[0]); e["label"] != "1,66 ₴" || e["rate_date"] == nil {
		t.Errorf("%v", e)
	}
	// A person's own pick wins.
	r.store.chosen[alice] = "EUR"
	_, m = quoteOf(t, r, alice, `{"items":[{"offer":"`+testOffer+`"}]}`, "Accept-Language", "uk")
	if m["currency"] != "EUR" {
		t.Errorf("%v", m)
	}
}

func TestAQuoteSaysWhatWouldStopTheRun(t *testing.T) {
	allow := &fakeAllowance{}
	r := quoteRig(t, withAllowance(allow))

	// No connection to the provider, then one.
	_, m := quoteOf(t, r, alice, `{"items":[{"offer":"`+testOffer+`","pay":"key"}]}`)
	ref := items(t, m)[0]["refusal"].(map[string]any)
	if ref["error"] != codeNoConnection || ref["status"] != 402.0 || ref["provider"] != "openrouter" {
		t.Errorf("no connection = %v", ref)
	}
	r.conns.list = []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}
	_, m = quoteOf(t, r, alice, `{"items":[{"offer":"`+testOffer+`","pay":"key"}]}`)
	if ref := items(t, m)[0]["refusal"]; ref != nil {
		t.Errorf("refused with a connection: %v", ref)
	}

	// A free run gets the allowance's settings and is asked about with the
	// files it would take.
	_, m = quoteOf(t, r, alice, `{"items":[{"offer":"`+heygenOff+`","pay":"free","inputs":[50]}]}`)
	item := items(t, m)[0]
	set := item["settings"].(map[string]any)
	if set["resolution"] != "768p" || set["duration"] != 5.0 || fmt.Sprint(set["frame_images"]) != "[first_frame]" || item["refusal"] != nil {
		t.Errorf("free heygen = %v", item)
	}
	if len(allow.asked) != 1 || allow.asked[0].Inputs != 1 || allow.asked[0].UserID != alice || !near(allow.asked[0].EstimateUSD, 0.09) {
		t.Errorf("asked %+v", allow.asked)
	}
	if allow.asked[0].Kind != catalog.Video {
		t.Errorf("kind %q", allow.asked[0].Kind)
	}

	// The limit, as POST /api/jobs would say it.
	frees := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	allow.check = &free.LimitError{Kind: catalog.Video, Limit: 1, Window: 7 * 24 * time.Hour, FreesAt: frees}
	_, m = quoteOf(t, r, alice, `{"items":[{"offer":"`+heygenOff+`","pay":"free"}]}`)
	ref = items(t, m)[0]["refusal"].(map[string]any)
	if ref["error"] != codeFreeLimit || ref["status"] != 429.0 || ref["frees_at"] != "2026-11-04T08:00:00Z" {
		t.Errorf("limit = %v", ref)
	}
	// An error that is no refusal is not hidden as one.
	allow.check = errors.New("connection reset")
	if code, _ := quoteOf(t, r, alice, `{"items":[{"offer":"`+heygenOff+`","pay":"free"}]}`); code != http.StatusInternalServerError {
		t.Errorf("%d", code)
	}
	allow.check = nil

	// A model off the allowance, and a file that is not hers.
	_, m = quoteOf(t, r, alice, fmt.Sprintf(`{"items":[{"offer":%q,"pay":"free"},{"offer":%q,"pay":"key","inputs":[51]}]}`, testOffer, testOffer))
	it := items(t, m)
	if it[0]["refusal"].(map[string]any)["error"] != codeNotFree || it[1]["refusal"].(map[string]any)["error"] != codeUnknownInput {
		t.Errorf("%v", it)
	}
	// Her own file counts in the price.
	_, m = quoteOf(t, r, alice, `{"items":[{"offer":"`+refsOffer+`","pay":"key","inputs":[50]}]}`)
	if e := estimate(t, items(t, m)[0]); !near(e["usd"].(float64), 0.05) {
		t.Errorf("%v", e)
	}
	// Only the free runs on a model of the allowance were asked about.
	if len(allow.asked) != 3 {
		t.Errorf("asked %d times", len(allow.asked))
	}
}

func TestAQuoteRequestIsChecked(t *testing.T) {
	r := quoteRig(t)
	many := strings.Repeat(`{"offer":"x"},`, maxQuoteItems)
	for name, body := range map[string]string{
		"no items":        `{"items":[]}`,
		"too many":        `{"items":[` + many + `{"offer":"x"}]}`,
		"no offer":        `{"items":[{}]}`,
		"bad payer":       `{"items":[{"offer":"x","pay":"card"}]}`,
		"not a file":      `{"items":[{"offer":"x","files":[{"content_type":"application/pdf"}]}]}`,
		"huge picture":    `{"items":[{"offer":"x","files":[{"content_type":"image/png","width":90000,"height":10}]}]}`,
		"negative length": `{"items":[{"offer":"x","files":[{"content_type":"video/mp4","seconds":-1}]}]}`,
		"unknown field":   `{"items":[{"offer":"x","price":0}]}`,
		"bad setting":     `{"items":[{"offer":"x","settings":{"n":{"a":1}}}]}`,
	} {
		if code, m := quoteOf(t, r, 0, body); code != http.StatusBadRequest || m["error"] != codeBadRequest {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
}

func TestAPhotoIsPricedAtTheSizeItWillHave(t *testing.T) {
	if w, h := fitLongSide(4032, 3024, 2048); w != 2048 || h != 1536 {
		t.Errorf("%dx%d", w, h)
	}
	if w, h := fitLongSide(800, 600, 2048); w != 800 || h != 600 {
		t.Errorf("%dx%d", w, h)
	}
	if w, h := fitLongSide(0, 0, 2048); w != 0 || h != 0 {
		t.Errorf("%dx%d", w, h)
	}
}

// A quote says the settings the run would go with: framy's defaults on a
// key, the shape following the first file, and the plan's alone when free.
func TestAQuoteGoesWithTheDefaults(t *testing.T) {
	r := quoteRig(t)
	r.store.offers[testOffer] = withParams(r.store.offers[testOffer])
	status, out := quoteOf(t, r, alice, `{"items":[
		{"offer":"`+testOffer+`","pay":"key"},
		{"offer":"`+testOffer+`","files":[{"content_type":"image/jpeg","width":900,"height":1600}]},
		{"offer":"`+testOffer+`","settings":{"aspect_ratio":"16:9"},"files":[{"content_type":"image/jpeg","width":900,"height":1600}]},
		{"offer":"`+heygenOff+`","pay":"free"}]}`)
	if status != http.StatusOK {
		t.Fatalf("%d %v", status, out)
	}
	got := items(t, out)
	for i, want := range []string{
		"map[aspect_ratio:1:1 resolution:1K]",
		"map[aspect_ratio:9:16 resolution:1K]",
		"map[aspect_ratio:16:9 resolution:1K]",
		"map[duration:5 resolution:768p]",
	} {
		if s := fmt.Sprint(got[i]["settings"]); s != want {
			t.Errorf("item %d settings = %s, want %s", i, s, want)
		}
	}
}

// The catalog tells the picker each model's place: its family and rank, the
// fast mode, the suggestions by situation, the note in the person's
// language, and the defaults of every offer.
func TestTheCatalogTellsThePickerWhereEachModelGoes(t *testing.T) {
	var noted string
	for _, k := range i18n.Keys() {
		if id, ok := strings.CutPrefix(k, "note."); ok {
			noted = id
			break
		}
	}
	if noted == "" {
		t.Fatal("i18n has no note")
	}
	r := quoteRig(t)
	m := withParams(image("openrouter:"+noted, catalog.OpenRouter))
	m.Traits.Family = &catalog.Family{ID: "x", Name: "X", Gen: "2", Tier: catalog.PlainTier, Label: "2", Rank: 1, OutdatedBy: "test/image"}
	m.Traits.Fast = "test/fast"
	m.Traits.Recommend = map[string]int{catalog.ForImage: 2}
	r.store.offers["openrouter:"+noted] = m
	img := r.store.offers[testOffer]
	img.Traits.Recommend = map[string]int{catalog.ForImage: 1, catalog.ForManyPhotos: 1}
	r.store.offers[testOffer] = img

	resp := r.do(0, http.MethodGet, "/api/catalog", nil, "Accept-Language", "uk-UA,uk;q=0.9")
	out := jsonOf(t, resp)
	var got map[string]any
	for _, x := range out["models"].([]any) {
		if x.(map[string]any)["id"] == noted {
			got = x.(map[string]any)
		}
	}
	if got == nil {
		t.Fatalf("no %s in %v", noted, out["models"])
	}
	if got["note"] != i18n.T("uk", "note."+noted) || got["fast"] != "test/fast" {
		t.Errorf("model = %v", got)
	}
	fam := got["family"].(map[string]any)
	if fam["id"] != "x" || fam["name"] != "X" || fam["rank"] != 1.0 || fam["outdated_by"] != "test/image" || fam["tier"] != "std" {
		t.Errorf("family = %v", fam)
	}
	of := got["offers"].([]any)[0].(map[string]any)
	if d := fmt.Sprint(of["defaults"]); d != "map[aspect_ratio:1:1 resolution:1K]" {
		t.Errorf("defaults = %s", d)
	}
	rec := out["recommend"].(map[string]any)
	if fmt.Sprint(rec) != fmt.Sprintf("map[image:[test/image %s] many_photos:[test/image] video:[] video_edit:[]]", noted) {
		t.Errorf("recommend = %v", rec)
	}
}
