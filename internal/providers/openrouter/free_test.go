// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// variantJob is a run of the test offer whose "cheap" route has a free variant
// that takes words alone.
func variantJob(settings map[string]any, inputs ...jobs.Input) jobs.Job {
	j := job(settings, inputs...)
	j.Offer.Routes[0].Free = &catalog.FreeVariant{Model: "test/image:free", Until: "2999-01-01"}
	zero := 0.0
	j.EstimateUSD = &zero
	return j
}

// serveChat is a fake OpenRouter that takes chat completions, records the
// last body and answers as told.
func serveChat(t *testing.T, answer string) (*Provider, *map[string]any) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/chat/completions" {
			t.Errorf("%s %s, want POST /api/v1/chat/completions", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	p := New(Config{
		Base: srv.URL + "/api/v1", HTTP: srv.Client(),
		Key: func(context.Context, jobs.Job) (string, error) { return "sk-or-v1-test", nil },
	})
	return p, &body
}

func chatImage(data []byte, cost string) string {
	usage := ""
	if cost != "" {
		usage = `,"usage":{"cost":` + cost + `}`
	}
	return `{"choices":[{"message":{"role":"assistant","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
		base64.StdEncoding.EncodeToString(data) + `"}}]}}]` + usage + `}`
}

// An image from words goes to the free variant through chat completions,
// pinned to the upstream that serves it, with its shape and nothing else.
func TestWordsGoToTheFreeVariant(t *testing.T) {
	p, body := serveChat(t, chatImage(png, ""))
	sub, err := p.Submit(context.Background(), variantJob(map[string]any{"n": 1, "aspect_ratio": "16:9"}))
	if err != nil {
		t.Fatal(err)
	}
	out := sub.Outcome
	if out.Err != nil || len(out.Results) != 1 || string(out.Results[0].Data) != string(png) || out.Results[0].ContentType != "image/png" {
		t.Fatalf("outcome %+v", out)
	}
	if out.CostUSD == nil || *out.CostUSD != 0 {
		t.Errorf("cost %v, want 0", out.CostUSD)
	}
	b := *body
	if b["model"] != "test/image:free" {
		t.Errorf("model %v", b["model"])
	}
	if got, _ := json.Marshal(b["modalities"]); string(got) != `["image"]` {
		t.Errorf("modalities %s", got)
	}
	if got, _ := json.Marshal(b["image_config"]); string(got) != `{"aspect_ratio":"16:9"}` {
		t.Errorf("image_config %s", got)
	}
	if got, _ := json.Marshal(b["provider"]); string(got) != `{"only":["cheap"]}` {
		t.Errorf("provider %s", got)
	}
	if _, ok := b["n"]; ok {
		t.Error("n went to chat completions")
	}
	msgs, _ := json.Marshal(b["messages"])
	if string(msgs) != `[{"content":[{"text":"a fox in the snow","type":"text"}],"role":"user"}]` {
		t.Errorf("messages %s", msgs)
	}
}

// What the variant does not take goes to the paid route, as the estimate
// priced it: two results, or a photo.
func TestTheFreeVariantTakesOnlyWhatItCan(t *testing.T) {
	for name, j := range map[string]jobs.Job{
		"two results": variantJob(map[string]any{"n": 2}),
		"a photo":     variantJob(nil, jobs.Input{ContentType: "image/png", URL: "https://example.com/a.png"}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := freeRoute(j, timeNow()); ok {
				t.Error("goes to the free variant")
			}
		})
	}
}

// A setting the variant has no field for refuses the run before anything
// goes.
func TestTheFreeVariantRefusesASettingItHasNoFieldFor(t *testing.T) {
	p, body := serveChat(t, chatImage(png, ""))
	_, err := p.Submit(context.Background(), variantJob(map[string]any{"seed": 4}))
	pe, ok := err.(*jobs.Error)
	if !ok || pe.Class != jobs.Invalid {
		t.Fatalf("err %v, want invalid", err)
	}
	if *body != nil {
		t.Error("a request went")
	}
}

// A free run priced at nothing whose variant is gone does not go to the
// paid route on framy's key.
func TestAFreeRunWhoseVariantLeftIsNotPaid(t *testing.T) {
	j := variantJob(nil)
	j.Offer.Routes[0].Free.Until = "2000-01-01"
	p, u := serve(t, reply(200, images("0.007", png)))
	_, err := p.Submit(context.Background(), j)
	if pe, ok := err.(*jobs.Error); !ok || pe.Class != jobs.Invalid {
		t.Fatalf("err %v, want invalid", err)
	}
	if u.calls != 0 {
		t.Errorf("%d paid calls", u.calls)
	}
}

func TestAnAnswerWithoutAnImageFails(t *testing.T) {
	p, _ := serveChat(t, `{"choices":[{"message":{"role":"assistant","content":"I cannot draw that."}}]}`)
	sub, err := p.Submit(context.Background(), variantJob(nil))
	if err != nil {
		t.Fatal(err)
	}
	if out := sub.Outcome; out.Err == nil || out.Err.Class != jobs.ProviderFailed || len(out.Results) != 0 {
		t.Errorf("outcome %+v", out)
	}
}

func timeNow() time.Time { return time.Now() }
