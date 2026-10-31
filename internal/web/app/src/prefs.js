// The settings: the person's keys, what is left free, the currency, the
// language and the account. Each change goes to the server first; the sheet
// shows what it said.
import { $, $$, h, ic } from "./dom.js";
import { t, lang, fmtNum, fmtIn, fmtTime } from "./i18n.js";
import { api } from "./api.js";
import { S, connection } from "./state.js";
import { PNAME, provMark } from "./catalog.js";
import { clearPrices } from "./price.js";
import { openSheet, closeSheets, toast, ask } from "./ui.js";
import { anim, morph, opt, placeSegs } from "./motion.js";
import { signOut, forget, refreshMe } from "./run.js";
import { LANGS, CURRENCIES } from "assets:lists";

const PROVIDERS = ["openrouter", "openai", "gemini"];
// Where each provider makes and revokes keys.
const KEYS_AT = { openrouter: "https://openrouter.ai/settings/keys", openai: "https://platform.openai.com/api-keys", gemini: "https://aistudio.google.com/apikey" };
const LOOK = { openrouter: "sk-or-v1-…", openai: "sk-…", gemini: "AIza…" };
export const OAUTH_START = "/oauth/openrouter/start";
// How a connect through OpenRouter ended (internal/oauth, Result).
const OAUTH = new Set(["connected", "cancelled", "expired", "refused", "unavailable", "signin"]);
// What a run showed about a key; Check asks the provider again once it is fixed.
const PROBLEM = new Set(["invalid", "no_credits", "needs_verification", "needs_billing"]);

// What each key's row is doing: edit (the form is open), draft (what is in
// it), busy, err (a refusal that kept nothing), wait (when it may be tried
// again), said (how a connect ended), away (the Mini App sent the person to
// the browser), going (the site is leaving for OpenRouter) and revoked (just
// disconnected).
const KS = {};
const ks = (p) => (KS[p] ||= {});
// Deleting the account: null, "busy" or "fail" (pressing again carries on).
let ACC = null;

export function mountPrefs() {
  // Back from OpenRouter with the browser's Back button: the page is the one it left.
  addEventListener("pageshow", (e) => { if (e.persisted && KS.openrouter?.going) update(() => { KS.openrouter.going = false; }); });
  return h("section", { class: "sheet", id: "prefs", hidden: true, "aria-label": t("prefs.title"), role: "dialog", "aria-modal": "true" },
    h("header", {}, h("h3", {}, t("prefs.title")), h("button", { type: "button", class: "x", "data-close": "" }, t("pick.done"))),
    h("div", { class: "body", id: "prefsBody" }));
}

// openPrefs opens the settings; with a provider, at that key: the form to
// paste it where one is needed, else its row.
export function openPrefs(p) {
  if (!S.signed) return;
  if (p) {
    const c = connection(p), k = ks(p);
    if (c ? c.status === "invalid" : p !== "openrouter") Object.assign(k, { edit: true, err: null, said: null });
  }
  renderPrefs();
  const fresh = $("#prefs").hidden;
  openSheet("#prefs");
  // Keys may have changed in another tab or in the bot: the sheet redraws when the answer is in.
  if (fresh) refreshMe();
  if (fresh) $$("#prefsBody > *").slice(0, 8).forEach((el, i) => anim(el, [{ opacity: 0, transform: "translateY(8px)" }, { opacity: 1, transform: "none" }], { duration: 300, delay: 90 + i * 30, fill: "backwards" }));
  if (!p) return;
  setTimeout(() => {
    const r = $(`#prefsBody [data-p="${p}"]`);
    if (!r) return;
    r.scrollIntoView({ block: "nearest" });
    const f = $(`#prefsBody [data-k="pk-${p}"] input`) || r.querySelector("button.primary") || r.querySelector("button");
    f?.focus({ preventScroll: true });
  }, 120);
}

const MORPH = { dur: 340, enter: (el, i) => anim(el, [{ opacity: 0, transform: "translateY(6px)" }, { opacity: 1, transform: "none" }], { duration: 260, delay: 80 + i * 30, fill: "backwards" }),
  leave: (g) => anim(g, [{ opacity: 1 }, { opacity: 0 }], { duration: 160, fill: "forwards" }) };
// A change of the sheet: rows glide to their new places, new ones rise.
function update(fn) {
  const body = $("#prefsBody");
  if (!body || $("#prefs").hidden) { fn?.(); return; }
  morph(body, () => { fn?.(); renderPrefs(); }, MORPH);
}
// The keys or the free counts changed: the rest of the page follows.
const meChanged = () => window.dispatchEvent(new Event("framy:me"));
export const prefsOpen = () => !!$("#prefs") && !$("#prefs").hidden;

/* Links and lines. */
// A link out: a new tab on the site, the browser in the Mini App.
function outLink(url) {
  const a = h("a", { href: url, target: "_blank", rel: "noopener noreferrer" }, url.replace(/^https:\/\//, ""));
  if (S.tg?.openLink) a.addEventListener("click", (e) => { e.preventDefault(); S.tg.openLink(url); });
  return a;
}
// A line with an element where its {link} stands.
function withLink(key, vars, el) {
  const [a, ...b] = t(key, { ...vars, link: "\u0001" }).split("\u0001");
  return b.length ? [a, el, b.join("")] : [a];
}
const warn = (text) => h("span", { class: "warn" }, ic("warn"), h("span", {}, text));
function size(bytes) {
  const mb = bytes / 1048576;
  if (mb >= 1024) return t("unit.gb", { n: fmtNum(mb / 1024, { maximumFractionDigits: 1 }) });
  return t("unit.mb", { n: fmtNum(mb, { maximumFractionDigits: mb < 10 ? 1 : 0 }) });
}
function curName(c) {
  try { return new Intl.DisplayNames([lang], { type: "currency" }).of(c) || c; } catch { return c; }
}

/* Keys. */
// The server's own check of a paste (internal/keys): 20 to 512 printable
// ASCII characters without spaces. Providers change how their keys begin, so
// the beginning is not checked.
const shaped = (v) => v.length >= 20 && v.length <= 512 && /^[\x21-\x7e]+$/.test(v);
function keyErr(p, code, saving) {
  const P = PNAME[p] || p;
  switch (code) {
    case "bad_key": case "key_rejected": case "key_forbidden": case "key_unreadable": case "keys_off": return t("key." + code, { provider: P });
    case "check_unavailable": return t(saving ? "key.check_unavailable" : "key.recheck_unavailable", { provider: P });
    case "network": return t("err.network");
    case "http_429": return t("err.busy");
    default: return t("key.failed", { provider: P });
  }
}
function putConn(c) {
  if (S.me) S.me.connections = [...(S.me.connections || []).filter((x) => x.provider !== c.provider), c];
}

// PUT /api/connections/{provider}: the provider checks the key before framy keeps it.
async function save(p, value) {
  const k = ks(p), v = String(value || "").trim();
  if (k.busy) return;
  k.draft = value;
  if (!shaped(v)) return update(() => { k.err = "bad_key"; });
  update(() => { Object.assign(k, { busy: true, err: null, said: null }); });
  try {
    putConn(await api(`/api/connections/${p}`, { method: "PUT", body: { key: v } }));
    clearInterval(k.tick);
    update(() => { Object.assign(k, { busy: false, edit: false, draft: "", revoked: false, wait: 0 }); });
    meChanged();
  } catch (e) {
    if (e.code === "sign_in") return lost();
    update(() => { k.busy = false; k.err = e.code; if (e.code === "check_unavailable") waitFor(p, e.retryAfter || 10); });
  }
}
// POST /api/connections/{provider}/check: Check. A refused key comes back as invalid.
async function recheck(p) {
  const k = ks(p);
  if (k.busy) return;
  update(() => { Object.assign(k, { busy: true, err: null, said: null }); });
  try {
    putConn(await api(`/api/connections/${p}/check`, { method: "POST" }));
    update(() => { k.busy = false; });
    meChanged();
  } catch (e) {
    if (e.code === "sign_in") return lost();
    // Gone meanwhile, from another tab or the bot: the list is read again.
    if (e.code === "not_found") { k.busy = false; await refreshMe(); update(); return; }
    update(() => { k.busy = false; k.err = e.code; if (e.code === "key_unreadable") k.edit = true; if (e.code === "check_unavailable") waitFor(p, e.retryAfter || 10); });
  }
}
// DELETE /api/connections/{provider}: framy forgets the key. The provider
// still knows it, so the sheet says where to revoke it.
async function disconnect(p) {
  const k = ks(p);
  if (k.busy) return;
  try { await api(`/api/connections/${p}`, { method: "DELETE" }); }
  catch (e) {
    if (e.code === "sign_in") return lost();
    if (e.code !== "not_found") { toast(keyErr(p, e.code)); return; }
  }
  if (S.me) S.me.connections = (S.me.connections || []).filter((x) => x.provider !== p);
  update(() => { Object.assign(k, { revoked: true, err: null, said: null, edit: false, draft: "" }); });
  meChanged();
}
// A check the provider did not answer may be tried again after Retry-After:
// the button counts down where it stands.
function waitFor(p, secs) {
  const k = ks(p);
  k.wait = Date.now() + secs * 1000;
  clearInterval(k.tick);
  const tick = () => {
    const left = Math.ceil((k.wait - Date.now()) / 1000);
    for (const b of $$(`#prefsBody [data-wait="${p}"]`)) { b.disabled = left > 0 || !!k.busy; b.lastChild.textContent = left > 0 ? t("key.in_s", { n: left }) : b.dataset.label; }
    if (left <= 0) { clearInterval(k.tick); k.wait = 0; }
  };
  k.tick = setInterval(tick, 250);
}
function waitBtn(p, label, attrs) {
  const k = ks(p), left = k.wait ? Math.ceil((k.wait - Date.now()) / 1000) : 0;
  return h("button", { type: "button", ...attrs, "data-wait": p, "data-label": label, disabled: !!k.busy || left > 0 },
    h("span", {}, k.busy ? t("key.checking") : left > 0 ? t("key.in_s", { n: left }) : label));
}
function edit(p) {
  update(() => { Object.assign(ks(p), { edit: true, err: null, said: null }); });
  setTimeout(() => $(`#prefsBody [data-k="pk-${p}"] input`)?.focus({ preventScroll: true }), 60);
}

/* OpenRouter by OAuth. The site leaves for openrouter.ai and comes back to
   /?connect=openrouter&result=…; the Mini App opens it in the browser and
   reads /api/me again when the person is back. */
function oauthStart() {
  const k = ks("openrouter");
  if (S.mini) {
    const url = location.origin + OAUTH_START;
    if (S.tg?.openLink) S.tg.openLink(url); else open(url, "_blank", "noopener");
    update(() => { Object.assign(k, { away: true, said: null }); });
    return;
  }
  update(() => { Object.assign(k, { going: true, said: null }); });
  try { sessionStorage.setItem("framy.draft", $("#prompt").value); } catch {}
  location.assign(OAUTH_START);
}
// The site, back with the result in its address.
export function oauthResult(res) {
  res = OAUTH.has(res) ? res : "unavailable";
  if (res === "signin" || !S.signed) { toast(t("oauth.signin"), { ms: 8000 }); return; }
  if (res === "connected") { toast(t("oauth.connected")); openPrefs(); return; }
  ks("openrouter").said = t("oauth." + res);
  openPrefs();
}
// The Mini App, visible again after openLink. It does not learn why no key
// came: the browser showed that.
export function oauthBack() {
  const k = KS.openrouter;
  if (!k?.away) return false;
  refreshMe().then(() => {
    const got = !!connection("openrouter");
    update(() => { Object.assign(k, { away: false, said: got ? null : t("oauth.mini_none") }); });
    if (got) toast(t("oauth.connected"));
  });
  return true;
}

function keyRows(p) {
  const c = connection(p), k = ks(p), P = PNAME[p], out = [];
  const row = (sub, ...btns) => sheetRow("key-" + p, provMark(p), P, sub, ...btns);
  let r;
  if (c) {
    const bad = c.status === "invalid" || k.err === "key_unreadable", problem = PROBLEM.has(c.status) ? t("key." + c.status, { provider: P }) : "";
    const sub = k.edit ? t("key.last4", { last4: c.last4 }) : k.err ? warn(keyErr(p, k.err)) : problem ? warn(problem)
      : t(c.status === "verified" ? "key.works" : "key.accepted", { last4: c.last4 });
    r = row(sub,
      bad ? (k.edit ? null : h("button", { type: "button", class: "primary", onclick: () => edit(p) }, t("key.repaste"))) : waitBtn(p, t("key.check"), { onclick: () => recheck(p) }),
      h("button", { type: "button", disabled: !!k.busy, onclick: () => disconnect(p) }, t("key.disconnect")));
    if (problem || (k.err && !k.edit)) r.classList.add("bad");
  } else if (p === "openrouter") {
    const paste = h("button", { type: "button", class: "link-t", onclick: () => edit(p) }, t("key.paste_link"));
    r = row(k.away ? t("key.away") : [t(S.mini ? "key.or_sub_mini" : "key.or_sub") + " ", paste],
      k.away ? h("button", { type: "button", class: "fixw", onclick: oauthStart }, t("key.open_again"))
        : h("button", { type: "button", class: "primary fixw", disabled: !!k.going, onclick: oauthStart }, ic(S.mini ? "out" : "plus"), h("span", {}, t(k.going ? "key.going" : "key.connect"))));
    // A long line: on a narrow sheet the button goes under it.
    r.classList.add("two");
  } else {
    r = row(t("key.paste_sub"), k.edit ? null : h("button", { type: "button", class: "primary", onclick: () => edit(p) }, ic("plus"), h("span", {}, t("key.add"))));
  }
  r.dataset.p = p;
  out.push(r);
  if (k.edit) {
    const input = h("input", { type: "password", name: "key-" + p, autocomplete: "off", autocapitalize: "off", spellcheck: "false", placeholder: LOOK[p],
      "aria-label": t("key.field", { provider: P }), "aria-invalid": String(!!k.err), value: k.draft || "" });
    input.addEventListener("input", () => { k.draft = input.value; });
    out.push(h("form", { class: "kform", "data-k": "pk-" + p, onsubmit: (e) => { e.preventDefault(); save(p, input.value); } }, input,
      waitBtn(p, t("key.check"), { class: "primary", type: "submit" }),
      h("button", { type: "button", class: "sq", "aria-label": t("cancel"), title: t("cancel"), onclick: () => update(() => { Object.assign(k, { edit: false, err: null, draft: "" }); }) }, ic("close")),
      h("p", { class: k.err ? "kerr" : "khint" }, k.err ? [ic("warn"), h("span", {}, keyErr(p, k.err, true))] : withLink("key.hint", {}, outLink(KEYS_AT[p])))));
  }
  if (k.said && !k.edit && !c) out.push(h("p", { class: "note said", "data-k": "ps-" + p }, ic("warn"), h("span", {}, k.said)));
  if (k.revoked && !c) out.push(h("p", { class: "note", "data-k": "pr-" + p }, withLink("key.revoked", { provider: P }, outLink(KEYS_AT[p]))));
  return out;
}

function sheetRow(k, mark, title, sub, ...btns) {
  const bs = btns.filter(Boolean);
  return h("div", { class: "srow" + (bs.length > 1 || bs[0]?.tagName === "SELECT" ? " two" : ""), "data-k": "p-" + k },
    mark, h("div", { class: "l" }, h("b", {}, title), h("span", {}, sub)), ...bs);
}

/* What is left free. */
function freeRow(kind) {
  const m = S.me?.free?.[kind === "video" ? "videos" : "images"];
  if (!m) return null;
  let sub = t(m.window_seconds >= 604800 ? "prefs.left_week" : "prefs.left_day", { left: m.left, limit: m.limit });
  if (S.closed[kind] > Date.now()) sub += " · " + t("prefs.closed_until", { time: fmtTime(S.closed[kind]) });
  else if (!m.left && m.frees_at) sub += " · " + t("prefs.next", { when: fmtIn(Date.parse(m.frees_at)) });
  return sheetRow("free-" + kind, ic(kind), t(kind === "video" ? "prefs.free_videos" : "prefs.free_images"), sub);
}

/* Currency and language: PATCH /api/me. */
async function setCurrency(c) {
  if (!S.me || S.me.currency === c) return;
  const was = S.me.currency;
  S.me.currency = c;
  renderPrefs();
  try { S.me = await api("/api/me", { method: "PATCH", body: { currency: c } }); }
  catch (e) {
    if (e.code === "sign_in") return lost();
    S.me.currency = was;
    toast(e.code === "network" ? t("err.network") : t("prefs.not_saved"));
  }
  clearPrices();
  meChanged();
  update();
}
// The page speaks the new language from its next load: it reloads with the
// draft kept and the settings open, where the person was.
async function setLanguage(code) {
  let me;
  try { me = await api("/api/me", { method: "PATCH", body: { language: code } }); }
  catch (e) {
    if (e.code === "sign_in") return lost();
    toast(e.code === "network" ? t("err.network") : t("prefs.not_saved"));
    update();
    return;
  }
  S.me = me;
  const to = me.language || code || lang;
  if (to === lang) { meChanged(); update(); return; }
  try {
    localStorage.setItem("framy.lang", to);
    sessionStorage.setItem("framy.prefs", "1");
    sessionStorage.setItem("framy.draft", $("#prompt").value);
  } catch {}
  location.reload();
}

/* The account. */
// DELETE /api/me: refused while a job is queued or running (409 jobs_active).
async function deleteAccount() {
  if (ACC === "busy") return;
  if (ACC !== "fail" && !(await ask({ title: t("prefs.delete_q"), text: t("prefs.delete_text"), yes: t("prefs.delete") }))) return;
  update(() => { ACC = "busy"; });
  try { await api("/api/me", { method: "DELETE", body: { confirm: true } }); }
  catch (e) {
    if (e.code === "sign_in") { ACC = null; return lost(); }
    if (e.code === "jobs_active") { update(() => { ACC = null; }); await activeJobs(); return; }
    // Files that did not go, or an answer that never came: the account is
    // there, and the next press goes on from what is left.
    update(() => { ACC = e.code === "storage_unavailable" || e.code === "network" || e.status >= 500 ? "fail" : null; });
    if (!ACC) toast(t("prefs.not_deleted"));
    return;
  }
  ACC = null;
  for (const p of Object.keys(KS)) { clearInterval(KS[p].tick); delete KS[p]; }
  closeSheets(true);
  forget();
  toast(t("prefs.deleted"), { ms: 6000 });
}
// What still runs: the waiting jobs can be cancelled; the started ones finish.
async function activeJobs() {
  let jobs = [];
  try { jobs = (await api("/api/jobs?limit=100")).jobs || []; } catch {}
  const queued = jobs.filter((j) => j.status === "queued"), running = jobs.some((j) => j.status === "running");
  if (!queued.length) {
    await ask({ title: t("prefs.wait_q"), text: t("prefs.wait_started"), yes: t("prefs.got_it"), one: true });
    return;
  }
  if (!(await ask({ title: t("prefs.wait_q"), text: t("prefs.wait_queued"), yes: t("prefs.cancel_waiting"), no: t("prefs.wait") }))) return;
  await Promise.all(queued.map((j) => api(`/api/jobs/${j.id}/cancel`, { method: "POST" }).catch(() => null)));
  // Nothing else running: the deletion the person agreed to goes on.
  if (!running) { update(() => { ACC = "fail"; }); return deleteAccount(); }
  await ask({ title: t("prefs.wait_q"), text: t("prefs.wait_started"), yes: t("prefs.got_it"), one: true });
}
// The session ended under the sheet.
function lost() {
  closeSheets(true);
  forget();
  toast(t("err.sign_in"), { ms: 6000 });
}

export function renderPrefs() {
  const body = $("#prefsBody");
  if (!body || !S.me) return;
  // A form being typed in keeps its focus across a redraw.
  const typing = document.activeElement?.closest?.("#prefsBody .kform")?.dataset.k;
  const st = S.me.storage;
  const lang0 = S.me.language || lang;
  const sel = h("select", { class: "pick", "aria-label": t("prefs.lang_row"), disabled: S.busy, onchange: (e) => setLanguage(e.target.value) },
    [["", t("prefs.lang_auto")], ...LANGS].map(([c, n]) => h("option", { value: c, selected: c === lang0, lang: c || null }, n)));
  const accSub = ACC === "busy" ? t("prefs.deleting") : ACC === "fail" ? warn(t("prefs.delete_failed")) : t("prefs.delete_sub");
  const free = [freeRow("image"), freeRow("video")].filter(Boolean);
  body.replaceChildren(
    h("div", { class: "group", "data-k": "g-keys" }, t("prefs.keys")),
    ...PROVIDERS.flatMap(keyRows),
    h("p", { class: "note", "data-k": "n-keys" }, t("prefs.keys_note")),
    free.length ? h("div", { class: "group", "data-k": "g-free" }, t("prefs.free")) : null, ...free,
    h("div", { class: "group", "data-k": "g-cur" }, t("prefs.currency")),
    h("div", { class: "seg wrapping cur", role: "radiogroup", "aria-label": t("prefs.currency"), "data-seg": "cur", "data-dur": 360, "data-k": "s-cur" },
      CURRENCIES.map((c) => opt({ key: "cur", v: c, on: S.me.currency === c, pick: () => setCurrency(c) }, c, h("small", {}, curName(c))))),
    h("p", { class: "note", "data-k": "n-cur" }, t("prefs.currency_note")),
    h("div", { class: "group", "data-k": "g-lang" }, t("prefs.language")),
    sheetRow("lang", ic("info"), t("prefs.lang_row"), t("prefs.lang_sub"), sel),
    h("div", { class: "group", "data-k": "g-acc" }, t("prefs.account")),
    st ? sheetRow("store", ic("stack"), t("prefs.stored", { n: fmtNum(st.files) }), t("prefs.stored_sub", { size: size(st.bytes) })) : null,
    S.mini ? null : sheetRow("out", ic("out"), t("prefs.signed_tg"), t("prefs.signed_sub"),
      h("button", { type: "button", onclick: async () => { await signOut(); if (!S.signed) closeSheets(); } }, t("auth.signout"))),
    sheetRow("del", ic("trash"), t("prefs.delete"), accSub,
      h("button", { type: "button", class: "danger", disabled: ACC === "busy", onclick: deleteAccount }, t(ACC === "busy" ? "prefs.deleting_btn" : ACC === "fail" ? "prefs.delete_again" : "prefs.delete_btn"))));
  placeSegs($("#prefs"));
  if (typing) $(`#prefsBody [data-k="${typing}"] input`)?.focus({ preventScroll: true });
}
