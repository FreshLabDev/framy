// SPDX-License-Identifier: Apache-2.0
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	catalogfiles "github.com/FreshLabDev/framy/catalog"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// png is the smallest file http.DetectContentType calls a PNG.
var png = []byte("\x89PNG\r\n\x1a\n0000IHDR")

const testKey = "AIzaSyTESTKEY-not-a-real-one-0123456789"

// offers are the real descriptors of catalog/gemini.yaml, so a test fails
// when the catalog lists something the adapter does not know how to send.
var offers = sync.OnceValues(func() (map[string]catalog.Offer, error) {
	f, err := catalog.LoadFiles(catalogfiles.FS)
	if err != nil {
		return nil, err
	}
	out := map[string]catalog.Offer{}
	for _, o := range f.Offers {
		out[o.ID] = o
	}
	return out, nil
})

func offerOf(t *testing.T, model string) catalog.Offer {
	t.Helper()
	all, err := offers()
	if err != nil {
		t.Fatal(err)
	}
	o, ok := all[catalog.OfferID(catalog.Gemini, model)]
	if !ok {
		t.Fatalf("catalog/gemini.yaml has no %s", model)
	}
	return o
}

const (
	nano21  = "gemini-nano-banana-2.1"
	flash31 = "gemini-3.1-flash-image"
	lite31  = "gemini-3.1-flash-lite-image"
	pro3    = "gemini-3-pro-image"
)

func jobOf(t *testing.T, model string, settings map[string]any, inputs ...jobs.Input) jobs.Job {
	t.Helper()
	of := offerOf(t, model)
	return jobs.Job{
		ID: 7, UserID: 1001, OfferID: of.ID, ModelID: "google/" + model, Provider: catalog.Gemini, Kind: catalog.Image,
		Prompt: "a fox in the snow", Settings: settings, Offer: of, Inputs: inputs,
	}
}

func job(t *testing.T, settings map[string]any, inputs ...jobs.Input) jobs.Job {
	return jobOf(t, nano21, settings, inputs...)
}

// upstream is a fake Gemini that records each request and answers as told.
type upstream struct {
	t      *testing.T
	mu     sync.Mutex
	calls  int
	path   string
	body   map[string]any
	header http.Header
	answer func(w http.ResponseWriter)
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if r.Method != http.MethodPost || r.URL.Path != "/v1beta/interactions" {
		u.t.Errorf("%s %s, want POST /v1beta/interactions", r.Method, r.URL.Path)
	}
	if r.URL.RawQuery != "" {
		u.t.Errorf("query %q: a key in the URL ends up in logs", r.URL.RawQuery)
	}
	u.header = r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	u.body = nil
	if err := json.Unmarshal(raw, &u.body); err != nil {
		u.t.Errorf("request body: %v", err)
	}
	u.answer(w)
}

func reply(status int, body string, headers ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// answerWith is a finished interaction in the steps schema: an interim
// thought, then the model's output, then what the run used.
func answerWith(usageJSON string, content ...string) string {
	return `{"id":"v1_ChdhYmMxMjM","object":"interaction","status":"completed","model":"gemini-nano-banana-2.1",` +
		`"created":"2026-10-27T10:00:00Z","updated":"2026-10-27T10:00:09Z","steps":[` +
		`{"type":"thought","signature":"Eq4BCqsB","summary":[{"type":"text","text":"Planning the composition"}]},` +
		`{"type":"model_output","content":[` + strings.Join(content, ",") + `]}],"usage":` + usageJSON + `}`
}

func imageBlock(data []byte, mime string) string {
	return `{"type":"image","mime_type":"` + mime + `","data":"` + base64.StdEncoding.EncodeToString(data) + `"}`
}

func textBlock(s string) string {
	raw, _ := json.Marshal(map[string]string{"type": "text", "text": s})
	return string(raw)
}

// usage1K is a text-to-image run at 1K: a short prompt, 200 tokens of
// thinking, one 1,120-token image.
const usage1K = `{"total_input_tokens":14,"total_output_tokens":1120,"total_thought_tokens":200,"total_tool_use_tokens":0,"total_tokens":1334,` +
	`"input_tokens_by_modality":[{"modality":"text","tokens":14}],"output_tokens_by_modality":[{"modality":"image","tokens":1120}]}`

// cost1K at Nano Banana 2.1's rates: $1.50 a million in, $7.50 a million
// for thinking, $30 a million for the image.
const cost1K = 14*1.5e-6 + 200*7.5e-6 + 1120*30e-6

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func answerOK() string { return answerWith(usage1K, imageBlock(png, "image/png")) }

func serve(t *testing.T, answer func(http.ResponseWriter)) (*Provider, *upstream) {
	t.Helper()
	u := &upstream{t: t, answer: answer}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	p := New(Config{
		Base: srv.URL + "/v1beta",
		HTTP: srv.Client(),
		Key:  func(context.Context, jobs.Job) (string, error) { return testKey, nil },
	})
	return p, u
}

func classOf(t *testing.T, err error) *jobs.Error {
	t.Helper()
	var pe *jobs.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v (%T) is not classified", err, err)
	}
	return pe
}

func TestAGenerationReturnsTheImageAndItsCost(t *testing.T) {
	p, u := serve(t, reply(200, answerOK()))
	sub, err := p.Submit(context.Background(), job(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	out := sub.Outcome
	if out == nil || out.Err != nil || len(out.Results) != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if got := out.Results[0]; got.ContentType != "image/png" || string(got.Data) != string(png) {
		t.Errorf("result %q %q", got.ContentType, got.Data)
	}
	if out.CostUSD == nil || !near(*out.CostUSD, cost1K) {
		t.Errorf("cost %v, want %v: the tokens times the rates, thinking at the text rate", out.CostUSD, cost1K)
	}
	if sub.ProviderJobID != "" {
		t.Errorf("an image has no provider job id, got %q", sub.ProviderJobID)
	}

	if got := u.header.Get("x-goog-api-key"); got != testKey {
		t.Errorf("x-goog-api-key %q", got)
	}
	if got := u.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization %q: Gemini takes the key in x-goog-api-key", got)
	}
	if got := u.header.Get("Api-Revision"); got != DefaultRevision {
		t.Errorf("Api-Revision %q: the schema is pinned", got)
	}
	b := u.body
	if b["model"] != "gemini-nano-banana-2.1" {
		t.Errorf("model %v: the offer's provider model id", b["model"])
	}
	input, _ := b["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["type"] != "text" || input[0].(map[string]any)["text"] != "a fox in the snow" {
		t.Errorf("input %v", b["input"])
	}
	if store, ok := b["store"]; !ok || store != false {
		t.Errorf("store %v: Google must not keep the prompt and the photos", store)
	}
	for _, absent := range []string{"response_format", "generation_config", "tools"} {
		if _, ok := b[absent]; ok {
			t.Errorf("%s sent with no setting asking for it: %v", absent, b[absent])
		}
	}
}

func TestSettingsGoWhereGoogleTakesThem(t *testing.T) {
	p, u := serve(t, reply(200, answerOK()))
	_, err := p.Submit(context.Background(), job(t, map[string]any{
		"resolution": "2K", "aspect_ratio": "16:9", "thinking_level": "high", "google_search": true, "n": 1.0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	rf, _ := u.body["response_format"].(map[string]any)
	if rf["type"] != "image" || rf["image_size"] != "2K" || rf["aspect_ratio"] != "16:9" {
		t.Errorf("response_format %v", u.body["response_format"])
	}
	gc, _ := u.body["generation_config"].(map[string]any)
	if gc["thinking_level"] != "high" {
		t.Errorf("generation_config %v: thinking_level is what OpenRouter cannot pass", u.body["generation_config"])
	}
	tools, _ := u.body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "google_search" {
		t.Errorf("tools %v", u.body["tools"])
	}
	if _, ok := u.body["n"]; ok {
		t.Errorf("n sent: Gemini has no such field")
	}

	// Search off sends no tool; the 512 option has its own spelling.
	p, u = serve(t, reply(200, answerOK()))
	if _, err := p.Submit(context.Background(), jobOf(t, flash31, map[string]any{"google_search": false, "resolution": "512"})); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.body["tools"]; ok {
		t.Errorf("tools %v with google_search off", u.body["tools"])
	}
	rf, _ = u.body["response_format"].(map[string]any)
	if rf["image_size"] != "512px" {
		t.Errorf("image_size %v, want 512px", rf["image_size"])
	}
	if _, ok := u.body["generation_config"]; ok {
		t.Errorf("generation_config %v: no thinking_level was chosen", u.body["generation_config"])
	}
}

func TestAnEditSendsItsReferencesInline(t *testing.T) {
	var gets atomic.Int32
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		if r.Header.Get("x-goog-api-key") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("the key went to storage")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0photo"))
	}))
	defer storage.Close()

	u := &upstream{t: t, answer: reply(200, answerWith(
		`{"total_input_tokens":544,"total_output_tokens":1120,"total_thought_tokens":0,`+
			`"input_tokens_by_modality":[{"modality":"text","tokens":28},{"modality":"image","tokens":516}],`+
			`"output_tokens_by_modality":[{"modality":"image","tokens":1120}]}`,
		textBlock("Here is your edit."), imageBlock(png, "image/png")))}
	srv := httptest.NewServer(u)
	defer srv.Close()
	p := New(Config{
		Base: srv.URL + "/v1beta", HTTP: srv.Client(), Fetch: storage.Client(),
		Key: func(context.Context, jobs.Job) (string, error) { return testKey, nil },
	})
	j := job(t, nil,
		jobs.Input{ContentType: "image/jpeg", URL: storage.URL + "/in/1.jpg?X-Amz-Signature=abc"},
		jobs.Input{ContentType: "image/png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)},
	)
	sub, err := p.Submit(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 1 {
		t.Errorf("%d downloads, want 1: the data URL needs none", gets.Load())
	}
	if out := sub.Outcome; out.Err != nil || len(out.Results) != 1 {
		t.Fatalf("outcome %+v", out)
	} else if want := 28*1.5e-6 + 516*1.5e-6 + 1120*30e-6; out.CostUSD == nil || !near(*out.CostUSD, want) {
		t.Errorf("cost %v, want %v: the photos are input_image tokens", out.CostUSD, want)
	}
	input, _ := u.body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input %v, want the prompt and two images", u.body["input"])
	}
	if first := input[0].(map[string]any); first["type"] != "text" {
		t.Errorf("the prompt comes first: %v", first)
	}
	for i, want := range []struct{ mime, data string }{
		{"image/jpeg", base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0photo"))},
		{"image/png", base64.StdEncoding.EncodeToString(png)},
	} {
		b := input[i+1].(map[string]any)
		if b["type"] != "image" || b["mime_type"] != want.mime || b["data"] != want.data {
			t.Errorf("reference %d: %v", i+1, b)
		}
		if _, ok := b["uri"]; ok {
			t.Errorf("reference %d went as a link", i+1)
		}
	}
	if strings.Contains(fmt.Sprint(u.body), "X-Amz-Signature") {
		t.Errorf("the presigned link reached Google")
	}
}

func TestWhatNoOfferTakesIsRefusedBeforeAnything(t *testing.T) {
	fifteen := make([]jobs.Input, 15)
	for i := range fifteen {
		fifteen[i] = jobs.Input{ContentType: "image/jpeg", URL: "https://r2.example/x.jpg"}
	}
	empty := job(t, nil)
	empty.Prompt = " "
	noModel := job(t, nil)
	noModel.Offer.ProviderModelID = ""
	for name, j := range map[string]jobs.Job{
		"512 on Nano Banana 2.1, which has none": job(t, map[string]any{"resolution": "512"}),
		"4K on Lite":                             jobOf(t, lite31, map[string]any{"resolution": "4K"}),
		"a lower-case k":                         job(t, map[string]any{"resolution": "1k"}),
		"minimal on Pro, which has no thinking":  jobOf(t, pro3, map[string]any{"thinking_level": "minimal"}),
		"medium on 3.1 Flash":                    jobOf(t, flash31, map[string]any{"thinking_level": "medium"}),
		"an unknown level":                       job(t, map[string]any{"thinking_level": "extreme"}),
		"search on Lite, which has none":         jobOf(t, lite31, map[string]any{"google_search": true}),
		"search given as text":                   job(t, map[string]any{"google_search": "yes"}),
		"two images a run":                       job(t, map[string]any{"n": 2.0}),
		"a fractional n":                         job(t, map[string]any{"n": 1.5}),
		"a seed, which Gemini lacks":             job(t, map[string]any{"seed": 42.0}),
		"the references as a setting":            job(t, map[string]any{"input_references": 3.0}),
		"an aspect ratio nobody has":             job(t, map[string]any{"aspect_ratio": "7:3"}),
		"1:8 on Pro":                             jobOf(t, pro3, map[string]any{"aspect_ratio": "1:8"}),
		"fifteen references":                     job(t, nil, fifteen...),
		"a video on Pro, which takes none":       jobOf(t, pro3, nil, jobs.Input{ContentType: "video/mp4", URL: "https://r2.example/x.mp4"}),
		"a GIF as a reference":                   job(t, nil, jobs.Input{ContentType: "image/gif", URL: "https://r2.example/x.gif"}),
		"an empty prompt":                        empty,
		"an offer with no model id":              noModel,
	} {
		t.Run(name, func(t *testing.T) {
			var asked, fetched atomic.Int32
			storage := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fetched.Add(1) }))
			defer storage.Close()
			p, u := serve(t, reply(200, answerOK()))
			p.cfg.Fetch = storage.Client()
			p.cfg.Key = func(context.Context, jobs.Job) (string, error) { asked.Add(1); return testKey, nil }
			_, err := p.Submit(context.Background(), j)
			if pe := classOf(t, err); pe.Class != jobs.Refused {
				t.Errorf("class %s (%v), want refused", pe.Class, err)
			}
			if u.calls != 0 || fetched.Load() != 0 || asked.Load() != 0 {
				t.Errorf("%d calls, %d downloads, %d key lookups: a run framy can tell is wrong costs nothing", u.calls, fetched.Load(), asked.Load())
			}
		})
	}
}

func TestEveryDescriptorSettingIsSentOrRefusedNeverDropped(t *testing.T) {
	all, err := offers()
	if err != nil {
		t.Fatal(err)
	}
	for id, of := range all {
		if of.Provider != catalog.Gemini {
			continue
		}
		for name, param := range of.Params {
			if name == referencesParam || name == "n" {
				continue
			}
			var value any
			var inputs []jobs.Input
			switch {
			case name == framesSetting:
				value = []any{"first_frame"}
				inputs = []jobs.Input{{ContentType: "image/png", URL: "data:image/png;base64,AAAA"}}
			case param.Type == catalog.Enum:
				value = param.Values[len(param.Values)-1]
			case param.Type == catalog.Boolean:
				value = true
			default:
				t.Fatalf("%s: %s is a %s, which this test does not know", id, name, param.Type)
			}
			j := jobs.Job{Kind: of.Kind, Prompt: "x", Offer: of, Settings: map[string]any{name: value}, Inputs: inputs}
			if _, err := planFor(j); err != nil {
				t.Errorf("%s: %s=%v is in the descriptor but the adapter does not send it: %v", id, name, value, err)
			}
		}
	}
}

func TestTheKeyWithoutBillingIsNotATemporaryLimit(t *testing.T) {
	const message = "Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 0, model: gemini-nano-banana-2.1\n" +
		"Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 0, model: gemini-nano-banana-2.1"
	rpc, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": 429, "status": "RESOURCE_EXHAUSTED", "message": message,
		"details": []any{
			map[string]any{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{
				map[string]any{"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests", "quotaValue": "0"}}},
			map[string]any{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "28s"},
		},
	}})
	quotaOnly, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": 429, "status": "RESOURCE_EXHAUSTED", "message": "You exceeded your current quota.",
		"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{
			map[string]any{"quotaMetric": "x/generate_content_free_tier_requests", "quotaId": "GenerateRequestsPerDayPerProjectPerModel-FreeTier", "quotaValue": "0"}}}},
	}})
	interactions, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "quota_exceeded", "message": message}})
	for name, body := range map[string]string{
		"the Interactions error": string(interactions),
		"the RPC error":          string(rpc),
		"a quota value of 0":     string(quotaOnly),
	} {
		t.Run(name, func(t *testing.T) {
			p, u := serve(t, reply(429, body, "Retry-After", "30"))
			_, err := p.Submit(context.Background(), job(t, nil))
			pe := classOf(t, err)
			if pe.Class != jobs.Key {
				t.Fatalf("class %s (%v), want key: waiting does not give a project billing", pe.Class, err)
			}
			if pe.RetryAfter != 0 {
				t.Errorf("retry after %s", pe.RetryAfter)
			}
			if got := KeyStatusOf(err); got != "needs_billing" {
				t.Errorf("key status %q, want needs_billing", got)
			}
			// What the connection store reads, without importing this package.
			var s interface{ KeyStatus() string }
			if !errors.As(err, &s) || s.KeyStatus() != "needs_billing" {
				t.Errorf("no KeyStatus in %v", err)
			}
			var k *KeyError
			if !errors.As(err, &k) || k.HTTPStatus != 429 {
				t.Errorf("key error %+v", k)
			}
			if !strings.HasPrefix(err.Error(), "key: gemini key status needs_billing") {
				t.Errorf("the text a job row keeps: %q", err.Error())
			}
			if u.calls != 1 {
				t.Errorf("%d calls", u.calls)
			}
		})
	}
}

func TestARealRateLimitIsAWait(t *testing.T) {
	rpc, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": 429, "status": "RESOURCE_EXHAUSTED",
		"message": "You exceeded your current quota. Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_paid_tier_requests, limit: 10, model: gemini-nano-banana-2.1. Please retry in 22.5s.",
		"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "22s"}},
	}})
	for name, c := range map[string]struct {
		body    string
		headers []string
		wait    time.Duration
	}{
		"a limit above 0, with RetryInfo": {string(rpc), nil, 22 * time.Second},
		"a limit of 0.5 is not 0":         {`{"error":{"code":"rate_limit_exceeded","message":"limit: 0.5, model: x"}}`, nil, 0},
		"a limit of 100":                  {`{"error":{"code":"rate_limit_exceeded","message":"limit: 100"}}`, nil, 0},
		"Retry-After wins":                {`{"error":{"code":"too_many_requests","message":"slow down"}}`, []string{"Retry-After", "30"}, 30 * time.Second},
		"the wait is in the message":      {`{"error":{"code":"rate_limit_exceeded","message":"Please retry in 11.8s"}}`, nil, 11800 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := serve(t, reply(429, c.body, c.headers...))
			_, err := p.Submit(context.Background(), job(t, nil))
			pe := classOf(t, err)
			if pe.Class != jobs.RateLimited {
				t.Fatalf("class %s (%v), want rate_limited", pe.Class, err)
			}
			if d := pe.RetryAfter - c.wait; d < -time.Second || d > time.Second {
				t.Errorf("retry after %s, want %s", pe.RetryAfter, c.wait)
			}
			if KeyStatusOf(err) != "" {
				t.Errorf("a wait is no verdict on the key: %q", KeyStatusOf(err))
			}
		})
	}
}

func TestErrorsAreToldApart(t *testing.T) {
	soon := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	for _, c := range []struct {
		name    string
		status  int
		body    string
		headers []string
		class   jobs.Class
		key     string
		wait    time.Duration
	}{
		{"a malformed request", 400, `{"error":{"code":"invalid_request","message":"Invalid value at 'response_format.image_size'"}}`, nil, jobs.Refused, "", 0},
		{"no billing, in a precondition", 400, `{"error":{"code":"failed_precondition","message":"Gemini API free tier is not available in your country. Please enable billing on your project in Google AI Studio."}}`, nil, jobs.Key, "needs_billing", 0},
		{"no billing, in the RPC form", 400, `{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"Please enable billing on your project."}}`, nil, jobs.Key, "needs_billing", 0},
		{"a precondition that is not billing", 400, `{"error":{"code":"failed_precondition","message":"User location is not supported for the API use."}}`, nil, jobs.Refused, "", 0},
		{"a wrong key, in the RPC form", 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"API key not valid. Please pass a valid API key.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com"}]}}`, nil, jobs.Key, "invalid", 0},
		{"an expired key", 400, `{"error":{"code":"invalid_request","message":"API key expired. Please renew the API key."}}`, nil, jobs.Key, "invalid", 0},
		{"a missing or revoked key", 401, `{"error":{"code":"authentication","message":"The API key is missing, invalid, or expired."}}`, nil, jobs.Key, "invalid", 0},
		{"a spent prepaid balance", 402, `{"error":{"code":"payment_required","message":"Your Prepay credit balance is depleted."}}`, nil, jobs.Money, "no_credits", 0},
		{"a spent prepaid balance, as a 429", 429, `{"error":{"code":"quota_exceeded","message":"Your prepayment credits are depleted. Please go to AI Studio to manage your project and billing."}}`, nil, jobs.Money, "no_credits", 0},
		{"a key that may not do this", 403, `{"error":{"code":"permission_denied","message":"Your API key does not have permission for this resource."}}`, nil, jobs.Key, "", 0},
		{"a model that is gone", 404, `{"error":{"code":"model_not_found","message":"The specified model was not found."}}`, nil, jobs.Refused, "", 0},
		{"a body too large", 413, `<html>Request Entity Too Large</html>`, nil, jobs.Refused, "", 0},
		{"overloaded, wait until a date", 503, `{"error":{"code":"service_unavailable","message":"The model is overloaded."}}`, []string{"Retry-After", soon}, jobs.Unavailable, "", 90 * time.Second},
		{"overloaded, in the RPC form", 503, `{"error":{"code":503,"status":"UNAVAILABLE","message":"The model is overloaded. Please try again later."}}`, nil, jobs.Unavailable, "", 0},
		{"a proxy's page", 502, `<html>Bad Gateway</html>`, nil, jobs.Unavailable, "", 0},
		{"an internal error", 500, `{"error":{"code":"api_error","message":"An unexpected error occurred."}}`, nil, jobs.ProviderFailed, "", 0},
		{"a deadline", 504, `{"error":{"code":"deadline_exceeded","message":"The request didn't finish."}}`, nil, jobs.Timeout, "", 0},
		{"the client's cancellation", 499, `{"error":{"code":"cancelled","message":"The client cancelled the request."}}`, nil, jobs.ProviderFailed, "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(c.status, c.body, c.headers...))
			_, err := p.Submit(context.Background(), job(t, nil))
			pe := classOf(t, err)
			if pe.Class != c.class {
				t.Errorf("class %s (%v), want %s", pe.Class, err, c.class)
			}
			if got := KeyStatusOf(err); got != c.key {
				t.Errorf("key status %q, want %q", got, c.key)
			}
			if d := pe.RetryAfter - c.wait; d < -2*time.Second || d > 2*time.Second {
				t.Errorf("retry after %s, want %s", pe.RetryAfter, c.wait)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("the key is in the error: %v", err)
			}
		})
	}
}

func TestABlockOnTheWireIsARefusal(t *testing.T) {
	for _, c := range []struct {
		name, body, reason string
		status             int
	}{
		{"the image was withheld", `{"error":{"code":"image_safety","message":"Safety violations blocked image generation."}}`, "IMAGE_SAFETY", 400},
		{"prohibited content", `{"error":{"code":"prohibited_content","message":"Prohibited content guidelines blocked the request."}}`, "PROHIBITED_CONTENT", 400},
		{"a blocklisted term", `{"error":{"code":"blocklist","message":"blocked"}}`, "BLOCKLIST", 400},
		{"no image", `{"error":{"code":"no_image","message":"The model was unable to generate an image."}}`, "NO_IMAGE", 400},
		{"a block that came as a 200", `{"error":{"code":"image_other","message":"Unspecified reasons blocked image generation."}}`, "IMAGE_OTHER", 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(c.status, c.body))
			sub, err := p.Submit(context.Background(), job(t, nil))
			var pe *jobs.Error
			if c.status == 200 {
				if err != nil || sub.Outcome == nil || sub.Outcome.Err == nil {
					t.Fatalf("a refusal in a 200 is the run's outcome: %v %+v", err, sub.Outcome)
				}
				pe = sub.Outcome.Err
			} else {
				pe = classOf(t, err)
			}
			if pe.Class != jobs.Moderation {
				t.Fatalf("class %s (%v), want moderation: a block is a refusal, not a failure", pe.Class, pe)
			}
			var r *Refusal
			if !errors.As(pe, &r) || r.Reason != c.reason {
				t.Errorf("refusal %+v, want reason %s", r, c.reason)
			}
		})
	}
}

func TestARefusalInAFinishedRunIsNotAFailure(t *testing.T) {
	const usagePrompt = `{"total_input_tokens":271,"total_output_tokens":0,"total_thought_tokens":0,"input_tokens_by_modality":[{"modality":"text","tokens":271}]}`
	for _, c := range []struct {
		name, body, reason, stage string
		cost                      bool
	}{
		{"a prompt feedback block", `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"usageMetadata":{"promptTokenCount":271}}`, "PROHIBITED_CONTENT", "prompt", false},
		{"the same, in snake_case", `{"id":"v1_x","status":"failed","prompt_feedback":{"block_reason":"OTHER","block_reason_message":"Blocked, most often because of a reference image."}}`, "OTHER", "prompt", false},
		{"a candidate's finish reason", `{"candidates":[{"finishReason":"IMAGE_SAFETY","finishMessage":"The image was withheld."}],"usageMetadata":{"promptTokenCount":271}}`, "IMAGE_SAFETY", "result", false},
		{"a finish reason inside an interaction", `{"id":"v1_x","status":"completed","steps":[{"type":"model_output","finish_reason":"IMAGE_PROHIBITED_CONTENT","content":[]}],"usage":` + usagePrompt + `}`, "IMAGE_PROHIBITED_CONTENT", "result", true},
		{"an error list", `{"id":"v1_x","status":"failed","errors":[{"code":"unexpected","message":"x"},{"code":"image_safety","message":"Safety violations blocked image generation."}],"usage":` + usagePrompt + `}`, "IMAGE_SAFETY", "result", true},
		{"an error object", `{"id":"v1_x","status":"failed","error":{"code":"safety","message":"blocked"}}`, "SAFETY", "", false},
		{"the filter that says nothing", `{"candidates":[{"finishReason":"OTHER"}]}`, "OTHER", "result", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(200, c.body))
			sub, err := p.Submit(context.Background(), job(t, nil))
			if err != nil {
				t.Fatalf("a refusal is the run's outcome, not a failed call: %v", err)
			}
			out := sub.Outcome
			if out == nil || out.Err == nil || len(out.Results) != 0 {
				t.Fatalf("outcome %+v", out)
			}
			if out.Err.Class != jobs.Moderation {
				t.Fatalf("class %s (%v), want moderation", out.Err.Class, out.Err)
			}
			var r *Refusal
			if !errors.As(out.Err, &r) || r.Reason != c.reason || r.Stage != c.stage {
				t.Errorf("refusal %+v, want %s at %q", r, c.reason, c.stage)
			}
			if (out.CostUSD != nil) != c.cost {
				t.Errorf("cost %v", out.CostUSD)
			}
		})
	}
}

func TestNoImageCarriesTheModelsWords(t *testing.T) {
	const said = "I can draw that, but I need to know which fox: the red or the arctic one?"
	long := strings.Repeat("слово ", 400)
	for _, c := range []struct {
		name, body, text string
	}{
		{"a finished run with words and no picture", answerWith(usage1K, textBlock(said)), said},
		{"NO_IMAGE as a finish reason", `{"id":"v1_x","status":"completed","steps":[{"type":"model_output","content":[` + textBlock(said) + `]}],"candidates":[{"finishReason":"NO_IMAGE"}]}`, said},
		{"NO_IMAGE with the words in the finish message", `{"candidates":[{"finishReason":"NO_IMAGE","finishMessage":"` + said + `"}]}`, said},
		{"NO_IMAGE as an error with the words beside it", `{"id":"v1_x","status":"failed","steps":[{"type":"model_output","content":[` + textBlock(said) + `]}],"errors":[{"code":"no_image","message":"The model was unable to generate an image."}]}`, said},
		{"NO_IMAGE as an error object with the words beside it", `{"id":"v1_x","status":"failed","steps":[{"type":"model_output","content":[` + textBlock(said) + `]}],"error":{"code":"no_image","message":"The model was unable to generate an image."}}`, said},
		{"two paragraphs", answerWith(usage1K, textBlock("First."), textBlock("Second.")), "First.\n\nSecond."},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(200, c.body))
			sub, err := p.Submit(context.Background(), job(t, nil))
			if err != nil {
				t.Fatal(err)
			}
			out := sub.Outcome
			if out == nil || out.Err == nil || out.Err.Class != jobs.Moderation {
				t.Fatalf("outcome %+v", out)
			}
			var r *Refusal
			if !errors.As(out.Err, &r) || r.Reason != ReasonNoImage || r.Text != c.text {
				t.Fatalf("refusal %+v, want NO_IMAGE with %q", r, c.text)
			}
			if !strings.Contains(out.Err.Error(), c.text) {
				t.Errorf("the job row keeps only the error text, and %q is not in %q", c.text, out.Err.Error())
			}
		})
	}

	t.Run("a long answer is cut on a letter", func(t *testing.T) {
		p, _ := serve(t, reply(200, answerWith(usage1K, textBlock(long))))
		sub, err := p.Submit(context.Background(), job(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		var r *Refusal
		if !errors.As(sub.Outcome.Err, &r) || len(r.Text) > maxText+len("…") || !strings.HasSuffix(r.Text, "…") || !utf8Valid(r.Text) {
			t.Errorf("text of %d bytes: %.40q", len(r.Text), r.Text)
		}
	})
	t.Run("the prompt is never in the refusal", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"promptFeedback":{"blockReason":"SAFETY"}}`))
		j := job(t, nil)
		j.Prompt = "a very private prompt"
		sub, _ := p.Submit(context.Background(), j)
		if strings.Contains(sub.Outcome.Err.Error(), "private") {
			t.Errorf("the prompt is in %v", sub.Outcome.Err)
		}
	})
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestWhatIsNotARefusalFails(t *testing.T) {
	for name, body := range map[string]string{
		"out of tokens":                `{"candidates":[{"finishReason":"MAX_TOKENS"}]}`,
		"a broken function call":       `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
		"an interaction that failed":   `{"id":"v1_x","status":"failed","errors":[{"code":"api_error","message":"boom"}]}`,
		"a cancelled interaction":      `{"id":"v1_x","status":"cancelled","steps":[]}`,
		"an incomplete interaction":    `{"id":"v1_x","status":"incomplete","steps":[]}`,
		"one still in progress":        `{"id":"v1_x","status":"in_progress"}`,
		"nothing at all":               `{"id":"v1_x","status":"completed","steps":[{"type":"model_output","content":[]}]}`,
		"an image that is only a link": answerWith(usage1K, `{"type":"image","mime_type":"image/png","uri":"https://generativelanguage.googleapis.com/v1beta/files/abc"}`),
		"broken base64":                answerWith(usage1K, `{"type":"image","mime_type":"image/png","data":"%%%"}`),
		"an interim image only":        `{"id":"v1_x","status":"completed","steps":[{"type":"thought","content":[{"type":"image","mime_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(png) + `"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, u := serve(t, reply(200, body))
			sub, err := p.Submit(context.Background(), job(t, nil))
			if err != nil {
				t.Fatalf("an answer that was taken is the run's outcome: %v", err)
			}
			out := sub.Outcome
			if out == nil || out.Err == nil || out.Err.Class != jobs.ProviderFailed || len(out.Results) != 0 {
				t.Fatalf("outcome %+v: want provider, not a refusal", out)
			}
			if u.calls != 1 {
				t.Errorf("%d calls: a paid run is never sent again", u.calls)
			}
		})
	}

	t.Run("a paid run with a broken image keeps its cost", func(t *testing.T) {
		p, _ := serve(t, reply(200, answerWith(usage1K, `{"type":"image","mime_type":"image/png","data":"%%%"}`)))
		sub, _ := p.Submit(context.Background(), job(t, nil))
		if sub.Outcome.CostUSD == nil || !near(*sub.Outcome.CostUSD, cost1K) {
			t.Errorf("cost %v", sub.Outcome.CostUSD)
		}
	})
}

func TestAnErrorInA200IsReadAndNotRetried(t *testing.T) {
	for name, body := range map[string]string{
		"a rate limit after the 200": `{"id":"v1_x","status":"failed","errors":[{"code":"rate_limit_exceeded","message":"Rate limited. Please retry in 5s"}]}`,
		"overloaded after the 200":   `{"error":{"code":"service_unavailable","message":"overloaded"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := serve(t, reply(200, body))
			sub, err := p.Submit(context.Background(), job(t, nil))
			if err != nil {
				t.Fatal(err)
			}
			if out := sub.Outcome; out == nil || out.Err == nil || out.Err.Class != jobs.ProviderFailed || out.Err.RetryAfter != 0 {
				t.Errorf("outcome %+v: Google took the request, so it is not sent again", out)
			}
		})
	}
}

func TestTheAnswerIsReadCarefully(t *testing.T) {
	t.Run("no media type", func(t *testing.T) {
		p, _ := serve(t, reply(200, answerWith(usage1K, `{"type":"image","data":"`+base64.StdEncoding.EncodeToString(png)+`"}`)))
		sub, err := p.Submit(context.Background(), job(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		if got := sub.Outcome.Results[0].ContentType; got != "image/png" {
			t.Errorf("sniffed %q", got)
		}
	})
	t.Run("words beside the picture are dropped, the picture is kept", func(t *testing.T) {
		p, _ := serve(t, reply(200, answerWith(usage1K, textBlock("Here you go."), imageBlock(png, "image/jpeg"))))
		sub, err := p.Submit(context.Background(), job(t, nil))
		if err != nil || sub.Outcome.Err != nil || len(sub.Outcome.Results) != 1 || sub.Outcome.Results[0].ContentType != "image/jpeg" {
			t.Fatalf("%v %+v", err, sub.Outcome)
		}
	})
	t.Run("two images, both paid for, both returned", func(t *testing.T) {
		p, _ := serve(t, reply(200, answerWith(usage1K, imageBlock(png, "image/png"), imageBlock(png, "image/png"))))
		sub, _ := p.Submit(context.Background(), job(t, nil))
		if len(sub.Outcome.Results) != 2 {
			t.Errorf("%d results", len(sub.Outcome.Results))
		}
	})
	t.Run("a blockReason of nothing is not a block", func(t *testing.T) {
		body := strings.Replace(answerOK(), `"steps"`, `"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"},"candidates":[{"finishReason":"STOP"}],"steps"`, 1)
		p, _ := serve(t, reply(200, body))
		sub, err := p.Submit(context.Background(), job(t, nil))
		if err != nil || sub.Outcome.Err != nil || len(sub.Outcome.Results) != 1 {
			t.Fatalf("%v %+v", err, sub.Outcome)
		}
	})
	t.Run("no usage, no cost", func(t *testing.T) {
		p, _ := serve(t, reply(200, strings.Replace(answerOK(), `,"usage":`+usage1K, "", 1)))
		sub, err := p.Submit(context.Background(), job(t, nil))
		if err != nil || len(sub.Outcome.Results) != 1 || sub.Outcome.CostUSD != nil {
			t.Fatalf("%v %+v", err, sub.Outcome)
		}
	})
	t.Run("an image with no output tokens is not priced from the rest", func(t *testing.T) {
		p, _ := serve(t, reply(200, answerWith(`{"total_input_tokens":14,"input_tokens_by_modality":[{"modality":"text","tokens":14}]}`, imageBlock(png, "image/png"))))
		sub, _ := p.Submit(context.Background(), job(t, nil))
		if sub.Outcome.CostUSD != nil {
			t.Errorf("cost %v: the image was free by the usage, which cannot be", *sub.Outcome.CostUSD)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		p, u := serve(t, reply(200, `{"id":"v1","steps":[{"type":"model_output","content":[{"type":"image","data":"`))
		sub, err := p.Submit(context.Background(), job(t, nil))
		// The 200 was cut: the run may be paid, so it is not tried again.
		if err != nil || sub.Outcome == nil || sub.Outcome.Err == nil || sub.Outcome.Err.Class != jobs.ProviderFailed || u.calls != 1 {
			t.Errorf("%v %+v, %d calls", err, sub.Outcome, u.calls)
		}
	})
	t.Run("too large", func(t *testing.T) {
		u := &upstream{t: t, answer: reply(200, answerOK())}
		srv := httptest.NewServer(u)
		defer srv.Close()
		p := New(Config{Base: srv.URL + "/v1beta", MaxResponse: 64, Key: func(context.Context, jobs.Job) (string, error) { return "k", nil }})
		_, err := p.Submit(context.Background(), job(t, nil))
		if pe := classOf(t, err); pe.Class != jobs.ProviderFailed || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("%v", err)
		}
	})
}

func TestCostIsTheTokensTimesTheRates(t *testing.T) {
	prices := offerOf(t, nano21).Routes[0].Prices
	pro := offerOf(t, pro3).Routes[0].Prices
	for _, c := range []struct {
		name   string
		prices []catalog.Price
		usage  string
		want   float64 // 0 means none
	}{
		{"a 1K image", prices, usage1K, cost1K},
		{"the documented price of a 1K image, without thinking", prices,
			`{"total_input_tokens":0,"output_tokens_by_modality":[{"modality":"image","tokens":1120}]}`, 0.0336},
		{"4K on Pro at its own rates", pro,
			`{"total_input_tokens":560,"input_tokens_by_modality":[{"modality":"image","tokens":560}],"output_tokens_by_modality":[{"modality":"image","tokens":2000}]}`,
			560*2e-6 + 2000*120e-6},
		{"modalities in capitals", prices,
			`{"input_tokens_by_modality":[{"modality":"TEXT","tokens":10}],"output_tokens_by_modality":[{"modality":"IMAGE","tokens":1120},{"modality":"TEXT","tokens":50}]}`,
			10*1.5e-6 + 1120*30e-6 + 50*7.5e-6},
		{"a search's tokens are input", prices,
			`{"total_tool_use_tokens":1000,"output_tokens_by_modality":[{"modality":"image","tokens":1120}]}`, 1000*1.5e-6 + 1120*30e-6},
		{"no split: the dearer rate, so the cost is never low", prices,
			`{"total_input_tokens":100,"total_output_tokens":1120}`, 100*1.5e-6 + 1120*30e-6},
		{"a rate the descriptor lacks", []catalog.Price{{Billable: "output_image", Unit: "token", CostUSD: 30e-6}}, usage1K, 0},
		{"a rate for tokens that were not used may be missing", []catalog.Price{{Billable: "output_image", Unit: "token", CostUSD: 30e-6}},
			`{"output_tokens_by_modality":[{"modality":"image","tokens":1120}]}`, 0.0336},
	} {
		t.Run(c.name, func(t *testing.T) {
			var u usage
			if err := json.Unmarshal([]byte(c.usage), &u); err != nil {
				t.Fatal(err)
			}
			got := cost(c.prices, &u)
			switch {
			case c.want == 0 && got != nil:
				t.Errorf("cost %v, want none", *got)
			case c.want != 0 && (got == nil || !near(*got, c.want)):
				t.Errorf("cost %v, want %v", got, c.want)
			}
		})
	}
	if cost(prices, nil) != nil {
		t.Error("a cost with no usage")
	}
}

func TestNoKeyNoCall(t *testing.T) {
	for name, c := range map[string]struct {
		key   func(context.Context, jobs.Job) (string, error)
		class jobs.Class
	}{
		"no connection": {func(context.Context, jobs.Job) (string, error) {
			return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no Gemini key connected yet")}
		}, jobs.Key},
		"an empty key":      {func(context.Context, jobs.Job) (string, error) { return "", nil }, jobs.Key},
		"a broken keystore": {func(context.Context, jobs.Job) (string, error) { return "", errors.New("decrypt") }, jobs.Internal},
		"no key function":   {nil, jobs.Key},
	} {
		t.Run(name, func(t *testing.T) {
			var fetched atomic.Int32
			storage := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fetched.Add(1) }))
			defer storage.Close()
			u := &upstream{t: t, answer: reply(200, answerOK())}
			srv := httptest.NewServer(u)
			defer srv.Close()
			p := New(Config{Base: srv.URL + "/v1beta", Fetch: storage.Client(), Key: c.key})
			_, err := p.Submit(context.Background(), job(t, nil, jobs.Input{ContentType: "image/jpeg", URL: storage.URL + "/x.jpg"}))
			if pe := classOf(t, err); pe.Class != c.class {
				t.Errorf("class %s, want %s", pe.Class, c.class)
			}
			if u.calls != 0 || fetched.Load() != 0 {
				t.Errorf("%d calls and %d downloads without a key", u.calls, fetched.Load())
			}
		})
	}
}

func TestTheKeyNeverReachesAnError(t *testing.T) {
	// Google does not echo the key, but if a message ever did, it must not
	// get to the job row or the log.
	body := `{"error":{"code":"invalid_request","message":"Bad request for key ` + testKey + ` in project 1"}}`
	p, _ := serve(t, reply(400, body))
	_, err := p.Submit(context.Background(), job(t, nil))
	if strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "<key>") {
		t.Errorf("%v", err)
	}
	p, _ = serve(t, reply(200, `{"error":{"code":"safety","message":"`+testKey+`"}}`))
	sub, _ := p.Submit(context.Background(), job(t, nil))
	if strings.Contains(sub.Outcome.Err.Error(), testKey) {
		t.Errorf("%v", sub.Outcome.Err)
	}
}

func TestReferencesThatCannotBeFetched(t *testing.T) {
	big := make([]byte, 3<<10)
	for name, c := range map[string]struct {
		status  int
		body    []byte
		cfg     func(*Config)
		inputs  int
		class   jobs.Class
		message string
	}{
		"a link that expired":     {status: 403, class: jobs.Storage, inputs: 1},
		"a file that was deleted": {status: 404, class: jobs.Storage, inputs: 1},
		"storage is down":         {status: 503, class: jobs.Unavailable, inputs: 1},
		"storage is throttling":   {status: 429, class: jobs.Unavailable, inputs: 1},
		"an empty file":           {status: 200, body: nil, class: jobs.Storage, inputs: 1},
		"a file over the limit":   {status: 200, body: big, cfg: func(c *Config) { c.MaxInput = 1 << 10 }, class: jobs.Refused, inputs: 1, message: "larger than"},
		"files over the total":    {status: 200, body: big[:2<<10], cfg: func(c *Config) { c.MaxInput = 4 << 10; c.MaxInputs = 3 << 10 }, class: jobs.Refused, inputs: 2, message: "together"},
	} {
		t.Run(name, func(t *testing.T) {
			storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write(c.body)
			}))
			defer storage.Close()
			p, u := serve(t, reply(200, answerOK()))
			p.cfg.Fetch = storage.Client()
			if c.cfg != nil {
				c.cfg(&p.cfg)
			}
			var in []jobs.Input
			for range c.inputs {
				in = append(in, jobs.Input{ContentType: "image/jpeg", URL: storage.URL + "/in/1.jpg?X-Amz-Signature=sig"})
			}
			_, err := p.Submit(context.Background(), job(t, nil, in...))
			pe := classOf(t, err)
			if pe.Class != c.class {
				t.Errorf("class %s (%v), want %s", pe.Class, err, c.class)
			}
			if c.message != "" && !strings.Contains(err.Error(), c.message) {
				t.Errorf("%v", err)
			}
			if u.calls != 0 {
				t.Errorf("%d paid calls for a run that never had its references", u.calls)
			}
			if strings.Contains(err.Error(), "X-Amz-Signature") {
				t.Errorf("the presigned link is in the error: %v", err)
			}
		})
	}

	t.Run("storage that cannot be reached is safe to retry and keeps its link out of the error", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		p, u := serve(t, reply(200, answerOK()))
		p.cfg.Fetch = &http.Client{}
		_, err = p.Submit(context.Background(), job(t, nil, jobs.Input{ContentType: "image/jpeg", URL: "http://" + addr + "/in/1.jpg?X-Amz-Signature=sig"}))
		if pe := classOf(t, err); pe.Class != jobs.Unavailable || strings.Contains(err.Error(), "X-Amz-Signature") || u.calls != 0 {
			t.Errorf("class %s, calls %d: %v", pe.Class, u.calls, err)
		}
	})
	t.Run("a stop during the download is left to the queue", func(t *testing.T) {
		release := make(chan struct{})
		storage := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		defer storage.Close()
		defer close(release)
		p, _ := serve(t, reply(200, answerOK()))
		p.cfg.Fetch = storage.Client()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := p.Submit(ctx, job(t, nil, jobs.Input{ContentType: "image/jpeg", URL: storage.URL + "/x.jpg"}))
		var pe *jobs.Error
		if errors.As(err, &pe) || err == nil || ctx.Err() == nil {
			t.Errorf("%v: the queue reads the deadline itself", err)
		}
	})
}

func TestAConnectionNeverMadeIsSafeToRetry(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	p := New(Config{Base: "http://" + addr + "/v1beta", Key: func(context.Context, jobs.Job) (string, error) { return "k", nil }})
	_, err = p.Submit(context.Background(), job(t, nil))
	if pe := classOf(t, err); pe.Class != jobs.Unavailable {
		t.Errorf("class %s (%v): nothing was sent, so it may go again", pe.Class, err)
	}
}

func TestAStopDuringTheCallIsLeftToTheQueue(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter, <-chan struct{}){
		"before the answer": func(_ http.ResponseWriter, release <-chan struct{}) { <-release },
		"while the answer is read": func(w http.ResponseWriter, release <-chan struct{}) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"v1","steps":[{"type":"model_output","content":[{"type":"image","data":"`)
			w.(http.Flusher).Flush()
			<-release
		},
	} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			p, _ := serve(t, func(w http.ResponseWriter) { answer(w, release) })
			t.Cleanup(func() { close(release) })
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := p.Submit(ctx, job(t, nil))
			var pe *jobs.Error
			if errors.As(err, &pe) {
				t.Fatalf("classified as %s; the queue reads the deadline itself and calls it a timeout", pe.Class)
			}
			if err == nil || ctx.Err() == nil {
				t.Errorf("%v", err)
			}
		})
	}
}
