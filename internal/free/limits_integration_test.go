// SPDX-License-Identifier: Apache-2.0
//go:build integration

package free

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

func TestTheSixthImageInADayIsRefusedWithTheTimeItFrees(t *testing.T) {
	h := newHarness(t, 100)
	refused := metric("framy_free_refused_total", "reason", "image_limit")
	// Five images queued 23, 20, 10, 2 and 1 hours ago.
	var oldest, second time.Time
	for i, ago := range []time.Duration{23 * time.Hour, 20 * time.Hour, 10 * time.Hour, 2 * time.Hour, time.Hour} {
		_, at := h.insert(row{user: alice, plan: ImageMAI, ago: ago})
		switch i {
		case 0:
			oldest = at
		case 1:
			second = at
		}
	}

	_, created, err := h.enqueue(alice, ImageMAI, 0.0195)
	limit := asLimit(t, err)
	if created || limit.Kind != catalog.Image || limit.Limit != 5 || limit.Window != 24*time.Hour || !limit.FreesAt.Equal(oldest.Add(24*time.Hour)) {
		t.Fatalf("created %v, %+v; want the sixth image refused until the oldest leaves the window at %s", created, limit, oldest.Add(24*time.Hour))
	}
	if n := count(t, h.pool, `SELECT count(*) FROM job WHERE user_id = $1`, alice); n != 5 {
		t.Fatalf("%d jobs after the refusal, want the 5 there were", n)
	}
	if n := count(t, h.pool, `SELECT count(*) FROM river_job`); n != 0 {
		t.Fatalf("%d River jobs; a refused run is not worked", n)
	}
	if got := metric("framy_free_refused_total", "reason", "image_limit"); got != refused+1 {
		t.Fatalf("image_limit refusals = %d, want %d", got, refused+1)
	}

	// A minute before the oldest leaves it is still refused; a minute after, the
	// sixth runs, and the seventh waits for the next one to leave.
	h.advance(time.Hour - time.Minute)
	asLimit(t, h.requireRefused(alice))
	h.advance(2 * time.Minute)
	h.mustEnqueue(alice, ImageMAI, 0.0195)
	limit = asLimit(t, h.requireRefused(alice))
	if !limit.FreesAt.Equal(second.Add(24 * time.Hour)) {
		t.Fatalf("frees at %s, want when the second image leaves: %s", limit.FreesAt, second.Add(24*time.Hour))
	}

	// Someone else's allowance is their own.
	h.mustEnqueue(bob, ImageMAI, 0.0195)
}

// requireRefused enqueues a free image and returns the error, which must be
// one.
func (h *harness) requireRefused(user int64) error {
	h.t.Helper()
	_, created, err := h.enqueue(user, ImageMAI, 0.0195)
	if err == nil || created {
		h.t.Fatalf("a run was allowed: created %v, %v", created, err)
	}
	return err
}

func TestImagesAndVideosAreCountedApartAndEverySurfaceTogether(t *testing.T) {
	h := newHarness(t, 100)
	// Four images from four surfaces; a fifth from the site is the last.
	for _, surface := range []string{"site", "mini_app", "group", "dm"} {
		h.insert(row{user: alice, plan: ImageMAI, surface: surface, ago: time.Hour})
	}
	// Images from yesterday's morning and last week no longer count.
	h.insert(row{user: alice, plan: ImageMAI, ago: 25 * time.Hour})
	h.insert(row{user: alice, plan: ImageKlein, ago: 6 * 24 * time.Hour})
	h.mustEnqueue(alice, ImageKlein, 0.009)
	asLimit(t, h.requireRefused(alice))

	// The images being used up leaves the video alone.
	h.mustEnqueue(alice, VideoHeyGen, 0.18)
	_, _, err := h.enqueue(alice, VideoGrok, 0.15)
	limit := asLimit(t, err)
	if limit.Kind != catalog.Video || limit.Limit != 1 || limit.Window != 7*24*time.Hour {
		t.Fatalf("limit = %+v; the video is one in 7 days, whichever video model", limit)
	}
	var queuedAt time.Time
	if err := h.pool.QueryRow(context.Background(), `SELECT queued_at FROM job WHERE user_id = $1 AND kind = 'video'`, alice).Scan(&queuedAt); err != nil {
		t.Fatal(err)
	}
	if !limit.FreesAt.Equal(queuedAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("frees at %s, want a week after the video: %s", limit.FreesAt, queuedAt.Add(7*24*time.Hour))
	}

	// A video from 6 days ago still counts and one from 8 days ago does not.
	h.insert(row{user: bob, plan: VideoHeyGen, ago: 8 * 24 * time.Hour})
	h.mustEnqueue(bob, VideoHeyGen, 0.18)
	h.insert(row{user: carol, plan: VideoHeyGen, ago: 6 * 24 * time.Hour})
	_, _, err = h.enqueue(carol, VideoHeyGen, 0.18)
	asLimit(t, err)
}

// Only a run that took something from the person takes a slot of theirs.
func TestOnlyRunsThatTookSomethingTakeASlot(t *testing.T) {
	cases := []struct {
		name          string
		status, class string
		counts        bool
	}{
		{"waiting", "queued", "", true},
		{"running", "running", "", true},
		{"done", "done", "", true},
		{"refused for its content", "failed", "moderation", true},
		{"failed at the provider", "failed", "provider", false},
		{"failed for want of money", "failed", "money", false},
		{"failed by a timeout", "failed", "timeout", false},
		{"failed on a setting the provider refused", "failed", "refused", false},
		{"not sent, as the adapter could tell it would fail", "failed", "invalid_request", false},
		{"failed on framy's key", "failed", "key", false},
		{"failed in storage", "failed", "storage", false},
		{"failed inside framy", "failed", "internal", false},
		{"interrupted", "failed", "interrupted", false},
		{"cancelled", "cancelled", "", false},
		{"expired", "expired", "", false},
		{"a draft waiting for Generate", "draft", "", false},
	}
	h := newHarness(t, 100)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			user := alice + int64(i)
			// Four runs that count, and the one under test.
			h.insertN(4, row{user: user, plan: ImageMAI, ago: 2 * time.Hour})
			h.insert(row{user: user, plan: ImageMAI, status: c.status, class: c.class, ago: 2 * time.Hour})
			_, _, err := h.enqueue(user, ImageMAI, 0.0195)
			if c.counts {
				asLimit(t, err)
			} else if err != nil {
				t.Fatalf("refused: %v; a %s run takes nothing from the person", err, c.name)
			}
		})
	}
}

// Two requests at the same moment must not both take the last slot. The
// pause keeps every request inside the allowance's check long enough that,
// without the person's lock, they would all see the same count.
func TestTwoRequestsAtOnceDoNotTakeTheLastSlot(t *testing.T) {
	h := newHarness(t, 100)
	h.a.pause = func() { time.Sleep(15 * time.Millisecond) }
	h.insertN(3, row{user: alice, plan: ImageMAI, ago: time.Hour})

	const requests = 12
	errs := make([]error, requests)
	created := make([]bool, requests)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range requests {
		wg.Go(func() {
			<-start
			_, created[i], errs[i] = h.enqueue(alice, ImageMAI, 0.0195)
		})
	}
	close(start)
	wg.Wait()

	won := 0
	for i := range requests {
		if created[i] {
			won++
		} else {
			asLimit(t, errs[i])
		}
	}
	if won != 2 {
		t.Fatalf("%d requests took a slot, want the 2 that were left", won)
	}
	if n := count(t, h.pool, `SELECT count(*) FROM job WHERE user_id = $1 AND free`, alice); n != 5 {
		t.Fatalf("%d runs on the books, want 5", n)
	}
}

func TestARepeatedRequestIsTheSameRequestNotASecondRun(t *testing.T) {
	h := newHarness(t, 100)
	h.insertN(4, row{user: alice, plan: ImageMAI, ago: time.Hour})
	n := newJob(alice, ImageMAI, 0.0195)
	n.DedupeKey = jobs.TelegramUpdate(42)
	first, created, err := h.q.Enqueue(context.Background(), n)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	// The update arrives again with the allowance used up: it is the job it
	// started, not a sixth run.
	again, created, err := h.q.Enqueue(context.Background(), n)
	if err != nil || created || again != first {
		t.Fatalf("again = %d, created %v, %v; want job %d back", again, created, err, first)
	}
	// A different update is a different run.
	n.DedupeKey = jobs.TelegramUpdate(43)
	if _, _, err := h.q.Enqueue(context.Background(), n); err == nil {
		t.Fatal("another update took the sixth slot")
	}

	// The same update twice at the same moment, one slot left: one job.
	h2 := newHarness(t, 100)
	h2.a.pause = func() { time.Sleep(15 * time.Millisecond) }
	h2.insertN(4, row{user: bob, plan: ImageMAI, ago: time.Hour})
	n = newJob(bob, ImageMAI, 0.0195)
	n.DedupeKey = jobs.TelegramUpdate(44)
	ids := make([]int64, 2)
	made := make([]bool, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { ids[i], made[i], errs[i] = h2.q.Enqueue(context.Background(), n) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || ids[0] != ids[1] || made[0] == made[1] {
		t.Fatalf("ids %v, created %v, errors %v; want one job, made once, and no refusal", ids, made, errs)
	}
}

func TestADraftTakesNoSlotUntilItIsGenerated(t *testing.T) {
	h := newHarness(t, 100)
	ctx := context.Background()
	h.insertN(4, row{user: alice, plan: ImageMAI, ago: time.Hour})

	// Two previews are fine; each is a promise of nothing.
	first, _, err := h.q.Draft(ctx, newJob(alice, ImageMAI, 0.0195))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := h.q.Draft(ctx, newJob(alice, ImageMAI, 0.0195))
	if err != nil {
		t.Fatal(err)
	}
	if u, err := h.a.Usage(ctx, alice); err != nil || u.Images.Used != 4 {
		t.Fatalf("usage = %+v, %v; drafts take no slot", u, err)
	}
	if err := h.q.Generate(ctx, first); err != nil {
		t.Fatal(err)
	}
	// The slot is gone when the second is pressed.
	err = h.q.Generate(ctx, second)
	asLimit(t, err)
	j, err := h.q.Get(ctx, second)
	if err != nil || j.Status != jobs.Draft {
		t.Fatalf("the refused draft = %s, %v; it stays a draft", j.Status, err)
	}
	if n := count(t, h.pool, `SELECT count(*) FROM river_job`); n != 1 {
		t.Fatalf("%d River jobs, want 1", n)
	}
	// No preview is offered for a run that could not start.
	if _, _, err := h.q.Draft(ctx, newJob(alice, ImageMAI, 0.0195)); err == nil {
		t.Fatal("a draft was made at the limit")
	}
	if err := h.q.Generate(ctx, first); !errors.Is(err, jobs.ErrNotDraft) {
		t.Fatalf("a second press = %v, want ErrNotDraft and not a refusal", err)
	}
}

// A draft is read by Generate from the table: its settings and its files
// must come out as they went in.
func TestAQueuedDraftIsCheckedAsItWasStored(t *testing.T) {
	h := newHarness(t, 100)
	ctx := context.Background()
	// A queue with no allowance lets a draft of six photos on MAI be made.
	open, err := jobs.NewQueue(h.pool, jobs.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var assets []int64
	for i := range 6 {
		var id int64
		err := h.pool.QueryRow(ctx, `
			INSERT INTO framy.asset (user_id, kind, object_key, content_type, bytes, sha256)
			VALUES ($1, 'upload', $2, 'image/jpeg', 10, 'x') RETURNING id`, carol, "u/carol/"+string(rune('a'+i))).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, id)
	}
	n := newJob(carol, ImageMAI, 0.0195)
	n.Inputs = assets
	id, _, err := open.Draft(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	err = h.q.Generate(ctx, id)
	if reason := asNotFree(t, err).Reason; reason == "" {
		t.Fatal("no reason")
	}
	if j, _ := h.q.Get(ctx, id); j.Status != jobs.Draft {
		t.Fatalf("status = %s", j.Status)
	}
	// With five photos it is a run like any other.
	n.Inputs = assets[:5]
	id, _, err = open.Draft(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.q.Generate(ctx, id); err != nil {
		t.Fatal(err)
	}
	// And a draft someone stored with a setting the allowance locks is not
	// queued either.
	n.Inputs = nil
	n.Settings = map[string]any{"n": 2}
	id, _, err = open.Draft(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	asNotFree(t, h.q.Generate(ctx, id))
}

func TestARunOnThePersonsOwnKeyIsNotTheAllowances(t *testing.T) {
	h := newHarness(t, 0) // the tier is closed
	h.insertN(5, row{user: alice, plan: ImageMAI, ago: time.Hour})
	n := newJob(alice, ImageMAI, 0.0195)
	n.Free = false
	// Any model, any setting, past every limit.
	n.Offer = catalog.Offer{ID: "openrouter:test/image", ModelID: "test/image", Provider: catalog.OpenRouter, ProviderModelID: "test/image", Kind: catalog.Image}
	n.Settings = map[string]any{"n": 3, "resolution": "4K"}
	if _, created, err := h.q.Enqueue(context.Background(), n); err != nil || !created {
		t.Fatalf("created %v, %v; the allowance is not asked about a run on the person's key", created, err)
	}
	if n := count(t, h.pool, `SELECT count(*) FROM job WHERE user_id = $1 AND free`, alice); n != 5 {
		t.Fatalf("%d free runs, want the 5 there were", n)
	}
}

func TestAFreeRunOffTheAllowanceCreatesNothing(t *testing.T) {
	h := newHarness(t, 100)
	before := metric("framy_free_refused_total", "reason", "not_free")
	other := newJob(alice, ImageMAI, 0.02)
	other.Offer = catalog.Offer{ID: "openrouter:test/image", ModelID: "test/image", Provider: catalog.OpenRouter, ProviderModelID: "test/image", Kind: catalog.Image}
	locked := newJob(alice, ImageKlein, 0.009)
	locked.Settings = map[string]any{"n": 1, "resolution": "4K"}
	noPrice := newJob(alice, ImageMAI, 0.0195)
	noPrice.Estimate = nil
	for name, n := range map[string]jobs.New{"another model": other, "another resolution": locked, "no estimate": noPrice} {
		t.Run(name, func(t *testing.T) {
			_, created, err := h.q.Enqueue(context.Background(), n)
			asNotFree(t, err)
			if created {
				t.Fatal("created")
			}
			if _, _, err := h.q.Draft(context.Background(), n); err == nil {
				t.Fatal("drafted")
			}
		})
	}
	if n := count(t, h.pool, `SELECT count(*) FROM job`); n != 0 {
		t.Fatalf("%d jobs", n)
	}
	if got := metric("framy_free_refused_total", "reason", "not_free"); got != before+6 {
		t.Fatalf("not_free refusals = %d, want %d", got, before+6)
	}
}

func TestUsageAndCheckSayWhatAdmitWouldWithoutTakingASlot(t *testing.T) {
	h := newHarness(t, 100)
	ctx := context.Background()
	h.insertN(3, row{user: alice, plan: ImageMAI, ago: time.Hour})
	_, firstAt := h.insert(row{user: alice, plan: ImageMAI, ago: 5 * time.Hour})

	u, err := h.a.Usage(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if u.Images.Used != 4 || u.Images.Left() != 1 || u.Images.Limit != 5 || u.Images.Window != 24*time.Hour || !u.Images.FreesAt.IsZero() ||
		u.Videos.Used != 0 || u.Videos.Left() != 1 || u.Videos.Window != 7*24*time.Hour {
		t.Fatalf("usage = %+v", u)
	}
	adm := jobs.Admission{UserID: alice, Offer: ImageMAI.Offer, Kind: catalog.Image, Settings: ImageMAI.Settings(), EstimateUSD: 0.0195}
	for range 3 {
		// Asking does not use the slot up.
		if err := h.a.Check(ctx, adm); err != nil {
			t.Fatal(err)
		}
	}
	h.mustEnqueue(alice, ImageMAI, 0.0195)
	u, _ = h.a.Usage(ctx, alice)
	if u.Images.Left() != 0 || !u.Images.FreesAt.Equal(firstAt.Add(24*time.Hour)) {
		t.Fatalf("usage = %+v; the oldest image frees the slot at %s", u.Images, firstAt.Add(24*time.Hour))
	}
	refused := metric("framy_free_refused_total", "reason", "image_limit")
	limit := asLimit(t, h.a.Check(ctx, adm))
	if !limit.FreesAt.Equal(firstAt.Add(24 * time.Hour)) {
		t.Fatalf("frees at %s", limit.FreesAt)
	}
	if metric("framy_free_refused_total", "reason", "image_limit") != refused {
		t.Fatal("a look at the allowance counted as a refusal")
	}
}

func TestTheFreeEditTakesAClipOfUpToFiveSeconds(t *testing.T) {
	h := newHarness(t, 100)
	ctx := context.Background()
	file := func(contentType string, seconds any) int64 {
		var id int64
		err := h.pool.QueryRow(ctx, `
			INSERT INTO framy.asset (user_id, kind, object_key, content_type, bytes, sha256, seconds)
			VALUES ($1, 'upload', 'u/1001/in/' || gen_random_uuid(), $2, 10, 'x', $3) RETURNING id`, alice, contentType, seconds).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	long, cut, photo, unknown := file("video/mp4", 6.0), file("video/mp4", 5.033), file("image/jpeg", nil), file("video/mp4", nil)
	edit := func(inputs ...int64) jobs.New {
		n := newJob(alice, VideoEdit, 0.15)
		n.Inputs = inputs
		return n
	}
	for name, tc := range map[string]struct {
		inputs []int64
		want   string
	}{
		"a clip of 6 s":            {[]int64{long}, "pick 5 s of it"},
		"a photo":                  {[]int64{photo}, "not a picture"},
		"a clip of unknown length": {[]int64{unknown}, "not known"},
	} {
		_, created, err := h.q.Enqueue(ctx, edit(tc.inputs...))
		if e := asNotFree(t, err); !strings.Contains(e.Reason, tc.want) || created {
			t.Errorf("%s: %q, created %v", name, e.Reason, created)
		}
	}
	// A draft is checked as it was stored, and a revision to the free edit
	// as a new run.
	open, err := jobs.NewQueue(h.pool, jobs.Config{})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := open.Draft(ctx, edit(long))
	if err != nil {
		t.Fatal(err)
	}
	asNotFree(t, h.q.Generate(ctx, id))
	paid := edit(long)
	paid.Free = false
	id, _, err = open.Draft(ctx, paid)
	if err != nil {
		t.Fatal(err)
	}
	asNotFree(t, h.q.Revise(ctx, id, edit(long)))
	// A revision that takes another part of the clip is asked about that
	// part: the first 5 s go free in place of the whole clip, and the whole
	// clip back on the allowance is refused, the files left as they were.
	if err := h.q.Revise(ctx, id, edit(cut)); err != nil {
		t.Fatalf("the first 5 s in place of the clip: %v", err)
	}
	asNotFree(t, h.q.Revise(ctx, id, edit(long)))
	var taken int64
	if err := h.pool.QueryRow(ctx, `SELECT asset_id FROM framy.job_input WHERE job_id=$1`, id).Scan(&taken); err != nil || taken != cut {
		t.Fatalf("the draft takes %d (%v), want the 5 s %d", taken, err, cut)
	}
	// 5 s cut from a longer clip comes out a frame over, and goes.
	if _, created, err := h.q.Enqueue(ctx, edit(cut)); err != nil || !created {
		t.Fatalf("a part of 5 s: %v, created %v", err, created)
	}
}
