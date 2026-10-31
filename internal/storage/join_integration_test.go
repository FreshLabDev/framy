// SPDX-License-Identifier: Apache-2.0
//go:build integration

package storage_test

import (
	"context"
	"math"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// videoJob is a done video job of user's that takes the files ids, in order.
func videoJob(t *testing.T, h *harness, user int64, ids ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	err := h.pool.QueryRow(ctx, `
		INSERT INTO framy.job (user_id, surface, offer_id, model_id, provider, kind, status, finished_at)
		VALUES ($1, 'site', $2, 'test/image', 'openrouter', 'video', 'done', now()) RETURNING id`, user, offer.ID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	for i, asset := range ids {
		if _, err := h.pool.Exec(ctx, `INSERT INTO framy.job_input (job_id, position, asset_id) VALUES ($1, $2, $3)`, id, i, asset); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

type joined struct {
	id            int64
	key           string
	seconds       float64
	width, height int
	audio         bool
	thumb         *string
	contentType   string
	segments      []float64
}

func joinedOf(t *testing.T, h *harness, job int64) (joined, bool) {
	t.Helper()
	var j joined
	err := h.pool.QueryRow(context.Background(), `
		SELECT id, object_key, seconds, width, height, audio, thumb_key, content_type, segments
		FROM framy.asset WHERE job_id=$1 AND kind='joined'`, job).Scan(
		&j.id, &j.key, &j.seconds, &j.width, &j.height, &j.audio, &j.thumb, &j.contentType, &j.segments)
	if err != nil {
		return joined{}, false
	}
	return j, true
}

// Continue from a clip's last frame: the run's video is kept as it came,
// and the clip with the video after it is kept beside it.
func TestAContinuationIsJoinedToItsClip(t *testing.T) {
	c := videoConverter(t)
	h := newHarness(t, storage.Config{Media: c})
	ctx := context.Background()
	first := videoJob(t, h, alice)
	if err := h.store.Keep(ctx, jobs.Job{ID: first, UserID: alice}, []jobs.Result{{ContentType: "video/mp4", Data: clip(t, "3")}}); err != nil {
		t.Fatal(err)
	}
	results, err := h.store.Results(ctx, first)
	if err != nil || len(results) != 1 {
		t.Fatalf("%v %v", results, err)
	}
	if _, ok := joinedOf(t, h, first); ok {
		t.Fatal("a run from no frame is joined")
	}
	if _, ok, err := h.store.Joined(ctx, first); err != nil || ok {
		t.Fatalf("Joined of a run from no frame: %v %v", ok, err)
	}
	frame, err := h.store.LastFrame(ctx, alice, results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var of int64
	var at float64
	if err := h.pool.QueryRow(ctx, `SELECT frame_of, frame_at FROM framy.asset WHERE id=$1`, frame.ID).Scan(&of, &at); err != nil {
		t.Fatal(err)
	}
	if of != results[0].ID || math.Abs(at-3) > 0.1 {
		t.Errorf("the frame names clip %d at %.3f s", of, at)
	}

	next := videoJob(t, h, alice, frame.ID)
	run := jobs.Job{ID: next, UserID: alice, Settings: map[string]any{"frame_images": []any{"first_frame"}}}
	if err := h.store.Keep(ctx, run, []jobs.Result{{ContentType: "video/mp4", Data: clip(t, "2.5")}}); err != nil {
		t.Fatal(err)
	}
	// A length in fractions of a second is kept as it is, not in whole seconds.
	part, err := h.store.Results(ctx, next)
	if err != nil || len(part) != 1 || math.Abs(part[0].Seconds-2.5) > 0.05 {
		t.Fatalf("the run's own video %+v %v", part, err)
	}
	j, ok := joinedOf(t, h, next)
	if !ok {
		t.Fatal("no joined clip")
	}
	if want := 3 + 2.5 - 1.0/30; math.Abs(j.seconds-want) > 0.05 || j.width != 320 || j.height != 180 || !j.audio || j.contentType != "video/mp4" {
		t.Errorf("the joined clip %+v, want %.3f s", j, want)
	}
	if _, ok := h.bucket.Get(j.key); !ok {
		t.Errorf("no %s in the bucket", j.key)
	}
	if j.thumb == nil && c.CheckThumb(ctx) == nil {
		t.Error("the joined clip has no thumbnail")
	}
	if len(j.segments) != 2 || j.segments[0] != 0 || math.Abs(j.segments[1]-3) > 0.05 {
		t.Errorf("the parts begin at %v, want 0 and 3", j.segments)
	}
	// The DM gets the joined clip: Joined reads it as the chat sends it.
	if a, ok, err := h.store.Joined(ctx, next); err != nil || !ok || a.Key != j.key || a.Kind != "joined" || a.JobID != next ||
		a.UserID != alice || a.ContentType != "video/mp4" || math.Abs(a.Seconds-j.seconds) > 0.001 {
		t.Fatalf("Joined = %+v %v %v", a, ok, err)
	}
	// Kept again, as after a crash: the same row.
	if err := h.store.Keep(ctx, run, []jobs.Result{{ContentType: "video/mp4", Data: clip(t, "2.5")}}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM framy.asset WHERE job_id=$1 AND kind='joined'`, next).Scan(&n)
	if n != 1 {
		t.Errorf("%d joined clips", n)
	}

	// Continue again from the joined clip: the next run joins to all of it,
	// a third part.
	again, err := h.store.LastFrame(ctx, alice, j.id)
	if err != nil {
		t.Fatal(err)
	}
	third := videoJob(t, h, alice, again.ID)
	if err := h.store.Keep(ctx, jobs.Job{ID: third, UserID: alice, Settings: run.Settings},
		[]jobs.Result{{ContentType: "video/mp4", Data: clip(t, "1.5")}}); err != nil {
		t.Fatal(err)
	}
	all, ok := joinedOf(t, h, third)
	if !ok {
		t.Fatal("the second continuation is not joined")
	}
	if want := j.seconds + 1.5 - 1.0/30; math.Abs(all.seconds-want) > 0.05 {
		t.Errorf("the clip of three parts is %.3f s, not %.3f", all.seconds, want)
	}
	if len(all.segments) != 3 || all.segments[1] != j.segments[1] || math.Abs(all.segments[2]-j.seconds) > 0.05 {
		t.Errorf("the parts begin at %v, after %v and %.3f s", all.segments, j.segments, j.seconds)
	}

	// The frame as a reference, not the first frame: nothing to join.
	ref := videoJob(t, h, alice, frame.ID)
	if err := h.store.Keep(ctx, jobs.Job{ID: ref, UserID: alice}, []jobs.Result{{ContentType: "video/mp4", Data: clip(t, "1")}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := joinedOf(t, h, ref); ok {
		t.Error("a run that took the frame as a reference is joined")
	}

	// The job's files go with the joined clip, so the job can go too.
	gone, err := h.store.DeleteJobFiles(ctx, alice, next)
	if err != nil || gone != 2 {
		t.Fatalf("%d files went: %v", gone, err)
	}
	if _, ok := h.bucket.Get(j.key); ok {
		t.Error("the joined clip is still in the bucket")
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM framy.job WHERE id=$1`, next); err != nil {
		t.Errorf("the job cannot go: %v", err)
	}
	h.workIsEmpty(t)
}
