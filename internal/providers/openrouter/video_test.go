// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/jobs"
)

func inImage(link string) jobs.Input { return jobs.Input{ContentType: "image/png", URL: link} }

const (
	linkA = "https://files.example/a.png?X-Amz-Signature=sigA"
	linkB = "https://files.example/b.png?X-Amz-Signature=sigB"
)

// bodyJSON is a request body as the fake saw it, for comparing.
func bodyJSON(t *testing.T, body map[string]any) string {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheVideoRequestCarriesTheSettingsAndTheFiles(t *testing.T) {
	const cb = "https://framy.example/hooks/or/7/mac"
	for _, tc := range []struct {
		name     string
		settings map[string]any
		inputs   []jobs.Input
		callback string
		want     string
	}{
		{
			name:     "text to video, settings as the database gives them back",
			settings: map[string]any{"resolution": "768p", "aspect_ratio": "16:9", "duration": 5.0, "generate_audio": true},
			want:     `{"aspect_ratio":"16:9","duration":5,"generate_audio":true,"model":"test/video","prompt":"a fox in the snow","resolution":"768p"}`,
		},
		{
			name:     "a callback url",
			settings: map[string]any{"duration": 6.0},
			callback: cb,
			want:     `{"callback_url":"` + cb + `","duration":6,"model":"test/video","prompt":"a fox in the snow"}`,
		},
		{
			name:     "a size instead of a resolution",
			settings: map[string]any{"size": "1280x720"},
			want:     `{"model":"test/video","prompt":"a fox in the snow","size":"1280x720"}`,
		},
		{
			name:     "a start frame, as a JSON list",
			settings: map[string]any{"frame_images": []any{"first_frame"}},
			inputs:   []jobs.Input{inImage(linkA)},
			want:     `{"frame_images":[{"frame_type":"first_frame","image_url":{"url":"` + linkA + `"},"type":"image_url"}],"model":"test/video","prompt":"a fox in the snow"}`,
		},
		{
			name:     "a start frame, as a Go list",
			settings: map[string]any{"frame_images": []string{"first_frame"}},
			inputs:   []jobs.Input{inImage(linkA)},
			want:     `{"frame_images":[{"frame_type":"first_frame","image_url":{"url":"` + linkA + `"},"type":"image_url"}],"model":"test/video","prompt":"a fox in the snow"}`,
		},
		{
			name:     "a start and an end frame",
			settings: map[string]any{"frame_images": []any{"first_frame", "last_frame"}},
			inputs:   []jobs.Input{inImage(linkA), inImage(linkB)},
			want: `{"frame_images":[` +
				`{"frame_type":"first_frame","image_url":{"url":"` + linkA + `"},"type":"image_url"},` +
				`{"frame_type":"last_frame","image_url":{"url":"` + linkB + `"},"type":"image_url"}],` +
				`"model":"test/video","prompt":"a fox in the snow"}`,
		},
		{
			name:   "files with no frame types are references",
			inputs: []jobs.Input{inImage(linkA), {ContentType: "video/mp4", URL: "https://files.example/c.mp4"}, {ContentType: "audio/mpeg", URL: "https://files.example/d.mp3"}},
			want: `{"input_references":[` +
				`{"image_url":{"url":"` + linkA + `"},"type":"image_url"},` +
				`{"type":"video_url","video_url":{"url":"https://files.example/c.mp4"}},` +
				`{"audio_url":{"url":"https://files.example/d.mp3"},"type":"audio_url"}],` +
				`"model":"test/video","prompt":"a fox in the snow"}`,
		},
		{
			name:     "a nil setting is no setting",
			settings: map[string]any{"resolution": nil},
			want:     `{"model":"test/video","prompt":"a fox in the snow"}`,
		},
		{
			name:     "continuing a video OpenRouter made",
			settings: map[string]any{"previous_job_id": "gen-vid-1761555600-abc123"},
			want:     `{"model":"test/video","previous_job_id":"gen-vid-1761555600-abc123","prompt":"a fox in the snow"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := videoRequest(videoJobOf(tc.settings, tc.inputs...), tc.callback)
			if err != nil {
				t.Fatal(err)
			}
			if got := bodyJSON(t, body); got != tc.want {
				t.Errorf("body\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A person's id never leaves framy in a video request: the Videos API
// documents no user field.
func TestAVideoRequestHasNoUserAndNoProviderPreferences(t *testing.T) {
	body, err := videoRequest(videoJobOf(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"user", "provider", "session_id", "callback_url"} {
		if _, ok := body[k]; ok {
			t.Errorf("the request has %s", k)
		}
	}
}

func TestAVideoRunThatCannotBeRightIsRefusedBeforeItIsSent(t *testing.T) {
	two := []jobs.Input{inImage(linkA), inImage(linkB)}
	for _, tc := range []struct {
		name     string
		prompt   *string // nil: the job's own
		settings map[string]any
		inputs   []jobs.Input
		want     string
	}{
		{name: "nothing to work from", prompt: new(string), want: "prompt is empty"},
		{name: "an unknown setting", settings: map[string]any{"fps": 24.0}, want: "fps is not a setting"},
		{name: "a value outside the enum", settings: map[string]any{"resolution": "8K"}, want: "resolution=8K"},
		{name: "a duration the model does not take", settings: map[string]any{"duration": 7.0}, want: "duration=7"},
		{name: "a fractional duration", settings: map[string]any{"duration": 5.5}, want: "duration=5.5"},
		{name: "a pass-through setting", settings: map[string]any{"negative_prompt": "blur"}, want: "negative_prompt is a setting of the model's own provider"},
		{name: "a field framy fills itself", settings: map[string]any{"callback_url": "https://evil.example/"}, want: "callback_url is not a setting"},
		{name: "a user field", settings: map[string]any{"user": "x"}, want: "user is not a setting"},
		{name: "a size and a resolution", settings: map[string]any{"size": "1280x720", "resolution": "768p"}, want: "size replaces resolution"},
		{name: "a size and an aspect ratio", settings: map[string]any{"size": "1280x720", "aspect_ratio": "16:9"}, want: "size replaces aspect_ratio"},
		{name: "frame types that are not a list", settings: map[string]any{"frame_images": "first_frame"}, inputs: two, want: "must list frame types"},
		{name: "a frame type that is not text", settings: map[string]any{"frame_images": []any{3.0}}, inputs: two, want: "must list frame types"},
		{name: "a frame type the model does not take", settings: map[string]any{"frame_images": []any{"middle_frame"}}, inputs: two, want: `frame of type "middle_frame"`},
		{name: "two frames of one type", settings: map[string]any{"frame_images": []any{"first_frame", "first_frame"}}, inputs: two, want: "two frames"},
		{name: "more frames than files", settings: map[string]any{"frame_images": []any{"first_frame", "last_frame"}}, inputs: two[:1], want: "names 2 frames, and the run has 1 files"},
		{name: "frames and references together", settings: map[string]any{"frame_images": []any{"first_frame"}}, inputs: two, want: "drops the references"},
		{name: "a frame that is not an image", settings: map[string]any{"frame_images": []any{"first_frame"}}, inputs: []jobs.Input{{ContentType: "video/mp4", URL: "https://files.example/c.mp4"}}, want: "a frame is an image"},
		{name: "a file OpenRouter does not take", inputs: []jobs.Input{{ContentType: "application/pdf", URL: "https://files.example/x.pdf"}}, want: "does not take"},
		{name: "a job id that is a path", settings: map[string]any{"previous_job_id": "../../keys"}, want: "not the id of a video job"},
		{name: "a job id that is not text", settings: map[string]any{"previous_job_id": 7.0}, want: "not the id of a video job"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := videoJobOf(tc.settings, tc.inputs...)
			if tc.prompt != nil {
				j.Prompt = *tc.prompt
			}
			f := newFakeOR(t)
			_, err := fakeProvider(f, testCallbacks()).Submit(context.Background(), j)
			pe := classOf(t, err)
			if pe.Class != jobs.Refused || !strings.Contains(pe.Error(), tc.want) {
				t.Errorf("class %s, error %q; want a refusal mentioning %q", pe.Class, pe.Error(), tc.want)
			}
			if n, _, _ := f.counts(); n != 0 {
				t.Errorf("%d requests were sent for a run that cannot be right", n)
			}
		})
	}
}

func TestAFileWithNoLinkIsAStorageFailureNotARefusal(t *testing.T) {
	_, err := videoRequest(videoJobOf(nil, jobs.Input{ContentType: "image/png"}), "")
	if pe := classOf(t, err); pe.Class != jobs.Storage {
		t.Errorf("class %s", pe.Class)
	}
}

func TestAPromptlessVideoFromAPhotoIsSent(t *testing.T) {
	j := videoJobOf(map[string]any{"frame_images": []any{"first_frame"}}, inImage(linkA))
	j.Prompt = "  "
	body, err := videoRequest(j, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["prompt"]; ok {
		t.Errorf("an empty prompt went along: %v", body["prompt"])
	}
}

func TestSubmitSendsTheVideoRequestAndReturnsTheJobId(t *testing.T) {
	f := newFakeOR(t)
	cb := testCallbacks()
	sub, err := fakeProvider(f, cb).Submit(context.Background(), videoJobOf(map[string]any{"resolution": "768p"}))
	if err != nil {
		t.Fatal(err)
	}
	if sub.ProviderJobID != f.id || sub.Outcome != nil {
		t.Errorf("submission %+v: want the job id and no outcome yet", sub)
	}
	h := f.submitted[0]
	if got := h.Get("Authorization"); got != "Bearer "+fakeKey {
		t.Errorf("authorization %q", got)
	}
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type %q", got)
	}
	if got := h.Get("X-Title"); got != "framy" {
		t.Errorf("X-Title %q", got)
	}
	body := f.lastSubmit()
	if body["model"] != "test/video" || body["resolution"] != "768p" {
		t.Errorf("body %v", body)
	}
	if got, want := body["callback_url"], cb.URL(7); got != want {
		t.Errorf("callback_url %v, want %v", got, want)
	}
	if u, _ := body["callback_url"].(string); !strings.HasPrefix(u, "https://framy.example/hooks/or/7/") {
		t.Errorf("callback_url %q is not framy's hook", u)
	}
}

func TestNoCallbackIsSentUnlessCallbacksAreOnAndHTTPS(t *testing.T) {
	good := testCallbacks()
	for name, cb := range map[string]Callbacks{
		"zero":          {},
		"no secret":     {PublicURL: good.PublicURL},
		"no public url": {Secret: good.Secret},
		"plain http":    {PublicURL: "http://framy.example", Secret: good.Secret},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeOR(t)
			if _, err := fakeProvider(f, cb).Submit(context.Background(), videoJobOf(nil)); err != nil {
				t.Fatal(err)
			}
			if _, ok := f.lastSubmit()["callback_url"]; ok {
				t.Errorf("a callback_url was sent: %v", f.lastSubmit()["callback_url"])
			}
		})
	}
}

func TestSubmitFailuresAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		header []string
		want   jobs.Class
	}{
		{"a key OpenRouter does not know", 401, `{"error":{"code":401,"message":"No auth credentials found","metadata":{"error_type":"authentication"}}}`, nil, jobs.Key},
		{"no credit", 402, `{"error":{"code":402,"message":"Insufficient credits"}}`, nil, jobs.Money},
		{"too many requests", 429, `{"error":{"code":429,"message":"slow down"}}`, []string{"Retry-After", "7"}, jobs.RateLimited},
		{"a provider that is down", 503, `{"error":{"code":503,"message":"no provider"}}`, nil, jobs.Unavailable},
		{"a request OpenRouter turns away", 400, `{"error":{"code":400,"message":"bad request"}}`, nil, jobs.Refused},
		{"a content filter", 400, `{"error":{"code":400,"message":"blocked","metadata":{"error_type":"content_policy_violation"}}}`, nil, jobs.Moderation},
		{"an error nobody can read", 500, `<html>oops</html>`, nil, jobs.ProviderFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOR(t)
			f.submitReply = func(w http.ResponseWriter) {
				for i := 0; i+1 < len(tc.header); i += 2 {
					w.Header().Set(tc.header[i], tc.header[i+1])
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}
			_, err := fakeProvider(f, testCallbacks()).Submit(context.Background(), videoJobOf(nil))
			pe := classOf(t, err)
			if pe.Class != tc.want {
				t.Errorf("class %s, want %s (%v)", pe.Class, tc.want, pe)
			}
			if tc.want == jobs.RateLimited && pe.RetryAfter != 7*time.Second {
				t.Errorf("retry after %s", pe.RetryAfter)
			}
			noSecrets(t, err)
		})
	}
}

// An answer the adapter cannot read after OpenRouter took the request must not
// be sent again: it may have been paid.
func TestAnAnswerWithNoJobIdIsNotRetried(t *testing.T) {
	for name, body := range map[string]string{
		"empty":         `{}`,
		"not JSON":      `accepted`,
		"a path as id":  `{"id":"../x","status":"pending"}`,
		"an id with ?":  `{"id":"a?b=c","status":"pending"}`,
		"a number":      `{"id":7}`,
		"a long id":     `{"id":"` + strings.Repeat("a", 300) + `"}`,
		"an empty text": `{"id":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeOR(t)
			f.submitReply = func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, body)
			}
			_, err := fakeProvider(f, testCallbacks()).Submit(context.Background(), videoJobOf(nil))
			pe := classOf(t, err)
			if pe.Class != jobs.ProviderFailed || !strings.Contains(pe.Error(), "may have been paid") {
				t.Errorf("class %s, %q", pe.Class, pe.Error())
			}
		})
	}
}

func TestSubmitNeedsAKey(t *testing.T) {
	f := newFakeOR(t)
	p := fakeProvider(f, testCallbacks(), func(c *Config) { c.Key = func(context.Context, jobs.Job) (string, error) { return "", nil } })
	_, err := p.Submit(context.Background(), videoJobOf(nil))
	if pe := classOf(t, err); pe.Class != jobs.Key {
		t.Errorf("class %s", pe.Class)
	}
	if n, _, _ := f.counts(); n != 0 {
		t.Errorf("%d requests with no key", n)
	}
}

// OpenRouter may answer the request with a job that is over already. The id is
// still returned, so the queue saves it.
func TestSubmitKeepsTheIdOfAJobThatIsOverAtOnce(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		f := newFakeOR(t)
		f.set(videoFailed, `"Request blocked by the content policy"`)
		f.submitReply = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write(f.jobDoc())
		}
		sub, err := fakeProvider(f, testCallbacks()).Submit(context.Background(), videoJobOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		if sub.ProviderJobID != f.id || sub.Outcome == nil || sub.Outcome.Err == nil || sub.Outcome.Err.Class != jobs.Moderation {
			t.Errorf("submission %+v", sub)
		}
	})
	t.Run("completed", func(t *testing.T) {
		f := newFakeOR(t)
		f.complete(0.036)
		f.submitReply = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write(f.jobDoc())
		}
		sub, err := fakeProvider(f, testCallbacks()).Submit(context.Background(), videoJobOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		if sub.Outcome == nil || len(sub.Outcome.Results) != 1 || sub.Outcome.CostUSD == nil || *sub.Outcome.CostUSD != 0.036 {
			t.Errorf("submission %+v", sub)
		}
	})
	t.Run("completed, but the video cannot be fetched yet", func(t *testing.T) {
		f := newFakeOR(t)
		f.complete(0.036)
		f.contentFunc = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }
		f.submitReply = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write(f.jobDoc())
		}
		sub, err := fakeProvider(f, testCallbacks()).Submit(context.Background(), videoJobOf(nil))
		if err != nil {
			t.Fatalf("the id would be lost with this error: %v", err)
		}
		if sub.ProviderJobID != f.id || sub.Outcome != nil {
			t.Errorf("submission %+v: want the id and no outcome, for the poll to finish", sub)
		}
	})
}

func pollJob() jobs.Job {
	j := videoJobOf(nil)
	j.ProviderJobID = "gen-vid-1761555600-abcdefghij0123456789"
	return j
}

func TestAStatusThatIsNotOverIsRunning(t *testing.T) {
	for _, status := range []string{videoPending, videoInProgress, "queued", "something-new"} {
		t.Run(status, func(t *testing.T) {
			f := newFakeOR(t)
			f.set(status, "")
			out, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
			if err != nil {
				t.Fatal(err)
			}
			if !out.Running || out.Err != nil || len(out.Results) != 0 {
				t.Errorf("outcome %+v", out)
			}
			if _, _, d := f.counts(); d != 0 {
				t.Errorf("%d downloads of a video that is not done", d)
			}
		})
	}
}

func TestACompletedVideoIsDownloadedWithTheKeyAndPriced(t *testing.T) {
	f := newFakeOR(t)
	f.complete(0.036)
	// The job object points the URLs elsewhere. They are not used: the key
	// would go with them.
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the job's own URL was fetched: %s", r.URL.Path)
	}))
	t.Cleanup(canary.Close)
	f.urlHost = canary.URL

	out, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
	if err != nil {
		t.Fatal(err)
	}
	if out.Running || out.Err != nil || len(out.Results) != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if r := out.Results[0]; r.ContentType != "video/mp4" || !bytes.Equal(r.Data, mp4) {
		t.Errorf("result %q %q", r.ContentType, r.Data)
	}
	if out.CostUSD == nil || *out.CostUSD != 0.036 {
		t.Errorf("cost %v, want 0.036", out.CostUSD)
	}
	if _, polls, downloads := f.counts(); polls != 1 || downloads != 1 {
		t.Errorf("%d polls and %d downloads", polls, downloads)
	}
	for what, auth := range map[string]string{"poll": f.polls[0], "download": f.downloads[0]} {
		if auth != "Bearer "+fakeKey {
			t.Errorf("the %s carried authorization %q", what, auth)
		}
	}
	if f.queries[0] != "index=0" {
		t.Errorf("download query %q", f.queries[0])
	}
}

func TestEveryVideoOfAJobIsDownloaded(t *testing.T) {
	f := newFakeOR(t)
	f.complete(0.1)
	f.urls = 2
	out, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 2 || strings.Join(f.queries, ",") != "index=0,index=1" {
		t.Errorf("%d results, queries %v", len(out.Results), f.queries)
	}
}

// Downloads go through the client for untrusted links (safehttp in
// production), and the other calls through the plain one.
func TestDownloadsUseTheFetchClient(t *testing.T) {
	f := newFakeOR(t)
	f.complete(0.036)
	var fetched atomic.Int32
	p := fakeProvider(f, testCallbacks(), func(c *Config) {
		c.Fetch = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
			fetched.Add(1)
			if !strings.Contains(r.URL.Path, "/content") {
				t.Errorf("the fetch client was used for %s", r.URL.Path)
			}
			return f.srv.Client().Transport.RoundTrip(r)
		})}
	})
	if _, err := p.Poll(context.Background(), pollJob()); err != nil {
		t.Fatal(err)
	}
	if fetched.Load() != 1 {
		t.Errorf("%d downloads through the fetch client", fetched.Load())
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAFinishedJobWithNoVideoFails(t *testing.T) {
	f := newFakeOR(t)
	f.complete(0.036)
	f.urls = 0
	out, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
	if err != nil {
		t.Fatal(err)
	}
	if out.Err == nil || out.Err.Class != jobs.ProviderFailed || out.CostUSD == nil {
		t.Errorf("outcome %+v", out)
	}
}

func TestAVideoThatEndedWithoutAResultIsClassified(t *testing.T) {
	cost := 0.0
	for _, tc := range []struct {
		name   string
		status string
		err    string
		want   jobs.Class
		in     string
	}{
		{"failed with a content filter in text", videoFailed, `"Generation blocked by content policy"`, jobs.Moderation, "content policy"},
		{"failed with a moderation text", videoFailed, `"Moderation system flagged the output"`, jobs.Moderation, ""},
		{"failed with a plain reason", videoFailed, `"Internal error in the video model"`, jobs.ProviderFailed, "Internal error"},
		{"failed with no reason", videoFailed, ``, jobs.ProviderFailed, "no reason given"},
		{"failed with an error object", videoFailed, `{"code":500,"message":"model crashed","metadata":{"provider_name":"Alibaba"}}`, jobs.ProviderFailed, "model crashed"},
		{"failed with a moderation object", videoFailed, `{"code":400,"message":"blocked","metadata":{"error_type":"content_policy_violation","reasons":["violence"]}}`, jobs.Moderation, "violence"},
		{"failed with a rate limit in the object", videoFailed, `{"code":429,"message":"slow down"}`, jobs.ProviderFailed, ""},
		{"cancelled", videoCancelled, ``, jobs.ProviderFailed, "cancelled"},
		{"expired", videoExpired, ``, jobs.Timeout, "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOR(t)
			f.set(tc.status, tc.err)
			f.cost = &cost
			out, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
			if err != nil {
				t.Fatal(err)
			}
			if out.Err == nil || out.Err.Class != tc.want || len(out.Results) != 0 || out.Running {
				t.Fatalf("outcome %+v (error %v)", out, out.Err)
			}
			if !strings.Contains(out.Err.Error(), tc.in) {
				t.Errorf("error %q lacks %q", out.Err.Error(), tc.in)
			}
			if out.CostUSD == nil {
				t.Errorf("the cost OpenRouter reported was dropped")
			}
		})
	}
}

func TestALongFailureTextIsClipped(t *testing.T) {
	f := newFakeOR(t)
	f.set(videoFailed, `"`+strings.Repeat("x", 5000)+`"`)
	out, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out.Err.Error()); n > 500 {
		t.Errorf("a %d byte error", n)
	}
}

func TestPollHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		// class is what the queue sees; "" is an error with no class, which the
		// queue takes for a provider failure and asks again.
		class jobs.Class
	}{
		{"a key that is gone", 401, `{"error":{"code":401,"message":"bad key","metadata":{"error_type":"authentication"}}}`, jobs.Key},
		{"too many requests", 429, `{"error":{"code":429,"message":"slow"}}`, jobs.RateLimited},
		{"a job OpenRouter does not find", 404, `{"error":{"code":404,"message":"not found"}}`, ""},
		{"a request OpenRouter turns away", 400, `{"error":{"code":400,"message":"bad"}}`, ""},
		{"a server error", 500, `oops`, jobs.ProviderFailed},
		{"a gateway timeout", 504, `oops`, jobs.Timeout},
		{"unavailable", 503, `oops`, jobs.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOR(t)
			f.pollReply = func(w http.ResponseWriter) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}
			_, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
			if err == nil {
				t.Fatal("no error")
			}
			var pe *jobs.Error
			switch {
			case tc.class == "" && errors.As(err, &pe):
				t.Errorf("class %s: the queue would end the run on a GET that said nothing about it", pe.Class)
			case tc.class != "" && (!errors.As(err, &pe) || pe.Class != tc.class):
				t.Errorf("error %v, want class %s", err, tc.class)
			}
			noSecrets(t, err)
		})
	}
}

func TestAnUnreadablePollAnswerIsAskedAgain(t *testing.T) {
	f := newFakeOR(t)
	f.pollReply = func(w http.ResponseWriter) { _, _ = io.WriteString(w, "<html>") }
	_, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
	if pe := classOf(t, err); pe.Class != jobs.ProviderFailed {
		t.Errorf("class %s", pe.Class)
	}
}

func TestAJobWithNoUsableIdIsNotPolled(t *testing.T) {
	for _, id := range []string{"", "../keys", "a/b", "a b", "a?x=1"} {
		f := newFakeOR(t)
		j := pollJob()
		j.ProviderJobID = id
		_, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), j)
		if pe := classOf(t, err); pe.Class != jobs.Internal {
			t.Errorf("id %q: class %s", id, pe.Class)
		}
		if _, polls, _ := f.counts(); polls != 0 {
			t.Errorf("id %q was polled", id)
		}
	}
}

func TestADownloadThatGoesWrongIsAskedAgainWithTheJobsCostKept(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content func(w http.ResponseWriter, r *http.Request)
		class   jobs.Class // "": an error with no class
	}{
		{"a 404, a copy still on its way", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }, ""},
		{"a 400", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }, ""},
		{"a 500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, ""},
		{"a 503", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, jobs.Unavailable},
		{"a page where the video should be", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>")
		}, ""},
		{"a JSON error with a 200", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"error":"x"}`)
		}, ""},
		{"an empty file", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "video/mp4") }, ""},
		{"a key that stopped working", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"code":401,"message":"No auth","metadata":{"error_type":"authentication"}}}`)
		}, jobs.Key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOR(t)
			f.complete(0.036)
			f.contentFunc = tc.content
			_, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
			if err == nil {
				t.Fatal("no error")
			}
			var pe *jobs.Error
			switch {
			case tc.class == "" && errors.As(err, &pe):
				t.Errorf("class %s: a finished job has a video, so the queue must ask again", pe.Class)
			case tc.class != "" && (!errors.As(err, &pe) || pe.Class != tc.class):
				t.Errorf("error %v, want class %s", err, tc.class)
			}
			noSecrets(t, err)
		})
	}
}

func TestAVideoAboveTheLimitEndsTheRunWithItsCost(t *testing.T) {
	f := newFakeOR(t)
	f.complete(0.036)
	f.video = bytes.Repeat([]byte("v"), 2000)
	for name, announce := range map[string]bool{"announced by Content-Length": true, "chunked": false} {
		t.Run(name, func(t *testing.T) {
			f.contentFunc = nil
			if !announce {
				f.contentFunc = func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "video/mp4")
					w.WriteHeader(200)
					for range 4 {
						_, _ = w.Write(f.video[:500])
						w.(http.Flusher).Flush()
					}
				}
			}
			p := fakeProvider(f, testCallbacks(), func(c *Config) { c.MaxResponse = 1000 })
			out, err := p.Poll(context.Background(), pollJob())
			if err != nil {
				t.Fatal(err)
			}
			if out.Err == nil || out.Err.Class != jobs.Storage || len(out.Results) != 0 {
				t.Fatalf("outcome %+v", out)
			}
			if out.CostUSD == nil || *out.CostUSD != 0.036 {
				t.Errorf("cost %v: the run was paid", out.CostUSD)
			}
		})
	}
}

// The key goes to OpenRouter and nowhere else: a download that is redirected
// to another host (a signed link) carries no Authorization, and the link
// itself never reaches an error.
func TestTheKeyDoesNotFollowARedirectToAnotherHost(t *testing.T) {
	var seen atomic.Pointer[string]
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seen.Store(&auth)
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(mp4)
	}))
	t.Cleanup(other.Close)
	// localhost is another host than 127.0.0.1, which is how the fake is reached.
	signed := strings.Replace(other.URL, "127.0.0.1", "localhost", 1) + "/signed?X-Signature=topsecretsig"

	f := newFakeOR(t)
	f.complete(0.036)
	f.contentFunc = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, signed, http.StatusFound) }
	out, err := fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || !bytes.Equal(out.Results[0].Data, mp4) {
		t.Fatalf("outcome %+v", out)
	}
	if got := seen.Load(); got == nil || *got != "" {
		t.Errorf("the other host saw authorization %v", got)
	}

	// And when the signed link fails, the error does not carry it.
	other.Close()
	_, err = fakeProvider(f, testCallbacks()).Poll(context.Background(), pollJob())
	if err == nil {
		t.Fatal("no error for a link that is gone")
	}
	noSecrets(t, err)
}

// noSecrets: neither the key nor a signed link is in an error, since errors go
// to the logs and to people.
func noSecrets(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, secret := range []string{fakeKey, "topsecretsig", "X-Amz-Signature", "sigA", "sigB"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error %q has %q in it", err, secret)
		}
	}
}

// The deadline is the queue's to read: an error from a call that ran out of
// time must keep the context's error and carry no class.
func TestADeadlineIsLeftToTheQueue(t *testing.T) {
	f := newFakeOR(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.pollReply = func(http.ResponseWriter) { <-release }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := fakeProvider(f, testCallbacks()).Poll(ctx, pollJob())
	var pe *jobs.Error
	if errors.As(err, &pe) {
		t.Fatalf("classified as %s", pe.Class)
	}
	if err == nil || ctx.Err() == nil {
		t.Errorf("%v", err)
	}
}
