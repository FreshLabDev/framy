// The catalog as the composer sees it: GET /api/catalog grouped into
// families, the free plans by offer, what each model takes, and the settings
// a person can change. Prices are not here: they come from POST /api/quote.
import { h, ic } from "./dom.js";
import { t, fmtNum } from "./i18n.js";
import LOGOS from "assets:logos";

export const C = { models: [], byId: {}, FAM: [], famOf: {}, varOf: {}, plans: {}, free: {}, recommend: {} };

export const PNAME = { openrouter: "OpenRouter", openai: "OpenAI", gemini: "AI Studio" };

export function loadCatalog(cat) {
  const models = cat.models || [];
  Object.assign(C, { models, byId: {}, FAM: [], famOf: {}, varOf: {}, plans: {}, free: cat.free || {}, recommend: cat.recommend || {} });
  for (const m of models) C.byId[m.id] = m;
  for (const p of cat.free?.plans || []) C.plans[p.offer] = p;
  const byKey = {};
  models.forEach((m, i) => {
    // A fast mode of a model is a switch in that model's settings, not a variant of its own.
    if (m.fast_of && C.byId[m.fast_of]) return;
    const fm = m.family || {}, key = fm.id || m.id;
    let f = byKey[key];
    if (!f) { f = byKey[key] = { key, name: fm.name || m.name, axes: !!fm.line, order: i, vars: [] }; C.FAM.push(f); }
    f.vars.push({ id: m.id, m, gen: fm.gen || "", tier: fm.tier || "", label: fm.label || m.name, note: m.note || "",
      line: fm.line || "", fmt: fm.format || "", rank: fm.rank ?? 0, outBy: fm.outdated_by, isDef: !!fm.default });
  });
  C.FAM.forEach((f, i) => {
    f.idx = i;
    // The strongest on the left: rank 0 is the family's best.
    f.vars.sort((a, b) => a.rank - b.rank);
    for (const v of f.vars) { C.famOf[v.id] = f; C.varOf[v.id] = v; }
    for (const v of f.vars) v.by = v.outBy && C.varOf[v.outBy] ? C.varOf[v.outBy] : undefined;
    f.cur = f.vars.filter((v) => !v.by);
    f.old = f.vars.filter((v) => v.by);
    if (!f.cur.length) { f.cur = f.vars; f.old = []; }
    f.maker = f.vars[0].m.maker;
    f.kind = f.vars[0].m.kind;
    f.def = f.cur.find((v) => v.isDef) || f.cur[0];
    f.title = f.cur.length > 1 || f.old.length ? f.name : f.vars[0].m.name;
  });
}

export const vname = (m) => { const f = C.famOf[m.id]; return f && f.vars.length > 1 ? `${f.name} ${C.varOf[m.id].label}` : m.name; };
export const famText = (f) => (f.name + " " + f.maker + " " + f.vars.map((v) => v.m.name + " " + v.label).join(" ")).toLowerCase();

/* Marks: a model's own where it has one, else its maker's, else a monogram. */
const MAKER_LOGO = { "Alibaba": "alibaba", "Black Forest Labs": "bfl", "ByteDance": "bytedance", "Google": "google", "Kling": "kling", "Krea": "krea",
  "Microsoft AI": "microsoft", "MiniMax": "minimax", "OpenAI": "openai", "Qwen": "qwen", "Recraft": "recraft", "Runway": "runway", "Tencent": "tencent", "SpaceXSI": "xai" };
const FAMILY_LOGO = { "nano-banana": "nanobanana", "flux": "flux", "grok-image": "grok", "grok-video": "grok", "hailuo": "hailuo", "happyhorse": "happyhorse", "qwen-image": "qwen", "gemini-omni": "gemini", "seedream": "doubao", "seedance": "jimeng" };
const PROVIDER_LOGO = { openrouter: "openrouter", openai: "openai", gemini: "aistudio" };
export function famLogo(f) {
  const id = f.vars[0].id;
  const own = FAMILY_LOGO[f.key] || (/black-forest-labs\/flux/.test(id) ? "flux" : /tencent/.test(id) ? "hunyuan" : null);
  return [own, MAKER_LOGO[f.maker]].filter(Boolean);
}
const monogram = (maker) => (maker || "?").split(/\s+/).map((w) => w[0]).join("").slice(0, 2).toUpperCase();
function maskOf(slug) { const i = h("i", { "aria-hidden": "true" }); i.style.setProperty("--m", `url("${LOGOS[slug]}")`); return i; }
export function lg(chain, maker, cls = "") {
  const slug = [].concat(chain).find((s) => LOGOS[s]);
  return h("span", { class: "lg " + cls, "aria-hidden": "true", title: maker }, slug ? maskOf(slug) : monogram(maker));
}
export function provMark(p) {
  const slug = PROVIDER_LOGO[p];
  if (!LOGOS[slug]) return ic("key");
  const i = maskOf(slug); i.className = "lgi"; return i;
}

/* Offers and payment. A payment is "free" or a provider whose key runs the offer. */
export const freePlan = (m) => { for (const o of m.offers) if (C.plans[o.id]) return { ...C.plans[o.id], offer: o }; return null; };
export const pays = (m) => (freePlan(m) ? ["free"] : []).concat(m.offers.map((o) => o.provider));
export const offerOf = (m, pay) => (pay === "free" ? freePlan(m)?.offer : m.offers.find((o) => o.provider === pay)) || m.offers[0];

/* What a model takes, from offers[].inputs. */
const anyIn = (m, f) => m.offers.some((o) => f(o.inputs || {}));
export const takesVideo = (m) => anyIn(m, (i) => !!i.videos);
export const takesAudio = (m) => anyIn(m, (i) => !!i.audio);
export const refVideo = (m) => anyIn(m, (i) => i.videos?.role === "reference");
export const needsVideo = (m) => m.offers.every((o) => (o.inputs?.videos?.min || 0) >= 1);
export const needsImage = (m) => m.offers.every((o) => (o.inputs?.images?.min || 0) >= 1);
const maxOf = (m, f) => Math.max(0, ...m.offers.map(f));
export const refsOf = (m) => maxOf(m, (o) => o.inputs?.images?.refs || 0);
export const framesOf = (m) => maxOf(m, (o) => o.inputs?.images?.frames || 0);
export const hasAudio = (m) => m.offers.some((o) => o.params.generate_audio);
const keepsLength = (m) => !!m.traits?.source_video?.keeps_length;
// How many photos go into one run of this model and payment, and as what.
export function photoLimit(m, pay) {
  const im = offerOf(m, pay)?.inputs?.images;
  let n = !im ? 0 : m.kind === "video" ? Math.max(im.frames || 0, im.refs || 0) : im.refs || im.max || 0;
  const plan = pay === "free" ? freePlan(m) : null;
  if (plan) n = Math.min(n, plan.start_frame ? 1 : plan.max_inputs ?? n);
  return n;
}

/* Settings. Only the ones a person decides go on the form; the rest keep the offer's default. */
export const SHOWN = ["aspect_ratio", "size", "resolution", "quality", "duration", "generate_audio", "thinking_level", "background", "google_search", "image_search", "creativity", "upscale_factor"];
export const SECONDARY = new Set(["background", "thinking_level", "google_search", "image_search", "creativity"]);
export const SEARCH = ["google_search", "image_search"];
export const SICON = { aspect_ratio: "ratio", size: "ratio", resolution: "res", quality: "bars", thinking_level: "bars", background: "checker", duration: "clock",
  generate_audio: "audio", fast: "bolt", google_search: "search", image_search: "image", upscale_factor: "res", creativity: "spark" };
// The fields of an offer in the form's order. "size" is a choice only where
// it is the one way to set the shape and the resolution, as on GPT Image: a
// video model lists it beside its own aspect_ratio and resolution, and a
// boolean "size" is a switch of the provider's.
const sizeOnly = (o) => o.params.size.type === "enum" && !o.params.aspect_ratio && !o.params.resolution;
export const fieldsOf = (o) => SHOWN.filter((n) => o.params[n] && (n !== "size" || sizeOnly(o)));

export const gcd = (a, b) => (b ? gcd(b, a % b) : a);
export const ratioOfSize = (s) => { const [w, hh] = String(s).split("x").map(Number); const g = gcd(w, hh) || 1; return `${w / g}:${hh / g}`; };
export const sizeK = (s) => { const m = Math.max(...String(s).split("x").map(Number)); return m >= 3800 ? "4K" : m >= 2000 ? "2K" : m >= 1500 ? "1.5K" : "1K"; };
export const ratioNum = (r) => { const [a, b] = String(r).split(/[:x]/).map(Number); return a && b ? a / b : 1; };
export const resScore = (v) => { const s = String(v); let m;
  if ((m = s.match(/^([\d.]+)k$/i))) return +m[1] * 1000; if ((m = s.match(/^(\d+)p$/i))) return +m[1];
  if ((m = s.match(/^(\d+)x(\d+)$/))) return Math.max(+m[1], +m[2]) / 1.024; return +s || 0; };
const STEP_ORDER = ["minimal", "low", "medium", "standard", "high", "hd", "xhigh", "max"];
const rankOf = (x) => { const s = String(x), i = STEP_ORDER.indexOf(s); return i >= 0 ? i : /^(\d+(\.\d+)?k|\d+p|\d+(x\d+)?)$/i.test(s) ? resScore(s) : NaN; };
// The smaller, weaker or shorter on the left; a list with no such order keeps the model's.
export const ordered = (v) => (v.some((x) => isNaN(rankOf(x))) ? v.slice() : v.slice().sort((a, b) => rankOf(a) - rankOf(b)));

export function label(name) { return t("set." + name); }
export function show(name, v) {
  if (name === "duration") return t("unit.s", { n: fmtNum(+v) });
  if (name === "generate_audio") return t(v ? "val.with_sound" : "val.no_sound");
  if (name === "fast") return t(v ? "val.on" : "val.off");
  if (SEARCH.includes(name)) return t(v ? "val.search" : "val.no");
  if (name === "upscale_factor") return "×" + fmtNum(+v);
  if (name === "creativity") return t(+v ? "val.creative" : "val.faithful");
  if (name === "size") return ratioOfSize(v) + " · " + sizeK(v);
  if (name === "resolution" || name === "aspect_ratio") return v === "auto" ? t("val.auto") : String(v);
  const k = "val." + v, s = t(k);
  return s === k ? String(v) : s;
}

export function pickVal(p, v) {
  if (v === undefined || !p) return undefined;
  if (p.type === "enum") return p.values.find((x) => String(x) === String(v));
  if (p.type === "range") return typeof v === "number" && v >= p.min && v <= p.max ? v : undefined;
  if (p.type === "boolean") return typeof v === "boolean" ? v : undefined;
}
function nearestRatio(values, r) { let best = null, d = Infinity; for (const v of values) { if (v === "auto") continue; const x = Math.abs(Math.log(ratioNum(v) / r)); if (x < d) { d = x; best = v; } } return best; }
function nearestSize(values, r, def) {
  const area = (s) => String(s).split("x").reduce((a, b) => a * b, 1);
  let best = def, d = Infinity;
  for (const v of values) { const x = Math.abs(Math.log(ratioNum(v) / r)) + 0.15 * Math.abs(Math.log(area(v) / area(def || v))); if (x < d) { d = x; best = v; } }
  return best;
}
// The default of a field, the photo's shape where the field is a shape.
export function defOf(o, name, src) {
  const p = o.params[name], d = o.defaults?.[name];
  if (src && name === "aspect_ratio" && p.type === "enum") return p.values.includes("auto") ? "auto" : nearestRatio(p.values, src);
  if (src && name === "size" && p.type === "enum") return nearestSize(p.values, src, d);
  if (d !== undefined) return d;
  if (p.type === "enum") return p.values[0];
  if (p.type === "boolean") return false;
  if (p.type === "range") return p.min;
}
// The settings a run goes with: what the person chose where the offer has it, else the default.
// Free runs go as the plan fixes them, the rest at the default.
export function settings(m, pay, wanted, src) {
  const o = offerOf(m, pay), plan = pay === "free" ? freePlan(m) : null, out = {};
  if (!o) return out;
  for (const name of fieldsOf(o)) {
    if (plan) { if (name in (plan.settings || {})) out[name] = plan.settings[name]; continue; }
    const w = pickVal(o.params[name], wanted[name]);
    out[name] = w !== undefined ? w : defOf(o, name, src);
  }
  if (plan) Object.assign(out, plan.settings || {});
  else if (m.fast && C.byId[m.fast]?.offers.some((x) => x.provider === pay)) out.fast = wanted.fast === true;
  return out;
}

/* What a model (or a family) can, in a few words. */
function unionOf(offers, name) { const all = []; for (const o of offers) for (const v of o.params[name]?.values || []) if (!all.some((x) => String(x) === String(v))) all.push(v); return all; }
export const union = (m, name) => unionOf(m.offers, name);
export function resRangeOf(offers) {
  let v = unionOf(offers, "resolution").filter((x) => x !== "auto");
  if (!v.length) v = unionOf(offers, "size").map((s) => sizeK(s));
  if (!v.length) return null;
  v.sort((a, b) => resScore(a) - resScore(b));
  return [String(v[0]), String(v[v.length - 1])];
}
export function durRangeOf(ms) {
  const d = ms.flatMap((m) => union(m, "duration")).map(Number).filter((x) => x > 0);
  if (d.length) return [Math.min(...d), Math.max(...d)];
  const p = ms.map((m) => m.traits?.picks_length).filter(Boolean);
  return p.length ? [Math.min(...p.map((x) => x.min)), Math.max(...p.map((x) => x.max))] : null;
}
export function facts(list, max = 4) {
  const ms = [].concat(list), offers = ms.flatMap((m) => m.offers), f = [];
  if (ms.some(freePlan)) f.push(["ticket", t("fact.free"), "hi"]);
  if (ms[0].kind === "image") {
    const refs = Math.max(0, ...offers.map((o) => o.inputs?.images?.refs || 0));
    if (ms.every(needsImage)) f.push(["photos", t("fact.from_photo")]);
    else if (refs) f.push(["photos", refs > 1 ? t("fact.photos", { n: refs }) : t("fact.photo_one")]);
    const r = resRangeOf(offers); if (r) f.push(["res", r[0] === r[1] ? r[1] : t("fact.up_to", { v: r[1] })]);
    if (ms.some((m) => /vector/i.test(m.name))) f.push(["vector", "SVG"]);
    if (unionOf(offers, "background").includes("transparent")) f.push(["checker", t("fact.transparent")]);
    const n = Math.max(0, ...offers.map((o) => o.params.n?.max || 0)); if (n > 1) f.push(["stack", t("fact.at_once", { n })]);
  } else {
    const refs = Math.max(0, ...ms.map(refsOf));
    if (ms.every(needsVideo)) f.push(["film", t("fact.edits_clip")]);
    else if (ms.some(refVideo)) f.push(["film", t(ms.some(takesAudio) ? "fact.clips_sound" : "fact.clips")]);
    else if (ms.some(takesVideo)) f.push(["film", t("fact.own_clip")]);
    else if (refs) f.push(["photos", refs > 1 ? t("fact.photos", { n: refs }) : t("fact.photo_one")]);
    const d = durRangeOf(ms);
    if (d) f.push(["clock", d[0] === d[1] ? t("unit.s", { n: d[0] }) : t("fact.len", { a: d[0], b: d[1] })]);
    else if (ms.some(keepsLength)) f.push(["clock", t("fact.len_as_clip")]);
    if (ms.some(hasAudio)) f.push(["audio", t("fact.sound")]);
    const r = resRangeOf(offers); if (r) f.push(["res", t("fact.up_to", { v: r[1] })]);
  }
  return f.slice(0, max);
}
export const factEl = ([icon, text, cls]) => h("span", { class: "fact" + (cls ? " " + cls : "") }, ic(icon), text);

// A model the maker closes: the day if named, and what replaces it.
export function deprecation(m) {
  if (!m.deprecated) return null;
  const to = m.successor && C.byId[m.successor];
  const toName = to ? (C.famOf[to.id] === C.famOf[m.id] ? C.varOf[to.id].label : vname(to)) : "";
  const when = m.shuts_down ? t("dep.shuts", { date: new Date(m.shuts_down + "T00:00:00Z").toLocaleDateString(document.documentElement.lang, { day: "numeric", month: "long" }) })
    : t("dep.closing", { maker: m.maker });
  return { to, toName, when, text: toName ? t("dep.with_successor", { when, name: toName }) : when };
}
