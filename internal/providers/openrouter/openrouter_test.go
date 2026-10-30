// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// png is the smallest file http.DetectContentType calls a PNG.
var png = []byte("\x89PNG\r\n\x1a\n0000IHDR")

func ptr(f float64) *float64 { return &f }

// offer has two routes: "cheap" takes up to two references, n up to 4 and
// the pass-through safety_tolerance; "dear" takes 4K and up to ten
// references, one image a run.
func offer() catalog.Offer {
	shared := func() map[string]catalog.Param {
		return map[string]catalog.Param{
			"aspect_ratio": {Type: catalog.Enum, Values: []any{"1:1", "16:9"}},
			"seed":         {Type: catalog.Boolean},
		}
	}
	cheap := shared()
	cheap["resolution"] = catalog.Param{Type: catalog.Enum, Values: []any{"1K", "2K"}}
	cheap["n"] = catalog.Param{Type: catalog.Range, Min: ptr(1), Max: ptr(4)}
	cheap["input_references"] = catalog.Param{Type: catalog.Range, Min: ptr(0), Max: ptr(2)}
	cheap["safety_tolerance"] = catalog.Param{Type: catalog.Range, Min: ptr(0), Max: ptr(6), Passthrough: true}
	dear := shared()
	dear["resolution"] = catalog.Param{Type: catalog.Enum, Values: []any{"1K", "2K", "4K"}}
	dear["n"] = catalog.Param{Type: catalog.Range, Min: ptr(1), Max: ptr(1)}
	dear["input_references"] = catalog.Param{Type: catalog.Range, Min: ptr(0), Max: ptr(10)}
	return catalog.Offer{
		ID: "openrouter:test/image", ModelID: "test/image", Provider: catalog.OpenRouter, ProviderModelID: "test/image", Kind: catalog.Image,
		Params: map[string]catalog.Param{
			"aspect_ratio":     cheap["aspect_ratio"],
			"resolution":       dear["resolution"],
			"n":                cheap["n"],
			"seed":             cheap["seed"],
			"safety_tolerance": cheap["safety_tolerance"],
		},
		Routes: []catalog.Route{{Provider: "cheap", Params: cheap}, {Provider: "dear", Params: dear}},
	}
}

func job(settings map[string]any, inputs ...jobs.Input) jobs.Job {
	of := offer()
	return jobs.Job{
		ID: 7, UserID: 1001, OfferID: of.ID, ModelID: of.ModelID, Provider: of.Provider, Kind: catalog.Image,
		Prompt: "a fox in the snow", Settings: settings, Free: true, Offer: of, Inputs: inputs,
	}
}

// upstream is a fake OpenRouter that records each request and answers as
// told.
type upstream struct {
	t      *testing.T
	mu     sync.Mutex
	calls  int
	body   map[string]any
	header http.Header
	answer func(w http.ResponseWriter)
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/images" {
		u.t.Errorf("%s %s, want POST /api/v1/images", r.Method, r.URL.Path)
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

func images(cost string, files ...[]byte) string {
	var data []string
	for _, f := range files {
		data = append(data, `{"b64_json":"`+base64.StdEncoding.EncodeToString(f)+`","media_type":"image/png"}`)
	}
	return `{"created":1761555600,"data":[` + strings.Join(data, ",") + `],"usage":{"prompt_tokens":0,"completion_tokens":1024,"total_tokens":1024,"cost":` + cost + `}}`
}

func serve(t *testing.T, answer func(http.ResponseWriter)) (*Provider, *upstream) {
	t.Helper()
	u := &upstream{t: t, answer: answer}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	p := New(Config{
		Base:  srv.URL + "/api/v1",
		HTTP:  srv.Client(),
		Title: "framy",
		Key:   func(context.Context, jobs.Job) (string, error) { return "sk-or-v1-test", nil },
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
	p, u := serve(t, reply(200, images("0.0195", png)))
	sub, err := p.Submit(context.Background(), job(nil))
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
	if out.CostUSD == nil || *out.CostUSD != 0.0195 {
		t.Errorf("cost %v, want 0.0195", out.CostUSD)
	}
	if sub.ProviderJobID != "" {
		t.Errorf("an image has no provider job id, got %q", sub.ProviderJobID)
	}

	if got := u.header.Get("Authorization"); got != "Bearer sk-or-v1-test" {
		t.Errorf("authorization %q", got)
	}
	if got := u.header.Get("X-Title"); got != "framy" {
		t.Errorf("X-Title %q", got)
	}
	b := u.body
	if b["model"] != "test/image" || b["prompt"] != "a fox in the snow" {
		t.Errorf("model and prompt: %v", b)
	}
	if b["user"] != jobs.SafetyID(1001) || strings.Contains(b["user"].(string), "1001") {
		t.Errorf("user %v: a hash of the person's id, never the id", b["user"])
	}
	prefs := b["provider"].(map[string]any)
	if prefs["sort"] != "price" {
		t.Errorf("provider %v: the cheapest route goes first, as the estimate assumes", prefs)
	}
	if _, pinned := prefs["only"]; pinned {
		t.Errorf("provider %v: every route takes a plain run, so none is left out", prefs)
	}
	if _, refs := b["input_references"]; refs {
		t.Errorf("a run without inputs sent references: %v", b)
	}
}

func TestAnEditSendsItsReferences(t *testing.T) {
	p, u := serve(t, reply(200, images("0.06", png, png)))
	sub, err := p.Submit(context.Background(), job(
		map[string]any{"resolution": "2K", "n": 2.0, "aspect_ratio": "16:9"},
		jobs.Input{ContentType: "image/jpeg", URL: "https://r2.example/in/1.jpg?X-Amz-Signature=abc"},
		jobs.Input{ContentType: "image/png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)},
	))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(sub.Outcome.Results); n != 2 {
		t.Fatalf("%d results, want 2", n)
	}
	refs, _ := u.body["input_references"].([]any)
	if len(refs) != 2 {
		t.Fatalf("input_references %v", u.body["input_references"])
	}
	for i, want := range []string{"https://r2.example/in/1.jpg?X-Amz-Signature=abc", "data:image/png;base64,"} {
		ref := refs[i].(map[string]any)
		url, _ := ref["image_url"].(map[string]any)["url"].(string)
		if ref["type"] != "image_url" || !strings.HasPrefix(url, want) {
			t.Errorf("reference %d: %v", i+1, ref)
		}
	}
	if u.body["resolution"] != "2K" || u.body["aspect_ratio"] != "16:9" {
		t.Errorf("settings: %v", u.body)
	}
	// n goes as a whole number, and only "cheap" makes two images a run.
	if n, ok := u.body["n"].(float64); !ok || n != 2 {
		t.Errorf("n %v", u.body["n"])
	}
	prefs := u.body["provider"].(map[string]any)
	if only, _ := prefs["only"].([]any); len(only) != 1 || only[0] != "cheap" {
		t.Errorf("provider %v, want only [cheap]", prefs)
	}
}

func TestAPassThroughSettingGoesToTheRoutesOptions(t *testing.T) {
	p, u := serve(t, reply(200, images("0.01", png)))
	if _, err := p.Submit(context.Background(), job(map[string]any{"safety_tolerance": 2.0, "seed": 42.0})); err != nil {
		t.Fatal(err)
	}
	if _, top := u.body["safety_tolerance"]; top {
		t.Errorf("a pass-through setting went as a field: %v", u.body)
	}
	if u.body["seed"] != 42.0 {
		t.Errorf("seed %v: OpenRouter lists it as a boolean, but it takes a number", u.body["seed"])
	}
	prefs := u.body["provider"].(map[string]any)
	opts, _ := prefs["options"].(map[string]any)
	cheap, _ := opts["cheap"].(map[string]any)
	if cheap["safety_tolerance"] != 2.0 || len(opts) != 1 {
		t.Errorf("options %v, want cheap.safety_tolerance 2", opts)
	}
	if only, _ := prefs["only"].([]any); len(only) != 1 || only[0] != "cheap" {
		t.Errorf("provider %v: only cheap takes safety_tolerance", prefs)
	}
}

func TestManyReferencesGoWhereTheyFit(t *testing.T) {
	p, u := serve(t, reply(200, images("0.03", png)))
	var in []jobs.Input
	for range 3 {
		in = append(in, jobs.Input{ContentType: "image/jpeg", URL: "https://r2.example/x.jpg"})
	}
	if _, err := p.Submit(context.Background(), job(nil, in...)); err != nil {
		t.Fatal(err)
	}
	prefs := u.body["provider"].(map[string]any)
	if only, _ := prefs["only"].([]any); len(only) != 1 || only[0] != "dear" {
		t.Errorf("provider %v: three references fit only dear", prefs)
	}
}

func TestWhatNoRouteTakesIsRefusedBeforeTheCall(t *testing.T) {
	many := make([]jobs.Input, 11)
	for i := range many {
		many[i] = jobs.Input{ContentType: "image/jpeg", URL: "https://r2.example/x.jpg"}
	}
	for name, j := range map[string]jobs.Job{
		"a resolution nobody has":               job(map[string]any{"resolution": "8K"}),
		"4K and two images":                     job(map[string]any{"resolution": "4K", "n": 2.0}),
		"a fractional seed":                     job(map[string]any{"seed": 1.5}),
		"a seed given as text":                  job(map[string]any{"seed": "42"}),
		"a setting the model lacks":             job(map[string]any{"style": "photo"}),
		"a reserved field":                      job(map[string]any{"model": "openai/gpt-image-2"}),
		"eleven references":                     job(nil, many...),
		"a video as a reference":                job(nil, jobs.Input{ContentType: "video/mp4", URL: "https://r2.example/x.mp4"}),
		"an empty prompt":                       func() jobs.Job { j := job(nil); j.Prompt = " "; return j }(),
		"an offer the catalog has no route for": func() jobs.Job { j := job(nil); j.Offer.Routes = nil; return j }(),
	} {
		t.Run(name, func(t *testing.T) {
			p, u := serve(t, reply(200, images("0.01", png)))
			_, err := p.Submit(context.Background(), j)
			if pe := classOf(t, err); pe.Class != jobs.Invalid {
				t.Errorf("class %s (%v), want invalid_request", pe.Class, err)
			}
			if u.calls != 0 {
				t.Errorf("%d calls: a run framy can tell is wrong costs nothing", u.calls)
			}
		})
	}
}

func TestAModeratedPromptIsARefusal(t *testing.T) {
	const flagged = "the exact words of the prompt"
	for name, body := range map[string]string{
		"flagged by OpenRouter's moderation": `{"error":{"code":403,"message":"openai/gpt-image-2 requires moderation on OpenAI. Your input was flagged for \"sexual\". No credits were charged.","metadata":{"reasons":["sexual"],"flagged_input":"` + flagged + `","provider_name":"OpenAI","model_slug":"openai/gpt-image-2"}}}`,
		"blocked by the provider's filter":   `{"error":{"code":403,"message":"Request blocked by safety filters","metadata":{"error_type":"content_policy_violation","provider_name":"Google AI Studio"}}}`,
		"refused by the model":               `{"error":{"code":403,"message":"The model refused","metadata":{"error_type":"refusal"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := serve(t, reply(403, body))
			_, err := p.Submit(context.Background(), job(nil))
			pe := classOf(t, err)
			if pe.Class != jobs.Moderation {
				t.Fatalf("class %s (%v), want moderation", pe.Class, err)
			}
			if strings.Contains(err.Error(), flagged) {
				t.Errorf("the error carries the flagged text: %v", err)
			}
			var api *APIError
			if !errors.As(err, &api) || !api.Moderated() {
				t.Errorf("no moderation details in %v", err)
			}
		})
	}

	p, _ := serve(t, reply(403, `{"error":{"code":403,"message":"flagged","metadata":{"reasons":["violence","hate"],"flagged_input":"`+flagged+`","provider_name":"OpenAI"}}}`))
	_, err := p.Submit(context.Background(), job(nil))
	var api *APIError
	if !errors.As(err, &api) || len(api.Reasons) != 2 || api.Reasons[0] != "violence" || !api.InputFlagged || api.Provider != "OpenAI" {
		t.Errorf("details %+v", api)
	}
}

// OpenRouter passes OpenAI's own filter on as a bare 400 (live, 27 October
// 2026), and only its words tell it from a setting OpenAI does not take.
func TestABare400FromTheSafetySystemIsARefusal(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       jobs.Class
	}{
		{"OpenAI's safety system", `{"error":{"code":400,"message":"Your request was rejected by the safety system. If you believe this is an error, contact us at help.openai.com and include the request ID req_1.","metadata":{"provider_name":"OpenAI"}}}`, jobs.Moderation},
		{"OpenAI's moderation code", `{"error":{"code":400,"message":"moderation_blocked: Your request was rejected","metadata":{"provider_name":"OpenAI"}}}`, jobs.Moderation},
		{"a size OpenAI does not take", `{"error":{"code":400,"message":"Invalid value: '9x9'. Supported values are: '1024x1024'.","metadata":{"provider_name":"OpenAI"}}}`, jobs.Refused},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(400, c.body))
			_, err := p.Submit(context.Background(), job(nil))
			if pe := classOf(t, err); pe.Class != c.want {
				t.Errorf("class %s (%v), want %s", pe.Class, err, c.want)
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
		wait    time.Duration
	}{
		{"no credits", 402, `{"error":{"code":402,"message":"Insufficient credits","metadata":{"limit_source":"openrouter_credits"}}}`, nil, jobs.Money, 0},
		{"the in-flight budget, with a wait", 402, `{"error":{"code":402,"message":"In-flight budget exceeded","metadata":{"limit_source":"openrouter_in_flight_budget"}}}`, []string{"Retry-After", "7"}, jobs.RateLimited, 7 * time.Second},
		{"the in-flight budget, without one", 402, `{"error":{"code":402,"message":"In-flight budget exceeded","metadata":{"limit_source":"openrouter_in_flight_budget"}}}`, nil, jobs.Money, 0},
		{"the key's limit", 403, `{"error":{"code":403,"message":"Key limit exceeded","metadata":{"limit_source":"openrouter_key_limit","limit_usd":5,"spend_usd":5}}}`, nil, jobs.Money, 0},
		{"a guardrail", 403, `{"error":{"code":403,"message":"Model not allowed by guardrail","metadata":{"error_type":"permission_denied"}}}`, nil, jobs.Key, 0},
		{"a revoked key", 401, `{"error":{"code":401,"message":"User not found.","metadata":{"error_type":"authentication"}}}`, nil, jobs.Key, 0},
		{"a bad setting", 400, `{"error":{"code":400,"message":"Invalid aspect_ratio","metadata":{"error_type":"invalid_request"}}}`, nil, jobs.Refused, 0},
		{"a reference it cannot fetch", 400, `{"error":{"code":400,"message":"Could not download image","metadata":{"error_type":"image_download_failed"}}}`, nil, jobs.Refused, 0},
		{"a model that is gone", 404, `{"error":{"code":404,"message":"Model not found"}}`, nil, jobs.Refused, 0},
		{"a body too large", 413, `<html>Request Entity Too Large</html>`, nil, jobs.Refused, 0},
		{"a rate limit", 429, `{"error":{"code":429,"message":"Rate limit exceeded","metadata":{"error_type":"rate_limit_exceeded"}}}`, []string{"Retry-After", "30"}, jobs.RateLimited, 30 * time.Second},
		{"a provider that is down", 502, `{"error":{"code":502,"message":"Provider returned error","metadata":{"error_type":"provider_unavailable"}}}`, nil, jobs.Unavailable, 0},
		{"no provider, wait until a date", 503, `{"error":{"code":503,"message":"No available provider"}}`, []string{"Retry-After", soon}, jobs.Unavailable, 90 * time.Second},
		{"overloaded", 529, `{"error":{"code":529,"message":"Provider overloaded"}}`, nil, jobs.Unavailable, 0},
		{"an internal error", 500, `{"error":{"code":500,"message":"Internal Server Error","metadata":{"error_type":"server"}}}`, nil, jobs.ProviderFailed, 0},
		{"Cloudflare's timeout", 524, `<html>A timeout occurred</html>`, nil, jobs.Timeout, 0},
		{"a request timeout", 408, `{"error":{"code":408,"message":"Request timed out"}}`, nil, jobs.Timeout, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(c.status, c.body, c.headers...))
			_, err := p.Submit(context.Background(), job(nil))
			pe := classOf(t, err)
			if pe.Class != c.class {
				t.Errorf("class %s (%v), want %s", pe.Class, err, c.class)
			}
			if d := pe.RetryAfter - c.wait; d < -2*time.Second || d > 2*time.Second {
				t.Errorf("retry after %s, want %s", pe.RetryAfter, c.wait)
			}
			if strings.Contains(err.Error(), "sk-or-v1-test") {
				t.Errorf("the key is in the error: %v", err)
			}
		})
	}
}

func TestAnErrorInA200IsReadAndNotRetried(t *testing.T) {
	for _, c := range []struct {
		name  string
		body  string
		class jobs.Class
		cost  *float64
	}{
		{"every provider failed after the 200", `{"error":{"code":502,"message":"Provider returned error","metadata":{"error_type":"provider_unavailable"}},"usage":{"cost":0.01}}`, jobs.ProviderFailed, ptr(0.01)},
		{"the result was flagged", `{"error":{"code":403,"message":"Output blocked","metadata":{"error_type":"content_policy_violation"}}}`, jobs.Moderation, nil},
		{"a rate limit after the 200", `{"error":{"code":"429","message":"Rate limited"}}`, jobs.ProviderFailed, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(200, c.body))
			sub, err := p.Submit(context.Background(), job(nil))
			if err != nil {
				t.Fatalf("an error in the body is the run's outcome, not a failed call: %v", err)
			}
			out := sub.Outcome
			if out == nil || out.Err == nil || out.Err.Class != c.class {
				t.Fatalf("outcome %+v, want the class %s", out, c.class)
			}
			if out.Err.RetryAfter != 0 || len(out.Results) != 0 {
				t.Errorf("outcome %+v", out)
			}
			if (c.cost == nil) != (out.CostUSD == nil) || (c.cost != nil && *c.cost != *out.CostUSD) {
				t.Errorf("cost %v, want %v", out.CostUSD, c.cost)
			}
		})
	}
}

func TestTheAnswerIsReadCarefully(t *testing.T) {
	t.Run("no media type", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(png)+`"}]}`))
		sub, err := p.Submit(context.Background(), job(nil))
		if err != nil {
			t.Fatal(err)
		}
		if got := sub.Outcome.Results[0].ContentType; got != "image/png" {
			t.Errorf("sniffed %q", got)
		}
		if sub.Outcome.CostUSD != nil {
			t.Errorf("no usage, yet a cost %v", *sub.Outcome.CostUSD)
		}
	})
	t.Run("a data URL", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"created":1,"data":[{"b64_json":"data:image/webp;base64,`+base64.StdEncoding.EncodeToString(png)+`"}]}`))
		sub, err := p.Submit(context.Background(), job(nil))
		if err != nil {
			t.Fatal(err)
		}
		if got := sub.Outcome.Results[0]; got.ContentType != "image/webp" || string(got.Data) != string(png) {
			t.Errorf("result %q %q", got.ContentType, got.Data)
		}
	})
	t.Run("broken base64", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"created":1,"data":[{"b64_json":"%%%"}],"usage":{"cost":0.02}}`))
		sub, err := p.Submit(context.Background(), job(nil))
		if err != nil {
			t.Fatal(err)
		}
		if out := sub.Outcome; out.Err == nil || out.Err.Class != jobs.ProviderFailed || out.CostUSD == nil {
			t.Errorf("outcome %+v: a paid run with a broken image fails and keeps its cost", out)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"created":1,"data":[{"b64_json":"`))
		_, err := p.Submit(context.Background(), job(nil))
		if pe := classOf(t, err); pe.Class != jobs.ProviderFailed {
			t.Errorf("class %s: a cut answer may have been paid, so it is not tried again", pe.Class)
		}
	})
	t.Run("too large", func(t *testing.T) {
		u := &upstream{t: t, answer: reply(200, images("0.01", png, png, png))}
		srv := httptest.NewServer(u)
		defer srv.Close()
		p := New(Config{Base: srv.URL + "/api/v1", MaxResponse: 64, Key: func(context.Context, jobs.Job) (string, error) { return "k", nil }})
		_, err := p.Submit(context.Background(), job(nil))
		if pe := classOf(t, err); pe.Class != jobs.ProviderFailed || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("%v", err)
		}
	})
}

func TestNoKeyNoCall(t *testing.T) {
	for name, c := range map[string]struct {
		key   func(context.Context, jobs.Job) (string, error)
		class jobs.Class
	}{
		"no connection": {func(context.Context, jobs.Job) (string, error) {
			return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no connection")}
		}, jobs.Key},
		"an empty key":      {func(context.Context, jobs.Job) (string, error) { return "", nil }, jobs.Key},
		"a broken keystore": {func(context.Context, jobs.Job) (string, error) { return "", errors.New("decrypt") }, jobs.Internal},
		"no key function":   {nil, jobs.Key},
	} {
		t.Run(name, func(t *testing.T) {
			u := &upstream{t: t, answer: reply(200, images("0.01", png))}
			srv := httptest.NewServer(u)
			defer srv.Close()
			p := New(Config{Base: srv.URL + "/api/v1", Key: c.key})
			_, err := p.Submit(context.Background(), job(nil))
			if pe := classOf(t, err); pe.Class != c.class {
				t.Errorf("class %s, want %s", pe.Class, c.class)
			}
			if u.calls != 0 {
				t.Errorf("%d calls without a key", u.calls)
			}
		})
	}
}

func TestAConnectionNeverMadeIsSafeToRetry(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	p := New(Config{Base: "http://" + addr + "/api/v1", Key: func(context.Context, jobs.Job) (string, error) { return "k", nil }})
	_, err = p.Submit(context.Background(), job(nil))
	if pe := classOf(t, err); pe.Class != jobs.Unavailable {
		t.Errorf("class %s (%v): nothing was sent, so it may go again", pe.Class, err)
	}
}

func TestAStopDuringTheCallIsLeftToTheQueue(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter, <-chan struct{}){
		"before the answer": func(_ http.ResponseWriter, release <-chan struct{}) { <-release },
		"while the answer is read": func(w http.ResponseWriter, release <-chan struct{}) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"`)
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
			_, err := p.Submit(ctx, job(nil))
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

// An image comes back from Submit, so nothing ever polls one: if the queue did,
// it would be a bug, and the answer says so without a call.
func TestAnImageIsNeverPolled(t *testing.T) {
	p, u := serve(t, reply(200, "{}"))
	_, err := p.Poll(context.Background(), job(nil))
	if pe := classOf(t, err); pe.Class != jobs.NoAdapter || u.calls != 0 {
		t.Errorf("class %s, %d calls", pe.Class, u.calls)
	}
}

// Audio and anything else that is not an image or a video has no adapter.
func TestOtherKindsHaveNoAdapter(t *testing.T) {
	p, u := serve(t, reply(200, "{}"))
	j := job(nil)
	j.Kind = catalog.Kind("audio")
	_, err := p.Submit(context.Background(), j)
	if pe := classOf(t, err); pe.Class != jobs.NoAdapter || u.calls != 0 {
		t.Errorf("class %s, %d calls", pe.Class, u.calls)
	}
}
