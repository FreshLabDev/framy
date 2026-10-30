// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
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
	if len(got) != 1 || got[0] != (SiteResult{JobID: job, UserID: 4401, Prompt: "a fox", Kind: "image", Model: "Chat image"}) {
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
	if err := store.SentSiteResult(ctx, ChatRun{JobID: job, ChatID: 4401, Lang: "uk"}, ChatResult{MessageID: 77, FileID: "f", FileUniqueID: "u"}); err != nil {
		t.Fatal(err)
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
