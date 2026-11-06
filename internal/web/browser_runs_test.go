// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	neturl "net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
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

// fakeOwnLine is framy's own line about a run that ended without a word
// from the provider: never shown as what the provider said.
const fakeOwnLine = "moderation: gemini blocked the prompt (SAFETY)"

// fakeWrapped is any other refusal stored the same way.
const fakeWrapped = `refused: openrouter 400: HTTP 400: {"error":{"code":"InvalidParameter","message":"The duration is too long. Request id: 0217abd"}}`

// loopWebM is a clip Chrome plays: two seconds of the field's colour, 32×18,
// VP8 (ffmpeg -f lavfi -i color=c=0x00606E:s=32x18:r=8:d=2 -c:v libvpx).
const loopWebM = "GkXfo59ChoEBQveBAULygQRC84EIQoKEd2VibUKHgQJChYECGFOAZwEAAAAAAANoEU2bdLpNu4tTq4QVSalmU6yBoU27i1OrhBZUrmtTrIHWTbuMU6uEElTDZ1OsggEyTbuMU6uEHFO7a1OsggNS7AEAAAAAAABZAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAVSalmsCrXsYMPQkBNgIxMYXZmNjMuMS4xMDFXQYxMYXZmNjMuMS4xMDFEiYhAn0AAAAAAABZUrmvXrgEAAAAAAABO14EBc8WILdqadVgB8fKcgQAitZyDdW5kiIEAhoVWX1ZQOIOBASPjg4QHc1lA4JCwgSC6gRKagQJVsIRVuYEBVe6BAOwBAAAAAAAAAgAAElTDZ/pzc59jwIBnyJlFo4dFTkNPREVSRIeMTGF2ZjYzLjEuMTAxc3PVY8CLY8WILdqadVgB8fJnyKBFo4dFTkNPREVSRIeTTGF2YzYzLjEuMTAxIGxpYnZweGfIoUWjiERVUkFUSU9ORIeTMDA6MDA6MDIuMDAwMDAwMDAwAB9DtnVBm+eBAKO9gQAAgNACAJ0BKiAAEgAARwiFhYiFhIgCAgJ1qgP4AgghCD0A/v4ND//SbPE2eJs+SR/+djfnY352N/5zIKOVgQB9ALEBAAEQEAAYABhYL/QACHAAo5WBAPoAsQEAARAQABgAGFgv9AAIcACjlYEBdwCxAQABEBAAGAAYWC/0AAhwAKOVgQH0ALEBAAEQEAAYABhYL/QACHAAo5WBAnEAsQEAARAQABgAGFgv9AAIcACjlYEC7gCxAQABEBAAGAAYWC/0AAhwAKOVgQNrALEBAAEQEBRgAGFgv9AAIcAAo5WBA+gAsQEAARAQABgAGFgv9AAIcACjlYEEZQCxAQABEBAAGAAYWC/0AAhwAKOVgQTiALEBAAEQEAAYABhYL/QACHAAo5WBBV8AsQEAARAQABgAGFgv9AAIcACjlYEF3ACxAQABEBAAGAAYWC/0AAhwAKOVgQZZALEBAAEQEAAYABhYL/QACHAAo5WBBtYAsQEAARAQABgAGFgv9AAIcACjlYEHUwCxAQABEBAAGAAYWC/0AAhwABxTu2uRu4+zgQC3iveBAfGCAbHwgQM="

// fakeSuggestion is a Search Suggestion as Google writes it: its own <style>
// and a chip that links to Google's results. fakeOdd has what Google's do
// not: a style attribute, an event handler, a script, a form that would post
// to framy and a link that is a script.
const fakeSuggestion = `<style>
.container { align-items: center; border-radius: 8px; display: flex; font-family: Google Sans, Roboto, sans-serif; font-size: 14px; line-height: 20px; padding: 8px 12px; }
.chip { display: inline-block; border: solid 1px; border-radius: 16px; min-width: 14px; padding: 5px 16px; text-align: center; user-select: none; margin: 0 8px; }
.carousel { overflow: auto; scrollbar-width: none; white-space: nowrap; margin-right: -12px; }
@media (prefers-color-scheme: dark) { .container { background-color: #1f1f1f; } .chip { background-color: #2c2c2c; border-color: #3c4043; color: #fff; text-decoration: none; } }
@media (prefers-color-scheme: light) { .container { background-color: #fafafa; } .chip { background-color: #fff; border-color: #d2d2d2; color: #5e5e5e; text-decoration: none; } }
</style>
<div class="container"><div class="headline"><svg width="18" height="18" viewBox="0 0 18 18"><circle cx="9" cy="9" r="8" fill="#4285f4"/></svg></div>
<div class="carousel"><a class="chip" href="https://www.google.com/search?q=eiffel+tower+at+night&amp;client=app-vertex-grounding">eiffel tower at night</a></div></div>`

const fakeOdd = `<div class="headline" style="display: flex; margin-right: 4px">odd</div><img src="data:," onerror="parent.__ran = true">` +
	`<script>parent.__ran = true</script><form method="post" action="/auth/logout"><button>out</button></form><a class="bad" href="javascript:parent.__ran = true">bad</a>`

// fakeRuns is the API of a signed-in person with a working OpenRouter key and
// no free pictures left, whose jobs follow the prompt: "fail:<class>" fails
// with that class, "expired:<class>" passes the run's hour with it as the
// last poll's, "retry" waits for a retry, "running" runs and never ends,
// "poll" is read without the event stream, "done" makes a picture ("slow"
// after two seconds of running), "faces" and "wrapped" are refused by the
// provider in its own JSON, and "refuse:<code>" is refused before any job.
type fakeRuns struct {
	mu      sync.Mutex
	next    int64
	jobs    map[int64]*fakeJob
	shares  int              // how many results were prepared for sharing
	batches []map[string]any // the bodies of POST /api/batches, in order
	lang    string           // the person's language, English when empty
	offDM   bool             // the site's results stay off the chat (Settings)
	// out is closed when the person signs out elsewhere: the open streams
	// end, as the server ends them, and every route answers 401 after.
	out     chan struct{}
	outOnce sync.Once
	// launchOld: the Mini App's launch data is past its hour, so POST
	// /auth/miniapp answers init_data_expired.
	launchOld atomic.Bool
	gone      bool // signed out: a "stream:401" job was watched
	expired   bool // the Mini App's launch data is refused, as an hour after Telegram signed it
	trades    int  // how many times the launch data was traded for a token
	// keyless: no key is connected, and a free picture is left by the meter,
	// as when the bot took the last one since the page read it.
	keyless bool
}

// signOutElsewhere is "Sign out everywhere" pressed on another device.
func (f *fakeRuns) signOutElsewhere() { f.outOnce.Do(func() { close(f.out) }) }

// signedOut answers 401 sign_in once the person signed out elsewhere.
func (f *fakeRuns) signedOut(w http.ResponseWriter) bool {
	select {
	case <-f.out:
		writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in"})
		return true
	default:
		return false
	}

}

type fakeJob struct {
	steps []map[string]any // what the events say, in order
	shown int              // how many of them were said
	stop  chan struct{}    // closed when the job is cancelled
}

func (f *fakeRuns) mount(srv *Server) {
	f.jobs = map[int64]*fakeJob{}
	f.out = make(chan struct{})
	lang := cmp.Or(f.lang, "en")
	srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
		if f.signedOut(w) {
			return
		}

		f.mu.Lock()
		gone, keyless := f.gone, f.keyless
		f.mu.Unlock()
		if gone {
			writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in"})
			return
		}
		conns := []map[string]any{{"id": "c1", "provider": "openrouter", "last4": "abcd", "status": "ok"}}
		images := map[string]any{"used": 5, "limit": 5, "left": 0, "window_seconds": 86400, "frees_at": time.Now().Add(3 * time.Hour).UTC()}
		if keyless {
			conns, images = []map[string]any{}, map[string]any{"used": 4, "limit": 5, "left": 1, "window_seconds": 86400}
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"user_id": 1, "name": "Ada", "language": lang, "currency": "USD", "deliver_to_telegram": !f.offDM, "connections": conns,
			"free": map[string]any{
				"images": images,
				"videos": map[string]any{"used": 0, "limit": 1, "left": 1, "window_seconds": 604800},
			},
		})
	})
	srv.HandleFunc("POST /api/jobs", f.create)
	srv.HandleFunc("POST /api/batches", f.batch)
	srv.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if f.signedOut(w) {
			return
		}
		j, id := f.job(r)
		if j == nil {
			writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
			return
		}
		f.mu.Lock()
		// Read without the stream, the job moves one step a read.
		if p := j.steps[0]["prompt"]; (p == "poll" || p == "stream:500") && j.shown < len(j.steps) {
			j.shown++
		}
		cur := j.steps[max(0, j.shown-1)]
		f.mu.Unlock()
		writeTestJSON(w, http.StatusOK, withID(cur, id))
	})
	srv.HandleFunc("GET /api/jobs/{id}/events", f.events, Stream())
	// The library: every job as it stands now, the newest first.
	srv.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for id := f.next; id > 0; id-- {
			if j := f.jobs[id]; j != nil {
				m := withID(j.steps[len(j.steps)-1], id)
				m["created_at"] = time.Now().UTC()
				out = append(out, m)
			}
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"jobs": out})
	})
	srv.HandleFunc("POST /api/jobs/{id}/cancel", f.cancel)
	srv.HandleFunc("POST /auth/miniapp", func(w http.ResponseWriter, _ *http.Request) {
		if f.launchOld.Load() {
			writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "init_data_expired"})
			return
		}

		f.mu.Lock()
		f.trades++
		expired := f.expired
		f.mu.Unlock()
		if expired {
			writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "init_data_expired"})
			return
		}
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
	// The last frame of any clip: a picture framy keeps, by id.
	srv.HandleFunc("POST /api/files/{id}/last-frame", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, http.StatusOK, map[string]any{"id": 72, "content_type": "image/jpeg", "bytes": 30000, "width": 832, "height": 480})
	})
	srv.HandleFunc("GET /test/clip.mp4", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "video/mp4") })
	srv.HandleFunc("GET /test/joined.mp4", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "video/mp4") })
	srv.HandleFunc("GET /test/loop.webm", func(w http.ResponseWriter, r *http.Request) {
		b, _ := base64.StdEncoding.DecodeString(loopWebM)
		w.Header().Set("Content-Type", "video/webm")
		http.ServeContent(w, r, "loop.webm", time.Time{}, bytes.NewReader(b))
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
	srv.HandleFunc("GET /test/result.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1365 1024"><rect width="1365" height="1024" fill="#00606E"/></svg>`)
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
	if f.signedOut(w) {
		return
	}
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
		case "free_limit":
			status = http.StatusTooManyRequests
			body["frees_at"] = time.Now().Add(50 * time.Hour).UTC()
		case "key_in_prompt":
			body["provider"] = "openrouter"
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
		steps = append(steps, at("running", nil), at("failed", map[string]any{"error": map[string]any{"class": cls, "message": fakeMessage, "from_provider": true}}))
	case strings.HasPrefix(p, "own:"):
		// framy's own line, as a run the provider ended with no words of its own has.
		steps = append(steps, at("running", nil), at("failed", map[string]any{"error": map[string]any{"class": strings.TrimPrefix(p, "own:"), "message": fakeOwnLine}}))
	case strings.HasPrefix(p, "late:"):
		// A sent run whose key stopped working while framy asked about it, for the hour.
		steps = append(steps, at("running", nil), at("expired", map[string]any{"error": map[string]any{"class": strings.TrimPrefix(p, "late:"), "message": fakeOwnLine}}))
	case strings.HasPrefix(p, "expired:"):
		cls := strings.TrimPrefix(p, "expired:")
		steps = append(steps, at("running", nil), at("expired", map[string]any{"error": map[string]any{"class": cls, "message": fakeMessage}}))
	case p == "faces", p == "wrapped":
		msg := map[string]string{"faces": fakeFaces, "wrapped": fakeWrapped}[p]
		steps = append(steps, at("running", nil), at("failed", map[string]any{"error": map[string]any{"class": "refused", "message": msg, "from_provider": p == "wrapped"}}))
	case p == "faces:photo":
		steps = append(steps, at("running", nil), at("failed", map[string]any{"error": map[string]any{"class": "faces", "message": fakeFacesWords}}))
	case p == "retry":
		steps = append(steps, at("queued", map[string]any{"error": map[string]any{"class": "rate_limited", "message": fakeMessage},
			"retry_at": time.Now().Add(30 * time.Second).UTC()}))
	case p == "running", p == "stream:404", p == "stream:401", p == "stream:expired":
		steps = append(steps, at("running", nil))
	case p == "done:video":
		url := "http://" + r.Host + "/test/clip.mp4"
		steps = append(steps, at("running", map[string]any{"kind": "video"}), at("done", map[string]any{"kind": "video",
			"cost":    map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"},
			"results": []map[string]any{{"id": 9, "content_type": "video/mp4", "url": url, "download": url + "?download=1", "width": 832, "height": 480, "seconds": 5}}}))
	case p == "done:nolength":
		// A video whose length neither framy nor the browser knows: the file gives nothing.
		url := "http://" + r.Host + "/test/clip.mp4"
		steps = append(steps, at("running", map[string]any{"kind": "video"}), at("done", map[string]any{"kind": "video",
			"cost":    map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"},
			"results": []map[string]any{{"id": 9, "content_type": "video/mp4", "url": url, "download": url + "?download=1", "width": 832, "height": 480}}}))
	case p == "done:loop":
		// A clip the browser plays.
		url := "http://" + r.Host + "/test/loop.webm"
		steps = append(steps, at("running", map[string]any{"kind": "video"}), at("done", map[string]any{"kind": "video",
			"cost":    map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"},
			"results": []map[string]any{{"id": 11, "content_type": "video/webm", "url": url, "download": url + "?download=1", "width": 32, "height": 18, "seconds": 2}}}))
	case p == "done:joined":
		// A run that went on from a clip's last frame, joined to that clip.
		url, joined := "http://"+r.Host+"/test/clip.mp4", "http://"+r.Host+"/test/joined.mp4"
		steps = append(steps, at("running", map[string]any{"kind": "video"}), at("done", map[string]any{"kind": "video",
			"cost":    map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"},
			"results": []map[string]any{{"id": 9, "content_type": "video/mp4", "url": url, "download": url + "?download=1", "width": 832, "height": 480, "seconds": 5}},
			"joined": map[string]any{"id": 10, "content_type": "video/mp4", "url": joined, "download": joined + "?download=1", "width": 832, "height": 480, "seconds": 9.96,
				"segments": []float64{0, 5}}}))
	case p == "done:svg":
		// A vector picture, as Recraft's vector models draw one.
		url := "http://" + r.Host + "/test/result.svg"
		steps = append(steps, at("running", nil), at("done", map[string]any{"cost": map[string]any{"usd": 0.08, "amount": 0.08, "currency": "USD", "label": "$0.080"},
			"results": []map[string]any{{"id": 11, "content_type": "image/svg+xml", "url": url, "download": url + "?download=1", "width": 1365, "height": 1024}}}))

	case p == "done:grounded":
		// A run grounded in Google Search: Google's suggestion and the pages it cites.
		url := "http://" + r.Host + "/test/result.png"
		steps = append(steps, at("running", nil), at("done", map[string]any{"cost": map[string]any{"usd": 0.04, "amount": 0.04, "currency": "USD", "label": "$0.040"},
			"results": []map[string]any{{"id": 7, "content_type": "image/png", "url": url, "download": url + "?download=1", "width": 1024, "height": 1024}},
			"grounding": map[string]any{"suggestions": []string{fakeSuggestion, fakeOdd}, "queries": []string{"eiffel tower at night"},
				"sources": []map[string]any{{"url": "https://en.wikipedia.org/wiki/Eiffel_Tower", "title": "wikipedia.org"}, {"url": "https://www.toureiffel.paris/en"}, {"url": "javascript:alert(1)", "title": "bad"}}}}))
	case p == "slow":
		for range 14 {
			steps = append(steps, at("running", nil))
		}
		fallthrough
	case p == "done", p == "poll", p == "done:big", p == "stream:500":
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

// batch answers a press of several frames as the server does: the frames
// with the same offer, payer and settings go as one job, whose results stand
// on those frames in order. "frames:<a>,<b>,…" gives the jobs, in order, the
// outcomes done, grounded (done, with Google's suggestion and a source),
// fail:<class> or running; any other prompt makes them all
// done, and "refuse:<code>" refuses the press for its second frame.
func (f *fakeRuns) batch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	var in struct {
		Prompt string `json:"prompt"`
		Frames []struct {
			Offer    string         `json:"offer"`
			Pay      string         `json:"pay"`
			Settings map[string]any `json:"settings"`
		} `json:"frames"`
	}
	var raw map[string]any
	if err != nil || json.Unmarshal(body, &in) != nil || json.Unmarshal(body, &raw) != nil || len(in.Frames) < 2 {
		writeTestJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
		return
	}
	f.mu.Lock()
	f.batches = append(f.batches, raw)
	f.mu.Unlock()
	if code, ok := strings.CutPrefix(in.Prompt, "refuse:"); ok {
		writeTestJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": code, "message": fakeMessage, "frame": 1})
		return
	}
	var groups [][]int
	seen := map[string]int{}
	for i, fr := range in.Frames {
		b, _ := json.Marshal(fr)
		if g, ok := seen[string(b)]; ok {
			groups[g] = append(groups[g], i)
			continue
		}
		seen[string(b)] = len(groups)
		groups = append(groups, []int{i})
	}
	outcomes, mapped := strings.CutPrefix(in.Prompt, "frames:")
	url := "http://" + r.Host + "/test/result.png"
	var out []map[string]any
	for g, frames := range groups {
		fr := in.Frames[frames[0]]
		set := map[string]any{}
		for k, v := range fr.Settings {
			set[k] = v
		}
		if len(frames) > 1 {
			set["n"] = len(frames)
		}
		usd := 0.04 * float64(len(frames))
		base := map[string]any{"kind": "image", "offer": fr.Offer, "model": strings.SplitN(fr.Offer, ":", 2)[1], "prompt": in.Prompt,
			"settings": set, "free": fr.Pay == "free", "surface": "site", "batch": "b1", "frames": frames,
			"estimate": map[string]any{"kind": "about", "usd": usd, "max_usd": usd, "currency": "USD", "label": fmt.Sprintf("$%.3f", usd)}}
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
		outcome := "done"
		if list := strings.Split(outcomes, ","); mapped && g < len(list) {
			outcome = list[g]
		}
		steps := []map[string]any{at("queued", nil), at("running", nil)}
		switch {
		case strings.HasPrefix(outcome, "fail:"):
			steps = append(steps, at("failed", map[string]any{"error": map[string]any{"class": strings.TrimPrefix(outcome, "fail:"), "message": fakeMessage, "from_provider": true}}))
		case outcome == "running":
		case outcome == "video":
			// Clips the browser plays.
			clip := "http://" + r.Host + "/test/loop.webm"
			var results []map[string]any
			for k := range frames {
				results = append(results, map[string]any{"id": 100 + 10*g + k, "content_type": "video/webm", "url": clip, "download": clip + "?download=1", "width": 32, "height": 18, "seconds": 2})
			}
			steps = append(steps, at("running", map[string]any{"kind": "video"}), at("done", map[string]any{"kind": "video", "results": results,
				"cost": map[string]any{"usd": usd, "amount": usd, "currency": "USD", "label": fmt.Sprintf("$%.3f", usd)}}))
		default:
			var grounding map[string]any
			if outcome == "grounded" {
				grounding = map[string]any{"suggestions": []string{fakeSuggestion}, "sources": []map[string]any{{"url": "https://en.wikipedia.org/wiki/Eiffel_Tower", "title": "wikipedia.org"}}}
			}
			var results []map[string]any
			for k := range frames {
				results = append(results, map[string]any{"id": 100 + 10*g + k, "content_type": "image/png", "url": url, "download": url + "?download=1", "width": 1024, "height": 1024})
			}
			done := at("done", map[string]any{"results": results,
				"cost": map[string]any{"usd": usd, "amount": usd, "currency": "USD", "label": fmt.Sprintf("$%.3f", usd)}})
			if grounding != nil {
				done["grounding"] = grounding
			}
			steps = append(steps, at("running", nil), done)
		}
		f.mu.Lock()
		f.next++
		id := f.next
		f.jobs[id] = &fakeJob{steps: steps, shown: 1, stop: make(chan struct{})}
		f.mu.Unlock()
		out = append(out, withID(steps[0], id))
	}
	writeTestJSON(w, http.StatusAccepted, map[string]any{"batch": "b1", "jobs": out})
}

// events says the job's steps a moment apart, then holds the stream open
// until the job is cancelled or the page goes.
func (f *fakeRuns) events(w http.ResponseWriter, r *http.Request) {
	if f.signedOut(w) {
		return
	}
	j, id := f.job(r)
	if j == nil {
		writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	switch j.steps[0]["prompt"] {
	case "poll":
		writeTestJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too_many_streams"})
		return
	case "stream:500":
		// framy failing, or the tunnel in front of it: an answer that is not the stream.
		w.Header().Set("Retry-After", "1")
		writeTestJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal"})
		return
	case "stream:404":
		writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	case "stream:401":
		f.mu.Lock()
		f.gone = true
		f.mu.Unlock()
		writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in"})
		return
	case "stream:expired":
		// The Mini App's token ran out and its launch data is too old to trade again.
		f.mu.Lock()
		f.expired = true
		f.mu.Unlock()
		writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in"})
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
		if st := j.steps[n]["status"]; st == "failed" || st == "expired" || st == "done" {
			return
		}
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			return
		case <-f.out:
			return
		}
	}
	select {
	case <-f.out:
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
  // The frame's words stand in it, or under it when it is too small for them (stage.js, caption).
  return { label: st.querySelector(".jbox .c b, .c.under b")?.textContent || "", sub: st.querySelector(".jbox .c small, .c.under small")?.textContent || "",
    why: st.querySelector(".jwhy")?.textContent || "", said: st.querySelector(".jbox .c .said, .c.under .said")?.textContent || "",
    acts: [...st.querySelectorAll(".jacts button, .jacts a, .jacts .busy")].map((b) => b.textContent.trim()),
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
		"money":           {"The key is out of credits", "", "Top up OpenRouter", false, true},
		"key":             {"The key does not work", "no longer accepts the key", "Replace the OpenRouter key", false, true},
		"refused":         {"Not accepted", "did not accept the request", "Edit the prompt", true, true},
		"region":          {"Region not served", "does not serve the country framy's server is in", "Another model", false, true},
		"rate_limited":    {"No answer", "Try again in a few minutes", "Try again", false, true},
		"unavailable":     {"No answer", "Try again in a few minutes", "Try again", false, true},
		"timeout":         {"Took too long", "may have charged up to $0.040", "Try again", false, false},
		"provider":        {"Provider failure", "may have charged up to $0.040", "Try again", true, false},
		"interrupted":     {"Run interrupted", "was not sent again", "Try again", false, false},
		"storage":         {"Not saved", "The provider charged for it", "Try again", false, false},
		"no_adapter":      {"Did not work", "cannot run OpenRouter models", "Edit the prompt", false, true},
		"invalid_request": {"Not sent", "does not take one of these settings", "Edit the prompt", false, true},
		"internal":        {"Did not work", "Something went wrong. Try again.", "Try again", false, false},
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

	// Only a run past its hour says so; whatever the last poll said, the
	// provider may have charged for it.
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("expired:provider", (s) => s.failed);`)
	if s["timeout"] == true || s["label"] != "Over an hour" || !strings.Contains(s["why"].(string), "may have charged up to $0.040") || s["said"] != "" {
		t.Errorf("expired: %v", s)
	}

	// A refusal for a real person in a file says so in framy's words, with no
	// JSON, and offers another model; Another model opens the picker.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("faces", (s) => s.failed);
	const other = [...document.querySelectorAll("#stage .jacts button")].find((b) => b.textContent.trim() === "Another model");
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
	// A refusal or a failure with no words of the provider's says nothing in
	// its name: framy's line about it is not the provider's.
	for _, cls := range []string{"moderation", "refused", "provider"} {
		s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("own:`+cls+`", (s) => s.failed);`)
		if s["said"] != "" || strings.Contains(s["text"].(string), "gemini blocked") {
			t.Errorf("%s with framy's own line: %v", cls, s)
		}
	}

	// A sent run whose key was refused, or ran short, while framy asked about
	// it went to the provider: it may be paid, and its price is not $0.
	for cls, w := range map[string]struct{ label, first string }{
		"key":   {"The key does not work", "Replace the OpenRouter key"},
		"money": {"No money on the key", "Top up OpenRouter"},
	} {
		s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("late:`+cls+`", (s) => s.failed);`)
		acts, _ := s["acts"].([]any)
		if s["timeout"] == true || s["label"] != w.label || !strings.Contains(s["why"].(string), "may have charged up to $0.040") ||
			strings.HasSuffix(s["line"].(string), " · $0") || len(acts) == 0 || acts[0] != w.first {
			t.Errorf("%s past the hour: %v", cls, s)
		}
	}

	// A job that waits to go again counts the time down, and Cancel stops it.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("retry", (s) => s.label === "Waiting to try again");
	const before = s.sub;
	let after = before;
	for (let i = 0; i < 30 && after === before; i++) { await wait(100); after = stage().sub; }
	const cancel = [...document.querySelectorAll("#stage .jacts button")].find((b) => b.textContent.trim() === "Cancel");
	cancel?.click();
	for (let i = 0; i < 40 && stage().label !== "Canceled"; i++) await wait(100);
	return { ...s, before, after, can: !!cancel, end: stage() };`)
	before, _ := s["before"].(string)
	if !strings.HasPrefix(before, "OpenRouter asked to wait. Next try in 0:") || s["after"] == before || s["can"] != true || s["dots"] != true {
		t.Errorf("retry: %v", s)
	}
	if end := s["end"].(map[string]any); end["label"] != "Canceled" || end["why"] != "" || !strings.HasSuffix(end["line"].(string), " · $0") {
		t.Errorf("cancelled retry: %v", end)
	}

	// A job already running cannot be stopped, and the stage says so instead
	// of a Cancel; under its label is the time since the press, never a percentage.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("running", (s) => s.label === "Making it");`)
	if acts := fmt.Sprint(s["acts"]); !strings.Contains(acts, "being made already") || strings.Contains(acts, "Cancel") || s["dots"] != true ||
		!strings.HasPrefix(fmt.Sprint(s["sub"]), "0:0") || !strings.HasPrefix(s["line"].(string), "MAI-Image") {
		t.Errorf("running: %v", s)
	}

	// Refusals before any job: what happened and what helps.
	for code, w := range map[string][2]string{
		"price_changed":   {"The price went up", "Create for ≈ $0.500"},
		"free_closed":     {"Free runs paused", "On your OpenRouter key"},
		"wrong_inputs":    {"The files do not fit", "Edit the prompt"},
		"unknown_setting": {"Not sent", "Edit the prompt"},
		// A key pasted into the prompt: its settings, where it belongs.
		"key_in_prompt": {"A key in the prompt", "Replace the OpenRouter key"},
	} {
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("refuse:`+code+`", (s) => s.failed);`)
		acts, _ := s["acts"].([]any)
		if s["label"] != w[0] || len(acts) == 0 || acts[0] != w[1] || strings.Contains(s["text"].(string), fakeMessage) || s["dots"] != true {
			t.Errorf("refusal %s: %v", code, s)
		}
	}
	// The free limit names the day and the time the next free run comes, in
	// the browser's time zone, not how long until it.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("refuse:free_limit", (s) => s.failed);`)
	if !regexp.MustCompile(`^The free images are used up\. The next free one: [A-Z][a-z]+ \d{1,2}\D+\d{1,2}:\d\d`).MatchString(fmt.Sprint(s["why"])) {
		t.Errorf("refusal free_limit: %v", s)
	}

	// Polling instead of the stream still brings the result, and says it
	// polls. Its pauses leave no listener behind on the job's signal.
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const P = AbortSignal.prototype, add0 = P.addEventListener, rm0 = P.removeEventListener, live = new WeakMap();
	let most = 0;
	P.addEventListener = function (type, fn, o) { if (type === "abort") { const l = live.get(this) || new Set(); l.add(fn); live.set(this, l); most = Math.max(most, l.size); } return add0.call(this, type, fn, o); };
	P.removeEventListener = function (type, fn, o) { if (type === "abort") live.get(this)?.delete(fn); return rm0.call(this, type, fn, o); };
	let tagged = false, s;
	try { s = await press("poll", (s) => { tagged ||= /checked every 5/.test(s.line); return s.done; }); }
	finally { P.addEventListener = add0; P.removeEventListener = rm0; }
	return { ...s, tagged, most };`)
	if s["done"] != true || s["tagged"] != true || s["most"] != float64(1) {
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
	[...document.querySelectorAll("#stage .jacts button")].find((b) => b.textContent.trim() === "Edit the prompt").click();
	await wait(600);
	return { ...s, prompt: document.querySelector("#prompt").value, ghost: !!document.querySelector("#stage .ghost") };`)
	if acts := fmt.Sprint(s["acts"]); s["done"] != true || !strings.HasPrefix(acts, "[Download Again Edit the prompt") || s["prompt"] != "done" || s["ghost"] != true ||
		s["revealing"] != true || s["settled"] != true || s["twice"] != false || s["once"] != true {
		t.Errorf("result: %v", s)
	}
	noErrors(t, ctx)
}

// Signed out everywhere from another device while a run goes: the server
// ends the run's stream, and the reconnect is refused. The page forgets the
// person and offers to sign in; the run stays, saying it goes on on the
// server, instead of hanging in the queue.
func TestBrowserASignOutElsewhereSaysTheRunGoesOn(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("running", (s) => s.label === "Making it");`)
	if s["timeout"] == true {
		t.Fatalf("the run never ran: %v", s)
	}
	runs.signOutElsewhere()
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	for (let i = 0; i < 80 && !document.querySelector(".top .signin"); i++) await wait(100);
	return { signin: !!document.querySelector(".top .signin"), me: !!document.querySelector("#meBtn"), ...stage() };`)
	if s["signin"] != true || s["me"] != false || s["sub"] != "Sign in again: the run goes on on the server" {
		t.Errorf("after a sign-out elsewhere: %v", s)
	}
}

// TestBrowserVideoWaitSaysLeave checks what a video being made says in place
// of Cancel: that the person can leave, and where the video will come, by
// the setting that sends the site's results to the chat. On a small phone
// the line wraps and nothing scrolls sideways.
func TestBrowserVideoWaitSaysLeave(t *testing.T) {
	for _, c := range []struct {
		off  bool
		w, h int64
		want string
	}{
		{false, 1440, 900, "The video is being made already and cannot be stopped. You can leave: it will come to your chat with framy and to the library."},
		{true, 320, 568, "The video is being made already and cannot be stopped. You can leave: it will be in the library."},
	} {
		runs := &fakeRuns{offDM: c.off}
		ts := fakeSite(t, runs.mount)
		ctx := browser(t, ts.URL, c.w, c.h, c.w < 768)
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	await press("running", (s) => s.label === "Making it");
	const b = document.querySelector("#stage .jacts > .busy"), r = b?.getBoundingClientRect();
	return { busy: b?.textContent.trim(), inside: !!r && r.left >= 0 && r.right <= innerWidth, overflow: document.documentElement.scrollWidth - innerWidth };`)
		if s["busy"] != c.want || s["inside"] != true || s["overflow"].(float64) > 0 {
			t.Errorf("%dx%d, off %v: %v", c.w, c.h, c.off, s)
		}
		noErrors(t, ctx)
	}
}

// TestBrowserRunsNoKey checks the way on for a person with no key when the
// free run is refused after all: where a person with a key gets the run on
// it, the card connects OpenRouter, and so does the button while the free
// runs are paused, with when they are back under the price.
func TestBrowserRunsNoKey(t *testing.T) {
	runs := &fakeRuns{keyless: true}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`return await press("refuse:free_limit", (s) => s.failed);`)
	if !strings.HasPrefix(fmt.Sprint(s["acts"]), "[Connect OpenRouter Edit the prompt") {
		t.Errorf("free limit with no key: %v", s)
	}
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("refuse:free_closed", (s) => s.failed);
	const go = document.querySelector("#go");
	for (let i = 0; i < 30 && !/Connect/.test(go.textContent); i++) await wait(100);
	s.go = [go.firstChild.nextSibling.textContent, go.disabled];
	s.cap = document.querySelector("#pcap").textContent;
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Connect OpenRouter")?.click();
	for (let i = 0; i < 30 && document.querySelector("#prefs").hidden; i++) await wait(100);
	await wait(400);
	s.focus = document.activeElement.closest('[data-p="openrouter"]') ? document.activeElement.textContent.trim() : "";
	return s;`)
	if s["label"] != "Free runs paused" || !strings.HasPrefix(fmt.Sprint(s["acts"]), "[Connect OpenRouter Edit the prompt") || fmt.Sprint(s["go"]) != "[Connect OpenRouter false]" ||
		!regexp.MustCompile(`^Free again at \d{1,2}:\d\d [AP]M$`).MatchString(fmt.Sprint(s["cap"])) || s["focus"] != "Connect" {
		t.Errorf("free paused with no key: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserRunsSaid checks what a screen reader hears of a run: a status
// region beside the stage says each phase once as it comes, never the
// clock, and why a run gave nothing.
func TestBrowserRunsSaid(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const el = document.querySelector("#stageSay"), heard = [];
	new MutationObserver(() => { if (el.textContent) heard.push(el.textContent); }).observe(el, { childList: true, characterData: true, subtree: true });
	await press("slow", (s) => s.done);
	await wait(300);
	const done = heard.splice(0);
	await press("fail:key", (s) => s.failed);
	await wait(300);
	return { role: el.getAttribute("role"), inStage: !!el.closest("#stage"), done, failed: heard };`)
	done := fmt.Sprint(s["done"])
	if s["role"] != "status" || s["inStage"] != false || done != "[Getting ready In the queue Making it Ready]" && done != "[In the queue Making it Ready]" {
		t.Errorf("a run said: %v", s)
	}
	if f, _ := s["failed"].([]any); len(f) == 0 || f[len(f)-1] != "The key does not work. OpenRouter no longer accepts the key: it was revoked, or this model is closed to it." {
		t.Errorf("a failed run said: %v", s["failed"])
	}
	noErrors(t, ctx)
}

// TestBrowserStreamFails checks a run whose events cannot be heard: a stream
// framy keeps failing gives way to reading the job, which ends with the
// result; a job that is not the person's leaves the stage saying so; signed
// out, the run says it goes on.
func TestBrowserStreamFails(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("stream:500", (s) => s.done);
	return { ...s, tag: document.querySelector("#stage .jtag")?.textContent || "" };`)
	if s["done"] != true {
		t.Errorf("a stream that fails: %v", s)
	}
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	document.querySelector("#stage .jacts .sq[aria-label]:last-child")?.click();
	await wait(300);
	const s = await press("stream:404", () => !document.querySelector("#stage .jbox"));
	return { ...s, toast: document.querySelector(".toast")?.textContent || "", ghost: !!document.querySelector("#stage .ghost") };`)
	if s["timeout"] == true || s["ghost"] != true || s["toast"] != "There is no such run in your library." {
		t.Errorf("a job that is gone: %v", s)
	}
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const s = await press("stream:401", (s) => /Sign in again/.test(s.sub));
	for (let i = 0; i < 30 && !/^Sign in/.test(document.querySelector("#go")?.textContent); i++) await wait(100);
	return { ...s, go: document.querySelector("#go")?.textContent || "" };`)
	if s["timeout"] == true || s["sub"] != "Sign in again: the run goes on on the server" || !strings.HasPrefix(s["go"].(string), "Sign in") {
		t.Errorf("signed out under a run: %v", s)
	}
	noErrors(t, ctx)
	// Signing in again comes back to the run, which the library goes on following.
	js[bool](t, ctx, `setTimeout(() => document.querySelector("#go").click(), 50); return true;`)
	back := ""
	for i := 0; i < 30 && !strings.HasPrefix(back, "/auth/"); i++ {
		time.Sleep(100 * time.Millisecond)
		back = js[string](t, ctx, `return location.pathname + location.search;`)
	}
	if !strings.HasPrefix(back, "/auth/login?return_to=%2Fr%2F") {
		t.Errorf("signing in from the run goes to %q", back)
	}
}

// TestBrowserGrounding shows a run grounded in Google Search: Google's
// suggestion in its frame, drawn with Google's own styles (the dark ones)
// although the page's CSP takes no inline style, no script of it running and
// no form or script link left in it, its chip opening Google's results
// outside the frame; the cited pages as links under the result and its
// buttons, a link that is not the web left out; the same in the library's
// viewer; and on the smallest phone the buttons still above the dock.
func TestBrowserGrounding(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	look := `
	const look = async (root) => {
		const [f, odd] = root.querySelectorAll(".ground iframe.gsug");
		for (let i = 0; i < 30 && !f?.contentDocument?.querySelector(".chip"); i++) await wait(100);
		const d = f?.contentDocument, chip = d?.querySelector(".chip"), cs = chip && d.defaultView.getComputedStyle(chip);
		const hl = odd?.contentDocument?.querySelector(".headline"), stage = document.querySelector("#stage .jbox")?.getBoundingClientRect();
		const opened = [];
		window.open = (u) => { opened.push(u); return null; };
		chip?.click();
		odd?.contentDocument?.querySelector("a.bad")?.click();
		const od = odd?.contentDocument;
		return { radius: cs?.borderRadius || "", flex: hl ? odd.contentWindow.getComputedStyle(hl).display : "", height: Math.round(f?.getBoundingClientRect().height || 0),
			inner: d?.documentElement.scrollHeight || 0, ran: !!window.__ran, dropped: !!od && !od.querySelector("script, form, [onerror]") && !od.querySelector("a.bad").hasAttribute("href"),
			dark: cs?.backgroundColor || "", opened, url: location.href,
			sources: [...root.querySelectorAll(".ground .gsrc a")].map((a) => a.textContent + " " + a.href), fits: !stage || stage.bottom <= innerHeight };
	};`
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+look+`
	await press("done:grounded", (s) => s.done);
	await wait(300);
	return await look(document.querySelector("#stage"));`)
	want := func(where string, s map[string]any) {
		t.Helper()
		// Google's dark styles: the frame's prefers-color-scheme follows its color-scheme.
		if s["radius"] != "16px" || s["flex"] != "flex" || s["ran"] != false || s["dropped"] != true || s["dark"] != "rgb(44, 44, 44)" ||
			fmt.Sprint(s["opened"]) != "[https://www.google.com/search?q=eiffel+tower+at+night&client=app-vertex-grounding]" || strings.Contains(fmt.Sprint(s["url"]), "google") ||
			fmt.Sprint(s["sources"]) != "[wikipedia.org https://en.wikipedia.org/wiki/Eiffel_Tower toureiffel.paris https://www.toureiffel.paris/en]" ||
			s["height"] != s["inner"] || s["height"].(float64) < 30 {
			t.Errorf("%s: %v", where, s)
		}
	}
	want("stage", s)
	if s["fits"] != true {
		t.Errorf("the result and its grounding do not fit the stage: %v", s)
	}
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+look+`
	document.querySelector('#top .tabs a[data-v="library"]').click();
	for (let i = 0; i < 40 && !document.querySelector("#library .li"); i++) await wait(100);
	document.querySelector("#library .li")?.click();
	for (let i = 0; i < 40 && !document.querySelector("#viewBody .ground"); i++) await wait(100);
	return await look(document.querySelector("#viewBody"));`)
	want("viewer", s)
	// What the page's CSP refused is what Google's HTML may not do in it: a style attribute
	// (which the page gives the frame through the CSSOM) and a handler, which never runs.
	refused := func(ctx context.Context) {
		t.Helper()
		for _, e := range js[[]string](t, ctx, `return window.__errs;`) {
			if e != "csp: style-src-attr inline" && e != "csp: script-src-attr inline" {
				t.Errorf("the page reported: %s", e)
			}
		}
	}
	refused(ctx)

	// In the Mini App a chip and a source open in Telegram's browser, and the Mini App stays.
	mini := miniBrowser(t, ts.URL)
	m := js[map[string]any](t, mini, pageHelpers+runHelpers+`
	for (let i = 0; i < 50 && !window.__tg?.main; i++) await wait(100);
	const p = document.querySelector("#prompt");
	p.value = "done:grounded"; p.dispatchEvent(new Event("input"));
	await wait(80);
	window.__tg.main();
	for (let i = 0; i < 140 && !stage().done; i++) await wait(100);
	const f = document.querySelector("#stage .ground iframe.gsug");
	for (let i = 0; i < 30 && !f?.contentDocument?.querySelector(".chip"); i++) await wait(100);
	f?.contentDocument?.querySelector(".chip")?.click();
	document.querySelector("#stage .gsrc a")?.click();
	await wait(100);
	return { links: window.__tg.links, url: location.pathname };`)
	if fmt.Sprint(m["links"]) != "[https://www.google.com/search?q=eiffel+tower+at+night&client=app-vertex-grounding https://en.wikipedia.org/wiki/Eiffel_Tower]" || m["url"] != "/" {
		t.Errorf("Mini App: %v", m)
	}
	refused(mini)

	// On the smallest phone the result's buttons stay above the dock, and Google's block follows them.
	small := browser(t, ts.URL, 320, 568, true)
	p := js[map[string]any](t, small, pageHelpers+runHelpers+`
	await press("done:grounded", (s) => s.done);
	await wait(300);
	const acts = document.querySelector("#stage .jacts").getBoundingClientRect(), g = document.querySelector("#stage .ground")?.getBoundingClientRect();
	return { acts: Math.round(acts.bottom), dock: Math.round(document.querySelector("#composer").getBoundingClientRect().top), after: !!g && g.top >= acts.bottom };`)
	if p["acts"].(float64) > p["dock"].(float64) || p["after"] != true {
		t.Errorf("a grounded result at 320x568: %v", p)
	}
	refused(small)
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

// TestBrowserRunFrameSize checks that a run's frame keeps one size for its
// shape while it is made, waits, is done and fails, and gives up room only
// when the buttons under a failure would go below the screen or behind the
// dock, and then only the room that is missing.
func TestBrowserRunFrameSize(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	for _, size := range [][3]int64{{1440, 900, 0}, {390, 844, 1}, {320, 568, 1}} {
		ctx := browser(t, ts.URL, size[0], size[1], size[2] == 1)
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const out = {};
	for (const [k, p, until] of [["running", "running", (s) => s.label === "Making it"], ["retry", "retry", (s) => /again/.test(s.label)], ["done", "done", (s) => s.done],
	  ["faces", "faces", (s) => s.failed], ["money", "fail:money", (s) => s.failed], ["limit", "refuse:free_limit", (s) => s.failed]]) {
	  const s = await press(p, until);
	  await wait(700);
	  scrollTo(0, 0); await wait(50);
	  const box = document.querySelector("#stage [data-k=pv]"), acts = document.querySelector("#stage .jacts").getBoundingClientRect();
	  const edge = (innerWidth < 960 ? document.querySelector("#composer").getBoundingClientRect().top : innerHeight) - 24;
	  // A frame at its smallest gives up nothing more: a short phone scrolls to the buttons.
	  out[k] = { timeout: !!s.timeout, w: box.offsetWidth, h: box.offsetHeight, room: Math.round(edge - acts.bottom), least: box.offsetHeight <= (innerWidth < 960 ? 96 : 160) };
	}
	return out;`)
		base := s["running"].(map[string]any)
		for k, v := range s {
			x := v.(map[string]any)
			if x["timeout"] == true {
				t.Errorf("%v %s: timed out", size, k)
				continue
			}
			same := x["w"] == base["w"] && x["h"] == base["h"]
			switch k {
			case "running", "retry", "done":
				if !same {
					t.Errorf("%v %s: the frame is %v×%v, while it was made %v×%v", size, k, x["w"], x["h"], base["w"], base["h"])
				}
			default:
				// A failure's buttons stand over the edge; the frame shrinks only to put them there.
				room := x["room"].(float64)
				if x["h"].(float64) > base["h"].(float64) || (room < -1 && x["least"] != true) || (!same && room > 3) {
					t.Errorf("%v %s: frame %v×%v (made %v×%v), %v px from the buttons to the edge", size, k, x["w"], x["h"], base["w"], base["h"], room)
				}
			}
		}
		if size[0] == 1440 {
			for _, k := range []string{"faces", "money", "limit"} {
				if x := s[k].(map[string]any); x["h"] != base["h"] {
					t.Errorf("%s on a computer: the frame shrank to %v from %v with room to spare", k, x["h"], base["h"])
				}
			}
		}
		noErrors(t, ctx)
	}
}

// TestBrowserFrameWords checks the words of a frame on the stage, while it
// runs and when it failed: in a frame large enough they stand under the
// figure with no dot drawn behind them and the dots keep at least half of
// the frame; in a smaller one, as on a short phone or one on its side, they
// stand under the frame, whole and on the field. The provider's own words,
// opened with a press, leave a frame they no longer fit in.
func TestBrowserFrameWords(t *testing.T) {
	for _, c := range []struct {
		w, h  int64
		lang  string
		phone bool
		under bool // the words of a failure with the provider's own words go under the frame
	}{{320, 568, "de", true, true}, {844, 390, "en", true, true}, {375, 812, "de", true, true}, {390, 844, "ru", true, false}, {1440, 900, "en", false, false}} {
		runs := &fakeRuns{lang: c.lang}
		ts := fakeSite(t, runs.mount)
		ctx := browserAs(t, ts.URL, c.w, c.h, opened{lang: c.lang, phone: c.phone})
		inLang(t, ctx, c.lang)
		for _, p := range []string{"fail:money", "fail:moderation", "running"} {
			got := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
			const s = await press(`+jsString(p)+`, () => document.querySelector("#stage .jbox canvas.ht") && document.querySelector("#stage .c"));
			if (s.timeout) return { bad: ["no frame with words"] };
			await wait(700);
			const bad = [];
			const check = (when) => {
			  const box = document.querySelector("#stage .jbox"), c = document.querySelector("#stage .c"), b = box.getBoundingClientRect(), r = c.getBoundingClientRect();
			  const words = [...c.querySelectorAll("b, small")].map((e) => e.getBoundingClientRect()).filter((x) => x.width && x.height);
			  if (c.parentElement !== box) {
			    if (r.top < b.bottom) bad.push(when + ": the words under the frame stand over it");
			    if (r.left < 0 || r.right > innerWidth) bad.push(when + ": the words under the frame leave the screen");
			    return "under";
			  }
			  if (r.top - b.top < b.height / 2) bad.push(when + ": the words take more than half of the frame, " + Math.round(r.top - b.top) + " of " + b.height);
			  if (r.bottom > b.bottom + 0.5) bad.push(when + ": the words leave the frame");
			  // No dot is drawn behind the words.
			  const cv = box.querySelector("canvas.ht"), k = cv.width / b.width, x = cv.getContext("2d");
			  for (const w of words) {
			    const d = x.getImageData(Math.floor((w.left - b.left) * k), Math.floor((w.top - b.top) * k), Math.ceil(w.width * k), Math.ceil(w.height * k)).data;
			    let n = 0;
			    for (let i = 0; i < d.length; i += 4) if (d[i] > 8 || Math.abs(d[i + 1] - 69) > 8 || Math.abs(d[i + 2] - 79) > 8) n++;
			    if (n) bad.push(when + ": " + n + " pixels of dots behind the words");
			  }
			  return "in";
			};
			const at = check("as it came");
			const said = document.querySelector("#stage .c .said");
			if (said) { said.click(); await wait(400); check("opened"); said.click(); await wait(400); check("closed"); }
			return { bad, at };`)
			bad, _ := got["bad"].([]any)
			if len(bad) > 0 {
				t.Errorf("%dx%d %s, %s: %v", c.w, c.h, c.lang, p, bad)
			}
			if p == "fail:moderation" && (got["at"] == "under") != c.under {
				t.Errorf("%dx%d %s: the provider's words stand %v the frame", c.w, c.h, c.lang, got["at"])
			}
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
    downloadFile() {}, openLink(u) { tg.links.push(u); }, close() { tg.closed = true; },
    SettingsButton: { show() { tg.settingsShown = true; }, hide() { tg.settingsShown = false; }, onClick(fn) { tg.settings = fn; } },
  } };
})();`

// telegramBridge is what a native Telegram client gives the page before any
// of its scripts runs; the page believes the launch data only with it.
const telegramBridge = `window.TelegramWebviewProxy = { postEvent() {} };`

// miniBrowser opens the page as the Mini App on a phone: with Telegram's
// launch data in the fragment, Telegram's bridge, and fakeTelegram served for
// Telegram's script.
func miniBrowser(t *testing.T, url string) context.Context {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(390, 844), chromedp.NoSandbox)
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelC := chromedp.NewContext(actx)
	t.Cleanup(func() { cancelC(); cancelA() })
	keepOnFailure(t, ctx)
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
			_, err := cdp.Call(ctx, tg, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: watch + telegramBridge})
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

// TestBrowserMiniOutsideTelegram opens a link with someone's Mini App launch
// data in an ordinary browser: the page stays the site, signed out, and never
// trades the data for a token, so nothing done in that tab lands in the
// account the data belongs to.
func TestBrowserMiniOutsideTelegram(t *testing.T) {
	var mu sync.Mutex
	trades := 0
	ts := fakeSite(t, func(srv *Server) {
		srv.HandleFunc("POST /auth/miniapp", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			trades++
			mu.Unlock()
			writeTestJSON(w, http.StatusOK, map[string]any{"token": "test-token", "expires_in": 3600})
		})
		srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in"})
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]any{"user_id": 1, "name": "Mallory", "language": "en", "currency": "USD"})
		})
	})
	user := `{"id":1,"first_name":"Mallory","language_code":"en"}`
	init := "auth_date=1&user=" + neturl.QueryEscape(user) + "&hash=test"
	ctx := browser(t, ts.URL+"/#tgWebAppData="+neturl.QueryEscape(init)+"&tgWebAppVersion=9.0&tgWebAppPlatform=ios", 390, 844, true)
	got := js[map[string]any](t, ctx, pageHelpers+`
	await wait(300);
	let kept = null;
	try { kept = sessionStorage.getItem("framy.init"); } catch {}
	return { mini: window.framy.mini, signin: vis(document.querySelector("#top .signin")), me: !!document.querySelector("#meBtn"), kept };`)
	mu.Lock()
	n := trades
	mu.Unlock()
	if got["mini"] != false || got["signin"] != true || got["me"] != false || got["kept"] != nil || n != 0 {
		t.Errorf("launch data outside Telegram: %v, %d trades", got, n)
	}
	noErrors(t, ctx)
}

// TestBrowserMeFails opens the page while framy cannot say who is there:
// GET /api/me answers 503 twice. That is no "signed out": the page offers no
// sign-in to a person who may well be signed in, says framy is out of reach,
// and asks again until the account comes.
func TestBrowserMeFails(t *testing.T) {
	var mu sync.Mutex
	asked := 0
	ts := fakeSite(t, func(srv *Server) {
		srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			asked++
			n := asked
			mu.Unlock()
			if n <= 2 {
				writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]any{"user_id": 1, "name": "Ada", "language": "en", "currency": "USD",
				"connections": []map[string]any{}, "free": map[string]any{
					"images": map[string]any{"used": 0, "limit": 5, "left": 5, "window_seconds": 86400},
					"videos": map[string]any{"used": 0, "limit": 1, "left": 1, "window_seconds": 604800}}})
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+`
	const go = document.querySelector("#go");
	const first = { signin: !!document.querySelector("#top .signin"), me: !!document.querySelector("#meBtn"), go: go.textContent, on: !go.disabled,
	  toast: document.querySelector(".toast")?.textContent || "" };
	for (let i = 0; i < 100 && !document.querySelector("#meBtn"); i++) await wait(100);
	return { ...first, after: !!document.querySelector("#meBtn"), cap: document.querySelector("#pcap").textContent, goAfter: go.textContent };`)
	if got["signin"] != false || got["me"] != false || got["go"] != "Offline" || got["on"] != false || !strings.HasPrefix(got["toast"].(string), "No connection to framy") {
		t.Errorf("while framy cannot say who is here: %v", got)
	}
	if got["after"] != true || got["cap"] != "Free left: 5 of 5" || !strings.HasPrefix(got["goAfter"].(string), "Create") {
		t.Errorf("once framy answers: %v", got)
	}
	noErrors(t, ctx)
}

// TestBrowserMiniExpired lets the Mini App's session end: framy stops taking
// its launch data, as it does an hour after Telegram signed it. Opened that
// late, the Mini App says to open it again and its main button closes it;
// what was typed then, also after the button, comes back on the next open. When the session ends under
// a run, the run says it goes on on the server, and the dead launch data is
// traded once, not again and again.
func TestBrowserMiniExpired(t *testing.T) {
	const old = "The Telegram session is too old. Close framy and open it again."
	runs := &fakeRuns{expired: true}
	ts := fakeSite(t, runs.mount)
	ctx := miniBrowser(t, ts.URL)
	trades := func() int { runs.mu.Lock(); defer runs.mu.Unlock(); return runs.trades }
	s := js[map[string]any](t, ctx, pageHelpers+`
	for (let i = 0; i < 50 && !window.__tg?.main; i++) await wait(100);
	await wait(200);
	const pc = document.querySelector("#pcap");
	const out = { button: window.__tg.params?.text || "", cap: pc.textContent, toast: document.querySelector(".toast")?.textContent || "",
		tabs: !!document.querySelector("#top .tabs"), cut: pc.scrollWidth > pc.clientWidth + 1 || pc.scrollHeight > pc.clientHeight + 1 };
	const p = document.querySelector("#prompt");
	p.value = "a fox"; p.dispatchEvent(new Event("input"));
	await wait(80);
	window.__tg.main();
	const closed = !!window.__tg.closed;
	// Typed on and closed by Telegram's own button, which the page does not hear.
	p.value = "a fox in the snow"; p.dispatchEvent(new Event("input"));
	return { ...out, closed };`)
	// The line under the price says it to its end, on two lines.
	if s["button"] != "Close framy" || s["cap"] != old || s["cut"] != false || s["toast"] != old || s["tabs"] != false || s["closed"] != true || trades() != 1 {
		t.Errorf("opened with old launch data: %v, %d trades", s, trades())
	}
	noErrors(t, ctx)

	// Opened again with launch data framy takes: what was typed is back.
	runs.mu.Lock()
	runs.expired = false
	runs.mu.Unlock()
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	s = js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	for (let i = 0; i < 50 && !window.__tg?.main; i++) await wait(100);
	const back = { prompt: document.querySelector("#prompt").value, button: window.__tg.params?.text || "" };
	const p = document.querySelector("#prompt");
	p.value = "stream:expired"; p.dispatchEvent(new Event("input"));
	await wait(80);
	window.__tg.main();
	for (let i = 0; i < 100 && !/Open framy/.test(stage().sub); i++) await wait(100);
	const s = stage(), toast = document.querySelector(".toast")?.textContent || "";
	await wait(2500);
	return { ...s, back, toast, button: window.__tg.params?.text || "", cap: document.querySelector("#pcap").textContent };`)
	back, _ := s["back"].(map[string]any)
	if back["prompt"] != "a fox in the snow" || back["button"] != "Create" {
		t.Errorf("opened again: %v", back)
	}
	if s["sub"] != "Open framy again: the run goes on on the server" || s["button"] != "Close framy" || s["cap"] != old || s["toast"] != old || trades() != 3 {
		t.Errorf("the session ended under a run: %v, %d trades", s, trades())
	}
	noErrors(t, ctx)
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
		return [...document.querySelectorAll("#stage .jacts button")].find((b) => b.getAttribute("aria-label") === "Share");
	};
	const b1 = await go("done");
	b1?.click();
	for (let i = 0; i < 30 && !window.__tg.shared.length; i++) await wait(100);
	document.querySelector("#stage .jacts button[aria-label='Make another']")?.click();
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

// TestBrowserMiniSVG checks a vector result in the Mini App: the picture
// shows, Download saves it as an .svg, and there is no Send to Telegram,
// which Telegram cannot take.
func TestBrowserMiniSVG(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := miniBrowser(t, ts.URL)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	for (let i = 0; i < 50 && !window.__tg?.main; i++) await wait(100);
	const p = document.querySelector("#prompt");
	p.value = "done:svg"; p.dispatchEvent(new Event("input"));
	await wait(80);
	window.__tg.main();
	for (let i = 0; i < 140 && !stage().done; i++) await wait(100);
	const img = document.querySelector("#stage img[src$='result.svg']");
	for (let i = 0; i < 30 && img && !img.complete; i++) await wait(100);
	const acts = [...document.querySelectorAll("#stage .jacts > button, #stage .jacts > a")];
	return { done: stage().done, shown: img?.naturalWidth || 0, names: acts.map((b) => b._name || "").filter(Boolean),
	  labels: acts.map((b) => b.getAttribute("aria-label") || b.textContent.trim()) };`)
	if s["done"] != true || s["shown"].(float64) <= 0 || fmt.Sprint(s["names"]) != "[framy-1-1.svg]" || strings.Contains(fmt.Sprint(s["labels"]), "Share") {
		t.Errorf("an SVG result: %v", s)
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.shares != 0 {
		t.Errorf("%d shares", runs.shares)
	}
	noErrors(t, ctx)
}

// TestBrowserMiniLaunchRunsOut: the Mini App's token ends with its launch,
// an hour after Telegram signed it, and the launch data then buys no other.
// A run pressed after that says to open framy again, not "something went
// wrong" with a code, and the person shows as signed out.
func TestBrowserMiniLaunchRunsOut(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := miniBrowser(t, ts.URL)
	js[bool](t, ctx, pageHelpers+`for (let i = 0; i < 50 && !window.__tg?.main; i++) await wait(100); return true;`)
	runs.launchOld.Store(true)
	runs.signOutElsewhere()
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const p = document.querySelector("#prompt");
	p.value = "done"; p.dispatchEvent(new Event("input"));
	await wait(80);
	window.__tg.main();
	let st = stage();
	for (let i = 0; i < 80 && !st.why; i++) { await wait(100); st = stage(); }
	return { label: st.label, why: st.why, acts: st.acts, me: !!document.querySelector("#meBtn") };`)
	if s["label"] != "Sign in again" || s["why"] != "The Telegram session is too old. Close framy and open it again." || s["me"] != false {
		t.Errorf("a run after the launch's hour: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserHalftoneRests checks that the dots are drawn only while they can
// be seen: not under a sheet that hides the stage, not with the library over
// it, and with reduced motion only twice a second, without following every
// frame of the screen. A sheet closing brings them back at once.
func TestBrowserHalftoneRests(t *testing.T) {
	const count = `
	const C2 = CanvasRenderingContext2D.prototype, fill0 = C2.fill, raf0 = window.requestAnimationFrame;
	let fills = 0, rafs = 0;
	C2.fill = function (...a) { fills++; return fill0.apply(this, a); };
	window.requestAnimationFrame = (cb) => { rafs++; return raf0(cb); };
	// What one second brings: frames asked for, and dots filled.
	const second = async () => { const f = fills, r = rafs; await wait(1000); return [rafs - r, fills - f]; };
	`
	for _, reduce := range []bool{false, true} {
		runs := &fakeRuns{}
		ts := fakeSite(t, runs.mount)
		ctx := browser(t, ts.URL, 390, 844, true)
		if reduce {
			err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
				_, err := cdp.Call(ctx, tg, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Features: []*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}}})
				return err
			}), chromedp.Reload())
			if err != nil {
				t.Fatal(err)
			}
			ready(t, ctx)
		}
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+count+`
	await press("running", (s) => s.dots && s.label === "Making it");
	await wait(400);
	const out = { open: await second() };
	document.querySelector("#meBtn").click();
	await wait(800);
	out.sheet = await second();
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	// The dots move again as the sheet goes, not up to half a second later.
	const f0 = fills;
	await wait(200);
	out.soon = fills - f0;
	await wait(400);
	out.back = await second();
	document.querySelector(".tab[data-v=library]").click();
	await wait(800);
	out.library = await second();
	return out;`)
		get := func(k string, i int) float64 { return s[k].([]any)[i].(float64) }
		if reduce {
			// Twice a second, a fill per shade, and no frames followed.
			if get("open", 0) > 2 || get("open", 1) == 0 || get("open", 1) > 3*9 || get("sheet", 1) > 0 || s["soon"].(float64) == 0 || get("back", 1) == 0 || get("library", 1) > 0 {
				t.Errorf("reduced motion: %v", s)
			}
		} else if get("open", 0) < 20 || get("open", 1) == 0 || get("sheet", 0) > 2 || get("sheet", 1) > 0 || s["soon"].(float64) == 0 || get("back", 1) == 0 ||
			get("library", 0) > 2 || get("library", 1) > 0 {
			t.Errorf("motion: %v", s)
		}
		noErrors(t, ctx)
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
	// Two runs, as TestBrowserSmooth's steps: a hiccup of a shared runner
	// lands in one, and a task that is long every time in both.
	const long = [];
	let s;
	for (let n = 0; n < 2; n++) {
	  window.__long = [];
	  const t0 = performance.now();
	  s = await press("slow", (s) => s.done);
	  await wait(2500);
	  long.push(window.__long.filter(([at]) => at > t0).map(([, ms]) => ms));
	}
	// A task known to be long, to show the observer works here.
	window.__long = [];
	await new Promise((r) => setTimeout(() => { const t = performance.now(); while (performance.now() - t < 120); r(); }));
	await wait(300);
	return { ...s, long, works: window.__long.some(([, ms]) => ms >= 100), settled: !stage().dots };`)
		if s["works"] != true {
			t.Fatal("the browser reports no long tasks, so the check cannot work")
		}
		// The better run counts: its longest task, on the laptop.
		worst := math.Inf(1)
		for _, run := range s["long"].([]any) {
			longest := 0.0
			for _, v := range run.([]any) {
				longest = max(longest, v.(float64)*scale)
			}
			worst = min(worst, longest)
		}
		if s["done"] != true || s["settled"] != true || worst > 50 {
			t.Errorf("%dx%d: the longest task %.0f ms on the laptop: %v", size[0], size[1], worst, s)
		}
		t.Logf("%dx%d: longest task %.0f ms on the laptop", size[0], size[1], worst)
		noErrors(t, ctx)
	}
}

// TestBrowserDeprecation reads the line of a model its maker is closing, in
// the picker and in the composer: a sentence with the stop of its language,
// which the code once added as "." in Chinese and Japanese too.
func TestBrowserDeprecation(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	read := `
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "Veo";
	q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	const picker = document.querySelector("#pkd .dwarn")?.textContent;
	document.querySelector("#pkrows .frow .fhit").click();
	await wait(300);
	const choose = [...document.querySelectorAll("#picker .det .choose")].find(vis);
	if (choose && !choose.disabled) choose.click();
	await wait(900);
	return { picker, composer: document.querySelector("#how .mvars .mnote")?.textContent };`
	s := js[map[string]any](t, ctx, pageHelpers+read)
	if s["picker"] != "Google is closing this model. Gemini Omni 1.1 Flash replaces it." ||
		s["composer"] != "Google is closing this model. Switch to Gemini Omni 1.1 Flash" {
		t.Errorf("English: %v", s)
	}
	js[map[string]any](t, ctx, `localStorage.setItem("framy.language", "zh"); setTimeout(() => location.reload(), 50); return {};`)
	time.Sleep(800 * time.Millisecond)
	ready(t, ctx)
	s = js[map[string]any](t, ctx, pageHelpers+read)
	stop := regexp.MustCompile(`\.(\s|$)`)
	for _, k := range []string{"picker", "composer"} {
		if v, _ := s[k].(string); !strings.Contains(v, "。") || stop.MatchString(v) {
			t.Errorf("Chinese %s: %q", k, v)
		}
	}
	noErrors(t, ctx)
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
	if dets, _ := s["dets"].(map[string]any); !strings.Contains(fmt.Sprint(dets["Seedance"]), "Real people in filesNot accepted") ||
		strings.Contains(fmt.Sprint(dets["Wan"]), "people in files") {
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

// TestBrowserPlanning checks GPT Image 2.6's complexity, "Planning", on a
// direct OpenAI offer: a row of its own with OpenAI's high picked and no
// prices, since it has no fee, what is picked going with the quote, and no
// such row on OpenRouter, which has no complexity.
func TestBrowserPlanning(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSiteWith(t, runs.mount, func(cat map[string]any) {
		// The fixture has GPT Image 2.5: its direct offers get 2.6's complexity.
		for _, m := range cat["models"].([]any) {
			for _, o := range m.(map[string]any)["offers"].([]any) {
				if o := o.(map[string]any); o["provider"] == "openai" {
					o["params"].(map[string]any)["complexity"] = map[string]any{"type": "enum", "values": []any{"low", "medium", "high"}}
					o["defaults"].(map[string]any)["complexity"] = "high"
				}
			}
		}
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+`
	const quotes = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/quote")) quotes.push(JSON.parse(o.body)); return fetch0(u, o); };
	const rows = () => [...document.querySelectorAll("#how [data-k^='h-f-']")].filter(vis).map((r) => r.querySelector(".lt").textContent);
	const sent = () => quotes.flatMap((q) => q.items).filter((i) => i.offer.startsWith("openai:")).map((i) => i.settings.complexity);
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "GPT Image";
	q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	const row = document.querySelector("#pkrows .frow");
	([...row.querySelectorAll(".pill:not(.old)")].find((b) => b.textContent.trim() === "2.5 Flare") || row.querySelector(".fhit")).click();
	await wait(900);
	document.querySelector('#how [data-seg="pay"] [data-v="openrouter"]').click();
	await wait(700);
	const openrouter = rows();
	document.querySelector('#how [data-seg="pay"] [data-v="openai"]').click();
	await wait(900);
	const openai = rows();
	const seg = () => document.querySelector('#how [data-seg="set-complexity"]');
	const values = [...seg().querySelectorAll(":scope > button")].map((b) => b.textContent + (b.getAttribute("aria-checked") === "true" ? "*" : ""));
	const prices = seg().querySelectorAll("small").length;
	const first = sent().at(-1);
	seg().querySelector('[data-v="low"]').click();
	await wait(900);
	return { openrouter, openai, values, prices, first, low: sent().at(-1), now: seg().querySelector('[aria-checked="true"]')?.dataset.v };`)
	if fmt.Sprint(s["openrouter"]) == "" || strings.Contains(fmt.Sprint(s["openrouter"]), "Planning") {
		t.Errorf("OpenRouter's offer: rows %v, want no Planning", s["openrouter"])
	}
	if !strings.Contains(fmt.Sprint(s["openai"]), "Planning") || fmt.Sprint(s["values"]) != "[Low Medium High*]" || s["prices"] != float64(0) {
		t.Errorf("OpenAI's offer: rows %v, values %v, %v prices", s["openai"], s["values"], s["prices"])
	}
	if s["first"] != "high" || s["low"] != "low" || s["now"] != "low" {
		t.Errorf("quoted %v, then %v after Low, picked %v", s["first"], s["low"], s["now"])
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
	[...document.querySelectorAll("#stage .jacts button")].find((b) => b.textContent.trim() === "Try without sound")?.click();
	for (let i = 0; i < 40 && sent.length < 2; i++) await wait(100);
	s.sound = sent.map((b) => b.settings.generate_audio);
	s.offers = sent.map((b) => b.offer);
	return s;`)
	if acts := fmt.Sprint(s["acts"]); s["label"] != "Sound refused" || s["why"] != "The model’s sound may break copyright." || s["said"] != "" ||
		!strings.HasPrefix(acts, "[Try without sound Edit the prompt") || !strings.HasSuffix(s["line"].(string), " · $0") {
		t.Errorf("sound: %v", s)
	}
	if fmt.Sprint(s["sound"]) != "[true false]" || fmt.Sprint(s["offers"]) != "[openrouter:bytedance/seedance-2.0-mini openrouter:bytedance/seedance-2.0-mini]" {
		t.Errorf("without sound: sent %v %v", s["sound"], s["offers"])
	}
	noErrors(t, ctx)
}

// TestBrowserUploadsGoAgain checks an upload framy no longer keeps: a file
// it swept an hour after a refused press goes up again on its own, when the
// prices find it gone, when the press finds it gone, and when the composer
// held it too long; a clip's last frame is taken from the clip again. A file
// a job took stays with the job and is not sent twice.
func TestBrowserUploadsGoAgain(t *testing.T) {
	runs := &fakeRuns{}
	var mu sync.Mutex
	next := 500
	ts := fakeSite(t, func(srv *Server) {
		runs.mount(srv)
		srv.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			id := next
			next++
			mu.Unlock()
			writeTestJSON(w, http.StatusCreated, map[string]any{"id": id, "content_type": "image/png", "bytes": 100, "width": 64, "height": 36})
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	// framy's sweep, played by the page: a file it no longer has is refused
	// by the run (unknown_input) and by its price.
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const until = async (fn, ms = 4000) => { const end = Date.now() + ms; while (Date.now() < end) { if (fn()) return true; await wait(50); } return !!fn(); };
	const gone = new Set(), sent = [], frames = [], fetch0 = window.fetch;
	const json = (status, b) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
	window.fetch = async (u, o) => {
	  const s = String(u);
	  if (/\/last-frame$/.test(s)) frames.push(s);
	  if (s.endsWith("/api/jobs") && o?.method === "POST") {
	    const b = JSON.parse(o.body);
	    sent.push((b.inputs || []).join("+") || "-");
	    if ((b.inputs || []).some((i) => gone.has(i))) return json(422, { error: "unknown_input", message: "an input is not one of your files" });
	  }
	  if (s.endsWith("/api/quote")) {
	    const items = JSON.parse(o.body).items, r = await fetch0(u, o), a = await r.json();
	    items.forEach((it, i) => { if ((it.inputs || []).some((x) => gone.has(x)) && a.items?.[i]) a.items[i].refusal = { error: "unknown_input", status: 422 }; });
	    return json(200, a);
	  }
	  return fetch0(u, o);
	};
	const go = () => document.querySelector("#go");
	const settled = () => until(() => !document.querySelector("#composer .q.wait") && !go().disabled);
	const out = {};
	await settled();
	const cv = document.createElement("canvas"); cv.width = 64; cv.height = 36;
	const png = await new Promise((r) => cv.toBlob(r, "image/png"));
	const dt = new DataTransfer(); dt.items.add(new File([png], "p.png", { type: "image/png" }));
	const inp = document.querySelector("#file"); inp.files = dt.files; inp.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(800);
	// A refused press keeps the photo framy took: 500.
	await press("refuse:free_limit", (s) => s.failed);
	await settled();
	// An hour later framy swept it, and the next price says so: the composer prices the photo as the browser read it, and Create stays.
	gone.add(500);
	const tried = new Set([document.querySelector("#how .ratios .rt[aria-checked=true]")?.dataset.v]);
	[...document.querySelectorAll("#how .ratios .rt")].find((b) => !tried.has(b.dataset.v) && !b.classList.contains("more")).click();
	await wait(1200);
	out.priced = [go().textContent.includes("do not fit"), go().disabled];
	await press("done", (s) => s.done);
	// The press finds it gone (its job was deleted): the photo goes up again, and the run once more.
	gone.add(501);
	await settled();
	await press("done", (s) => s.done);
	// An hour on, the job that took 502 still holds it: it goes as it is.
	const now0 = Date.now;
	Date.now = () => now0() + 51 * 60e3;
	try { await settled(); await press("done", (s) => s.done); } finally { Date.now = now0; }
	// A new photo of a refused press, held for longer than framy keeps it: it goes up again before the press.
	document.querySelector("#files .thumb .rm")?.click();
	await wait(300);
	const dt2 = new DataTransfer(); dt2.items.add(new File([png], "q.png", { type: "image/png" }));
	inp.files = dt2.files; inp.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(800);
	await settled();
	out.photos = document.querySelectorAll("#files .thumb").length;
	await press("refuse:free_limit", (s) => s.failed);
	await settled();
	Date.now = () => now0() + 51 * 60e3;
	try { await press("done", (s) => s.done); } finally { Date.now = now0; }
	out.sent = sent.join(" ");
	// A clip's last frame, held too long, is taken from the clip again.
	document.querySelector("#files .thumb .rm")?.click();
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	await press("done:video", (s) => !!document.querySelector("#stage .jacts button[aria-label=Continue]"));
	document.querySelector("#stage .jacts button[aria-label=Continue]").click();
	await until(() => document.querySelector("#files .thumb[data-k=flf72]"));
	const was = frames.length, n = sent.length;
	Date.now = () => now0() + 51 * 60e3;
	try { await press("done", () => sent.length > n); } finally { Date.now = now0; }
	out.frame = [frames.length - was, sent[n]];
	return out;`)
	want := map[string]string{
		// refused, the price found 500 gone, a new upload; 501 refused, sent again as 502; 502 held by its job;
		// a new photo refused as 503, held too long, 504.
		"priced": "[false false]", "sent": "500 501 501 502 502 503 504", "photos": "1", "frame": "[1 72]",
	}
	for k, w := range want {
		if got := fmt.Sprint(s[k]); got != w {
			t.Errorf("%s = %s, want %s", k, got, w)
		}
	}
	noErrors(t, ctx)
}

// TestBrowserPictureGoesOn checks what a picture framy made can become: the
// photo of the next run (Change the image) or the first frame of a video
// (Animate). It goes back into the composer by its id, with nothing to
// upload and an empty prompt to say what to change, and its price is asked
// with it.
func TestBrowserPictureGoesOn(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const quotes = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/quote")) quotes.push(JSON.parse(o.body)); return fetch0(u, o); };
	const asked = (kind) => quotes.flatMap((q) => q.items).some((it) => it.pay && (it.inputs || []).includes(7) && it.offer.includes(kind === "video" ? "video" : ""));
	const there = async (kind) => {
	  let th = null;
	  for (let i = 0; i < 50 && !th; i++) { await wait(100); th = document.querySelector("#files .thumb[data-k=fr7]"); }
	  await wait(600);
	  return { kind: document.querySelector("#kind [aria-checked=true]")?.dataset.v, thumb: !!th, files: document.querySelectorAll("#files .thumb").length,
	    slot: th?.closest("[data-k^=g-]")?.dataset.k || "", prompt: document.querySelector("#prompt").value, focus: document.activeElement === document.querySelector("#prompt"),
	    frame: !!document.querySelector("#stage [data-k=pv]"), asked: asked(kind), model: document.querySelector("#how .mname")?.textContent || "" };
	};
	let s = await press("done", (s) => s.done);
	const acts = s.acts;
	document.querySelector("#stage .jacts button[aria-label='Change the image']").click();
	const change = await there("image");
	quotes.length = 0;
	s = await press("done", (s) => s.done);
	document.querySelector("#stage .jacts button[aria-label=Animate]").click();
	const anim = await there("video");
	return { acts, change, anim };`)
	if acts := fmt.Sprint(s["acts"]); !strings.HasPrefix(acts, "[Download Again Edit the prompt Change the image Animate") {
		t.Errorf("the buttons on a picture: %v", acts)
	}
	c, _ := s["change"].(map[string]any)
	if c["kind"] != "image" || c["thumb"] != true || c["files"] != float64(1) || c["prompt"] != "" || c["focus"] != true || c["frame"] != true || c["asked"] != true {
		t.Errorf("change the picture: %v", c)
	}
	a, _ := s["anim"].(map[string]any)
	if a["kind"] != "video" || a["thumb"] != true || a["files"] != float64(1) || a["slot"] != "g-start" || a["prompt"] != "" || a["focus"] != true || a["asked"] != true {
		t.Errorf("animate: %v", a)
	}
	noErrors(t, ctx)
}

// TestBrowserLastFrame checks Continue from a model that cannot go on from a
// video: the clip's last frame comes to the composer marked with Continue's
// icon and no words, and its price is asked again with that frame, never
// taken from the run without it.
func TestBrowserLastFrame(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const quotes = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/quote")) quotes.push(JSON.parse(o.body)); return fetch0(u, o); };
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	const p = document.querySelector("#prompt");
	p.value = "done:video"; p.dispatchEvent(new Event("input"));
	await wait(600);
	document.querySelector("#go").click();
	let cont = null;
	for (let i = 0; i < 100 && !cont; i++) { await wait(100); cont = document.querySelector("#stage .jacts button[aria-label=Continue]"); }
	const before = quotes.length;
	cont?.click();
	let th = null;
	for (let i = 0; i < 50 && !th; i++) { await wait(100); th = document.querySelector("#files .thumb[data-k=flf72]"); }
	await wait(600);
	const tag = th?.querySelector(".tag");
	// The composer's own run (its pay is set; the prices beside a choice have none),
	// priced before Continue without the frame and asked again with it.
	const pre = quotes.slice(0, before).flatMap((q) => q.items), post = quotes.slice(before).flatMap((q) => q.items);
	const asked = post.filter((it) => it.pay && (it.inputs || []).includes(72) && pre.some((b) => b.offer === it.offer && b.pay === it.pay && !(b.inputs || []).length));
	const brief = (it) => it.offer.split("/").pop() + "|" + it.pay + "|" + (it.inputs || []).join(",");
	return { cont: !!cont, thumb: !!th, icon: !!tag?.querySelector("svg"), words: tag?.textContent ?? null, asked: asked.length,
	  pre: [...new Set(pre.map(brief))].join(" "), post: [...new Set(post.map(brief))].join(" ") };`)
	if s["cont"] != true || s["thumb"] != true || s["icon"] != true || s["words"] != "" {
		t.Errorf("last frame: %v", s)
	}
	if n, _ := s["asked"].(float64); n < 1 {
		t.Errorf("the price with the last frame was not asked: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserJoined checks a run that went on from a clip's last frame and
// came back joined to it: the stage plays and saves the joined clip, the new
// part saves on its own, and Continue takes the last frame of all of it.
func TestBrowserJoined(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const frames = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { const m = String(u).match(/\/api\/files\/(\d+)\/last-frame$/); if (m) frames.push(+m[1]); return fetch0(u, o); };
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	const p = document.querySelector("#prompt");
	p.value = "done:joined"; p.dispatchEvent(new Event("input"));
	await wait(300);
	document.querySelector("#go").click();
	let cont = null;
	for (let i = 0; i < 100 && !cont; i++) { await wait(100); cont = document.querySelector("#stage .jacts button[aria-label=Continue]"); }
	const st = document.querySelector("#stage");
	const video = st.querySelector(".jbox video")?.getAttribute("src") || "";
	const links = [...st.querySelectorAll(".jacts a")].map((a) => [a.getAttribute("href").replace(location.origin, ""), a.getAttribute("download"), a.textContent.trim(), a.getAttribute("aria-label") || ""].join(" | "));
	cont?.click();
	for (let i = 0; i < 30 && !frames.length; i++) await wait(100);
	return { video: video.replace(location.origin, ""), links, frames };`)
	want := []any{"/test/joined.mp4?download=1 | framy-1-joined.mp4 | Download | ", "/test/clip.mp4?download=1 | framy-1-1.mp4 | New part | New part"}
	if s["video"] != "/test/joined.mp4" || fmt.Sprint(s["links"]) != fmt.Sprint(want) || fmt.Sprint(s["frames"]) != "[10]" {
		t.Errorf("joined: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserPriceBeforeCreate checks that a run on a key starts at the price
// the bar shows, never before the bar shows one: the price beside a choice
// stands for the run at once, Create waits while a price is on its way, and a
// price that did not come is asked again by itself.
func TestBrowserPriceBeforeCreate(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const until = async (fn, ms = 4000) => { const end = Date.now() + ms; while (Date.now() < end) { if (fn()) return true; await wait(50); } return !!fn(); };
	const sent = [], fetch0 = window.fetch;
	let hold = null, release = null, fail = 0, quotes = 0;
	window.fetch = async (u, o) => {
	  const s = String(u);
	  if (s.endsWith("/api/jobs") && o?.method === "POST") sent.push(JSON.parse(o.body));
	  if (s.endsWith("/api/quote")) {
	    quotes++;
	    if (fail > 0) { fail--; return new Response('{"error":"internal","message":"x"}', { status: 500, headers: { "Content-Type": "application/json" } }); }
	    if (hold) await hold;
	  }
	  return fetch0(u, o);
	};
	const holdQuotes = () => { hold = new Promise((r) => { release = () => { hold = null; r(); }; }); };
	const go = () => document.querySelector("#go");
	const bar = () => document.querySelector("#pnum").textContent;
	const usdOf = (x) => +String(x).replace(/[^\d.]/g, "");
	const out = {};
	await until(() => !document.querySelector("#composer .q.wait") && !go().disabled);
	// On the free allowance, the key's tile shows its price: picked, it is the bar's at once.
	const keyTile = document.querySelector('#how [data-seg="pay"] [data-v="openrouter"]');
	const tilePrice = keyTile.querySelector(".usd")?.textContent || "";
	const p = document.querySelector("#prompt");
	p.value = "done"; p.dispatchEvent(new Event("input"));
	holdQuotes();
	keyTile.click();
	await wait(200);
	out.twin = [bar() === tilePrice, go().disabled];
	await press("done", (s) => s.done);
	out.twinSent = [sent.length, usdOf(tilePrice) === sent[0]?.shown_usd];
	release();
	await until(() => !document.querySelector("#composer .q.wait"));
	// A ratio no price was asked for: Create waits for it, and Ctrl+Enter too.
	const tried = new Set([document.querySelector("#how .ratios .rt[aria-checked=true]")?.dataset.v]);
	const ratio = () => { const b = [...document.querySelectorAll("#how .ratios .rt")].find((b) => !tried.has(b.dataset.v) && !b.classList.contains("more") && !b.disabled); tried.add(b.dataset.v); return b; };
	holdQuotes();
	ratio().click();
	await wait(300);
	p.value = "done"; p.dispatchEvent(new Event("input"));
	p.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
	go().click();
	await wait(300);
	out.waiting = [bar(), go().disabled, sent.length];
	release();
	await until(() => !go().disabled);
	const shown = bar();
	go().click();
	await until(() => sent.length === 2);
	out.after = Math.abs(usdOf(shown) - sent[1].shown_usd) < 0.0011;
	out.shown = [shown, sent[1].shown_usd];
	// A price that did not come is asked again by itself, and Create follows it.
	await until(() => !document.querySelector("#composer .q.wait") && !go().disabled);
	fail = 1;
	const q0 = quotes;
	ratio().click();
	await wait(300);
	out.failed = [bar(), go().disabled];
	const t0 = Date.now();
	await until(() => !go().disabled, 5000);
	out.retried = [!go().disabled, quotes - q0 >= 2, Date.now() - t0 < 4000];
	return out;`)
	want := map[string]string{
		"twin": "[true false]", "twinSent": "[1 true]", "waiting": "[… true 1]", "after": "true",
		"failed": "[… true]", "retried": "[true true true]",
	}
	for k, w := range want {
		if got := fmt.Sprint(s[k]); got != w {
			t.Errorf("%s = %s, want %s", k, got, w)
		}
	}
	noErrors(t, ctx)
}

// TestBrowserByKey checks the way out of a free run the allowance refused:
// the run comes back to the composer on the person's key with its price on
// the bar and waits for Create, which sends that price.
func TestBrowserByKey(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const until = async (fn, ms = 4000) => { const end = Date.now() + ms; while (Date.now() < end) { if (fn()) return true; await wait(50); } return !!fn(); };
	const sent = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/jobs") && o?.method === "POST") sent.push(JSON.parse(o.body)); return fetch0(u, o); };
	const go = () => document.querySelector("#go");
	// A video goes free while the week's one is left, as it is here.
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	const pay = () => document.querySelector('#how [data-seg="pay"] [aria-checked="true"]')?.dataset.v;
	const was = pay();
	const s = await press("refuse:free_closed", (s) => s.failed);
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "On my OpenRouter key").click();
	await wait(600);
	const out = { was, first: s.acts[0], sent: sent.map((b) => b.pay).join(","), back: !document.querySelector("#stage .jwhy"),
	  key: pay(), focus: document.activeElement === go(),
	  prompt: document.querySelector("#prompt").value };
	await until(() => !go().disabled && !/…/.test(document.querySelector("#pnum").textContent));
	const bar = document.querySelector("#pnum").textContent;
	const p = document.querySelector("#prompt");
	p.value = "done"; p.dispatchEvent(new Event("input"));
	go().click();
	await until(() => sent.length === 2);
	out.then = [sent[1]?.pay, Math.abs(+bar.replace(/[^\d.]/g, "") - sent[1]?.shown_usd) < 0.0011];
	return out;`)
	want := map[string]string{
		"was": "free", "first": "On my OpenRouter key", "sent": "free", "back": "true", "key": "openrouter", "focus": "true",
		"prompt": "refuse:free_closed", "then": "[key true]",
	}
	for k, w := range want {
		if got := fmt.Sprint(s[k]); got != w {
			t.Errorf("%s = %s, want %s", k, got, w)
		}
	}
	noErrors(t, ctx)
}

// TestBrowserTakePriceOnce checks that "Create for" the new price starts one
// run however fast it is pressed twice.
func TestBrowserTakePriceOnce(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const sent = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/jobs") && o?.method === "POST") sent.push(JSON.parse(o.body)); return fetch0(u, o); };
	await press("refuse:price_changed", (s) => s.failed);
	const b = [...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Create for ≈ $0.500");
	b.click(); b.click();
	await wait(800);
	return { sent: sent.length, shown: sent.at(-1)?.shown_usd };`)
	if s["sent"] != float64(2) || s["shown"] != 0.5 {
		t.Errorf("take the price: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserSignOutStopsRun checks a sign-out while a press still sends its
// files: the upload stops there, and no job is made after it.
func TestBrowserSignOutStopsRun(t *testing.T) {
	runs := &fakeRuns{}
	var stopped atomic.Bool
	ts := fakeSite(t, func(srv *Server) {
		runs.mount(srv)
		srv.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, r *http.Request) {
			// Read whole, the request hears the browser drop it.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
				stopped.Store(true)
				return
			case <-time.After(1500 * time.Millisecond):
			}
			writeTestJSON(w, http.StatusCreated, map[string]any{"id": 500, "content_type": "image/png", "bytes": 100, "width": 64, "height": 36})
		})
		srv.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+prefsHelpers+`
	const sent = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/jobs") && o?.method === "POST") sent.push(o.body); return fetch0(u, o); };
	const cv = document.createElement("canvas"); cv.width = 64; cv.height = 36;
	const png = await new Promise((r) => cv.toBlob(r, "image/png"));
	const dt = new DataTransfer(); dt.items.add(new File([png], "p.png", { type: "image/png" }));
	const inp = document.querySelector("#file"); inp.files = dt.files; inp.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(800);
	const p = document.querySelector("#prompt");
	p.value = "done"; p.dispatchEvent(new Event("input"));
	await wait(50);
	document.querySelector("#go").click();
	await wait(200);
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && btn(sheet(), "Sign out"));
	btn(sheet(), "Sign out").click();
	await yes();
	await until(() => document.querySelector(".signin"));
	await wait(2000);
	return { sent: sent.length, go: document.querySelector("#go").textContent.trim() };`)
	if s["sent"] != float64(0) || !strings.HasPrefix(s["go"].(string), "Sign in to create") || !stopped.Load() {
		t.Errorf("signed out while sending: %v, upload stopped %v", s, stopped.Load())
	}
	noErrors(t, ctx)
}

// TestBrowserPhotoLinks checks the links the person's photos show by: one
// the run on the stage still holds keeps its picture for Edit the prompt,
// and one nothing holds any more lets go of its file.
func TestBrowserPhotoLinks(t *testing.T) {
	runs := &fakeRuns{}
	var mu sync.Mutex
	next := 500
	ts := fakeSite(t, func(srv *Server) {
		runs.mount(srv)
		srv.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			id := next
			next++
			mu.Unlock()
			writeTestJSON(w, http.StatusCreated, map[string]any{"id": id, "content_type": "image/png", "bytes": 100, "width": 64, "height": 36})
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const cv = document.createElement("canvas"); cv.width = 64; cv.height = 36;
	const png = await new Promise((r) => cv.toBlob(r, "image/png"));
	const inp = document.querySelector("#file");
	const add = async (name) => { const dt = new DataTransfer(); dt.items.add(new File([png], name, { type: "image/png" })); inp.files = dt.files; inp.dispatchEvent(new Event("change", { bubbles: true })); await wait(800); };
	const src = () => document.querySelector("#files .thumb img")?.getAttribute("src");
	const shows = async (u) => { const im = new Image(); im.src = u; try { await im.decode(); return true; } catch { return false; } };
	const out = {};
	await add("a.png");
	const a = src();
	await press("done", (s) => s.done);
	// Off the composer, the photo is still the run's.
	document.querySelector("#files .thumb .rm").click();
	await wait(400);
	out.held = [document.querySelectorAll("#files .thumb").length, await shows(a)];
	[...document.querySelectorAll("#stage button")].find((b) => b.textContent.trim() === "Back to the last result").click();
	await wait(600);
	[...document.querySelectorAll("#stage .jacts > button")].find((b) => b.textContent.trim() === "Edit the prompt").click();
	await wait(800);
	out.back = [src() === a, await shows(src())];
	// Off the composer again, and the next run holds another photo: nothing shows the first one any more.
	document.querySelector("#files .thumb .rm").click();
	await wait(400);
	await add("b.png");
	const b = src();
	await press("done", (s) => s.done);
	out.gone = [await shows(a), await shows(b)];
	// Continue puts the clip's last frame in place of what the composer held:
	// a photo added after the run, which no run took, lets go of its file.
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	await press("done:video", () => !!document.querySelector("#stage .jacts button[aria-label=Continue]"));
	const srcs = () => [...document.querySelectorAll("#files .thumb img")].map((i) => i.getAttribute("src"));
	const was = srcs();
	await add("c.png");
	const c = srcs().find((u) => !was.includes(u));
	[...document.querySelectorAll("#stage button")].find((b) => b.textContent.trim() === "Back to the last result").click();
	await wait(600);
	document.querySelector("#stage .jacts button[aria-label=Continue]").click();
	for (let i = 0; i < 40 && !document.querySelector("#files .thumb[data-k=flf72]"); i++) await wait(100);
	out.dropped = [!!c, await shows(c)];
	return out;`)
	if fmt.Sprint(s["held"]) != "[0 true]" || fmt.Sprint(s["back"]) != "[true true]" || fmt.Sprint(s["gone"]) != "[false true]" ||
		fmt.Sprint(s["dropped"]) != "[true false]" {
		t.Errorf("photo links: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserDraftAcrossSignIn checks that signing in keeps what the person
// put together: the kind, the model they picked, a setting, the photo and the
// words come back after Telegram's page, while a model they never picked
// gives way to their own default. A file the browser could not keep is said,
// and a page the browser brings back from its cache drops what it kept.
func TestBrowserDraftAcrossSignIn(t *testing.T) {
	var signed atomic.Bool
	ts := fakeSite(t, func(srv *Server) {
		srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
			if !signed.Load() {
				writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in", "message": "sign in first"})
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]any{
				"user_id": 1, "name": "Ada", "language": "en", "currency": "USD",
				"default_models": map[string]any{"image": "google/gemini-nano-banana-2.1"},
				"connections":    []map[string]any{{"id": "c1", "provider": "openrouter", "last4": "abcd", "status": "ok"}},
				"free": map[string]any{
					"images": map[string]any{"used": 0, "limit": 5, "left": 5, "window_seconds": 86400},
					"videos": map[string]any{"used": 0, "limit": 1, "left": 1, "window_seconds": 604800},
				},
			})
		})
		// Telegram's page, signed in at once.
		srv.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
			signed.Store(true)
			http.Redirect(w, r, r.URL.Query().Get("return_to"), http.StatusFound)
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	look := `
	const head = () => document.querySelector("#how .mhead")?.textContent || "";
	const ratio = () => document.querySelector("#how .ratios .rt[aria-checked=true]")?.dataset.v || "";
	const look = () => ({ kind: document.querySelector("#kind > button[aria-checked=true], #kind > button[aria-pressed=true], #kind > button.on")?.textContent.trim() || "",
	  head: head(), ratio: ratio(), prompt: document.querySelector("#prompt").value,
	  thumbs: [...document.querySelectorAll("#files .thumb img")].filter((i) => i.complete && i.naturalWidth > 0).length });
	`
	before := js[map[string]any](t, ctx, pageHelpers+look+`
	const p = document.querySelector("#prompt");
	p.value = "a red kite"; p.dispatchEvent(new Event("input"));
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	document.querySelector("#how .mhead").click();
	await wait(700);
	const q = document.querySelector("#pksearch");
	q.value = "Seedance"; q.dispatchEvent(new Event("input", { bubbles: true }));
	await wait(800);
	const row = document.querySelector("#pkrows .frow");
	([...row.querySelectorAll(".pill:not(.old)")].find((b) => b.textContent.trim() === "2.0 Mini") || row.querySelector(".fhit")).click();
	await wait(900);
	[...document.querySelectorAll("#how .ratios .rt")].find((b) => b.getAttribute("aria-checked") !== "true" && !b.classList.contains("more") && !b.disabled).click();
	await wait(500);
	const cv = document.createElement("canvas"); cv.width = 64; cv.height = 36;
	cv.getContext("2d").fillRect(0, 0, 64, 36);
	const png = await new Promise((r) => cv.toBlob(r, "image/png"));
	const dt = new DataTransfer(); dt.items.add(new File([png], "kite.png", { type: "image/png" }));
	const inp = document.querySelector("#file"); inp.files = dt.files; inp.dispatchEvent(new Event("change", { bubbles: true }));
	await wait(900);
	return look();`)
	js[bool](t, ctx, `document.querySelector("#top .signin").click(); return true;`)
	time.Sleep(1500 * time.Millisecond)
	ready(t, ctx)
	after := js[map[string]any](t, ctx, pageHelpers+look+`
	await wait(600);
	const out = { ...look(), signed: !!document.querySelector("#meBtn"), toast: document.querySelector(".toast")?.textContent || "" };
	document.querySelectorAll("#kind > button")[0].click();
	await wait(700);
	out.image = head();
	return out;`)
	if before["thumbs"] != 1.0 || !strings.Contains(fmt.Sprint(before["head"]), "Seedance") || before["ratio"] == "" || before["prompt"] != "a red kite" {
		t.Fatalf("before signing in: %v", before)
	}
	for _, k := range []string{"kind", "head", "ratio", "prompt", "thumbs"} {
		if fmt.Sprint(after[k]) != fmt.Sprint(before[k]) {
			t.Errorf("%s after signing in = %v, before %v", k, after[k], before[k])
		}
	}
	if after["signed"] != true || after["toast"] != "" || !strings.Contains(fmt.Sprint(after["image"]), "Nano Banana") {
		t.Errorf("after signing in: %v", after)
	}
	// A file the load could not keep (its write did not end in time): the composer says so.
	js[bool](t, ctx, `
	const d = { v: 1, at: Date.now(), kind: "image", model: {}, pay: {}, wanted: {}, frames: {}, files: [{ kind: "image", name: "lost.png", w: 4, h: 4, blob: "none",
	  meta: { content_type: "image/png", bytes: 10, width: 4, height: 4 }, upload: 0 }], lost: 0 };
	sessionStorage.setItem("framy.compose", JSON.stringify(d));
	location.reload();
	return true;`)
	time.Sleep(800 * time.Millisecond)
	ready(t, ctx)
	lost := js[map[string]any](t, ctx, `
	await new Promise((r) => setTimeout(r, 400));
	return { toast: document.querySelector(".toast")?.textContent || "", thumbs: document.querySelectorAll("#files .thumb").length };`)
	if lost["toast"] != "Add the files again: the browser did not keep them." || lost["thumbs"] != 0.0 {
		t.Errorf("a file not kept: %v", lost)
	}
	// Back from Telegram's page by the back button, out of the page cache: the
	// load did not happen, and what was kept for it goes, file and all.
	cached := js[map[string]any](t, ctx, `
	const idb = (fn) => new Promise((ok, no) => { const r = indexedDB.open("framy-draft", 1); r.onupgradeneeded = () => r.result.createObjectStore("files");
	  r.onsuccess = () => { const db = r.result, tx = db.transaction("files", "readwrite"), q = fn(tx.objectStore("files")); tx.oncomplete = () => { db.close(); ok(q?.result); }; tx.onerror = () => no(tx.error); }; r.onerror = () => no(r.error); });
	await idb((st) => st.put({ at: Date.now(), file: new Blob(["x"]) }, "kept"));
	sessionStorage.setItem("framy.compose", JSON.stringify({ v: 1, at: Date.now(), kind: "video", files: [{ kind: "image", blob: "kept" }], lost: 0 }));
	dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
	await new Promise((r) => setTimeout(r, 300));
	return { draft: sessionStorage.getItem("framy.compose"), file: !!(await idb((st) => st.get("kept"))) };`)
	if cached["draft"] != nil || cached["file"] != false {
		t.Errorf("back from the page cache: %v", cached)
	}
	noErrors(t, ctx)
}

// TestBrowserResultStays checks a result on the stage is drawn once: the end
// of the run reads the account again, and a new state of the account (the
// settings opened, the page back from another tab) leaves the picture, the
// video and the frames of a press as they were, a video with its sound on.
func TestBrowserResultStays(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	let mes = 0;
	addEventListener("framy:me", () => mes++);
	const out = {};
	await press("done", (s) => s.done);
	document.querySelector("#stage .jbox img").__mark = 1;
	for (let i = 0; i < 30 && !mes; i++) await wait(100);
	out.ended = [mes > 0, document.querySelector("#stage .jbox img")?.__mark === 1];
	window.dispatchEvent(new Event("framy:me"));
	out.me = document.querySelector("#stage .jbox img")?.__mark === 1;

	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	mes = 0;
	await press("done:video", () => document.querySelector("#stage .player video"));
	document.querySelector("#stage .player video").__mark = 1;
	document.querySelector("#stage .player [aria-label='Sound on']").click();
	for (let i = 0; i < 30 && !mes; i++) await wait(100);
	window.dispatchEvent(new Event("framy:me"));
	const v = document.querySelector("#stage .player video");
	out.video = [mes > 0, v?.__mark === 1, v?.muted];

	document.querySelectorAll("#kind > button")[0].click();
	await wait(700);
	document.querySelector("#stage .addf").click(); await wait(500);
	mes = 0;
	await press("frames:done,done", () => document.querySelectorAll("#stage .jbox.cell.done img").length === 2);
	document.querySelectorAll("#stage .jbox.cell.done img").forEach((e) => { e.__mark = 1; });
	for (let i = 0; i < 30 && !mes; i++) await wait(100);
	window.dispatchEvent(new Event("framy:me"));
	out.press = [mes > 0, [...document.querySelectorAll("#stage .jbox.cell.done img")].map((e) => e.__mark === 1).join(",")];
	return out;`)
	if fmt.Sprint(s["ended"]) != "[true true]" || s["me"] != true || fmt.Sprint(s["video"]) != "[true true false]" || fmt.Sprint(s["press"]) != "[true true,true]" {
		t.Errorf("the result drawn again: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserVideoMotion checks the videos of a run with and without reduced
// motion. They start by themselves, without sound, only for a person who has
// not asked for less motion; the stage's player then waits on its first
// frame for Play, and so does the whole screen. Each frame of a press has a
// button that stops and starts its clip, and a clip stopped there stays
// stopped while the page goes to the library and back and while the whole
// screen is open over it; the others stop while they are out of sight.
func TestBrowserVideoMotion(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		runs := &fakeRuns{}
		ts := fakeSite(t, runs.mount)
		ctx := browser(t, ts.URL, 1440, 900, false)
		if reduce {
			err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
				_, err := cdp.Call(ctx, tg, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Features: []*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}}})
				return err
			}), chromedp.Reload())
			if err != nil {
				t.Fatal(err)
			}
			ready(t, ctx)
		}
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	const out = { reduce: matchMedia("(prefers-reduced-motion: reduce)").matches };
	const until = async (fn, ms = 4000) => { const end = Date.now() + ms; while (Date.now() < end) { if (fn()) return true; await wait(50); } return !!fn(); };
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	// On the key: the free video of the week is one, and a press here has two.
	[...document.querySelectorAll("#how [data-seg=pay] > button")].find((b) => /OpenRouter/.test(b.textContent))?.click();
	await until(() => !document.querySelector("#composer .q.wait") && !document.querySelector("#go").disabled);
	await press("done:loop", () => document.querySelector("#stage .player video"));
	const one = document.querySelector("#stage .player video");
	// The dots give way to the clip: at once with reduced motion, else once it shows.
	await until(() => !document.querySelector("#stage .jbox > canvas.ht"), 5000);
	await wait(300);
	out.one = [one.autoplay, one.muted, one.paused, !!document.querySelector("#stage .jbox > canvas.ht"), document.querySelector("#stage .player .pp")?.getAttribute("aria-label")];

	document.querySelector("#stage .jacts button[aria-label='Make another']").click();
	await wait(500);
	document.querySelector("#stage .addf").click(); await wait(500);
	await press("frames:video", () => document.querySelectorAll("#stage .jbox.cell.done video").length === 2);
	const vs = () => [...document.querySelectorAll("#stage .jbox.cell.done video")], bs = () => [...document.querySelectorAll("#stage .jbox.cell .tpp")];
	if (!out.reduce) await until(() => vs().every((v) => !v.paused));
	else await wait(900);
	out.cells = vs().map((v) => [v.autoplay, v.muted, v.paused].join(",")).join(" ");
	out.btns = bs().map((b) => b.getAttribute("aria-label") + (b.classList.contains("still") ? "*" : "")).join(",");
	// Play stands beside the button that opens the clip, not inside it.
	out.inside = bs().filter((b) => b.parentElement.closest("button, a[href], [role=button]")).length;
	bs()[0].click();
	await until(() => vs()[0].paused !== out.reduce);
	out.pressed = [vs()[0].paused, bs()[0].getAttribute("aria-label"), document.querySelector("#full").hidden];
	if (out.reduce) { bs()[0].click(); await until(() => vs()[0].paused); }

	document.querySelector("#top .tabs a[data-v=library]").click();
	await until(() => vs().every((v) => v.paused));
	out.away = vs().map((v) => v.paused).join(",");
	document.querySelector("#top .tabs a[data-v=create]").click();
	await wait(600);
	out.back = vs().map((v) => v.paused).join(",");

	document.querySelectorAll("#stage .jbox.cell.done > .fhit")[1].click();
	await until(() => !document.querySelector("#full").hidden && document.querySelectorAll("#full video").length === 2);
	await wait(600);
	out.full = [vs().map((v) => v.paused).join(","), document.querySelectorAll("#full video")[1].paused];
	document.querySelector("#full .fbar .x").click();
	await until(() => document.querySelector("#full").hidden);
	await wait(400);
	out.closed = vs().map((v) => v.paused).join(",");
	return out;`)
		want := map[string]string{
			"one": "[true true false false Pause]", "cells": "true,true,false true,true,false", "btns": "Pause,Pause", "inside": "0", "pressed": "[true Play true]",
			"away": "true,true", "back": "true,false", "full": "[true,true false]", "closed": "true,false",
		}
		if reduce {
			want = map[string]string{
				"one": "[false true true false Play]", "cells": "false,true,true false,true,true", "btns": "Play*,Play*", "inside": "0", "pressed": "[false Pause true]",
				"away": "true,true", "back": "true,true", "full": "[true,true true]", "closed": "true,true",
			}
		}
		if s["reduce"] != reduce {
			t.Fatalf("reduced motion is %v, not %v", s["reduce"], reduce)
		}
		for k, w := range want {
			if got := fmt.Sprint(s[k]); got != w {
				t.Errorf("reduce %v: %s = %s, want %s", reduce, k, got, w)
			}
		}
		noErrors(t, ctx)
	}
}

// TestBrowserPlayerNoLength checks the keys of the player's bar before the
// clip's length is known: they do nothing, and nothing breaks.
func TestBrowserPlayerNoLength(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, runs.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	await press("done:nolength", () => document.querySelector("#stage .player .ptrack"));
	const tr = document.querySelector("#stage .player .ptrack");
	tr.focus();
	const keys = ["PageUp", "PageDown", "Home", "End", "ArrowRight", "ArrowLeft"];
	const kept = keys.map((key) => tr.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));
	await wait(100);
	return { kept, time: document.querySelector("#stage .player .ptime").textContent };`)
	if fmt.Sprint(s["kept"]) != "[true true true true true true]" || s["time"] != "0:00 / 0:00" {
		t.Errorf("keys with no length: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserPlayer checks the site's own player on a joined clip, on a wide
// screen and a phone: no browser controls, the bar cut into the clip's parts
// by their length, the part under a press lit with the time there, the sound,
// and a bar that fits the frame.
func TestBrowserPlayer(t *testing.T) {
	for _, size := range [][2]int64{{1440, 900}, {375, 812}} {
		runs := &fakeRuns{}
		ts := fakeSite(t, runs.mount)
		ctx := browser(t, ts.URL, size[0], size[1], size[0] < 960)
		s := js[map[string]any](t, ctx, pageHelpers+runHelpers+`
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	await press("done:joined", () => document.querySelector("#stage .player .pseg"));
	const pl = document.querySelector("#stage .player"), v = pl.querySelector("video"), tr = pl.querySelector(".ptrack");
	const segs = [...tr.querySelectorAll(".pseg")], w = segs.map((e) => e.getBoundingClientRect().width);
	const lit = () => segs.findIndex((e) => e.classList.contains("on"));
	const r = { controls: v.hasAttribute("controls"), n: segs.length, ratio: w[0] / w[1], lit0: lit(),
	  fits: pl.querySelector(".pbar").scrollWidth <= pl.clientWidth, over: document.documentElement.scrollWidth - innerWidth };
	const b = segs[1].getBoundingClientRect(), x = b.left + b.width / 2, y = b.top + b.height / 2;
	tr.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, clientX: x, clientY: y, pointerId: 7, isPrimary: true }));
	r.lit1 = lit(); r.time = pl.querySelector(".ptime").textContent; r.now = tr.getAttribute("aria-valuenow");
	tr.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, clientX: x, clientY: y, pointerId: 7, isPrimary: true }));
	pl.querySelector("[aria-label='Sound on']").click();
	await wait(150);
	r.muted = v.muted; r.snd = !!pl.querySelector("[aria-label='Sound off']");
	return r;`)
		ratio, _ := s["ratio"].(float64)
		if s["controls"] != false || s["n"] != 2.0 || ratio < 0.9 || ratio > 1.12 || s["lit0"] != 0.0 || s["fits"] != true || s["over"] != 0.0 ||
			s["lit1"] != 1.0 || s["time"] != "0:07 / 0:09" || s["now"] != "7" || s["muted"] != false || s["snd"] != true {
			t.Errorf("%dx%d: the player: %v", size[0], size[1], s)
		}
		noErrors(t, ctx)
	}
}
