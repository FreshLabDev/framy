// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
)

var inlineJobs atomic.Int64

// inlineJob makes a done job of person user with prompt, whose chat run
// ended with a result of fileType, or with none when fileType is empty.
func inlineJob(t *testing.T, store *Store, user int64, prompt, fileType string, deleted bool) int64 {
	t.Helper()
	ctx := context.Background()
	n := inlineJobs.Add(1)
	job := chatJob(t, store, user, "inline:"+strconv.FormatInt(n, 10))
	if _, err := store.pool.Exec(ctx, `
		UPDATE job SET status = 'done', queued_at = now(), finished_at = now(), prompt = $2, deleted_at = CASE WHEN $3 THEN now() END
		WHERE id = $1`, job, prompt, deleted); err != nil {
		t.Fatal(err)
	}
	if err := store.AddChatRun(ctx, ChatRun{JobID: job, ChatID: -100, CommandMessageID: 10, Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveChatRun(ctx, job, StagePreview, StageGenerating); err != nil {
		t.Fatal(err)
	}
	if fileType == "" {
		return job
	}
	res := ChatResult{MessageID: 100 + n, FileID: "file-" + strconv.FormatInt(n, 10), FileUniqueID: "u", FileType: fileType}
	if ok, err := store.FinishChatRun(ctx, job, StageDone, res); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	return job
}

func TestInlineResultsAreThePersonsPhotosAndVideos(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	photo := inlineJob(t, store, 4501, "A red fox", FilePhoto, false)
	inlineJob(t, store, 4501, "a fox as a file", FileDocument, false)
	inlineJob(t, store, 4501, "a fox that moves", FileAnimation, false)
	inlineJob(t, store, 4501, "a fox deleted", FilePhoto, true)
	inlineJob(t, store, 4501, "a fox still making", "", false)
	inlineJob(t, store, 4502, "someone else's fox", FilePhoto, false)
	video := inlineJob(t, store, 4501, "Fox in the rain", FileVideo, false)

	ids := func(search string, before int64, limit int) []int64 {
		t.Helper()
		got, err := store.InlineResults(ctx, 4501, search, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, r := range got {
			out = append(out, r.JobID)
		}
		return out
	}
	got, err := store.InlineResults(ctx, 4501, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := []InlineResult{
		{JobID: video, Prompt: "Fox in the rain", Model: "Chat image", FileID: got[0].FileID, FileType: FileVideo},
		{JobID: photo, Prompt: "A red fox", Model: "Chat image", FileID: got[1].FileID, FileType: FilePhoto},
	}
	if !reflect.DeepEqual(got, want) || got[0].FileID == "" || got[0].FileID == got[1].FileID {
		t.Fatalf("results = %+v", got)
	}
	for search, want := range map[string][]int64{
		"FOX":     {video, photo},
		"rain":    {video},
		"red fox": {photo},
		"%":       nil,
		"_":       nil,
		"wolf":    nil,
	} {
		if got := ids(search, 0, 20); !reflect.DeepEqual(got, want) {
			t.Errorf("search %q = %v, want %v", search, got, want)
		}
	}
	// A page at a time, older than the last one listed.
	if got := ids("", 0, 1); !reflect.DeepEqual(got, []int64{video}) {
		t.Fatalf("first page = %v", got)
	}
	if got := ids("", video, 1); !reflect.DeepEqual(got, []int64{photo}) {
		t.Fatalf("second page = %v", got)
	}
	if got := ids("", photo, 1); got != nil {
		t.Fatalf("past the end = %v", got)
	}
}

// A result sent from the site keeps its file type, and so is listed.
func TestASiteResultIsListedInline(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	job := siteJobOf(t, store, 4503, "site", 0, false)
	if _, err := store.pool.Exec(ctx, `UPDATE job SET queued_at = now() WHERE id = $1`, job); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimSiteResult(ctx, job); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := store.SentSiteResult(ctx, ChatRun{JobID: job, ChatID: 4503, Lang: "en"}, ChatResult{MessageID: 9, FileID: "f", FileUniqueID: "u", FileType: FilePhoto}); err != nil {
		t.Fatal(err)
	}
	got, err := store.InlineResults(ctx, 4503, "", 0, 20)
	if err != nil || len(got) != 1 || got[0] != (InlineResult{JobID: job, Prompt: "a fox", Model: "Chat image", FileID: "f", FileType: FilePhoto}) {
		t.Fatalf("results = %+v, %v", got, err)
	}
}
