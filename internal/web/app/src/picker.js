// The model picker: recommended models first, then every family, by company
// or by price, with a search. On a wide screen the family under the pointer
// shows its card on the right.
import { $, $$, h, ic, isWide } from "./dom.js";
import { anim, rise, morph, opt, placeSegs, placeSeg, fly, ticker, EASE, OUT, SPRING } from "./motion.js";
import { t, fmtNum, byName } from "./i18n.js";
import { C, PNAME, vname, famText, famLogo, lg, provMark, freePlan, facts, refsOf, framesOf, needsImage, needsVideo, resRangeOf, durRangeOf, union, hasAudio, deprecation, fieldsOf, label } from "./catalog.js";
import { S, M, images, payOf, keyOk, settings, altItem, misfit, famMisfit, freePer } from "./state.js";
import { minEl, lowest, need } from "./price.js";
import { openSheet, closeSheets } from "./ui.js";
import { act, render, setModel } from "./composer.js";

let query = "";
const ROWS = (o = {}) => ({ dur: o.dur || 440, ease: EASE,
  enter: (el, i) => anim(el, [{ opacity: 0, transform: "translateY(10px)" }, { opacity: 1, transform: "none" }], { duration: 360, delay: 80 + Math.min(i, 12) * (o.step ?? 30), fill: "backwards" }),
  leave: (g) => anim(g, [{ opacity: 1, transform: "none" }, { opacity: 0, transform: "scale(.97)" }], { duration: 180, easing: OUT, fill: "forwards" }) });
const listUpdate = (fn, o) => morph($("#pkrows"), fn, ROWS(o));

export function mountPicker() {
  return h("section", { class: "sheet wide", id: "picker", hidden: true, "aria-label": t("pick.label"), role: "dialog", "aria-modal": "true" },
    h("header", {}, h("h3", { id: "pickTitle" }), h("button", { type: "button", class: "x", "data-close": "" }, t("pick.done"))),
    h("div", { class: "body" }, h("div", { class: "pk" },
      h("div", { class: "pkl" },
        h("div", { class: "pktools" },
          h("input", { class: "search", id: "pksearch", type: "search", placeholder: t("pick.search"), "aria-label": t("pick.search_label"), autocomplete: "off",
            oninput: (e) => listUpdate(() => { query = e.target.value; renderPicker(); }, { dur: 300, step: 15 }) }),
          h("div", { class: "seg", id: "pksort", role: "radiogroup", "aria-label": t("pick.order"), "data-seg": "sort", "data-dur": 380 })),
        h("div", { id: "pkrows" })),
      h("div", { class: "pkd", id: "pkd" }))));
}

// The cheapest way to run a model now: by the person's working keys if they have any for it.
function bestItems(m) {
  const ps = m.offers.map((o) => o.provider), mine = ps.filter(keyOk);
  return (S.signed && mine.length ? mine : ps).map((p) => altItem(m, p, settings(m, p, {})));
}
// The picker's prices, asked for while the page is idle, a few models at a time.
export function prefetchPicker() {
  const ms = C.models.filter((m) => C.famOf[m.id]), idle = window.requestIdleCallback || ((f) => setTimeout(() => f(null), 16));
  let i = 0;
  const step = (dl) => {
    const end = performance.now() + 8;
    while (i < ms.length && (dl ? dl.timeRemaining() > 2 : performance.now() < end)) bestItems(ms[i++]).forEach(need);
    if (i < ms.length) idle(step);
  };
  idle(step);
}

export function openPicker() {
  S.info = null; S.hover = null; query = "";
  $("#pksearch").value = "";
  $("#pickTitle").textContent = t(S.kind === "video" ? "pick.video" : "pick.image");
  // Built while hidden, so the sheet is laid out once, with its new rows.
  renderSort(false);
  renderPicker({ fresh: true });
  openSheet("#picker");
  $("#picker .body").scrollTop = 0;
  placeSegs($("#picker"));
}
function renderSort(place = true) {
  $("#pksort").replaceChildren(...[["maker", "grid", t("pick.by_maker")], ["price", "sort", t("pick.cheaper")]].map(([k, i, l]) =>
    opt({ key: "sort", v: k, on: S.sort === k, pick: () => listUpdate(() => { S.sort = k; renderSort(); renderPicker(); }, { dur: 560 }) }, ic(i), l)));
  if (place) placeSeg($("#pksort"));
}

let hoverT = 0;
const hoverFam = (f, wide, vid) => (e) => {
  if (!wide || e.pointerType !== "mouse" || S.info != null || (S.hover === f.idx && (!vid || S.detVar[f.idx] === vid))) return;
  clearTimeout(hoverT);
  hoverT = setTimeout(() => { S.hover = f.idx; if (vid) S.detVar[f.idx] = vid; renderDetail(); }, 90);
};
let found = [];
function clearSearch() { listUpdate(() => { query = ""; $("#pksearch").value = ""; renderPicker(); }, { dur: 300, step: 15 }); $("#pksearch").focus(); }
const focusId = () => S.model[S.kind];
const lowUsd = (f) => (f.cur.some((v) => freePlan(v.m)) ? 0 : Math.min(...f.cur.map((v) => { const e = lowest(bestItems(v.m)); return !e || e.kind === "unknown" ? Infinity : e.usd; })));

function renderPicker(o = {}) {
  const box = $("#pkrows"), wide = isWide(), ql = query.trim().toLowerCase();
  const fams = C.FAM.filter((f) => f.kind === S.kind && (!ql || famText(f).includes(ql)));
  found = [];
  const rows = [], ghead = (k, icon, title, cnt) => h("div", { class: "ghead", "data-k": "g-" + k }, icon, title, cnt ? h("span", { class: "cnt" }, cnt) : null);
  const count = (fs) => fmtNum(fs.reduce((a, f) => a + f.cur.length, 0));
  if (!ql) {
    const recKey = S.kind === "image" ? (images().length > 5 ? "many_photos" : "image") : "video";
    const rec = (C.recommend[recKey] || []).map((id) => C.byId[id]).filter((m) => m && C.famOf[m.id]);
    if (rec.length) rows.push(ghead("rec", ic("spark"), images().length > 5 && S.kind === "image" ? t("pick.rec_photos", { n: images().length }) : t("pick.rec")),
      h("div", { class: "recs", "data-k": "recs" }, rec.map((m) => recCard(m, wide))));
  }
  if (S.sort === "price") {
    const sorted = fams.slice().sort((a, b) => lowUsd(a) - lowUsd(b));
    found = sorted;
    if (sorted.length) rows.push(ghead("all", ic("sort"), ql ? t("pick.found") : t("pick.cheap_first"), count(sorted)), ...sorted.map((f) => famRow(f, wide)));
  } else {
    if (ql && fams.length) rows.push(ghead("all", ic("search"), t("pick.found"), count(fams)));
    for (const mk of [...new Set(fams.map((f) => f.maker))].sort(byName)) {
      const fs = fams.filter((f) => f.maker === mk).sort((a, b) => a.order - b.order);
      found.push(...fs);
      rows.push(ghead("m-" + mk, lg(famLogo(fs[0]).slice(-1), mk, "s"), mk, count(fs)), ...fs.map((f) => famRow(f, wide)));
    }
  }
  if (!fams.length) rows.push(h("div", { class: "pkempty", "data-k": "none" }, h("p", {}, t("pick.nothing", { q: query.trim() })),
    h("button", { type: "button", class: "x", onclick: clearSearch }, ic("close"), t("pick.reset"))));
  box.replaceChildren(...rows);
  if (o.fresh) rise(rows.flatMap((r) => (r.classList.contains("recs") ? [...r.children] : [r])).slice(0, 16), wide ? { x: 24, y: 0, step: 26, delay: 90, dur: 420 } : { y: 14, step: 26, delay: 90 });
  renderDetail();
  placeSegs($("#picker"));
}

const perWord = (f) => t(f.kind === "image" ? "pick.per_image" : "pick.per_video");
function famPrice(f) {
  const free = f.cur.some((v) => freePlan(v.m)), items = f.cur.flatMap((v) => bestItems(v.m));
  if (free) return h("span", { class: "fprice" }, h("b", {}, t("pay.free")), h("small", {}, minEl(items, { text: (e) => (e.kind === "unknown" ? "" : t("pick.or", { p: priceTextShort(e) })) })));
  return h("span", { class: "fprice" }, h("b", {}, minEl(items)), h("small", {}, minEl(items, { wait: "", text: (e) => (e.kind === "unknown" ? t("pick.no_price") : (e.discount ? "−" + Math.round(e.discount * 100) + "% · " : "") + perWord(f)) })));
}
const priceTextShort = (e) => (e.kind === "from" ? t("price.from", { p: e.label }) : e.kind === "about" ? t("price.about", { p: e.label }) : e.kind === "up_to" ? t("price.up_to", { p: e.label }) : e.label);
function recCard(m, wide) {
  const f = C.famOf[m.id], why = misfit(m), plan = freePlan(m);
  const reason = why || [plan ? freePer(m.kind) : "", ...facts(m, 4).filter((x) => x[0] !== "ticket").map((x) => x[1])].filter(Boolean).slice(0, 2).join(" · ");
  return h("button", { type: "button", class: "rcard" + (why ? " dim" : ""), "aria-current": String(focusId() === m.id), onclick: (e) => choose(C.varOf[m.id], e.currentTarget), onpointermove: hoverFam(f, wide, m.id) },
    lg(famLogo(f), f.maker), h("span", { class: "nm" }, vname(m)), h("span", { class: "why" }, reason), h("span", { class: "pr" }, plan ? t("pay.free") : minEl(bestItems(m))));
}
// A family is one row: the whole row picks it, the variants are pills inside it.
function famRow(f, wide) {
  const cur = focusId(), mine = C.famOf[cur] === f, why = famMisfit(f);
  const pick = C.varOf[mine ? cur : S.lastVar[f.idx]] || f.def;
  const sub = why ? [ic("warn"), why] : facts(f.cur.map((v) => v.m), 3).filter((x) => x[0] !== "ticket").map((x) => x[1]).join(" · ");
  const olds = S.sort === "price" || query.trim() ? f.old : [];
  const vars = f.cur.length > 1 || olds.length ? h("div", { class: "fvars" },
    f.cur.length > 1 ? f.cur.map((v) => h("button", { type: "button", class: "pill", "aria-pressed": String(v.id === cur), onclick: () => choose(v, $(`#pkrows [data-k="fam-${f.idx}"]`)), onpointermove: hoverFam(f, wide, v.id) }, v.label)) : null,
    olds.map((v) => h("button", { type: "button", class: "pill old", "aria-pressed": String(v.id === cur), title: t("pick.outdated", { v: v.by.label }), onclick: () => choose(v, $(`#pkrows [data-k="fam-${f.idx}"]`)) }, v.label))) : null;
  return h("div", { class: "frow" + (why ? " dim" : ""), "aria-current": String(mine), "data-k": "fam-" + f.idx, onpointermove: hoverFam(f, wide) },
    h("button", { type: "button", class: "fhit", "aria-label": t("pick.choose_name", { name: f.title }), onclick: (e) => choose(pick, e.currentTarget.parentNode) }),
    lg(famLogo(f), f.maker), h("span", { class: "fbody" }, h("span", { class: "nm" }, f.title), sub && sub.length ? h("span", { class: "fsub" + (why ? " why" : "") }, sub) : null),
    famPrice(f), vars);
}

let detFam = null;
function renderDetail() {
  const box = $("#pkd");
  if (!isWide()) { box.replaceChildren(); detFam = null; return; }
  const cur = C.famOf[focusId()], q = query.trim() && found.length && !found.includes(cur) ? found[0] : cur;
  const f = (S.hover != null ? C.FAM[S.hover] : null) || q;
  const ok = f && f.kind === S.kind, prev = detFam;
  detFam = ok ? f.idx : null;
  box.replaceChildren(ok ? detail(f) : "");
  if (ok && prev !== null && prev !== f.idx) anim(box.firstChild, [{ opacity: 0.4 }, { opacity: 1 }], { duration: 160 });
  placeSegs(box);
}

// Choosing a model: its mark flies from the row into the composer while the sheet slides away.
function choose(v, from) {
  const src = from?.querySelector(".lg"), r0 = src?.getBoundingClientRect();
  const m0 = M(), same = m0.id === v.id, old = vname(m0);
  act(() => { setModel(v.id); render(); });
  closeSheets();
  if (!same) {
    const dst = $$("#how .mcard .lg, #dtools .chipbtn .lg").find((el) => el.offsetParent);
    fly(src, r0, dst);
    ticker($$("#how .mname, #dtools .chipbtn .nm").find((el) => el.offsetParent), old, 1);
  }
}

function vlist(f, sel, pickIt) {
  const key = "dl-" + f.idx;
  return h("div", { class: "vlist", role: "radiogroup", "aria-label": t("var.label"), "data-seg": key, "data-dur": 380 }, f.cur.map((v) =>
    opt({ key, v: v.id, on: v.id === sel.id, cls: "vl", pick: () => pickIt(v) }, h("b", {}, v.label),
      h("span", { class: "p" }, freePlan(v.m) ? t("pay.free") : minEl(bestItems(v.m))), v.note ? h("span", { class: "n" }, v.note) : null)));
}
function detail(f) {
  const want = S.detVar[f.idx], cur = focusId();
  const sel = (want && C.famOf[want] === f && C.varOf[want]) || (C.famOf[cur] === f && C.varOf[cur]) || f.def, m = sel.m;
  const pickIt = (v) => { S.detVar[f.idx] = v.id; if (S.info == null) S.hover = f.idx; renderDetail(); rise($("#pkd .dl")?.children || [], { y: 4, step: 18, dur: 260 }); };
  const rows = [], plan = freePlan(m), dep = deprecation(m);
  if (f.cur.length < 2) rows.push(["coin", t("det.price_now"), plan && payOf(m) === "free" ? t("pay.free") : minEl(bestItems(m))]);
  if (plan) rows.push(["ticket", t("det.free"), freePer(m.kind)]);
  if (m.kind === "image") { const refs = refsOf(m); rows.push(["photos", t("det.photos_in"), needsImage(m) ? t("det.photos_need", { n: refs }) : refs ? t("det.up_to", { n: refs }) : t("det.none")]); }
  else {
    const fr = framesOf(m), refs = refsOf(m);
    if (fr) rows.push(["frames", t("det.frames"), t(fr > 1 ? "det.first_last" : "det.first")]);
    if (refs) rows.push(["photos", t("det.refs"), t("det.up_to", { n: refs })]);
    if (needsVideo(m)) rows.push(["film", t("det.clip"), t("det.clip_need")]);
  }
  if (m.traits?.faces) rows.push(["warn", t("det.people"), t(m.traits.faces === "adults" ? "det.people_adults" : "det.people_no")]);
  const r = resRangeOf(m.offers); if (r) rows.push(["res", t("det.resolution"), r[0] === r[1] ? r[0] : `${r[0]} – ${r[1]}`]);
  const ar = union(m, "aspect_ratio"), sz = union(m, "size");
  if (ar.length || sz.length) rows.push(["ratio", t("det.ratios"), ar.length ? (ar.includes("auto") ? t("det.ratios_auto", { n: ar.filter((x) => x !== "auto").length }) : String(ar.length)) : String(sz.length)]);
  if (m.kind === "video") {
    const d = durRangeOf([m]); if (d) rows.push(["clock", t("det.length"), d[0] === d[1] ? t("unit.s", { n: d[0] }) : t("fact.len", { a: d[0], b: d[1] })]);
    rows.push([hasAudio(m) ? "audio" : "mute", t("det.sound"), t(hasAudio(m) ? "det.yes" : "det.no")]);
  } else {
    const n = Math.max(0, ...m.offers.map((o) => o.params.n?.max || 0));
    rows.push(["stack", t("det.at_once"), n > 1 ? t("det.at_once_n", { n }) : t("det.at_once_one")]);
  }
  rows.push(["key", t("det.keys"), m.offers.map((o) => h("span", { class: "prov" }, provMark(o.provider), PNAME[o.provider] || o.provider))]);
  const sets = [...new Set(m.offers.flatMap(fieldsOf))];
  if (sets.length) rows.push(["sliders", t("det.settings"), sets.map(label).join(", ")]);
  const isCur = cur === m.id, why = misfit(m), many = f.vars.length > 1;
  const sub = [f.maker, f.cur.length > 1 ? t("pick.versions", { n: f.cur.length }) : ""].filter(Boolean).join(" · ");
  return h("div", { class: "det" },
    h("div", { class: "dhead" }, lg(famLogo(f), f.maker, "l"), h("div", {}, h("h4", {}, f.title), h("div", { class: "mk" }, sub))),
    f.cur.length > 1 ? vlist(f, sel, pickIt) : null,
    dep ? h("p", { class: "dwarn" }, ic("clock"), h("span", {}, dep.text + "."))
      : sel.by ? h("p", { class: "dwarn" }, ic("clock"), h("span", {}, t("var.outdated", { v: sel.by.label }) + " ", h("button", { type: "button", class: "link-t", onclick: () => pickIt(sel.by) }, t("pick.see", { v: sel.by.label })))) : null,
    m.description ? h("p", { class: "ddesc" }, m.description) : null,
    h("div", { class: "dl" }, rows.map(([i, k, v]) => h("div", {}, h("span", {}, ic(i), k), h("span", {}, v)))),
    f.old.length ? h("p", { class: "dold" }, t("pick.old") + " ", f.old.map((v, i) => [i ? ", " : "", h("button", { type: "button", "aria-pressed": String(v.id === sel.id), title: t("pick.newer", { v: v.by.label }), onclick: () => pickIt(v) }, v.label)])) : null,
    h("button", { type: "button", class: "choose", disabled: isCur, onclick: (e) => choose(sel, e.currentTarget.closest(".det")) },
      isCur ? t("pick.chosen") : (many ? t("pick.choose_v", { v: sel.label }) : t("pick.choose")) + (why ? ` (${why})` : "")));
}
