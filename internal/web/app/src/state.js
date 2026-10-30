// What the composer holds and the rules that follow from it: which model and
// payment, the settings a run goes with, which photos go, and what the one
// button does now.
import { C, freePlan, pays, offerOf, settings as settingsOf, photoLimit, needsImage, needsVideo, takesVideo, takesAudio, PNAME } from "./catalog.js";
import { t, fmtTime, fmtIn } from "./i18n.js";
import { known, need } from "./price.js";

export const S = {
  signed: false, me: null, mini: false, online: navigator.onLine,
  kind: "image", model: { image: "", video: "" }, pay: {}, wanted: {}, files: [],
  more: false, allRatios: false,
  sort: "maker", info: null, hover: null, detVar: {}, lastVar: {},
  job: null, busy: false, closed: { image: 0, video: 0 },
};

export const M = () => C.byId[S.model[S.kind]];
export const images = () => S.files.filter((f) => f.kind === "image");

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

export const fitsFree = (m) => { const p = freePlan(m); return !!p && images().length <= (p.start_frame ? 1 : p.max_inputs ?? 0) && images().length >= (p.min_inputs || 0); };
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

export function sourceRatio() { const i = images()[0]; return i ? i.w / i.h : null; }
export const settings = (m, pay, wanted = S.wanted) => settingsOf(m, pay, wanted, sourceRatio());

// Which attached photos go into the run: the first ones up to the model's limit.
export function route(m, pay) {
  const max = photoLimit(m, pay), plan = pay === "free" ? freePlan(m) : null;
  let n = 0;
  return S.files.map((f) => {
    if (f.kind !== "image") return { why: t("why.no_clips_yet") };
    if (!max) return { why: plan && offerOf(m, "openrouter")?.inputs?.images ? t("why.free_no_photos") : t("why.no_photos") };
    if (n >= max) return { why: t("why.max_photos", { n: max }), more: true };
    n++;
    return { go: true };
  });
}

// What the composer quotes for the run: the run as POST /api/jobs would get it.
export function runItem(m = M(), pay = payOf(m), set = settings(m, pay)) {
  const r = route(m, pay), files = S.files.filter((f, i) => r[i]?.go).map((f) => f.meta);
  const o = offerOf(m, pay);
  return { offer: o.id, pay: S.signed ? (pay === "free" ? "free" : "key") : "", settings: set, files };
}
// The same model and payment with other settings, for the prices beside a choice.
export const altItem = (m, pay, set) => ({ ...runItem(m, pay, set), pay: "" });

// A model that cannot take what is attached says why, quietly.
export function misfit(m) {
  const n = images().length;
  if (needsVideo(m)) return t("misfit.needs_clip");
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
  const pay = payOf(m), empty = !prompt.trim() && !S.files.length;
  if (!S.signed) return { label: t(S.mini ? "go.create" : "go.signin"), act: "signin", on: !empty || S.mini };
  if (!S.online) return { label: t("go.offline"), on: false };
  if (S.busy) return { label: t("go.starting"), on: false };
  if (pay === "free") {
    if (S.closed[m.kind] > Date.now()) return { label: t("go.free_closed", { time: fmtTime(S.closed[m.kind]) }), on: false };
    if (freeLeft(m.kind) <= 0) return { label: t("go.free_out"), on: false, why: freesAt(m.kind) ? t("bar.free_back", { when: fmtIn(freesAt(m.kind)) }) : "" };
  } else if (!keyOk(pay)) return { label: t("go.need_key", { provider: PNAME[pay] || pay }), on: false, why: t("bar.keys_later") };
  if (needsVideo(m)) return { label: t("go.need_clip"), on: false };
  if (!images().length && needsImage(m)) return { label: t("go.add_photo"), on: false };
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
    default: return t("go.cant");
  }
}
// Asks for the prices the composer shows now.
export const quoteNow = () => { const m = M(); if (m) need(runItem(m)); };
export { takesVideo, takesAudio };
