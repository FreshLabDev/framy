// SPDX-License-Identifier: Apache-2.0
//go:build live

// The live tests run on Google's Gemini API itself and spend real money. The
// key comes from a file and goes nowhere but the x-goog-api-key header; its
// project must have billing, since the image models have no free tier:
//
//	GEMINI_TEST_KEY_FILE=/path/to/key go test -tags=live -run Live -v ./internal/providers/gemini/
//
// What they cost, at the rates of catalog/gemini.yaml, on the default model
// (Nano Banana 2.1, 1K, thinking_level minimal):
//
//   - TestLiveGenerationAndEdit: two images, about $0.034 each, and the
//     tokens of the prompt and two reference images, under a cent: about
//     $0.07 in all.
//   - TestLiveAKeyWithoutBilling: nothing. It runs only with
//     GEMINI_TEST_UNBILLED_KEY_FILE, a key from a project without billing,
//     and Google refuses before it makes anything.
//   - TestLiveProbes: only with GEMINI_TEST_PROBES=1. A prompt that asks for
//     words and no picture (a cent or less, $0.034 if the model draws
//     anyway) and one image grounded in Google Search (about $0.034, plus
//     $0.014 a search query beyond the free 5,000 a month).
//
// GEMINI_TEST_MODEL names another model of catalog/gemini.yaml. GEMINI_TEST_OUT,
// when set, is a directory the images are written to, together with each
// answer as Google sent it, the pictures cut out: a refusal's real shape is
// what the fixtures in gemini_test.go should be taken from.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	imagepng "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

func keyFrom(t *testing.T, env string) string {
	t.Helper()
	path := os.Getenv(env)
	if path == "" {
		t.Skipf("set %s to run this live test; it spends real money", env)
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

func liveModel() string {
	if m := os.Getenv("GEMINI_TEST_MODEL"); m != "" {
		return m
	}
	return nano21
}

// bigData matches a picture inside an answer.
var bigData = regexp.MustCompile(`"data"\s*:\s*"[A-Za-z0-9+/=]{200,}"`)

// answers numbers the recorded answers across providers, so none is
// overwritten.
var answers atomic.Int32

// recorder keeps each answer for GEMINI_TEST_OUT, pictures cut out.
type recorder struct {
	next http.RoundTripper
	dir  string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil || r.dir == "" {
		return resp, err
	}
	raw, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if rerr != nil {
		return resp, nil
	}
	cut := bigData.ReplaceAllStringFunc(string(raw), func(m string) string { return fmt.Sprintf(`"data":"<%d characters of base64>"`, len(m)) })
	name := filepath.Join(r.dir, fmt.Sprintf("answer-%02d-http%d.json", answers.Add(1), resp.StatusCode))
	_ = os.WriteFile(name, []byte(cut), 0o644)
	return resp, nil
}

func liveProvider(key string) *Provider {
	return New(Config{
		// GEMINI_TEST_BASE points the run at a proxy or a fake; Google's
		// own API when empty.
		Base:      os.Getenv("GEMINI_TEST_BASE"),
		HTTP:      &http.Client{Transport: &recorder{next: http.DefaultTransport, dir: os.Getenv("GEMINI_TEST_OUT")}},
		UserAgent: "framy-live-test (+https://github.com/FreshLabDev/framy)",
		Key:       func(context.Context, jobs.Job) (string, error) { return key, nil },
	})
}

func liveJob(t *testing.T, prompt string, settings map[string]any, inputs ...jobs.Input) jobs.Job {
	t.Helper()
	j := jobOf(t, liveModel(), settings, inputs...)
	j.Prompt = prompt
	return j
}

// cheap is what keeps a run at its floor: the smallest picture and the least
// thinking the model allows.
func cheap(model string) map[string]any {
	s := map[string]any{"resolution": "1K"}
	if _, ok := offerOfModel(model).Params["thinking_level"]; ok {
		s["thinking_level"] = "minimal"
	}
	return s
}

func offerOfModel(model string) catalog.Offer {
	all, err := offers()
	if err != nil {
		panic(err)
	}
	return all[catalog.OfferID(catalog.Gemini, model)]
}

func run(t *testing.T, p *Provider, j jobs.Job) (jobs.Outcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sub, err := p.Submit(ctx, j)
	if err != nil {
		return jobs.Outcome{}, err
	}
	return *sub.Outcome, nil
}

func save(t *testing.T, name string, r jobs.Result) {
	t.Helper()
	dir := os.Getenv("GEMINI_TEST_OUT")
	if dir == "" {
		return
	}
	ext := strings.TrimPrefix(r.ContentType, "image/")
	path := filepath.Join(dir, name+"."+ext)
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

func costOf(out jobs.Outcome) string {
	if out.CostUSD == nil {
		return "none"
	}
	return fmt.Sprintf("$%.6f", *out.CostUSD)
}

func TestLiveGenerationAndEdit(t *testing.T) {
	key := keyFrom(t, "GEMINI_TEST_KEY_FILE")
	p := liveProvider(key)
	model := liveModel()

	out, err := run(t, p, liveJob(t, "A red apple on a wooden table, soft daylight, photo", cheap(model)))
	if err != nil {
		t.Fatalf("generation: %v (key status %q)", err, KeyStatusOf(err))
	}
	if out.Err != nil || len(out.Results) == 0 {
		t.Fatalf("generation: %+v", out)
	}
	if out.CostUSD == nil || *out.CostUSD <= 0 || *out.CostUSD > 0.5 {
		t.Errorf("generation cost %s: the price from the usage must come back, and a 1K image is cents", costOf(out))
	}
	t.Logf("generation on %s: %d image(s), %s, %d bytes, cost %s", model, len(out.Results), out.Results[0].ContentType, len(out.Results[0].Data), costOf(out))
	save(t, "generation", out.Results[0])

	apple := out.Results[0]
	out, err = run(t, p, liveJob(t, "Put the apple from the first image on the blue background of the second image",
		cheap(model),
		jobs.Input{ContentType: apple.ContentType, URL: dataURL(apple)},
		jobs.Input{ContentType: "image/png", URL: dataURL(backdrop(t))},
	))
	if err != nil {
		t.Fatalf("edit with two references: %v (key status %q)", err, KeyStatusOf(err))
	}
	if out.Err != nil || len(out.Results) == 0 {
		t.Fatalf("edit with two references: %+v", out)
	}
	if out.CostUSD == nil || *out.CostUSD <= 0 {
		t.Errorf("edit cost %s", costOf(out))
	}
	t.Logf("edit: %d image(s), cost %s", len(out.Results), costOf(out))
	save(t, "edit", out.Results[0])
}

func TestLiveAKeyWithoutBilling(t *testing.T) {
	key := keyFrom(t, "GEMINI_TEST_UNBILLED_KEY_FILE")
	p := liveProvider(key)
	out, err := run(t, p, liveJob(t, "A red apple on a wooden table", cheap(liveModel())))
	if err == nil {
		t.Fatalf("the run succeeded (%d image(s), cost %s): this key's project has billing", len(out.Results), costOf(out))
	}
	var pe *jobs.Error
	if !errors.As(err, &pe) || pe.Class != jobs.Key {
		t.Fatalf("%v: want a key error, so the job is not retried", err)
	}
	if got := KeyStatusOf(err); got != StatusNeedsBilling {
		t.Fatalf("key status %q, want %q: %v", got, StatusNeedsBilling, err)
	}
	t.Logf("refused as expected: %v", err)
}

func TestLiveProbes(t *testing.T) {
	key := keyFrom(t, "GEMINI_TEST_KEY_FILE")
	if os.Getenv("GEMINI_TEST_PROBES") == "" {
		t.Skip("set GEMINI_TEST_PROBES=1 to run the probes; they spend a few cents")
	}
	p := liveProvider(key)
	model := liveModel()

	t.Run("words and no picture", func(t *testing.T) {
		out, err := run(t, p, liveJob(t, "Do not draw anything. Answer in words only: what is the capital of France?", cheap(model)))
		if err != nil {
			t.Fatalf("%v", err)
		}
		if out.Err == nil {
			t.Skipf("the model drew %d image(s) anyway, cost %s", len(out.Results), costOf(out))
		}
		var r *Refusal
		if out.Err.Class != jobs.Moderation || !errors.As(out.Err, &r) || r.Text == "" {
			t.Fatalf("outcome %+v: want a refusal that carries the model's words", out.Err)
		}
		t.Logf("%v (cost %s)", out.Err, costOf(out))
	})

	t.Run("grounded in Google Search", func(t *testing.T) {
		if _, ok := offerOfModel(model).Params["google_search"]; !ok {
			t.Skipf("%s has no search grounding", model)
		}
		s := cheap(model)
		s["google_search"] = true
		out, err := run(t, p, liveJob(t, "A poster of today's weather in Kyiv, as a simple illustration", s))
		if err != nil {
			t.Fatalf("%v", err)
		}
		if out.Err != nil || len(out.Results) == 0 {
			t.Fatalf("outcome %+v", out)
		}
		t.Logf("grounded: cost %s", costOf(out))
		save(t, "grounded", out.Results[0])
	})
}
