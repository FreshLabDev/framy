// SPDX-License-Identifier: Apache-2.0
//go:build integration

package api

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// aJob inserts a job of user as the queue leaves it: status, kind and
// prompt, queued unless it is a draft, ended when its status ends.
func aJob(t *testing.T, pool *pgxpool.Pool, user int64, status, kind, prompt string, isFree bool) int64 {
	t.Helper()
	offer, model := paidOffer, "test/image"
	if isFree {
		offer, model = freeOffer, "microsoft/mai-image-2.6-flash"
	}
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO job (user_id, surface, offer_id, model_id, provider, kind, prompt, settings, free, status,
		                 chat_id, message_id, error, error_class, queued_at, finished_at)
		VALUES ($1, 'site', $2, $3, 'openrouter', $4, $5, '{"n": 1}', $6, $7,
		        42, 7, CASE WHEN $7 = 'failed' THEN 'the provider said no' END,
		        CASE WHEN $7 = 'failed' THEN 'moderation' END,
		        CASE WHEN $7 <> 'draft' THEN now() END,
		        CASE WHEN $7 IN ('done', 'failed', 'cancelled', 'expired') THEN now() END)
		RETURNING id`, user, offer, model, kind, prompt, isFree, status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// anAsset inserts a file of user: an upload a job takes, or a result of job.
func anAsset(t *testing.T, pool *pgxpool.Pool, user, job int64, key string, input bool) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if input {
		err := pool.QueryRow(ctx, `INSERT INTO asset (user_id, kind, object_key, content_type, bytes, sha256)
			VALUES ($1, 'upload', $2, 'image/jpeg', 3, 'x') RETURNING id`, user, key).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, pool, `INSERT INTO job_input (job_id, position, asset_id)
			VALUES ($1, (SELECT count(*) FROM job_input WHERE job_id = $1), $2)`, job, id)
		return id
	}
	err := pool.QueryRow(ctx, `INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, sha256)
		VALUES ($1, 'result', $2, $3, 'image/png', 3, 'x') RETURNING id`, user, job, key).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func idsOfStates(states []State) []int64 {
	var out []int64
	for _, s := range states {
		out = append(out, s.ID)
	}
	return out
}

func TestTheHistoryIsReadFromTheDatabase(t *testing.T) {
	_, pool := pg(t)
	ctx := context.Background()
	d := pgData{pool: pool}
	fox := aJob(t, pool, alice, "done", "image", "A red fox in snow", false)
	in := anAsset(t, pool, alice, fox, "u/1001/in/a.jpg", true)
	out := anAsset(t, pool, alice, fox, "u/1001/out/a.png", false)
	run := aJob(t, pool, alice, "failed", "video", "fox running", false)
	anAsset(t, pool, alice, run, "u/1001/in/b.jpg", true)
	cotton := aJob(t, pool, alice, "done", "image", "100% cotton shirt", false)
	den := aJob(t, pool, alice, "done", "image", "fox_den at night", false)
	aJob(t, pool, alice, "draft", "image", "draft fox", false)
	gone := aJob(t, pool, alice, "done", "image", "a deleted fox", false)
	exec(t, pool, `UPDATE job SET deleted_at = now() WHERE id = $1`, gone)
	aJob(t, pool, bob, "done", "image", "bob's fox", false)

	for _, tc := range []struct {
		name string
		f    Filter
		want []int64
	}{
		{"everything", Filter{Limit: 10}, []int64{den, cotton, run, fox}},
		{"a kind", Filter{Kind: catalog.Video, Limit: 10}, []int64{run}},
		{"any case", Filter{Query: "FOX", Limit: 10}, []int64{den, run, fox}},
		{"a percent sign is a percent sign", Filter{Query: "%", Limit: 10}, []int64{cotton}},
		{"so is an underscore", Filter{Query: "_", Limit: 10}, []int64{den}},
		{"a backslash", Filter{Query: `\`, Limit: 10}, nil},
		{"a page", Filter{Before: run, Limit: 10}, []int64{fox}},
		{"a short page", Filter{Limit: 2}, []int64{den, cotton}},
	} {
		got, err := d.List(ctx, alice, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if ids := idsOfStates(got); len(ids) != len(tc.want) || (len(ids) > 0 && (ids[0] != tc.want[0] || ids[len(ids)-1] != tc.want[len(tc.want)-1])) {
			t.Errorf("%s: %v, want %v", tc.name, ids, tc.want)
		}
	}

	// A job of the list comes with what was asked and its files; a failed
	// one with its inputs and no results.
	got, err := d.List(ctx, alice, Filter{Query: "fox", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	last := got[len(got)-1]
	if last.Prompt != "A red fox in snow" || last.Model != "test/image" || last.Surface != "site" || last.Settings["n"] != float64(1) {
		t.Errorf("%+v", last)
	}
	if len(last.Inputs) != 1 || last.Inputs[0].ID != in || len(last.Results) != 1 || last.Results[0].ID != out {
		t.Errorf("inputs %v, results %v", last.Inputs, last.Results)
	}
	if failed := got[1]; failed.ID != run || len(failed.Inputs) != 1 || len(failed.Results) != 0 {
		t.Errorf("the failed job: %+v", failed)
	}
	if last.Joined != nil {
		t.Errorf("a job with no joined clip: %+v", last.Joined)
	}

	// A continuation's joined clip comes apart from its results.
	var joined int64
	if err := pool.QueryRow(ctx, `INSERT INTO asset (user_id, kind, job_id, object_key, content_type, bytes, sha256, seconds)
		VALUES ($1, 'joined', $2, 'u/1001/out/joined.mp4', 'video/mp4', 3, 'x', 9.96) RETURNING id`, alice, fox).Scan(&joined); err != nil {
		t.Fatal(err)
	}
	s, err := d.State(ctx, alice, fox)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Results) != 1 || s.Results[0].ID != out || s.Joined == nil || s.Joined.ID != joined || s.Joined.Seconds != 9.96 {
		t.Errorf("results %v, joined %+v", s.Results, s.Joined)
	}
	if _, err := d.State(ctx, alice, gone); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted job reads: %v", err)
	}
}

func TestARemovedJobIsClearedButStillCountsAsFree(t *testing.T) {
	_, pool := pg(t)
	ctx := context.Background()
	d := pgData{pool: pool}
	allow, err := free.New(free.Config{Pool: pool, DailyBudgetUSD: 1, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	ran := aJob(t, pool, alice, "done", "image", "a free fox", true)
	anAsset(t, pool, alice, ran, "u/1001/in/c.jpg", true)
	exec(t, pool, `INSERT INTO chat_run (job_id, chat_id, command_message_id, lang) VALUES ($1, 42, 7, 'en')`, ran)
	refused := aJob(t, pool, alice, "failed", "image", "a refused fox", true)
	running := aJob(t, pool, alice, "running", "image", "a running fox", true)
	before, err := allow.Usage(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{ran, refused} {
		if err := d.Remove(ctx, alice, id); err != nil {
			t.Fatalf("remove %d: %v", id, err)
		}
	}
	var prompt, settings string
	var errText, chat *string
	var class *string
	if err := pool.QueryRow(ctx, `SELECT prompt, settings::text, error, chat_id::text, error_class FROM job WHERE id = $1 AND deleted_at IS NOT NULL`, refused).
		Scan(&prompt, &settings, &errText, &chat, &class); err != nil {
		t.Fatal(err)
	}
	if prompt != "" || settings != "{}" || errText != nil || chat != nil {
		t.Errorf("left: %q %s %v %v", prompt, settings, errText, chat)
	}
	if class == nil || *class != string(jobs.Moderation) {
		t.Errorf("the class the allowance counts went: %v", class)
	}
	var inputs, runs int
	pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM job_input WHERE job_id = $1), (SELECT count(*) FROM chat_run WHERE job_id = $1)`, ran).Scan(&inputs, &runs)
	if inputs != 0 || runs != 0 {
		t.Errorf("job_input %d, chat_run %d", inputs, runs)
	}

	after, err := allow.Usage(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if before.Images.Used != 3 || after.Images.Used != before.Images.Used {
		t.Errorf("free images used %d, then %d: deleting gives no run back", before.Images.Used, after.Images.Used)
	}

	for _, tc := range []struct {
		user, id int64
		want     error
	}{
		{alice, running, ErrActive},
		{alice, ran, ErrNotFound},
		{bob, refused, ErrNotFound},
		{alice, 1 << 40, ErrNotFound},
	} {
		if err := d.Remove(ctx, tc.user, tc.id); !errors.Is(err, tc.want) {
			t.Errorf("remove %d of %d: %v, want %v", tc.id, tc.user, err, tc.want)
		}
	}
}

func TestForgettingAnAccount(t *testing.T) {
	_, pool := pg(t)
	ctx := context.Background()
	d := pgData{pool: pool}
	for _, table := range []string{"connection", "oauth_state", "album_item"} {
		exec(t, pool, `DELETE FROM `+table+` WHERE user_id IN (1001, 1002)`)
	}
	for _, user := range []int64{alice, bob} {
		exec(t, pool, `INSERT INTO connection (id, user_id, provider, ciphertext, wrapped_dek, kek_version, last4, status)
			VALUES (gen_random_uuid(), $1, 'openai', 'c', 'd', 1, 'WXYZ', 'valid')`, user)
		exec(t, pool, `INSERT INTO profile (user_id, currency) VALUES ($1, 'UAH')`, user)
		exec(t, pool, `INSERT INTO album_item (chat_id, media_group_id, message_id, user_id, kind, file_id, file_unique_id, sent_at)
			VALUES (-100, 'g', $1, $1, 'photo', 'f', 'u', now())`, user)
	}
	exec(t, pool, `INSERT INTO oauth_state (state_hash, user_id, provider, verifier, expires_at)
		VALUES (sha256('s'), 1001, 'openrouter', 'v', now() + interval '10 minutes')`)
	done := aJob(t, pool, alice, "done", "image", "a fox", false)
	draft := aJob(t, pool, alice, "draft", "image", "a draft fox", false)
	running := aJob(t, pool, alice, "running", "image", "a running fox", false)
	bobs := aJob(t, pool, bob, "done", "image", "bob's fox", false)

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	// While a run goes, nothing is forgotten.
	if active, err := d.Active(ctx, alice); err != nil || !active {
		t.Fatalf("active %v, %v", active, err)
	}
	if err := d.Forget(ctx, alice); !errors.Is(err, ErrActive) {
		t.Fatalf("forget mid-run: %v", err)
	}
	if count(`SELECT count(*) FROM connection WHERE user_id = 1001`) != 1 {
		t.Fatal("a refused forget deleted the key")
	}

	exec(t, pool, `UPDATE job SET status = 'done', finished_at = now() WHERE id = $1`, running)
	if err := d.Forget(ctx, alice); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"connection": 0, "oauth_state": 0, "profile": 0, "album_item": 0} {
		if n := count(`SELECT count(*) FROM ` + table + ` WHERE user_id = 1001`); n != want {
			t.Errorf("%s: %d rows of alice left", table, n)
		}
		if table != "oauth_state" {
			if n := count(`SELECT count(*) FROM ` + table + ` WHERE user_id = 1002`); n != 1 {
				t.Errorf("%s: bob's row went", table)
			}
		}
	}
	if n := count(`SELECT count(*) FROM job WHERE id = ANY($1) AND deleted_at IS NOT NULL AND prompt = ''`, []int64{done, draft, running}); n != 3 {
		t.Errorf("%d of alice's 3 jobs cleared", n)
	}
	if n := count(`SELECT count(*) FROM job WHERE id = $1 AND status = 'cancelled'`, draft); n != 1 {
		t.Error("the draft was not cancelled")
	}
	if n := count(`SELECT count(*) FROM job WHERE id = $1 AND deleted_at IS NULL AND prompt <> ''`, bobs); n != 1 {
		t.Error("bob's job was touched")
	}
	if n := count(`SELECT count(*) FROM core.person WHERE telegram_user_id = 1001`); n != 1 {
		t.Error("the person went from core")
	}
	if list, err := d.List(ctx, alice, Filter{Limit: 10}); err != nil || len(list) != 0 {
		t.Errorf("the history after: %v, %v", idsOfStates(list), err)
	}
	// Forgetting again finds nothing to do.
	if err := d.Forget(ctx, alice); err != nil {
		t.Errorf("a second forget: %v", err)
	}
}
