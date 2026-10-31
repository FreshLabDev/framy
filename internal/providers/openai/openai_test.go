// SPDX-License-Identifier: Apache-2.0
package openai

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	catalogfiles "github.com/FreshLabDev/framy/catalog"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
)

// png is the smallest file http.DetectContentType calls a PNG.
var png = []byte("\x89PNG\r\n\x1a\n0000IHDR")

// A person whose id must never appear in a request.
const person = int64(7770042)

const testKey = "sk-proj-TESTKEY0123456789abcdef"

const flare = "openai:gpt-image-2.5-flare"

var (
	catOnce sync.Once
	catSnap catalog.Snapshot
	catErr  error
)

// realOffer is an offer of the catalog as the service builds it from
// catalog/openai.yaml and the overlay, with no OpenRouter in it.
func realOffer(t *testing.T, id string) (catalog.Model, catalog.Offer) {
	t.Helper()
	catOnce.Do(func() {
		var files catalog.Files
		if files, catErr = catalog.LoadFiles(catalogfiles.FS); catErr != nil {
			return
		}
		catSnap = catalog.Build(catalog.Upstream{}, files, time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC))
	})
	if catErr != nil {
		t.Fatal(catErr)
	}
	for _, m := range catSnap.Models {
		for _, of := range m.Offers {
			if of.ID == id {
				return m, of
			}
		}
	}
	t.Fatalf("offer %s is not in the catalog", id)
	return catalog.Model{}, catalog.Offer{}
}

// job is a run of offer id with the settings a composer would send.
func jobOf(t *testing.T, id string, settings map[string]any, inputs ...jobs.Input) jobs.Job {
	t.Helper()
	m, of := realOffer(t, id)
	return jobs.Job{
		ID: 7, UserID: person, OfferID: of.ID, ModelID: m.ID, Provider: of.Provider, Kind: catalog.Image,
		Prompt: "a fox in the snow", Settings: settings, Offer: of, Inputs: inputs,
	}
}

func job(t *testing.T, settings map[string]any, inputs ...jobs.Input) jobs.Job {
	t.Helper()
	return jobOf(t, flare, settings, inputs...)
}

// medium is what the composer sends for a plain run.
func medium() map[string]any {
	return map[string]any{"size": "1024x1024", "quality": "medium"}
}

// upstream is a fake OpenAI that records each request and answers as told.
// It holds every request to the rules that must hold whatever the test is
// about: a concrete quality and size, the hash and not the id, and the key in
// the header alone.
type upstream struct {
	t      *testing.T
	mu     sync.Mutex
	calls  int
	path   string
	body   map[string]any
	raw    string
	header http.Header
	answer func(w http.ResponseWriter)
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	u.path = r.URL.Path
	if r.Method != http.MethodPost || (u.path != "/v1/images/generations" && u.path != "/v1/images/edits") {
		u.t.Errorf("%s %s, want POST /v1/images/generations or /v1/images/edits", r.Method, u.path)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		u.t.Errorf("content type %q", ct)
	}
	u.header = r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	u.raw = string(raw)
	u.body = nil
	if err := json.Unmarshal(raw, &u.body); err != nil {
		u.t.Errorf("request body: %v", err)
	}
	if q, _ := u.body["quality"].(string); !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, q) {
		u.t.Errorf("quality %v: a paid request names a level, never auto and never nothing", u.body["quality"])
	}
	if s, _ := u.body["size"].(string); !strings.Contains(s, "x") {
		u.t.Errorf("size %v: a paid request names a size", u.body["size"])
	}
	if u.body["safety_identifier"] != jobs.SafetyID(person) {
		u.t.Errorf("safety_identifier %v, want the hash %s", u.body["safety_identifier"], jobs.SafetyID(person))
	}
	if strings.Contains(u.raw, strconv.FormatInt(person, 10)) {
		u.t.Errorf("the person's id is in the request: %s", u.raw)
	}
	if strings.Contains(u.raw, testKey) {
		u.t.Errorf("the key is in the request body")
	}
	if _, old := u.body["user"]; old {
		u.t.Errorf("user is deprecated; safety_identifier replaces it")
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

// images is the answer of the Images API for a GPT image model, with the
// usage as it reports it: text and image tokens in, image tokens out.
func images(text, image, out int, files ...[]byte) string {
	var data []string
	for _, f := range files {
		data = append(data, `{"b64_json":"`+base64.StdEncoding.EncodeToString(f)+`"}`)
	}
	return fmt.Sprintf(`{"created":1761555600,"background":"opaque","output_format":"png","quality":"medium","size":"1024x1024","data":[%s],`+
		`"usage":{"total_tokens":%d,"input_tokens":%d,"output_tokens":%d,"input_tokens_details":{"text_tokens":%d,"image_tokens":%d}}}`,
		strings.Join(data, ","), text+image+out, text+image, out, text, image)
}

func serve(t *testing.T, answer func(http.ResponseWriter)) (*Provider, *upstream) {
	t.Helper()
	u := &upstream{t: t, answer: answer}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	p := New(Config{
		Base:      srv.URL + "/v1",
		HTTP:      srv.Client(),
		UserAgent: "framy-test",
		Key:       func(context.Context, jobs.Job) (string, error) { return testKey, nil },
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

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// within says whether got is no further than the share of want from it.
func within(got, want, share float64) bool { return math.Abs(got-want) <= share*want }

func TestAGenerationReturnsTheImageAndTheCostFromItsTokens(t *testing.T) {
	// 439 tokens is what OpenAI's formula gives for 2.5 at medium, 1024x1024.
	p, u := serve(t, reply(200, images(12, 0, 439, png)))
	sub, err := p.Submit(context.Background(), job(t, medium()))
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
	// 12 text tokens at $5 per million and 439 image tokens at $30.
	if want := 12*5e-6 + 439*30e-6; out.CostUSD == nil || !near(*out.CostUSD, want) {
		t.Errorf("cost %v, want %v", out.CostUSD, want)
	}
	if sub.ProviderJobID != "" {
		t.Errorf("an image has no provider job id, got %q", sub.ProviderJobID)
	}

	if u.path != "/v1/images/generations" {
		t.Errorf("path %s: a run with no references is a generation", u.path)
	}
	if got := u.header.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("authorization %q", got)
	}
	if got := u.header.Get("User-Agent"); got != "framy-test" {
		t.Errorf("user agent %q", got)
	}
	b := u.body
	if b["model"] != "gpt-image-2.5-flare" || b["prompt"] != "a fox in the snow" {
		t.Errorf("model and prompt: %v", b)
	}
	if b["quality"] != "medium" || b["size"] != "1024x1024" {
		t.Errorf("settings: %v", b)
	}
	for _, name := range []string{"images", "n", "response_format", "moderation", "stream", "input_fidelity"} {
		if _, sent := b[name]; sent {
			t.Errorf("%s was sent: %v", name, b)
		}
	}
}

func TestAnEditSendsItsReferencesAsLinks(t *testing.T) {
	// A second reference as a data URL, as the live test sends it.
	data := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	link := "https://acct.r2.cloudflarestorage.com/framy/u/1/in/a.jpg?X-Amz-Signature=abc"
	p, u := serve(t, reply(200, images(20, 1360, 439, png, png)))
	sub, err := p.Submit(context.Background(), job(t,
		map[string]any{"size": "1536x1024", "quality": "high", "n": 2.0},
		jobs.Input{ContentType: "image/jpeg", URL: link},
		jobs.Input{ContentType: "image/png", URL: data},
	))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(sub.Outcome.Results); n != 2 {
		t.Fatalf("%d results, want 2", n)
	}
	// The references' tokens are billed at the image-input rate.
	if want := 20*5e-6 + 1360*8e-6 + 439*30e-6; !near(*sub.Outcome.CostUSD, want) {
		t.Errorf("cost %v, want %v", *sub.Outcome.CostUSD, want)
	}
	if u.path != "/v1/images/edits" {
		t.Errorf("path %s: references make it an edit", u.path)
	}
	refs, _ := u.body["images"].([]any)
	if len(refs) != 2 {
		t.Fatalf("images %v", u.body["images"])
	}
	for i, want := range []string{link, data} {
		ref, _ := refs[i].(map[string]any)
		if ref["image_url"] != want || len(ref) != 1 {
			t.Errorf("reference %d: %v", i+1, ref)
		}
	}
	if u.body["quality"] != "high" || u.body["size"] != "1536x1024" {
		t.Errorf("settings: %v", u.body)
	}
	// n goes as a whole number, not as 2.0.
	if !strings.Contains(u.raw, `"n":2,`) && !strings.Contains(u.raw, `"n":2}`) {
		t.Errorf("n is not a whole number in %s", u.raw)
	}
}

func TestEverySizeOfTheRulesGoesNotOnlyTheCatalogsList(t *testing.T) {
	for _, size := range []string{"1024x1024", "1536x1024", "1024x1536", "2048x2048", "3840x2160", "2160x3840", "1280x720", "3072x1024", "1024x3072", "800x832", "2560x1440"} {
		t.Run(size, func(t *testing.T) {
			p, u := serve(t, reply(200, images(10, 0, 100, png)))
			if _, err := p.Submit(context.Background(), job(t, map[string]any{"size": size, "quality": "low"})); err != nil {
				t.Fatal(err)
			}
			if u.body["size"] != size {
				t.Errorf("size %v, want %s", u.body["size"], size)
			}
		})
	}
}

func TestEveryQualityOfTheModelGoes(t *testing.T) {
	for id, levels := range map[string][]string{
		"openai:gpt-image-2.5-sunburst": {"low", "medium", "high", "xhigh", "max"},
		"openai:gpt-image-2.5-flare":    {"low", "medium", "high", "xhigh", "max"},
		"openai:gpt-image-2":            {"low", "medium", "high"},
	} {
		for _, q := range levels {
			t.Run(id+" "+q, func(t *testing.T) {
				p, u := serve(t, reply(200, images(10, 0, 100, png)))
				if _, err := p.Submit(context.Background(), jobOf(t, id, map[string]any{"size": "1024x1024", "quality": q})); err != nil {
					t.Fatal(err)
				}
				if u.body["quality"] != q || u.body["model"] != strings.TrimPrefix(id, "openai:") {
					t.Errorf("body %v", u.body)
				}
			})
		}
	}
}

func TestTheOtherSettingsGoAsFields(t *testing.T) {
	p, u := serve(t, reply(200, images(10, 0, 100, png)))
	_, err := p.Submit(context.Background(), job(t, map[string]any{
		"size": "1024x1024", "quality": "low", "output_format": "webp", "output_compression": 80.0, "background": "transparent", "n": 3.0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	b := u.body
	if b["output_format"] != "webp" || b["background"] != "transparent" || b["output_compression"] != 80.0 || b["n"] != 3.0 {
		t.Errorf("body %v", b)
	}
}

func TestANoSizeIsTheOneTheEstimateAssumes(t *testing.T) {
	p, u := serve(t, reply(200, images(10, 0, 100, png)))
	if _, err := p.Submit(context.Background(), job(t, map[string]any{"quality": "low"})); err != nil {
		t.Fatal(err)
	}
	if u.body["size"] != "1024x1024" {
		t.Errorf("size %v: left out, OpenAI would choose auto", u.body["size"])
	}
}

func TestWhatFramyCanTellIsWrongIsRefusedBeforeTheCall(t *testing.T) {
	withAuto := func(id string) jobs.Job {
		j := jobOf(t, id, map[string]any{"size": "1024x1024", "quality": "auto"})
		// A catalog that lists auto by mistake must not get it sent.
		p := j.Offer.Params["quality"]
		p.Values = append(slices.Clone(p.Values), "auto")
		j.Offer.Params = map[string]catalog.Param{"quality": p, "size": j.Offer.Params["size"]}
		return j
	}
	many := make([]jobs.Input, 17)
	for i := range many {
		many[i] = jobs.Input{ContentType: "image/jpeg", URL: "https://r2.example/x.jpg"}
	}
	long := job(t, medium())
	long.Prompt = strings.Repeat("я", maxPrompt+1)
	empty := job(t, medium())
	empty.Prompt = " \n"
	noRoute := job(t, medium())
	noRoute.Offer.Routes = nil
	noText := job(t, medium())
	noText.Offer.Routes = []catalog.Route{{Prices: []catalog.Price{{Billable: "output_image", Unit: "token", CostUSD: 3e-5}}}}
	noOutput := job(t, medium())
	noOutput.Offer.Routes = []catalog.Route{{Prices: []catalog.Price{{Billable: "input_text", Unit: "token", CostUSD: 5e-6}}}}
	noImageRate := job(t, medium(), jobs.Input{ContentType: "image/png", URL: "https://r2.example/x.png"})
	noImageRate.Offer.Routes = []catalog.Route{{Prices: []catalog.Price{
		{Billable: "input_text", Unit: "token", CostUSD: 5e-6}, {Billable: "output_image", Unit: "token", CostUSD: 3e-5}}}}
	noModel := job(t, medium())
	noModel.Offer.ProviderModelID = ""
	huge := "data:image/png;base64," + strings.Repeat("A", (maxInputBytes/3+2)*4)

	for name, j := range map[string]jobs.Job{
		"no quality: OpenAI would pick auto":           job(t, map[string]any{"size": "1024x1024"}),
		"quality auto":                                 job(t, map[string]any{"size": "1024x1024", "quality": "auto"}),
		"quality auto, when the catalog lists it":      withAuto(flare),
		"quality auto on the older model":              withAuto("openai:gpt-image-2"),
		"xhigh on gpt-image-2":                         jobOf(t, "openai:gpt-image-2", map[string]any{"size": "1024x1024", "quality": "xhigh"}),
		"a quality nobody has":                         job(t, map[string]any{"size": "1024x1024", "quality": "ultra"}),
		"size auto":                                    job(t, map[string]any{"size": "auto", "quality": "low"}),
		"a size that is not WxH":                       job(t, map[string]any{"size": "big", "quality": "low"}),
		"a size given as a number":                     job(t, map[string]any{"size": 1024, "quality": "low"}),
		"a side that is not a multiple of 16":          job(t, map[string]any{"size": "1000x1000", "quality": "low"}),
		"a side past 3840":                             job(t, map[string]any{"size": "4096x1024", "quality": "low"}),
		"a ratio past 3 to 1":                          job(t, map[string]any{"size": "3840x1024", "quality": "low"}),
		"too few pixels":                               job(t, map[string]any{"size": "512x512", "quality": "low"}),
		"too many pixels":                              job(t, map[string]any{"size": "3840x2400", "quality": "low"}),
		"eleven images":                                job(t, map[string]any{"size": "1024x1024", "quality": "low", "n": 11.0}),
		"a fractional n":                               job(t, map[string]any{"size": "1024x1024", "quality": "low", "n": 1.5}),
		"a transparent background in JPEG":             job(t, map[string]any{"size": "1024x1024", "quality": "low", "background": "transparent", "output_format": "jpeg"}),
		"a compression of a PNG":                       job(t, map[string]any{"size": "1024x1024", "quality": "low", "output_compression": 50.0}),
		"a compression past 100":                       job(t, map[string]any{"size": "1024x1024", "quality": "low", "output_format": "jpeg", "output_compression": 101.0}),
		"a format nobody has":                          job(t, map[string]any{"size": "1024x1024", "quality": "low", "output_format": "gif"}),
		"a setting the model lacks":                    job(t, map[string]any{"size": "1024x1024", "quality": "low", "style": "vivid"}),
		"a setting of OpenRouter's":                    job(t, map[string]any{"size": "1024x1024", "quality": "low", "aspect_ratio": "16:9"}),
		"a reserved field: the model":                  job(t, map[string]any{"size": "1024x1024", "quality": "low", "model": "gpt-image-1"}),
		"a reserved field: the safety identifier":      job(t, map[string]any{"size": "1024x1024", "quality": "low", "safety_identifier": "me"}),
		"a reserved field: the moderation":             job(t, map[string]any{"size": "1024x1024", "quality": "low", "moderation": "low"}),
		"a reserved field: the mask":                   job(t, map[string]any{"size": "1024x1024", "quality": "low", "mask": "x"}),
		"seventeen references":                         job(t, medium(), many...),
		"a video as a reference":                       job(t, medium(), jobs.Input{ContentType: "video/mp4", URL: "https://r2.example/x.mp4"}),
		"a GIF as a reference":                         job(t, medium(), jobs.Input{ContentType: "image/gif", URL: "https://r2.example/x.gif"}),
		"a reference by a plain http link":             job(t, medium(), jobs.Input{ContentType: "image/png", URL: "http://r2.example/x.png"}),
		"a reference that is a file path":              job(t, medium(), jobs.Input{ContentType: "image/png", URL: "file:///etc/passwd"}),
		"a data URL that is not base64":                job(t, medium(), jobs.Input{ContentType: "image/png", URL: "data:image/png,abc"}),
		"a data URL past 50 MiB":                       job(t, medium(), jobs.Input{ContentType: "image/png", URL: huge}),
		"a prompt past 32,000 characters":              long,
		"an empty prompt":                              empty,
		"an offer the catalog has no route for":        noRoute,
		"an offer with no input text price":            noText,
		"an offer with no output image price":          noOutput,
		"an edit of an offer with no image-input rate": noImageRate,
		"an offer with no model id":                    noModel,
	} {
		t.Run(name, func(t *testing.T) {
			p, u := serve(t, reply(200, images(10, 0, 100, png)))
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

func TestAReferenceWithNoLinkIsAStorageFailure(t *testing.T) {
	p, u := serve(t, reply(200, images(10, 0, 100, png)))
	_, err := p.Submit(context.Background(), job(t, medium(), jobs.Input{ContentType: "image/png"}))
	if pe := classOf(t, err); pe.Class != jobs.Storage || u.calls != 0 {
		t.Errorf("class %s, %d calls", pe.Class, u.calls)
	}
}

func TestAModeratedRunIsARefusalNotAFailure(t *testing.T) {
	const flagged = "the exact words of the prompt"
	const msg = "Your request was rejected by the safety system. If you believe this is an error, contact us at help.openai.com and include the request ID req_8a1c."
	for name, c := range map[string]struct {
		body       string
		stage      string
		categories []string
	}{
		"details inside the error": {
			`{"error":{"code":"moderation_blocked","message":"` + msg + `","param":null,"type":"image_generation_user_error","moderation_details":{"moderation_stage":"input","categories":["sexual"]}}}`,
			"input", []string{"sexual"},
		},
		"details beside the error": {
			`{"error":{"code":"moderation_blocked","message":"` + msg + `","param":null,"type":"image_generation_user_error"},"moderation_details":{"moderation_stage":"output","categories":["violence","hate"]}}`,
			"output", []string{"violence", "hate"},
		},
		"the stage inside the error alone": {
			`{"error":{"code":"moderation_blocked","message":"` + msg + `","type":"image_generation_user_error","moderation_stage":"output"}}`,
			"output", nil,
		},
		"categories as flags": {
			`{"error":{"code":"moderation_blocked","message":"` + msg + `","type":"image_generation_user_error","moderation_details":{"moderation_stage":"input","categories":{"sexual":true,"violence":false,"self-harm":true}}}}`,
			"input", []string{"self-harm", "sexual"},
		},
		"the code alone": {
			`{"error":{"code":"moderation_blocked","message":"` + msg + `","type":"image_generation_user_error"}}`,
			"unknown", nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := serve(t, reply(400, c.body, "X-Request-Id", "req_8a1c"))
			j := job(t, medium())
			j.Prompt = flagged
			_, err := p.Submit(context.Background(), j)
			pe := classOf(t, err)
			if pe.Class != jobs.Moderation {
				t.Fatalf("class %s (%v), want moderation: the person's run is refused, not failed", pe.Class, err)
			}
			if strings.Contains(err.Error(), flagged) {
				t.Errorf("the error carries the prompt: %v", err)
			}
			var api *APIError
			if !errors.As(err, &api) || !api.Moderated() || api.Stage != c.stage || !slices.Equal(api.Categories, c.categories) {
				t.Fatalf("details %+v, want stage %s and %v", api, c.stage, c.categories)
			}
			if !strings.Contains(err.Error(), c.stage+" stage") || !strings.Contains(err.Error(), "req_8a1c") {
				t.Errorf("the reason lacks its stage or the request id: %v", err)
			}
			if pe.RetryAfter != 0 {
				t.Errorf("retry after %s: a refusal is never retried", pe.RetryAfter)
			}
		})
	}
}

func TestOtherUserErrorsOfTheImageEndpointAreNotModeration(t *testing.T) {
	p, _ := serve(t, reply(400, `{"error":{"code":"invalid_image","message":"The image could not be read.","type":"image_generation_user_error"}}`))
	_, err := p.Submit(context.Background(), job(t, medium()))
	if pe := classOf(t, err); pe.Class != jobs.Refused {
		t.Errorf("class %s (%v), want refused", pe.Class, err)
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
		{"a wrong key", 401, `{"error":{"message":"Incorrect API key provided: ` + testKey + `. You can find your API key at https://platform.openai.com/account/api-keys.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, nil, jobs.Key, 0},
		{"a wrong key, echoed in OpenAI's masked form", 401, `{"error":{"message":"Incorrect API key provided: sk-proj-********************cdef.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, nil, jobs.Key, 0},
		{"an IP the key does not allow", 401, `{"error":{"message":"Incorrect IP address.","type":"invalid_request_error","code":"invalid_ip"}}`, nil, jobs.Key, 0},
		{"an organization that is not verified", 403, `{"error":{"message":"Your organization must be verified to use the model ` + "`gpt-image-2.5-sunburst`" + `. Please go to: https://platform.openai.com/settings/organization/general and click on Verify Organization.","type":"invalid_request_error","param":null,"code":null}}`, nil, jobs.Key, 0},
		{"a project without the model", 403, `{"error":{"message":"Project ` + "`proj_abc`" + ` does not have access to model ` + "`gpt-image-2.5-sunburst`" + `","type":"invalid_request_error","param":null,"code":"model_not_found"}}`, nil, jobs.Key, 0},
		{"a key without the scope", 403, `{"error":{"message":"You have insufficient permissions for this operation. Missing scopes: api.model.request.","type":"invalid_request_error","code":null}}`, nil, jobs.Key, 0},
		{"a model the key may not use", 404, `{"error":{"message":"The model ` + "`gpt-image-2.5-flare`" + ` does not exist or you do not have access to it.","type":"invalid_request_error","param":null,"code":"model_not_found"}}`, nil, jobs.Key, 0},
		{"a region OpenAI does not serve", 403, `{"error":{"code":"unsupported_country_region_territory","message":"Country, region, or territory not supported","param":null,"type":"request_forbidden"}}`, nil, jobs.ProviderFailed, 0},
		{"no credit", 429, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`, nil, jobs.Money, 0},
		{"no credit, by the code", 429, `{"error":{"message":"Your credit balance is too low.","type":"invalid_request_error","code":"credit_balance_exhausted"}}`, nil, jobs.Money, 0},
		{"the organization's spend limit", 429, `{"error":{"message":"You have reached your organization's monthly spend limit.","type":"invalid_request_error","code":"organization_spend_limit_exceeded"}}`, []string{"Retry-After", "60"}, jobs.Money, 0},
		{"the project's spend limit", 429, `{"error":{"message":"project limit","type":"invalid_request_error","code":"project_spend_limit_exceeded"}}`, nil, jobs.Money, 0},
		{"the organization's usage limit", 429, `{"error":{"message":"usage limit","type":"invalid_request_error","code":"organization_usage_limit_exceeded"}}`, nil, jobs.Money, 0},
		{"the billing hard limit", 400, `{"error":{"message":"Billing hard limit has been reached","type":"billing_limit_user_error","code":"billing_hard_limit_reached"}}`, nil, jobs.Money, 0},
		{"a rate limit", 429, `{"error":{"message":"Rate limit reached for gpt-image-2.5-flare in organization org-x on images per minute: Limit 20, Used 20, Requested 1.","type":"requests","param":null,"code":"rate_limit_exceeded"}}`, []string{"Retry-After", "20"}, jobs.RateLimited, 20 * time.Second},
		{"slow down", 429, `{"error":{"message":"Slow down","type":"requests","code":"slow_down"}}`, nil, jobs.RateLimited, 0},
		{"an engine that is overloaded", 503, `{"error":{"message":"The engine is currently overloaded, please try again later","type":"server_error","param":null,"code":null}}`, []string{"Retry-After", soon}, jobs.Unavailable, 90 * time.Second},
		{"a bad setting", 400, `{"error":{"message":"Invalid value: 'ultra'. Supported values are: 'low', 'medium', 'high'.","type":"invalid_request_error","param":"quality","code":"invalid_value"}}`, nil, jobs.Refused, 0},
		{"a reference it cannot fetch", 400, `{"error":{"message":"Error while downloading the image.","type":"invalid_request_error","param":"images[0].image_url","code":"invalid_image_url"}}`, nil, jobs.Refused, 0},
		{"a body too large", 413, `<html>Request Entity Too Large</html>`, nil, jobs.Refused, 0},
		{"a body it cannot process", 422, `{"error":{"message":"unprocessable","type":"invalid_request_error"}}`, nil, jobs.Refused, 0},
		{"an internal error", 500, `{"error":{"message":"The server had an error while processing your request. Sorry about that!","type":"server_error","param":null,"code":null}}`, nil, jobs.ProviderFailed, 0},
		{"a bad gateway", 502, `<html>Bad gateway</html>`, nil, jobs.ProviderFailed, 0},
		{"a gateway timeout", 504, `<html>Gateway Time-out</html>`, nil, jobs.Timeout, 0},
		{"Cloudflare's timeout", 524, `<html>A timeout occurred</html>`, nil, jobs.Timeout, 0},
		{"a request timeout", 408, `{"error":{"message":"Request timed out","type":"timeout"}}`, nil, jobs.Timeout, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(c.status, c.body, c.headers...))
			_, err := p.Submit(context.Background(), job(t, medium()))
			pe := classOf(t, err)
			if pe.Class != c.class {
				t.Errorf("class %s (%v), want %s", pe.Class, err, c.class)
			}
			if d := pe.RetryAfter - c.wait; d < -2*time.Second || d > 2*time.Second {
				t.Errorf("retry after %s, want %s", pe.RetryAfter, c.wait)
			}
			for _, secret := range []string{testKey, "sk-proj"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("a key is in the error: %v", err)
				}
			}
		})
	}
}

func TestAnUnverifiedOrganizationIsToldFromOtherForbiddens(t *testing.T) {
	for body, want := range map[string]bool{
		`{"error":{"message":"Your organization must be verified to use the model ` + "`gpt-image-2`" + `.","type":"invalid_request_error"}}`: true,
		`{"error":{"message":"Project does not have access to model","code":"model_not_found"}}`:                                              false,
	} {
		p, _ := serve(t, reply(403, body))
		_, err := p.Submit(context.Background(), job(t, medium()))
		var api *APIError
		if !errors.As(err, &api) || api.NeedsVerification() != want {
			t.Errorf("%s: %+v, want verification %v", body, api, want)
		}
	}
}

// What an answer proves about the key reaches the connection's status
// through the interface internal/keys reads, without an import of it.
func TestTheKeyStatusAnAnswerProves(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`, "invalid"},
		{403, `{"error":{"message":"Your organization must be verified to use the model.","type":"invalid_request_error"}}`, "needs_verification"},
		{403, `{"error":{"message":"Project does not have access to model","code":"model_not_found"}}`, ""},
		{429, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`, ""},
		{400, `{"error":{"message":"Invalid size","type":"invalid_request_error"}}`, ""},
	} {
		p, _ := serve(t, reply(tc.status, tc.body))
		_, err := p.Submit(context.Background(), job(t, medium()))
		var hint interface{ KeyStatus() string }
		if !errors.As(err, &hint) || hint.KeyStatus() != tc.want {
			t.Errorf("%d %s: %v, want status %q", tc.status, tc.body, err, tc.want)
		}
	}
}

func TestOnlyARefusalBeforeTheRunIsRetried(t *testing.T) {
	retried := map[jobs.Class]bool{jobs.RateLimited: true, jobs.Unavailable: true}
	for status, body := range map[int]string{
		400: `{"error":{"code":"moderation_blocked","message":"x","type":"image_generation_user_error"}}`,
		401: `{"error":{"code":"invalid_api_key","message":"x"}}`,
		429: `{"error":{"code":"insufficient_quota","message":"x"}}`,
		500: `{"error":{"message":"x","type":"server_error"}}`,
		502: `bad gateway`,
		504: `timeout`,
	} {
		p, _ := serve(t, reply(status, body))
		_, err := p.Submit(context.Background(), job(t, medium()))
		if pe := classOf(t, err); retried[pe.Class] {
			t.Errorf("%d is %s: retried, though the run may have started", status, pe.Class)
		}
	}
}

func TestAnErrorInA200IsReadAndNotRetried(t *testing.T) {
	for _, c := range []struct {
		name  string
		body  string
		class jobs.Class
		cost  *float64
	}{
		{"the result was blocked", `{"error":{"code":"moderation_blocked","message":"blocked","type":"image_generation_user_error","moderation_details":{"moderation_stage":"output","categories":["violence"]}}}`, jobs.Moderation, nil},
		{"a failure after the 200, with its tokens", `{"error":{"message":"The server had an error","type":"server_error"},"usage":{"input_tokens":10,"output_tokens":100,"input_tokens_details":{"text_tokens":10,"image_tokens":0}}}`, jobs.ProviderFailed, func() *float64 { v := 10*5e-6 + 100*30e-6; return &v }()},
		{"a rate limit after the 200", `{"error":{"code":"rate_limit_exceeded","message":"Rate limited","type":"requests"}}`, jobs.ProviderFailed, nil},
		{"an engine that is overloaded after the 200", `{"error":{"message":"overloaded","type":"server_error"}}`, jobs.ProviderFailed, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _ := serve(t, reply(200, c.body))
			sub, err := p.Submit(context.Background(), job(t, medium()))
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
			if (c.cost == nil) != (out.CostUSD == nil) || (c.cost != nil && !near(*c.cost, *out.CostUSD)) {
				t.Errorf("cost %v, want %v", out.CostUSD, c.cost)
			}
		})
	}
}

func TestTheAnswerIsReadCarefully(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01")
	t.Run("a JPEG is named by its bytes", func(t *testing.T) {
		p, u := serve(t, reply(200, strings.Replace(images(10, 0, 100, jpeg), `"output_format":"png"`, `"output_format":"jpeg"`, 1)))
		sub, err := p.Submit(context.Background(), job(t, map[string]any{"size": "1024x1024", "quality": "low", "output_format": "jpeg"}))
		if err != nil {
			t.Fatal(err)
		}
		if got := sub.Outcome.Results[0]; got.ContentType != "image/jpeg" || string(got.Data) != string(jpeg) {
			t.Errorf("result %q %q", got.ContentType, got.Data)
		}
		if u.body["output_format"] != "jpeg" {
			t.Errorf("body %v", u.body)
		}
	})
	t.Run("bytes that say nothing take the answer's format", func(t *testing.T) {
		p, _ := serve(t, reply(200, strings.Replace(images(10, 0, 100, []byte("not a picture")), `"output_format":"png"`, `"output_format":"webp"`, 1)))
		sub, err := p.Submit(context.Background(), job(t, medium()))
		if err != nil {
			t.Fatal(err)
		}
		if got := sub.Outcome.Results[0].ContentType; got != "image/webp" {
			t.Errorf("type %q", got)
		}
	})
	t.Run("no format anywhere is a PNG", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString([]byte("not a picture"))+`"}]}`))
		sub, err := p.Submit(context.Background(), job(t, medium()))
		if err != nil {
			t.Fatal(err)
		}
		if got := sub.Outcome.Results[0].ContentType; got != "image/png" {
			t.Errorf("type %q", got)
		}
		if sub.Outcome.CostUSD != nil {
			t.Errorf("no usage, yet a cost %v", *sub.Outcome.CostUSD)
		}
	})
	t.Run("several images", func(t *testing.T) {
		p, _ := serve(t, reply(200, images(10, 0, 300, png, png, png)))
		sub, err := p.Submit(context.Background(), job(t, map[string]any{"size": "1024x1024", "quality": "low", "n": 3.0}))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(sub.Outcome.Results); n != 3 {
			t.Errorf("%d results, want 3", n)
		}
	})
	t.Run("broken base64 keeps the cost of a paid run", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"created":1,"data":[{"b64_json":"%%%"}],"usage":{"input_tokens":5,"output_tokens":100,"input_tokens_details":{"text_tokens":5,"image_tokens":0}}}`))
		sub, err := p.Submit(context.Background(), job(t, medium()))
		if err != nil {
			t.Fatal(err)
		}
		if out := sub.Outcome; out.Err == nil || out.Err.Class != jobs.ProviderFailed || out.CostUSD == nil {
			t.Errorf("outcome %+v: a paid run with a broken image fails and keeps its cost", out)
		}
	})
	t.Run("an answer with no image keeps the cost", func(t *testing.T) {
		p, _ := serve(t, reply(200, images(5, 0, 100)))
		sub, err := p.Submit(context.Background(), job(t, medium()))
		if err != nil {
			t.Fatal(err)
		}
		if out := sub.Outcome; len(out.Results) != 0 || out.Err != nil || out.CostUSD == nil {
			t.Errorf("outcome %+v: the queue ends it as no result, with the cost", out)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		p, _ := serve(t, reply(200, `{"created":1,"data":[{"b64_json":"`))
		_, err := p.Submit(context.Background(), job(t, medium()))
		if pe := classOf(t, err); pe.Class != jobs.ProviderFailed {
			t.Errorf("class %s: a cut answer may have been paid, so it is not tried again", pe.Class)
		}
	})
	t.Run("too large", func(t *testing.T) {
		u := &upstream{t: t, answer: reply(200, images(10, 0, 100, png, png, png))}
		srv := httptest.NewServer(u)
		defer srv.Close()
		p := New(Config{Base: srv.URL + "/v1", MaxResponse: 64, Key: func(context.Context, jobs.Job) (string, error) { return testKey, nil }})
		_, err := p.Submit(context.Background(), job(t, medium()))
		if pe := classOf(t, err); pe.Class != jobs.ProviderFailed || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("%v", err)
		}
	})
}

func TestTheCostIsEachKindOfTokenAtItsOwnRate(t *testing.T) {
	_, of := realOffer(t, flare)
	r, err := ratesOf(of, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.text != 5e-6 || r.image != 8e-6 || r.output != 30e-6 {
		t.Fatalf("rates %+v: the catalog says $5, $8 and $30 per million", r)
	}
	type d = struct {
		TextTokens  int64 `json:"text_tokens"`
		ImageTokens int64 `json:"image_tokens"`
	}
	for _, c := range []struct {
		name string
		u    usage
		want float64
	}{
		{"text in", usage{InputTokens: 1000, InputTokensDetails: &d{TextTokens: 1000}}, 1000 * 5e-6},
		{"image in", usage{InputTokens: 1000, InputTokensDetails: &d{ImageTokens: 1000}}, 1000 * 8e-6},
		{"image out", usage{OutputTokens: 1000}, 1000 * 30e-6},
		{"all three", usage{InputTokens: 1500, OutputTokens: 439, InputTokensDetails: &d{TextTokens: 100, ImageTokens: 1400}}, 100*5e-6 + 1400*8e-6 + 439*30e-6},
		{"input with no details is the dearer rate", usage{InputTokens: 1000}, 1000 * 8e-6},
		{"input the details miss is the dearer rate", usage{InputTokens: 1000, InputTokensDetails: &d{TextTokens: 100, ImageTokens: 600}}, 100*5e-6 + 600*8e-6 + 300*8e-6},
		{"nothing", usage{}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := r.cost(c.u); got == nil || !near(*got, c.want) {
				t.Errorf("cost %v, want %v", got, c.want)
			}
		})
	}
	if got := *r.cost(usage{OutputTokens: 439}); got != 0.01317 {
		t.Errorf("cost %v: binary noise left in", got)
	}
}

func TestTheCostFromTheTokensMatchesTheEstimate(t *testing.T) {
	// The tokens OpenAI's formula gives for a 1024x1024 image: the estimate
	// is made from them, and the actual cost from the usage of the answer.
	for _, c := range []struct {
		id      string
		quality string
		tokens  int
	}{
		{"openai:gpt-image-2.5-flare", "low", 196},
		{"openai:gpt-image-2.5-flare", "medium", 439},
		{"openai:gpt-image-2.5-sunburst", "high", 1756},
		{"openai:gpt-image-2.5-sunburst", "max", 7024},
		{"openai:gpt-image-2", "medium", 1756},
	} {
		t.Run(c.id+" "+c.quality, func(t *testing.T) {
			settings := map[string]any{"size": "1024x1024", "quality": c.quality}
			j := jobOf(t, c.id, settings)
			m, of := realOffer(t, c.id)
			est := price.Of(m, of, price.Request{Settings: settings, Prompt: j.Prompt})
			// The prompt is 17 characters; a token is about 4 of them.
			p, _ := serve(t, reply(200, images(5, 0, c.tokens, png)))
			sub, err := p.Submit(context.Background(), j)
			if err != nil {
				t.Fatal(err)
			}
			got := *sub.Outcome.CostUSD
			if !within(got, est.USD, 0.2) {
				t.Errorf("cost %.6f, estimate %.6f: more than 20%% apart", got, est.USD)
			}
		})
	}
}

func TestNoKeyNoCall(t *testing.T) {
	for name, c := range map[string]struct {
		key   func(context.Context, jobs.Job) (string, error)
		class jobs.Class
	}{
		"no connection": {func(context.Context, jobs.Job) (string, error) {
			return "", &jobs.Error{Class: jobs.Key, Err: errors.New("no OpenAI key connected yet")}
		}, jobs.Key},
		"an empty key":      {func(context.Context, jobs.Job) (string, error) { return "", nil }, jobs.Key},
		"a broken keystore": {func(context.Context, jobs.Job) (string, error) { return "", errors.New("decrypt") }, jobs.Internal},
		"no key function":   {nil, jobs.Key},
	} {
		t.Run(name, func(t *testing.T) {
			u := &upstream{t: t, answer: reply(200, images(10, 0, 100, png))}
			srv := httptest.NewServer(u)
			defer srv.Close()
			p := New(Config{Base: srv.URL + "/v1", Key: c.key})
			_, err := p.Submit(context.Background(), job(t, medium()))
			if pe := classOf(t, err); pe.Class != c.class {
				t.Errorf("class %s, want %s", pe.Class, c.class)
			}
			if u.calls != 0 {
				t.Errorf("%d calls without a key", u.calls)
			}
		})
	}
}

func TestTheKeyIsAskedForTheJobItPays(t *testing.T) {
	var asked jobs.Job
	u := &upstream{t: t, answer: reply(200, images(10, 0, 100, png))}
	srv := httptest.NewServer(u)
	defer srv.Close()
	p := New(Config{Base: srv.URL + "/v1", Key: func(_ context.Context, j jobs.Job) (string, error) {
		asked = j
		return testKey, nil
	}})
	if _, err := p.Submit(context.Background(), job(t, medium())); err != nil {
		t.Fatal(err)
	}
	if asked.ID != 7 || asked.UserID != person {
		t.Errorf("the key was asked for job %d of %d", asked.ID, asked.UserID)
	}
}

func TestAConnectionNeverMadeIsSafeToRetry(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	p := New(Config{Base: "http://" + addr + "/v1", Key: func(context.Context, jobs.Job) (string, error) { return testKey, nil }})
	_, err = p.Submit(context.Background(), job(t, medium()))
	if pe := classOf(t, err); pe.Class != jobs.Unavailable {
		t.Errorf("class %s (%v): nothing was sent, so it may go again", pe.Class, err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("the key is in %v", err)
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
			_, err := p.Submit(ctx, job(t, medium()))
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

func TestVideoIsNotRun(t *testing.T) {
	p, u := serve(t, reply(200, "{}"))
	j := job(t, medium())
	j.Kind = catalog.Video
	_, err := p.Submit(context.Background(), j)
	if pe := classOf(t, err); pe.Class != jobs.NoAdapter || u.calls != 0 {
		t.Errorf("class %s, %d calls", pe.Class, u.calls)
	}
	if _, err := p.Poll(context.Background(), j); classOf(t, err).Class != jobs.NoAdapter {
		t.Errorf("poll: %v", err)
	}
}

func TestTheDefaultBaseIsOpenAIs(t *testing.T) {
	if got := New(Config{}).cfg.Base; got != "https://api.openai.com/v1" {
		t.Errorf("base %q", got)
	}
	if got := New(Config{Base: "https://example.test/v1/"}).cfg.Base; got != "https://example.test/v1" {
		t.Errorf("base %q", got)
	}
}
