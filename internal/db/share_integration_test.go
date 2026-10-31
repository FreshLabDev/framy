// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// resultAsset keeps a result of job for user and returns its id.
func resultAsset(t *testing.T, store *Store, user, job int64, contentType string) int64 {
	t.Helper()
	var id int64
	key := "share/" + strconv.FormatInt(job, 10) + "/" + strconv.FormatInt(inlineJobs.Add(1), 10)
	if err := store.pool.QueryRow(context.Background(), `
		INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, width, height, sha256)
		VALUES ($1, 'result', $2, $3, $4, 2048, 1024, 768, 'x') RETURNING id`, user, job, key, contentType).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAShareSourceIsTheResultWithItsFileInTelegram(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	job := inlineJob(t, store, 4601, "A red fox", FilePhoto, false)
	first := resultAsset(t, store, 4601, job, "image/png")
	second := resultAsset(t, store, 4601, job, "image/png")

	src, err := store.ShareSource(ctx, 4601, first)
	if err != nil {
		t.Fatal(err)
	}
	if src.AssetID != first || src.JobID != job || src.ContentType != "image/png" || src.Bytes != 2048 || src.Width != 1024 ||
		src.Height != 768 || src.Prompt != "A red fox" || src.Model != "Chat image" || src.FileID == "" || src.FileType != FilePhoto {
		t.Fatalf("first = %+v", src)
	}
	// The chat run keeps the album's first message: the second result has
	// no file of its own there.
	if src, err := store.ShareSource(ctx, 4601, second); err != nil || src.FileID != "" || src.FileType != "" {
		t.Fatalf("second = %+v, %v", src, err)
	}
	// A continuation's joined clip goes too, as a file of its own.
	var joined int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, sha256)
		VALUES (4601, 'joined', $1, $2, 'video/mp4', 4096, 'x') RETURNING id`, job, "share/joined/"+strconv.FormatInt(job, 10)).Scan(&joined); err != nil {
		t.Fatal(err)
	}
	if src, err := store.ShareSource(ctx, 4601, joined); err != nil || src.AssetID != joined || src.FileID != "" || src.ContentType != "video/mp4" {
		t.Fatalf("joined = %+v, %v", src, err)
	}
}

func TestOnlyTheOwnersDoneResultIsShared(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	done := resultAsset(t, store, 4611, inlineJob(t, store, 4611, "fox", "", false), "image/png")
	deleted := resultAsset(t, store, 4611, inlineJob(t, store, 4611, "fox", FilePhoto, true), "image/png")
	runningJob := inlineJob(t, store, 4611, "fox", "", false)
	running := resultAsset(t, store, 4611, runningJob, "image/png")
	if _, err := store.pool.Exec(ctx, `UPDATE job SET status = 'running', finished_at = NULL WHERE id = $1`, runningJob); err != nil {
		t.Fatal(err)
	}
	if src, err := store.ShareSource(ctx, 4611, done); err != nil || src.FileID != "" {
		t.Fatalf("a result never sent: %+v, %v", src, err)
	}
	for name, c := range map[string]struct{ user, asset int64 }{
		"someone else's": {4612, done}, "deleted": {4611, deleted}, "running": {4611, running}, "none": {4611, 1 << 40},
	} {
		if _, err := store.ShareSource(ctx, c.user, c.asset); !errors.Is(err, ErrNoResult) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
