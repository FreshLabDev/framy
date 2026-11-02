// What the composer holds and the rules that follow from it: which model and
// payment, the settings a run goes with, which files go, and what the one
// button does now.
import { C, freePlan, pays, offerOf, settings as settingsOf, photoLimit, needsImage, needsVideo, takesVideo, takesAudio, framesOf, refsOf, refClipsOf, soundsOf, PNAME } from "./catalog.js";
import { t, fmtTime, fmtAt, fmtNum, fmtSecs } from "./i18n.js";
import { known, need } from "./price.js";

export const S = {
  signed: false, me: null, mini: false, tg: null, online: navigator.onLine,
  // meV counts the person's changes to the account on this page: a read of
  // /api/me begun before one is older than what the page holds.
  meV: 0,
  kind: "image", model: { image: "", video: "" }, pay: {}, wanted: {}, files: [],
  // cut is the part of the first clip a run takes ({start, seconds}); cont the
  // finished video a run goes on from ({job, model, pay, secs, url, poster}).
  cut: null, cont: null,
  more: false, allRatios: false,
  sort: "maker", info: null, hover: null, detVar: {}, lastVar: {},
  run: null, showJob: false, busy: false, closed: { image: 0, video: 0 },
  // The view the page shows: "create" (the composer) or "library".
  view: "create",
};

/* Frames (docs/design.md, Frames): one press of Create makes every frame of
   the kind. A frame keeps only what differs from the common model, payment
   and settings; one frame alone has nothing of its own. scope is the frame
   the composer changes now, null for all of them. */
let frameSeq = 0;
export const newFrame = (own) => ({ id: ++frameSeq, own: own ? structuredClone(own) : { wanted: {} } });
S.frames = { image: [newFrame()], video: [newFrame()] };
S.scope = { image: null, video: null };
export const MAXFR = { image: 6, video: 4 };
export const FR = () => S.frames[S.kind];
export const scopeFr = () => FR().find((f) => f.id === S.scope[S.kind]) || null;
export const common = () => C.byId[S.model[S.kind]];
export const frModel = (fr) => C.byId[fr.own.model] || common();
export const frPay = (fr) => { const m = frModel(fr); return fr.own.pay && pays(m).includes(fr.own.pay) ? fr.own.pay : basePay(m); };
export const frWanted = (fr) => ({ ...S.wanted, ...fr.own.wanted });
// What the composer changes now: one frame's, or the common one of all.
export const wanted = () => { const fr = scopeFr(); return fr ? frWanted(fr) : S.wanted; };
export const M = () => { const fr = scopeFr(); return fr ? frModel(fr) : common(); };
export function setModelHere(id) {
  const fr = scopeFr();
  if (fr) { fr.own.model = id; delete fr.own.pay; } else S.model[S.kind] = id;
  const f = C.famOf[id];
  if (f) S.lastVar[f.idx] = id;
}
export function setPayHere(m, p) { const fr = scopeFr(); if (fr) fr.own.pay = p; else S.pay[m.id] = p; }
// A chosen setting also keeps the payment it was chosen under.
export function setWantedHere(name, x) {
  const fr = scopeFr();
  if (fr) { fr.own.pay ??= frPay(fr); fr.own.wanted[name] = x; return; }
  const m = M();
  S.pay[m.id] ??= payOf(m);
  S.wanted[name] = x;
}
// A frame keeps only what differs from the rest; a frame alone is the common setup.
export function tidy() {
  const frs = FR();
  if (frs.length === 1) {
    const o = frs[0].own;
    if (o.model && C.byId[o.model]) S.model[S.kind] = o.model;
    if (o.pay) S.pay[S.model[S.kind]] = o.pay;
    Object.assign(S.wanted, o.wanted);
    frs[0].own = { wanted: {} };
    S.scope[S.kind] = null;
    return;
  }
  for (const fr of frs) {
    const o = fr.own;
    if (o.model && (o.model === S.model[S.kind] || !C.byId[o.model])) delete o.model;
    const m = frModel(fr);
    if (o.pay && (o.pay === basePay(m) || !pays(m).includes(o.pay))) delete o.pay;
    const base = settingsOf(m, frPay(fr), S.wanted, sourceRatio());
    for (const [k, v] of Object.entries(o.wanted)) if (k in base && String(base[k]) === String(v)) delete o.wanted[k];
  }
  if (S.scope[S.kind] != null && !scopeFr()) S.scope[S.kind] = null;
}
// What a frame has of its own, for the marks on its frame and in the form.
export function ownBits(fr) {
  const o = fr.own, m = frModel(fr), pay = frPay(fr), bits = [];
  if (o.model) bits.push({ k: "model", m });
  if (o.pay) bits.push({ k: "pay", pay });
  if (pay !== "free") { const s = settings(m, pay, frWanted(fr)); for (const k of Object.keys(o.wanted)) if (k in s) bits.push({ k, v: s[k] }); }
  return bits;
}
export const hasOwn = (fr) => ownBits(fr).length > 0;
// The frames of one press: each with its model, payment and settings; a free
// frame past what is left of the allowance is short.
export function plan() {
  let free = S.signed ? freeLeft(S.kind) : Infinity;
  return FR().map((fr, i) => {
    const m = frModel(fr), pay = frPay(fr), set = settings(m, pay, frWanted(fr));
    return { fr, i, m, pay, set, short: pay === "free" && free-- <= 0 };
  });
}
// The runs a press pays for: its frames that are not free, as POST /api/quote prices them.
export const paidItems = (P = plan()) => P.filter((it) => it.pay !== "free").map((it) => runItem(it.m, it.pay, it.set));
export const images = () => S.files.filter((f) => f.kind === "image");
export const clips = () => S.files.filter((f) => f.kind === "video");
export const clip = () => clips()[0];
export const sounds = () => S.files.filter((f) => f.kind === "audio");

/* The slots of a video (docs/design.md, Video): a picture is its start
   frame, its end frame or a reference, and a clip is a reference when it was
   put there, else whatever the model takes a clip as: the video it edits or a
   reference. The mode follows from what is in them; frames go alone. A
   picture of the image tab has no slot. */
export const slotOf = (f) => (f.kind === "image" ? f.slot || "ref" : f.kind === "audio" || f.slot === "ref" ? "ref" : "source");
export const sources = () => S.files.filter((f) => f.kind === "video" && f.slot !== "ref");
export const inSlot = (s) => S.files.filter((f) => S.kind === "video" && slotOf(f) === s);
export const framed = () => inSlot("start").length + inSlot("end").length > 0;
const SLOTS = { start: 0, end: 1 };
// Pictures put in the video without a slot: one or two are its frames, more
// are references, and so are all of them once references are there. When
// they do not fit in the free frames, the frames become references too.
export function placeFiles() {
  if (S.kind !== "video") return;
  const loose = S.files.filter((f) => f.kind === "image" && !f.slot);
  if (!loose.length) return;
  const refs = S.files.some((f) => f.slot === "ref") || clips().length > 0, free = ["start", "end"].filter((s) => !inSlot(s).length);
  if (!refs && loose.length <= free.length) { loose.forEach((f, i) => { f.slot = free[i]; }); return; }
  for (const f of S.files) if (f.kind === "image") f.slot = "ref";
}
// An end frame with no start frame becomes the start.
export function settleSlots() {
  if (!inSlot("start").length) for (const f of inSlot("end").slice(0, 1)) f.slot = "start";
}
// A video model that cannot take what is in the slots is not offered.
export function refuses(m) {
  if (m.kind !== "video" || S.kind !== "video") return false;
  const st = inSlot("start").length, en = inSlot("end").length, ri = S.files.filter((f) => f.kind === "image" && slotOf(f) === "ref").length;
  const rc = S.files.filter((f) => f.kind === "video" && f.slot === "ref").length;
  if (en && framesOf(m) < 2) return true;
  if (st && framesOf(m) < 1) return true;
  if (ri > refsOf(m)) return true;
  if (rc > refClipsOf(m)) return true;
  if (sounds().length > soundsOf(m)) return true;
  return sources().length > 0 && !takesVideo(m);
}

// framy's own choice for a kind: the first recommended model the picker shows.
export function autoModel(k) {
  const rec = (C.recommend[k] || []).find((id) => C.byId[id] && C.famOf[id]);
  return rec || C.models.find((m) => m.kind === k && C.famOf[m.id])?.id || "";
}
export function initModels() {
  for (const k of ["image", "video"]) S.model[k] = autoModel(k);
}
// The person's default models, where they are still in the catalog; one that
// left it gives way to framy's choice.
export const ownDefault = (k) => { const id = S.me?.default_models?.[k]; return id && C.byId[id] && C.famOf[id] ? id : ""; };
export function useDefaults() {
  for (const k of ["image", "video"]) { const id = ownDefault(k); if (id) S.model[k] = id; }
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
  // A free run takes no sound.
  if (sounds().length) return false;
  // A free video from a picture takes it as the start frame only.
  if (p.start_frame && S.kind === "video" && images().some((f) => slotOf(f) !== "start")) return false;
  return n <= freeTakes(p) && n >= (p.min_inputs || 0) && !(S.cont && S.cont.model === m.id);
};
// The payment of a model: the frame's own, else the one chosen, else free while it lasts, else a working key, else what the model has.
export function payOf(m) {
  const fr = scopeFr();
  if (m && fr?.own.pay && frModel(fr) === m && pays(m).includes(fr.own.pay)) return fr.own.pay;
  return basePay(m);
}
function basePay(m) {
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
export const settings = (m, pay, w = wanted()) => withFrames(m, pay, settingsOf(m, pay, w, sourceRatio()));
// The Fast switch is the site's own, never a setting the server sees: on, the
// run goes to the model's fast mode (Wan 3.0 Prime for Wan 3.0) with the
// settings that fit it.
export function fastOf(m, pay, set) {
  if (!("fast" in set)) return { m, set };
  const { fast, ...rest } = set, f = fast ? C.byId[m.fast] : null;
  return f ? { m: f, set: settings(f, pay, { ...wanted(), ...rest }) } : { m, set: rest };
}

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
// framy checks a clip against the model's limits to the millisecond, so only
// rounding is let through here.
const SLACK = 0.005;
const lenOf = (f) => (f === clip() && S.cut ? S.cut.seconds : f.secs);
// The frames that take the first clip: the shortest of them sets how long its part is, the longest-minded how short.
function clipTakers() {
  const c = clip(), i = c ? S.files.indexOf(c) : -1;
  return i < 0 ? [] : plan().filter((it) => route(it.m, it.pay)[i]?.go);
}
export function cutLimits() {
  const ts = clipTakers();
  if (!ts.length) { const m = M(), pay = payOf(m); return { max: maxSecs(m, pay), min: minSecs(m, pay) }; }
  return { max: Math.min(...ts.map((it) => maxSecs(it.m, it.pay))), min: Math.max(...ts.map((it) => minSecs(it.m, it.pay))) };
}
// The part of the first clip a press needs picked: the shortest any of its frames takes.
export function cutNeeded() {
  const cs = clipTakers().map((it) => needsCut(it.m, it.pay)).filter(Boolean);
  return cs.length ? Math.min(...cs) : 0;
}
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
  // In a video the pictures go by their slots, and frames go alone.
  const vid = m.kind === "video" && S.kind === "video";
  const nf = plan ? (plan.start_frame ? 1 : 0) : i.images?.frames || 0, frames = vid && nf > 0 && framed();
  const nr = plan ? (plan.start_frame ? 0 : Math.min(i.images?.refs || 0, plan.max_inputs ?? Infinity)) : i.images?.refs || 0;
  const keyRefs = !!offerOf(m, "openrouter")?.inputs?.images?.refs;
  let n = 0, nv = 0, all = 0, secs = 0, na = 0, asecs = 0;
  return S.files.map((f) => {
    // A sound is a reference, within the model's count, length and size;
    // framy takes one up to a minute where the model names no length.
    if (f.kind === "audio") {
      const ai = i.audio, mx = ai?.secs?.max || 60;
      if (frames) return { why: t("why.frames_alone") };
      if (!ai) return { why: plan && offerOf(m, "openrouter")?.inputs?.audio ? t("why.free_no_sounds") : t("why.no_sounds") };
      if (plan) return { why: t("why.free_no_sounds") };
      if (na >= ai.max) return { why: t("why.max_sounds", { n: ai.max }), more: true };
      if (f.secs && f.secs > mx + SLACK) return { why: t("why.clip_long", { n: fmtNum(mx) }) };
      if (f.secs && ai.secs?.min && f.secs < ai.secs.min - SLACK) return { why: t("why.clip_short", { n: fmtNum(ai.secs.min) }) };
      if (f.secs && ai.total_secs && asecs + f.secs > ai.total_secs + SLACK) return { why: t("why.clips_total", { n: fmtNum(ai.total_secs) }) };
      if (ai.max_bytes && f.bytes > ai.max_bytes) return { why: t("why.sound_big", { n: fmtNum(Math.floor(ai.max_bytes / 1e6)) }) };
      if (all >= cap) return { why: t("why.max_files", { n: cap }) };
      na++; all++; asecs += f.secs || 0;
      return { go: true, tag: f.secs ? fmtSecs(f.secs) : "" };
    }
    if (vid && f.kind === "image") {
      const s = slotOf(f);
      if (s === "ref" && frames) return { why: t("why.frames_alone") };
      if (all >= cap) return { why: t("why.max_files", { n: cap }) };
      if (s === "start" && !nf) return { why: plan ? t("why.free_no_photos") : t("why.no_frames") };
      if (s === "end" && nf < 2) return { why: t("why.no_end") };
      if (s === "ref" && !nr) return { why: plan && keyRefs ? t(plan.start_frame ? "why.free_start" : "why.free_no_photos") : t("why.no_refs") };
      if (s === "ref" && n >= nr) return { why: t("why.max_photos", { n: nr }), more: true };
      if (s === "ref") n++;
      all++;
      return { go: true };
    }
    if (f.kind === "video") {
      if (frames) return { why: t("why.frames_alone") };
      if (vid && f.slot === "ref" && vi?.role !== "reference") return { why: t("why.no_ref_clips") };
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
// The files of a run in the order they go: the frames first, as frame_images names them.
const rank = (f) => (S.kind === "video" && f.kind === "image" ? SLOTS[f.slot] ?? 2 : 2);
export const going = (m, pay) => { const r = route(m, pay); return S.files.filter((f, i) => r[i]?.go).sort((a, b) => rank(a) - rank(b)); };
// Which of a video's first pictures are frames: frame_images, sent as the
// slots say. A free run's plan sets it on the server.
function withFrames(m, pay, set) {
  if (pay === "free" || m.kind !== "video" || S.kind !== "video" || !offerOf(m, pay)?.params?.frame_images) return set;
  const r = route(m, pay), on = (s) => S.files.some((f, i) => r[i]?.go && f.kind === "image" && slotOf(f) === s);
  const list = on("start") ? (on("end") ? ["first_frame", "last_frame"] : ["first_frame"]) : [];
  return list.length ? { ...set, frame_images: list } : set;
}

// A continuation fits the model that made the video, through the same provider, on a key.
export const contFits = (m, pay) => !!S.cont && m.kind === "video" && m.id === S.cont.model && pay === S.cont.pay;

// What the composer quotes for the run: the run as POST /api/jobs would get it.
// A file framy keeps goes by its id; one still in the browser, or a clip to be
// cut, by what the browser read of it.
export function runItem(m0 = M(), pay = payOf(m0), set0 = settings(m0, pay)) {
  const { m, set } = fastOf(m0, pay, set0), files = [], inputs = [];
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

/* What the one button does now: it always names the next step, for every frame of the press. */
export function next(prompt) {
  if (!M()) return { label: t("go.create"), on: false };
  const items = plan(), cont = items.some((it) => contFits(it.m, it.pay)), empty = !prompt.trim() && !S.files.length && !cont;
  if (!S.signed) return { label: t(S.mini ? "go.create" : "go.signin"), act: "signin", on: !empty || S.mini };
  if (!S.online) return { label: t("go.offline"), on: false };
  if (S.busy) return { label: t("go.starting"), on: false };
  const fi = items.find((it) => it.pay === "free");
  if (fi) {
    const k = fi.m.kind;
    if (S.closed[k] > Date.now()) return { label: t("go.free_closed", { time: fmtTime(S.closed[k]) }), on: false };
    if (freeLeft(k) <= 0) return { label: t("go.free_out"), on: false, why: freesAt(k) ? t("bar.free_back", { at: fmtAt(freesAt(k)) }) : "" };
    if (items.some((it) => it.short)) return { label: t("go.free_only", { n: freeLeft(k) }), on: false, why: freesAt(k) ? t("bar.free_back", { at: fmtAt(freesAt(k)) }) : "" };
  }
  const nokey = items.find((it) => it.pay !== "free" && !keyOk(it.pay));
  if (nokey) return keyStep(nokey.pay);
  for (const it of items) {
    const r = route(it.m, it.pay);
    if (needsVideo(it.m) && !contFits(it.m, it.pay) && !S.files.some((f, i) => f.kind === "video" && r[i]?.go)) return { label: t("go.need_clip"), on: false };
    if (!images().length && needsImage(it.m)) return { label: t("go.add_photo"), on: false };
  }
  const cut = cutNeeded();
  if (cut) return { label: t("go.pick_part", { n: fmtNum(cut) }), icon: "film", act: "cut", on: true };
  for (const it of items) {
    const q = known(runItem(it.m, it.pay, it.set));
    if (q?.refusal?.error === "no_connection" || q?.refusal?.error === "connection_invalid") return keyStep(offerOf(it.m, it.pay).provider);
    if (q?.refusal) return { label: refusalLabel(q.refusal, it.m), on: false };
  }
  return { label: t("go.create"), icon: "spark", act: "create", on: !empty };
}
// A key is missing or stopped working: the button opens the settings at it.
const keyStep = (p) => ({ label: t(connection(p) ? "go.rekey" : "go.connect", { provider: PNAME[p] || p }), icon: "key", act: "key", provider: p, on: true });
export function refusalLabel(r, m) {
  switch (r.error) {
    case "free_limit": return t("go.free_out");
    case "free_closed": return t("go.free_closed", { time: fmtTime(Date.parse(r.until) || Date.now()) });
    case "no_connection": case "connection_invalid": return t("go.connect", { provider: PNAME[offerOf(m, payOf(m)).provider] || "" });
    case "wrong_inputs": case "unknown_input": return t("go.files_dont_fit");
    case "not_free": return t("go.not_free");
    case "cannot_continue": return t("go.cannot_continue");
    default: return t("go.cant");
  }
}
// Asks for the prices the composer shows now: of every frame.
export const quoteNow = () => { if (M()) for (const it of plan()) need(runItem(it.m, it.pay, it.set)); };
export { takesVideo, takesAudio };
