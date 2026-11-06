// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// fakePrefs is the API of a signed-in person for the settings: their keys and
// settings as the server would keep them. A pasted key passes the provider's
// check unless it says otherwise: "rejected", "forbidden", "region" or
// "down" in it.
// Check on a kept key goes by its last four: "dead" is refused (the key turns
// invalid), "busy" gets no answer. The keys are made up and never real.
type fakePrefs struct {
	mu        sync.Mutex
	conns     map[string]map[string]any
	currency  string // the person's pick, "" for Auto, which gives UAH
	language  string
	langPick  bool              // the language is the person's own pick
	defaults  map[string]string // default models by kind
	deliver   bool              // the site's results go to the DM
	puts      int               // keys sent to be checked
	patches   []map[string]any  // PATCH /api/me bodies
	active    string            // what a deletion finds still going: "queued", "running" or ""
	kind      string            // and of what kind: "image" or "video"
	storeFail int               // deletions the storage fails first
	making    int               // disconnects refused first: a video on the key is still being made
	deleted   bool
	outFail   int  // sign-outs the session store fails first
	out       bool // signed out: the cookie is gone
	// slowMe: GET /api/me answers this late with the account as it was, and
	// a PATCH sent with it is kept only after the GET has read it.
	slowMe time.Duration
	// meDown: GET /api/me answers this many times 503 unavailable first, as
	// when the sessions cannot be read for a moment.
	meDown int
	// outAll counts POST /auth/logout/all, which answers 503 the first
	// outAllDown times; after one that went, nobody is signed in.
	outAll, outAllDown int
	allOut             bool
	// photo is the person's photo_url, none when empty.
	photo string
	// launchOld: the Mini App's launch data is past its hour. Its token is
	// refused, and POST /auth/miniapp answers init_data_expired.
	launchOld bool
	// patchLate: PATCH /api/me answers this late; patching counts the ones
	// being answered at once, mostPatching the most there were.
	patchLate              time.Duration
	patching, mostPatching int
}

func (f *fakePrefs) me() map[string]any {
	list := []map[string]any{}
	for _, p := range []string{"openrouter", "openai", "gemini"} {
		if c := f.conns[p]; c != nil {
			list = append(list, c)
		}
	}
	cur := f.currency
	if cur == "" {
		cur = "UAH"
	}
	return map[string]any{
		"user_id": 1, "name": "Ada", "language": f.language, "language_chosen": f.langPick,
		"currency": cur, "currency_chosen": f.currency != "", "currency_auto": "UAH", "connections": list,
		"default_models": maps.Clone(f.defaults), "deliver_to_telegram": f.deliver,
		"free": map[string]any{
			"images": map[string]any{"used": 5, "limit": 5, "left": 0, "window_seconds": 86400, "frees_at": time.Now().Add(3 * time.Hour).UTC()},
			"videos": map[string]any{"used": 0, "limit": 1, "left": 1, "window_seconds": 604800},
		},
		"storage":   map[string]any{"files": 12, "bytes": 31457280},
		"photo_url": f.photo,
	}
}

func (f *fakePrefs) connect(p, last4, status string) {
	f.conns[p] = map[string]any{"id": "c-" + p, "provider": p, "last4": last4, "status": status,
		"created_at": time.Now().UTC(), "updated_at": time.Now().UTC()}
}

func (f *fakePrefs) mount(srv *Server) {
	f.conns = map[string]map[string]any{}
	f.currency, f.language, f.defaults, f.deliver = "", "en", map[string]string{}, true
	signIn := func(w http.ResponseWriter) bool {
		if f.deleted || f.out || f.allOut || f.launchOld {
			writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign_in"})
			return false
		}
		return true
	}
	srv.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		if f.meDown > 0 {
			f.meDown--
			f.mu.Unlock()
			w.Header().Set("Retry-After", "5")
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
			return
		}
		if !signIn(w) {
			f.mu.Unlock()
			return
		}
		me, late := f.me(), f.slowMe
		f.mu.Unlock()
		time.Sleep(late)
		writeTestJSON(w, http.StatusOK, me)
	})
	srv.HandleFunc("PATCH /api/me", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		late := f.slowMe
		f.patching++
		f.mostPatching = max(f.mostPatching, f.patching)
		patchLate := f.patchLate
		f.mu.Unlock()
		defer f.with(func() { f.patching-- })
		time.Sleep(patchLate)
		if late > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.patches = append(f.patches, b)
		if c, ok := b["currency"].(string); ok {
			f.currency = c
		}
		if l, ok := b["language"].(string); ok {
			f.langPick = l != ""
			if l == "" {
				l = "en"
			}
			f.language = l
		}
		if d, ok := b["default_models"].(map[string]any); ok {
			for k, v := range d {
				if id, _ := v.(string); id == "" {
					delete(f.defaults, k)
				} else {
					f.defaults[k] = id
				}
			}
		}
		if on, ok := b["deliver_to_telegram"].(bool); ok {
			f.deliver = on
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
		case strings.Contains(b.Key, "region"):
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "key_region", "provider": p})
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
		if f.making > 0 {
			f.making--
			writeTestJSON(w, http.StatusConflict, map[string]any{"error": "key_in_use", "provider": r.PathValue("p")})
			return
		}
		delete(f.conns, r.PathValue("p"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		jobs := []map[string]any{{"id": 6, "status": "done"}}
		if f.active != "" {
			jobs = append(jobs, map[string]any{"id": 7, "status": f.active, "kind": cmp.Or(f.kind, "image")})
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
	srv.HandleFunc("POST /auth/logout/all", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.outAll++
		if f.outAllDown > 0 {
			f.outAllDown--
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
			return
		}
		f.allOut = true
		w.WriteHeader(http.StatusNoContent)
	})
	// As internal/auth: the session store failing keeps the session and its cookie.
	srv.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.outFail > 0 {
			f.outFail--
			writeTestJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "unavailable"})
			return
		}
		f.out = true
		w.WriteHeader(http.StatusNoContent)
	})
	srv.HandleFunc("POST /auth/miniapp", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		old := f.launchOld
		f.mu.Unlock()
		if old {
			writeTestJSON(w, http.StatusUnauthorized, map[string]any{"error": "init_data_expired"})
			return
		}
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
async function typeKey(p, v) {
  const i = form(p).querySelector("input");
  i.value = v; i.dispatchEvent(new Event("input"));
  form(p).requestSubmit();
  await wait(50);
}
const defRow = (k) => { const r = document.querySelector('#prefsBody [data-k="p-def-' + k + '"]'); return r ? { title: r.querySelector(".l b").textContent, sub: r.querySelector(".l > span").textContent,
  btns: [...r.querySelectorAll(":scope > button")].map((b) => b.textContent.trim()) } : null; };
const kerr = (p) => form(p)?.querySelector(".kerr")?.textContent || "";
const toastText = () => document.querySelector(".toast")?.textContent || "";
async function yes() { const b = await until(() => !document.querySelector("#ask").hidden && document.querySelector("#ask .yes")); const q = document.querySelector("#ask h2").textContent; b.click(); await wait(100); return q; }
// The question and what it says under it, before the press.
async function yesSaid() { await until(() => !document.querySelector("#ask").hidden && document.querySelector("#ask .yes")); const p = document.querySelector("#ask p")?.textContent || ""; return (await yes()) + ": " + p; }
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
	  out: !!btn(sheet(), "Sign out"), who: note("p-out"), store: note("p-store"), free: note("p-free-image") };`)
	// Each says what the key needs to make anything, which the check on save
	// cannot see: credits, or a project with billing.
	if fmt.Sprint(s["or"]) != "map[bad:false btns:[Connect] sub:Sign in through OpenRouter and pay with its credits. Have a key? Paste it]" ||
		fmt.Sprint(s["oa"]) != "map[bad:false btns:[Key] sub:Needs credits on the account]" ||
		fmt.Sprint(s["ge"]) != "map[bad:false btns:[Key] sub:Needs a project with billing]" ||
		fmt.Sprint(s["groups"]) != "[Model keys Free Default models Telegram Currency Language Account]" ||
		s["out"] != true || s["who"] != "AAdaA sign-in lasts 30 days after your last visitSign outSign out everywhere" ||
		s["store"] != "Files stored: 1230 MB · images, videos and uploads" || !strings.HasPrefix(s["free"].(string), "Images0 of 5 left in any 24 hours · next one in 3 hours") {
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
		"sk-test-forbidden-000000000": "OpenAI accepted the key but does not let it list the models. Allow that in the key’s settings.",
		"sk-test-region-00000000000":  "OpenAI does not serve the country framy’s server is in, so framy cannot check or use the key.",
	} {
		s = prefs(t, ctx, `
	await typeKey("openai", `+jsString(key)+`);
	await until(() => kerr("openai") && !form("openai").querySelector("[data-wait]").disabled);
	return { err: kerr("openai"), kept: form("openai").querySelector("input").value, markup: document.documentElement.outerHTML.includes(`+jsString(key)+`) };`)
		if s["err"] != want || s["kept"] != key || s["markup"] != false {
			t.Errorf("%s: %v", key, s)
		}
	}
	// No answer: the button counts down the Retry-After, then can be pressed again.
	s = prefs(t, ctx, `
	await typeKey("openai", "sk-test-down-000000000000");
	await until(() => /did not answer/.test(kerr("openai")));
	const b = form("openai").querySelector("[data-wait]"), during = [b.textContent, b.disabled];
	await until(() => !form("openai").querySelector("[data-wait]").disabled, 4000);
	const a = form("openai").querySelector("[data-wait]");
	return { err: kerr("openai"), during, after: [a.textContent, a.disabled] };`)
	if s["err"] != "OpenAI did not answer. The key is not saved; try again in 2 seconds." || fmt.Sprint(s["during"]) != "[In 2 s true]" || fmt.Sprint(s["after"]) != "[Check false]" {
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

	// A key a video is still being made on stays, and the toast says why.
	f.with(func() { f.making = 1 })
	s = prefs(t, ctx, `
	btn(rowOf("openai"), "Disconnect").click();
	await until(() => toastText());
	return { toast: toastText(), row: rowText("openai") };`)
	if s["toast"] != "OpenAI is still making a video on this key, and framy needs the key to fetch it. Disconnect it once the video is ready." ||
		fmt.Sprint(s["row"]) != "map[bad:false btns:[Check Disconnect] sub:Key …abcd · accepted]" {
		t.Errorf("a key a video needs: %v", s)
	}

	// Disconnecting says where to revoke the key too.
	s = prefs(t, ctx, `
	btn(rowOf("openai"), "Disconnect").click();
	await until(() => note("pr-openai"));
	return { note: note("pr-openai"), row: rowText("openai") };`)
	if s["note"] != "The key is gone from framy. Revoke it at OpenAI too: platform.openai.com/api-keys." || fmt.Sprint(s["row"]) != "map[bad:false btns:[Key] sub:Needs credits on the account]" {
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
	if fmt.Sprint(s["r"]) != "map[bad:true btns:[Paste again Disconnect] sub:AI Studio no longer accepts the key]" || s["focused"] != true {
		t.Errorf("invalid: %v", s)
	}
	// Check with no answer counts down on the row.
	s = prefs(t, ctx, `
	await typeKey("gemini", "AIza-test-good-000000000000busy");
	await until(() => !form("gemini"));
	btn(rowOf("gemini"), "Check").click();
	await until(() => rowText("gemini").bad);
	return rowText("gemini");`)
	if fmt.Sprint(s) != "map[bad:true btns:[In 2 s Disconnect] sub:AI Studio did not answer. Try again in 2 seconds.]" {
		t.Errorf("check without an answer: %v", s)
	}

	// The currency goes to the server, and the prices follow; Auto, which
	// names what it gives, goes back to the currency of the country.
	s = prefs(t, ctx, `
	const on = () => sheet().querySelector('[data-seg="cur"] [aria-checked="true"]').dataset.v;
	const first = on();
	sheet().querySelector('[data-seg="cur"] [data-v="EUR"]').click();
	await until(() => on() === "EUR");
	await wait(300);
	const names = [...sheet().querySelectorAll('[data-seg="cur"] small')].map((x) => x.textContent);
	sheet().querySelector('[data-seg="cur"] [data-v="auto"]').click();
	await until(() => on() === "auto");
	await wait(300);
	return { first, names, back: on() };`)
	f.with(func() {
		if len(f.patches) != 2 || f.patches[0]["currency"] != "EUR" || f.patches[1]["currency"] != "" || f.currency != "" {
			t.Errorf("currency sent: %v", f.patches)
		}
	})
	if s["first"] != "auto" || s["back"] != "auto" || !strings.Contains(fmt.Sprint(s["names"]), "Euro") || !strings.HasPrefix(fmt.Sprint(s["names"]), "[UAH ") {
		t.Errorf("currency: %v", s)
	}

	// Connect goes to OpenRouter and back: the address loses the result, the
	// settings open on the new key.
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[any](leave+`document.querySelector('#prefsBody [data-p="openrouter"] button.primary').click()`)); err != nil {
		t.Fatal(err)
	}
	reloaded(t, ctx)
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
	if s["url"] != "" || s["said"] != "This attempt to connect expired: more than 10 minutes passed, or a newer one was started. Press Connect once more." {
		t.Errorf("expired: %v", s)
	}

	// A refusal in Telegram opens the Mini App with ?settings=<provider>: the
	// settings open at that key, and the address forgets it. A provider the
	// site does not know opens them at the top.
	for _, tc := range []struct{ query, focus string }{{"openrouter", "Connect"}, {"bogus", ""}} {
		if err := chromedp.Do(ctx, chromedp.Navigate(ts.URL+"/?settings="+tc.query)); err != nil {
			t.Fatal(err)
		}
		ready(t, ctx)
		s = prefs(t, ctx, `
	await until(() => !sheet().hidden);
	await wait(400);
	const row = document.activeElement.closest("[data-p]");
	return { url: location.search, focus: row ? row.dataset.p + ":" + document.activeElement.textContent.trim() : "" };`)
		want := ""
		if tc.focus != "" {
			want = tc.query + ":" + tc.focus
		}
		if s["url"] != "" || s["focus"] != want {
			t.Errorf("settings=%s: %v", tc.query, s)
		}
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

	// A started video holds the deletion up to the run's hour, and the
	// dialog says so rather than "a few minutes".
	f.with(func() { f.active, f.kind = "running", "video" })
	s = prefs(t, ctx, `
	btn(sheet(), "Delete").click();
	const q1 = await yes();
	const q2 = await yesSaid();
	await until(() => document.querySelector("#ask").hidden);
	return { q1, q2 };`)
	if s["q1"] != "Delete the account?" || s["q2"] != "Runs finish first: A started video can take up to an hour. Then the account can be deleted." {
		t.Errorf("delete while a video runs: %v", s)
	}
	f.with(func() {
		if f.deleted {
			t.Error("deleted while a video was running")
		}
	})

	// Deleting the account: a waiting run is cancelled first, a storage that
	// fails is pressed again without a second question, and then nobody is
	// signed in.
	f.with(func() { f.active, f.kind, f.storeFail = "queued", "image", 1 })
	s = prefs(t, ctx, `
	btn(sheet(), "Delete").click();
	const q1 = await yes();
	const q2 = await yesSaid();
	await until(() => note("p-del").startsWith("Delete the accountNot every file"));
	const failed = note("p-del");
	btn(sheet(), "Again").click();
	await until(() => sheet().hidden && document.querySelector(".signin"));
	const top = [...document.querySelectorAll("#top .logo span, #top .signin > span")].filter(vis).map((e) => e.textContent).join(" ");
	return { q1, q2, failed, asked: !document.querySelector("#ask").hidden, toast: toastText(), top };`)
	if s["q1"] != "Delete the account?" || s["q2"] != "Runs finish first: Some runs have not started yet and can be canceled. Then the account can be deleted." ||
		s["failed"] != "Delete the accountNot every file was deleted, so the account is still here. Press again to finish deleting.Again" ||
		s["asked"] != false || s["toast"] != "The account is deleted, and every session is closed." || s["top"] != "framy Sign in with Telegram" {
		t.Errorf("delete: %v", s)
	}
	f.with(func() {
		if !f.deleted || f.active != "" {
			t.Errorf("deleted %v, still going %q", f.deleted, f.active)
		}
	})
	noErrors(t, ctx)
}

// The photo of the last sign-in that no longer loads, its link outlived by
// a session kept going for months, gives way to the initial of the name.
func TestBrowserAPhotoThatDoesNotLoadIsTheInitial(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	f.photo = ts.URL + "/no-such-photo.jpg"
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := prefs(t, ctx, `
	await until(() => document.querySelector("#meBtn span.av"));
	const av = document.querySelector("#meBtn .av");
	return { tag: av.tagName, text: av.textContent };`)
	if s["tag"] != "SPAN" || s["text"] != "A" {
		t.Errorf("the avatar: %v", s)
	}
}

// Back from a sign-in that could not start (Telegram out of reach, or too
// many tries from one address), the site says so in the person's language
// and drops the reason from the address.
func TestBrowserSignInUnavailable(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL+"/?sign_in=unavailable", 1440, 900, false)
	s := prefs(t, ctx, `
	await until(() => toastText());
	return { toast: toastText(), url: location.pathname + location.search };`)
	if s["toast"] != "Sign-in is not available right now. Try again in a few minutes." || s["url"] != "/" {
		t.Errorf("back from a sign-in that could not start: %v", s)
	}
}

// In the Mini App past its launch's hour, the next read of the person finds
// the token refused and the launch data too old to trade again: the page
// says to open framy again, once, instead of only showing nobody, and a
// form of the settings says the same.
func TestBrowserMiniLaunchRunsOutInTheSettings(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	ctx := miniBrowser(t, ts.URL)
	prefs(t, ctx, `await until(() => document.querySelector("#meBtn")); return {};`)
	f.with(func() { f.launchOld = true })
	s := prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => toastText());
	const toast = toastText();
	await until(() => sheet().hidden);
	await wait(300);
	return { toast, toasts: document.querySelectorAll(".toast").length, me: !!document.querySelector("#meBtn"), open: !sheet().hidden };`)
	if s["toast"] != "The Telegram session is too old. Close framy and open it again." || s["toasts"] != 1.0 || s["me"] != false || s["open"] != false {
		t.Errorf("opening the settings after the launch's hour: %v", s)
	}
	noErrors(t, ctx)
}

// The sessions could not be read when the page opened: /api/me answered 503,
// not 401. The page does not know who is here, so it offers no sign-in, says
// framy is out of reach and asks again, and the person appears.
func TestBrowserASessionUnreadAtStartIsAskedAgain(t *testing.T) {
	f := &fakePrefs{meDown: 1}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := prefs(t, ctx, `
	const first = { me: !!document.querySelector("#meBtn"), signin: !!document.querySelector(".top .signin") };
	const toast = toastText();
	const back = await until(() => document.querySelector("#meBtn"), 8000);
	return { first, back: !!back, signin: !!document.querySelector(".top .signin"), toast };`)
	if fmt.Sprint(s["first"]) != "map[me:false signin:false]" || s["back"] != true || s["signin"] != false || s["toast"] != "No connection to framy. Runs keep going on the server, and the results come when the connection is back." {
		t.Errorf("after an outage at start: %v", s)
	}
	f.with(func() {
		if f.meDown != 0 {
			t.Errorf("the page never asked again: %d", f.meDown)
		}
	})
}

// A cookie or a token that got out is ended from the settings: every
// session of the person's, this one too. One the server could not end is
// no sign-out, and the page stays as it was; the next press goes.
func TestBrowserSignOutEverywhere(t *testing.T) {
	f := &fakePrefs{outAllDown: 1}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden);
	const row = [...document.querySelectorAll('#prefsBody [data-k="p-out"] > button')].map((b) => b.textContent.trim());
	btn(sheet(), "Sign out everywhere").click();
	const q = await yes();
	await until(() => toastText());
	const failed = { toast: toastText(), open: !sheet().hidden, me: !!document.querySelector("#meBtn") };
	btn(sheet(), "Sign out everywhere").click();
	await yes();
	await until(() => sheet().hidden && document.querySelector(".signin"));
	await wait(100);
	return { row, q, failed, toast: toastText(), top: document.querySelector("#top").textContent };`)
	if fmt.Sprint(s["row"]) != "[Sign out Sign out everywhere]" || s["q"] != "Sign out of framy on every device?" ||
		fmt.Sprint(s["failed"]) != "map[me:true open:true toast:framy is busy right now. Try again in a minute.]" ||
		s["toast"] != "You signed out." || s["top"] != "framySign in with TelegramSign in" {
		t.Errorf("sign out everywhere: %v", s)
	}
	f.with(func() {
		if f.outAll != 2 || !f.allOut {
			t.Errorf("logout/all asked %d times, everyone out %v", f.outAll, f.allOut)
		}
	})
	noErrors(t, ctx)
}

// TestBrowserPrefsSignOut checks that the page says signed out only when the
// server ended the session: a refusal keeps the person signed in and says so,
// and the next press signs them out.
func TestBrowserPrefsSignOut(t *testing.T) {
	f := &fakePrefs{outFail: 1}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	s := prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && rowOf("gemini"));
	btn(sheet(), "Sign out").click();
	const q = await yes();
	await until(() => toastText());
	const r = { q, toast: toastText(), open: !sheet().hidden, out: !!btn(sheet(), "Sign out"), me: vis(document.querySelector("#meBtn")) };
	btn(sheet(), "Sign out").click();
	await yes();
	await until(() => sheet().hidden && document.querySelector(".signin"));
	r.toast2 = toastText(); r.top = document.querySelector("#top").textContent;
	return r;`)
	if s["q"] != "Sign out of framy?" || s["toast"] != "Could not sign out, you are still signed in. Try again in a minute." || s["open"] != true ||
		s["out"] != true || s["me"] != true || s["toast2"] != "You signed out." || s["top"] != "framySign in with TelegramSign in" {
		t.Errorf("sign out: %v", s)
	}
	f.with(func() {
		if !f.out || f.outFail != 0 {
			t.Errorf("signed out %v, failures left %d", f.out, f.outFail)
		}
	})
	noErrors(t, ctx)
}

// TestBrowserPrefsSignOutLate checks that a read of the account sent before
// the sign-out, and answered after it, does not show the person signed in again.
func TestBrowserPrefsSignOutLate(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	// Opening the settings reads the account again, and it answers late.
	f.with(func() { f.slowMe = 600 * time.Millisecond })
	s := prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && btn(sheet(), "Sign out"));
	btn(sheet(), "Sign out").click();
	await yes();
	await until(() => document.querySelector(".signin"));
	await wait(1000);
	return { top: document.querySelector("#top").textContent, go: document.querySelector("#go").textContent.trim() };`)
	if s["top"] != "framySign in with TelegramSign in" || !strings.HasPrefix(s["go"].(string), "Sign in to create") {
		t.Errorf("after a late read: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserFreeUsedUp opens the composer of a person with no key whose
// free pictures are used up. The button connects OpenRouter instead of
// standing grey, the price is the run's on that key rather than "Free", and
// when free runs are back reads to its end under it; on a phone it runs
// under the button, across the dock. Free is no choice in Pay with, and the week's
// free video still goes free.
func TestBrowserFreeUsedUp(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 390, 844, true)
	s := prefs(t, ctx, `
	const p = document.querySelector("#prompt"), go = document.querySelector("#go"), pc = document.querySelector("#pcap");
	p.value = "a fox in the snow"; p.dispatchEvent(new Event("input"));
	await until(() => /Connect/.test(go.textContent) && /\$/.test(document.querySelector("#podo").dataset.t));
	await wait(400);
	const out = { go: go.firstChild.nextSibling.textContent, on: !go.disabled, price: document.querySelector("#podo").dataset.t, cap: pc.textContent,
	  cut: pc.scrollWidth > pc.clientWidth + 1 || pc.scrollHeight > pc.clientHeight + 1, under: pc.getBoundingClientRect().top >= go.getBoundingClientRect().bottom - 1 };
	go.click();
	await until(() => !sheet().hidden);
	await wait(400);
	out.focus = document.activeElement.textContent.trim();
	out.inRow = !!document.activeElement.closest('[data-p="openrouter"]');
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
	await until(() => sheet().hidden);
	document.querySelectorAll("#kind > button")[1].click();
	await until(() => /Create/.test(go.textContent));
	await wait(400);
	out.video = [go.firstChild.nextSibling.textContent, document.querySelector("#podo").dataset.t];
	return out;`)
	if s["go"] != "Connect OpenRouter" || s["on"] != true || !strings.HasPrefix(fmt.Sprint(s["price"]), "≈ $") ||
		!regexp.MustCompile(`^Next free run: (?:[A-Z][a-z]+ \d{1,2} at )?\d{1,2}:\d\d [AP]M$`).MatchString(fmt.Sprint(s["cap"])) || s["cut"] != false || s["under"] != true {
		t.Errorf("free used up on a phone: %v", s)
	}
	if s["focus"] != "Connect" || s["inRow"] != true {
		t.Errorf("the button leads to OpenRouter's row: %v", s)
	}
	if fmt.Sprint(s["video"]) != "[Create Free]" {
		t.Errorf("the free video: %v", s["video"])
	}

	// Pay with has Free used up and not to be picked, and OpenRouter picked.
	ctx = browser(t, ts.URL, 1440, 900, false)
	s = prefs(t, ctx, `
	await until(() => document.querySelector('#how [data-seg="pay"] [data-v="free"]'));
	const seg = (v) => document.querySelector('#how [data-seg="pay"] [data-v="' + v + '"]');
	seg("free").click();
	await wait(300);
	return { free: [seg("free").disabled, seg("free").getAttribute("aria-checked"), seg("free").textContent], key: seg("openrouter").getAttribute("aria-checked"),
	  go: document.querySelector("#go").firstChild.nextSibling.textContent };`)
	if fmt.Sprint(s["free"]) != "[true false FreeUsed up]" || s["key"] != "true" || s["go"] != "Connect OpenRouter" {
		t.Errorf("pay with: %v", s)
	}
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
	`+leave+`
	sel.value = "ru"; sel.dispatchEvent(new Event("change"));
	return {};`)
	reloaded(t, ctx)
	s := prefs(t, ctx, `
	await until(() => !sheet().hidden);
	await wait(300);
	return { lang: document.documentElement.lang, title: sheet().querySelector("h3").textContent, sel: sheet().querySelector("select.pick").value,
	  prompt: document.querySelector("#prompt").value, opts: sheet().querySelector("select.pick").options.length, kept: localStorage.getItem("framy.language") };`)
	f.with(func() {
		if len(f.patches) != 1 || f.patches[0]["language"] != "ru" {
			t.Errorf("language sent: %v", f.patches)
		}
	})
	if s["lang"] != "ru" || s["title"] != "Настройки" || s["sel"] != "ru" || s["prompt"] != "a fox" || s["opts"] != float64(17) || s["kept"] != "ru" {
		t.Errorf("language: %v", s)
	}
	// Automatic again: the language core resolves, and the list says Automatic.
	prefs(t, ctx, `
	const sel = sheet().querySelector("select.pick");
	`+leave+`
	sel.value = ""; sel.dispatchEvent(new Event("change"));
	return {};`)
	reloaded(t, ctx)
	s = prefs(t, ctx, `
	await until(() => !sheet().hidden);
	return { lang: document.documentElement.lang, sel: sheet().querySelector("select.pick").value, shown: sheet().querySelector("select.pick").selectedOptions[0].textContent,
	  kept: localStorage.getItem("framy.language") };`)
	if s["lang"] != "en" || s["sel"] != "" || s["shown"] != "Automatic" || s["kept"] != "en" {
		t.Errorf("automatic: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserPrefsBelarusian checks the settings and the composer in
// Belarusian, whose data Chromium's Intl lacks: when the next free run comes,
// the names of the currencies and the numbers are Belarusian, not English.
func TestBrowserPrefsBelarusian(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	f.with(func() { f.language = "be" })
	ctx := browser(t, ts.URL, 1440, 900, false)
	ready(t, ctx)
	s := prefs(t, ctx, `
	const back = document.querySelector("#composer").innerText;
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && rowOf("gemini"));
	await wait(300);
	return { lang: document.documentElement.lang, back, free: note("p-free-image"), store: note("p-store"), text: sheet().innerText };`)
	month := `(студзеня|лютага|сакавіка|красавіка|мая|чэрвеня|ліпеня|жніўня|верасня|кастрычніка|лістапада|снежня)`
	text := fmt.Sprint(s["text"])
	if s["lang"] != "be" || !strings.Contains(fmt.Sprint(s["free"]), "праз 3 гадзіны") || !strings.Contains(fmt.Sprint(s["store"]), "30") ||
		!strings.Contains(text, "долар ЗША") || !strings.Contains(text, "беларускі рубель") || strings.Contains(text, "Dollar") {
		t.Errorf("settings in Belarusian: %v", s)
	}
	if !regexp.MustCompile(`(?:\d{1,2} ` + month + ` у )?\d{1,2}:\d\d`).MatchString(fmt.Sprint(s["back"])) {
		t.Errorf("when the next free run comes, in Belarusian: %q", s["back"])
	}
	noErrors(t, ctx)
}

// TestBrowserPrefsDefaults checks the rows of what the site keeps for the
// person: what a key spent through framy, the models the composer opens on,
// picked in the picker and back in the settings after it, and the switch of
// the DM.
func TestBrowserPrefsDefaults(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	f.with(func() {
		f.connect("openai", "ab12", "verified")
		f.conns["openai"]["spent"] = map[string]any{"usd": 0.42, "amount": 0.42, "currency": "USD", "label": "$0.42"}
		f.conns["openai"]["spent_30d"] = map[string]any{"usd": 0.1, "amount": 0.1, "currency": "USD", "label": "$0.10"}
		f.connect("gemini", "cd34", "valid")
		f.conns["gemini"]["spent"] = map[string]any{"usd": 0, "amount": 0, "currency": "USD", "label": "$0.00"}
	})
	ready(t, ctx)
	const name = `const name = () => [...document.querySelectorAll("#how .mname > span, #dtools .chipbtn .nm > span")].find(vis).textContent;`
	s := prefs(t, ctx, name+`
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && defRow("image"));
	await wait(500);
	const tog = sheet().querySelector('[data-k="p-tg"] .tog');
	return { oa: rowText("openai").sub, ge: rowText("gemini").sub, image: defRow("image"), video: defRow("video"), composer: name(),
	  tg: note("p-tg"), on: tog.getAttribute("aria-checked"), lang: sheet().querySelector("select.pick").value };`)
	if s["oa"] != "Key …ab12 · worksThrough framy: $0.42 in all, $0.10 in 30 days" || s["ge"] != "Key …cd34 · accepted" ||
		fmt.Sprint(s["image"]) != "map[btns:[Change] sub:MAI-Image 2.6 Flash, framy’s choice title:Images]" ||
		fmt.Sprint(s["video"]) != "map[btns:[Change] sub:HeyGen Video, framy’s choice title:Videos]" || s["composer"] != "MAI-Image 2.6 Flash" ||
		s["tg"] != "Send results to the chatWhat you make on the site also comes to your chat with framy" || s["on"] != "true" || s["lang"] != "" {
		t.Errorf("rows: %v", s)
	}

	// Change: the picker of pictures, without the settings under it; a pick
	// comes back to them, and the composer is on it too. The settings read
	// the account again as they open, and that answer, older than the pick,
	// comes after it.
	f.with(func() { f.slowMe = 400 * time.Millisecond })
	s = prefs(t, ctx, name+`
	btn(document.querySelector('#prefsBody [data-k="p-def-image"]'), "Change").click();
	await until(() => vis(document.querySelector("#picker")) && document.querySelector("#pkrows .frow"));
	await wait(500);
	const title = document.querySelector("#pickTitle").textContent, under = !sheet().hidden;
	// The makers' marks are in the page from the start, never a file still loading.
	const marks = [...document.querySelectorAll("#picker .lg > i")].map((i) => i.style.getPropertyValue("--m"));
	const inline = marks.length > 10 && marks.every((m) => m.startsWith('url("data:image/svg+xml;base64,'));
	// The tab icon is framy's own file, which a full Chrome fetches; the makers' marks are inline.
	const svgs = performance.getEntriesByType("resource").filter((r) => r.name.endsWith(".svg") && !/\/assets\/framy\.[0-9a-f]{12}\.svg$/.test(r.name)).length;
	// Every maker of the catalog has a mark, none is left with letters.
	const letters = [...document.querySelectorAll("#picker .lg")].filter((l) => !l.querySelector("i")).map((l) => l.title);
	const row = [...document.querySelectorAll("#pkrows .frow")].find((r) => r.getAttribute("aria-current") !== "true" && !r.classList.contains("dim"));
	const fam = row.querySelector(".nm").textContent;
	row.querySelector(".fhit").click();
	await until(() => !sheet().hidden && defRow("image").btns.length === 2);
	await wait(800);
	const image = defRow("image");
	btn(sheet(), "Done").click();
	await until(() => sheet().hidden);
	await wait(400);
	return { title, under, fam, image, inline, svgs, letters, picker: document.querySelector("#picker").hidden, composer: name() };`)
	f.with(func() { f.slowMe = 0 })
	var picked string
	f.with(func() {
		if len(f.patches) == 1 {
			picked, _ = f.patches[0]["default_models"].(map[string]any)["image"].(string)
		}
		if picked == "" || picked == "microsoft/mai-image-2.6-flash" || f.defaults["image"] != picked {
			t.Errorf("default sent: %v, kept %v", f.patches, f.defaults)
		}
	})
	image, _ := s["image"].(map[string]any)
	if s["title"] != "Default model for images" || s["under"] != false || s["picker"] != true || s["inline"] != true || s["svgs"] != float64(0) ||
		fmt.Sprint(s["letters"]) != "[]" ||
		fmt.Sprint(image["btns"]) != "[Reset Change]" || !strings.HasPrefix(fmt.Sprint(image["sub"]), fmt.Sprint(s["fam"])) ||
		strings.Contains(fmt.Sprint(image["sub"]), "framy’s choice") || s["composer"] != image["sub"] {
		t.Errorf("pick a default: %v (sent %q)", s, picked)
	}

	// Escape in the picker goes back to the settings and keeps the model;
	// Reset goes back to framy's choice; the switch turns the DM off.
	s = prefs(t, ctx, `
	document.querySelector("#meBtn").click();
	await until(() => !sheet().hidden && defRow("image"));
	await wait(400);
	btn(document.querySelector('#prefsBody [data-k="p-def-video"]'), "Change").click();
	await until(() => vis(document.querySelector("#picker")));
	await wait(400);
	const title = document.querySelector("#pickTitle").textContent;
	const lgs = [...document.querySelectorAll("#picker .lg")];
	const letters = lgs.filter((l) => !l.querySelector("i")).map((l) => l.title);
	const marked = [...new Set(lgs.filter((l) => l.querySelector("i")).map((l) => l.title))].filter((m) => m === "HeyGen" || m === "TBC").sort();
	document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
	await until(() => !sheet().hidden);
	await wait(400);
	const back = { picker: document.querySelector("#picker").hidden, scrim: document.querySelector("#scrim").hidden, video: defRow("video").sub };
	btn(document.querySelector('#prefsBody [data-k="p-def-image"]'), "Reset").click();
	await until(() => defRow("image").btns.length === 1);
	await wait(300);
	sheet().querySelector('[data-k="p-tg"] .tog').click();
	await until(() => sheet().querySelector('[data-k="p-tg"] .tog').getAttribute("aria-checked") === "false");
	await wait(300);
	return { title, letters, marked, back, image: defRow("image").sub, tg: note("p-tg") };`)
	if s["title"] != "Default model for videos" || fmt.Sprint(s["back"]) != "map[picker:true scrim:false video:HeyGen Video, framy’s choice]" ||
		fmt.Sprint(s["letters"]) != "[]" || fmt.Sprint(s["marked"]) != "[HeyGen TBC]" ||
		s["image"] != "MAI-Image 2.6 Flash, framy’s choice" || s["tg"] != "Send results to the chatWhat you make on the site stays on the site" {
		t.Errorf("back, reset, switch: %v", s)
	}
	f.with(func() {
		if len(f.patches) != 3 || fmt.Sprint(f.patches[1]) != "map[default_models:map[image:]]" || fmt.Sprint(f.patches[2]) != "map[deliver_to_telegram:false]" ||
			len(f.defaults) != 0 || f.deliver {
			t.Errorf("sent: %v, kept %v, deliver %v", f.patches, f.defaults, f.deliver)
		}
	})

	// The next visit opens on the person's models.
	f.with(func() { f.defaults["video"] = "google/veo-3.1" })
	prefs(t, ctx, leave+`setTimeout(() => location.reload(), 50); return {};`)
	reloaded(t, ctx)
	s = prefs(t, ctx, name+`
	const image = name();
	document.querySelectorAll("#kind > button")[1].click();
	await wait(700);
	return { image, video: name() };`)
	if s["image"] != "MAI-Image 2.6 Flash" || s["video"] != "Veo 3.1" {
		t.Errorf("opens on: %v", s)
	}
	noErrors(t, ctx)
}

// TestBrowserPrefsPhone checks the settings fit a phone with a key's form
// open and its refusal shown, and that the Mini App connects OpenRouter in
// the browser and finds the key when Telegram is back.
func TestBrowserPrefsPhone(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	// A default model of the person's: its row has two buttons.
	f.with(func() { f.defaults["image"] = "bytedance-seed/seedream-5-0-pro" })
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
	// A row's buttons that go under its text start where the text does, the one
	// that did not fit beside the one before as well: Sign out everywhere.
	const under = [...body.querySelectorAll(".srow.btns")].filter(vis).flatMap((row) => {
	  const l = row.querySelector(".l").getBoundingClientRect().left;
	  return [...row.querySelectorAll(":scope > button, :scope > select")].filter(vis).filter((b) => b.getBoundingClientRect().left < l - 0.5).map((b) => b.textContent.trim().slice(0, 30));
	});
	return { out, under, scroll: body.scrollWidth - body.clientWidth, page: document.documentElement.scrollWidth - innerWidth };`)
		if len(s["out"].([]any)) > 0 || len(s["under"].([]any)) > 0 || s["scroll"].(float64) > 0 || s["page"].(float64) > 0 {
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
	const before = rowText("openrouter"), out = !!btn(sheet(), "Sign out"), who = note("p-out");
	btn(rowOf("openrouter"), "Connect").click();
	await until(() => rowText("openrouter").btns[0] === "Open again");
	return { shown, before, out, who, away: rowText("openrouter"), links: window.__tg.links.map((u) => new URL(u).pathname) };`)
	// Whose account a key goes to shows in the Mini App too, with no Sign out.
	if s["shown"] != true || s["out"] != false || s["who"] != "AAdaSigned in with Telegram" ||
		fmt.Sprint(s["before"]) != "map[bad:false btns:[Connect] sub:Opens in the browser. Pay with OpenRouter credits. Have a key? Paste it]" ||
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

// TestBrowserPrefsRows checks where the buttons of the settings stand. On a
// phone they go under the text in every row, at its edge, so the text has
// the width and the buttons one place; the switch of the DM stays at the end
// of its line. On a computer they stand at the end of the text's line, but
// for a key with a problem, whose warning needs the width. Deleting the
// account is framed and has the bin, unlike any other button.
func TestBrowserPrefsRows(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, func(srv *Server) {
		f.mount(srv)
		f.connect("openrouter", "or42", "valid")
		f.connect("openai", "dead", "invalid")
	})
	for _, c := range []struct {
		w     int64
		phone bool
	}{{360, true}, {390, true}, {1440, false}} {
		ctx := browserAs(t, ts.URL, c.w, 844, opened{phone: c.phone})
		bad := js[[]string](t, ctx, pageHelpers+`
		document.querySelector("#meBtn").click();
		for (let i = 0; i < 40 && !document.querySelector("#prefsBody .srow"); i++) await wait(100);
		await wait(900);
		const bad = [], phone = `+fmt.Sprint(c.phone)+`;
		for (const r of document.querySelectorAll("#prefsBody .srow")) {
		  const l = r.querySelector(".l").getBoundingClientRect(), row = r.getBoundingClientRect(), name = r.querySelector(".l b").textContent;
		  for (const b of r.querySelectorAll(":scope > button, :scope > select")) {
		    const br = b.getBoundingClientRect(), under = br.top >= l.bottom - 1;
		    if (b.matches(".tog")) { if (under || br.right < row.right - 12) bad.push(name + ": the switch is not at the end of its line"); continue; }
		    if (phone && !under) bad.push(name + ": " + b.textContent + " beside the text");
		    if (!phone && under !== r.matches(".bad")) bad.push(name + ": " + b.textContent + (under ? " under the text" : " beside a warning"));
		  }
		  const first = r.querySelector(":scope > button:not(.tog), :scope > select");
		  if (first && phone && Math.abs(first.getBoundingClientRect().left - l.left) > 1) bad.push(name + ": the buttons are not at the text's edge");
		}
		const del = document.querySelector("#prefsBody .srow > button.danger"), cs = del && getComputedStyle(del);
		const plain = [...document.querySelectorAll("#prefsBody .srow > button:not(.danger, .primary, .tog)")].map((b) => getComputedStyle(b));
		if (!del || cs.borderTopWidth !== "2px" || cs.backgroundColor !== "rgba(0, 0, 0, 0)" || !del.querySelector(".ic.trash") ||
		  plain.some((p) => p.borderTopWidth === cs.borderTopWidth || p.backgroundColor === cs.backgroundColor)) bad.push("the delete button looks like the others");
		return bad;`)
		if len(bad) > 0 {
			t.Errorf("at %d: %v", c.w, bad)
		}
		noErrors(t, ctx)
	}
}

// TestBrowserGuestLanguage checks a visitor not signed in picks the page's
// language beside Sign in: the bar still fits a small phone, the page loads
// again in the pick with what was typed, the pick holds, and it goes to the
// account they sign in with when that account has not chosen its own.
func TestBrowserGuestLanguage(t *testing.T) {
	f := &fakePrefs{}
	ts := fakeSite(t, f.mount)
	f.with(func() { f.out = true })
	for _, w := range []int64{320, 1440} {
		ctx := browserAs(t, ts.URL, w, 700, opened{phone: w < 960})
		s := js[map[string]any](t, ctx, pageHelpers+`
		const g = document.querySelector(".top .glang"), sel = g?.querySelector("select"), si = document.querySelector(".top .signin"), code = g?.querySelector("span").textContent;
		const r = (e) => e.getBoundingClientRect();
		return { code, val: sel?.value, name: sel?.getAttribute("aria-label"), n: sel?.options.length, de: [...(sel?.options || [])].find((o) => o.value === "de")?.textContent,
		  wide: document.documentElement.scrollWidth <= innerWidth, beside: !!si && r(g).right <= r(si).left + 1 && Math.abs(r(g).top + r(g).height / 2 - (r(si).top + r(si).height / 2)) < 2,
		  tall: r(g).height >= (matchMedia("(pointer: coarse)").matches ? 44 : 40) };`)
		if s["code"] != "EN" || s["val"] != "en" || s["name"] != "Language" || s["n"] != float64(16) || s["de"] != "Deutsch" || s["wide"] != true || s["beside"] != true || s["tall"] != true {
			t.Errorf("at %d px the visitor's language: %v", w, s)
		}
		noErrors(t, ctx)
	}
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[any](t, ctx, pageHelpers+`
	const p = document.querySelector("#prompt"); p.value = "a fox"; p.dispatchEvent(new Event("input"));
	const sel = document.querySelector(".top .glang select");
	`+leave+`
	sel.value = "de"; sel.dispatchEvent(new Event("change"));
	return 1;`)
	reloaded(t, ctx)
	s := js[map[string]any](t, ctx, pageHelpers+`
	return { lang: document.documentElement.lang, code: document.querySelector(".top .glang span").textContent, signin: document.querySelector(".top .signin").getAttribute("aria-label"),
	  prompt: document.querySelector("#prompt").value, kept: localStorage.getItem("framy.language") };`)
	if s["lang"] != "de" || s["code"] != "DE" || s["signin"] == "Sign in with Telegram" || s["prompt"] != "a fox" || s["kept"] != "de" {
		t.Errorf("after the pick: %v", s)
	}
	// Signed in now, to an account with no language of its own: it takes the pick.
	f.with(func() { f.out, f.language, f.langPick = false, "en", false })
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	s = js[map[string]any](t, ctx, pageHelpers+`
	await until(() => document.querySelector("#meBtn"));
	return { lang: document.documentElement.lang, me: document.querySelector("#meBtn").getAttribute("aria-label"), glang: !!document.querySelector(".top .glang"), flag: localStorage.getItem("framy.guest_lang") };`)
	f.with(func() {
		if len(f.patches) != 1 || f.patches[0]["language"] != "de" || f.language != "de" {
			t.Errorf("the account was sent %v and speaks %q", f.patches, f.language)
		}
	})
	if s["lang"] != "de" || s["me"] != "Einstellungen" || s["glang"] != false || s["flag"] != nil {
		t.Errorf("signed in after the pick: %v", s)
	}
	noErrors(t, ctx)

	// An account that chose its own language keeps it, and the page follows it.
	g := &fakePrefs{}
	gs := fakeSite(t, g.mount)
	g.with(func() { g.out = true })
	ctx = browser(t, gs.URL, 1440, 900, false)
	js[any](t, ctx, pageHelpers+`
	const sel = document.querySelector(".top .glang select");
	`+leave+`
	sel.value = "fr"; sel.dispatchEvent(new Event("change"));
	return 1;`)
	reloaded(t, ctx)
	g.with(func() { g.out, g.language, g.langPick = false, "ru", true })
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	ready(t, ctx)
	s = js[map[string]any](t, ctx, pageHelpers+`
	await until(() => document.querySelector("#meBtn"));
	await wait(300);
	return { lang: document.documentElement.lang, kept: localStorage.getItem("framy.language") };`)
	g.with(func() {
		if len(g.patches) != 0 {
			t.Errorf("an account with its own language was sent %v", g.patches)
		}
	})
	if s["lang"] != "ru" || s["kept"] != "ru" {
		t.Errorf("an account with its own language: %v", s)
	}
	noErrors(t, ctx)
}
