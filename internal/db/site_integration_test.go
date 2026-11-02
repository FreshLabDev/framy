// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

var siteJobs atomic.Int64

// siteJobOf makes a job of person user on surface, done finished ago, and
// deleted when asked.
func siteJobOf(t *testing.T, store *Store, user int64, surface string, ago time.Duration, deleted bool) int64 {
	t.Helper()
	ctx := context.Background()
	draft := chatJob(t, store, user, "site:"+strconv.FormatInt(siteJobs.Add(1), 10))
	_, err := store.pool.Exec(ctx, `
		UPDATE job SET surface = $2, chat_id = NULL, message_id = NULL, status = 'done', results = 1,
		       finished_at = now() - make_interval(secs => $3), deleted_at = CASE WHEN $4 THEN now() END
		WHERE id = $1`, draft, surface, ago.Seconds(), deleted)
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

func TestASiteResultIsSentOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	job := siteJobOf(t, store, 4401, "site", time.Minute, false)
	siteJobOf(t, store, 4401, "site", 2*time.Hour, false)     // too old
	siteJobOf(t, store, 4401, "site", time.Minute, true)      // deleted
	siteJobOf(t, store, 4401, "mini_app", time.Minute, false) // in Telegram already
	siteJobOf(t, store, 4401, "dm", time.Minute, false)

	since := time.Now().Add(-time.Hour)
	listed := func() []SiteResult {
		t.Helper()
		got, err := store.SiteResults(ctx, since, 20)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := listed()
	if len(got) != 1 || !reflect.DeepEqual(got[0], SiteResult{JobID: job, UserID: 4401, Prompt: "a fox", Kind: "image", Model: "Chat image", Deliver: true}) {
		t.Fatalf("site results = %+v", got)
	}

	if ok, err := store.ClaimSiteResult(ctx, job); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, err := store.ClaimSiteResult(ctx, job); err != nil || ok {
		t.Fatalf("a second claim: %v %v", ok, err)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("claimed and listed: %+v", got)
	}
	// Released, it is listed again.
	if err := store.ReleaseSiteResult(ctx, job); err != nil {
		t.Fatal(err)
	}
	if got := listed(); len(got) != 1 {
		t.Fatalf("released and not listed: %+v", got)
	}

	if ok, err := store.ClaimSiteResult(ctx, job); err != nil || !ok {
		t.Fatalf("claim again: %v %v", ok, err)
	}
	// The result went as the clip Continue joined it to.
	var joined int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, sha256)
		VALUES (4401, 'joined', $1, $2, 'video/mp4', 4096, 'x') RETURNING id`, job, "site/joined/"+strconv.FormatInt(job, 10)).Scan(&joined); err != nil {
		t.Fatal(err)
	}
	if err := store.SentSiteResult(ctx, ChatRun{JobID: job, ChatID: 4401, Lang: "uk"},
		ChatResult{MessageID: 77, FileID: "f", FileUniqueID: "u", FileType: FileVideo, AssetID: joined}); err != nil {
		t.Fatal(err)
	}
	var sentAsset int64
	if err := store.pool.QueryRow(ctx, `SELECT result_asset_id FROM chat_run WHERE job_id = $1`, job).Scan(&sentAsset); err != nil || sentAsset != joined {
		t.Fatalf("result asset %d, want %d: %v", sentAsset, joined, err)
	}
	var state string
	var message int64
	if err := store.pool.QueryRow(ctx, `SELECT state, message_id FROM site_delivery WHERE job_id = $1`, job).Scan(&state, &message); err != nil ||
		state != DeliverySent || message != 77 {
		t.Fatalf("delivery %q %d %v", state, message, err)
	}
	// A sent result is not given back, and its run is done in the DM.
	if err := store.ReleaseSiteResult(ctx, job); err != nil {
		t.Fatal(err)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("sent and listed: %+v", got)
	}
	run, err := store.ChatRun(ctx, job)
	if err != nil || run.Stage != StageDone || run.ChatID != 4401 || run.ResultMessageID != 77 || run.UserID != 4401 || run.Lang != "uk" ||
		run.Prompt != "a fox" || run.CommandMessageID != 0 {
		t.Fatalf("run = %+v, %v", run, err)
	}
	if id, found, err := store.ResultRun(ctx, 4401, 77); err != nil || !found || id != job {
		t.Fatalf("result run: %d %v %v", id, found, err)
	}
	if open, err := store.OpenChatRuns(ctx); err != nil || len(open) != 0 {
		t.Fatalf("open runs: %+v %v", open, err)
	}
}

func TestASiteResultThatWillNotGoEnds(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	closed := siteJobOf(t, store, 4402, "site", time.Minute, false)
	failed := siteJobOf(t, store, 4402, "site", time.Minute, false)
	for _, c := range []struct {
		job   int64
		state string
	}{{closed, DeliveryClosed}, {failed, DeliveryFailed}} {
		if ok, err := store.ClaimSiteResult(ctx, c.job); err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		if err := store.EndSiteResult(ctx, c.job, c.state); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := store.pool.QueryRow(ctx, `SELECT state FROM site_delivery WHERE job_id = $1`, c.job).Scan(&state); err != nil || state != c.state {
			t.Fatalf("state %q, want %q: %v", state, c.state, err)
		}
	}
	if err := store.EndSiteResult(ctx, closed, DeliverySent); err == nil {
		t.Fatal("sent taken as an end")
	}
	if got, err := store.SiteResults(ctx, time.Now().Add(-time.Hour), 20); err != nil || len(got) != 0 {
		t.Fatalf("ended and listed: %+v %v", got, err)
	}
	if _, err := store.ChatRun(ctx, closed); err != ErrNoRun {
		t.Fatalf("a run for a result not sent: %v", err)
	}
}

// pressJobOf makes a site job of person user part of press batch at frames,
// in status, ended ago unless it is running.
func pressJobOf(t *testing.T, store *Store, user int64, batch string, frames []int32, status string, ago time.Duration) int64 {
	t.Helper()
	job := siteJobOf(t, store, user, "site", ago, false)
	_, err := store.pool.Exec(context.Background(), `
		UPDATE job SET batch = $2, batch_frames = $3, status = $4,
		       finished_at = CASE WHEN $4 = 'running' THEN NULL ELSE finished_at END
		WHERE id = $1`, job, batch, frames, status)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

// A press comes whole, once its last job ended, its jobs next to each
// other, and the hour is counted from its last end.
func TestAPressComesWholeAfterItsLastJob(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const user = 4411
	alone := siteJobOf(t, store, user, "site", 2*time.Minute, false)
	first := pressJobOf(t, store, user, "p-now", []int32{1, 2}, "done", 5*time.Minute)
	slow := pressJobOf(t, store, user, "p-now", []int32{0}, "running", 0)
	early := pressJobOf(t, store, user, "p-slow", []int32{0}, "done", 90*time.Minute)
	late := pressJobOf(t, store, user, "p-slow", []int32{1}, "done", 10*time.Minute)
	pressJobOf(t, store, user, "p-old", []int32{0}, "done", 100*time.Minute)
	pressJobOf(t, store, user, "p-old", []int32{1}, "done", 70*time.Minute)
	kept := pressJobOf(t, store, user, "p-failed", []int32{0}, "done", 3*time.Minute)
	pressJobOf(t, store, user, "p-failed", []int32{1}, "failed", time.Minute)
	gone := pressJobOf(t, store, user, "p-deleted", []int32{0}, "done", 4*time.Minute)
	deleted := pressJobOf(t, store, user, "p-deleted", []int32{1}, "running", 0)
	if _, err := store.pool.Exec(ctx, `UPDATE job SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}

	listed := func() []int64 {
		t.Helper()
		got, err := store.SiteResults(ctx, time.Now().Add(-time.Hour), 20)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, r := range got {
			ids = append(ids, r.JobID)
			if r.Batch == "p-slow" && r.JobID == late && !reflect.DeepEqual(r.Frames, []int32{1}) {
				t.Fatalf("frames of %d = %v", late, r.Frames)
			}
			if r.JobID == alone && (r.Batch != "" || r.Frames != nil) {
				t.Fatalf("a job made alone = %+v", r)
			}
		}
		return ids
	}
	// The press p-now waits for its slow job; p-slow comes whole, by its
	// last end; p-old ended over an hour ago; a failed or deleted job holds
	// nothing back, and p-failed comes when its failed job ended.
	if got, want := listed(), []int64{early, late, gone, alone, kept}; !reflect.DeepEqual(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}

	if _, err := store.pool.Exec(ctx, `UPDATE job SET status = 'done', finished_at = now() WHERE id = $1`, slow); err != nil {
		t.Fatal(err)
	}
	if got, want := listed(), []int64{early, late, gone, alone, kept, first, slow}; !reflect.DeepEqual(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
}
