// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// fakeVideos keeps clips in memory. A test sets err for what the next
// clip runs into, and hold to keep the clips being worked on until it is
// closed.
type fakeVideos struct {
	mu     sync.Mutex
	err    error
	hold   chan struct{}
	asked  []videoCall
	tokens int
}

// videoCall is one call of a Videos method, as it was asked.
type videoCall struct {
	op    string
	user  int64
	token string
	file  int64
	cut   *media.Cut
}

func (v *fakeVideos) VideoUpload(userID int64) (storage.VideoUpload, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.tokens++
	token := fmt.Sprintf("%032x", v.tokens)
	return storage.VideoUpload{
		Token: token, URL: "https://bucket.example/incoming/" + token + "?X-Amz-Signature=secret",
		ExpiresAt: time.Date(2026, 10, 30, 12, 30, 0, 0, time.UTC), MaxBytes: 512 << 20, MaxSeconds: 60,
	}, nil
}

func (v *fakeVideos) work(ctx context.Context, c videoCall) (storage.Asset, error) {
	v.mu.Lock()
	v.asked = append(v.asked, c)
	hold, err := v.hold, v.err
	v.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return storage.Asset{}, ctx.Err()
		}
	}
	if err != nil {
		return storage.Asset{}, err
	}
	secs := 8.0
	if c.cut != nil {
		secs = c.cut.Seconds
	}
	return storage.Asset{ID: 60, ContentType: "video/mp4", Bytes: 4 << 20, Width: 1280, Height: 720, Seconds: secs, FPS: 30, Audio: true}, nil
}

func (v *fakeVideos) TakeVideo(ctx context.Context, userID int64, token string, cut *media.Cut) (storage.Asset, error) {
	return v.work(ctx, videoCall{op: "take", user: userID, token: token, cut: cut})
}

func (v *fakeVideos) CutVideo(ctx context.Context, userID, assetID int64, cut media.Cut) (storage.Asset, error) {
	return v.work(ctx, videoCall{op: "cut", user: userID, file: assetID, cut: &cut})
}

func (v *fakeVideos) LastFrame(_ context.Context, userID, assetID int64) (storage.Asset, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.asked = append(v.asked, videoCall{op: "frame", user: userID, file: assetID})
	if v.err != nil {
		return storage.Asset{}, v.err
	}
	return storage.Asset{ID: 61, ContentType: "image/jpeg", Bytes: 200 << 10, Width: 1280, Height: 720}, nil
}

func (v *fakeVideos) set(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
}

func (v *fakeVideos) calls() []videoCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]videoCall(nil), v.asked...)
}

// withVideos gives the rig clips.
func withVideos(v *fakeVideos) func(*rigOptions) {
	return func(o *rigOptions) {
		prev := o.cfg
		o.cfg = func(c *Config) {
			if prev != nil {
				prev(c)
			}
			c.Videos = v
		}
	}
}

func videoRig(t *testing.T) (*rig, *fakeVideos) {
	t.Helper()
	v := &fakeVideos{}
	return newRig(t, withVideos(v)), v
}

// startClip posts a clip request and returns its task.
func startClip(t *testing.T, r *rig, user int64, body string) string {
	t.Helper()
	resp := r.post(user, "/api/videos", "", body)
	m := jsonOf(t, resp)
	task, _ := m["task"].(string)
	if resp.StatusCode != http.StatusAccepted || m["status"] != "processing" || task == "" {
		t.Fatalf("start %s: %d %v", body, resp.StatusCode, m)
	}
	if loc := resp.Header.Get("Location"); loc != "/api/videos/"+task {
		t.Errorf("Location = %q", loc)
	}
	return task
}

// waitClip polls a task until it ended, and returns the last answer.
func waitClip(t *testing.T, r *rig, user int64, task string) (int, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := r.do(user, http.MethodGet, "/api/videos/"+task, nil)
		m := jsonOf(t, resp)
		if m["status"] != "processing" || time.Now().After(deadline) {
			return resp.StatusCode, m
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAClipIsPutByLinkAndTaken(t *testing.T) {
	r, v := videoRig(t)
	resp := r.post(alice, "/api/videos/uploads", "", "")
	up := jsonOf(t, resp)
	token, _ := up["token"].(string)
	if resp.StatusCode != http.StatusCreated || len(token) != 32 || up["max_bytes"] != float64(512<<20) || up["max_seconds"] != 60.0 {
		t.Fatalf("upload: %d %v", resp.StatusCode, up)
	}
	if u, _ := up["url"].(string); !strings.HasPrefix(u, "https://bucket.example/incoming/") || up["expires_at"] != "2026-10-30T12:30:00Z" {
		t.Errorf("url and expiry: %v", up)
	}

	task := startClip(t, r, alice, `{"upload":"`+token+`"}`)
	status, m := waitClip(t, r, alice, task)
	file, _ := m["file"].(map[string]any)
	if status != http.StatusOK || m["status"] != "done" || m["task"] != task || file == nil {
		t.Fatalf("task: %d %v", status, m)
	}
	if file["id"] != 60.0 || file["content_type"] != "video/mp4" || file["seconds"] != 8.0 || file["fps"] != 30.0 || file["audio"] != true || file["width"] != 1280.0 {
		t.Errorf("file: %v", file)
	}
	if c := v.calls(); len(c) != 1 || c[0].op != "take" || c[0].user != alice || c[0].token != token || c[0].cut != nil {
		t.Errorf("calls: %+v", c)
	}
	// The same request again is that task, and takes nothing again; another
	// person's poll finds nothing.
	if again := startClip(t, r, alice, `{"upload":"`+token+`"}`); again != task {
		t.Errorf("the same clip again started task %s, not %s", again, task)
	}
	if len(v.calls()) != 1 {
		t.Error("the clip was taken twice")
	}
	if resp := r.do(bob, http.MethodGet, "/api/videos/"+task, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob's poll: %d", resp.StatusCode)
	}
	if resp := r.do(0, http.MethodGet, "/api/videos/"+task, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a poll without a person: %d", resp.StatusCode)
	}

	// A part of it is another task, and the part is what the store gets.
	part := startClip(t, r, alice, `{"upload":"`+token+`","start":2.5,"seconds":5}`)
	if part == task {
		t.Fatal("a part of the clip got the whole clip's task")
	}
	if status, m := waitClip(t, r, alice, part); status != http.StatusOK || m["file"].(map[string]any)["seconds"] != 5.0 {
		t.Fatalf("part: %d %v", status, m)
	}
	if c := v.calls(); len(c) != 2 || c[1].cut == nil || *c[1].cut != (media.Cut{Start: 2.5, Seconds: 5}) {
		t.Errorf("calls: %+v", c)
	}
}

func TestAKeptClipIsCut(t *testing.T) {
	r, v := videoRig(t)
	task := startClip(t, r, alice, `{"file":70,"start":0,"seconds":5}`)
	if status, m := waitClip(t, r, alice, task); status != http.StatusOK || m["status"] != "done" {
		t.Fatalf("%d %v", status, m)
	}
	if c := v.calls(); len(c) != 1 || c[0].op != "cut" || c[0].file != 70 || *c[0].cut != (media.Cut{Seconds: 5}) {
		t.Errorf("calls: %+v", c)
	}
}

// A clip that failed is told with the error it got, and the same request
// again starts it over: the clip may have been put again since.
func TestAClipThatFailedSaysWhyAndStartsAgain(t *testing.T) {
	r, v := videoRig(t)
	token := strings.Repeat("ab", 16)
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"too long", &media.TooLongError{Seconds: 75.126, Max: 60}, http.StatusUnprocessableEntity, "video_too_long"},
		{"never put", storage.ErrNoUpload, http.StatusNotFound, "no_upload"},
		{"not found", storage.ErrNotFound, http.StatusNotFound, "not_found"},
		{"a picture", storage.ErrNotVideo, http.StatusUnprocessableEntity, "not_a_video"},
		{"too big", storage.ErrTooBig, http.StatusRequestEntityTooLarge, "too_large"},
		{"unsupported", media.ErrUnsupportedVideo, http.StatusUnsupportedMediaType, "unsupported_type"},
		{"unreadable", media.ErrUnreadableVideo, http.StatusUnprocessableEntity, "unreadable"},
		{"huge frames", media.ErrVideoTooLarge, http.StatusRequestEntityTooLarge, "frame_too_large"},
		{"a part past the end", media.ErrBadCut, http.StatusUnprocessableEntity, "bad_cut"},
		{"took too long", context.DeadlineExceeded, http.StatusServiceUnavailable, "busy"},
		{"something else", errors.New("ffmpeg said /tmp/x/in: secret"), http.StatusInternalServerError, "internal"},
	} {
		v.set(c.err)
		task := startClip(t, r, alice, `{"upload":"`+token+`"}`)
		status, m := waitClip(t, r, alice, task)
		if status != c.status || m["error"] != c.code || m["status"] != "failed" || m["task"] != task {
			t.Errorf("%s: %d %v", c.name, status, m)
		}
		if msg, _ := m["message"].(string); strings.Contains(msg, "secret") {
			t.Errorf("%s: the answer says what the error said: %q", c.name, msg)
		}
		if c.code == "video_too_long" && (m["seconds"] != 75.13 || m["max_seconds"] != 60.0) {
			t.Errorf("too long: %v", m)
		}
	}
	if n := len(v.calls()); n != 11 {
		t.Errorf("%d clips taken, want each failure started again", n)
	}
}

func TestAClipRequestIsChecked(t *testing.T) {
	r, v := videoRig(t)
	token := strings.Repeat("ab", 16)
	for _, c := range []struct {
		name, body string
		status     int
		field      string
	}{
		{"not JSON", `nope`, http.StatusBadRequest, ""},
		{"two objects", `{"file":1,"start":0,"seconds":5}{}`, http.StatusBadRequest, ""},
		{"unknown field", `{"upload":"` + token + `","fps":30}`, http.StatusBadRequest, ""},
		{"neither", `{}`, http.StatusBadRequest, "upload"},
		{"both", `{"upload":"` + token + `","file":1,"start":0,"seconds":5}`, http.StatusBadRequest, "upload"},
		{"start alone", `{"upload":"` + token + `","start":1}`, http.StatusBadRequest, "seconds"},
		{"too short a part", `{"upload":"` + token + `","start":0,"seconds":0.05}`, http.StatusBadRequest, "seconds"},
		{"before the start", `{"upload":"` + token + `","start":-1,"seconds":5}`, http.StatusBadRequest, "seconds"},
		{"a kept file whole", `{"file":70}`, http.StatusBadRequest, "seconds"},
		{"a file of no id", `{"file":-3,"start":0,"seconds":5}`, http.StatusBadRequest, "file"},
		{"a made-up token", `{"upload":"../u/1002/in/x"}`, http.StatusNotFound, ""},
	} {
		resp := r.post(alice, "/api/videos", "", c.body)
		m := jsonOf(t, resp)
		if resp.StatusCode != c.status || (c.field != "" && m["field"] != c.field) {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
	}
	if resp := r.post(0, "/api/videos/uploads", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an upload without a person: %d", resp.StatusCode)
	}
	if n := len(v.calls()); n != 0 {
		t.Errorf("a bad request reached the store %d times", n)
	}
}

func TestNoClipsWithoutStorage(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/api/videos/uploads", "/api/videos", "/api/files/70/last-frame"} {
		resp := r.post(alice, path, "", `{"file":70,"start":0,"seconds":5}`)
		if m := jsonOf(t, resp); resp.StatusCode != http.StatusServiceUnavailable || m["error"] != "storage_unavailable" {
			t.Errorf("%s: %d %v", path, resp.StatusCode, m)
		}
	}
}

// One person has three clips worked on at most; the rest of the service is
// not theirs to fill.
func TestAPersonHasThreeClipsAtATime(t *testing.T) {
	r, v := videoRig(t)
	v.hold = make(chan struct{})
	var tasks []string
	for i := range 3 {
		tasks = append(tasks, startClip(t, r, alice, fmt.Sprintf(`{"file":%d,"start":0,"seconds":5}`, 70+i)))
	}
	resp := r.post(alice, "/api/videos", "", `{"file":80,"start":0,"seconds":5}`)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusServiceUnavailable || m["error"] != "busy" || resp.Header.Get("Retry-After") == "" {
		t.Errorf("a fourth: %d %v", resp.StatusCode, m)
	}
	bobs := startClip(t, r, bob, `{"file":90,"start":0,"seconds":5}`)
	close(v.hold)
	for _, task := range tasks {
		if status, m := waitClip(t, r, alice, task); status != http.StatusOK || m["status"] != "done" {
			t.Errorf("%s: %d %v", task, status, m)
		}
	}
	if status, _ := waitClip(t, r, bob, bobs); status != http.StatusOK {
		t.Errorf("bob's: %d", status)
	}
	// Once they ended, the next one goes.
	startClip(t, r, alice, `{"file":80,"start":0,"seconds":5}`)
}

func TestTheLastFrameOfAClipIsKeptAsAPhoto(t *testing.T) {
	r, v := videoRig(t)
	resp := r.post(alice, "/api/files/70/last-frame", "", "")
	m := jsonOf(t, resp)
	if resp.StatusCode != http.StatusCreated || m["id"] != 61.0 || m["content_type"] != "image/jpeg" || m["seconds"] != nil {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if c := v.calls(); len(c) != 1 || c[0].op != "frame" || c[0].user != alice || c[0].file != 70 {
		t.Errorf("calls: %+v", c)
	}
	for _, c := range []struct {
		name, path string
		err        error
		status     int
		code       string
	}{
		{"not a file id", "/api/files/x/last-frame", nil, http.StatusNotFound, "not_found"},
		{"somebody else's", "/api/files/70/last-frame", storage.ErrNotFound, http.StatusNotFound, "not_found"},
		{"a picture", "/api/files/70/last-frame", storage.ErrNotVideo, http.StatusUnprocessableEntity, "not_a_video"},
		{"unreadable", "/api/files/70/last-frame", media.ErrUnreadableVideo, http.StatusUnprocessableEntity, "unreadable"},
	} {
		v.set(c.err)
		resp := r.post(alice, c.path, "", "")
		if m := jsonOf(t, resp); resp.StatusCode != c.status || m["error"] != c.code {
			t.Errorf("%s: %d %v", c.name, resp.StatusCode, m)
		}
	}
}
