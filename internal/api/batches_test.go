// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/jobs"
)

const (
	otherOffer = "openrouter:test/other"
	freeImage  = "openrouter:microsoft/mai-image-2.6-flash"
)

func TestFramesOfOneRunSplitByWhatTheOfferMakesAtOnce(t *testing.T) {
	for _, c := range []struct {
		name string
		keys []string
		most []int
		want string
	}{
		{"five at four", []string{"a", "a", "a", "a", "a"}, []int{4, 4, 4, 4, 4}, "[[0 1 2 3] [4]]"},
		{"three models", []string{"a", "b", "c"}, []int{4, 4, 4}, "[[0] [1] [2]]"},
		{"one at a time", []string{"a", "a", "a"}, []int{1, 1, 1}, "[[0] [1] [2]]"},
		{"a run between", []string{"a", "a", "b", "a", "a", "a"}, []int{4, 4, 4, 4, 4, 4}, "[[0 1 3 4] [2] [5]]"},
	} {
		if got := fmt.Sprint(splitFrames(c.keys, c.most)); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// batchRig is a job rig whose test offer makes up to four at once, with a
// second paid model.
func batchRig(t *testing.T) *rig {
	t.Helper()
	r := jobRig(t)
	one, four := 1.0, 4.0
	m := r.store.offers[testOffer]
	m.Offers[0].Params = maps.Clone(m.Offers[0].Params)
	m.Offers[0].Params["n"] = catalog.Param{Type: catalog.Range, Min: &one, Max: &four}
	r.store.offers[testOffer] = m
	r.store.offers[otherOffer] = image(otherOffer, catalog.OpenRouter)
	return r
}

func frame(offer, pay string, extra ...string) string {
	s := fmt.Sprintf(`{"offer":%q,"pay":%q`, offer, pay)
	for _, e := range extra {
		s += "," + e
	}
	return s + "}"
}

func press(frames ...string) string {
	return `{"prompt":"a fox in the snow","frames":[` + strings.Join(frames, ",") + `]}`
}

func pressWith(extra string, frames ...string) string {
	return `{"prompt":"a fox in the snow",` + extra + `,"frames":[` + strings.Join(frames, ",") + `]}`
}

func TestFiveSameFramesAreTwoJobsOfFourAndOne(t *testing.T) {
	r := batchRig(t)
	same := frame(testOffer, "key")
	resp := r.post(alice, "/api/batches", nextKey(), press(same, same, same, same, same))
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	got := r.queue.batches[len(r.queue.batches)-1]
	if len(got) != 2 {
		t.Fatalf("%d jobs", len(got))
	}
	if got[0].Settings["n"] != 4.0 || fmt.Sprint(got[0].BatchFrames) != "[0 1 2 3]" || !near(got[0].Estimate.USD, 0.16) {
		t.Errorf("the first: n=%v frames=%v $%v", got[0].Settings["n"], got[0].BatchFrames, got[0].Estimate.USD)
	}
	if got[1].Settings["n"] != 1.0 || fmt.Sprint(got[1].BatchFrames) != "[4]" || !near(got[1].Estimate.USD, 0.04) {
		t.Errorf("the second: n=%v frames=%v $%v", got[1].Settings["n"], got[1].BatchFrames, got[1].Estimate.USD)
	}
	if got[0].Batch == "" || got[0].Batch != got[1].Batch || got[0].DedupeKey == got[1].DedupeKey {
		t.Errorf("batch %q %q, keys %q %q", got[0].Batch, got[1].Batch, got[0].DedupeKey, got[1].DedupeKey)
	}
	jobsOut := m["jobs"].([]any)
	if m["batch"] != got[0].Batch || len(jobsOut) != 2 {
		t.Fatalf("answer %v", m)
	}
	if j := jobsOut[0].(map[string]any); j["batch"] != got[0].Batch || fmt.Sprint(j["frames"]) != "[0 1 2 3]" {
		t.Errorf("the first job says %v %v", j["batch"], j["frames"])
	}
}

func TestEachFrameMayHaveItsOwnModelAndSettings(t *testing.T) {
	r := batchRig(t)
	r.store.offers[testOffer] = withParams(r.store.offers[testOffer])
	resp := r.post(alice, "/api/batches", nextKey(), press(
		frame(testOffer, "key"),
		frame(otherOffer, "key"),
		frame(testOffer, "key", `"settings":{"resolution":"2K"}`),
		frame(freeImage, "free"),
		frame(testOffer, "key"),
	))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, jsonOf(t, resp))
	}
	var lines []string
	for _, n := range r.queue.batches[len(r.queue.batches)-1] {
		lines = append(lines, fmt.Sprintf("%s %v n=%v free=%v", n.Offer.ID, n.BatchFrames, n.Settings["n"], n.Free))
	}
	want := []string{
		testOffer + " [0 4] n=2 free=false",
		otherOffer + " [1] n=<nil> free=false",
		testOffer + " [2] n=1 free=false",
		freeImage + " [3] n=1 free=true",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("jobs:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestFreeFramesGoOneByOne(t *testing.T) {
	r := batchRig(t)
	free := frame(freeImage, "free")
	if resp := r.post(alice, "/api/batches", nextKey(), press(free, free)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d", resp.StatusCode)
	}
	if got := r.queue.batches[len(r.queue.batches)-1]; len(got) != 2 {
		t.Errorf("%d jobs", len(got))
	}
}

func TestARepeatedPressIsTheSameBatch(t *testing.T) {
	r := batchRig(t)
	key := nextKey()
	body := press(frame(testOffer, "key"), frame(otherOffer, "key"))
	first := jsonOf(t, r.post(alice, "/api/batches", key, body))
	calls := len(r.queue.batches)
	resp := r.post(alice, "/api/batches", key, body)
	again := jsonOf(t, resp)
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Idempotent-Replayed") != "true" || len(r.queue.batches) != calls {
		t.Fatalf("%d %q, %d presses", resp.StatusCode, resp.Header.Get("Idempotent-Replayed"), len(r.queue.batches))
	}
	if again["batch"] != first["batch"] || len(again["jobs"].([]any)) != 2 {
		t.Errorf("%v\n%v", first, again)
	}
	// Another person's same key is another press.
	if resp := r.post(bob, "/api/batches", key, body); resp.StatusCode != http.StatusAccepted || resp.Header.Get("Idempotent-Replayed") != "" {
		t.Errorf("bob: %d %q", resp.StatusCode, resp.Header.Get("Idempotent-Replayed"))
	}
}

func TestAPressIsChecked(t *testing.T) {
	r := batchRig(t)
	r.store.offers["openrouter:test/video"] = video("openrouter:test/video", catalog.OpenRouter)
	v := frame("openrouter:test/video", "key")
	for _, c := range []struct {
		name, body string
		status     int
		code       string
		frame      any
	}{
		{"no frames", press(), http.StatusBadRequest, codeBadRequest, nil},
		{"seven", press(strings.Repeat(frame(testOffer, "key")+",", 6) + frame(testOffer, "key")), http.StatusBadRequest, codeBadRequest, nil},
		{"five videos", press(v, v, v, v, v), http.StatusBadRequest, codeBadRequest, nil},
		{"n in a frame", press(frame(testOffer, "key", `"settings":{"n":2}`)), http.StatusBadRequest, codeBadRequest, nil},
		{"no payer", press(frame(testOffer, "")), http.StatusBadRequest, codeBadRequest, nil},
		{"pictures and videos", press(frame(testOffer, "key"), v), http.StatusBadRequest, codeBadRequest, 1.0},
		{"an unknown offer", press(frame(testOffer, "key"), frame("openrouter:nope", "key")), http.StatusUnprocessableEntity, codeUnknownOffer, 1.0},
		{"a setting the offer lacks", press(frame(testOffer, "key", `"settings":{"style":"x"}`)), http.StatusUnprocessableEntity, codeUnknownSetting, 0.0},
		{"not their file", pressWith(`"inputs":[51]`, frame(testOffer, "key")), http.StatusUnprocessableEntity, codeUnknownInput, 0.0},
		{"a frame's own file", press(frame(testOffer, "key", `"inputs":[51]`)), http.StatusUnprocessableEntity, codeUnknownInput, 0.0},
		{"one frame not free", press(frame(freeImage, "free"), frame(testOffer, "free")), http.StatusUnprocessableEntity, codeNotFree, 1.0},
	} {
		before := len(r.queue.batches)
		resp := r.post(alice, "/api/batches", nextKey(), c.body)
		m := jsonOf(t, resp)
		if resp.StatusCode != c.status || m["error"] != c.code || m["frame"] != c.frame {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
		if len(r.queue.batches) != before {
			t.Errorf("%s: something was queued", c.name)
		}
	}
	// No key: nothing starts.
	if resp := r.post(alice, "/api/batches", "", press(frame(testOffer, "key"))); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no key: %d", resp.StatusCode)
	}
}

func TestAFramesOwnPromptAndFilesReplaceThePresss(t *testing.T) {
	r := batchRig(t)
	resp := r.post(alice, "/api/batches", nextKey(), pressWith(`"inputs":[50]`,
		frame(testOffer, "key"),
		frame(testOffer, "key", `"prompt":"a cat"`, `"inputs":[]`),
	))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, jsonOf(t, resp))
	}
	got := r.queue.batches[len(r.queue.batches)-1]
	if len(got) != 2 || got[0].Prompt != "a fox in the snow" || fmt.Sprint(got[0].Inputs) != "[50]" ||
		got[1].Prompt != "a cat" || len(got[1].Inputs) != 0 {
		t.Errorf("%+v", got)
	}
}

func TestAPressDearerThanShownStartsNothing(t *testing.T) {
	r := batchRig(t)
	r.store.lang[alice] = "en"
	frames := []string{frame(testOffer, "key"), frame(otherOffer, "key"), frame(freeImage, "free")}
	before := len(r.queue.batches)
	resp := r.post(alice, "/api/batches", nextKey(), pressWith(`"shown_usd":0.05`, frames...))
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusConflict || m["error"] != codePriceChanged || len(r.queue.batches) != before {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	// The two paid frames, $0.04 each; the free one costs the person nothing.
	if e := m["estimate"].(map[string]any); e["usd"] != 0.08 || e["label"] != "$0.08" {
		t.Errorf("the new price = %v", e)
	}
	if resp := r.post(alice, "/api/batches", nextKey(), pressWith(`"shown_usd":0.08`, frames...)); resp.StatusCode != http.StatusAccepted {
		t.Errorf("the price shown: %d", resp.StatusCode)
	}
}

func TestARefusalOfTheQueueStartsNoFrame(t *testing.T) {
	r := batchRig(t)
	r.queue.err = &free.LimitError{Kind: catalog.Image, Limit: 5}
	resp := r.post(alice, "/api/batches", nextKey(), press(frame(freeImage, "free"), frame(freeImage, "free")))
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusTooManyRequests || m["error"] != codeFreeLimit {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
	r.queue.err = errors.New("the database is gone")
	if resp := r.post(alice, "/api/batches", nextKey(), press(frame(testOffer, "key"))); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("%d", resp.StatusCode)
	}
}

func TestAQuoteOfFramesSaysTheJobsAndTheTotal(t *testing.T) {
	r := batchRig(t)
	r.store.lang[alice] = "en"
	item := func(offer, pay string) string {
		return fmt.Sprintf(`{"offer":%q,"pay":%q,"prompt":"a fox"}`, offer, pay)
	}
	same := item(testOffer, "key")
	resp := r.post(alice, "/api/quote", "", `{"batch":true,"items":[`+strings.Join([]string{same, same, item(otherOffer, "key"), same, same, item(freeImage, "free")}, ",")+`]}`)
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if fmt.Sprint(m["jobs"]) != "[[0 1 3 4] [2] [5]]" {
		t.Errorf("jobs %v", m["jobs"])
	}
	if total := m["total"].(map[string]any); !near(total["usd"].(float64), 0.20) || total["label"] != "$0.20" || total["kind"] != "exact" {
		t.Errorf("total %v", total)
	}
	// Without batch the answer is as before.
	m = jsonOf(t, r.post(alice, "/api/quote", "", `{"items":[`+same+`]}`))
	if _, ok := m["jobs"]; ok {
		t.Errorf("jobs without a batch: %v", m)
	}
	// Every frame free: nothing to pay, no total.
	m = jsonOf(t, r.post(alice, "/api/quote", "", `{"batch":true,"items":[`+item(freeImage, "free")+`]}`))
	if _, ok := m["total"]; ok || fmt.Sprint(m["jobs"]) != "[[0]]" {
		t.Errorf("free: %v", m)
	}
}

func TestAHistoryPageEndsWhereABatchDoes(t *testing.T) {
	r := batchRig(t)
	put := func(id int64, batch string) {
		r.data.set(alice, State{ID: id, Status: jobs.Done, Kind: catalog.Image, Offer: testOffer, Batch: batch})
	}
	put(1, "")
	put(2, "b")
	put(3, "b")
	put(4, "b")
	put(5, "")
	ids := func(m map[string]any) string {
		var out []string
		for _, j := range m["jobs"].([]any) {
			out = append(out, fmt.Sprint(j.(map[string]any)["id"]))
		}
		return strings.Join(out, " ")
	}
	// Two would cut the batch after job 4: the page takes 3 and 2 as well.
	m := jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs?limit=2", nil))
	if ids(m) != "5 4 3 2" || m["next"] != 2.0 {
		t.Errorf("first page: %s next %v", ids(m), m["next"])
	}
	m = jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs?limit=2&before=2", nil))
	if ids(m) != "1" || m["next"] != nil {
		t.Errorf("second page: %s next %v", ids(m), m["next"])
	}
	// A page that ends with the batch whole is cut as before.
	m = jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs?limit=4", nil))
	if ids(m) != "5 4 3 2" || m["next"] != 2.0 {
		t.Errorf("four: %s next %v", ids(m), m["next"])
	}
	// One job of a batch on a page brings the rest of it.
	m = jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs?limit=1&before=5", nil))
	if ids(m) != "4 3 2" || m["next"] != 2.0 {
		t.Errorf("from 4: %s next %v", ids(m), m["next"])
	}
}
