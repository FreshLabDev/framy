// The composer: what to make, the model, how to pay, the settings, the price
// and the one button. Every change goes through act(), which lets the blocks
// glide from where they stood to where they stand now.
import { $, $$, h, ic, isPhone, MAC } from "./dom.js";
import { anim, rise, spin, opt, placeSegs, placeSeg, fitSegs, snap, ticker, roll, follow, EASE, SPRING, OUT, motion } from "./motion.js";
import { t, fmtNum, fmtIn, fmtSecs } from "./i18n.js";
import { C, PNAME, vname, famLogo, lg, provMark, freePlan, pays, offerOf, facts, factEl, deprecation, fieldsOf, SECONDARY, SEARCH, SICON,
  label, show, ordered, ratioNum, ratioOfSize, sizeK, resScore, needsVideo } from "./catalog.js";
import { S, M, images, clip, payOf, usable, keyOk, connection, freeLeft, freeLimit, freesAt, freePer, settings, route, runItem, altItem, next, sourceRatio, quoteNow,
  maxSecs, needsCut, contFits } from "./state.js";
import { priceEl, priceText, known, need, onPrices, usd } from "./price.js";
import { renderStage, stageFrom, refit, runOver } from "./stage.js";
import { openPicker } from "./picker.js";
import { openSheet, closeSheets } from "./ui.js";
import { addFiles, removeFile } from "./files.js";
import { generate, signIn } from "./run.js";
import { openCut, thumbMedia, frameEl, looksLikeClip } from "./clips.js";

const FORM = { rel: true, dur: 380, after: 90,
  enter: (el, i, wait) => anim(el, [{ opacity: 0, transform: "translateY(6px)" }, { opacity: 1, transform: "none" }], { duration: 280, delay: 110 + wait + i * 30, fill: "backwards" }),
  leave: (g) => anim(g, [{ opacity: 1 }, { opacity: 0 }], { duration: 110, easing: OUT, fill: "forwards" }) };
const FILES = { dur: 360,
  enter: (el) => anim(el, [{ transform: "scale(.4) rotate(-6deg)", opacity: 0 }, { transform: "none", opacity: 1 }], { duration: 480, easing: SPRING }),
  leave: (g) => anim(g, [{ transform: "none", opacity: 1 }, { transform: "scale(.4)", opacity: 0 }], { duration: 220, easing: OUT, fill: "forwards" }) };

let ACTING = 0;
// Every change of the composer: the form's blocks glide, new ones rise, gone ones fade; the frame on the stage refits.
export function act(fn) {
  const how = $("#how"), host = $("#composer");
  const a = snap(how, FORM), b = snap($("#files"), FILES), c = follow(host), d = stageFrom();
  // A change after a result is the next run being set up: the stage shows its frame again.
  if (S.showJob && runOver(S.run)) S.showJob = false;
  ACTING++;
  try { fn(); renderStage(); } finally { ACTING--; }
  a(); b(); c(); d();
}

export function mountComposer() {
  return h("section", { class: "composer", id: "composer", "aria-label": t("composer.label") },
    h("div", { class: "dtools", id: "dtools" }),
    h("div", { class: "seg kind", role: "radiogroup", "aria-label": t("kind.label"), id: "kind", "data-seg": "kind", "data-dur": 460 }),
    h("div", { id: "pwrap" },
      h("label", { class: "sr", for: "prompt" }, t("prompt.label")),
      h("textarea", { id: "prompt", rows: 3, maxlength: 4000 }),
      h("div", { class: "ptools", id: "ptools" })),
    h("div", { class: "files", id: "files" }),
    h("input", { type: "file", id: "file", multiple: true, accept: "image/png,image/jpeg,image/webp,image/gif,image/avif,image/heic,video/mp4,video/quicktime,video/webm,video/x-matroska,.mkv,.mov,.m4v", hidden: true }),
    h("div", { class: "how", id: "how" }),
    h("div", { class: "bar", id: "bar" },
      h("div", { class: "price", "aria-live": "polite" }, h("b", { class: "pnum", id: "pnum" }), h("span", { id: "pcap" })),
      h("button", { type: "button", class: "go", id: "go" })));
}

export function wireComposer() {
  const p = $("#prompt");
  try { p.value = sessionStorage.getItem("framy.draft") || ""; } catch {}
  p.addEventListener("input", () => { fitPrompt(); renderBar(); try { sessionStorage.setItem("framy.draft", p.value); } catch {} });
  p.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return;
    e.preventDefault();
    const b = $("#go");
    if (!b.disabled) b.click(); else anim(b, [{ transform: "none" }, { transform: "translateX(-4px)" }, { transform: "translateX(4px)" }, { transform: "none" }], { duration: 240 });
  });
  $("#file").addEventListener("change", (e) => { addFiles([...e.target.files]); e.target.value = ""; });
  // Photos come in by drop or paste too.
  const comp = $("#composer");
  comp.dataset.drop = t("files.drop");
  let depth = 0;
  document.addEventListener("dragenter", (e) => { if (![...(e.dataTransfer?.types || [])].includes("Files")) return; depth++; comp.classList.add("drag"); });
  document.addEventListener("dragleave", () => { if (--depth <= 0) { depth = 0; comp.classList.remove("drag"); } });
  document.addEventListener("dragover", (e) => { if ([...(e.dataTransfer?.types || [])].includes("Files")) e.preventDefault(); });
  document.addEventListener("drop", (e) => {
    depth = 0; comp.classList.remove("drag");
    const fs = [...(e.dataTransfer?.files || [])];
    if (fs.length) { e.preventDefault(); addFiles(fs); }
  });
  document.addEventListener("paste", (e) => {
    const fs = [...(e.clipboardData?.files || [])].filter((f) => f.type.startsWith("image/") || looksLikeClip(f));
    if (fs.length) { e.preventDefault(); addFiles(fs); }
  });
  // A price can make a row of tiles stack, and its marker has to follow.
  onPrices(() => { renderBar(); for (const g of fitSegs($$("#how [data-seg]"))) placeSeg(g, false, true); });
  // A clip turned the composer to video: the form slides over as on a switch.
  addEventListener("framy:kind", (e) => anim($("#how"), [{ opacity: 0, transform: `translateX(${e.detail === "video" ? 36 : -36}px)` }, { opacity: 1, transform: "none" }], { duration: 420, easing: EASE }));
  // On a phone the composer is a dock: the page keeps room for it under the stage.
  addEventListener("resize", () => { if (howStale && howShown()) render(); });
  new ResizeObserver(() => { document.documentElement.style.setProperty("--dockh", comp.offsetHeight + "px"); if (isPhone()) refit(); }).observe(comp);
}

export function render() {
  const m = M();
  if (!m) return;
  const pay = payOf(m);
  // A part picked for a model that takes more than the next one does is picked again.
  if (S.cut && (!clip() || S.cut.seconds > maxSecs(m, pay) + 0.005)) S.cut = null;
  $("#kind").replaceChildren(...[["image", t("kind.image")], ["video", t("kind.video")]].map(([k, l]) =>
    opt({ key: "kind", v: k, on: S.kind === k, title: l, pick: () => setKind(k) }, ic(k), h("span", { class: "t" }, l))));
  $("#prompt").placeholder = contFits(m, pay) ? t("prompt.cont") : needsVideo(m) ? t("prompt.clip") : m.kind === "video" ? t("prompt.video") : images().length ? t("prompt.photo") : t("prompt.image");
  renderTools(m, pay);
  renderFiles(m, pay);
  if (howShown()) { renderHow(m, pay); howStale = false; } else howStale = true;
  renderBar();
  if (!ACTING) renderStage();
  placeSegs($("#composer"));
  placeSegs($("#tune"));
  quoteNow();
}

// Image and video: the form slides over in the direction of the switch.
function setKind(k) {
  const dir = k === "video" ? 1 : -1;
  act(() => { S.kind = k; render(); });
  anim($("#how"), [{ opacity: 0, transform: `translateX(${dir * 36}px)` }, { opacity: 1, transform: "none" }], { duration: 420, easing: EASE });
}

/* Phone: the model and the settings sit in one row over the prompt; the settings open in a sheet. */
function renderTools(m) {
  const f = C.famOf[m.id];
  $("#dtools").replaceChildren(
    h("button", { type: "button", class: "chipbtn", onclick: openPicker, "aria-haspopup": "dialog" }, lg(famLogo(f), f.maker, "o"), h("span", { class: "nm" }, h("span", {}, vname(m))), ic("chevd")),
    h("button", { type: "button", class: "chipbtn set", onclick: openTune, "aria-label": t("tune.title") }, ic("sliders"), h("span", {}, t("tune.short"))),
    h("button", { type: "button", class: "chipbtn att", onclick: () => $("#file").click(), "aria-label": t("files.add") }, ic("plus")));
}
// On a phone the settings live in a sheet: they are built when it opens, not
// on every change behind it.
let howStale = false;
const howShown = () => !isPhone() || $("#how").parentElement !== $("#composer");
export function openTune() {
  const body = $("#tuneBody");
  body.append($("#how"));
  if (howStale) { const m = M(); renderHow(m, payOf(m)); howStale = false; }
  openSheet("#tune");
  placeSegs($("#tune"));
}
export function tuneClosed() { const how = $("#how"); if (how.parentElement !== $("#composer")) $("#composer").insertBefore(how, $("#bar")); placeSegs($("#composer")); }

/* Files: photos and clips stay in the browser until Create; the ones the model will not take stand apart. */
function renderFiles(m, pay) {
  const box = $("#files"), R = route(m, pay), c = clip();
  box.replaceChildren();
  const thumb = (f, r) => h("div", { class: "thumb" + (r.go ? "" : " off") + (f.kind === "video" ? " clip" : ""), title: f.name + (r.why ? ": " + r.why : ""), "data-k": "f" + f.id },
    thumbMedia(f),
    f.kind === "video" ? h("span", { class: "tag" }, ic("film"), r.go && r.tag ? r.tag : f.secs ? fmtSecs(f.secs) : "") : null,
    h("button", { type: "button", class: "rm", "aria-label": t("files.remove"), onclick: () => act(() => { removeFile(f); render(); }) }, ic("close")));
  const go = [], off = [];
  S.files.forEach((f, i) => (R[i].go ? go : off).push(thumb(f, R[i])));
  const cont = contEl(m, pay);
  if (cont) box.append(cont);
  const max = R.filter((r) => r.go).length;
  if (!off.length) box.append(...go);
  else {
    if (go.length) box.append(h("div", { class: "fgroup", "data-k": "g-go" }, h("span", { class: "gl" }, t("files.going", { n: max })), h("div", { class: "gt" }, go)));
    box.append(h("div", { class: "fgroup out", "data-k": "g-off" }, h("span", { class: "gl" }, t("files.off")), h("div", { class: "gt" }, off)));
  }
  box.append(h("button", { type: "button", class: "addfile", "data-k": "f-add", onclick: () => $("#file").click() }, ic("plus"), t("files.add")));
  const why = [...new Set(R.filter((r) => !r.go).map((r) => r.why))];
  // The first clip's part: the one picked, or a button to pick it.
  const ci = c ? S.files.indexOf(c) : -1, cutting = ci >= 0 && R[ci].go && c.secs > 0;
  if (cutting) {
    const need = needsCut(m, pay), mx = maxSecs(m, pay);
    const what = S.cut ? t("files.cut_taken", { a: fmtNum(S.cut.start, { maximumFractionDigits: 1 }), b: fmtNum(S.cut.start + S.cut.seconds, { maximumFractionDigits: 1 }) })
      : need ? t("files.clip_len", { len: fmtSecs(c.secs), n: fmtNum(mx) }) : "";
    box.append(h("p", { class: "fhint cut", "data-k": "f-cut" }, what ? h("span", {}, what + " ") : null,
      h("button", { type: "button", class: "link-t", onclick: openCut }, t(S.cut ? "act.change_part" : "act.pick_part"))));
  }
  const note = why.length ? why.join(" ") : S.files.length ? (cutting ? "" : t("files.kept")) : cont ? "" : t("files.hint");
  if (note) box.append(h("p", { class: "fhint" + (why.length ? "" : " quiet"), "data-k": "f-hint" }, note));
  const none = !S.files.length && !cont;
  box.classList.toggle("nofiles", none);
  $("#pwrap").classList.toggle("tools", none);
  $("#ptools").replaceChildren(...(none ? [h("button", { type: "button", class: "pattach", onclick: () => $("#file").click() }, ic("plus"), t("files.add")), h("span", {}, t("files.or_drop"))] : []));
  fitPrompt();
}
// The video a run goes on from: its last frame as a still, and why it does
// not go when the model or the payment is another.
function contEl(m, pay) {
  const c = S.cont;
  if (!c || m.kind !== "video") return null;
  const fits = contFits(m, pay), other = C.byId[c.model];
  return h("div", { class: "fgroup cont" + (fits ? "" : " out"), "data-k": "g-cont" }, h("span", { class: "gl" }, t("cont.label")),
    h("div", { class: "gt" }, h("div", { class: "thumb clip" + (fits ? "" : " off"), "data-k": "f-cont" }, frameEl(c.url, Math.max(0, (c.secs || 0) - 0.05)),
      h("span", { class: "tag" }, ic("next"), c.secs ? fmtSecs(c.secs) : ""),
      h("button", { type: "button", class: "rm", "aria-label": t("files.remove"), onclick: () => act(() => { S.cont = null; render(); }) }, ic("close"))),
      h("p", { class: "fhint quiet" }, fits ? t("cont.hint") : t("cont.only", { model: other ? vname(other) : "" }))));
}
// The prompt is as tall as its text.
function fitPrompt() {
  const p = $("#prompt");
  if (!p.getClientRects().length) return;
  p.style.height = "auto";
  p.style.height = p.scrollHeight + "px";
}

/* The model card, the payment, the settings. */
function renderHow(m, pay) {
  const f = C.famOf[m.id];
  const head = h("button", { type: "button", class: "mhead", "aria-haspopup": "dialog", onclick: openPicker },
    lg(famLogo(f), f.maker),
    h("span", { class: "txt" }, h("span", { class: "mname" }, h("span", {}, vname(m))),
      h("span", { class: "facts" }, facts(m, 4).filter((x) => x[0] !== "ticket").slice(0, 3).map((x) => { const e = factEl(x); e.dataset.k = "fa-" + x[0]; return e; }))),
    ic("chev"));
  $("#how").replaceChildren(...[h("div", { class: "sec", "data-k": "h-model" }, h("div", { class: "mcard" }, head, variantBlock(m))), paySec(m, pay), settingsBlock(m, pay)].filter(Boolean));
}

// The price beside a choice: free is the ticket, a key's price is the quote.
const freeTag = () => h("span", { class: "free", title: t("pay.free") }, ic("ticket"), h("span", { class: "fw" }, t("pay.free_short")));
function priceNow(m) {
  const p = payOf(m);
  return p === "free" ? freeTag() : priceEl(altItem(m, p, settings(m, p)));
}

/* Variants in the card: a row of them with their prices, or for Recraft three axes. */
const AX = [["line", "ax.line", { base: "ax.base", utility: "ax.utility", styles: "ax.styles" }], ["tier", "ax.tier", null], ["fmt", "ax.format", { raster: "ax.raster", vector: "ax.vector" }]];
function tierOf(v) { return /pro/i.test(v.label) ? "pro" : /flash/i.test(v.label) ? "flash" : "std"; }
function axesControl(f, cur, onPick, id, priceOf) {
  const val = (v, k) => (k === "tier" ? tierOf(v) : v[k]);
  const names = { tier: { pro: "Pro", std: t("ax.std"), flash: "Flash" } };
  return h("div", { class: "axes" }, AX.map(([k, title, nm]) => {
    const vals = ["base", "utility", "styles", "pro", "std", "flash", "raster", "vector"].filter((x) => f.cur.some((v) => val(v, k) === x));
    if (vals.length < 2) return null;
    const same = (v) => AX.filter(([k2]) => k2 !== k && val(v, k2) === val(cur, k2)).length;
    const key = `${id}-${k}`;
    return h("div", { class: "axis" }, h("span", { class: "k" }, t(title)), h("div", { class: "seg", role: "radiogroup", "aria-label": t(title), "data-seg": key, "data-dur": 340 }, vals.map((x) => {
      const exact = f.cur.find((v) => val(v, k) === x && same(v) === AX.length - 1);
      const near = exact || f.cur.filter((v) => val(v, k) === x).sort((a, b) => same(b) - same(a) || a.rank - b.rank)[0];
      return opt({ key, v: x, on: val(cur, k) === x, cls: exact ? "" : "off", title: exact ? near.label : t("ax.moves_to", { v: near.label }), pick: () => onPick(near) },
        nm ? t(nm[x]) : names.tier[x], h("small", {}, priceOf(near.m)));
    })));
  }));
}
function variantBlock(m) {
  const f = C.famOf[m.id], v = C.varOf[m.id];
  if (f.vars.length < 2) return null;
  const kids = [];
  if (f.cur.length > 1) kids.push(f.axes ? axesControl(f, v, pickVar, "ax", priceNow)
    : h("div", { class: "seg" + (f.cur.length > 3 ? " wrapping" : ""), role: "radiogroup", "aria-label": t("var.label"), "data-seg": "var", "data-dur": 420 },
      f.cur.map((x) => opt({ key: "var", v: x.id, on: x.id === m.id, title: x.note ? `${x.label}: ${x.note}` : x.label, pick: () => pickVar(x) }, x.label, h("small", {}, priceNow(x.m))))));
  const dep = deprecation(m);
  if (dep) kids.push(h("p", { class: "mnote" }, ic("clock"), h("span", {}, dep.to ? [dep.when + ". ", h("button", { type: "button", onclick: () => pickVar(C.varOf[dep.to.id] || { id: dep.to.id }) }, t("var.take", { name: dep.toName }))] : dep.text + ".")));
  else if (v.by) kids.push(h("p", { class: "mnote" }, ic("clock"), h("span", {}, t("var.outdated", { v: v.by.label }) + " ", h("button", { type: "button", onclick: () => pickVar(v.by) }, t("var.take", { name: v.by.label })))));
  else if (v.note) kids.push(h("p", { class: "mnote info" }, ic("info"), h("span", {}, f.axes ? h("b", {}, v.label + ": ") : null, v.note)));
  return kids.length ? h("div", { class: "mvars" }, kids) : null;
}
export function setModel(id) { S.model[S.kind] = id; const f = C.famOf[id]; if (f) S.lastVar[f.idx] = id; }
// A variant: the block slides, the name rolls in the direction of the move.
function pickVar(x) {
  const m0 = M(), f = C.famOf[x.id], old = vname(m0);
  const row = f.cur.includes(C.varOf[x.id]) && f.cur.includes(C.varOf[m0.id]) ? f.cur : f.vars;
  const dir = row.indexOf(C.varOf[x.id]) >= row.indexOf(C.varOf[m0.id]) ? 1 : -1;
  act(() => { setModel(x.id); render(); });
  ticker($("#how .mname"), old, dir);
}

function paySec(m, pay) {
  const ps = pays(m), kind = m.kind;
  const sub = (p) => {
    if (p === "free") return S.signed ? (freeLeft(kind) > 0 ? t("pay.left", { n: freeLeft(kind) }) : t("pay.ran_out")) : freePer(kind);
    if (S.signed && !keyOk(p)) return connection(p) ? t("pay.key_bad") : t("pay.key_needed");
    return priceEl(altItem(m, p, settings(m, p)));
  };
  const name = (p) => (p === "free" ? [ic("ticket"), t("pay.free")] : [provMark(p), PNAME[p] || p]);
  const lab = h("span", { class: "lab" }, ic("coin"), t("pay.label"));
  if (ps.length === 1 && usable(m, ps[0])) return null;
  if (ps.length === 1) return h("div", { class: "paysec", "data-k": "h-pay" }, h("div", { class: "sec row" }, lab, h("div", { class: "pay1" }, name(ps[0]), h("small", {}, sub(ps[0])))));
  return h("div", { class: "paysec", "data-k": "h-pay" }, h("div", { class: "sec row" }, lab,
    h("div", { class: "seg", role: "radiogroup", "aria-label": t("pay.label"), "data-seg": "pay", "data-dur": 380 }, ps.map((p) =>
      opt({ key: "pay", v: p, on: pay === p, cls: usable(m, p) ? "" : "off", title: p === "free" ? freePer(kind) : connection(p) ? t("pay.your_key", { last4: connection(p).last4 }) : null,
        pick: () => act(() => { S.pay[m.id] = p; render(); }) }, h("span", { class: "row" }, name(p)), h("small", {}, sub(p)))))));
}

/* Settings: the ones a person decides, each as the control that fits it. */
// Settings whose price usually depends on the value carry the price of each value; the rest do not.
const PRICED = new Set(["resolution", "quality", "duration", "size", "generate_audio", "thinking_level", "google_search", "image_search", "upscale_factor", "fast"]);
const unpriced = new Map(); // offer id -> Set of names found not to change the price
function settingsBlock(m, pay) {
  const o = offerOf(m, pay), s = settings(m, pay), box = h("div", { class: "sets", "data-k": "h-sets" });
  if (pay === "free") {
    const plan = freePlan(m), fixed = plan.settings || {};
    const parts = Object.entries(fixed).filter(([k]) => k !== "n").map(([k, v]) => (k === "duration" ? t("free.clip", { len: show(k, v) }) : show(k, v)));
    const open = fieldsOf(o).filter((x) => !(x in fixed));
    if (open.includes("aspect_ratio") || open.includes("size")) parts.push(t("free.model_picks"));
    const keyed = m.offers.find((x) => keyOk(x.provider)) || m.offers[0];
    box.append(h("p", { class: "fixedline", "data-k": "h-fixed" }, t("free.line", { what: parts.join(", ") || t("free.defaults") }) + " ",
      open.length && pays(m).length > 1 ? h("button", { type: "button", class: "link-t", onclick: () => act(() => { S.pay[m.id] = keyed.provider; render(); }) }, t("free.tune")) : null));
    return box;
  }
  const names = [...fieldsOf(o), ...("fast" in s ? ["fast"] : [])];
  const prim = names.filter((x) => !SECONDARY.has(x)), sec = names.filter((x) => SECONDARY.has(x));
  for (const name of prim) box.append(...[].concat(settingField(m, pay, o, s, name)));
  if (sec.length && !isPhone()) for (const name of sec) box.append(...[].concat(settingField(m, pay, o, s, name)));
  else if (sec.length) {
    box.append(h("button", { type: "button", class: "morebtn", "data-k": "h-more", "aria-expanded": String(S.more), onclick: toggleMore },
      ic("sliders"), S.more ? t("set.less") : t("set.more", { n: sec.length }), ic("chevd", "chev" + (S.more ? " flip" : ""))));
    if (S.more) box.append(h("div", { class: "secondary", "data-k": "h-sec" }, sec.map((name) => settingField(m, pay, o, s, name))));
  }
  return box;
}
function toggleMore() {
  const was = S.more;
  act(() => { S.more = !was; render(); });
  spin($("#how .morebtn .chev"), was ? 180 : 0, was ? 0 : 180);
}
const setWanted = (name, x) => { const m = M(); S.pay[m.id] ??= payOf(m); S.wanted[name] = x; };
function settingField(m, pay, o, s, name) {
  if (name === "size") return sizeFields(m, pay, o, s);
  const p = o.params[name] || { type: "boolean" }, v = s[name], key = "set-" + name;
  const set = (x) => act(() => { setWanted(name, x); render(); });
  const priced = PRICED.has(name) && !unpriced.get(o.id)?.has(name);
  const values = p.type === "enum" ? p.values : p.type === "boolean" ? [false, true] : null;
  if (priced && values) learnPriced(m, pay, o, s, name, values);
  const priceWith = (x) => (priced ? h("small", {}, priceEl(altItem(m, pay, { ...s, [name]: x }))) : null);
  const src = sourceRatio();
  let control;
  if (name === "aspect_ratio") control = ratioControl(name, p, v, set, src);
  else if (p.type === "enum" && (name === "quality" || name === "thinking_level" || name === "resolution") && p.values.length >= 3)
    control = stepSlider(name, ordered(p.values), v, (x) => setWanted(name, x), priceWith);
  else if (p.type === "boolean" && name !== "generate_audio") control = toggle(name, !!v, set, priceWith(!v));
  else if (p.type === "boolean") control = h("div", { class: "seg", role: "radiogroup", "aria-label": label(name), "data-seg": key }, [false, true].map((x) =>
    opt({ key, v: x, on: v === x, pick: () => set(x) }, h("span", { class: "row" }, ic(x ? "audio" : "mute"), show(name, x)), priceWith(x))));
  else if (p.type === "enum" && p.values.length <= 5) control = h("div", { class: "seg" + (p.values.length > 4 ? " wrapping" : ""), role: "radiogroup", "aria-label": label(name), "data-seg": key },
    ordered(p.values).map((x) => opt({ key, v: x, on: String(v) === String(x), pick: () => set(x) }, show(name, x), priceWith(x))));
  else control = rangeControl(name, p, v);
  const sec = fieldSec(name, label(name), SICON[name], fieldBits(name, v, src), control);
  // Under the last search switch, while one is on: Google bills searches past its monthly allowance.
  if (SEARCH.includes(name) && name === fieldsOf(o).filter((x) => SEARCH.includes(x)).at(-1) && SEARCH.some((x) => s[x]))
    return [sec, h("p", { class: "snote", "data-k": "h-snote" }, t("set.search_note"))];
  return sec;
}
// Once the prices of every value are known and all are the same, the setting stops showing them.
function learnPriced(m, pay, o, s, name, values) {
  const items = values.map((x) => altItem(m, pay, { ...s, [name]: x }));
  const check = () => {
    const got = items.map(known);
    if (got.some((g) => !g)) return false;
    const us = got.map((g) => (g.estimate?.kind === "unknown" ? NaN : g.estimate?.usd));
    if (us.every((u) => Math.abs(u - us[0]) < 1e-9)) {
      if (!unpriced.has(o.id)) unpriced.set(o.id, new Set());
      unpriced.get(o.id).add(name);
      if (M() === m) act(() => render());
    }
    return true;
  };
  const k = o.id + "|" + name + "|" + JSON.stringify(s);
  if (!pending.has(k) && !check()) pending.set(k, check);
}
const pending = new Map();
onPrices(() => { for (const [k, c] of [...pending]) if (c()) pending.delete(k); });

function toggle(name, v, set, sub) {
  return h("button", { type: "button", class: "tog", role: "switch", "aria-checked": String(v), "aria-label": label(name),
    onclick: (e) => { e.currentTarget.setAttribute("aria-checked", String(!v)); setTimeout(() => set(!v), 160); } },
    h("span", { class: "knob" }), h("span", { class: "tt" }, t(v ? "val.on" : "val.off")), sub);
}
function rangeControl(name, p, v) {
  const vals = p.type === "enum" ? ordered(p.values) : null;
  const step = p.type === "range" ? (p.max - p.min > 3 ? 0.5 : 0.1) : 1;
  const r = vals ? h("input", { type: "range", min: 0, max: vals.length - 1, step: 1, value: Math.max(0, vals.findIndex((x) => String(x) === String(v))), "aria-label": label(name) })
    : h("input", { type: "range", min: p.min, max: p.max, step, value: v, "aria-label": label(name) });
  const val = () => (vals ? vals[+r.value] : +r.value);
  const cur = h("b", {}, show(name, v));
  fillRange(r);
  r.addEventListener("input", () => { setWanted(name, val()); cur.textContent = show(name, val()); fillRange(r); renderBar(); });
  r.addEventListener("change", () => act(() => render()));
  return h("div", { class: "rng" }, h("div", { class: "rline" }, r, cur),
    h("div", { class: "rends", "aria-hidden": "true" }, h("span", {}, show(name, vals ? vals[0] : p.min)), h("span", {}, show(name, vals ? vals[vals.length - 1] : p.max))));
}
const fieldSec = (name, lab, icon, bits, control) => h("div", { class: "sec row", "data-k": "h-f-" + name },
  h("span", { class: "lab" }, ic(icon || "sliders"), h("span", { class: "lt" }, lab), bits.length ? h("span", { class: "labr" }, bits) : null), control);
const fillRange = (r) => r.style.setProperty("--f", (+r.value - +r.min) / (+r.max - +r.min || 1));
// Weak to strong as a slider with named steps: the chosen one in bold, the price under each.
function stepSlider(name, vals, v, store, sub, gapOf = () => null) {
  const i0 = Math.max(0, vals.findIndex((x) => String(x) === String(v)));
  const r = h("input", { type: "range", min: 0, max: vals.length - 1, step: 1, value: i0, "aria-label": label(name), "aria-valuetext": show(name, vals[i0]) });
  const ticks = h("div", { class: "ticks", "aria-hidden": "true" }, vals.map((x, i) =>
    h("button", { type: "button", tabindex: -1, class: (i === i0 ? "on" : "") + (gapOf(x) ? " gap" : ""), title: gapOf(x) || null, onclick: () => act(() => { store(x); render(); }) },
      h("span", {}, show(name, x)), sub(x))));
  [...ticks.children].forEach((b, i) => b.style.setProperty("--i", i));
  const mark = (i) => [...ticks.children].forEach((b, k) => b.classList.toggle("on", k === i));
  fillRange(r);
  r.addEventListener("input", () => { const x = vals[+r.value]; store(x); mark(+r.value); fillRange(r); r.setAttribute("aria-valuetext", show(name, x)); renderBar(); });
  r.addEventListener("change", () => act(() => render()));
  const box = h("div", { class: "steps" }, r, ticks);
  box.style.setProperty("--n", vals.length);
  return box;
}
function fieldBits(name, v, src) {
  if (!src) return [];
  if (name === "aspect_ratio" && v === "auto") return [h("span", { class: "right" }, t("set.as_photo"))];
  if ((name === "aspect_ratio" || name === "size") && S.wanted[name] === undefined) return [h("span", { class: "right" }, t("set.near_photo"))];
  return [];
}
/* GPT Image's one "size" is both the shape and the resolution; here they are two rows like everywhere else. */
function sizeFields(m, pay, o, s) {
  const all = o.params.size.values.map(String), v = String(s.size), src = sourceRatio();
  const R = [...new Set(all.map(ratioOfSize))], K = [...new Set(all.map(sizeK))].sort((a, b) => resScore(a) - resScore(b));
  const cr = ratioOfSize(v), ck = sizeK(v), find = (r, k) => all.find((x) => ratioOfSize(x) === r && sizeK(x) === k);
  const set = (x) => act(() => { setWanted("size", x); render(); });
  const byK = (r) => all.filter((x) => ratioOfSize(x) === r).sort((a, b) => Math.abs(resScore(sizeK(a)) - resScore(ck)) - Math.abs(resScore(sizeK(b)) - resScore(ck)))[0];
  const byR = (k) => all.filter((x) => sizeK(x) === k).sort((a, b) => Math.abs(Math.log(ratioNum(ratioOfSize(a)) / ratioNum(cr))) - Math.abs(Math.log(ratioNum(ratioOfSize(b)) / ratioNum(cr))))[0];
  const shapes = ratioControl("size-r", { values: R }, cr, (r) => set(find(r, ck) || byK(r)), null,
    (r) => (find(r, ck) ? null : t("set.only_in", { v: r, list: K.filter((k) => find(r, k)).join(", ") })));
  const has = (k) => R.filter((r) => find(r, k));
  const kseg = stepSlider("resolution", K, ck, (k) => setWanted("size", find(cr, k) || byR(k)),
    (k) => h("small", {}, find(cr, k) ? priceEl(altItem(m, pay, { ...s, size: find(cr, k) })) : has(k).join(", ")),
    (k) => (find(cr, k) ? null : t("set.only_at", { v: k, list: has(k).join(", ") })));
  return [fieldSec("size", label("aspect_ratio"), "ratio", fieldBits("size", v, src), shapes), fieldSec("size-k", label("resolution"), "res", [], kseg)];
}
function ratioControl(name, p, v, set, src, gapOf = () => null) {
  const COMMON = ["auto", "1:1", "4:3", "3:4", "3:2", "2:3", "16:9", "9:16"];
  let list = S.allRatios || p.values.length <= 8 ? p.values.slice() : p.values.filter((x) => COMMON.includes(String(x)));
  if (!list.some((x) => String(x) === String(v))) list.push(v);
  const rank = (x) => { const i = COMMON.indexOf(String(x)); return i >= 0 ? i : 100 - ratioNum(x); };
  list.sort((a, b) => rank(a) - rank(b));
  const key = "set-" + name;
  const shape = (r, auto) => { const k = 24, w = r >= 1 ? k : k * r, hh = r >= 1 ? k / r : k; return h("span", { class: "shp" + (auto ? " auto" : ""), style: { width: Math.max(6, w) + "px", height: Math.max(6, hh) + "px" } }); };
  const tiles = list.map((x) => {
    const auto = x === "auto", r = auto ? src || 1 : ratioNum(x), gap = gapOf(x);
    const b = opt({ key, v: x, on: String(x) === String(v), cls: "rt" + (gap ? " gap" : ""), title: gap || (auto ? t(src ? "set.as_photo" : "val.auto") : String(x)), pick: () => set(x) },
      shape(r, auto), auto ? t(src ? "val.photo" : "val.auto") : String(x));
    b.dataset.k = `h-rt-${name}-${x}`; b.dataset.km = "stay";
    return b;
  });
  const hidden = p.values.length - list.length;
  if (hidden > 0) tiles.push(h("button", { type: "button", class: "rt more", "data-k": `h-rt-${name}-more`, "data-km": "stay", onclick: () => act(() => { S.allRatios = true; render(); }) }, "+" + hidden));
  else if (S.allRatios && p.values.length > 8) tiles.push(h("button", { type: "button", class: "rt more", "data-k": `h-rt-${name}-more`, "data-km": "stay", onclick: () => act(() => { S.allRatios = false; render(); }) }, t("set.fewer")));
  const box = h("div", { class: "ratios one", role: "radiogroup", "aria-label": label("aspect_ratio"), "data-seg": key, "data-dur": 360 }, tiles);
  box.style.setProperty("--n", Math.ceil(tiles.length / Math.ceil(tiles.length / 8)));
  return box;
}

/* The bar: the price of the run and the one button. */
let lastVal = null;
export function renderBar() {
  const m = M();
  if (!m) return;
  const pay = payOf(m), nx = next($("#prompt").value), kind = m.kind;
  const item = runItem(m, pay), q = known(item);
  if (!q) need(item);
  const free = pay === "free", e = q?.estimate;
  const unknown = !free && (!e || e.kind === "unknown");
  const text = free ? t("bar.free") : !q ? "…" : unknown ? t("price.unknown") : priceText(e);
  const val = free ? 0 : unknown ? 1e9 : e.usd;
  const el = $("#pnum");
  el.classList.toggle("usd", !free && !unknown);
  if (!free && !unknown && e.currency && e.currency !== "USD") el.title = usd(e.kind === "up_to" ? e.max_usd : e.usd); else el.removeAttribute("title");
  if (el.dataset.t !== text) roll(el, text, lastVal == null ? 1 : val >= lastVal ? 1 : -1);
  lastVal = val;
  const cap = $("#pcap"), was = cap.textContent;
  let parts;
  if (nx.why) parts = [nx.why];
  else if (free) parts = [S.signed ? t("bar.free_left", { left: freeLeft(kind), limit: freeLimit(kind) }) : t("bar.free_signed_out", { per: freePer(kind) })];
  else if (unknown && q) parts = [t("bar.unknown")];
  else if (e && e.discount && e.list_label) parts = [h("s", { class: "was" }, priceText({ ...e, label: e.list_label })), " ", h("b", { class: "off" }, "−" + fmtNum(Math.round(e.discount * 100)) + "%")];
  else parts = [""];
  cap.replaceChildren(...parts);
  if (was !== cap.textContent && !cap.getAnimations().length) anim(cap, [{ opacity: 0 }, { opacity: 1 }], { duration: 240, delay: 120, fill: "backwards" });
  const btn = $("#go");
  btn.replaceChildren(nx.icon ? ic(nx.icon) : "", nx.label, nx.on ? h("small", { class: "kbd" }, MAC ? "⌘↵" : "Ctrl ↵") : "");
  btn.disabled = !nx.on;
  btn.onclick = nx.act === "create" ? generate : nx.act === "signin" ? signIn : nx.act === "cut" ? openCut : null;
  btn.title = nx.on ? `${nx.label} (${MAC ? "⌘ Enter" : "Ctrl+Enter"})` : "";
  window.dispatchEvent(new CustomEvent("framy:button", { detail: nx }));
}
