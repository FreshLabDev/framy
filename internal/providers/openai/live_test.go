// SPDX-License-Identifier: Apache-2.0
//go:build live

// The live tests run on OpenAI itself and spend real money. The key is read
// from a file and goes nowhere but the Authorization header:
//
//	OPENAI_TEST_KEY_FILE=/path/to/key go test -tags=live -run Live -v ./internal/providers/openai/
//
// What they cost, at gpt-image-2.5-flare (the model of OPENAI_TEST_MODEL):
//
//   - TestLiveGenerationAndEdit: a medium 1024x1024 image is about $0.013, and
//     the edit with two references about $0.013 for the image plus $0.01 to
//     $0.02 for the references, so about $0.05 in all;
//   - TestLiveEditFromALink, which runs only when OPENAI_TEST_IMAGE_URL names
//     a picture OpenAI can fetch (an https link, such as a presigned R2 one):
//     a low image is about $0.006 plus the reference, about $0.02;
//   - TestLiveModerationIsARefusal, which runs only when OPENAI_TEST_MODERATION=1:
//     it sends a prompt OpenAI's safety system refuses, which is not billed.
//     It is opt-in because a refused prompt sits in the key's record at
//     OpenAI.
//
// OPENAI_TEST_OUT, when set, is a directory the images are written to.
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	imagepng "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
)

// OpenAI's prices of 26 October 2026 for the GPT image models, in dollars per
// million tokens (https://developers.openai.com/api/docs/pricing). They are
// written here again, not read from the catalog, so a catalog that drifts
// from the price page fails the test.
const (
	pageText   = 5.0
	pageImage  = 8.0
	pageOutput = 30.0
)

func liveKey(t *testing.T) string {
	t.Helper()
	path := os.Getenv("OPENAI_TEST_KEY_FILE")
	if path == "" {
		t.Skip("set OPENAI_TEST_KEY_FILE to run the live tests; they spend real money")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the key file: %v", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		t.Fatal("the key file is empty")
	}
	return key
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// recorder keeps the usage of the last answer, so the test can price the
// tokens itself.
type recorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	last *usage
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	var a struct {
		Usage *usage `json:"usage"`
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = nil
	if resp.StatusCode == http.StatusOK && json.Unmarshal(body, &a) == nil {
		r.last = a.Usage
	}
	return resp, nil
}

func (r *recorder) usage(t *testing.T) usage {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil {
		t.Fatal("the answer carried no usage")
	}
	return *r.last
}

func liveProvider(key string) (*Provider, *recorder) {
	rec := &recorder{base: http.DefaultTransport}
	return New(Config{
		// OPENAI_TEST_BASE points the test at another server, such as a fake
		// that speaks OpenAI's shapes; empty is OpenAI itself.
		Base:      os.Getenv("OPENAI_TEST_BASE"),
		HTTP:      &http.Client{Transport: rec},
		UserAgent: "framy-live-test (+https://github.com/FreshLabDev/framy)",
		Key:       func(context.Context, jobs.Job) (string, error) { return key, nil },
	}), rec
}

func run(t *testing.T, p *Provider, j jobs.Job) (jobs.Outcome, error) {
	t.Helper()
	// The queue gives an image call 180 seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	sub, err := p.Submit(ctx, j)
	if err != nil {
		return jobs.Outcome{}, err
	}
	return *sub.Outcome, nil
}

func liveJob(t *testing.T, settings map[string]any, prompt string, inputs ...jobs.Input) jobs.Job {
	t.Helper()
	j := jobOf(t, "openai:"+env("OPENAI_TEST_MODEL", "gpt-image-2.5-flare"), settings, inputs...)
	j.Prompt = prompt
	return j
}

func save(t *testing.T, name string, r jobs.Result) {
	t.Helper()
	dir := os.Getenv("OPENAI_TEST_OUT")
	if dir == "" {
		return
	}
	path := filepath.Join(dir, name+"."+strings.TrimPrefix(r.ContentType, "image/"))
	if err := os.WriteFile(path, r.Data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

func dataURL(r jobs.Result) string {
	return "data:" + r.ContentType + ";base64," + base64.StdEncoding.EncodeToString(r.Data)
}

// backdrop is a second reference framy draws itself: a blue gradient.
func backdrop(t *testing.T) jobs.Result {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 512, 512))
	for y := range 512 {
		for x := range 512 {
			img.Set(x, y, color.RGBA{R: 20, G: uint8(60 + y/8), B: uint8(140 + x/5), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := imagepng.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return jobs.Result{ContentType: "image/png", Data: buf.Bytes()}
}

// atThePage prices the usage at the published rates.
func atThePage(u usage) (text, image, out float64) {
	var t, i int64
	if d := u.InputTokensDetails; d != nil {
		t, i = d.TextTokens, d.ImageTokens
	}
	return float64(t) * pageText / 1e6, float64(i) * pageImage / 1e6, float64(u.OutputTokens) * pageOutput / 1e6
}

func TestLiveGenerationAndEdit(t *testing.T) {
	key := liveKey(t)
	p, rec := liveProvider(key)
	settings := map[string]any{"size": "1024x1024", "quality": "medium"}

	// A generation: the cost from the tokens is the price page's, and within
	// 20% of the estimate.
	j := liveJob(t, settings, "A red apple on a wooden table, soft daylight, photo")
	out, err := run(t, p, j)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if out.Err != nil || len(out.Results) == 0 {
		t.Fatalf("generation: %+v", out)
	}
	if out.CostUSD == nil || *out.CostUSD <= 0 {
		t.Fatalf("generation: the actual price must come back, got %v", out.CostUSD)
	}
	u := rec.usage(t)
	text, img, outTokens := atThePage(u)
	if want := text + img + outTokens; !within(*out.CostUSD, want, 0.001) {
		t.Errorf("generation cost %.6f, but the tokens %+v at the price page's rates are %.6f", *out.CostUSD, u, want)
	}
	m, of := realOffer(t, j.OfferID)
	est := price.Of(m, of, price.Request{Settings: settings, Prompt: j.Prompt})
	t.Logf("generation: %s, %d bytes, %d input and %d output tokens, cost $%.6f, estimate $%.6f (%s)",
		out.Results[0].ContentType, len(out.Results[0].Data), u.InputTokens, u.OutputTokens, *out.CostUSD, est.USD, est.Kind)
	if !within(*out.CostUSD, est.USD, 0.2) {
		t.Errorf("generation: cost $%.6f is more than 20%% from the estimate $%.6f; the formula's tokens may be off for this model: %d tokens were made", *out.CostUSD, est.USD, u.OutputTokens)
	}
	save(t, "generation", out.Results[0])

	// An edit with two references. The estimate cannot price the references'
	// tokens until the overlay knows their count, so it is held to the part
	// it does price: the cost without the references' tokens.
	apple := out.Results[0]
	back := backdrop(t)
	ej := liveJob(t, settings, "Put the apple from the first image on the blue background of the second image",
		jobs.Input{ContentType: apple.ContentType, URL: dataURL(apple)},
		jobs.Input{ContentType: "image/png", URL: dataURL(back)},
	)
	out, err = run(t, p, ej)
	if err != nil {
		t.Fatalf("edit with two references: %v", err)
	}
	if out.Err != nil || len(out.Results) == 0 {
		t.Fatalf("edit with two references: %+v", out)
	}
	if out.CostUSD == nil || *out.CostUSD <= 0 {
		t.Fatalf("edit: the actual price must come back, got %v", out.CostUSD)
	}
	u = rec.usage(t)
	text, img, outTokens = atThePage(u)
	if want := text + img + outTokens; !within(*out.CostUSD, want, 0.001) {
		t.Errorf("edit cost %.6f, but the tokens %+v at the price page's rates are %.6f", *out.CostUSD, u, want)
	}
	if u.InputTokensDetails == nil || u.InputTokensDetails.ImageTokens == 0 {
		t.Errorf("edit: the references brought no image tokens: %+v", u)
	}
	estEdit := price.Of(m, of, price.Request{Settings: settings, Prompt: ej.Prompt, Images: []price.Image{{Width: 1024, Height: 1024}, {Width: 512, Height: 512}}})
	withoutRefs := *out.CostUSD - img
	t.Logf("edit: %d image(s), cost $%.6f; the references took %d image tokens ($%.6f), the rest $%.6f against the estimate $%.6f (%s, unknown: %v)",
		len(out.Results), *out.CostUSD, u.InputTokensDetails.ImageTokens, img, withoutRefs, estEdit.USD, estEdit.Kind, estEdit.Unknown)
	if !within(withoutRefs, estEdit.USD, 0.2) {
		t.Errorf("edit: the part the estimate prices, $%.6f, is more than 20%% from $%.6f", withoutRefs, estEdit.USD)
	}
	save(t, "edit", out.Results[0])
}

func TestLiveEditFromALink(t *testing.T) {
	key := liveKey(t)
	link := os.Getenv("OPENAI_TEST_IMAGE_URL")
	if link == "" {
		t.Skip("set OPENAI_TEST_IMAGE_URL to an https link OpenAI can fetch to check that edits take links")
	}
	p, rec := liveProvider(key)
	j := liveJob(t, map[string]any{"size": "1024x1024", "quality": "low"}, "Make it look like a watercolor painting",
		jobs.Input{ContentType: env("OPENAI_TEST_IMAGE_TYPE", "image/jpeg"), URL: link})
	out, err := run(t, p, j)
	if err != nil {
		t.Fatalf("edit from a link: %v", err)
	}
	if out.Err != nil || len(out.Results) == 0 || out.CostUSD == nil {
		t.Fatalf("edit from a link: %+v", out)
	}
	t.Logf("edit from a link: cost $%.6f, usage %+v", *out.CostUSD, rec.usage(t))
	save(t, "edit-link", out.Results[0])
}

func TestLiveModerationIsARefusal(t *testing.T) {
	key := liveKey(t)
	if os.Getenv("OPENAI_TEST_MODERATION") != "1" {
		t.Skip("set OPENAI_TEST_MODERATION=1 to send a prompt OpenAI's safety system refuses")
	}
	p, _ := liveProvider(key)
	j := liveJob(t, map[string]any{"size": "1024x1024", "quality": "low"}, "An explicit pornographic photo of two nude adults having sex")
	out, err := run(t, p, j)
	pe := (*jobs.Error)(nil)
	switch {
	case errors.As(err, &pe):
	case err != nil:
		t.Fatalf("a failed call, not a refusal: %v", err)
	case out.Err != nil:
		pe = out.Err
	default:
		t.Fatalf("the prompt was not refused: %d image(s)", len(out.Results))
	}
	if pe.Class != jobs.Moderation {
		t.Fatalf("class %s (%v), want moderation", pe.Class, pe)
	}
	var api *APIError
	if !errors.As(pe, &api) || api.Stage == "" {
		t.Errorf("no moderation stage in %v", pe)
	}
	t.Logf("refused: %v", pe)
}
