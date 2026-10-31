// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// fakePrefs is the API of a signed-in person for the settings: their keys and
// settings as the server would keep them. A pasted key passes the provider's
// check unless it says otherwise: "rejected", "forbidden" or "down" in it.
// Check on a kept key goes by its last four: "dead" is refused (the key turns
// invalid), "busy" gets no answer. The keys are made up and never real.
type fakePrefs struct {
	mu        sync.Mutex
	conns     map[string]map[string]any
	currency  string
	language  string
	puts      int              // keys sent to be checked
	patches   []map[string]any // PATCH /api/me bodies
	active    string           // what a deletion finds still going: "queued", "running" or ""
	storeFail int              // deletions the storage fails first
	deleted   bool
}

func (f *fakePrefs) me() map[string]any {
	list := []map[string]any{}
	for _, p := range []string{"openrouter", "openai", "gemini"} {
		if c := f.conns[p]; c != nil {
			list = append(list, c)
		}
	}
	return map[string]any{
		"user_id": 1, "name": "Ada", "language": f.language, "currency": f.currency, "currency_chosen": true, "connections": list,
		"free": map[string]any{
			"images": map[string]any{"used": 5, "limit": 5, "left": 0, "window_seconds": 86400, "frees_at": time.Now().Add(3 * time.Hour).UTC()},
			"videos": map[string]any{"used": 0, "limit": 1, "left": 1, "window_seconds": 604800},
		},
		"storage": map[string]any{"files": 12, "bytes": 31457280},
	}
}

func (f *fakePrefs) connect(p, last4, status string) {
	f.conns[p] = map[string]any{"id": "c-" + p, "provider": p, "last4": last4, "status": status,
		"created_at": time.Now().UTC(), "updated_at": time.Now().UTC()}
}

func (f *fakePrefs) mount(srv *Server) {
	f.conns = map[string]map[string]any{}
	f.currency, f.language = "USD", "en"
	signIn := func(w http.ResponseWriter) bool {
		if f.deleted {
			writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in"})
			return false
		}
		return true
	}
	srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if signIn(w) {
			writeTestJSON(w, http.StatusOK, f.me())
		}
	})
	srv.HandleFunc("PATCH /api/me", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.patches = append(f.patches, b)
		if c, ok := b["currency"].(string); ok {
			f.currency = c
		}
		if l, ok := b["language"].(string); ok {
			if l == "" {
				l = "en"
			}
			f.language = l
		}
		writeTestJSON(w, http.StatusOK, f.me())
	})
	srv.HandleFunc("DELETE /api/me", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Confirm bool `json:"confirm"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case !b.Confirm:
			writeTestJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "field": "confirm"})
		case f.active != "":
			writeTestJSON(w, http.StatusConflict, map[string]any{"error": "jobs_active"})
		case f.storeFail > 0:
			f.storeFail--
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "storage_unavailable"})
		default:
			f.deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv.HandleFunc("PUT /api/connections/{p}", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		p := r.PathValue("p")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.puts++
		switch {
		case strings.Contains(b.Key, "rejected"):
			writeTestJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "key_rejected", "provider": p})
		case strings.Contains(b.Key, "forbidden"):
			writeTestJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "key_forbidden", "provider": p})
		case strings.Contains(b.Key, "down"):
			w.Header().Set("Retry-After", "2")
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "check_unavailable", "provider": p})
		default:
			f.connect(p, b.Key[len(b.Key)-4:], "valid")
			writeTestJSON(w, http.StatusOK, f.conns[p])
		}
	})
	srv.HandleFunc("POST /api/connections/{p}/check", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("p")
		f.mu.Lock()
		defer f.mu.Unlock()
		c := f.conns[p]
		switch {
		case c == nil:
			writeTestJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "provider": p})
		case c["last4"] == "busy":
			w.Header().Set("Retry-After", "2")
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "check_unavailable", "provider": p})
		case c["last4"] == "dead":
			c["status"] = "invalid"
			writeTestJSON(w, http.StatusOK, c)
		default:
			if c["status"] != "verified" {
				c["status"] = "valid"
			}
			writeTestJSON(w, http.StatusOK, c)
		}
	})
	srv.HandleFunc("DELETE /api/connections/{p}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.conns, r.PathValue("p"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		jobs := []map[string]any{{"id": 6, "status": "done"}}
		if f.active != "" {
			jobs = append(jobs, map[string]any{"id": 7, "status": f.active})
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	})
	srv.HandleFunc("POST /api/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.active == "queued" {
			f.active = ""
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"id": 7, "status": "cancelled"})
	})
	// Any run is refused for a key the person does not have.
	srv.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, http.StatusPaymentRequired, map[string]any{"error": "no_connection", "provider": "openai"})
	})
	// OpenRouter's consent is given at once: the callback saved a key.
	srv.HandleFunc("GET /oauth/openrouter/start", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.connect("openrouter", "or42", "valid")
		f.mu.Unlock()
		http.Redirect(w, r, "/?connect=openrouter&result=connected", http.StatusSeeOther)
	})
	srv.HandleFunc("POST /auth/miniapp", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, http.StatusOK, map[string]any{"token": "test-token", "expires_in": 3600})
	})
}

func (f *fakePrefs) with(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }

const prefsHelpers = `
const sheet = () => document.querySelector("#prefs");
const rowOf = (p) => document.querySelector('#prefsBody [data-p="' + p + '"]');
const rowText = (p) => { const r = rowOf(p); return r ? { sub: r.querySelector(".l > span").textContent,
  btns: [...r.querySelectorAll(":scope > button")].map((b) => b.textContent.trim()), bad: r.classList.contains("bad") } : null; };
const form = (p) => document.querySelector('#prefsBody [data-k="pk-' + p + '"]');
const note = (k) => document.querySelector('#prefsBody [data-k="' + k + '"]')?.textContent || "";
const btn = (root, label) => [...(root || document).querySelectorAll("button")].find((b) => b.textContent.trim() === label);
async function until(f, n = 80) { for (let i = 0; i < n; i++) { const v = f(); if (v) return v; await wait(100); } return null; }
async function typeKey(p, v) {
  const i = form(p).querySelector("input");
  i.value = v; i.dispatchEvent(new Event("input"));
  form(p).requestSubmit();
  await wait(50);
}
const kerr = (p) => form(p)?.querySelector(".kerr")?.textContent || "";
const toastText = () => document.querySelector(".toast")?.textContent || "";
async function yes() { const b = await until(() => !document.querySelector("#ask").hidden && document.querySelector("#ask .yes")); const q = document.querySelector("#ask h2").textContent; b.click(); await wait(100); return q; }
`

// prefs runs a step of the settings in the page and returns what it found.
func prefs(t *testing.T, ctx context.Context, body string) map[string]any {
	t.Helper()
	return js[map[string]any](t, ctx, pageHelpers+prefsHelpers+body)
}

func TestBrowserPrefs(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)

	// The person's picture opens the settings: three keys, none connected.
	s := prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && rowOf("gemini"));
	await wait(500);
	return { or: rowText("openrouter"), oa: rowText("openai"), ge: rowText("gemini"), groups: [...document.querySelectorAll("#prefs .group")].map((g) => g.textContent),
	  out: !!btn(sheet(), "Sign out"), store: note("p-store"), free: note("p-free-image") };`)
	if fmt.Sprint(s["or"]) != "map[bad:false btns:[Connect] sub:Sign in through OpenRouter. Have a key? Paste it]" ||
		fmt.Sprint(s["oa"]) != "map[bad:false btns:[Key] sub:Paste a key, framy checks it]" || fmt.Sprint(s["groups"]) != "[Model keys Free Currency Language Account]" ||
		s["out"] != true || s["store"] != "Files stored: 1230 MB · pictures, clips and uploads" || !strings.HasPrefix(s["free"].(string), "Pictures0 left of 5 a day · next one in 3 hours") {
		t.Errorf("settings: %v", s)
	}

	// A paste that cannot be a key is refused before anything is sent.
	s = prefs(t, ctx, `
	btn(rowOf("openai"), "Key").click();
	await until(() => form("openai"));
	await wait(300);
	const focused = document.activeElement === form("openai").querySelector("input"), hint = form("openai").querySelector(".khint").textContent;
	await typeKey("openai", "sk short");
	await until(() => kerr("openai"));
	return { focused, hint, err: kerr("openai"), invalid: form("openai").querySelector("input").getAttribute("aria-invalid") };`)
	if s["focused"] != true || s["hint"] != "Keys are made at platform.openai.com/api-keys. Only the last 4 characters will show." ||
		s["err"] != "This does not look like a key from OpenAI. Copy it whole." || s["invalid"] != "true" {
		t.Errorf("bad key: %v", s)
	}
	f.with(func() {
		if f.puts != 0 {
			t.Errorf("a paste shaped like no key was sent %d times", f.puts)
		}
	})

	// What the provider's check refused, each in its own words; the paste stays to be fixed.
	for key, want := range map[string]string{
		"sk-test-rejected-0000000000": "OpenAI did not accept the key: it is wrong or revoked.",
		"sk-test-forbidden-000000000": "OpenAI accepted the key but does not let it list the models. Allow that in the key's settings.",
	} {
		s = prefs(t, ctx, `
	await typeKey("openai", `+jsString(key)+`);
	await until(() => kerr("openai") && !form("openai").querySelector("[data-wait]").disabled);
	return { err: kerr("openai"), kept: form("openai").querySelector("input").value };`)
		if s["err"] != want || s["kept"] != key {
			t.Errorf("%s: %v", key, s)
		}
	}
	// No answer: the button counts down the Retry-After, then can be pressed again.
	s = prefs(t, ctx, `
	await typeKey("openai", "sk-test-down-000000000000");
	await until(() => /did not answer/.test(kerr("openai")));
	const b = form("openai").querySelector("[data-wait]"), during = [b.textContent, b.disabled];
	await until(() => !form("openai").querySelector("[data-wait]").disabled, 40);
	const a = form("openai").querySelector("[data-wait]");
	return { err: kerr("openai"), during, after: [a.textContent, a.disabled] };`)
	if s["err"] != "OpenAI did not answer. The key is not saved; try again in 10 seconds." || fmt.Sprint(s["during"]) != "[In 2 s true]" || fmt.Sprint(s["after"]) != "[Check false]" {
		t.Errorf("no answer: %v", s)
	}

	// A key that passes: the row says so with its last four, the form goes,
	// and the composer runs on it.
	s = prefs(t, ctx, `
	await typeKey("openai", "sk-test-good-00000000abcd");
	await until(() => !form("openai"));
	await wait(400);
	const r = rowText("openai");
	btn(rowOf("openai"), "Check").click();
	await until(() => rowText("openai").btns[0] === "Check" && !rowOf("openai").querySelector("[data-wait]").disabled);
	return { r, after: rowText("openai") };`)
	if fmt.Sprint(s["r"]) != "map[bad:false btns:[Check Disconnect] sub:Key …abcd · accepted]" || fmt.Sprint(s["after"]) != fmt.Sprint(s["r"]) {
		t.Errorf("connected: %v", s)
	}

	// Disconnecting says where to revoke the key too.
	s = prefs(t, ctx, `
	btn(rowOf("openai"), "Disconnect").click();
	await until(() => note("pr-openai"));
	return { note: note("pr-openai"), row: rowText("openai") };`)
	if s["note"] != "The key is gone from framy. Revoke it at OpenAI too: platform.openai.com/api-keys." || fmt.Sprint(s["row"]) != "map[bad:false btns:[Key] sub:Paste a key, framy checks it]" {
		t.Errorf("disconnected: %v", s)
	}

	// A kept key the provider refuses on Check turns invalid: it is pasted again.
	s = prefs(t, ctx, `
	btn(rowOf("gemini"), "Key").click();
	await until(() => form("gemini"));
	await typeKey("gemini", "AIza-test-good-000000000000dead");
	await until(() => !form("gemini"));
	btn(rowOf("gemini"), "Check").click();
	await until(() => rowText("gemini").bad);
	const r = rowText("gemini");
	btn(rowOf("gemini"), "Paste again").click();
	await until(() => form("gemini"));
	await wait(300);
	return { r, focused: document.activeElement === form("gemini").querySelector("input") };`)
	if fmt.Sprint(s["r"]) != "map[bad:true btns:[Paste again Disconnect] sub:Gemini no longer accepts the key]" || s["focused"] != true {
		t.Errorf("invalid: %v", s)
	}
	// Check with no answer counts down on the row.
	s = prefs(t, ctx, `
	await typeKey("gemini", "AIza-test-good-000000000000busy");
	await until(() => !form("gemini"));
	btn(rowOf("gemini"), "Check").click();
	await until(() => rowText("gemini").bad);
	return rowText("gemini");`)
	if fmt.Sprint(s) != "map[bad:true btns:[In 2 s Disconnect] sub:Gemini did not answer. Try again in 10 seconds.]" {
		t.Errorf("check without an answer: %v", s)
	}

	// The currency goes to the server, and the prices follow.
	s = prefs(t, ctx, `
	sheet().querySelector('[data-seg="cur"] [data-v="EUR"]').click();
	await until(() => sheet().querySelector('[data-seg="cur"] [data-v="EUR"]').getAttribute("aria-checked") === "true");
	await wait(300);
	return { on: sheet().querySelector('[data-seg="cur"] [aria-checked="true"]').dataset.v, names: [...sheet().querySelectorAll('[data-seg="cur"] small')].map((x) => x.textContent) };`)
	f.with(func() {
		if len(f.patches) != 1 || f.patches[0]["currency"] != "EUR" || f.currency != "EUR" {
			t.Errorf("currency sent: %v", f.patches)
		}
	})
	if s["on"] != "EUR" || !strings.Contains(fmt.Sprint(s["names"]), "Euro") {
		t.Errorf("currency: %v", s)
	}

	// Connect goes to OpenRouter and back: the address loses the result, the
	// settings open on the new key.
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[any](`document.querySelector('#prefsBody [data-p="openrouter"] button.primary').click()`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	ready(t, ctx)
	s = prefs(t, ctx, `
	await until(() => !sheet().hidden && rowText("openrouter")?.sub.startsWith("Key"));
	return { url: location.pathname + location.search, row: rowText("openrouter"), toast: toastText() };`)
	if s["url"] != "/" || fmt.Sprint(s["row"]) != "map[bad:false btns:[Check Disconnect] sub:Key …or42 · accepted]" || s["toast"] != "OpenRouter is connected." {
		t.Errorf("connect: %v", s)
	}

	// A connect that ended without a key says why under OpenRouter.
	f.with(func() { delete(f.conns, "openrouter") })
	if err := chromedp.Do(ctx, chromedp.Navigate(ts.URL+"/?connect=openrouter&result=expired")); err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	s = prefs(t, ctx, `
	await until(() => !sheet().hidden && note("ps-openrouter"));
	return { url: location.search, said: note("ps-openrouter") };`)
	if s["url"] != "" || s["said"] != "The connect expired: more than 10 minutes passed, or it was started again. Press Connect once more." {
		t.Errorf("expired: %v", s)
	}

	// The composer without a key leads to it: the button says Connect and opens the settings there.
	s = prefs(t, ctx, `
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	await until(() => sheet().hidden);
	document.querySelector('#how [data-seg="pay"] [data-v="openrouter"]').click();
	await until(() => /Connect/.test(document.querySelector("#go").textContent));
	const go = [document.querySelector("#go").firstChild.nextSibling.textContent, document.querySelector("#go").disabled];
	document.querySelector("#go").click();
	await until(() => !sheet().hidden);
	await wait(400);
	return { go, focus: document.activeElement.textContent.trim(), inRow: !!document.activeElement.closest('[data-p="openrouter"]') };`)
	if fmt.Sprint(s["go"]) != "[Connect OpenRouter false]" || s["focus"] != "Connect" || s["inRow"] != true {
		t.Errorf("composer without a key: %v", s)
	}

	// OpenRouter's key can be pasted too; a run refused for a missing key has
	// a button to that key's form.
	s = js[map[string]any](t, ctx, pageHelpers+prefsHelpers+runHelpers+`
	btn(rowOf("openrouter"), "Paste it").click();
	await until(() => form("openrouter"));
	await typeKey("openrouter", "sk-or-v1-test-good-000000or42");
	await until(() => rowText("openrouter")?.sub.startsWith("Key"));
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	await until(() => sheet().hidden);
	await wait(300);
	const st = await press("draw a fox", (s) => s.acts.length);
	btn(document.querySelector("#stage"), "Connect OpenAI")?.click();
	await until(() => !sheet().hidden && form("openai"));
	await wait(400);
	return { acts: st.acts, focused: document.activeElement === form("openai")?.querySelector("input") };`)
	if fmt.Sprint(s["acts"]) != "[Connect OpenAI Edit the prompt Make another]" || s["focused"] != true {
		t.Errorf("refused for no key: %v", s)
	}

	// Deleting the account: a waiting run is cancelled first, a storage that
	// fails is pressed again without a second question, and then nobody is
	// signed in.
	f.with(func() { f.active, f.storeFail = "queued", 1 })
	s = prefs(t, ctx, `
	btn(sheet(), "Delete").click();
	const q1 = await yes();
	const q2 = await yes();
	await until(() => note("p-del").startsWith("Delete the accountNot every file"));
	const failed = note("p-del");
	btn(sheet(), "Again").click();
	await until(() => sheet().hidden && document.querySelector(".signin"));
	return { q1, q2, failed, asked: !document.querySelector("#ask").hidden, toast: toastText(), top: document.querySelector("#top").textContent };`)
	if s["q1"] != "Delete the account?" || s["q2"] != "Runs finish first" ||
		s["failed"] != "Delete the accountNot every file was deleted, the account is still here. Press again and the deletion goes on.Again" ||
		s["asked"] != false || s["toast"] != "The account is deleted, and every session is closed." || s["top"] != "framySign in with Telegram" {
		t.Errorf("delete: %v", s)
	}
	f.with(func() {
		if !f.deleted || f.active != "" {
			t.Errorf("deleted %v, still going %q", f.deleted, f.active)
		}
	})
	noErrors(t, ctx)
}

// TestBrowserPrefsLanguage checks that a new language is the server's and
// the page's: it reloads in it, with the settings open again.
func TestBrowserPrefsLanguage(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	prefs(t, ctx, `
	document.querySelector("#prompt").value = "a fox"; document.querySelector("#prompt").dispatchEvent(new Event("input"));
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden);
	const sel = sheet().querySelector("select.pick");
	sel.value = "ru"; sel.dispatchEvent(new Event("change"));
	return {};`)
	time.Sleep(800 * time.Millisecond)
	ready(t, ctx)
	s := prefs(t, ctx, `
	await until(() => !sheet().hidden);
	await wait(300);
	return { lang: document.documentElement.lang, title: sheet().querySelector("h3").textContent, sel: sheet().querySelector("select.pick").value,
	  prompt: document.querySelector("#prompt").value, opts: sheet().querySelector("select.pick").options.length };`)
	f.with(func() {
		if len(f.patches) != 1 || f.patches[0]["language"] != "ru" {
			t.Errorf("language sent: %v", f.patches)
		}
	})
	if s["lang"] != "ru" || s["title"] != "Настройки" || s["sel"] != "ru" || s["prompt"] != "a fox" || s["opts"] != float64(17) {
		t.Errorf("language: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserPrefsPhone checks the settings fit a phone with a key's form
// open and its refusal shown, and that the Mini App connects OpenRouter in
// the browser and finds the key when Telegram is back.
func TestBrowserPrefsPhone(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	for _, size := range [][2]int64{{320, 568}, {375, 812}} {
		ctx := browser(t, ts.URL, size[0], size[1], true)
		s := prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && rowOf("gemini"));
	btn(rowOf("openai"), "Key").click();
	await until(() => form("openai"));
	await typeKey("openai", "sk-test-forbidden-000000000");
	await until(() => kerr("openai"));
	await wait(500);
	const body = document.querySelector("#prefsBody"), w = body.getBoundingClientRect();
	const out = [...body.querySelectorAll("button, input, select, .l, .kerr")].filter(vis).filter((el) => { const r = el.getBoundingClientRect(); return r.left < w.left - 0.5 || r.right > w.right + 0.5; })
	  .map((el) => el.textContent.trim().slice(0, 30) || el.tagName);
	return { out, scroll: body.scrollWidth - body.clientWidth, page: document.documentElement.scrollWidth - innerWidth };`)
		if len(s["out"].([]any)) > 0 || s["scroll"].(float64) > 0 || s["page"].(float64) > 0 {
			t.Errorf("%dx%d: %v", size[0], size[1], s)
		}
		noErrors(t, ctx)
	}

	ctx := miniBrowser(t, ts.URL)
	s := prefs(t, ctx, `
	await until(() => window.__tg.settings);
	const shown = window.__tg.settingsShown;
	window.__tg.settings();
	await until(() => !sheet().hidden && rowOf("openrouter"));
	const before = rowText("openrouter"), out = !!btn(sheet(), "Sign out");
	btn(rowOf("openrouter"), "Connect").click();
	await until(() => rowText("openrouter").btns[0] === "Open again");
	return { shown, before, out, away: rowText("openrouter"), links: window.__tg.links.map((u) => new URL(u).pathname) };`)
	if s["shown"] != true || s["out"] != false || fmt.Sprint(s["before"]) != "map[bad:false btns:[Connect] sub:Opens in the browser. Have a key? Paste it]" ||
		fmt.Sprint(s["away"]) != "map[bad:false btns:[Open again] sub:OpenRouter is open in the browser. Sign in there, allow access and come back here.]" ||
		fmt.Sprint(s["links"]) != "[/oauth/openrouter/start]" {
		t.Errorf("mini app connect: %v", s)
	}
	// Back in Telegram with the key made in the browser.
	f.with(func() { f.connect("openrouter", "mini", "valid") })
	s = prefs(t, ctx, `
	document.dispatchEvent(new Event("visibilitychange"));
	await until(() => rowText("openrouter")?.sub.startsWith("Key"));
	return { row: rowText("openrouter"), toast: toastText() };`)
	if fmt.Sprint(s["row"]) != "map[bad:false btns:[Check Disconnect] sub:Key …mini · accepted]" || s["toast"] != "OpenRouter is connected." {
		t.Errorf("mini app back: %v", s)
	}
	noErrors(t, ctx)
}
