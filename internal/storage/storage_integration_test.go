// SPDX-License-Identifier: Apache-2.0
//go:build integration

// The store with a real PostgreSQL, a fake bucket that checks signatures as
// R2 does, and the real ffmpeg:
//
//	FRAMY_TEST_DATABASE_URL=postgres://… go test -tags=integration ./internal/storage/
package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/r2/r2test"
	"github.com/FreshLabDev/framy/internal/storage"
	"github.com/FreshLabDev/framy/migrations"
)

const (
	alice = int64(3001)
	bob   = int64(3002)
)

var offer = catalog.Offer{ID: "openrouter:test/image", ModelID: "test/image", Provider: "openrouter", ProviderModelID: "test/image", Kind: catalog.Image}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("FRAMY_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set FRAMY_TEST_DATABASE_URL to run the database tests")
	}
	if u, err := url.Parse(raw); err != nil || !strings.Contains(strings.TrimPrefix(u.Path, "/"), "test") {
		t.Fatal("FRAMY_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\"")
	}
	ctx := context.Background()
	store, err := db.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	bootstrap, err := os.ReadFile(filepath.Join("..", "..", "deploy", "core-init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	pool := store.Pool()
	if _, err := pool.Exec(ctx, string(bootstrap)); err != nil {
		t.Fatalf("core bootstrap: %v", err)
	}
	if err := store.Migrate(ctx, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, stmt := range []string{
		`TRUNCATE framy.chat_run, framy.job_input, framy.asset, framy.job, framy.river_job`,
		`UPDATE framy.runtime_state SET bucket_alert_step = 0`,
		`INSERT INTO core.person (telegram_user_id) VALUES (3001), (3002) ON CONFLICT DO NOTHING`,
		`INSERT INTO framy.catalog_model (id, kind, name, maker) VALUES ('test/image', 'image', 'Test image', 'test') ON CONFLICT DO NOTHING`,
		`INSERT INTO framy.catalog_offer (id, model_id, provider, provider_model_id, name, fetched_at)
		 VALUES ('openrouter:test/image', 'test/image', 'openrouter', 'test/image', 'Test image', now()) ON CONFLICT DO NOTHING`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return pool
}

// converter is the real ffmpeg, or a skip where it cannot run the tests,
// unless FRAMY_MEDIA_TESTS asks for them.
func converter(t *testing.T) *media.Converter {
	t.Helper()
	c := &media.Converter{}
	if err := c.Check(context.Background()); err != nil {
		if os.Getenv("FRAMY_MEDIA_TESTS") != "" {
			t.Fatalf("ffmpeg: %v", err)
		}
		t.Skipf("ffmpeg cannot run the media tests here: %v", err)
	}
	return c
}

type harness struct {
	pool   *pgxpool.Pool
	bucket *r2test.Server
	store  *storage.Store
	work   string
	mu     sync.Mutex
	told   []string
	notify func(context.Context, string) error
}

func newHarness(t *testing.T, cfg storage.Config) *harness {
	t.Helper()
	h := &harness{pool: testPool(t), bucket: r2test.New(t), work: filepath.Join(t.TempDir(), "work")}
	h.notify = func(context.Context, string) error { return nil }
	cfg.Pool, cfg.Bucket, cfg.Work = h.pool, h.bucket.Client(t), media.Work{Root: h.work}
	if cfg.Media == nil {
		cfg.Media = &media.Converter{}
	}
	// The fake provider below is on the loopback, which safehttp refuses.
	cfg.Fetch = http.DefaultClient
	cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.Notify = func(ctx context.Context, text string) error {
		h.mu.Lock()
		h.told = append(h.told, text)
		notify := h.notify
		h.mu.Unlock()
		return notify(ctx, text)
	}
	s, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.store = s
	return h
}

// workIsEmpty: no working file outlives its work.
func (h *harness) workIsEmpty(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(h.work)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a working file outlived its work: %s", e.Name())
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "media", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pngBytes(t *testing.T, w, h int, alpha uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 120, A: alpha})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestAnIPhonePhotoIsKeptNormalized(t *testing.T) {
	c := converter(t)
	h := newHarness(t, storage.Config{Media: c})
	a, err := h.store.Upload(context.Background(), alice, bytes.NewReader(fixture(t, "iphone.heic")))
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentType != "image/jpeg" || a.Width != 1200 || a.Height != 1600 || !strings.HasPrefix(a.Key, "u/3001/in/") || !strings.HasSuffix(a.Key, ".jpg") {
		t.Errorf("%+v", a)
	}
	o, ok := h.bucket.Get(a.Key)
	if !ok || o.ContentType != "image/jpeg" || int64(len(o.Data)) != a.Bytes || sum(o.Data) != a.SHA256 {
		t.Fatalf("the object is %d bytes %q, the row says %d %s", len(o.Data), o.ContentType, a.Bytes, a.SHA256)
	}
	if bytes.Contains(o.Data, []byte("GPS")) || bytes.Contains(o.Data, []byte("iPhone")) {
		t.Error("the GPS went into the bucket")
	}
	var bytesInDB int64
	if err := h.pool.QueryRow(context.Background(), `SELECT bytes FROM framy.asset WHERE id=$1 AND user_id=$2 AND kind='upload'`, a.ID, alice).Scan(&bytesInDB); err != nil || bytesInDB != a.Bytes {
		t.Errorf("the row: %d %v", bytesInDB, err)
	}
	u, err := h.store.Usage(context.Background(), alice)
	if err != nil || u.Files != 1 || u.Bytes != a.Bytes {
		t.Errorf("usage %+v %v", u, err)
	}
	// An AVIF and a PNG with alpha come through too.
	avif, err := h.store.Upload(context.Background(), alice, bytes.NewReader(fixture(t, "tiled.avif")))
	if err != nil || avif.ContentType != "image/jpeg" || avif.Width != 1600 {
		t.Errorf("the AVIF: %+v %v", avif, err)
	}
	alpha, err := h.store.Upload(context.Background(), alice, bytes.NewReader(pngBytes(t, 64, 48, 100)))
	if err != nil || alpha.ContentType != "image/png" || !strings.HasSuffix(alpha.Key, ".png") {
		t.Errorf("the PNG with alpha: %+v %v", alpha, err)
	}
	h.workIsEmpty(t)
}

func TestWhatIsNotAPhotoIsNotKept(t *testing.T) {
	c := converter(t)
	h := newHarness(t, storage.Config{Media: c, MaxUpload: 1 << 20})
	ctx := context.Background()
	if _, err := h.store.Upload(ctx, alice, strings.NewReader("<html>not a photo</html>")); !errors.Is(err, media.ErrUnsupported) {
		t.Errorf("html: %v", err)
	}
	if _, err := h.store.Upload(ctx, alice, bytes.NewReader(make([]byte, 2<<20))); !errors.Is(err, storage.ErrTooBig) {
		t.Errorf("2 MB over a 1 MB limit: %v", err)
	}
	// A body that breaks off is not a photo either.
	if _, err := h.store.Upload(ctx, alice, io.MultiReader(bytes.NewReader(fixture(t, "exif.jpg")[:100]), errReader{})); err == nil {
		t.Error("a cut upload was kept")
	}
	if keys := h.bucket.Keys(); len(keys) != 0 {
		t.Errorf("objects kept: %v", keys)
	}
	if u, _ := h.store.Usage(ctx, alice); u.Files != 0 {
		t.Errorf("rows kept: %+v", u)
	}
	h.workIsEmpty(t)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// provider is an upstream that fetches its inputs as OpenRouter would and
// answers with what results says.
type provider struct {
	mu      sync.Mutex
	fetched map[int64][][]byte
	results func(j jobs.Job) []jobs.Result
	submits map[int64]int
}

func (p *provider) Submit(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	p.mu.Lock()
	p.submits[j.ID]++
	p.mu.Unlock()
	var got [][]byte
	for _, in := range j.Inputs {
		resp, err := http.Get(in.URL)
		if err != nil {
			return jobs.Submission{}, err
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return jobs.Submission{}, &jobs.Error{Class: jobs.Refused, Err: errors.New("the input link did not work")}
		}
		got = append(got, b)
	}
	p.mu.Lock()
	p.fetched[j.ID] = got
	p.mu.Unlock()
	cost := 0.02
	return jobs.Submission{Outcome: &jobs.Outcome{Results: p.results(j), CostUSD: &cost}}, nil
}

func (p *provider) Poll(context.Context, jobs.Job) (jobs.Outcome, error) {
	return jobs.Outcome{}, errors.New("no background runs")
}

func runQueue(t *testing.T, h *harness, p *provider) *jobs.Queue {
	t.Helper()
	q, err := jobs.NewQueue(h.pool, jobs.Config{
		Providers: map[string]jobs.Provider{"openrouter": p},
		Sink:      h.store,
		Inputs:    h.store,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		WaitSlot:  100 * time.Millisecond,
		FetchPoll: 50 * time.Millisecond,
		StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Stop(ctx)
	})
	return q
}

func wait(t *testing.T, q *jobs.Queue, id int64) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		j, err := q.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status.Final() {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d still %s", id, j.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// upload puts a file in without ffmpeg, as Upload would leave it.
func upload(t *testing.T, h *harness, user int64, key string, data []byte) int64 {
	t.Helper()
	h.bucket.Put(key, r2test.Object{Data: data, ContentType: "image/png"})
	var id int64
	err := h.pool.QueryRow(context.Background(), `
		INSERT INTO framy.asset (user_id, kind, object_key, content_type, bytes, sha256)
		VALUES ($1, 'upload', $2, 'image/png', $3, $4) RETURNING id`, user, key, len(data), sum(data)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAResultOutlivesItsProviderLink(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	first, second := pngBytes(t, 32, 32, 255), pngBytes(t, 16, 8, 255)
	in1 := upload(t, h, alice, "u/3001/in/one.png", first)
	in2 := upload(t, h, alice, "u/3001/in/two.png", second)

	video := []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2a fake video")
	links := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A provider that says nothing of the type.
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(video)
	}))
	made := pngBytes(t, 40, 30, 255)
	p := &provider{fetched: map[int64][][]byte{}, submits: map[int64]int{}, results: func(jobs.Job) []jobs.Result {
		return []jobs.Result{{ContentType: "image/png", Data: made}, {URL: links.URL + "/result.mp4?token=secret"}}
	}}
	q := runQueue(t, h, p)
	id, _, err := q.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: offer, Prompt: "both", Inputs: []int64{in2, in1}})
	if err != nil {
		t.Fatal(err)
	}
	j := wait(t, q, id)
	if j.Status != jobs.Done {
		t.Fatalf("%s %s %s", j.Status, j.ErrorClass, j.Error)
	}
	// The provider fetched both inputs by their presigned links, in order.
	p.mu.Lock()
	got := p.fetched[id]
	p.mu.Unlock()
	if len(got) != 2 || !bytes.Equal(got[0], second) || !bytes.Equal(got[1], first) {
		t.Errorf("the provider fetched %d inputs", len(got))
	}
	// The provider's link is gone; the results are not.
	links.Close()
	img, vid := "u/3001/out/"+strconv.FormatInt(id, 10)+"-0.png", "u/3001/out/"+strconv.FormatInt(id, 10)+"-1.mp4"
	if o, ok := h.bucket.Get(img); !ok || !bytes.Equal(o.Data, made) || o.ContentType != "image/png" {
		t.Errorf("the image result: %v %q", ok, o.ContentType)
	}
	if o, ok := h.bucket.Get(vid); !ok || !bytes.Equal(o.Data, video) || o.ContentType != "video/mp4" {
		t.Errorf("the video result: %v %q", ok, o.ContentType)
	}
	var rows int
	var w, hgt *int
	_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM framy.asset WHERE job_id=$1 AND kind='result' AND user_id=$2`, id, alice).Scan(&rows)
	_ = h.pool.QueryRow(ctx, `SELECT width, height FROM framy.asset WHERE object_key=$1`, img).Scan(&w, &hgt)
	if rows != 2 || w == nil || *w != 40 || *hgt != 30 {
		t.Errorf("%d result rows, the image %v×%v", rows, w, hgt)
	}

	// The owner's link works and downloads under a name; nobody else gets one.
	var resultID int64
	_ = h.pool.QueryRow(ctx, `SELECT id FROM framy.asset WHERE object_key=$1`, img).Scan(&resultID)
	link, err := h.store.Link(ctx, alice, resultID, "framy кот.png")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(body, made) || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="framy ___.png"`) {
		t.Errorf("the owner's link: %d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	if _, err := h.store.Link(ctx, bob, resultID, ""); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("bob's link to alice's result: %v", err)
	}

	// Keeping the results again, as after a crash, adds nothing.
	jj, _ := q.Get(ctx, id)
	if err := h.store.Keep(ctx, jj, p.results(jj)[:1]); err != nil {
		t.Fatal(err)
	}
	_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM framy.asset WHERE job_id=$1`, id).Scan(&rows)
	if rows != 2 {
		t.Errorf("%d rows after keeping again", rows)
	}

	// Deleting the result removes its objects and rows; bob cannot.
	if n, err := h.store.DeleteResults(ctx, bob, id); err != nil || n != 0 {
		t.Errorf("bob deleted %d: %v", n, err)
	}
	if err := h.store.Delete(ctx, bob, resultID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("bob's delete: %v", err)
	}
	if n, err := h.store.DeleteResults(ctx, alice, id); err != nil || n != 2 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	if _, ok := h.bucket.Get(img); ok {
		t.Error("the image is still in the bucket")
	}
	if _, ok := h.bucket.Get(vid); ok {
		t.Error("the video is still in the bucket")
	}
	_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM framy.asset WHERE job_id=$1`, id).Scan(&rows)
	if rows != 0 {
		t.Errorf("%d rows left", rows)
	}
	if resp, err := http.Get(link); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("the old link after the delete: %v %v", resp.StatusCode, err)
	}
	h.workIsEmpty(t)
}

func TestADeletedInputFailsTheJobBeforeItIsPaid(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	in := upload(t, h, alice, "u/3001/in/gone.png", pngBytes(t, 8, 8, 255))
	p := &provider{fetched: map[int64][][]byte{}, submits: map[int64]int{}, results: func(jobs.Job) []jobs.Result {
		return []jobs.Result{{ContentType: "image/png", Data: []byte("x")}}
	}}
	q, err := jobs.NewQueue(h.pool, jobs.Config{
		Providers: map[string]jobs.Provider{"openrouter": p}, Sink: h.store, Inputs: h.store,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), WaitSlot: 100 * time.Millisecond, FetchPoll: 50 * time.Millisecond, StopGrace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := q.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: offer, Prompt: "edit", Inputs: []int64{in}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Delete(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Stop(c)
	})
	j := wait(t, q, id)
	if j.Status != jobs.Failed || j.ErrorClass != jobs.Storage || j.Submits != 0 || j.SubmittedAt != nil {
		t.Errorf("%s %s, %d submits: %s", j.Status, j.ErrorClass, j.Submits, j.Error)
	}
	if !strings.Contains(j.Error, "deleted") {
		t.Errorf("the reason: %q", j.Error)
	}
	if _, ok := h.bucket.Get("u/3001/in/gone.png"); ok {
		t.Error("the deleted input is still in the bucket")
	}
}

// The chat draws a preview again from the files a draft takes, in their
// order, and reads them from the bucket.
func TestJobInputsAreTheDraftsFilesInOrder(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	second := upload(t, h, alice, "u/3001/in/b.png", pngBytes(t, 8, 8, 255))
	first := upload(t, h, alice, "u/3001/in/a.png", pngBytes(t, 8, 8, 255))
	q, err := jobs.NewQueue(h.pool, jobs.Config{Providers: map[string]jobs.Provider{"openrouter": &provider{}}, Sink: h.store, Inputs: h.store})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := q.Draft(ctx, jobs.New{UserID: alice, Surface: jobs.Group, Offer: offer, Prompt: "x", Inputs: []int64{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.store.JobInputs(ctx, id)
	if err != nil || len(got) != 2 || got[0].ID != first || got[1].ID != second {
		t.Fatalf("inputs = %+v, %v", got, err)
	}
	r, err := h.store.Open(ctx, got[0])
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if none, err := h.store.JobInputs(ctx, id+1); err != nil || len(none) != 0 {
		t.Fatalf("another job's = %+v, %v", none, err)
	}
}

func TestAJobTakesOnlyThePersonsOwnFiles(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	bobs := upload(t, h, bob, "u/3002/in/bob.png", pngBytes(t, 8, 8, 255))
	mine := upload(t, h, alice, "u/3001/in/mine.png", pngBytes(t, 8, 8, 255))
	p := &provider{fetched: map[int64][][]byte{}, submits: map[int64]int{}}
	q, err := jobs.NewQueue(h.pool, jobs.Config{Providers: map[string]jobs.Provider{"openrouter": p}, Sink: h.store, Inputs: h.store})
	if err != nil {
		t.Fatal(err)
	}
	for _, inputs := range [][]int64{{bobs}, {mine, bobs}, {mine, 999999}} {
		if _, _, err := q.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: offer, Prompt: "x", Inputs: inputs}); !errors.Is(err, jobs.ErrNoInput) {
			t.Errorf("inputs %v: %v, want ErrNoInput", inputs, err)
		}
	}
	var jobsMade int
	_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM framy.job`).Scan(&jobsMade)
	if jobsMade != 0 {
		t.Errorf("%d jobs made from someone else's file", jobsMade)
	}
}

func TestAResultThatIsNotMediaIsRefused(t *testing.T) {
	h := newHarness(t, storage.Config{MaxResult: 1024})
	ctx := context.Background()
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer big.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>an error page</html>")
	}))
	defer page.Close()
	var jobID int64
	err := h.pool.QueryRow(ctx, `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, status, finished_at)
		VALUES ($1, 'site', $2, 'test/image', 'openrouter', 'image', 'done', now()) RETURNING id`, alice, offer.ID).Scan(&jobID)
	if err != nil {
		t.Fatal(err)
	}
	j := jobs.Job{ID: jobID, UserID: alice}
	for name, r := range map[string]jobs.Result{
		"an html page":     {URL: page.URL},
		"text bytes":       {Data: []byte("plain text, not a picture")},
		"over the limit":   {URL: big.URL},
		"no bytes no link": {},
	} {
		if err := h.store.Keep(ctx, j, []jobs.Result{r}); err == nil {
			t.Errorf("%s was kept", name)
		}
	}
	if keys := h.bucket.Keys(); len(keys) != 0 {
		t.Errorf("objects kept: %v", keys)
	}
	h.workIsEmpty(t)
}

func TestTheOperatorHearsWhenTheBucketPassesAStep(t *testing.T) {
	h := newHarness(t, storage.Config{AlertStep: 1000})
	ctx := context.Background()
	var jobID int64
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, status, finished_at)
		VALUES ($1, 'site', $2, 'test/image', 'openrouter', 'image', 'done', now()) RETURNING id`, alice, offer.ID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	// keep keeps n results of about 370 bytes each; keeping them again
	// writes the same objects and adds nothing.
	keep := func(n int) {
		t.Helper()
		data := append(pngBytes(t, 1, 1, 255), make([]byte, 300)...)
		j := jobs.Job{ID: jobID, UserID: alice}
		results := make([]jobs.Result, n)
		for k := range results {
			results[k] = jobs.Result{ContentType: "image/png", Data: data}
		}
		if err := h.store.Keep(ctx, j, results); err != nil {
			t.Fatal(err)
		}
	}
	told := func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.told)
	}
	keep(1)
	if told() != 0 {
		t.Fatal("told below the step")
	}
	keep(3) // past 1000 bytes
	if told() != 1 || !strings.Contains(h.told[0], "R2 bucket") {
		t.Fatalf("told %d times: %v", told(), h.told)
	}
	keep(3) // the same results again: no new step
	if told() != 1 {
		t.Errorf("told %d times for the same step", told())
	}
	// A failed message is tried again by the next write.
	h.mu.Lock()
	h.notify = func(context.Context, string) error { return errors.New("telegram is down") }
	h.mu.Unlock()
	keep(6) // past 2000
	h.mu.Lock()
	h.notify = func(context.Context, string) error { return nil }
	h.mu.Unlock()
	keep(6)
	if told() != 3 {
		t.Errorf("told %d times, want the failed message and its retry", told())
	}
	var step int
	_ = h.pool.QueryRow(ctx, `SELECT bucket_alert_step FROM framy.runtime_state`).Scan(&step)
	if step != 2 {
		t.Errorf("step %d", step)
	}
}

func TestAWorkingFileGoesWhenItsWorkFails(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	h.bucket.FailNext(500, 500, 500)
	var jobID int64
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, status, finished_at)
		VALUES ($1, 'site', $2, 'test/image', 'openrouter', 'image', 'done', now()) RETURNING id`, alice, offer.ID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	links := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t, 4, 4, 255))
	}))
	defer links.Close()
	if err := h.store.Keep(ctx, jobs.Job{ID: jobID, UserID: alice}, []jobs.Result{{URL: links.URL}}); err == nil {
		t.Fatal("kept while the bucket failed")
	}
	h.workIsEmpty(t)
}

// aged makes an upload look as old as age.
func aged(t *testing.T, h *harness, id int64, age time.Duration) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE framy.asset SET created_at = now() - make_interval(secs => $2) WHERE id=$1`, id, age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// takenBy makes a job of status that lists the files, as the queue would.
func takenBy(t *testing.T, h *harness, status string, ids ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	var job int64
	err := h.pool.QueryRow(ctx, `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, prompt, status, finished_at)
		VALUES ($1, 'site', 'openrouter:test/image', 'test/image', 'openrouter', 'image', 'x', $2,
		        CASE WHEN $2 IN ('done', 'failed', 'cancelled', 'expired') THEN now() END)
		RETURNING id`, alice, status).Scan(&job)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if _, err := h.pool.Exec(ctx, `INSERT INTO framy.job_input (job_id, position, asset_id) VALUES ($1, $2, $3)`, job, i, id); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

func (h *harness) assetExists(t *testing.T, id int64) bool {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM framy.asset WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestAnUploadNoJobTookGoesAfterAnHour(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	img := pngBytes(t, 8, 8, 255)
	left := upload(t, h, alice, "u/3001/in/left.png", img)
	fresh := upload(t, h, alice, "u/3001/in/fresh.png", img)
	taken := upload(t, h, alice, "u/3001/in/taken.png", img)
	declined := upload(t, h, bob, "u/3002/in/declined.png", img)
	aged(t, h, left, 2*time.Hour)
	aged(t, h, fresh, 50*time.Minute)
	aged(t, h, taken, 2*time.Hour)
	aged(t, h, declined, 2*time.Hour)
	takenBy(t, h, "queued", taken)
	// A draft someone said No to still took its file: it stays until the
	// person deletes it, as every file does.
	takenBy(t, h, "cancelled", declined)

	n, err := h.store.SweepUploads(ctx, storage.UploadTTL)
	if err != nil || n != 1 {
		t.Fatalf("swept %d, %v; want 1", n, err)
	}
	if h.assetExists(t, left) {
		t.Error("the row of the upload no job took is still there")
	}
	if _, ok := h.bucket.Get("u/3001/in/left.png"); ok {
		t.Error("the object of the upload no job took is still there")
	}
	for _, id := range []int64{fresh, taken, declined} {
		if !h.assetExists(t, id) {
			t.Errorf("asset %d went", id)
		}
	}
	for _, key := range []string{"u/3001/in/fresh.png", "u/3001/in/taken.png", "u/3002/in/declined.png"} {
		if _, ok := h.bucket.Get(key); !ok {
			t.Errorf("%s went", key)
		}
	}
	if n, err := h.store.SweepUploads(ctx, storage.UploadTTL); err != nil || n != 0 {
		t.Fatalf("a second sweep: %d, %v", n, err)
	}
}

func TestTheSweepPassesByAnUploadAJobIsTaking(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	id := upload(t, h, alice, "u/3001/in/now.png", pngBytes(t, 8, 8, 255))
	aged(t, h, id, 2*time.Hour)

	// A job inserting its job_input holds this lock on the file until it
	// commits; nothing is committed yet, so the sweep cannot see the job.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM framy.asset WHERE id=$1 FOR KEY SHARE`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := h.store.SweepUploads(ctx, storage.UploadTTL); err != nil || n != 0 {
		t.Fatalf("swept %d, %v while a job held the file", n, err)
	}
	if _, ok := h.bucket.Get("u/3001/in/now.png"); !ok {
		t.Fatal("the object went while a job held the file")
	}
	// The job did not happen after all.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := h.store.SweepUploads(ctx, storage.UploadTTL); err != nil || n != 1 {
		t.Fatalf("swept %d, %v after the job let go", n, err)
	}
}

func TestAJobCannotTakeAnUploadTheSweepIsDeleting(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	id := upload(t, h, alice, "u/3001/in/late.png", pngBytes(t, 8, 8, 255))
	aged(t, h, id, 2*time.Hour)
	q, err := jobs.NewQueue(h.pool, jobs.Config{Providers: map[string]jobs.Provider{"openrouter": &provider{}}, Sink: h.store, Inputs: h.store})
	if err != nil {
		t.Fatal(err)
	}

	// The sweep's lock, held while the object goes.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM framy.asset WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	enqueued := make(chan error, 1)
	go func() {
		_, _, err := q.Enqueue(ctx, jobs.New{UserID: alice, Surface: jobs.Site, Offer: offer, Prompt: "x", Inputs: []int64{id}})
		enqueued <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if _, err := tx.Exec(ctx, `DELETE FROM framy.asset WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-enqueued:
		if !errors.Is(err, jobs.ErrNoInput) {
			t.Fatalf("enqueue: %v, want ErrNoInput", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the enqueue never ended")
	}
	var made int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM framy.job`).Scan(&made); err != nil || made != 0 {
		t.Fatalf("%d jobs made, %v", made, err)
	}
}

func TestAnObjectThatWouldNotGoKeepsItsRow(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	id := upload(t, h, alice, "u/3001/in/stuck.png", pngBytes(t, 8, 8, 255))
	aged(t, h, id, 2*time.Hour)
	h.bucket.FailNext(403)
	if n, err := h.store.SweepUploads(ctx, storage.UploadTTL); err == nil || n != 0 {
		t.Fatalf("swept %d, %v; want an error", n, err)
	}
	if !h.assetExists(t, id) {
		t.Fatal("the row went before its object")
	}
	if n, err := h.store.SweepUploads(ctx, storage.UploadTTL); err != nil || n != 1 {
		t.Fatalf("the next sweep: %d, %v", n, err)
	}
}

func TestATestBotWritesUnderItsPrefix(t *testing.T) {
	h := newHarness(t, storage.Config{Prefix: "test/", Media: converter(t)})
	ctx := context.Background()
	a, err := h.store.Upload(ctx, alice, bytes.NewReader(pngBytes(t, 8, 8, 255)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.Key, "test/u/3001/in/") {
		t.Fatalf("key = %q", a.Key)
	}
	if _, ok := h.bucket.Get(a.Key); !ok {
		t.Fatal("no object under the prefixed key")
	}
	for _, key := range h.bucket.Keys() {
		if !strings.HasPrefix(key, "test/") {
			t.Errorf("%q is outside the prefix", key)
		}
	}
}

// resultOf gives job a result file of user, as Keep would.
func resultOf(t *testing.T, h *harness, user, job int64, key string) int64 {
	t.Helper()
	h.bucket.Put(key, r2test.Object{Data: []byte("png"), ContentType: "image/png"})
	var id int64
	err := h.pool.QueryRow(context.Background(), `
		INSERT INTO framy.asset (user_id, kind, job_id, object_key, content_type, bytes, sha256)
		VALUES ($1, 'result', $2, $3, 'image/png', 3, 'x') RETURNING id`, user, job, key).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestADeletedJobTakesItsResultsAndTheUploadsOnlyItTook(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	img := pngBytes(t, 8, 8, 255)
	own := upload(t, h, alice, "u/3001/in/own.png", img)
	shared := upload(t, h, alice, "u/3001/in/shared.png", img)
	job := takenBy(t, h, "done", own, shared)
	other := takenBy(t, h, "done", shared)
	res := resultOf(t, h, alice, job, "u/3001/out/1-0.png")
	kept := resultOf(t, h, alice, other, "u/3001/out/2-0.png")

	n, err := h.store.DeleteJobFiles(ctx, alice, job)
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, %v; want the result and the own upload", n, err)
	}
	for _, id := range []int64{own, res} {
		if h.assetExists(t, id) {
			t.Errorf("asset %d is still there", id)
		}
	}
	for _, key := range []string{"u/3001/in/own.png", "u/3001/out/1-0.png"} {
		if _, ok := h.bucket.Get(key); ok {
			t.Errorf("%s is still in the bucket", key)
		}
	}
	// The other job keeps the upload they share, and its own result.
	for _, id := range []int64{shared, kept} {
		if !h.assetExists(t, id) {
			t.Errorf("asset %d went", id)
		}
	}
	// Bob cannot delete alice's files through her job's id.
	if n, err := h.store.DeleteJobFiles(ctx, bob, other); err != nil || n != 0 {
		t.Errorf("bob deleted %d, %v", n, err)
	}
}

func TestDeletingEveryFileOfAPerson(t *testing.T) {
	h := newHarness(t, storage.Config{})
	ctx := context.Background()
	img := pngBytes(t, 8, 8, 255)
	var keys []string
	for i := range 300 {
		key := fmt.Sprintf("u/3001/in/%d.png", i)
		upload(t, h, alice, key, img)
		keys = append(keys, key)
	}
	job := takenBy(t, h, "done")
	resultOf(t, h, alice, job, "u/3001/out/9-0.png")
	bobs := upload(t, h, bob, "u/3002/in/bob.png", img)
	late := upload(t, h, alice, "u/3001/in/late.png", img)
	if _, err := h.pool.Exec(ctx, `UPDATE framy.asset SET created_at = now() + interval '1 hour' WHERE id = $1`, late); err != nil {
		t.Fatal(err)
	}

	n, err := h.store.DeleteAll(ctx, alice, time.Now())
	if err != nil || n != 301 {
		t.Fatalf("deleted %d, %v; want 301", n, err)
	}
	for _, key := range append(keys, "u/3001/out/9-0.png") {
		if _, ok := h.bucket.Get(key); ok {
			t.Fatalf("%s is still in the bucket", key)
		}
	}
	// Bob's file stays, and so does one kept after the deletion began.
	if !h.assetExists(t, bobs) || !h.assetExists(t, late) {
		t.Error("a file that was not to go went")
	}
	if n, err := h.store.DeleteAll(ctx, alice, time.Now()); err != nil || n != 0 {
		t.Errorf("a second pass deleted %d, %v", n, err)
	}
}

func TestLinksOfAPageComeInOneRead(t *testing.T) {
	h := newHarness(t, storage.Config{})
	img := pngBytes(t, 8, 8, 255)
	a := upload(t, h, alice, "u/3001/in/a.png", img)
	b := upload(t, h, alice, "u/3001/in/b.png", img)
	bobs := upload(t, h, bob, "u/3002/in/c.png", img)
	links, err := h.store.LinkAll(context.Background(), alice, []int64{a, b, bobs})
	if err != nil || len(links) != 2 || links[a] == "" || links[b] == "" || links[bobs] != "" {
		t.Fatalf("%v, %v", links, err)
	}
}
