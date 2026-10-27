// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"testing"

	"github.com/FreshLabDev/tg"
)

// chatJob makes a draft job of person id with the dedupe key key.
func chatJob(t *testing.T, store *Store, id int64, key string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := store.Touch(ctx, tg.User{ID: id}, nil); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO catalog_model (id, kind, name, maker) VALUES ('test/chat', 'image', 'Chat image', 'test')
		 ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO catalog_offer (id, model_id, provider, provider_model_id, name, routes, fetched_at)
		 VALUES ('openrouter:test/chat', 'test/chat', 'openrouter', 'test/chat', 'Test: Chat image', '[]', now())
		 ON CONFLICT (id) DO NOTHING`,
	} {
		if _, err := store.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	var job int64
	err := store.pool.QueryRow(ctx, `
		INSERT INTO job (user_id, surface, chat_id, message_id, dedupe_key, offer_id, model_id, provider, kind, prompt, free, status)
		VALUES ($1, 'group', -100, 10, $2, 'openrouter:test/chat', 'test/chat', 'openrouter', 'image', 'a fox', true, 'draft')
		RETURNING id`, id, key).Scan(&job)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestAChatRunFollowsItsJob(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	job := chatJob(t, store, 4301, "tg:-100:10")

	if got, found, err := store.JobByKey(ctx, "tg:-100:10"); err != nil || !found || got != job {
		t.Fatalf("by key: %d %v %v", got, found, err)
	}
	if _, found, err := store.JobByKey(ctx, "tg:-100:11"); err != nil || found {
		t.Fatalf("an unknown key: %v %v", found, err)
	}
	if _, err := store.ChatRun(ctx, job); err != ErrNoRun {
		t.Fatalf("before: %v", err)
	}
	run := ChatRun{JobID: job, ChatID: -1003712830340, ThreadID: 3, CommandMessageID: 10, PreviewMessageID: 3_000_000_000, Lang: "uk", Taken: 2, Album: 4, Note: "run.album_part"}
	if err := store.AddChatRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	// The same update again changes nothing.
	again := run
	again.PreviewMessageID = 99
	if err := store.AddChatRun(ctx, again); err != nil {
		t.Fatal(err)
	}
	got, err := store.ChatRun(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if got.PreviewMessageID != 3_000_000_000 || got.ChatID != -1003712830340 || got.Stage != StagePreview || got.ThreadID != 3 || got.Lang != "uk" || got.Taken != 2 ||
		got.Album != 4 || got.Note != "run.album_part" || !got.GenerateAt.IsZero() {
		t.Fatalf("run = %+v", got)
	}
	// The model's own name, not the offer's with the maker before it.
	if got.UserID != 4301 || got.Prompt != "a fox" || !got.Free || got.Status != "draft" || got.Model != "Chat image" {
		t.Fatalf("the job's side = %+v", got)
	}
	open, err := store.OpenChatRuns(ctx)
	if err != nil || len(open) != 1 || open[0].JobID != job {
		t.Fatalf("open = %+v, %v", open, err)
	}

	// Generate moves it once; a second press finds it moved.
	if moved, err := store.MoveChatRun(ctx, job, StagePreview, StageGenerating); err != nil || !moved {
		t.Fatalf("move: %v %v", moved, err)
	}
	if moved, err := store.MoveChatRun(ctx, job, StagePreview, StageGenerating); err != nil || moved {
		t.Fatalf("second move: %v %v", moved, err)
	}
	if got, _ := store.ChatRun(ctx, job); got.Stage != StageGenerating || got.GenerateAt.IsZero() {
		t.Fatalf("generating = %+v", got)
	}
	if _, err := store.FinishChatRun(ctx, job, StageGenerating, ChatResult{}); err == nil {
		t.Fatal("generating taken as an end")
	}
	res := ChatResult{MessageID: 3_000_000_001, FileID: "file", FileUniqueID: "unique"}
	if done, err := store.FinishChatRun(ctx, job, StageDone, res); err != nil || !done {
		t.Fatalf("finish: %v %v", done, err)
	}
	if done, err := store.FinishChatRun(ctx, job, StageFailed, ChatResult{}); err != nil || done {
		t.Fatalf("a finished run finished again: %v %v", done, err)
	}
	var msg int64
	var file, unique string
	if err := store.pool.QueryRow(ctx, `SELECT result_message_id, result_file_id, result_file_unique_id FROM chat_run WHERE job_id=$1`, job).
		Scan(&msg, &file, &unique); err != nil || msg != 3_000_000_001 || file != "file" || unique != "unique" {
		t.Fatalf("result = %d %q %q, %v", msg, file, unique, err)
	}
	if open, _ := store.OpenChatRuns(ctx); len(open) != 0 {
		t.Fatalf("a finished run is open: %+v", open)
	}
}

func TestStopOnlyStopsAGeneratingRun(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	job := chatJob(t, store, 4302, "tg:-100:20")
	if err := store.AddChatRun(ctx, ChatRun{JobID: job, ChatID: -100, CommandMessageID: 20, PreviewMessageID: 21, Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	if stopped, err := store.StopChatRun(ctx, job); err != nil || stopped {
		t.Fatalf("a preview stopped: %v %v", stopped, err)
	}
	if _, err := store.MoveChatRun(ctx, job, StagePreview, StageGenerating); err != nil {
		t.Fatal(err)
	}
	if stopped, err := store.StopChatRun(ctx, job); err != nil || !stopped {
		t.Fatalf("stop: %v %v", stopped, err)
	}
	got, _ := store.ChatRun(ctx, job)
	if !got.Stopped || got.Stage != StageGone {
		t.Fatalf("run = %+v", got)
	}
	// A job that goes takes its run with it.
	if _, err := store.pool.Exec(ctx, `DELETE FROM job WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChatRun(ctx, job); err != ErrNoRun {
		t.Fatalf("after the job: %v", err)
	}
}
