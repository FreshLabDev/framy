// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
)

const aKey = "sk-test-0123456789abcdefWXYZ"

func (r *rig) send(user int64, method, path, body string) (int, map[string]any) {
	r.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	resp := r.do(user, method, path, rd, "Content-Type", "application/json")
	if resp.StatusCode == http.StatusNoContent {
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	return resp.StatusCode, jsonOf(r.t, resp)
}

func TestAKeyIsSavedCheckedAndDeleted(t *testing.T) {
	r := newRig(t)
	status, c := r.send(alice, http.MethodPut, "/api/connections/gemini", `{"key":"`+aKey+`"}`)
	if status != http.StatusOK || c["provider"] != "gemini" || c["last4"] != "WXYZ" || c["status"] != "valid" {
		t.Fatalf("%d %v", status, c)
	}
	if len(r.conns.saved) != 1 || r.conns.saved[0] != aKey {
		t.Fatalf("saved %v", r.conns.saved)
	}
	// The answer holds no part of the key beyond the last four.
	for k, v := range c {
		if s, ok := v.(string); ok && strings.Contains(s, "0123456789") {
			t.Errorf("%s carries the key", k)
		}
	}
	if status, c = r.send(alice, http.MethodPost, "/api/connections/gemini/check", ""); status != http.StatusOK || c["status"] != "valid" {
		t.Fatalf("recheck: %d %v", status, c)
	}
	if status, _ = r.send(alice, http.MethodDelete, "/api/connections/gemini", ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if status, c = r.send(alice, http.MethodDelete, "/api/connections/gemini", ""); status != http.StatusNotFound || c["error"] != "not_found" {
		t.Fatalf("second delete: %d %v", status, c)
	}
}

func TestWhatTheKeysServiceRefusesIsSaidPlainly(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{keys.ErrBadKey, http.StatusUnprocessableEntity, "bad_key"},
		{keys.ErrRejected, http.StatusUnprocessableEntity, "key_rejected"},
		{keys.ErrForbidden, http.StatusUnprocessableEntity, "key_forbidden"},
		{keys.ErrUnavailable, http.StatusServiceUnavailable, "check_unavailable"},
		{keys.ErrDisabled, http.StatusServiceUnavailable, "keys_off"},
		{keys.ErrUnreadable, http.StatusConflict, "key_unreadable"},
	} {
		r := newRig(t)
		r.conns.opErr = tc.err
		status, m := r.send(alice, http.MethodPut, "/api/connections/openai", `{"key":"`+aKey+`"}`)
		if status != tc.status || m["error"] != tc.code || m["provider"] != "openai" {
			t.Errorf("%v: %d %v", tc.err, status, m)
		}
		if strings.Contains(m["message"].(string), aKey) {
			t.Errorf("%v: the message quotes the key", tc.err)
		}
	}
}

func TestAConnectionRequestThatCannotBeIsRefused(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{http.MethodPut, "/api/connections/anthropic", `{"key":"` + aKey + `"}`, http.StatusNotFound, "unknown_provider"},
		{http.MethodPut, "/api/connections/openai", `{"key":"` + aKey + `","extra":1}`, http.StatusBadRequest, "bad_request"},
		{http.MethodPut, "/api/connections/openai", `{"key":` + aKey + `}`, http.StatusBadRequest, "bad_request"},
		{http.MethodPut, "/api/connections/openai", `{"key":"` + strings.Repeat("k", 5000) + `"}`, http.StatusRequestEntityTooLarge, "too_large"},
		{http.MethodPost, "/api/connections/openai/check", "", http.StatusNotFound, "not_found"},
	} {
		status, m := r.send(alice, tc.method, tc.path, tc.body)
		if status != tc.status || m["error"] != tc.code {
			t.Errorf("%s %s: %d %v", tc.method, tc.path, status, m)
		}
		if msg, _ := m["message"].(string); strings.Contains(msg, "0123456789") {
			t.Errorf("%s %s: the message quotes the body", tc.method, tc.path)
		}
	}
	if len(r.conns.saved) != 0 {
		t.Errorf("saved %d keys", len(r.conns.saved))
	}
	if status, _ := r.send(0, http.MethodPut, "/api/connections/openai", `{"key":"`+aKey+`"}`); status != http.StatusUnauthorized {
		t.Errorf("nobody: %d", status)
	}
}

func TestTheSettingsChangeTheCurrencyAndTheLanguage(t *testing.T) {
	r := newRig(t)
	status, m := r.send(alice, http.MethodPatch, "/api/me", `{"currency":"UAH","language":"uk"}`)
	if status != http.StatusOK || m["currency"] != "UAH" || m["currency_chosen"] != true || m["language"] != "uk" {
		t.Fatalf("%d %v", status, m)
	}
	// A field left out stays; an empty one goes back to the default.
	status, m = r.send(alice, http.MethodPatch, "/api/me", `{"currency":""}`)
	if status != http.StatusOK || m["currency_chosen"] != false || m["language"] != "uk" {
		t.Fatalf("%d %v", status, m)
	}
	status, _ = r.send(alice, http.MethodPatch, "/api/me", `{"language":""}`)
	if status != http.StatusOK || len(r.store.cleared) != 1 {
		t.Fatalf("%d, cleared %v", status, r.store.cleared)
	}
	for _, body := range []string{`{"currency":"GBP"}`, `{"language":"xx"}`, `{"theme":"dark"}`} {
		if status, m := r.send(alice, http.MethodPatch, "/api/me", body); status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, status, m)
		}
	}
}

// The language picked on the site is said to be picked; withdrawn, it is
// "Automatic" again, whatever language core still resolves.
func TestTheSettingsSayWhetherTheLanguageWasPicked(t *testing.T) {
	r := newRig(t)
	if _, m := r.send(alice, http.MethodPatch, "/api/me", `{"language":"uk"}`); m["language_chosen"] != true {
		t.Fatalf("%v", m)
	}
	if _, m := r.send(alice, http.MethodPatch, "/api/me", `{"currency":"EUR"}`); m["language_chosen"] != true {
		t.Errorf("a field left out moved the language: %v", m)
	}
	if _, m := r.send(alice, http.MethodPatch, "/api/me", `{"language":""}`); m["language_chosen"] != false {
		t.Errorf("%v", m)
	}
}

func TestTheSettingsKeepTheDefaultModelsAndTheDM(t *testing.T) {
	r := newRig(t)
	r.store.offers["openrouter:test-image"] = image("openrouter:test-image", "openrouter")
	r.store.offers["openrouter:test-video"] = video("openrouter:test-video", "openrouter")

	status, m := r.send(alice, http.MethodPatch, "/api/me", `{"default_models":{"image":"test-image"}}`)
	if d, _ := m["default_models"].(map[string]any); status != http.StatusOK || d["image"] != "test-image" || d["video"] != nil {
		t.Fatalf("%d %v", status, m)
	}
	status, m = r.send(alice, http.MethodPatch, "/api/me", `{"default_models":{"video":"test-video"},"deliver_to_telegram":false}`)
	if d, _ := m["default_models"].(map[string]any); status != http.StatusOK || d["image"] != "test-image" || d["video"] != "test-video" || m["deliver_to_telegram"] != false {
		t.Fatalf("%d %v", status, m)
	}
	// An empty id goes back to framy's default; the other kind stays.
	status, m = r.send(alice, http.MethodPatch, "/api/me", `{"default_models":{"image":""}}`)
	if d, _ := m["default_models"].(map[string]any); status != http.StatusOK || d["image"] != nil || d["video"] != "test-video" || m["deliver_to_telegram"] != false {
		t.Fatalf("%d %v", status, m)
	}
	if m := jsonOf(t, r.do(alice, "GET", "/api/me", nil)); m["deliver_to_telegram"] != false {
		t.Errorf("GET forgot the DM: %v", m)
	}
	if st := r.store.settings[bob]; st != (db.Settings{}) {
		t.Errorf("bob's settings moved: %+v", st)
	}

	// Not a model of that kind, not a model at all, not a bool: refused, and
	// nothing of the request is kept.
	for _, body := range []string{
		`{"default_models":{"image":"test-video"}}`,
		`{"default_models":{"video":"test-image"}}`,
		`{"currency":"EUR","default_models":{"image":"openrouter:test-image"}}`,
		`{"default_models":{"audio":"test-image"}}`,
		`{"deliver_to_telegram":"no"}`,
	} {
		status, m := r.send(alice, http.MethodPatch, "/api/me", body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, status, m)
		}
	}
	if m := jsonOf(t, r.do(alice, "GET", "/api/me", nil)); m["currency_chosen"] != false {
		t.Errorf("a refused request kept its currency: %v", m)
	}

	// An unread catalog is framy's fault, not a refusal of the model.
	down := newRig(t)
	down.store.catalogErr = errors.New("connection reset")
	if status, m := down.send(alice, http.MethodPatch, "/api/me", `{"default_models":{"image":"test-image"}}`); status != http.StatusInternalServerError {
		t.Errorf("catalog down: %d %v", status, m)
	}
	// Sending the default back needs no catalog.
	if status, m := down.send(alice, http.MethodPatch, "/api/me", `{"default_models":{"image":""}}`); status != http.StatusOK {
		t.Errorf("catalog down, empty id: %d %v", status, m)
	}
}

func TestDeletingTheAccount(t *testing.T) {
	r := newRig(t)
	history(r)
	r.data.set(alice, State{ID: 14, Status: jobs.Done, Kind: catalog.Image, FinishedAt: ended(r.api.now())})
	status, _ := r.send(alice, http.MethodDelete, "/api/me", `{"confirm":true}`)
	if status != http.StatusNoContent {
		t.Fatalf("%d", status)
	}
	if len(r.files.all) != 1 || len(r.data.forgot) != 1 || len(r.store.cleared) != 1 || len(r.revoked) != 1 || r.revoked[0] != alice {
		t.Fatalf("files %v, forgot %v, language %v, revoked %v", r.files.all, r.data.forgot, r.store.cleared, r.revoked)
	}
	if got := idsOf(t, jsonOf(t, r.do(bob, http.MethodGet, "/api/jobs", nil))); len(got) != 1 {
		t.Errorf("bob's history %v", got)
	}
}

func TestAnAccountIsNotDeletedByAccidentOrMidRun(t *testing.T) {
	r := newRig(t)
	history(r) // job 14 is running
	for _, body := range []string{"", `{}`, `{"confirm":false}`} {
		if status, m := r.send(alice, http.MethodDelete, "/api/me", body); status != http.StatusBadRequest {
			t.Errorf("%q: %d %v", body, status, m)
		}
	}
	status, m := r.send(alice, http.MethodDelete, "/api/me", `{"confirm":true}`)
	if status != http.StatusConflict || m["error"] != "jobs_active" {
		t.Fatalf("%d %v", status, m)
	}
	if len(r.files.all) != 0 || len(r.data.forgot) != 0 || len(r.revoked) != 0 {
		t.Fatalf("files %v, forgot %v, revoked %v", r.files.all, r.data.forgot, r.revoked)
	}
}

func TestAnAccountWhoseFilesWouldNotGoIsKept(t *testing.T) {
	r := newRig(t)
	r.files.err = errors.New("r2 is down")
	status, m := r.send(alice, http.MethodDelete, "/api/me", `{"confirm":true}`)
	if status != http.StatusServiceUnavailable || m["error"] != "storage_unavailable" {
		t.Fatalf("%d %v", status, m)
	}
	if len(r.data.forgot) != 0 || len(r.revoked) != 0 {
		t.Fatalf("forgot %v, revoked %v: a second try has to be possible", r.data.forgot, r.revoked)
	}
}
