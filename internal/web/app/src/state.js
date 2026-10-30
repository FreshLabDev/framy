// What the composer holds and the rules that follow from it: which model and
// payment, the settings a run goes with, which files go, and what the one
// button does now.
import { C, freePlan, pays, offerOf, settings as settingsOf, photoLimit, needsImage, needsVideo, takesVideo, takesAudio, PNAME } from "./catalog.js";
import { t, fmtTime, fmtIn, fmtNum, fmtSecs } from "./i18n.js";
import { known, need } from "./price.js";

export const S = {
  signed: false, me: null, mini: false, tg: null, online: navigator.onLine,
  kind: "image", model: { image: "", video: "" }, pay: {}, wanted: {}, files: [],
  // cut is the part of the first clip a run takes ({start, seconds}); cont the
  // finished video a run goes on from ({job, model, pay, secs, url, poster}).
  cut: null, cont: null,
  more: false, allRatios: false,
  sort: "maker", info: null, hover: null, detVar: {}, lastVar: {},
  run: null, showJob: false, busy: false, closed: { image: 0, video: 0 },
};

export const M = () => C.byId[S.model[S.kind]];
export const images = () => S.files.filter((f) => f.kind === "image");
export const clips = () => S.files.filter((f) => f.kind === "video");
export const clip = () => clips()[0];

export function initModels() {
  for (const k of ["image", "video"]) {
    const rec = (C.recommend[k] || []).find((id) => C.byId[id] && C.famOf[id]);
    S.model[k] = rec || C.models.find((m) => m.kind === k && C.famOf[m.id])?.id || "";
  }
}

/* The free allowance, from /api/me. */
const meter = (kind) => S.me?.free?.[kind === "video" ? "videos" : "images"];
const allowance = (kind) => C.free?.[kind === "video" ? "videos" : "images"];
export function freeLeft(kind) {
  if (S.closed[kind] > Date.now()) return 0;
  const m = meter(kind);
  return m ? m.left : allowance(kind)?.limit ?? 0;
}
export const freeLimit = (kind) => meter(kind)?.limit ?? allowance(kind)?.limit ?? 0;
export const freesAt = (kind) => { const at = meter(kind)?.frees_at; return at ? Date.parse(at) : 0; };
export function freePer(kind) {
  const w = (meter(kind) || allowance(kind))?.window_seconds || 86400, n = freeLimit(kind);
  return t(w >= 604800 ? "free.per_week" : "free.per_day", { n });
}

/* Keys: a connection to the provider that is not known to be broken. */
const BROKEN = new Set(["invalid", "needs_billing", "needs_verification", "no_credits"]);
export const connection = (p) => S.me?.connections?.find((c) => c.provider === p);
export const keyOk = (p) => { const c = connection(p); return !!c && !BROKEN.has(c.status); };

// A free plan takes photos, or one clip for an edit; never both.
const freeTakes = (p) => (p.start_frame ? 1 : p.max_inputs ?? 0);
export const fitsFree = (m) => {
  const p = freePlan(m);
  if (!p) return false;
  const edit = needsVideo(m), n = edit ? clips().length : images().length;
  if (edit ? images().length : clips().length) return false;
  return n <= freeTakes(p) && n >= (p.min_inputs || 0) && !(S.cont && S.cont.model === m.id);
};
// The payment of a model: the one chosen, else free while it lasts, else a working key, else what the model has.
export function payOf(m) {
  if (!m) return "";
  const want = S.pay[m.id];
  if (want && pays(m).includes(want)) return want;
  if (freePlan(m) && fitsFree(m) && (!S.signed || freeLeft(m.kind) > 0)) return "free";
  const keyed = m.offers.map((o) => o.provider).filter(keyOk);
  if (keyed.length) return keyed[0];
  return freePlan(m) ? "free" : m.offers[0].provider;
}
export const usable = (m, pay) => !S.signed || (pay === "free" ? freeLeft(m.kind) > 0 : keyOk(pay));

export function sourceRatio() { const i = images()[0] || clip(); return i && i.w && i.h ? i.w / i.h : null; }
export const settings = (m, pay, wanted = S.wanted) => settingsOf(m, pay, wanted, sourceRatio());

/* Clips. */
export const inputsOf = (m, pay) => offerOf(m, pay)?.inputs || {};
// The longest clip the model takes on this payment: the most of one clip and
// of all clips together, and the free plan's own limit where it names one.
export function maxSecs(m, pay) {
  const v = inputsOf(m, pay).videos, plan = pay === "free" ? freePlan(m) : null;
  const mx = Math.min(v?.secs?.max || m.traits?.source_video?.max_seconds || Infinity, v?.total_secs || Infinity);
  return plan?.max_source_seconds ? Math.min(mx, plan.max_source_seconds) : mx;
}
export const minSecs = (m, pay) => inputsOf(m, pay).videos?.secs?.min || m.traits?.source_video?.min_seconds || 0;
const SLACK = 0.05;
const lenOf = (f) => (f === clip() && S.cut ? S.cut.seconds : f.secs);
// The first clip is longer than the model takes, and no part of it is chosen yet.
export function needsCut(m = M(), pay = payOf(m)) {
  const c = clip();
  if (!c || !c.secs || !route(m, pay)[S.files.indexOf(c)]?.go) return 0;
  const mx = maxSecs(m, pay);
  if (S.cut && S.cut.seconds <= mx + SLACK) return 0;
  return c.secs > mx + SLACK ? mx : 0;
}

// Which attached files go into the run, and why the others stay out.
export function route(m, pay) {
  const max = photoLimit(m, pay), plan = pay === "free" ? freePlan(m) : null, i = inputsOf(m, pay), vi = i.videos, cap = i.files || Infinity;
  let n = 0, nv = 0, all = 0, secs = 0;
  return S.files.map((f) => {
    if (f.kind === "video") {
      if (!vi) return { why: t("why.no_clips") };
      if (plan && !needsVideo(m)) return { why: t("why.free_no_clips") };
      if (nv >= vi.max) return { why: vi.max > 1 ? t("why.max_clips", { n: vi.max }) : t("why.one_clip"), more: true };
      const len = lenOf(f), mx = maxSecs(m, pay);
      // The first clip can be cut to fit; the others go as they are.
      if (f.secs && len > mx + SLACK && f !== clip()) return { why: t("why.clip_long", { n: fmtNum(mx) }) };
      if (f.secs && vi.secs?.min && Math.min(len, mx) < vi.secs.min - SLACK) return { why: t("why.clip_short", { n: fmtNum(vi.secs.min) }) };
      if (f.secs && vi.total_secs && secs + Math.min(len, mx) > vi.total_secs + SLACK) return { why: t("why.clips_total", { n: fmtNum(vi.total_secs) }) };
      if (all >= cap) return { why: t("why.max_files", { n: cap }) };
      nv++; all++; secs += Math.min(len || 0, mx);
      return { go: true, tag: f.secs ? fmtSecs(Math.min(len, mx)) : "" };
    }
    if (!max) return { why: plan && offerOf(m, "openrouter")?.inputs?.images ? t("why.free_no_photos") : t("why.no_photos") };
    if (n >= max) return { why: t("why.max_photos", { n: max }), more: true };
    if (all >= cap) return { why: t("why.max_files", { n: cap }) };
    n++; all++;
    return { go: true };
  });
}
export const going = (m, pay) => { const r = route(m, pay); return S.files.filter((f, i) => r[i]?.go); };

// A continuation fits the model that made the video, through the same provider, on a key.
export const contFits = (m, pay) => !!S.cont && m.kind === "video" && m.id === S.cont.model && pay === S.cont.pay;

// What the composer quotes for the run: the run as POST /api/jobs would get it.
// A file framy keeps goes by its id; one still in the browser, or a clip to be
// cut, by what the browser read of it.
export function runItem(m = M(), pay = payOf(m), set = settings(m, pay)) {
  const files = [], inputs = [];
  for (const f of going(m, pay)) {
    const cut = f === clip() && S.cut;
    if (f.upload && !cut) inputs.push(f.upload);
    else files.push(cut ? { ...f.meta, seconds: S.cut.seconds } : f.meta);
  }
  const o = offerOf(m, pay), it = { offer: o.id, pay: S.signed ? (pay === "free" ? "free" : "key") : "", settings: set, files };
  if (inputs.length) it.inputs = inputs;
  if (contFits(m, pay)) it.continues = S.cont.job;
  return it;
}
// The same model and payment with other settings, for the prices beside a choice.
export const altItem = (m, pay, set) => ({ ...runItem(m, pay, set), pay: "" });

// A model that cannot take what is attached says why, quietly.
export function misfit(m) {
  const n = images().length, c = clips().length;
  if (needsVideo(m) && !c) return t("misfit.needs_clip");
  if (c && !takesVideo(m)) return t("misfit.no_clips");
  if (!n && needsImage(m)) return t("misfit.needs_photo");
  const lim = Math.max(...pays(m).map((p) => photoLimit(m, p)));
  if (n > lim) return lim ? t("misfit.max_photos", { n: lim }) : t("misfit.no_photos");
  return "";
}
export const famMisfit = (f) => (f.cur.every((v) => misfit(v.m)) ? misfit(f.def.m) : "");

/* What the one button does now: it always names the next step. */
export function next(prompt) {
  const m = M();
  if (!m) return { label: t("go.create"), on: false };
  const pay = payOf(m), cont = contFits(m, pay), empty = !prompt.trim() && !S.files.length && !cont;
  if (!S.signed) return { label: t(S.mini ? "go.create" : "go.signin"), act: "signin", on: !empty || S.mini };
  if (!S.online) return { label: t("go.offline"), on: false };
  if (S.busy) return { label: t("go.starting"), on: false };
  if (pay === "free") {
    if (S.closed[m.kind] > Date.now()) return { label: t("go.free_closed", { time: fmtTime(S.closed[m.kind]) }), on: false };
    if (freeLeft(m.kind) <= 0) return { label: t("go.free_out"), on: false, why: freesAt(m.kind) ? t("bar.free_back", { when: fmtIn(freesAt(m.kind)) }) : "" };
  } else if (!keyOk(pay)) return { label: t("go.need_key", { provider: PNAME[pay] || pay }), on: false, why: t("bar.keys_later") };
  const r = route(m, pay);
  if (needsVideo(m) && !cont && !S.files.some((f, i) => f.kind === "video" && r[i]?.go)) return { label: t("go.need_clip"), on: false };
  if (!images().length && needsImage(m)) return { label: t("go.add_photo"), on: false };
  const cut = needsCut(m, pay);
  if (cut) return { label: t("go.pick_part", { n: fmtNum(cut) }), icon: "film", act: "cut", on: true };
  const q = known(runItem());
  if (q?.refusal) return { label: refusalLabel(q.refusal, m), on: false };
  return { label: t("go.create"), icon: "spark", act: "create", on: !empty };
}
export function refusalLabel(r, m) {
  switch (r.error) {
    case "free_limit": return t("go.free_out");
    case "free_closed": return t("go.free_closed", { time: fmtTime(Date.parse(r.until) || Date.now()) });
    case "no_connection": case "connection_invalid": return t("go.need_key", { provider: PNAME[offerOf(m, payOf(m)).provider] || "" });
    case "wrong_inputs": case "unknown_input": return t("go.files_dont_fit");
    case "not_free": return t("go.not_free");
    case "cannot_continue": return t("go.cannot_continue");
    default: return t("go.cant");
  }
}
// Asks for the prices the composer shows now.
export const quoteNow = () => { const m = M(); if (m) need(runItem(m)); };
export { takesVideo, takesAudio };
