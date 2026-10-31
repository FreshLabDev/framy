// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// onStack is a second store on the same database, as the test bot's process
// would be (migrations/029_stack.sql).
func onStack(store *Store, stack string) *Store { return &Store{pool: store.pool, stack: stack} }

func TestEachStackKeepsItsOwnOffset(t *testing.T) {
	prod := testStore(t)
	test := onStack(prod, "test")
	ctx := context.Background()
	t.Cleanup(func() { _, _ = prod.pool.Exec(context.Background(), `TRUNCATE stack_state`) })

	if off, err := test.Offset(ctx); err != nil || off != 0 {
		t.Fatalf("a new stack starts at %d, %v", off, err)
	}
	if err := prod.AdvanceOffset(ctx, 900); err != nil {
		t.Fatal(err)
	}
	if err := test.AdvanceOffset(ctx, 12); err != nil {
		t.Fatal(err)
	}
	if err := test.AdvanceOffset(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if off, _ := prod.Offset(ctx); off != 900 {
		t.Errorf("production at %d", off)
	}
	if off, _ := test.Offset(ctx); off != 12 {
		t.Errorf("test at %d, want 12: an offset never goes back", off)
	}
	// Production's stays where every release has read it.
	var legacy int64
	if err := prod.pool.QueryRow(ctx, `SELECT telegram_offset FROM runtime_state WHERE singleton`).Scan(&legacy); err != nil || legacy != 900 {
		t.Errorf("runtime_state has %d, %v", legacy, err)
	}
}

// One person's DM with each bot has the same chat id, and each bot counts its
// message ids from one, so the same ids name two messages in two chats.
func TestEachStackFollowsOnlyTheRunsInItsBotsChats(t *testing.T) {
	prod := testStore(t)
	test := onStack(prod, "test")
	ctx := context.Background()
	const person = 4901
	mine := chatJob(t, prod, person, "tg:4901:10")
	theirs := chatJob(t, test, person, "tg:test:4901:10")
	for _, s := range []struct {
		store *Store
		job   int64
	}{{prod, mine}, {test, theirs}} {
		if err := s.store.AddChatRun(ctx, ChatRun{JobID: s.job, ChatID: person, CommandMessageID: 10, Lang: "en"}); err != nil {
			t.Fatal(err)
		}
	}

	for _, s := range []struct {
		name  string
		store *Store
		job   int64
		other int64
	}{{"production", prod, mine, theirs}, {"test", test, theirs, mine}} {
		runs, err := s.store.OpenChatRuns(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || runs[0].JobID != s.job {
			t.Errorf("%s follows %+v, want only job %d", s.name, runs, s.job)
		}
		if _, err := s.store.ChatRun(ctx, s.other); !errors.Is(err, ErrNoRun) {
			t.Errorf("%s reads the other bot's run: %v", s.name, err)
		}
		if _, err := s.store.ChatRun(ctx, s.job); err != nil {
			t.Errorf("%s cannot read its own run: %v", s.name, err)
		}
	}

	// Both results go out as message 20 of the DM, each in its own bot's.
	for _, s := range []struct {
		store *Store
		job   int64
		file  string
	}{{prod, mine, "prod-file"}, {test, theirs, "test-file"}} {
		if _, err := s.store.MoveChatRun(ctx, s.job, StagePreview, StageGenerating); err != nil {
			t.Fatal(err)
		}
		res := ChatResult{MessageID: 20, FileID: s.file, FileUniqueID: "u", FileType: FilePhoto}
		if ok, err := s.store.FinishChatRun(ctx, s.job, StageDone, res); err != nil || !ok {
			t.Fatalf("finish %d: %v %v", s.job, ok, err)
		}
	}
	if got, ok, err := prod.ResultRun(ctx, person, 20); err != nil || !ok || got != mine {
		t.Errorf("production's reply finds job %d %v %v, want %d", got, ok, err, mine)
	}
	if got, ok, err := test.ResultRun(ctx, person, 20); err != nil || !ok || got != theirs {
		t.Errorf("the test bot's reply finds job %d %v %v, want %d", got, ok, err, theirs)
	}

	// Inline mode resends a file by an id only its own bot can use.
	for _, s := range []struct {
		name  string
		store *Store
		file  string
	}{{"production", prod, "prod-file"}, {"test", test, "test-file"}} {
		if _, err := s.store.pool.Exec(ctx, `UPDATE job SET status = 'done', queued_at = now(), finished_at = now() WHERE user_id = $1`, person); err != nil {
			t.Fatal(err)
		}
		got, err := s.store.InlineResults(ctx, person, "", 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].FileID != s.file {
			t.Errorf("%s inline: %+v, want only %s", s.name, got, s.file)
		}
	}
}

func TestAShareFromTheOtherSiteUploadsTheFile(t *testing.T) {
	prod := testStore(t)
	test := onStack(prod, "test")
	ctx := context.Background()
	job := inlineJob(t, prod, 4902, "A red fox", FilePhoto, false)
	asset := resultAsset(t, prod, 4902, job, "image/png")

	src, err := prod.ShareSource(ctx, 4902, asset)
	if err != nil || src.FileID == "" {
		t.Fatalf("production's own file: %+v %v", src, err)
	}
	src, err = test.ShareSource(ctx, 4902, asset)
	if err != nil {
		t.Fatal(err)
	}
	if src.FileID != "" || src.JobID != job {
		t.Errorf("the test bot gets %+v: the file_id is production's bot's", src)
	}
}

func TestASiteResultGoesFromItsOwnStacksBot(t *testing.T) {
	prod := testStore(t)
	test := onStack(prod, "test")
	ctx := context.Background()
	mine := siteJobOf(t, prod, 4903, "site", time.Minute, false)
	theirs := siteJobOf(t, prod, 4903, "site", time.Minute, false)
	if _, err := prod.pool.Exec(ctx, `UPDATE job SET stack = 'test' WHERE id = $1`, theirs); err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct {
		name  string
		store *Store
		job   int64
	}{{"production", prod, mine}, {"test", test, theirs}} {
		got, err := s.store.SiteResults(ctx, time.Now().Add(-time.Hour), 10)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, r := range got {
			if r.UserID == 4903 {
				ids = append(ids, r.JobID)
			}
		}
		if len(ids) != 1 || ids[0] != s.job {
			t.Errorf("%s sends %v, want only job %d", s.name, ids, s.job)
		}
	}
	// The DM run the test bot sent is the test bot's.
	if ok, err := test.ClaimSiteResult(ctx, theirs); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := test.SentSiteResult(ctx, ChatRun{JobID: theirs, ChatID: 4903, Lang: "en"}, ChatResult{MessageID: 5, FileID: "f", FileType: FilePhoto}); err != nil {
		t.Fatal(err)
	}
	if _, err := test.ChatRun(ctx, theirs); err != nil {
		t.Errorf("the test bot's DM run: %v", err)
	}
	if _, err := prod.ChatRun(ctx, theirs); !errors.Is(err, ErrNoRun) {
		t.Errorf("production sees the test bot's DM run: %v", err)
	}
}

func TestEachStackHasItsOwnAlbums(t *testing.T) {
	prod := albumStore(t)
	test := onStack(prod, "test")
	ctx := context.Background()
	// Message 30 of one person's DM with each bot, in an album of each.
	a := photoItem(4904, "g-prod", 30)
	b := photoItem(4904, "g-test", 30)
	b.FileID = "test-file"
	if err := prod.NoteAlbumItem(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := test.NoteAlbumItem(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, err := prod.AlbumItems(ctx, 4904, "g-prod"); err != nil || len(got) != 1 || got[0].FileID != a.FileID {
		t.Errorf("production's album: %+v %v", got, err)
	}
	if got, err := test.AlbumItems(ctx, 4904, "g-test"); err != nil || len(got) != 1 || got[0].FileID != "test-file" {
		t.Errorf("the test bot's album: %+v %v", got, err)
	}
	if got, err := prod.AlbumItems(ctx, 4904, "g-test"); err != nil || len(got) != 0 {
		t.Errorf("production reads the test bot's album: %+v %v", got, err)
	}
}
