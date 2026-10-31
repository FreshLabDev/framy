// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// The English message every failed fake job carries. The stage shows a
// provider's own words only for the classes where they help; this one must
// never be on the stage for the others.
const fakeMessage = "upstream said: blocked by filter 7"

// fakeFaces is how BytePlus refuses a Seedance clip with a person in it, as
// the adapter stores it: framy's prefixes around the provider's JSON.
const fakeFaces = `refused: openrouter 400: HTTP 400: {"error":{"code":"InputVideoSensitiveContentDetected.PrivacyInformation",` +
	`"message":"The request failed because the input video 'content[1]' may contain real person. Request id: 0217abc","param":"content[1]","type":"BadRequest"}}`

// fakeFacesWords is the same refusal as the server says it now: class
// faces, with only the provider's words.
const fakeFacesWords = "The request failed because the input image 'content[0]' may contain real person."

// fakeWrapped is any other refusal stored the same way.
const fakeWrapped = `refused: openrouter 400: HTTP 400: {"error":{"code":"InvalidParameter","message":"The duration is too long. Request id: 0217abd"}}`

// fakeRuns is the API of a signed-in person with a working OpenRouter key and
// no free pictures left, whose jobs follow the prompt: "fail:<class>" fails
// with that class, "retry" waits for a retry, "running" runs and never ends,
// "poll" is read without the event stream, "done" makes a picture ("slow"
// after two seconds of running), "faces" and "wrapped" are refused by the
// provider in its own JSON, and "refuse:<code>" is refused before any job.
type fakeRuns struct {
	mu     sync.Mutex
	next   int64
	jobs   map[int64]*fakeJob
	shares int // how many results were prepared for sharing
}

type fakeJob struct {
	steps []map[string]any // what the events say, in order
	shown int              // how many of them were said
	stop  chan struct{}    // closed when the job is cancelled
}

func (f *fakeRuns) mount(srv *Server) {
	f.jobs = map[int64]*fakeJob{}
	srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, http.StatusOK, map[string]any{
			"user_id": 1, "name": "Ada", "language": "en", "currency": "USD",
			"connections": []map[string]any{{"id": "c1", "provider": "openrouter", "last4": "abcd", "status": "ok"}},
			"free": map[string]any{
				"images": map[string]any{"used": 5, "limit": 5, "left": 0, "window_seconds": 86400, "frees_at": time.Now().Add(3 * time.Hour).UTC()},
				"videos": map[string]any{"used": 0, "limit": 1, "left": 1, "window_seconds": 604800},
			},
		})
	})
	srv.HandleFunc("POST /api/jobs", f.create)
	srv.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j, id := f.job(r)
		if j == nil {
			writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
			return
		}
		f.mu.Lock()
		// Read without the stream, the job moves one step a read.
		if j.steps[0]["prompt"] == "poll" && j.shown < len(j.steps) {
			j.shown++
		}
		cur := j.steps[max(0, j.shown-1)]
		f.mu.Unlock()
		writeTestJSON(w, http.StatusOK, withID(cur, id))
	})
	srv.HandleFunc("GET /api/jobs/{id}/events", f.events, Stream())
	srv.HandleFunc("POST /api/jobs/{id}/cancel", f.cancel)
	srv.HandleFunc("POST /auth/miniapp", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, http.StatusOK, map[string]any{"token": "test-token", "expires_in": 3600})
	})
	srv.HandleFunc("POST /api/files/{id}/share", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.shares++
		f.mu.Unlock()
		if r.PathValue("id") == "8" {
			writeTestJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "not_shareable", "message": fakeMessage})
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"id": "prep-" + r.PathValue("id"), "expires_at": time.Now().Add(24 * time.Hour).UTC()})
	})
	srv.HandleFunc("GET /test/result.png", func(w http.ResponseWriter, _ *http.Request) {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for i := range img.Pix {
			img.Pix[i] = 0xC0
		}
		img.Set(1, 1, color.Black)
		var b bytes.Buffer
		_ = png.Encode(&b, img)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b.Bytes())
	})
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withID(m map[string]any, id int64) map[string]any {
	out := map[string]any{"id": id}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (f *fakeRuns) job(r *http.Request) (*fakeJob, int64) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs[id], id
}

func (f *fakeRuns) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Offer    string         `json:"offer"`
		Prompt   string         `json:"prompt"`
		Settings map[string]any `json:"settings"`
		Pay      string         `json:"pay"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
		return
	}
	if code, ok := strings.CutPrefix(in.Prompt, "refuse:"); ok {
		body := map[string]any{"error": code, "message": fakeMessage}
		status := http.StatusUnprocessableEntity
		switch code {
		case "price_changed":
			status = http.StatusConflict
			body["estimate"] = map[string]any{"kind": "about", "usd": 0.5, "max_usd": 0.5, "currency": "USD", "label": "$0.500"}
		case "free_closed":
			status = http.StatusServiceUnavailable
			body["until"] = time.Now().Add(15 * time.Minute).UTC()
		}
		writeTestJSON(w, status, body)
		return
	}
	model := strings.SplitN(in.Offer, ":", 2)[1]
	base := map[string]any{"kind": "image", "offer": in.Offer, "model": model, "prompt": in.Prompt, "settings": in.Settings, "free": false,
		"surface": "site", "estimate": map[string]any{"kind": "about", "usd": 0.04, "max_usd": 0.04, "currency": "USD", "label": "$0.040"}}
	at := func(status string, more map[string]any) map[string]any {
		m := map[string]any{"status": status}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range more {
			m[k] = v
		}
		return m
	}
	steps := []map[string]any{at("queued", nil)}
	switch p := in.Prompt; {
	case strings.HasPrefix(p, "fail:"):
		cls := strings.TrimPrefix(p, "fail:")
		steps = append(steps, at("running", nil), at("failed", map[string]any{"error": map[string]any{"class": cls, "message": fakeMessage}}))
	case p == "faces", p == "wrapped":
		msg := map[string]string{"faces": fakeFaces, "wrapped": fakeWrapped}[p]
		steps = append(steps, at("running", nil), at("failed", map[string]any{"error": map[string]any{"class": "refused", "message": msg}}))
	case p == "faces:photo":
		steps = append(steps, at("running", nil), at("failed", map[string]any{"error": map[string]any{"class": "faces", "message": fakeFacesWords}}))
	case p == "retry":
		steps = append(steps, at("queued", map[string]any{"error": map[string]any{"class": "rate_limited", "message": fakeMessage},
			"retry_at": time.Now().Add(30 * time.Second).UTC()}))
	case p == "running":
		steps = append(steps, at("running", nil))
	case p == "slow":
		for range 14 {
			steps = append(steps, at("running", nil))
		}
		fallthrough
	case p == "done", p == "poll", p == "done:big":
		// Result 8 is one Telegram will not take.
		url, rid := "http://"+r.Host+"/test/result.png", 7
		if p == "done:big" {
			rid = 8
		}
		steps = append(steps, at("running", nil), at("done", map[string]any{"cost": map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"},
			"results": []map[string]any{{"id": rid, "content_type": "image/png", "url": url, "download": url + "?download=1", "width": 1024, "height": 1024}}}))
	}
	f.mu.Lock()
	f.next++
	id := f.next
	f.jobs[id] = &fakeJob{steps: steps, shown: 1, stop: make(chan struct{})}
	f.mu.Unlock()
	writeTestJSON(w, http.StatusAccepted, withID(steps[0], id))
}

// events says the job's steps a moment apart, then holds the stream open
// until the job is cancelled or the page goes.
func (f *fakeRuns) events(w http.ResponseWriter, r *http.Request) {
	j, id := f.job(r)
	if j == nil {
		writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	if j.steps[0]["prompt"] == "poll" {
		writeTestJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too_many_streams"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	say := func(n int, m map[string]any) {
		b, _ := json.Marshal(withID(m, id))
		fmt.Fprintf(w, "id: %d\nevent: job\ndata: %s\n\n", n, b)
		if fl != nil {
			fl.Flush()
		}
	}
	for n := range j.steps {
		f.mu.Lock()
		j.shown = max(j.shown, n+1)
		f.mu.Unlock()
		say(n+1, j.steps[n])
		if st := j.steps[n]["status"]; st == "failed" || st == "done" {
			return
		}
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	select {
	case <-j.stop:
		f.mu.Lock()
		last := j.steps[len(j.steps)-1]
		f.mu.Unlock()
		say(len(j.steps), last)
	case <-r.Context().Done():
	}
}

func (f *fakeRuns) cancel(w http.ResponseWriter, r *http.Request) {
	j, id := f.job(r)
	if j == nil {
		writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	f.mu.Lock()
	cur := j.steps[j.shown-1]
	if cur["status"] != "queued" {
		f.mu.Unlock()
		writeTestJSON(w, http.StatusConflict, map[string]any{"error": "job_running"})
		return
	}
	done := map[string]any{}
	for k, v := range cur {
		done[k] = v
	}
	done["status"] = "cancelled"
	delete(done, "retry_at")
	delete(done, "error")
	j.steps = append(j.steps, done)
	j.shown = len(j.steps)
	close(j.stop)
	f.mu.Unlock()
	writeTestJSON(w, http.StatusOK, withID(done, id))
}

// runHelpers press Create with a prompt and read what the stage says.
const runHelpers = `
const stage = () => {
  const st = document.querySelector("#stage");
  return { label: st.querySelector(".jbox .c b")?.textContent || "", sub: st.querySelector(".jbox .c small")?.textContent || "",
    why: st.querySelector(".jwhy")?.textContent || "", said: st.querySelector(".jbox .c .said")?.textContent || "",
    acts: [...st.querySelectorAll(".jacts > button, .jacts > a, .jacts > .busy")].map((b) => b.textContent.trim()),
    failed: !!st.querySelector(".jbox.failed"), done: !!st.querySelector(".jbox.done img"), line: st.querySelector(".jline")?.textContent || "",
    dots: !!st.querySelector(".jbox > canvas.ht"),
    text: st.textContent };
};
async function press(prompt, until) {
  const p = document.querySelector("#prompt");
  p.value = prompt;
  p.dispatchEvent(new Event("input"));
  await wait(50);
  document.querySelector("#go").click();
  for (let i = 0; i < 140; i++) { await wait(100); const s = stage(); if (until(s)) return s; }
  return { timeout: true, ...stage() };
}
`

func TestBrowserRuns(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)

	// Every class a job can fail with has its own words, and the English
	// message shows, on the frame, only where it is the provider's own reason.
	// A run on a key that cost nothing shows its price as $0 instead of a
	// line saying so.
	type want struct {
		label, why, first string
		said, zero        bool
	}
	for cls, w := range map[string]want{
		"moderation":      {"Refused by the rules", "", "Edit the prompt", true, true},
		"money":           {"No money on the key", "", "Top up OpenRouter", false, true},
		"key":             {"The key does not work", "no longer accepts the key", "Replace the OpenRouter key", false, true},
		"refused":         {"Not accepted", "did not accept the request", "Edit the prompt", true, true},
		"rate_limited":    {"No answer", "Try again in a few minutes", "Try again", false, true},
		"unavailable":     {"No answer", "Try again in a few minutes", "Try again", false, true},
		"timeout":         {"Over an hour", "may have charged up to $0.040", "Try again", false, false},
		"provider":        {"Provider failure", "may have charged up to $0.040", "Try again", true, false},
		"interrupted":     {"Run interrupted", "was not sent again", "Try again", false, false},
		"storage":         {"Not saved", "The provider charged for it", "Try again", false, false},
		"no_adapter":      {"Did not work", "cannot run OpenRouter models", "Edit the prompt", false, true},
		"invalid_request": {"Not sent", "does not take one of these settings", "Edit the prompt", false, true},
		"internal":        {"Did not work", "It did not work. Try again.", "Try again", false, false},
	} {
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("fail:`+cls+`", (s) => s.failed);`)
		text, _ := s["text"].(string)
		acts, _ := s["acts"].([]any)
		first := ""
		if len(acts) > 0 {
			first, _ = acts[0].(string)
		}
		if s["timeout"] == true || s["label"] != w.label || !strings.Contains(s["why"].(string), w.why) || first != w.first || s["dots"] != true {
			t.Errorf("%s: %v", cls, s)
		}
		if shown := strings.Contains(text, fakeMessage); shown != w.said || strings.Contains(s["said"].(string), fakeMessage) != w.said {
			t.Errorf("%s: the provider's message shown %v, want %v on the frame: %q", cls, shown, w.said, text)
		}
		if zero := strings.HasSuffix(s["line"].(string), " · $0"); zero != w.zero || w.zero && strings.Contains(text, "Nothing was charged") {
			t.Errorf("%s: the price shown as $0 %v, want %v: %q", cls, zero, w.zero, s["line"])
		}
	}

	// A refusal for a real person in a file says so in framy's words, with no
	// JSON, and offers another model; Another model opens the picker.
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("faces", (s) => s.failed);
	const other = [...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Another model");
	other?.click();
	await wait(500);
	s.picker = !document.querySelector("#picker").hidden;
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	await wait(500);
	return s;`)
	if acts := fmt.Sprint(s["acts"]); s["label"] != "A person in the clip" || !strings.Contains(s["why"].(string), "does not take photos or clips with real people") ||
		!strings.HasPrefix(acts, "[Another model") || s["said"] != "" || strings.Contains(s["text"].(string), "{") || s["picker"] != true ||
		!strings.HasSuffix(s["line"].(string), " · $0") {
		t.Errorf("faces: %v", s)
	}
	// Class faces, with the provider's words only, names the file from them.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("faces:photo", (s) => s.failed);`)
	if acts := fmt.Sprint(s["acts"]); s["label"] != "A person in the photo" || s["said"] != "" || !strings.HasPrefix(acts, "[Another model") ||
		!strings.HasSuffix(s["line"].(string), " · $0") {
		t.Errorf("faces class: %v", s)
	}
	// Any other refusal in the provider's JSON shows only its message.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("wrapped", (s) => s.failed);`)
	if said := s["said"].(string); !strings.HasSuffix(said, "said: “The duration is too long.”") || strings.ContainsAny(said, "{}") {
		t.Errorf("wrapped refusal: %q", said)
	}

	// A job that waits to go again counts the time down, and Cancel stops it.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("retry", (s) => s.label === "Waiting to try again");
	const before = s.sub;
	let after = before;
	for (let i = 0; i < 30 && after === before; i++) { await wait(100); after = stage().sub; }
	const cancel = [...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Cancel");
	cancel?.click();
	for (let i = 0; i < 40 && stage().label !== "Cancelled"; i++) await wait(100);
	return { ...s, before, after, can: !!cancel, end: stage() };`)
	before, _ := s["before"].(string)
	if !strings.HasPrefix(before, "OpenRouter asked to wait. Next try in 0:") || s["after"] == before || s["can"] != true || s["dots"] != true {
		t.Errorf("retry: %v", s)
	}
	if end := s["end"].(map[string]any); end["label"] != "Cancelled" || end["why"] != "" || !strings.HasSuffix(end["line"].(string), " · $0") {
		t.Errorf("cancelled retry: %v", end)
	}

	// A job already running cannot be stopped, and the stage says so instead of a Cancel.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("running", (s) => s.label === "Making it");`)
	if acts := fmt.Sprint(s["acts"]); !strings.Contains(acts, "being made already") || strings.Contains(acts, "Cancel") || s["dots"] != true ||
		s["sub"] != "" || !strings.HasPrefix(s["line"].(string), "MAI-Image") {
		t.Errorf("running: %v", s)
	}

	// Refusals before any job: what happened and what helps.
	for code, w := range map[string][2]string{
		"price_changed":   {"The price went up", "Create for ≈ $0.500"},
		"free_closed":     {"Free runs paused", "On my OpenRouter key"},
		"wrong_inputs":    {"The files do not fit", "Edit the prompt"},
		"unknown_setting": {"Not sent", "Edit the prompt"},
	} {
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("refuse:`+code+`", (s) => s.failed);`)
		acts, _ := s["acts"].([]any)
		if s["label"] != w[0] || len(acts) == 0 || acts[0] != w[1] || strings.Contains(s["text"].(string), fakeMessage) || s["dots"] != true {
			t.Errorf("refusal %s: %v", code, s)
		}
	}

	// Polling instead of the stream still brings the result, and says it polls.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	let tagged = false;
	const s = await press("poll", (s) => { tagged ||= /checked every 5/.test(s.line); return s.done; });
	return { ...s, tagged };`)
	if s["done"] != true || s["tagged"] != true {
		t.Errorf("poll: %v", s)
	}

	// A result: the buttons on it, and Edit puts the prompt back in the composer.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("done", (s) => s.done);
	// The picture shows through the dots, and then the dots go.
	const revealing = stage().dots;
	for (let i = 0; i < 40 && stage().dots; i++) await wait(100);
	s.revealing = revealing; s.settled = !stage().dots && stage().done;
	s.twice = !document.querySelector("#stage .jprompt").hidden;
	document.querySelector("#prompt").value = "";
	document.querySelector("#prompt").dispatchEvent(new Event("input", { bubbles: true }));
	s.once = !document.querySelector("#stage .jprompt").hidden;
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Edit the prompt").click();
	await wait(600);
	return { ...s, prompt: document.querySelector("#prompt").value, ghost: !!document.querySelector("#stage .ghost") };`)
	if acts := fmt.Sprint(s["acts"]); s["done"] != true || !strings.HasPrefix(acts, "[Download Again Edit the prompt") || s["prompt"] != "done" || s["ghost"] != true ||
		s["revealing"] != true || s["settled"] != true || s["twice"] != false || s["once"] != true {
		t.Errorf("result: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserRunsPhone checks a failure on a phone: the reason and its
// buttons sit over the dock and nothing scrolls sideways.
func TestBrowserRunsPhone(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	for _, size := range [][2]int64{{390, 844}, {320, 568}} {
		ctx := browser(t, ts.URL, size[0], size[1], true)
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("fail:moderation", (s) => s.failed);
	await wait(500);
	const dock = document.querySelector("#composer").getBoundingClientRect(), acts = document.querySelector("#stage .jacts").getBoundingClientRect();
	scrollTo(0, document.documentElement.scrollHeight);
	await wait(100);
	const a2 = document.querySelector("#stage .jacts").getBoundingClientRect();
	return { ...s, overflow: document.documentElement.scrollWidth - innerWidth, over: Math.round(dock.top - a2.bottom), first: Math.round(dock.top - acts.bottom) };`)
		if s["failed"] != true || s["overflow"].(float64) > 0 || s["over"].(float64) < 0 {
			t.Errorf("%dx%d: %v", size[0], size[1], s)
		}
		noErrors(t, ctx)
	}
}

// fakeTelegram stands in for telegram-web-app.js: what the Mini App asks of
// Telegram is kept in window.__tg for the checks to read, and its main
// button is pressed by calling window.__tg.main().
const fakeTelegram = `(() => {
  const tg = window.__tg = { shared: [], links: [], main: null, params: null, back: false, backClick: null, settings: null, settingsShown: false, events: {} };
  const button = { setParams(p) { tg.params = { ...tg.params, ...p }; }, show() {}, hide() {}, onClick(fn) { tg.main = fn; } };
  window.Telegram = { WebApp: {
    version: "8.0", platform: "ios", initData: "",
    isVersionAtLeast: (v) => parseFloat(v) <= 8,
    ready() {}, expand() {}, setHeaderColor() {}, setBackgroundColor() {}, setBottomBarColor() {}, disableVerticalSwipes() {},
    MainButton: button, BackButton: { show() { tg.back = true; }, hide() { tg.back = false; }, onClick(fn) { tg.backClick = fn; } },
    onEvent(name, fn) { tg.events[name] = fn; },
    shareMessage(id) { tg.shared.push(id); },
    downloadFile() {}, openLink(u) { tg.links.push(u); },
    SettingsButton: { show() { tg.settingsShown = true; }, hide() { tg.settingsShown = false; }, onClick(fn) { tg.settings = fn; } },
  } };
})();`

// miniBrowser opens the page as the Mini App on a phone: with Telegram's
// launch data in the fragment, and fakeTelegram served for Telegram's script.
func miniBrowser(t *testing.T, url string) context.Context {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(390, 844), chromedp.NoSandbox)
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelC := chromedp.NewContext(actx)
	t.Cleanup(func() { cancelC(); cancelA() })
	if _, err := chromedp.Call(ctx, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*telegram-web-app.js*"}}}); err != nil {
		t.Fatal(err)
	}
	paused := chromedp.Events(ctx, fetch.RequestPaused)
	go func() {
		for ev, err := range paused {
			if err != nil {
				return
			}
			_, _ = chromedp.Call(ctx, fetch.FulfillRequest, fetch.FulfillRequestParams{RequestID: ev.RequestID, ResponseCode: 200,
				ResponseHeaders: []*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/javascript"}}, Body: []byte(fakeTelegram)})
		}
	}()
	user := `{"id":1,"first_name":"Ada","language_code":"en"}`
	init := "auth_date=1&user=" + neturl.QueryEscape(user) + "&hash=test"
	err := chromedp.Do(ctx,
		chromedp.EmulateViewport(390, 844, chromedp.EmulateMobile, chromedp.EmulateTouch, chromedp.EmulateScale(2)),
		chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: watch})
			return err
		}),
		chromedp.Navigate(url+"/#tgWebAppData="+neturl.QueryEscape(init)+"&tgWebAppVersion=8.0&tgWebAppPlatform=ios"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	return ctx
}

// TestBrowserMiniShare checks Share in the Mini App: a result goes to
// Telegram's chat picker as the prepared message framy saved, and one
// Telegram will not take says to download it. The site has no Share.
func TestBrowserMiniShare(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := miniBrowser(t, ts.URL)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	for (let i = 0; i < 50 && !window.__tg?.main; i++) await wait(100);
	const go = async (prompt) => {
		const p = document.querySelector("#prompt");
		p.value = prompt; p.dispatchEvent(new Event("input"));
		await wait(80);
		window.__tg.main();
		for (let i = 0; i < 140 && !stage().done; i++) await wait(100);
		return [...document.querySelectorAll("#stage .jacts > button")].find((b) => b.getAttribute("aria-label") === "Share");
	};
	const b1 = await go("done");
	b1?.click();
	for (let i = 0; i < 30 && !window.__tg.shared.length; i++) await wait(100);
	document.querySelector("#stage .jacts > button[aria-label='Make another']")?.click();
	await wait(300);
	const b2 = await go("done:big");
	b2?.click();
	let toast = "";
	for (let i = 0; i < 30 && !toast; i++) { await wait(100); toast = document.querySelector(".toast")?.textContent || ""; }
	return { found: !!b1 && !!b2, shared: window.__tg.shared, toast, overflow: document.documentElement.scrollWidth - innerWidth };`)
	if s["found"] != true || fmt.Sprint(s["shared"]) != "[prep-7]" || !strings.Contains(fmt.Sprint(s["toast"]), "Download it instead") || s["overflow"].(float64) > 0 {
		t.Errorf("share: %v", s)
	}
	noErrors(t, ctx)

	site := browser(t, ts.URL, 1440, 900, false)
	acts := js[string](t, site, pageHelpers+runHelpers+`return (await press("done", (s) => s.done)).acts.join(" ");`)
	if strings.Contains(acts, "Share") {
		t.Errorf("the site offers Share: %s", acts)
	}
}

// TestBrowserRunSmooth checks the halftone on the stage keeps the main thread
// free on the laptop's slow CPU (see TestBrowserSmooth): two seconds of the
// generating loop, then the result showing through the dots.
func TestBrowserRunSmooth(t *testing.T) {
	for _, size := range [][2]int64{{1440, 900}, {390, 844}} {
		runs := &fakeRuns{}
		ts := fakeSite(t, runs.mount)
		ctx := browser(t, ts.URL, size[0], size[1], size[0] < 960)
		scale := slowCPU(t, ctx)
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	await wait(2500); // the idle prefetch of the picker's prices
	window.__long = [];
	const t0 = performance.now();
	const s = await press("slow", (s) => s.done);
	await wait(2500);
	const long = window.__long.filter(([at]) => at > t0).map(([, ms]) => ms);
	// A task known to be long, to show the observer works here.
	window.__long = [];
	await new Promise((r) => setTimeout(() => { const t = performance.now(); while (performance.now() - t < 120); r(); }));
	await wait(300);
	return { ...s, long, works: window.__long.some(([, ms]) => ms >= 100), settled: !stage().dots };`)
		if s["works"] != true {
			t.Fatal("the browser reports no long tasks, so the check cannot work")
		}
		worst := 0.0
		for _, v := range s["long"].([]any) {
			worst = max(worst, v.(float64)*scale)
		}
		if s["done"] != true || s["settled"] != true || worst > 50 {
			t.Errorf("%dx%d: the longest task %.0f ms on the laptop: %v", size[0], size[1], worst, s)
		}
		t.Logf("%dx%d: longest task %.0f ms on the laptop", size[0], size[1], worst)
		noErrors(t, ctx)
	}
}

// TestBrowserVideoSettings checks the form of a video model that lists a size
// beside its own aspect_ratio and resolution: one row of each, and no size in
// the run. And the Fast switch, which the server never sees: on, the run goes
// to the fast mode of the model.
func TestBrowserVideoSettings(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const sent = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/jobs") && o?.method === "POST") sent.push(JSON.parse(o.body)); return fetch0(u, o); };
	const rows = () => [...document.querySelectorAll("#how [data-k^='h-f-']")].filter(vis).map((r) => r.querySelector(".lt").textContent);
	const dets = {};
	async function pick(fam, label) {
	  document.querySelector("#how .mhead").click();
	  await wait(700);
	  const q = document.querySelector("#pksearch");
	  q.value = fam;
	  q.dispatchEvent(new Event("input", { bubbles: true }));
	  await wait(800);
	  dets[fam] = document.querySelector("#pkd .dl")?.textContent || "";
	  // A variant is a pill in its family's row; a family with one current variant is the row itself.
	  const row = document.querySelector("#pkrows .frow");
	  ([...row.querySelectorAll(".pill:not(.old)")].find((b) => b.textContent.trim() === label) || row.querySelector(".fhit")).click();
	  await wait(900);
	}
	const fast = (on) => { const b = document.querySelector("#how [data-k='h-f-fast'] button"); if (b.textContent.startsWith("On") !== on) b.click(); };
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	await pick("Seedance", "2.0 Mini");
	const seedance = rows();
	// A photo attached: the one hint under the files says the model takes no real people.
	const cv = document.createElement("canvas"); cv.width = 64; cv.height = 36;
	const png = await new Promise((r) => cv.toBlob(r, "image/png"));
	const dt = new DataTransfer(); dt.items.add(new File([png], "p.png", { type: "image/png" }));
	const inp = document.querySelector("#file"); inp.files = dt.files; inp.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(1200);
	const hints = [...document.querySelectorAll("#files .fhint")].map((x) => x.textContent);
	document.querySelector("#files .thumb .rm")?.click();
	await wait(700);
	await press("done", () => sent.length > 0);
	await pick("Wan", "3.0");
	const wan = rows();
	fast(true);
	await wait(400);
	await press("done", () => sent.length > 1);
	fast(false);
	await wait(400);
	await press("done", () => sent.length > 2);
	return { seedance, wan, sent, dets, hints };`)
	// Seedance refuses real people in files, and its card says so before the run.
	if dets, _ := s["dets"].(map[string]any); !strings.Contains(fmt.Sprint(dets["Seedance"]), "People in filesNot taken") ||
		strings.Contains(fmt.Sprint(dets["Wan"]), "People in files") {
		t.Errorf("people in files: %v", dets)
	}
	if hints := fmt.Sprint(s["hints"]); hints != "[Does not take real people in photos or clips.]" {
		t.Errorf("the hint under a photo for Seedance: %s", hints)
	}
	count := func(rows []any, label string) (n int) {
		for _, r := range rows {
			if r == label {
				n++
			}
		}
		return n
	}
	seedance, _ := s["seedance"].([]any)
	if count(seedance, "Ratio") != 1 || count(seedance, "Resolution") != 1 {
		t.Errorf("Seedance 2.0 Mini: rows %v, want one Ratio and one Resolution", seedance)
	}
	sent, _ := s["sent"].([]any)
	if len(sent) != 3 {
		t.Fatalf("runs sent: %v", sent)
	}
	body := func(i int) (string, map[string]any) {
		b := sent[i].(map[string]any)
		set, _ := b["settings"].(map[string]any)
		return b["offer"].(string), set
	}
	if offer, set := body(0); offer != "openrouter:bytedance/seedance-2.0-mini" || set["size"] != nil || set["aspect_ratio"] == nil || set["resolution"] == nil {
		t.Errorf("Seedance run: %s %v", offer, set)
	}
	if wan, _ := s["wan"].([]any); count(wan, "Fast mode") != 1 {
		t.Errorf("Wan 3.0: rows %v, want a Fast switch", wan)
	}
	if offer, set := body(1); offer != "openrouter:alibaba/wan-3.0-prime" || set["fast"] != nil {
		t.Errorf("Wan 3.0 with Fast on: %s %v", offer, set)
	}
	if offer, set := body(2); offer != "openrouter:alibaba/wan-3.0" || set["fast"] != nil {
		t.Errorf("Wan 3.0 with Fast off: %s %v", offer, set)
	}
	noErrors(t, ctx)
}

// TestBrowserRunSound checks a run whose sound the model refused: framy's
// words, nothing charged, and Try without sound, which sends the same run
// again with the sound off.
func TestBrowserRunSound(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const sent = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/jobs") && o?.method === "POST") sent.push(JSON.parse(o.body)); return fetch0(u, o); };
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "Seedance";
	q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	const row = document.querySelector("#pkrows .frow");
	([...row.querySelectorAll(".pill:not(.old)")].find((b) => b.textContent.trim() === "2.0 Mini") || row.querySelector(".fhit")).click();
	await wait(900);
	const s = await press("fail:sound", (s) => s.failed);
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Try without sound")?.click();
	for (let i = 0; i < 40 && sent.length < 2; i++) await wait(100);
	s.sound = sent.map((b) => b.settings.generate_audio);
	s.offers = sent.map((b) => b.offer);
	return s;`)
	if acts := fmt.Sprint(s["acts"]); s["label"] != "Sound refused" || s["why"] != "The model's sound may break copyright." || s["said"] != "" ||
		!strings.HasPrefix(acts, "[Try without sound Edit the prompt") || !strings.HasSuffix(s["line"].(string), " · $0") {
		t.Errorf("sound: %v", s)
	}
	if fmt.Sprint(s["sound"]) != "[true false]" || fmt.Sprint(s["offers"]) != "[openrouter:bytedance/seedance-2.0-mini openrouter:bytedance/seedance-2.0-mini]" {
		t.Errorf("without sound: sent %v %v", s["sound"], s["offers"])
	}
	noErrors(t, ctx)
}
