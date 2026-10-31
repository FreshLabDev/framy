// SPDX-License-Identifier: Apache-2.0
//go:build integration

package api

import (
	"context"
	"errors"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
)

func TestAJobToContinueIsReadFromTheDatabase(t *testing.T) {
	_, pool := pg(t)
	ctx := context.Background()
	d := pgData{pool: pool}
	run := aJob(t, pool, alice, "done", "video", "a fox running", false)
	exec(t, pool, `UPDATE job SET provider_job_id = 'gen-or-1' WHERE id = $1`, run)
	// A poster frame before the video, and a second video after it: the
	// first video is the one that goes on.
	exec(t, pool, `INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, sha256, width, height)
		VALUES ($1, 'result', $2, 'u/1001/out/poster.jpg', 'image/jpeg', 3, 'x', 1280, 720)`, alice, run)
	var video int64
	if err := pool.QueryRow(ctx, `INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, sha256, width, height, seconds, fps, audio)
		VALUES ($1, 'result', $2, 'u/1001/out/a.mp4', 'video/mp4', 4096, 'x', 1280, 720, 6.04, 24, true) RETURNING id`, alice, run).Scan(&video); err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, sha256)
		VALUES ($1, 'result', $2, 'u/1001/out/b.mp4', 'video/mp4', 3, 'x')`, alice, run)

	c, err := d.Continuation(ctx, alice, run)
	if err != nil {
		t.Fatal(err)
	}
	if c.ModelID != "test/image" || c.Provider != "openrouter" || c.ProviderJobID != "gen-or-1" || c.Status != jobs.Done || c.Free {
		t.Errorf("job: %+v", c)
	}
	if c.Video == nil || c.Video.ID != video || c.Video.Seconds != 6.04 || c.Video.Width != 1280 || c.Video.Bytes != 4096 {
		t.Errorf("video: %+v", c.Video)
	}
	if _, err := d.Continuation(ctx, bob, run); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob reads alice's job: %v", err)
	}

	// A job with no video kept, and one deleted.
	bare := aJob(t, pool, alice, "failed", "video", "nothing came", false)
	if c, err := d.Continuation(ctx, alice, bare); err != nil || c.Video != nil || c.ProviderJobID != "" || c.Status != jobs.Failed {
		t.Errorf("a job with no video: %+v %v", c, err)
	}
	exec(t, pool, `UPDATE job SET deleted_at = now() WHERE id = $1`, run)
	if _, err := d.Continuation(ctx, alice, run); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted job: %v", err)
	}

	// A kept clip comes with its size and length, an old one with no length.
	in, err := d.Inputs(ctx, alice, []int64{video})
	if err != nil || len(in) != 1 || in[0].Seconds != 6.04 || in[0].Bytes != 4096 || in[0].ContentType != "video/mp4" {
		t.Errorf("inputs: %+v %v", in, err)
	}
}
