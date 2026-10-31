// The library and the viewer. The library is every run of the person's,
// newest first by day, filtered by kind and searched by prompt
// (GET /api/jobs); the viewer is one run on a sheet, at /r/<job id>, with
// what can be done with it. The page's views and their addresses live here
// too: / is the composer, /library the library. The Mini App keeps its
// address and walks back with Telegram's back button instead.
import { $, $$, h, ic, isWide, isPhone } from "./dom.js";
import { anim, morph, opt, placeSegs, leadSeg, motion, OUT } from "./motion.js";
import { t, lang, fmtNum, fmtSecs } from "./i18n.js";
import { api, isFinal } from "./api.js";
import { C, vname, famLogo, lg, show, SICON, SHOWN, factEl, ratioNum, ratioOfSize } from "./catalog.js";
import { S } from "./state.js";
import { failure } from "./errors.js";
import { known, priceEl, priceText, onPrices } from "./price.js";
import { thumbMedia } from "./clips.js";
import { openSheet, closeSheets, toast, ask } from "./ui.js";
import { view, B, ticking, renderStage, refit } from "./stage.js";
import { runOfJob, again, removeJob, signIn, shownOf, onLeave, copyPrompt, refreshMe } from "./run.js";

/* Views and addresses. */

const RUN = /^\/r\/([1-9]\d{0,17})$/;
function setPath(path, how, state = null) {
  if (S.mini || how === "none" || (how === "push" && location.pathname === path)) return;
  history[how === "push" ? "pushState" : "replaceState"](state, "", path);
}

// go shows one view: "create" or "library". how is what it does to the
// address: push it, replace it, or leave it (a load, the back button).
export function go(v, how = "push") {
  if (S.view !== v) {
    S.view = v;
    $("#app").dataset.view = v;
    $("#create").hidden = v !== "create";
    $("#library").hidden = v !== "library";
    if (v === "library") showLibrary();
    else { placeSegs($("#create")); renderStage(); refit(); }
    if (how !== "none") {
      scrollTo(0, 0);
      anim($(v === "library" ? "#library" : "#create"), [{ opacity: 0, transform: "translateY(8px)" }, { opacity: 1, transform: "none" }], { duration: 280 });
    }
    window.dispatchEvent(new Event("framy:view"));
    polling();
  }
  setPath(v === "library" ? "/library" : "/", how);
}

// route shows what the address names, at load and on back and forward.
export function route() {
  const p = location.pathname, m = RUN.exec(p);
  closeFull(true);
  if (m) {
    go("library", "none");
    if (V.id !== +m[1]) openRun(+m[1], !!history.state?.lib);
    return;
  }
  if (viewerOpen()) { V.quiet = true; V.pushed = false; closeSheets(); }
  go(p === "/library" ? "library" : "create", "none");
}
addEventListener("popstate", () => { if (!S.mini) route(); });

// The tabs in the header, for whoever is signed in.
export function tabs() {
  if (!S.signed) return null;
  const tab = (v, icon, label, href) => h("a", { class: "tab", href, "data-v": v, "aria-current": S.view === v ? "page" : null,
    onclick: (e) => { if (!S.mini && (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey)) return; e.preventDefault(); leadSeg("view"); go(v); } },
  ic(icon), h("span", { class: "t" }, label));
  return h("nav", { class: "tabs", "data-seg": "view", "aria-label": t("tab.label") },
    tab("create", "spark", t("tab.create"), "/"), tab("library", "grid", t("tab.library"), "/library"));
}

// A run of the library that goes back to the composer (Again, Edit, Continue)
// takes the page there first.
onLeave(() => { closeFull(true); if (viewerOpen()) { V.quiet = true; closeSheets(true); } go("create"); });

/* The library. */

const PAGE = 30;
// busy is "page" while the first page loads, "more" while the next one does.
const L = { kind: "all", q: "", jobs: [], next: 0, busy: "", loaded: false, err: "", more: "", gen: 0, at: 0, any: false, dirty: false };
const TILES = new Map();
let Q = null;

export function mountLibrary() {
  return h("main", { class: "library", id: "library", hidden: true, "aria-label": t("tab.library") });
}

const matches = (j) => (L.kind === "all" || j.kind === L.kind) && (!L.q || (j.prompt || "").toLocaleLowerCase().includes(L.q.toLocaleLowerCase()));
const shown = () => L.jobs.filter(matches);
const libOn = () => S.view === "library";

function params(more) {
  const p = new URLSearchParams({ limit: String(PAGE) });
  if (L.kind !== "all") p.set("kind", L.kind);
  if (L.q) p.set("q", L.q);
  if (more) p.set("before", String(L.next));
  return p;
}
const stamp = (js) => { const now = Date.now(); for (const j of js) j._at = now; return js; };

// The first page for what the library shows now, or with more the next one.
async function load(more = false) {
  if (!S.signed || (more && (L.busy || !L.next))) return;
  const gen = more ? L.gen : ++L.gen;
  L.busy = more ? "more" : "page"; L.more = "";
  if (more) footer();
  try {
    const r = await api("/api/jobs?" + params(more));
    if (gen !== L.gen) return;
    const got = stamp(r?.jobs || []);
    L.busy = "";
    swap(() => {
      L.jobs = more ? L.jobs.concat(got.filter((j) => !L.jobs.some((x) => x.id === j.id))) : got;
      L.next = r?.next || 0; L.loaded = true; L.err = "";
      if (!more) L.at = Date.now();
      if (got.length) L.any = true;
    });
  } catch (e) {
    if (gen !== L.gen) return;
    L.busy = "";
    if (e.status === 401) { refreshMe(); return; }
    if (more) { L.more = e.code; footer(); } else swap(() => { L.err = e.code; L.loaded = true; });
  }
}
// Back in the library: the newest page is read again, so runs made in the bot
// or in another tab show up, and the older pages already read stay.
async function refresh() {
  if (!L.loaded || L.err || L.busy) return load();
  const gen = L.gen;
  try {
    const r = await api("/api/jobs?" + params(false));
    if (gen !== L.gen || L.busy) return;
    const got = stamp(r?.jobs || []), low = r?.next ? got.at(-1).id : 0, older = L.jobs.filter((j) => j.id < low);
    swap(() => { L.jobs = got.concat(older); L.next = older.length ? L.next : r?.next || 0; L.at = Date.now(); if (got.length) L.any = true; });
  } catch (e) { if (e.status === 401) refreshMe(); }
}

const LIBMORPH = { dur: 360,
  enter: (el, i, wait) => anim(el, [{ opacity: 0, transform: "translateY(10px)" }, { opacity: 1, transform: "none" }], { duration: 300, delay: wait + Math.min(i, 12) * 24, fill: "backwards" }),
  leave: (g) => anim(g, [{ opacity: 1 }, { opacity: 0 }], { duration: 160, easing: OUT, fill: "forwards" }) };
// A change of the list: shown at once where the library is in view, else when it is next.
function swap(fn) {
  if (libOn() && !$("#library").hidden) morph($("#library"), () => { fn(); renderLibrary(); }, LIBMORPH);
  else { fn(); L.dirty = true; }
  if (viewerOpen()) nav();
  polling();
}

function showLibrary() {
  renderLibrary();
  if (!S.signed) return;
  if (!L.loaded || L.err) load();
  else if (Date.now() - L.at > 15000) refresh();
}

export function libMe() {
  if (!S.signed) {
    L.gen++;
    Object.assign(L, { jobs: [], next: 0, busy: "", loaded: false, err: "", more: "", at: 0, any: false });
    TILES.clear();
    if (viewerOpen()) { V.quiet = true; closeSheets(true); setPath("/library", "replace"); }
  }
  if (libOn()) { renderLibrary(); if (S.signed && !L.loaded && !L.busy) load(); }
  polling();
}

function renderLibrary() {
  const root = $("#library");
  if (!root) return;
  L.dirty = false;
  const list = shown(), focused = Q && document.activeElement === Q.input;
  let body;
  if (!S.signed) body = [signedOut()];
  else if (!L.loaded || (!list.length && L.busy === "page")) body = [loading()];
  else if (L.err && !list.length) body = [slim(t("lib.failed"), "again", t("act.retry"), () => { swap(() => { L.err = ""; L.loaded = false; }); load(); })];
  else if (!list.length) body = [empty()];
  else body = [...days(list).map(dayEl), foot(list)];
  const tools = S.signed && L.loaded && (L.any || L.kind !== "all" || !!L.q);
  root.replaceChildren(h("div", { class: "libhead", "data-k": "lhead" }, h("h1", {}, t("tab.library")), tools ? kinds() : null, tools ? searchBox() : null), ...body);
  placeSegs(root);
  watchFoot();
  if (focused) Q.input.focus({ preventScroll: true });
}

function kinds() {
  return h("div", { class: "seg", role: "radiogroup", "aria-label": t("lib.show"), "data-seg": "lib" },
    [["all", "grid", t("lib.all")], ["image", "image", t("lib.images")], ["video", "video", t("lib.videos")]].map(([k, i, label]) =>
      opt({ key: "lib", v: k, on: L.kind === k, pick: () => setKind(k) }, h("span", { class: "row" }, ic(i), label))));
}
function setKind(k) {
  if (L.kind === k) return;
  // What is read already shows at once; the server's page follows.
  swap(() => { L.kind = k; L.next = 0; L.busy = "page"; });
  load();
}
// The search field is one element kept across renders, so typing never loses it.
function searchBox() {
  if (Q) return Q.box;
  const input = h("input", { type: "search", class: "lq", placeholder: t("lib.search"), "aria-label": t("lib.search"), autocomplete: "off", spellcheck: "false", maxlength: "200", enterkeyhint: "search" });
  const clr = h("button", { type: "button", class: "clr", "aria-label": t("lib.clear"), title: t("lib.clear"), tabindex: "-1", onclick: () => { input.value = ""; find(""); input.focus(); } }, ic("close"));
  const box = h("label", { class: "lsearch" }, ic("search"), input, clr);
  let tm = 0;
  input.addEventListener("input", () => { box.classList.toggle("has", !!input.value); clearTimeout(tm); tm = setTimeout(() => find(input.value), 250); });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { clearTimeout(tm); find(input.value); }
    else if (e.key === "Escape" && input.value) { e.preventDefault(); clearTimeout(tm); input.value = ""; find(""); }
  });
  Q = { box, input };
  return box;
}
function find(v) {
  Q?.box.classList.toggle("has", !!v);
  const q = v.trim();
  if (q === L.q) return;
  swap(() => { L.q = q; L.next = 0; L.busy = "page"; });
  load();
}

// Days: today and yesterday by name, the others by date, with the year when it is another.
const fmts = new Map();
const fmtOf = (k, make) => { let f = fmts.get(k); if (!f) fmts.set(k, (f = make())); return f; };
const dayStart = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
function dayName(d) {
  const now = new Date(), ago = Math.max(0, Math.round((dayStart(now) - dayStart(d)) / 864e5));
  if (ago < 2) {
    const s = fmtOf("r" + lang, () => new Intl.RelativeTimeFormat(lang, { numeric: "auto" })).format(-ago, "day");
    return s.charAt(0).toLocaleUpperCase(lang) + s.slice(1);
  }
  const year = d.getFullYear() !== now.getFullYear();
  return fmtOf("d" + lang + year, () => new Intl.DateTimeFormat(lang, { day: "numeric", month: "long", ...(year ? { year: "numeric" } : {}) })).format(d);
}
function days(list) {
  const out = [];
  for (const j of list) {
    const d = new Date(j.created_at), k = d.toDateString(), g = out.at(-1);
    if (g && g.k === k) g.jobs.push(j); else out.push({ k, d, jobs: [j] });
  }
  return out;
}
const dayEl = (g) => h("section", { class: "lday" }, h("h2", { "data-k": "ld-" + g.k }, dayName(g.d)), h("div", { class: "libgrid" }, g.jobs.map(tile)));

const nameOf = (id) => String(id || "").split("/").pop();
const coverOf = (j) => { const r = j.results?.[0]; return r ? r.thumb?.url || (j.kind === "video" ? "" : r.url || "") : ""; };
const sigOf = (j) => [j.status, j.results?.length || 0, coverOf(j), j.results?.[0]?.url ? 1 : 0, j.cost?.label || "", j.error?.class || "", j.retry_at || "", j.prompt].join("|");
// What the line under a tile says after the model: the price, or where the run stands.
function said(j) {
  switch (j.status) {
    case "done": return j.cost?.label || (j.free ? t("bar.free") : j.estimate && j.estimate.kind !== "unknown" ? priceText(j.estimate) : "");
    case "queued": return t(j.retry_at ? "stage.retry" : "stage.queued");
    case "running": return t("stage.running");
    case "cancelled": return t("stage.cancelled");
    default: return failure(j).label;
  }
}
// A tile keeps its element while the run is the same, so its picture never
// loads twice; one that left the list (morph takes its key) is made anew.
function tile(j) {
  const sig = sigOf(j), old = TILES.get(j.id);
  if (old && old._sig === sig && old.dataset.k && !old.inert) return old;
  const m = C.byId[j.model], f = m && C.famOf[m.id];
  const a = h("a", { class: "li", href: "/r/" + j.id, "data-k": "li-" + j.id,
    onclick: (e) => { if (!S.mini && (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey)) return; e.preventDefault(); openView(L.jobs.find((x) => x.id === j.id) || j, true); } },
  cover(j), h("p", { class: "p" }, j.prompt || t("kind." + j.kind)),
  h("span", { class: "m" }, f ? lg(famLogo(f), f.maker, "s") : null,
    h("span", {}, [m ? vname(m) : nameOf(j.model), said(j), j.surface === "guest" ? t("lib.guest") : ""].filter(Boolean).join(" · "))));
  a._sig = sig;
  TILES.set(j.id, a);
  return a;
}
function cover(j) {
  const res = j.results?.[0], vid = j.kind === "video";
  if (j.status === "done" && res) {
    const src = coverOf(j), kids = [];
    if (src) {
      const im = h("img", { src, alt: "", loading: "lazy", decoding: "async" });
      // A link read more than an hour ago no longer opens: the run is read again for new ones.
      im.addEventListener("error", () => relink(j.id), { once: true });
      if (!im.complete) im.addEventListener("load", () => anim(im, [{ opacity: 0 }, { opacity: 1 }], { duration: 240 }), { once: true });
      kids.push(im);
    } else kids.push(h("span", { class: "c" }, ic(vid ? "film" : "image"), res.url ? null : h("small", {}, t("lib.gone"))));
    if (vid && res.seconds) kids.push(h("span", { class: "badge" }, ic("play"), fmtSecs(res.seconds)));
    if (j.results.length > 1) kids.push(h("span", { class: "badge n" }, "×" + fmtNum(j.results.length)));
    return h("span", { class: "cover" }, kids);
  }
  const wait = j.status === "queued" || j.status === "running", stop = j.status === "cancelled";
  return h("span", { class: "cover none" + (wait ? " wait" : " fail") }, h("i"), h("i"), h("i"), h("i"),
    h("span", { class: "c" }, ic(wait ? "clock" : stop ? "stop" : "warn"), h("b", {}, said(j))));
}
const relinked = new Set();
async function relink(id) {
  if (relinked.has(id)) return;
  relinked.add(id);
  try { took(await api(`/api/jobs/${id}`), true); } catch (e) { if (e.status === 404) gone(id); }
}

const frame = () => h("div", { class: "lframe", "aria-hidden": "true" }, h("i"), h("i"), h("i"), h("i"));
const slim = (text, icon, label, fn) => h("div", { class: "lempty slim", "data-k": "lempty" },
  h("div", { class: "t" }, h("p", {}, text), h("button", { type: "button", class: "x", onclick: fn }, ic(icon), label)));
function signedOut() {
  return h("div", { class: "lempty", "data-k": "lempty" }, frame(), h("div", { class: "t" }, h("h2", {}, t("lib.signin_title")), h("p", {}, t("lib.signin_text")),
    h("button", { type: "button", class: "go", onclick: signIn }, ic("send"), t("auth.signin"))));
}
function empty() {
  if (L.q) return slim(t("lib.none_q", { q: L.q }), "close", t("lib.reset"), () => { if (Q) Q.input.value = ""; find(""); });
  if (L.kind !== "all") return slim(t(L.kind === "video" ? "lib.none_videos" : "lib.none_images"), "grid", t("lib.show_all"), () => setKind("all"));
  return h("div", { class: "lempty", "data-k": "lempty" }, frame(), h("div", { class: "t" }, h("h2", {}, t("lib.empty_title")), h("p", {}, t("lib.empty_text")),
    h("button", { type: "button", class: "go", onclick: () => { go("create"); if (!isPhone()) $("#prompt").focus(); } }, ic("spark"), t("go.create"))));
}
function spinner() {
  const s = h("i", { class: "lspin", "aria-hidden": "true" });
  anim(s, [{ transform: "rotate(0deg)" }, { transform: "rotate(360deg)" }], { duration: 900, iterations: Infinity, easing: "linear" });
  return s;
}
const loading = () => h("div", { class: "libmore", "data-k": "lload", role: "status" }, spinner(), t("lib.loading"));
// The end of the list: where the next page loads, or a word that there is no more.
function foot(list) {
  const el = (...kids) => h("div", { class: "libmore", "data-k": "lmore", role: "status" }, ...kids);
  if (L.busy) return el(spinner(), t(L.busy === "more" ? "lib.more" : "lib.loading"));
  if (L.more) return el(t("lib.more_failed"), h("button", { type: "button", class: "x", onclick: () => load(true) }, ic("again"), t("act.retry")));
  if (L.next) return el();
  return list.length > PAGE ? el(t("lib.end")) : null;
}
function footer() {
  const old = $("#library .libmore"), list = shown();
  if (!old || !list.length || !libOn()) return;
  const nx = foot(list);
  if (nx) old.replaceWith(nx); else old.remove();
  watchFoot();
}
const io = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) load(true); }, { rootMargin: "0px 0px 600px 0px" });
function watchFoot() {
  io.disconnect();
  const f = $("#library .libmore");
  if (f && L.next && !L.busy && libOn()) io.observe(f);
}

/* Fresh states of the runs: from the stage's own run as it goes, and read
   every 5 seconds for the ones still going that the stage does not follow. */

// took puts a new state of a job in the list and the viewer. quiet: a job
// read again, never one to add.
function took(j, quiet = false) {
  if (!j?.id) return;
  j._at ??= Date.now();
  const i = L.jobs.findIndex((x) => x.id === j.id);
  let changed = false;
  if (i >= 0) { changed = sigOf(L.jobs[i]) !== sigOf(j); L.jobs[i] = j; }
  else if (L.loaded && !quiet && (!L.jobs.length || j.id > L.jobs[0].id)) { L.jobs.unshift(j); changed = matches(j); if (j.status) L.any = true; }
  if (V.j?.id === j.id) {
    const was = sigOf(V.j);
    V.j = j; V.run.job = j;
    if (was !== sigOf(j)) fill();
  }
  if (changed) swap(() => {});
  else polling();
}
addEventListener("framy:job", (e) => took(e.detail));

let pollT = 0, polling_ = false;
function actives() {
  const own = S.run?.phase === "job" && !isFinal(S.run.job?.status) ? S.run.job.id : 0, out = new Map();
  for (const j of [V.j, ...(libOn() ? shown() : [])]) if (j && !isFinal(j.status) && j.id !== own) out.set(j.id, j);
  return [...out.values()].slice(0, 6);
}
function polling() {
  const on = S.signed && (libOn() || viewerOpen()) && actives().length > 0;
  if (on && !pollT) pollT = setInterval(poll, 5000);
  else if (!on && pollT) { clearInterval(pollT); pollT = 0; }
}
async function poll() {
  if (document.hidden || polling_) return;
  polling_ = true;
  try {
    for (const j of actives()) {
      try { took(await api(`/api/jobs/${j.id}`), true); }
      catch (e) { if (e.status === 404) gone(j.id); if (e.status === 401) { refreshMe(); break; } }
    }
  } finally { polling_ = false; polling(); }
}

// A run that is no longer there: deleted here or elsewhere. The viewer goes on to the next one.
function gone(id) {
  const list = shown(), at = list.findIndex((x) => x.id === id), nx = list[at + 1] || list[at - 1];
  TILES.delete(id);
  swap(() => { L.jobs = L.jobs.filter((x) => x.id !== id); });
  if (V.id !== id) return;
  closeFull(true);
  if (nx && viewerOpen()) showIn(nx);
  else if (viewerOpen()) closeSheets();
}

/* The viewer. */

const V = { id: 0, j: null, run: null, i: 0, pushed: false, quiet: false, want: 0 };
const viewerOpen = () => !!V.id && !$("#viewer").hidden;

export function mountViewer() {
  const el = h("section", { class: "sheet wide", id: "viewer", hidden: true, role: "dialog", "aria-modal": "true", "aria-labelledby": "viewTitle" },
    h("header", {}, h("h3", { id: "viewTitle" }), h("div", { class: "vnav", id: "vnav" }), h("button", { type: "button", class: "x", "data-close": "" }, t("view.close"))),
    h("div", { class: "body", id: "viewBody" }));
  el.addEventListener("closed", closed);
  return el;
}
function closed() {
  const quiet = V.quiet;
  V.quiet = false;
  closeFull(true);
  $("#viewBody").replaceChildren();
  V.id = 0; V.j = null; V.run = null;
  polling();
  if (quiet) return;
  // Closed by its own button: the address goes back to the library.
  if (V.pushed) { V.pushed = false; history.back(); } else setPath("/library", "replace");
}

// The run with id, at its own address: from the list read already, else read now.
async function openRun(id, pushed) {
  if (!S.signed) return;
  const have = L.jobs.find((j) => j.id === id);
  if (have) { openView(have, false, pushed); return; }
  V.want = id;
  try {
    const j = await api(`/api/jobs/${id}`);
    if (V.want !== id || !libOn()) return;
    j._at = Date.now();
    openView(j, false, pushed);
  } catch (e) {
    if (V.want !== id) return;
    if (e.status === 404) { toast(t("view.not_found")); setPath("/library", "replace"); }
    else if (e.status === 401) refreshMe();
    else toast(e.code === "network" ? t("err.network") : t("lib.failed"));
  } finally { if (V.want === id) V.want = 0; }
}
function openView(j, push, pushed = push) {
  V.want = 0;
  V.id = j.id; V.j = j; V.run = runOfJob(j); V.i = 0; V.pushed = !S.mini && pushed;
  if (push) setPath("/r/" + j.id, "push", { lib: 1 });
  fill();
  openSheet("#viewer");
  size();
  fresh();
  polling();
}
// Another run in the open viewer: the next one, the one before, or the one after a deleted one.
function showIn(j, d = 0) {
  V.id = j.id; V.j = j; V.run = runOfJob(j); V.i = 0;
  setPath("/r/" + j.id, "replace", V.pushed ? { lib: 1 } : null);
  fill();
  if (d) anim($("#viewBody").firstChild, [{ opacity: 0, transform: `translateX(${d * 32}px)` }, { opacity: 1, transform: "none" }], { duration: 300 });
  fresh();
  polling();
}
// Links live 70 minutes: a run read long ago is read again before its file is opened.
function fresh() {
  const j = V.j;
  if (j && Date.now() - (j._at || 0) > 50 * 60e3) api(`/api/jobs/${j.id}`).then((x) => { x._at = Date.now(); took(x, true); }, (e) => { if (e.status === 404) gone(j.id); });
}

async function step(d) {
  const list = shown(), i = list.findIndex((x) => x.id === V.id), k = i + d;
  if (i < 0 || k < 0) return;
  if (k >= list.length) {
    if (!L.next) return;
    await load(true);
    if (shown().length <= k || !viewerOpen()) return;
    return step(d);
  }
  showIn(shown()[k], d);
}
addEventListener("keydown", (e) => {
  if (!viewerOpen() || !$("#ask").hidden || e.metaKey || e.ctrlKey || e.altKey || e.target.closest?.("input, textarea, select, video")) return;
  const rtl = document.dir === "rtl";
  if (e.key === "ArrowLeft") { e.preventDefault(); step(rtl ? 1 : -1); }
  else if (e.key === "ArrowRight") { e.preventDefault(); step(rtl ? -1 : 1); }
});

function nav() {
  const el = $("#vnav"), list = shown(), i = list.findIndex((x) => x.id === V.id);
  if (!el) return;
  if (i < 0 || (list.length < 2 && !L.next)) { el.replaceChildren(); return; }
  el.replaceChildren(
    h("button", { type: "button", class: "ib", "aria-label": t("view.prev"), title: t("view.prev"), disabled: i === 0, onclick: () => step(-1) }, ic("chevl")),
    h("span", { "aria-live": "polite" }, t("view.of", { i: fmtNum(i + 1), n: fmtNum(list.length) })),
    h("button", { type: "button", class: "ib", "aria-label": t("view.next"), title: t("view.next"), disabled: i === list.length - 1 && !L.next, onclick: () => step(1) }, ic("chev")));
}

function ratioOf(j) {
  const r = j.results?.[V.i] || j.results?.[0];
  if (r?.width && r?.height) return r.width / r.height;
  const s = j.settings || {};
  if (s.size && /x/.test(s.size)) return ratioNum(ratioOfSize(s.size));
  if (s.aspect_ratio && s.aspect_ratio !== "auto") return ratioNum(s.aspect_ratio);
  const f = j.inputs?.[0];
  return f?.width && f?.height ? f.width / f.height : j.kind === "video" ? 16 / 9 : 1;
}
// The result, or the frame with what the run is at, as on the stage but without the dots.
function mediaOf(j, v) {
  const res = v.res;
  if (v.done && res.url) {
    const vid = res.content_type?.startsWith("video/");
    const el = vid ? h("video", { src: res.url, poster: res.thumb?.url || null, controls: true, playsinline: true, loop: true, muted: true, autoplay: true })
      : h("img", { src: res.url, alt: j.prompt || "", decoding: "async" });
    // The thumbnail stands in while the original loads.
    const ph = !vid && res.thumb?.url ? h("img", { class: "ph", src: res.thumb.url, alt: "" }) : null;
    if (ph) el.addEventListener("load", () => ph.remove(), { once: true });
    if (!vid) el.addEventListener("click", () => openFull(V.i));
    const big = h("button", { type: "button", class: "vexp", "aria-label": t("full.open"), title: t("full.open"), onclick: () => openFull(V.i) }, ic("expand"));
    return h("div", { class: "vbox vfit" + (vid ? "" : " zoom") }, ph, el, big);
  }
  const st = v.status || h("div", { class: "c" }, ic(j.kind), h("b", {}, t("lib.gone")));
  return h("div", { class: "jbox vfit" + (v.fail ? " failed" : "") }, h("i"), h("i"), h("i"), h("i"), st);
}
// The largest box of the run's shape that fits beside (or, on a phone, over) what it says.
function size() {
  const box = $("#viewBody .vfit"), main = $("#viewBody .vmain");
  if (!box || !main || !main.clientWidth) return;
  const r = ratioOf(V.j), W = main.clientWidth, top = $("#viewer header").offsetHeight;
  const H = Math.max(180, isWide() ? innerHeight - top - 48 - ($("#viewBody .vpicks")?.offsetHeight || 0) : innerHeight * 0.56);
  let w = W, hh = W / r;
  if (hh > H) { hh = H; w = H * r; }
  box.style.width = Math.round(w) + "px";
  box.style.height = Math.round(hh) + "px";
}
addEventListener("resize", () => { if (viewerOpen()) size(); });

// The price of Again is the price now: the same run quoted again, with the files framy keeps by id.
function itemOf(j) {
  const ids = (j.inputs || []).filter((f) => f.url).map((f) => f.id), it = { offer: j.offer, pay: j.free ? "free" : "key", settings: j.settings || {}, files: [] };
  if (ids.length) it.inputs = ids;
  return it;
}
const ready = (it) => !!known(it) && !S.busy;
function againBtn(label, cls) {
  const j = V.j;
  if (!C.byId[j.model]) return null;
  const it = itemOf(j), b = B("again", label, () => againNow(it), cls);
  b.dataset.again = "";
  b._it = it;
  if (!j.free) b.querySelector(".t").append(" · ", priceEl(it));
  b.disabled = !ready(it);
  return b;
}
onPrices(() => { for (const b of $$("#viewBody [data-again], #full [data-again]")) b.disabled = !ready(b._it); });
function againNow(it) {
  const got = known(it), run = V.run;
  if (!got || S.busy || !run) return;
  const e = got.estimate;
  run.shown = run.pay === "free" ? null : shownOf(e);
  run.shownLabel = e ? priceText(e) : ""; run.priceLabel = e?.label || "";
  again(run);
}

async function del() {
  const j = V.j;
  if (!j || !(await ask({ title: t("view.del_q"), text: t("view.del_text") + (j.free ? " " + t("view.del_free") : ""), yes: t("view.delete") }))) return;
  try { await removeJob(j.id); }
  catch (e) {
    // storage_unavailable: the files did not go, so the run stays and a second press is safe.
    toast(e.code === "job_active" ? t("view.del_active") : e.code === "network" ? t("err.network") : e.code === "storage_unavailable" ? t("view.del_storage") : t("view.del_failed"));
    if (e.code === "job_active") api(`/api/jobs/${j.id}`).then((x) => took(x, true), () => {});
    return;
  }
  toast(t("view.deleted"));
  gone(j.id);
}

const zeroOf = (label) => (label || "$0").replace(/\d[\d.,]*/, "0");
function fill() {
  const j = V.j, run = V.run;
  if (!j) return;
  const m = C.byId[j.model];
  $("#viewTitle").textContent = t("kind." + j.kind);
  nav();
  const v = view(run, { res: V.i, again: againBtn });
  const cost = j.cost?.label ? j.cost.label : v.zero ? zeroOf(j.estimate?.label) : j.free ? t("bar.free") : j.estimate && !v.fail ? priceText(j.estimate) : "";
  const line = h("p", { class: "jline" }, [m ? vname(m) : nameOf(j.model), cost].filter(Boolean).join(" · "));
  const [text, note] = v.why || [];
  const why = text || note ? h("div", { class: "jwhy" }, h("p", {}, text, text && note ? " " : "", note ? h("span", { class: "note" }, note) : null)) : null;
  const acts = h("div", { class: "jacts acts" }, v.acts.filter(Boolean), isFinal(j.status) ? B("trash", t("view.delete"), del, "sq") : null);
  const prompt = j.prompt ? h("p", { class: "jprompt" }, h("span", { class: "tx" }, j.prompt),
    h("button", { type: "button", class: "cp", "aria-label": t("act.copy"), title: t("act.copy"), onclick: () => copyPrompt(j.prompt) }, ic("copy"))) : null;
  $("#viewBody").replaceChildren(h("div", { class: "vrun", "data-run": j.id },
    h("div", { class: "vmain" }, mediaOf(j, v), picks(j)),
    h("div", { class: "vside" }, line, why, acts, prompt, facts(j), inputs(j))));
  size();
  ticking();
}
// More than one result: small pictures under the big one pick which shows.
function picks(j) {
  const rs = j.status === "done" ? j.results || [] : [];
  if (rs.length < 2) return null;
  return h("div", { class: "vpicks", role: "group", "aria-label": t("stage.label") }, rs.map((r, i) =>
    h("button", { type: "button", "aria-pressed": String(i === V.i), "aria-label": t("view.result", { n: fmtNum(i + 1) }), onclick: () => { if (V.i !== i) { V.i = i; fill(); } } },
      r.thumb?.url || (r.url && !r.content_type?.startsWith("video/")) ? h("img", { src: r.thumb?.url || r.url, alt: "" }) : ic(j.kind))));
}
/* The whole screen for a run's results: a swipe or the arrows go between
   them, Escape or Close goes back to the viewer. */

let FULL = null;
export const fullOpen = () => !!FULL;
export function mountFull() {
  return h("div", { class: "full", id: "full", hidden: true, role: "dialog", "aria-modal": "true", "aria-label": t("full.label") });
}
const dirOf = () => (document.dir === "rtl" ? -1 : 1);
const fullAt = () => (FULL ? Math.round(Math.abs(FULL.strip.scrollLeft) / Math.max(1, FULL.strip.clientWidth)) : 0);
function fullGo(d) {
  if (!FULL) return;
  const k = Math.max(0, Math.min(FULL.rs.length - 1, fullAt() + d));
  FULL.strip.scrollTo({ left: dirOf() * k * FULL.strip.clientWidth, behavior: motion() ? "smooth" : "auto" });
}
function openFull(at = 0) {
  const j = V.j, rs = (j?.results || []).filter((r) => r.url);
  if (!rs.length || FULL) return;
  const box = $("#full"), n = rs.length, rtl = dirOf() < 0;
  const cells = rs.map((r) => {
    const vid = r.content_type?.startsWith("video/"), ratio = r.width && r.height ? r.width / r.height : ratioOf(j);
    const m = vid ? h("video", { src: r.url, poster: r.thumb?.url || null, controls: true, playsinline: true, loop: true, muted: true, preload: "metadata" })
      : h("img", { src: r.url, alt: j.prompt || "", decoding: "async" });
    return h("div", { class: "fcell" }, h("div", { class: "fimg", style: { "--r": String(ratio) } }, m));
  });
  const strip = h("div", { class: "fstrip" }, cells), cnt = h("span", { class: "cnt", "aria-live": "polite" });
  const nav = n > 1 ? [h("button", { type: "button", class: "fnav s", "aria-label": t("view.prev"), title: t("view.prev"), onclick: () => fullGo(-1) }, ic(rtl ? "chev" : "chevl")),
    h("button", { type: "button", class: "fnav e", "aria-label": t("view.next"), title: t("view.next"), onclick: () => fullGo(1) }, ic(rtl ? "chevl" : "chev"))] : [];
  const dots = n > 1 ? h("div", { class: "dots", "aria-hidden": "true" }, rs.map(() => h("i"))) : null;
  const acts = h("div", { class: "jacts acts" });
  FULL = { j, rs, strip, at: -1, back: document.activeElement };
  // What the strip shows now: the count, the dots, the arrows, the actions of that result, and only its video plays.
  const upd = () => {
    if (!FULL) return;
    const c = fullAt();
    if (c === FULL.at) return;
    FULL.at = c;
    cnt.textContent = n > 1 ? t("view.of", { i: fmtNum(c + 1), n: fmtNum(n) }) : t("kind." + j.kind);
    if (nav.length) { nav[0].disabled = c === 0; nav[1].disabled = c === n - 1; }
    dots?.querySelectorAll("i").forEach((e, i) => e.classList.toggle("on", i === c));
    acts.replaceChildren(...view(V.run, { res: j.results.indexOf(rs[c]), again: againBtn }).acts.filter(Boolean));
    cells.forEach((cell, i) => { const v = cell.querySelector("video"); if (v && i === c) v.play().catch(() => {}); else v?.pause(); });
  };
  let raf = 0;
  strip.addEventListener("scroll", () => { if (!raf) raf = requestAnimationFrame(() => { raf = 0; upd(); }); }, { passive: true });
  // A press beside the picture closes it where there is a mouse; on a phone it would end a swipe.
  strip.addEventListener("click", (e) => { if (matchMedia("(hover: hover)").matches && !e.target.closest(".fimg")) closeFull(); });
  box.replaceChildren(h("div", { class: "fbar" }, cnt, h("button", { type: "button", class: "x", onclick: () => closeFull() }, ic("close"), t("view.close"))),
    h("div", { class: "fmid" }, strip, nav), h("div", { class: "fbot" }, dots, acts));
  box.hidden = false;
  document.documentElement.classList.add("full-on");
  box.tabIndex = -1;
  box.focus({ preventScroll: true });
  strip.scrollLeft = dirOf() * at * strip.clientWidth;
  upd();
  anim(box, [{ opacity: 0 }, { opacity: 1 }], { duration: 200 });
  anim(cells[at]?.firstChild, [{ transform: "scale(.94)", opacity: 0.4 }, { transform: "none", opacity: 1 }], { duration: 320 });
}
export function closeFull(quick = false) {
  const box = $("#full");
  if (!FULL || !box) return;
  const back = FULL.back;
  FULL = null;
  box.querySelectorAll("video").forEach((v) => v.pause());
  const done = () => { if (FULL) return; box.hidden = true; box.replaceChildren(); document.documentElement.classList.remove("full-on"); if (!quick) back?.focus?.({ preventScroll: true }); };
  const a = quick ? null : anim(box, [{ opacity: 1 }, { opacity: 0 }], { duration: 160, fill: "forwards" });
  if (a) a.finished.then(done, done); else done();
}
// Caught before the viewer's own keys: Escape closes only this, the arrows go between results, not runs.
addEventListener("keydown", (e) => {
  if (!FULL || !$("#ask").hidden) return;
  if (e.key === "Escape") { e.stopPropagation(); e.preventDefault(); closeFull(); }
  else if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && !e.target.closest?.("video")) {
    e.stopPropagation(); e.preventDefault();
    const d = e.key === "ArrowLeft" ? -1 : 1;
    fullGo(dirOf() < 0 ? -d : d);
  }
}, true);
addEventListener("resize", () => { if (FULL) FULL.strip.scrollLeft = dirOf() * FULL.at * FULL.strip.clientWidth; });

// Where a run was made, when not on the site.
const WHERE = new Set(["mini_app", "dm", "group", "guest"]);
function facts(j) {
  const s = j.settings || {}, out = [];
  for (const n of SHOWN) if (s[n] != null && s[n] !== "") out.push(factEl([SICON[n] || "info", show(n, s[n])]));
  const when = fmtOf("w" + lang, () => new Intl.DateTimeFormat(lang, { dateStyle: "long", timeStyle: "short" })).format(new Date(j.created_at));
  const where = WHERE.has(j.surface) ? t("view.where_" + j.surface) : "";
  return [out.length ? h("div", { class: "facts vfacts" }, out) : null, h("p", { class: "vwhen" }, when, where ? h("br") : null, where)];
}
// The files the run took, the ones framy still keeps.
function inputs(j) {
  const fs = j.inputs || [];
  if (!fs.length) return null;
  return h("div", { class: "vins" }, h("span", { class: "gl" }, t("view.inputs")), h("div", { class: "gt" }, fs.map((f) => {
    const vid = String(f.content_type).startsWith("video/");
    return h("span", { class: "thumb" + (vid ? " clip" : "") }, thumbMedia({ kind: vid ? "video" : "image", poster: vid ? f.thumb?.url || "" : "", url: vid ? f.url : f.thumb?.url || f.url }),
      vid && f.seconds ? h("span", { class: "tag" }, ic("film"), fmtSecs(f.seconds)) : null);
  })));
}
