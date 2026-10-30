// SPDX-License-Identifier: Apache-2.0
//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// videoConverter is the real ffmpeg with its encoders, or a skip, unless
// FRAMY_MEDIA_TESTS asks for the tests.
func videoConverter(t *testing.T) *media.Converter {
	t.Helper()
	c := converter(t)
	if err := c.CheckVideo(context.Background()); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot make clips here: %v", err)
	}
	return c
}

// clip is an MP4 of secs seconds of the test pattern with a tone.
func clip(t *testing.T, secs string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "clip.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-v", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d="+secs, "-f", "lavfi", "-i", "sine=d="+secs,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest",
		"-metadata", "location=+48.8584+002.2945/", "-y", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("making the clip: %v: %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// put sends data to a presigned link as the browser does.
func put(t *testing.T, link string, data []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, link, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "video/mp4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
}

func (h *harness) incoming(t *testing.T) []string {
	t.Helper()
	var keys []string
	for _, k := range h.bucket.Keys() {
		if strings.HasPrefix(k, "incoming/") {
			keys = append(keys, k)
		}
	}
	return keys
}

func TestAClipPutByLinkIsKeptWithItsLength(t *testing.T) {
	h := newHarness(t, storage.Config{Media: videoConverter(t)})
	ctx := context.Background()
	up, err := h.store.VideoUpload(alice)
	if err != nil {
		t.Fatal(err)
	}
	if !storage.ValidToken(up.Token) || up.MaxBytes != storage.DefaultMaxVideo || up.MaxSeconds != media.DefaultMaxSeconds {
		t.Fatalf("%+v", up)
	}
	put(t, up.URL, clip(t, "2"))
	if keys := h.incoming(t); len(keys) != 1 || keys[0] != "incoming/3001/"+up.Token {
		t.Fatalf("incoming %v", keys)
	}
	// The token is alice's: bob's id is in no key of hers.
	if _, err := h.store.TakeVideo(ctx, bob, up.Token, nil); !errors.Is(err, storage.ErrNoUpload) {
		t.Fatalf("bob takes alice's clip: %v", err)
	}
	for _, token := range []string{"", "x", strings.Repeat("A", 32), "../3002/" + up.Token[8:]} {
		if _, err := h.store.TakeVideo(ctx, alice, token, nil); !errors.Is(err, storage.ErrNoUpload) {
			t.Errorf("%q: %v", token, err)
		}
	}
	a, err := h.store.TakeVideo(ctx, alice, up.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != "upload" || a.ContentType != "video/mp4" || a.Width != 320 || a.Height != 180 ||
		math.Abs(a.Seconds-2) > 0.1 || a.FPS != 30 || !a.Audio || !strings.HasPrefix(a.Key, "u/3001/in/") || !strings.HasSuffix(a.Key, ".mp4") {
		t.Fatalf("%+v", a)
	}
	var secs, fps float64
	var audio bool
	if err := h.pool.QueryRow(ctx, `SELECT seconds, fps, audio FROM framy.asset WHERE id=$1`, a.ID).Scan(&secs, &fps, &audio); err != nil ||
		math.Abs(secs-2) > 0.1 || fps != 30 || !audio {
		t.Fatalf("row %v %v %v: %v", secs, fps, audio, err)
	}
	o, ok := h.bucket.Get(a.Key)
	if !ok || bytes.Contains(o.Data, []byte("48.8584")) {
		t.Errorf("kept %v, with the place: %v", ok, ok && bytes.Contains(o.Data, []byte("48.8584")))
	}
	if keys := h.incoming(t); len(keys) != 0 {
		t.Errorf("incoming after the take: %v", keys)
	}
	// Taken once: the token names nothing now.
	if _, err := h.store.TakeVideo(ctx, alice, up.Token, nil); !errors.Is(err, storage.ErrNoUpload) {
		t.Errorf("a second take: %v", err)
	}
	h.workIsEmpty(t)
}

func TestAClipFramyCannotTakeGoesFromIncoming(t *testing.T) {
	h := newHarness(t, storage.Config{Media: videoConverter(t), MaxVideo: 64 << 10})
	ctx := context.Background()
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"text":    {[]byte("an error page, not a clip"), media.ErrUnsupportedVideo},
		"picture": {pngBytes(t, 8, 8, 255), media.ErrUnsupportedVideo},
		"too big": {make([]byte, 65<<10), storage.ErrTooBig},
	} {
		up, err := h.store.VideoUpload(alice)
		if err != nil {
			t.Fatal(err)
		}
		put(t, up.URL, tc.data)
		if _, err := h.store.TakeVideo(ctx, alice, up.Token, nil); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
		if keys := h.incoming(t); len(keys) != 0 {
			t.Errorf("%s: incoming %v", name, keys)
		}
	}
	var rows int
	_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM framy.asset WHERE user_id=$1`, alice).Scan(&rows)
	if rows != 0 {
		t.Errorf("%d rows", rows)
	}
	h.workIsEmpty(t)
}

func TestATooLongClipWaitsForAPartOfIt(t *testing.T) {
	c := videoConverter(t)
	c.MaxSeconds = 1
	h := newHarness(t, storage.Config{Media: c})
	ctx := context.Background()
	up, err := h.store.VideoUpload(alice)
	if err != nil {
		t.Fatal(err)
	}
	if up.MaxSeconds != 1 {
		t.Errorf("max seconds %v", up.MaxSeconds)
	}
	put(t, up.URL, clip(t, "2"))
	var long *media.TooLongError
	if _, err := h.store.TakeVideo(ctx, alice, up.Token, nil); !errors.As(err, &long) {
		t.Fatalf("%v", err)
	}
	if keys := h.incoming(t); len(keys) != 1 {
		t.Fatalf("the clip went from incoming: %v", keys)
	}
	a, err := h.store.TakeVideo(ctx, alice, up.Token, &media.Cut{Start: 0.5, Seconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(a.Seconds-1) > 0.1 {
		t.Errorf("the part is %v s", a.Seconds)
	}
	if keys := h.incoming(t); len(keys) != 0 {
		t.Errorf("incoming %v", keys)
	}
}

func TestAVideoResultIsReadCutAndItsLastFrameKept(t *testing.T) {
	h := newHarness(t, storage.Config{Media: videoConverter(t)})
	ctx := context.Background()
	var jobID int64
	err := h.pool.QueryRow(ctx, `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, status, finished_at)
		VALUES ($1, 'site', $2, 'test/image', 'openrouter', 'video', 'done', now()) RETURNING id`, alice, offer.ID).Scan(&jobID)
	if err != nil {
		t.Fatal(err)
	}
	made := clip(t, "3")
	if err := h.store.Keep(ctx, jobs.Job{ID: jobID, UserID: alice}, []jobs.Result{{ContentType: "video/mp4", Data: made}}); err != nil {
		t.Fatal(err)
	}
	results, err := h.store.Results(ctx, jobID)
	if err != nil || len(results) != 1 {
		t.Fatalf("%v %v", results, err)
	}
	r := results[0]
	if r.Width != 320 || r.Height != 180 || math.Abs(r.Seconds-3) > 0.1 || !r.Audio {
		t.Fatalf("the result %+v", r)
	}
	// The result is kept as it came: the place is still in it.
	if o, _ := h.bucket.Get(r.Key); !bytes.Equal(o.Data, made) {
		t.Error("the result was changed")
	}

	part, err := h.store.CutVideo(ctx, alice, r.ID, media.Cut{Start: 1, Seconds: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if part.Kind != "upload" || math.Abs(part.Seconds-1.5) > 0.1 || part.ID == r.ID {
		t.Errorf("the part %+v", part)
	}
	frame, err := h.store.LastFrame(ctx, alice, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Kind != "upload" || !strings.HasPrefix(frame.ContentType, "image/") || frame.Width != 320 || frame.Height != 180 || frame.Seconds != 0 {
		t.Errorf("the frame %+v", frame)
	}
	var audio *bool
	_ = h.pool.QueryRow(ctx, `SELECT audio FROM framy.asset WHERE id=$1`, frame.ID).Scan(&audio)
	if audio != nil {
		t.Errorf("a picture's row says audio %v", *audio)
	}

	// Only the person's clips, and only clips.
	if _, err := h.store.CutVideo(ctx, bob, r.ID, media.Cut{Start: 0, Seconds: 1}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("bob cuts alice's result: %v", err)
	}
	if _, err := h.store.LastFrame(ctx, alice, frame.ID); !errors.Is(err, storage.ErrNotVideo) {
		t.Errorf("the last frame of a picture: %v", err)
	}
	if _, err := h.store.CutVideo(ctx, alice, r.ID, media.Cut{Start: 5, Seconds: 1}); !errors.Is(err, media.ErrBadCut) {
		t.Errorf("a part after the end: %v", err)
	}
	h.workIsEmpty(t)
}

// A chat keeps the clip it read from Telegram, cut or whole, and the file
// stays where the chat has it.
func TestAClipFromAChatIsKept(t *testing.T) {
	h := newHarness(t, storage.Config{Media: videoConverter(t)})
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clip")
	if err := os.WriteFile(path, clip(t, "3"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := h.store.KeepVideo(ctx, alice, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != "upload" || a.ContentType != "video/mp4" || a.Width != 320 || math.Abs(a.Seconds-3) > 0.1 || !a.Audio {
		t.Fatalf("%+v", a)
	}
	cut, err := h.store.KeepVideo(ctx, alice, path, &media.Cut{Seconds: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(cut.Seconds-1.5) > 0.1 || cut.ID == a.ID {
		t.Fatalf("the cut: %+v", cut)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the chat's file: %v", err)
	}
	if err := os.WriteFile(path, []byte("not a clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.KeepVideo(ctx, alice, path, nil); !errors.Is(err, media.ErrUnsupportedVideo) {
		t.Errorf("not a clip: %v", err)
	}
	h.workIsEmpty(t)
}
