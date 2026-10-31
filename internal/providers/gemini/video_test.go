// SPDX-License-Identifier: Apache-2.0
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

const omni = "gemini-omni-1.1-flash"

// mp4 is the start of an MP4 file, enough for http.DetectContentType.
var mp4 = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isomvideo-bytes")

func omniJob(t *testing.T, settings map[string]any, inputs ...jobs.Input) jobs.Job {
	t.Helper()
	j := jobOf(t, omni, settings, inputs...)
	j.Kind, j.ModelID = catalog.Video, "google/"+omni
	j.Prompt = "a paper boat on a rainy street"
	return j
}

// googleVideo is a fake Gemini for Omni: POST /interactions starts a run,
// GET /interactions/{id} answers from polls in turn, and a file is served
// for a link.
type googleVideo struct {
	t     *testing.T
	mu    sync.Mutex
	start func(w http.ResponseWriter)
	polls []func(w http.ResponseWriter)
	file  func(w http.ResponseWriter, r *http.Request)

	posts, gets, downloads int
	body                   map[string]any
	paths                  []string
}

func (g *googleVideo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("x-goog-api-key") != testKey || r.Header.Get("Api-Revision") != DefaultRevision {
		g.t.Errorf("%s %s without the key or the revision", r.Method, r.URL.Path)
	}
	if strings.Contains(r.URL.RawQuery, "key=") {
		g.t.Errorf("the key in the URL: %s", r.URL.RawQuery)
	}
	g.paths = append(g.paths, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1beta/interactions":
		g.posts++
		raw, _ := io.ReadAll(r.Body)
		g.body = nil
		if err := json.Unmarshal(raw, &g.body); err != nil {
			g.t.Errorf("request body: %v", err)
		}
		g.start(w)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1beta/interactions/"):
		g.gets++
		if len(g.polls) == 0 {
			g.t.Errorf("an unexpected poll")
			w.WriteHeader(500)
			return
		}
		next := g.polls[0]
		if len(g.polls) > 1 {
			g.polls = g.polls[1:]
		}
		next(w)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1beta/files/"):
		g.downloads++
		g.file(w, r)
	default:
		g.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func serveVideo(t *testing.T, g *googleVideo) *Provider {
	t.Helper()
	g.t = t
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return New(Config{
		Base: srv.URL + "/v1beta", HTTP: srv.Client(), Fetch: srv.Client(),
		Key: func(context.Context, jobs.Job) (string, error) { return testKey, nil },
	})
}

const running = `{"id":"v1_ChdvbW5pMTIz","object":"interaction","status":"in_progress","model":"gemini-omni-1.1-flash"}`

// omniUsage: a 5 s video at 720p, 28,960 tokens, after a short prompt and
// some thinking.
const omniUsage = `{"total_input_tokens":12,"total_output_tokens":28960,"total_thought_tokens":300,` +
	`"input_tokens_by_modality":[{"modality":"text","tokens":12}],"output_tokens_by_modality":[{"modality":"video","tokens":28960}]}`

const omniCost = 12*1.5e-6 + 300*9e-6 + 28960*17.5e-6

func finished(content ...string) string {
	return `{"id":"v1_ChdvbW5pMTIz","object":"interaction","status":"completed","model":"gemini-omni-1.1-flash","steps":[` +
		`{"type":"user_input","content":[{"type":"text","text":"a paper boat"}]},` +
		`{"type":"thought","signature":"Eq4B"},` +
		`{"type":"model_output","content":[` + strings.Join(content, ",") + `]}],"usage":` + omniUsage + `}`
}

func videoBlock(data []byte) string {
	return `{"type":"video","mime_type":"video/mp4","data":"` + base64.StdEncoding.EncodeToString(data) + `"}`
}

func poll(t *testing.T, p *Provider, j jobs.Job) jobs.Outcome {
	t.Helper()
	out, err := p.Poll(context.Background(), j)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	return out
}

func TestAVideoStartsInTheBackgroundAndIsPolled(t *testing.T) {
	g := &googleVideo{
		start: reply(200, running),
		polls: []func(http.ResponseWriter){reply(200, running), reply(200, finished(videoBlock(mp4)))},
	}
	p := serveVideo(t, g)
	j := omniJob(t, map[string]any{"resolution": "4K", "aspect_ratio": "9:16"})
	sub, err := p.Submit(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ProviderJobID != "v1_ChdvbW5pMTIz" || sub.Outcome != nil {
		t.Fatalf("submission %+v: the id, and the video later", sub)
	}
	b := g.body
	if b["background"] != true || b["store"] != true {
		t.Errorf("background %v store %v: Google runs a video in the background only when it is stored", b["background"], b["store"])
	}
	if b["model"] != omni {
		t.Errorf("model %v", b["model"])
	}
	rf, _ := b["response_format"].(map[string]any)
	if rf["type"] != "video" || rf["resolution"] != "4k" || rf["aspect_ratio"] != "9:16" {
		t.Errorf("response_format %v: 4K is written 4k", b["response_format"])
	}
	if _, ok := rf["image_size"]; ok {
		t.Errorf("image_size on a video")
	}
	input, _ := b["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["text"] != j.Prompt {
		t.Errorf("input %v", b["input"])
	}

	j.ProviderJobID = sub.ProviderJobID
	if out := poll(t, p, j); !out.Running {
		t.Fatalf("first poll %+v, want running", out)
	}
	out := poll(t, p, j)
	if out.Running || out.Err != nil || len(out.Results) != 1 {
		t.Fatalf("second poll %+v", out)
	}
	if r := out.Results[0]; r.ContentType != "video/mp4" || string(r.Data) != string(mp4) {
		t.Errorf("result %q %q", r.ContentType, r.Data)
	}
	if out.CostUSD == nil || !near(*out.CostUSD, omniCost) {
		t.Errorf("cost %v, want %v: video tokens at the video rate, thinking at the text rate", out.CostUSD, omniCost)
	}
	if g.paths[1] != "GET /v1beta/interactions/v1_ChdvbW5pMTIz" {
		t.Errorf("polled %v", g.paths)
	}
}

func TestARunOverAtOnceNeedsNoPoll(t *testing.T) {
	g := &googleVideo{start: reply(200, finished(videoBlock(mp4)))}
	p := serveVideo(t, g)
	sub, err := p.Submit(context.Background(), omniJob(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if sub.ProviderJobID == "" || sub.Outcome == nil || len(sub.Outcome.Results) != 1 {
		t.Fatalf("submission %+v", sub)
	}
}

func TestAnAnswerWithoutAnIdIsNotSentAgain(t *testing.T) {
	for name, body := range map[string]string{
		"no id":      `{"status":"in_progress"}`,
		"a bad id":   `{"id":"../../files","status":"in_progress"}`,
		"not json":   `<html>`,
		"empty body": ``,
	} {
		t.Run(name, func(t *testing.T) {
			p := serveVideo(t, &googleVideo{start: reply(200, body)})
			_, err := p.Submit(context.Background(), omniJob(t, nil))
			if pe := classOf(t, err); pe.Class != jobs.ProviderFailed {
				t.Errorf("class %s: the run may have been paid", pe.Class)
			}
		})
	}
}

func TestFramesAreNamedInThePrompt(t *testing.T) {
	first := jobs.Input{ContentType: "image/png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("first"))}
	last := jobs.Input{ContentType: "image/jpeg", URL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("last"))}

	g := &googleVideo{start: reply(200, running)}
	p := serveVideo(t, g)
	// The composer may list the last frame first.
	if _, err := p.Submit(context.Background(), omniJob(t, map[string]any{"frame_images": []any{"last_frame", "first_frame"}}, last, first)); err != nil {
		t.Fatal(err)
	}
	input, _ := g.body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input %v", g.body["input"])
	}
	for i, want := range []string{"first", "last"} {
		b := input[i].(map[string]any)
		if b["type"] != "image" || b["data"] != base64.StdEncoding.EncodeToString([]byte(want)) {
			t.Errorf("input %d is %v, want the %s frame: the first frame goes first", i, b, want)
		}
	}
	text := input[2].(map[string]any)
	if text["type"] != "text" || text["text"] != "<FIRST_FRAME> <LAST_FRAME> a paper boat on a rainy street" {
		t.Errorf("text %v: the tags bind the pictures to their roles", text)
	}

	// A first frame alone.
	if _, err := p.Submit(context.Background(), omniJob(t, map[string]any{"frame_images": []string{"first_frame"}}, first)); err != nil {
		t.Fatal(err)
	}
	input, _ = g.body["input"].([]any)
	if text := input[len(input)-1].(map[string]any); text["text"] != "<FIRST_FRAME> a paper boat on a rainy street" {
		t.Errorf("text %v", text)
	}

	// Without frames the pictures go untagged, and Omni decides.
	if _, err := p.Submit(context.Background(), omniJob(t, nil, first)); err != nil {
		t.Fatal(err)
	}
	input, _ = g.body["input"].([]any)
	if text := input[len(input)-1].(map[string]any); text["text"] != "a paper boat on a rainy street" {
		t.Errorf("text %v", text)
	}

	posts := g.posts
	for name, c := range map[string]struct {
		frames any
		inputs []jobs.Input
	}{
		"a last frame alone":          {[]any{"last_frame"}, []jobs.Input{last}},
		"more frames than files":      {[]any{"first_frame", "last_frame"}, []jobs.Input{first}},
		"a frame type Omni lacks":     {[]any{"middle_frame"}, []jobs.Input{first}},
		"the same frame twice":        {[]any{"first_frame", "first_frame"}, []jobs.Input{first, last}},
		"not a list":                  {"first_frame", []jobs.Input{first}},
		"a video as a frame":          {[]any{"first_frame"}, []jobs.Input{{ContentType: "video/mp4", URL: "data:video/mp4;base64,AAAA"}}},
		"a list of something else":    {[]any{1}, []jobs.Input{first}},
		"frames on a model with none": {nil, nil},
	} {
		if c.frames == nil {
			j := jobOf(t, nano21, map[string]any{"frame_images": []any{"first_frame"}}, first)
			if _, err := p.Submit(context.Background(), j); classOf(t, err).Class != jobs.Invalid {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if _, err := p.Submit(context.Background(), omniJob(t, map[string]any{"frame_images": c.frames}, c.inputs...)); classOf(t, err).Class != jobs.Invalid {
			t.Errorf("%s: %v, want invalid_request", name, err)
		}
	}
	if g.posts != posts {
		t.Errorf("%d paid calls for runs refused before anything", g.posts-posts)
	}
}

func TestAVideoToEditGoesInlineBeforeTheWords(t *testing.T) {
	g := &googleVideo{start: reply(200, running)}
	p := serveVideo(t, g)
	clip := jobs.Input{ContentType: "video/quicktime", URL: "data:video/quicktime;base64," + base64.StdEncoding.EncodeToString(mp4)}
	ref := jobs.Input{ContentType: "image/png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}
	if _, err := p.Submit(context.Background(), omniJob(t, nil, clip, ref)); err != nil {
		t.Fatal(err)
	}
	input, _ := g.body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input %v", g.body["input"])
	}
	if b := input[0].(map[string]any); b["type"] != "video" || b["mime_type"] != "video/mov" || b["data"] != base64.StdEncoding.EncodeToString(mp4) {
		t.Errorf("video %v: QuickTime is video/mov at Google", b)
	}
	if b := input[1].(map[string]any); b["type"] != "image" {
		t.Errorf("reference %v", b)
	}
	if b := input[2].(map[string]any); b["type"] != "text" {
		t.Errorf("the words go last in an Omni request: %v", b)
	}
}

func TestContinuingSendsThePreviousInteraction(t *testing.T) {
	g := &googleVideo{start: reply(200, running)}
	p := serveVideo(t, g)
	j := omniJob(t, map[string]any{"previous_interaction_id": "v1_ChdwcmV2aW91cw"})
	j.Prompt = "Make the boat red. Keep everything else the same."
	if _, err := p.Submit(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if g.body["previous_interaction_id"] != "v1_ChdwcmV2aW91cw" || g.body["store"] != true {
		t.Errorf("body %v", g.body)
	}
	for _, bad := range []any{"../files/x", "", 42, "v1 with spaces"} {
		j := omniJob(t, map[string]any{"previous_interaction_id": bad})
		if _, err := p.Submit(context.Background(), j); classOf(t, err).Class != jobs.Invalid {
			t.Errorf("%v: %v", bad, err)
		}
	}
	// An image does not continue: it is never stored.
	if _, err := p.Submit(context.Background(), jobOf(t, nano21, map[string]any{"previous_interaction_id": "v1_x"})); classOf(t, err).Class != jobs.Invalid {
		t.Errorf("an image continued: %v", err)
	}
}

func TestADurationIsSentInGooglesSpelling(t *testing.T) {
	of := offerOf(t, omni)
	max := 10.0
	min := 3.0
	of.Params["duration"] = catalog.Param{Type: catalog.Range, Min: &min, Max: &max}
	defer delete(of.Params, "duration")
	j := omniJob(t, map[string]any{"duration": 6.0})
	j.Offer = of
	pl, err := planFor(j)
	if err != nil {
		t.Fatal(err)
	}
	if pl.format == nil || pl.format.Duration != "6s" {
		t.Errorf("format %+v, want duration 6s", pl.format)
	}
}

func TestAVideoThatEndsWithoutOneSaysWhy(t *testing.T) {
	said := "I can't make a video of a real politician."
	for name, c := range map[string]struct {
		poll   string
		class  jobs.Class
		reason string
		text   string
	}{
		"a blocked prompt": {
			poll:  `{"id":"v1_x","status":"failed","errors":[{"code":"prohibited_content","message":"The prompt violates the usage policies."}]}`,
			class: jobs.Moderation, reason: "PROHIBITED_CONTENT",
		},
		"an error code as a link": {
			poll:  `{"id":"v1_x","status":"failed","errors":[{"code":"https://ai.google.dev/errors#safety","message":"Blocked."}]}`,
			class: jobs.Moderation, reason: "SAFETY",
		},
		"words and no video": {
			poll:  `{"id":"v1_x","status":"completed","steps":[{"type":"model_output","content":[` + textBlock(said) + `]}],"usage":` + omniUsage + `}`,
			class: jobs.Moderation, reason: ReasonNoVideo, text: said,
		},
		"a failure": {
			poll:  `{"id":"v1_x","status":"failed","errors":[{"code":"internal","message":"Internal error."}]}`,
			class: jobs.ProviderFailed,
		},
		"cancelled": {
			poll:  `{"id":"v1_x","status":"cancelled"}`,
			class: jobs.ProviderFailed,
		},
		"waiting for an action": {
			poll:  `{"id":"v1_x","status":"requires_action"}`,
			class: jobs.ProviderFailed,
		},
		"a video with no data": {
			poll:  finished(`{"type":"video","mime_type":"video/mp4"}`),
			class: jobs.ProviderFailed,
		},
		"a video that is not base64": {
			poll:  finished(`{"type":"video","mime_type":"video/mp4","data":"not base64!"}`),
			class: jobs.ProviderFailed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := serveVideo(t, &googleVideo{polls: []func(http.ResponseWriter){reply(200, c.poll)}})
			j := omniJob(t, nil)
			j.ProviderJobID = "v1_x"
			out := poll(t, p, j)
			if out.Err == nil || out.Err.Class != c.class {
				t.Fatalf("outcome %+v, want class %s", out, c.class)
			}
			if c.reason == "" {
				return
			}
			var r *Refusal
			if !errors.As(out.Err, &r) || r.Reason != c.reason || r.Text != c.text {
				t.Errorf("refusal %+v, want %s with %q", r, c.reason, c.text)
			}
		})
	}
}

func TestPollingTroubleIsAskedAgain(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
		// class is the error's, "" for one the queue retries as it is.
		class jobs.Class
	}{
		"not found yet":     {404, `{"error":{"code":"not_found","message":"Interaction not found."}}`, ""},
		"a bad request":     {400, `{"error":{"code":"invalid_request","message":"Bad."}}`, ""},
		"down":              {503, `{"error":{"code":"service_unavailable","message":"Down."}}`, jobs.Unavailable},
		"a server error":    {500, `{"error":{"code":"internal","message":"Oops."}}`, jobs.ProviderFailed},
		"throttled":         {429, `{"error":{"code":"rate_limit_exceeded","message":"Slow down. Please retry in 3s."}}`, jobs.RateLimited},
		"a revoked key":     {401, `{"error":{"code":"unauthenticated","message":"API key expired."}}`, jobs.Key},
		"an unreadable 200": {200, `{"status":`, jobs.ProviderFailed},
	} {
		t.Run(name, func(t *testing.T) {
			p := serveVideo(t, &googleVideo{polls: []func(http.ResponseWriter){reply(c.status, c.body)}})
			j := omniJob(t, nil)
			j.ProviderJobID = "v1_x"
			_, err := p.Poll(context.Background(), j)
			if err == nil {
				t.Fatal("no error")
			}
			var pe *jobs.Error
			switch {
			case c.class == "" && errors.As(err, &pe):
				t.Errorf("%v is classed %s: a GET that is no verdict must be asked again", err, pe.Class)
			case c.class != "" && classOf(t, err).Class != c.class:
				t.Errorf("class %s, want %s", classOf(t, err).Class, c.class)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("the key in %v", err)
			}
		})
	}

	p := serveVideo(t, &googleVideo{})
	j := omniJob(t, nil)
	j.ProviderJobID = "../files/x"
	if _, err := p.Poll(context.Background(), j); classOf(t, err).Class != jobs.Internal {
		t.Errorf("a bad id: %v", err)
	}
	img := jobOf(t, nano21, nil)
	if _, err := p.Poll(context.Background(), img); classOf(t, err).Class != jobs.NoAdapter {
		t.Errorf("an image polled: %v", err)
	}
}

func TestAVideoLeftAsAFileIsFetchedFromTheBase(t *testing.T) {
	g := &googleVideo{
		// The link names another host: only the file's name is taken.
		polls: []func(http.ResponseWriter){reply(200, finished(`{"type":"video","mime_type":"video/mp4","uri":"https://files.example/v1beta/files/abc-123:download?alt=media"}`))},
		file: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1beta/files/abc-123:download" || r.URL.Query().Get("alt") != "media" {
				t.Errorf("download %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4)
		},
	}
	p := serveVideo(t, g)
	j := omniJob(t, nil)
	j.ProviderJobID = "v1_x"
	out := poll(t, p, j)
	if out.Err != nil || len(out.Results) != 1 || string(out.Results[0].Data) != string(mp4) || g.downloads != 1 {
		t.Fatalf("outcome %+v, %d downloads", out, g.downloads)
	}

	// A file not written yet is asked for again.
	g.polls = []func(http.ResponseWriter){reply(200, finished(`{"type":"video","uri":"https://x/v1beta/files/abc-123:download?alt=media"}`))}
	g.file = func(w http.ResponseWriter, r *http.Request) {
		reply(404, `{"error":{"code":"not_found","message":"File is processing."}}`)(w)
	}
	if _, err := p.Poll(context.Background(), j); err == nil {
		t.Errorf("a file not ready: no error")
	} else if pe := (*jobs.Error)(nil); errors.As(err, &pe) {
		t.Errorf("a file not ready is %s, want asked again", pe.Class)
	}
	// A link without a file's name is no result.
	g.polls = []func(http.ResponseWriter){reply(200, finished(`{"type":"video","uri":"https://x/elsewhere/video.mp4"}`))}
	if out := poll(t, p, j); out.Err == nil || out.Err.Class != jobs.ProviderFailed {
		t.Errorf("outcome %+v", out)
	}
}

func TestATooLargeVideoIsNotAskedAgain(t *testing.T) {
	big := make([]byte, 4<<10)
	copy(big, mp4)
	p := serveVideo(t, &googleVideo{polls: []func(http.ResponseWriter){reply(200, finished(videoBlock(big)))}})
	p.cfg.MaxVideo = 1 << 10
	j := omniJob(t, nil)
	j.ProviderJobID = "v1_x"
	if out := poll(t, p, j); out.Err == nil || out.Err.Class != jobs.Storage {
		t.Errorf("outcome %+v, want storage", out)
	}
	p.cfg.MaxVideo, p.cfg.MaxVideoAnswer = DefaultMaxVideo, 1<<10
	if out := poll(t, p, j); out.Err == nil || out.Err.Class != jobs.Storage {
		t.Errorf("an answer over its limit: %+v, want storage", out)
	}
}

func TestImageSearchGoesBesideWebSearch(t *testing.T) {
	for name, c := range map[string]struct {
		model    string
		settings map[string]any
		want     string
	}{
		"both":         {nano21, map[string]any{"google_search": true, "image_search": true}, `[{"search_types":["web_search","image_search"],"type":"google_search"}]`},
		"images alone": {flash31, map[string]any{"image_search": true}, `[{"search_types":["image_search"],"type":"google_search"}]`},
		"web alone":    {pro3, map[string]any{"google_search": true}, `[{"type":"google_search"}]`},
		"both off":     {nano21, map[string]any{"google_search": false, "image_search": false}, `null`},
	} {
		t.Run(name, func(t *testing.T) {
			p, u := serve(t, reply(200, answerOK()))
			if _, err := p.Submit(context.Background(), jobOf(t, c.model, c.settings)); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(u.body["tools"])
			if string(got) != c.want {
				t.Errorf("tools %s, want %s", got, c.want)
			}
		})
	}
	// Image Search is 2.1's and 3.1 Flash's only.
	for _, model := range []string{pro3, lite31} {
		p, u := serve(t, reply(200, answerOK()))
		if _, err := p.Submit(context.Background(), jobOf(t, model, map[string]any{"image_search": true})); classOf(t, err).Class != jobs.Invalid || u.calls != 0 {
			t.Errorf("%s with image_search: %v", model, err)
		}
	}
}

func TestAGroundedImageBringsWhatGoogleAsksToShow(t *testing.T) {
	suggestions := `<style>.chip{color:#1f1f1f}</style><div class="container"><a class="chip" href="https://www.google.com/search?q=timareta+butterfly">timareta butterfly</a></div>`
	answer := `{"id":"v1_g","status":"completed","steps":[` +
		`{"type":"google_search_call","id":"call_1","arguments":{"queries":["timareta butterfly","timareta butterfly","  "]}},` +
		`{"type":"google_search_result","call_id":"call_1","result":[{"search_suggestions":` + mustJSON(suggestions) + `}]},` +
		`{"type":"code_execution_result","result":"a string, which is another step's shape"},` +
		`{"type":"model_output","content":[` +
		`{"type":"text","text":"A Timareta butterfly.","annotations":[` +
		`{"type":"url_citation","url":"https://en.wikipedia.org/wiki/Timareta","title":"Timareta","start_index":0,"end_index":10},` +
		`{"type":"url_citation","url":"https://en.wikipedia.org/wiki/Timareta","title":"again"},` +
		`{"type":"url_citation","url":"javascript:alert(1)","title":"not a page"},` +
		`{"type":"file_citation","file_id":"f"}]},` +
		imageBlock(png, "image/png") + `]}],"usage":` + usage1K + `}`
	p, _ := serve(t, reply(200, answer))
	sub, err := p.Submit(context.Background(), job(t, map[string]any{"image_search": true}))
	if err != nil {
		t.Fatal(err)
	}
	out := sub.Outcome
	if out.Err != nil || len(out.Results) != 1 {
		t.Fatalf("outcome %+v", out)
	}
	g := out.Grounding
	if g == nil {
		t.Fatal("no grounding: Google's terms ask that the suggestions be shown")
	}
	if len(g.Suggestions) != 1 || g.Suggestions[0] != suggestions {
		t.Errorf("suggestions %q", g.Suggestions)
	}
	if len(g.Queries) != 1 || g.Queries[0] != "timareta butterfly" {
		t.Errorf("queries %q", g.Queries)
	}
	if len(g.Sources) != 1 || g.Sources[0] != (jobs.Source{URL: "https://en.wikipedia.org/wiki/Timareta", Title: "Timareta"}) {
		t.Errorf("sources %+v", g.Sources)
	}

	// An ungrounded run brings none.
	p, _ = serve(t, reply(200, answerOK()))
	sub, _ = p.Submit(context.Background(), job(t, nil))
	if sub.Outcome.Grounding != nil {
		t.Errorf("grounding %+v without a search", sub.Outcome.Grounding)
	}
}

func mustJSON(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestOutputFormatAndInputDetail(t *testing.T) {
	p, u := serve(t, reply(200, answerOK()))
	ref := jobs.Input{ContentType: "image/png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}
	if _, err := p.Submit(context.Background(), job(t, map[string]any{"output_format": "jpeg", "media_resolution": "ultra_high"}, ref)); err != nil {
		t.Fatal(err)
	}
	rf, _ := u.body["response_format"].(map[string]any)
	if rf["type"] != "image" || rf["mime_type"] != "image/jpeg" {
		t.Errorf("response_format %v", u.body["response_format"])
	}
	input, _ := u.body["input"].([]any)
	if b := input[1].(map[string]any); b["resolution"] != "ultra_high" {
		t.Errorf("reference %v: media_resolution goes on each picture", b)
	}
	if _, ok := u.body["media_resolution"]; ok {
		t.Errorf("media_resolution at the top")
	}
}

func TestAVideoBesideTheImagePrompt(t *testing.T) {
	clip := jobs.Input{ContentType: "video/mp4", URL: "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4)}
	p, u := serve(t, reply(200, answerOK()))
	if _, err := p.Submit(context.Background(), jobOf(t, lite31, nil, clip)); err != nil {
		t.Fatal(err)
	}
	input, _ := u.body["input"].([]any)
	if len(input) != 2 || input[1].(map[string]any)["type"] != "video" || input[1].(map[string]any)["mime_type"] != "video/mp4" {
		t.Errorf("input %v", u.body["input"])
	}
	// Pro takes no video, and nobody takes a sound.
	for _, c := range []jobs.Job{
		jobOf(t, pro3, nil, clip),
		jobOf(t, nano21, nil, jobs.Input{ContentType: "audio/mpeg", URL: "data:audio/mpeg;base64,AAAA"}),
		omniJob(t, nil, jobs.Input{ContentType: "audio/mpeg", URL: "data:audio/mpeg;base64,AAAA"}),
	} {
		p, u := serve(t, reply(200, answerOK()))
		if _, err := p.Submit(context.Background(), c); classOf(t, err).Class != jobs.Invalid || u.calls != 0 {
			t.Errorf("%s with %s: %v", c.Offer.ID, c.Inputs[0].ContentType, err)
		}
	}
}

func TestCostByModality(t *testing.T) {
	prices := pricesOf(offerOf(t, omni))
	for name, c := range map[string]struct {
		usage string
		want  *float64
	}{
		"a video from a picture": {
			`{"total_input_tokens":270,"total_output_tokens":57920,"input_tokens_by_modality":[{"modality":"text","tokens":10},{"modality":"image","tokens":260}],"output_tokens_by_modality":[{"modality":"video","tokens":57920}]}`,
			ptr(270*1.5e-6 + 57920*17.5e-6),
		},
		// Audio has no rate of its own: the dearest rate of its direction.
		"sound in and out": {
			`{"total_input_tokens":100,"total_output_tokens":1000,"input_tokens_by_modality":[{"modality":"audio","tokens":100}],"output_tokens_by_modality":[{"modality":"audio","tokens":1000}]}`,
			ptr(100*1.5e-6 + 1000*17.5e-6),
		},
		"no split": {
			`{"total_input_tokens":100,"total_output_tokens":1000}`,
			ptr(100*1.5e-6 + 1000*17.5e-6),
		},
	} {
		var u usage
		if err := json.Unmarshal([]byte(c.usage), &u); err != nil {
			t.Fatal(err)
		}
		got := cost(prices, &u)
		if got == nil || !near(*got, *c.want) {
			t.Errorf("%s: cost %v, want %v", name, got, *c.want)
		}
	}
	// A descriptor with no rate at all for a direction gives no cost.
	if got := cost([]catalog.Price{{Billable: "output_video", Unit: "token", CostUSD: 1}}, &usage{TotalInput: 5, TotalOutput: 1}); got != nil {
		t.Errorf("cost %v without an input rate", *got)
	}
}

func ptr(f float64) *float64 { return &f }
