// SPDX-License-Identifier: Apache-2.0
//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/webp"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/r2/r2test"
	"github.com/FreshLabDev/framy/internal/storage"
)

// thumbConverter is the real ffmpeg with WebP and the video encoders, or a
// skip, unless FRAMY_MEDIA_TESTS asks for the tests.
func thumbConverter(t *testing.T) *media.Converter {
	t.Helper()
	c := videoConverter(t)
	if err := c.CheckThumb(context.Background()); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot write thumbnails here: %v", err)
	}
	return c
}

func doneJob(t *testing.T, h *harness, user int64) int64 {
	t.Helper()
	var id int64
	err := h.pool.QueryRow(context.Background(), `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, status, finished_at)
		VALUES ($1, 'site', $2, 'test/image', 'openrouter', 'image', 'done', now()) RETURNING id`, user, offer.ID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// webpSize decodes the object under key as a WebP.
func webpSize(t *testing.T, h *harness, key string) (int, int) {
	t.Helper()
	o, ok := h.bucket.Get(key)
	if !ok {
		t.Fatalf("no %s in the bucket", key)
	}
	if o.ContentType != "image/webp" {
		t.Errorf("%s is %q", key, o.ContentType)
	}
	pic, err := webp.Decode(bytes.NewReader(o.Data))
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return pic.Bounds().Dx(), pic.Bounds().Dy()
}

func TestAResultIsKeptWithItsThumbnail(t *testing.T) {
	h := newHarness(t, storage.Config{Media: thumbConverter(t)})
	ctx := context.Background()
	job := doneJob(t, h, alice)
	results := []jobs.Result{{ContentType: "image/png", Data: pngBytes(t, 1200, 600, 255)}, {ContentType: "video/mp4", Data: clip(t, "2")}}
	if err := h.store.Keep(ctx, jobs.Job{ID: job, UserID: alice}, results); err != nil {
		t.Fatal(err)
	}
	base := "out/" + strconv.FormatInt(job, 10)
	if w, hh := webpSize(t, h, base+"-0.thumb.webp"); w != media.ThumbLongSide || hh != media.ThumbLongSide/2 {
		t.Errorf("the picture's thumbnail is %d×%d", w, hh)
	}
	// A clip's thumbnail is its poster frame, which is smaller than a
	// thumbnail's limit and keeps its size.
	if w, hh := webpSize(t, h, base+"-1.thumb.webp"); w != 320 || hh != 180 {
		t.Errorf("the clip's thumbnail is %d×%d", w, hh)
	}

	kept, err := h.store.Results(ctx, job)
	if err != nil || len(kept) != 2 {
		t.Fatalf("%v %v", kept, err)
	}
	links, err := h.store.LinkAll(ctx, alice, []int64{kept[0].ID, kept[1].ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	th := links[kept[0].ID].Thumb
	if th == nil || th.Width != media.ThumbLongSide || th.Height != media.ThumbLongSide/2 {
		t.Fatalf("the thumbnail's link %+v", th)
	}
	resp, err := http.Get(th.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if o, _ := h.bucket.Get(base + "-0.thumb.webp"); resp.StatusCode != 200 || !bytes.Equal(body, o.Data) {
		t.Errorf("the thumbnail's link: %d", resp.StatusCode)
	}
	// The thumbnails count towards what the person keeps.
	u, err := h.store.Usage(ctx, alice)
	if err != nil || u.Files != 2 || u.Bytes <= kept[0].Bytes+kept[1].Bytes {
		t.Errorf("usage %+v, the results alone %d: %v", u, kept[0].Bytes+kept[1].Bytes, err)
	}

	// Deleting the results takes their thumbnails.
	if n, err := h.store.DeleteResults(ctx, alice, job); err != nil || n != 2 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	if keys := h.bucket.Keys(); len(keys) != 0 {
		t.Errorf("left in the bucket: %v", keys)
	}
	h.workIsEmpty(t)
}

func TestAResultKeptBeforeThumbnailsGetsOne(t *testing.T) {
	h := newHarness(t, storage.Config{Media: thumbConverter(t)})
	ctx := context.Background()
	job := doneJob(t, h, alice)
	old := "u/3001/out/" + strconv.FormatInt(job, 10) + "-0.png"
	h.bucket.Put(old, r2test.Object{Data: pngBytes(t, 600, 900, 255), ContentType: "image/png"})
	var good int64
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO framy.asset (user_id, kind, job_id, object_key, content_type, bytes, sha256)
		VALUES ($1, 'result', $2, $3, 'image/png', 1, 'x') RETURNING id`, alice, job, old).Scan(&good); err != nil {
		t.Fatal(err)
	}
	broken := resultOf(t, h, alice, job, "u/3001/out/"+strconv.FormatInt(job, 10)+"-1.png")

	made, err := h.store.FillThumbs(ctx, 10)
	if err != nil || made != 1 {
		t.Fatalf("made %d: %v", made, err)
	}
	if w, hh := webpSize(t, h, "u/3001/out/"+strconv.FormatInt(job, 10)+"-0.thumb.webp"); w != 320 || hh != 480 {
		t.Errorf("the thumbnail is %d×%d, want 320×480", w, hh)
	}
	var key *string
	var w, hh *int
	_ = h.pool.QueryRow(ctx, `SELECT thumb_key, thumb_width, thumb_height FROM framy.asset WHERE id=$1`, good).Scan(&key, &w, &hh)
	if key == nil || *w != 320 || *hh != 480 {
		t.Errorf("the row: %v %v %v", key, w, hh)
	}
	// A file framy cannot read is tried a few times, then left.
	var tries int
	_ = h.pool.QueryRow(ctx, `SELECT thumb_tries FROM framy.asset WHERE id=$1`, broken).Scan(&tries)
	if tries != 3 {
		t.Errorf("the broken result was tried %d times", tries)
	}
	if made, err := h.store.FillThumbs(ctx, 10); err != nil || made != 0 {
		t.Errorf("a second pass made %d: %v", made, err)
	}
	h.workIsEmpty(t)
}

// Every view within the window gets the same link, so the browser keeps the
// file; the next window gets a new one.
func TestALinkStaysTheSameThroughItsWindow(t *testing.T) {
	now := time.Now()
	h := newHarness(t, storage.Config{Now: func() time.Time { return now }})
	ctx := context.Background()
	img := pngBytes(t, 8, 8, 255)
	id := upload(t, h, alice, "u/3001/in/a.png", img)
	names := map[int64]string{id: "framy-5-1.png"}
	first, err := h.store.LinkAll(ctx, alice, []int64{id}, names)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := h.store.LinkAll(ctx, alice, []int64{id}, names)
	if first[id].URL != again[id].URL || first[id].Download != again[id].Download {
		t.Error("two views in one window got different links")
	}
	if one, _ := h.store.Link(ctx, alice, id, ""); one != first[id].URL {
		t.Error("Link and LinkAll differ")
	}
	resp, err := http.Get(first[id].URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "private, max-age=4200" {
		t.Errorf("%d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	now = now.Add(time.Hour)
	if next, _ := h.store.LinkAll(ctx, alice, []int64{id}, names); next[id].URL == first[id].URL {
		t.Error("the next window got the same link")
	}
}

// A person deleting a result never waits for its thumbnail: the loop holds
// no lock on the row while it works, and a thumbnail made for a result that
// went meanwhile goes too.
func TestADeleteDoesNotWaitForAThumbnail(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	job := doneJob(t, h, alice)
	key := "u/3001/out/" + strconv.FormatInt(job, 10) + "-0.png"
	id := resultOf(t, h, alice, job, key)
	started, release := make(chan struct{}), make(chan struct{})
	storage.SetThumbMaker(h.store, func(ctx context.Context, _, thumb string) (int, int, int64, error) {
		close(started)
		<-release
		h.bucket.Put(thumb, r2test.Object{Data: []byte("webp"), ContentType: "image/webp"})
		return 320, 320, 4, nil
	})
	type result struct {
		made int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		made, err := h.store.FillThumbs(ctx, 1)
		done <- result{made, err}
	}()
	<-started
	deleted := make(chan error, 1)
	go func() { deleted <- h.store.Delete(ctx, alice, id) }()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the delete waited for the thumbnail")
	}
	close(release)
	if r := <-done; r.err != nil || r.made != 0 {
		t.Fatalf("made %d: %v", r.made, r.err)
	}
	if keys := h.bucket.Keys(); len(keys) != 0 {
		t.Errorf("left in the bucket: %v", keys)
	}
}

// A thumbnail made while nothing deletes its result is on the row, and the
// try counts once.
func TestAThumbnailIsRecordedOnItsRow(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	job := doneJob(t, h, alice)
	key := "u/3001/out/" + strconv.FormatInt(job, 10) + "-0.png"
	id := resultOf(t, h, alice, job, key)
	storage.SetThumbMaker(h.store, func(_ context.Context, _, thumb string) (int, int, int64, error) {
		h.bucket.Put(thumb, r2test.Object{Data: []byte("webp"), ContentType: "image/webp"})
		return 320, 160, 4, nil
	})
	if made, err := h.store.FillThumbs(ctx, 5); err != nil || made != 1 {
		t.Fatalf("made %d: %v", made, err)
	}
	var thumb *string
	var tries int
	if err := h.pool.QueryRow(ctx, `SELECT thumb_key, thumb_tries FROM framy.asset WHERE id = $1`, id).Scan(&thumb, &tries); err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(key, ".png") + ".thumb.webp"; thumb == nil || *thumb != want || tries != 1 {
		t.Errorf("thumb %v, tries %d", thumb, tries)
	}
	if err := h.store.Delete(ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	if keys := h.bucket.Keys(); len(keys) != 0 {
		t.Errorf("left in the bucket: %v", keys)
	}
}

// An SVG is kept as an .svg with the size its root declares, by bytes or by
// a link whatever type the provider names, and gets no thumbnail: ffmpeg
// does not draw one, and the site shows the file itself.
func TestAnSVGIsKeptAsItCame(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	drawn := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1365 1024"><rect width="9" height="9"/></svg>`)
	links := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(drawn)
	}))
	defer links.Close()
	storage.SetThumbMaker(h.store, func(context.Context, string, string) (int, int, int64, error) {
		t.Error("a thumbnail was made of an SVG")
		return 0, 0, 0, errors.New("no")
	})
	job := doneJob(t, h, alice)
	results := []jobs.Result{{ContentType: "image/svg+xml", Data: drawn}, {Data: drawn}, {URL: links.URL + "/out"}}
	if err := h.store.Keep(ctx, jobs.Job{ID: job, UserID: alice}, results); err != nil {
		t.Fatal(err)
	}
	kept, err := h.store.Results(ctx, job)
	if err != nil || len(kept) != 3 {
		t.Fatalf("%d results: %v", len(kept), err)
	}
	for i, a := range kept {
		if want := "out/" + strconv.FormatInt(job, 10) + "-" + strconv.Itoa(i) + ".svg"; a.Key != want ||
			a.ContentType != "image/svg+xml" || a.Width != 1365 || a.Height != 1024 || a.Thumb != nil {
			t.Errorf("result %d: %+v", i, a)
		}
		if o, ok := h.bucket.Get(a.Key); !ok || o.ContentType != "image/svg+xml" || !bytes.Equal(o.Data, drawn) {
			t.Errorf("result %d in the bucket: %v %q", i, ok, o.ContentType)
		}
	}
	if made, err := h.store.FillThumbs(ctx, 5); err != nil || made != 0 {
		t.Fatalf("made %d: %v", made, err)
	}
	var tries int
	if err := h.pool.QueryRow(ctx, `SELECT max(thumb_tries) FROM framy.asset WHERE job_id = $1`, job).Scan(&tries); err != nil || tries != 0 {
		t.Errorf("tries %d: %v", tries, err)
	}
	if storage.Ext("image/svg+xml") != ".svg" {
		t.Errorf("an SVG downloads as %q", storage.Ext("image/svg+xml"))
	}
}
