// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
)

const (
	extOffer  = "openrouter:test/extender"
	fluxOffer = "openrouter:black-forest-labs/flux-video-edit"
)

// extender continues a video of its own, as Omni does: it picks 3 to 10 s,
// and the whole clip may be billed, at $0.10 a second.
func extender() catalog.Model {
	of := catalog.Offer{
		ID: extOffer, ModelID: "test/extender", Provider: catalog.OpenRouter, ProviderModelID: "test/extender",
		Kind: catalog.Video, Name: "Extender", InputModalities: []string{"text", "image", "video"},
		Params: map[string]catalog.Param{"resolution": {Type: catalog.Enum, Values: []any{"720p"}}},
		Routes: []catalog.Route{{SKUs: map[string]string{"duration_seconds_720p": "0.1"}}},
	}
	return catalog.Model{
		ID: of.ModelID, Kind: catalog.Video, Name: "Extender", Offers: []catalog.Offer{of},
		Traits: catalog.Traits{
			Continue: true, PicksLength: &catalog.Length{Min: 3, Max: 10},
			SourceVideo: &catalog.SourceVideo{MaxSeconds: 10},
		},
	}
}

// flux is FLUX Video Edit, the free edit: one clip, as long as it is.
func flux() catalog.Model {
	of := catalog.Offer{
		ID: fluxOffer, ModelID: "black-forest-labs/flux-video-edit", Provider: catalog.OpenRouter, ProviderModelID: "black-forest-labs/flux-video-edit",
		Kind: catalog.Video, Name: "FLUX Video Edit", InputModalities: []string{"text", "video"},
		Routes: []catalog.Route{{SKUs: map[string]string{"duration_seconds": "0.03"}}},
	}
	return catalog.Model{
		ID: of.ModelID, Kind: catalog.Video, Name: "FLUX Video Edit", Free: true, Offers: []catalog.Offer{of},
		Traits: catalog.Traits{SourceVideo: &catalog.SourceVideo{Required: true, MaxSeconds: 10, KeepsLength: true}},
	}
}

// continueRig has the extender, alice's job 7 that made a 6 s video with
// it, and a clip of alice's whose length is not known.
func continueRig(t *testing.T, opts ...func(*rigOptions)) *rig {
	t.Helper()
	r := jobRig(t, opts...)
	r.store.offers[extOffer] = extender()
	r.store.offers[fluxOffer] = flux()
	r.data.set(alice, State{ID: 7, Status: jobs.Done, Kind: catalog.Video, Offer: extOffer})
	r.data.conts[7] = madeBy("test/extender")
	r.data.addAsset(alice, InputAsset{ID: 52, ContentType: "video/mp4", Width: 1280, Height: 720, Bytes: 3 << 20})
	r.data.addAsset(alice, InputAsset{ID: 53, ContentType: "video/mp4", Width: 1280, Height: 720, Bytes: 3 << 20, Seconds: 6})
	return r
}

func madeBy(model string) Continuation {
	return Continuation{
		ModelID: model, Provider: catalog.OpenRouter, ProviderJobID: "gen-or-1", Status: jobs.Done,
		Video: &InputAsset{ID: 71, ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 6},
	}
}

func TestARunContinuesAVideoOfThePersons(t *testing.T) {
	r := continueRig(t)
	resp := r.post(alice, "/api/jobs", nextKey(), body(extOffer, "key", `"continues":7`))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	n := r.queue.last()
	if n.Settings["previous_job_id"] != "gen-or-1" {
		t.Errorf("settings: %v", n.Settings)
	}
	// 3 to 10 s more, on top of the 6 s it goes on from.
	if n.Estimate == nil || math.Abs(n.Estimate.MaxUSD-1.6) > 1e-9 || math.Abs(n.Estimate.USD-0.3) > 1e-9 {
		t.Errorf("estimate: %+v", n.Estimate)
	}
}

func TestARunContinuesOnlyWhatItMay(t *testing.T) {
	r := continueRig(t)
	r.data.set(bob, State{ID: 8, Status: jobs.Done})
	r.data.conts[8] = madeBy("test/extender")
	job := 20
	for _, c := range []struct {
		name  string
		offer string
		cont  func(*Continuation)
		want  string
	}{
		{"a model that does not continue", testOffer, nil, "does not continue"},
		{"another person's", extOffer, nil, "not one of yours"},
		{"unknown", extOffer, nil, "not one of yours"},
		{"still running", extOffer, func(c *Continuation) { c.Status = jobs.Running }, "not done"},
		{"another model's", extOffer, func(c *Continuation) { c.ModelID = "test/other" }, "the model that made it"},
		{"another provider's", extOffer, func(c *Continuation) { c.Provider = catalog.Gemini }, "the same provider"},
		{"a free run's", extOffer, func(c *Continuation) { c.Free = true }, "free allowance"},
		{"with no id at the provider", extOffer, func(c *Continuation) { c.ProviderJobID = "" }, "no id"},
		{"with no video", extOffer, func(c *Continuation) { c.Video = nil }, "no video kept"},
		{"of no known length", extOffer, func(c *Continuation) { c.Video.Seconds = 0 }, "not known"},
	} {
		id := int64(7)
		switch c.name {
		case "another person's":
			id = 8
		case "unknown":
			id = 99
		}
		if c.cont != nil {
			job++
			id = int64(job)
			cont := madeBy("test/extender")
			v := *cont.Video
			cont.Video = &v
			c.cont(&cont)
			r.data.set(alice, State{ID: id, Status: cont.Status})
			r.data.conts[id] = cont
		}
		resp := r.post(alice, "/api/jobs", nextKey(), body(c.offer, "key", `"continues":`+strconv.FormatInt(id, 10)))
		m := jsonOf(t, resp)
		msg, _ := m["message"].(string)
		if resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "cannot_continue" || m["continues"] != float64(id) || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
	}
	if r.queue.calls() != 0 {
		t.Errorf("%d refused runs reached the queue", r.queue.calls())
	}
}

func TestAContinuationIsFramysToFill(t *testing.T) {
	r := continueRig(t)
	for _, c := range []struct{ name, body, field string }{
		{"previous_job_id", body(extOffer, "key", `"settings":{"previous_job_id":"gen-of-somebody"}`), "settings.previous_job_id"},
		{"previous_interaction_id", body(extOffer, "key", `"settings":{"previous_interaction_id":"v1_x"}`), "settings.previous_interaction_id"},
		{"a negative job", body(extOffer, "key", `"continues":-7`), "continues"},
	} {
		resp := r.post(alice, "/api/jobs", nextKey(), c.body)
		if m := jsonOf(t, resp); resp.StatusCode != http.StatusBadRequest || m["field"] != c.field {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
	}
	// The database failing is framy's error, not the person's.
	r.data.mu.Lock()
	r.data.failedConts = errors.New("connection reset")
	r.data.mu.Unlock()
	resp := r.post(alice, "/api/jobs", nextKey(), body(extOffer, "key", `"continues":7`))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("a failing read: %d %v", resp.StatusCode, m)
	}
}

// A kept clip framy read no length of cannot be checked or priced: it is
// cut first, which reads one.
func TestAClipOfUnknownLengthIsCutFirst(t *testing.T) {
	r := continueRig(t)
	resp := r.post(alice, "/api/jobs", nextKey(), body(extOffer, "key", `"inputs":[52]`))
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "wrong_inputs" || m["input"] != 52.0 {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
	// Over the model's 10 s is said with the model's limit.
	r.data.addAsset(alice, InputAsset{ID: 54, ContentType: "video/mp4", Width: 1280, Height: 720, Bytes: 3 << 20, Seconds: 12})
	resp = r.post(alice, "/api/jobs", nextKey(), body(extOffer, "key", `"inputs":[54]`))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || m["error"] != "wrong_inputs" {
		t.Errorf("12 s: %d %v", resp.StatusCode, m)
	}
	resp = r.post(alice, "/api/jobs", nextKey(), body(extOffer, "key", `"inputs":[53]`))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("6 s: %d %v", resp.StatusCode, m)
	}
	if est := r.queue.last().Estimate; est == nil || math.Abs(est.MaxUSD-1.6) > 1e-9 {
		t.Errorf("an extended upload's estimate: %+v", est)
	}
}

func TestAQuoteContinuesAVideo(t *testing.T) {
	a := &fakeAllowance{}
	r := continueRig(t, withAllowance(a))
	_, m := quoteOf(t, r, alice, `{"items":[
		{"offer":"`+extOffer+`","pay":"key","continues":7},
		{"offer":"`+extOffer+`","pay":"key","continues":99},
		{"offer":"`+extOffer+`","pay":"free","continues":7},
		{"offer":"`+fluxOffer+`","pay":"free","inputs":[53]},
		{"offer":"`+fluxOffer+`","pay":"free","files":[{"content_type":"video/mp4","seconds":4.2,"width":1280,"height":720,"bytes":1000}]}
	]}`)
	list := items(t, m)
	if est := list[0]["estimate"].(map[string]any); est["max_usd"] != 1.6 || list[0]["refusal"] != nil {
		t.Errorf("continued: %v", list[0])
	}
	if s, _ := list[0]["settings"].(map[string]any); s == nil || s["previous_job_id"] != nil {
		t.Errorf("the quote gives the provider's job id away: %v", s)
	}
	for i, want := range map[int]string{1: "not one of yours", 2: "your own key"} {
		ref, _ := list[i]["refusal"].(map[string]any)
		if msg, _ := ref["message"].(string); ref["error"] != "cannot_continue" || !strings.Contains(msg, want) {
			t.Errorf("item %d: %v", i, list[i])
		}
	}
	// The free edit is asked about with the clips' lengths, kept or not.
	a.mu.Lock()
	asked := a.asked
	a.mu.Unlock()
	if len(asked) != 2 || len(asked[0].Videos) != 1 || asked[0].Videos[0] != 6 || len(asked[1].Videos) != 1 || asked[1].Videos[0] != 4.2 {
		t.Errorf("asked: %+v", asked)
	}

	// Without a person nothing is continued.
	_, m = quoteOf(t, r, 0, `{"items":[{"offer":"`+extOffer+`","continues":7}]}`)
	ref, _ := items(t, m)[0]["refusal"].(map[string]any)
	if msg, _ := ref["message"].(string); ref["error"] != "cannot_continue" || !strings.Contains(msg, "sign in") {
		t.Errorf("anonymous: %v", m)
	}
}

// The extender runs on the person's key alone: a connection they hold.
func TestAContinuationNeedsTheConnection(t *testing.T) {
	r := continueRig(t)
	r.conns.list = []keys.Connection{{Provider: catalog.OpenAI, Status: keys.Valid}}
	resp := r.post(alice, "/api/jobs", nextKey(), body(extOffer, "key", `"continues":7`))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusPaymentRequired || m["error"] != "no_connection" {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
}
