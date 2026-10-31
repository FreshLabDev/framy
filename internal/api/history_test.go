// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

func ended(at time.Time) *time.Time { return &at }

// history fills alice's history: four jobs of hers, a draft and one of bob's.
func history(r *rig) {
	done := ended(time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC))
	r.data.set(alice, State{ID: 11, Status: jobs.Done, Kind: catalog.Image, Prompt: "A red fox in snow", Model: "m/one", Surface: "site",
		Settings: map[string]any{"resolution": "1K"}, FinishedAt: done,
		Inputs: []Result{{ID: 501, ContentType: "image/jpeg"}}, Results: []Result{{ID: 601, ContentType: "image/png"}}})
	r.data.set(alice, State{ID: 12, Status: jobs.Failed, Kind: catalog.Video, Prompt: "fox running", FinishedAt: done, ErrorClass: jobs.Moderation})
	r.data.set(alice, State{ID: 13, Status: jobs.Done, Kind: catalog.Image, Prompt: "100% cotton shirt", FinishedAt: done})
	r.data.set(alice, State{ID: 14, Status: jobs.Running, Kind: catalog.Image, Prompt: "a cat"})
	r.data.set(alice, State{ID: 15, Status: jobs.Draft, Kind: catalog.Image, Prompt: "draft fox"})
	r.data.set(bob, State{ID: 16, Status: jobs.Done, Kind: catalog.Image, Prompt: "bob's fox", FinishedAt: done})
}

func idsOf(t *testing.T, m map[string]any) []float64 {
	t.Helper()
	list, ok := m["jobs"].([]any)
	if !ok {
		t.Fatalf("no jobs in %v", m)
	}
	var out []float64
	for _, j := range list {
		out = append(out, j.(map[string]any)["id"].(float64))
	}
	return out
}

func TestTheHistoryIsThePersonsJobsNewestFirst(t *testing.T) {
	r := newRig(t)
	history(r)
	m := jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs", nil))
	if got := idsOf(t, m); len(got) != 4 || got[0] != 14 || got[3] != 11 {
		t.Fatalf("ids %v: a draft and bob's job are not alice's history", got)
	}
	if _, ok := m["next"]; ok {
		t.Errorf("a single page says next: %v", m["next"])
	}
	// A job comes as GET gives it, with what was asked and the links of its
	// files.
	last := m["jobs"].([]any)[3].(map[string]any)
	if last["prompt"] != "A red fox in snow" || last["model"] != "m/one" || last["surface"] != "site" {
		t.Errorf("job %v", last)
	}
	if s, _ := last["settings"].(map[string]any); s["resolution"] != "1K" {
		t.Errorf("settings %v", last["settings"])
	}
	in, _ := last["inputs"].([]any)
	res, _ := last["results"].([]any)
	if len(in) != 1 || len(res) != 1 ||
		!strings.Contains(in[0].(map[string]any)["url"].(string), "obj-501") ||
		!strings.Contains(res[0].(map[string]any)["url"].(string), "obj-601") {
		t.Errorf("inputs %v, results %v", in, res)
	}
	// Only a result saves as a file of framy's.
	if _, ok := in[0].(map[string]any)["download"]; ok || !strings.HasSuffix(res[0].(map[string]any)["download"].(string), "save=framy-11-1.png") {
		t.Errorf("downloads: inputs %v, results %v", in, res)
	}
}

func TestTheHistoryFiltersSearchesAndPages(t *testing.T) {
	r := newRig(t)
	history(r)
	for _, tc := range []struct {
		query string
		want  []float64
	}{
		{"?kind=video", []float64{12}},
		{"?q=FOX", []float64{12, 11}},
		{"?q=100%25", []float64{13}},
		{"?kind=image&q=fox", []float64{11}},
		{"?before=13", []float64{12, 11}},
	} {
		got := idsOf(t, jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs"+tc.query, nil)))
		if len(got) != len(tc.want) || (len(got) > 0 && (got[0] != tc.want[0] || got[len(got)-1] != tc.want[len(tc.want)-1])) {
			t.Errorf("%s: %v, want %v", tc.query, got, tc.want)
		}
	}
	m := jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs?limit=2", nil))
	if got := idsOf(t, m); len(got) != 2 || m["next"] != float64(13) {
		t.Fatalf("first page %v, next %v", got, m["next"])
	}
	m = jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs?limit=2&before=13", nil))
	if got := idsOf(t, m); len(got) != 2 || got[0] != 12 || m["next"] != nil {
		t.Fatalf("last page %v, next %v", got, m["next"])
	}
}

func TestAWrongHistoryRequestIsRefused(t *testing.T) {
	r := newRig(t)
	for _, q := range []string{"?kind=audio", "?before=x", "?before=-1", "?limit=0", "?limit=101", "?q=" + strings.Repeat("a", 201)} {
		resp := r.do(alice, http.MethodGet, "/api/jobs"+q, nil)
		if m := jsonOf(t, resp); resp.StatusCode != http.StatusBadRequest || m["error"] != "bad_request" {
			t.Errorf("%s: %d %v", q, resp.StatusCode, m)
		}
	}
	if resp := r.do(0, http.MethodGet, "/api/jobs", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("nobody: %d", resp.StatusCode)
	}
}

func TestDeletingAJobTakesItsFilesAndItsPlaceInTheHistory(t *testing.T) {
	r := newRig(t)
	history(r)
	resp := r.do(alice, http.MethodDelete, "/api/jobs/11", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("%d", resp.StatusCode)
	}
	if len(r.files.jobs) != 1 || r.files.jobs[0] != 11 || !r.data.removed[11] {
		t.Fatalf("files of %v, removed %v", r.files.jobs, r.data.removed)
	}
	if resp := r.do(alice, http.MethodGet, "/api/jobs/11", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a deleted job answers %d", resp.StatusCode)
	}
	if got := idsOf(t, jsonOf(t, r.do(alice, http.MethodGet, "/api/jobs", nil))); len(got) != 3 {
		t.Errorf("history after %v", got)
	}
}

func TestOnlyAnEndedJobOfOnesOwnIsDeleted(t *testing.T) {
	r := newRig(t)
	history(r)
	for _, tc := range []struct {
		user   int64
		path   string
		status int
		code   string
	}{
		{alice, "/api/jobs/14", http.StatusConflict, "job_active"},
		{alice, "/api/jobs/16", http.StatusNotFound, "not_found"},
		{bob, "/api/jobs/11", http.StatusNotFound, "not_found"},
		{alice, "/api/jobs/x", http.StatusNotFound, "not_found"},
	} {
		resp := r.do(tc.user, http.MethodDelete, tc.path, nil)
		if m := jsonOf(t, resp); resp.StatusCode != tc.status || m["error"] != tc.code {
			t.Errorf("%d %s: %d %v", tc.user, tc.path, resp.StatusCode, m)
		}
	}
	if len(r.files.jobs) != 0 || len(r.data.removed) != 0 {
		t.Errorf("files of %v, removed %v", r.files.jobs, r.data.removed)
	}
}

func TestAJobWhoseFilesWouldNotGoStays(t *testing.T) {
	r := newRig(t)
	history(r)
	r.files.err = errors.New("r2 is down")
	resp := r.do(alice, http.MethodDelete, "/api/jobs/11", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusServiceUnavailable || m["error"] != "storage_unavailable" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if r.data.removed[11] {
		t.Error("the job went before its files")
	}
}

func TestStoppingAQueuedJob(t *testing.T) {
	r := newRig(t)
	history(r)
	r.data.set(alice, State{ID: 20, Status: jobs.Queued, Kind: catalog.Image})
	resp := r.do(alice, http.MethodPost, "/api/jobs/20/cancel", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusOK || m["status"] != "cancelled" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	// A job that ended comes back as it is, and nothing is cancelled.
	resp = r.do(alice, http.MethodPost, "/api/jobs/11/cancel", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusOK || m["status"] != "done" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if len(r.queue.cancelled) != 1 {
		t.Errorf("cancelled %v", r.queue.cancelled)
	}
}

func TestARunningJobOrADraftIsNotStoppedFromTheSite(t *testing.T) {
	r := newRig(t)
	history(r)
	for path, code := range map[string]string{"/api/jobs/14/cancel": "job_running", "/api/jobs/15/cancel": "job_draft"} {
		resp := r.do(alice, http.MethodPost, path, nil)
		if m := jsonOf(t, resp); resp.StatusCode != http.StatusConflict || m["error"] != code {
			t.Errorf("%s: %d %v", path, resp.StatusCode, m)
		}
	}
	// Queued when read, running by the time the queue is asked.
	r.data.set(alice, State{ID: 21, Status: jobs.Queued, Kind: catalog.Image})
	r.queue.cancelErr = jobs.ErrRunning
	resp := r.do(alice, http.MethodPost, "/api/jobs/21/cancel", nil)
	if m := jsonOf(t, resp); resp.StatusCode != http.StatusConflict || m["error"] != "job_running" {
		t.Errorf("%d %v", resp.StatusCode, m)
	}
	if resp := r.do(bob, http.MethodPost, "/api/jobs/20/cancel", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob stops alice's job: %d", resp.StatusCode)
	}
}
