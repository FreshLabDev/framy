// Signing in and out, and a press of Create from the files going up to the
// result: the steps before the job, the job and its events, what the server
// refused, cancelling, and the actions on what came of it.
import { $ } from "./dom.js";
import { t } from "./i18n.js";
import { api, watchJob, isFinal, outsOf } from "./api.js";
import { S, M, payOf, settings, fastOf, going, clip, contFits, keyOk, runItem, plan, paidItems, newFrame } from "./state.js";
import { C, offerOf, PNAME, framesOf, photoLimit } from "./catalog.js";
import { known, knownTotal, priceText, clearPrices } from "./price.js";
import { act, render, renderBar } from "./composer.js";
import { renderStage, runChanged } from "./stage.js";
import { upload } from "./files.js";
import { takeClip, openCut } from "./clips.js";
import { openPicker } from "./picker.js";
import { refusal, failure, providerOf } from "./errors.js";
import { toast, ask } from "./ui.js";

export function signIn() {
  // The Mini App signs in with its launch data, which a load trades again.
  if (S.mini) { location.reload(); return; }
  try { sessionStorage.setItem("framy.draft", $("#prompt").value); } catch {}
  // Back where the person was: the composer, the library or one run of it.
  const back = /^\/(library|r\/\d+)$/.test(location.pathname) ? location.pathname : "/";
  location.assign("/auth/login?return_to=" + encodeURIComponent(back));
}

export async function signOut() {
  const yes = await ask({ title: t("auth.signout_q"), yes: t("auth.signout") });
  if (!yes) return;
  try { await api("/auth/logout", { method: "POST" }); } catch {}
  forget();
  toast(t("auth.signed_out"));
}
// Nobody is signed in any more: signed out, the account deleted, or the session gone.
export function forget() {
  stopWatch?.();
  S.signed = false; S.me = null; S.run = null;
  clearPrices();
  window.dispatchEvent(new Event("framy:me"));
}

export async function refreshMe() {
  const v = S.meV;
  try {
    const me = await api("/api/me");
    // A setting changed meanwhile, and its answer is the newer account.
    if (S.meV !== v) return;
    const was = S.me?.currency;
    S.me = me; S.signed = true;
    if (was && was !== me.currency) clearPrices();
  } catch (e) {
    if (e.status === 401) { S.me = null; S.signed = false; }
  }
  window.dispatchEvent(new Event("framy:me"));
}

/* A run: what was pressed, and where it stands. phase is "prep" while the
   files go up, "job" once the server made the job, "refused" when it would
   not, "cancelled" when the person stopped it before. */
let seq = 0, stopWatch = null;
const newKey = () => "site-" + crypto.randomUUID();
export const shownOf = (e) => (e && e.kind !== "unknown" ? (e.kind === "up_to" ? e.max_usd : e.usd) : null);

function fromComposer() {
  const m0 = M(), pay = payOf(m0), { m, set } = fastOf(m0, pay, settings(m0, pay)), e = known(runItem(m, pay, set))?.estimate;
  const files = going(m, pay), c = clip();
  return { kind: m.kind, model: m.id, pay, offer: offerOf(m, pay).id, prompt: $("#prompt").value.trim(), settings: set, files,
    clip: files.includes(c) ? c : null, cut: files.includes(c) && S.cut ? { ...S.cut } : null, cont: contFits(m, pay) ? S.cont : null,
    shown: pay === "free" ? null : shownOf(e), shownLabel: e ? priceText(e) : "", priceLabel: e?.label || "" };
}
// A press of several frames (docs/design.md, Frames): each frame with its
// model, payment, settings and the files it takes; the price is the sum framy
// gave for the frames that are not free. The press's own model and settings
// are the first frame's, for what is said of it as a whole.
function pressOf() {
  const P = plan(), c = clip();
  const frames = P.map((it) => {
    const { m, set } = fastOf(it.m, it.pay, it.set), files = going(m, it.pay);
    return { model: m.id, pay: it.pay, offer: offerOf(m, it.pay).id, settings: set, files, clip: files.includes(c) ? c : null,
      cut: files.includes(c) && S.cut ? { ...S.cut } : null, cont: contFits(m, it.pay) ? S.cont : null };
  });
  const paid = paidItems(P), e = paid.length ? knownTotal(paid)?.total : null, files = S.files.filter((f) => frames.some((fm) => fm.files.includes(f)));
  return { multi: true, kind: S.kind, prompt: $("#prompt").value.trim(), frames, files, ...frames[0], clip: files.includes(c) ? c : null,
    cut: files.includes(c) && S.cut ? { ...S.cut } : null, cont: frames.find((fm) => fm.cont)?.cont || null,
    shown: paid.length ? shownOf(e) : null, shownLabel: e ? priceText(e) : "", priceLabel: e?.label || "" };
}
// A run again as it was: the same model, settings, files and words; a press, every frame of it.
const copyOf = (r, more = {}) => ({ kind: r.kind, model: r.model, pay: r.pay, offer: r.offer, prompt: r.prompt, settings: r.settings, files: r.files,
  clip: r.clip, cut: r.cut, cont: r.cont, shown: r.shown, shownLabel: r.shownLabel, priceLabel: r.priceLabel,
  ...(r.multi ? { multi: true, frames: r.frames.map((fm) => ({ ...fm })) } : {}), ...more });
// A frame of a press, a cell on the stage, stands for its press in what is done with it.
const pressOfRun = (run) => run?.press || run;

export function generate() {
  if (!M() || S.busy) return;
  start(plan().length > 1 ? pressOf() : fromComposer());
}

/* The jobs of a run: one, or a press's. */
export const jobsOf = (run) => run?.jobs || (run?.job ? [run.job] : []);
// One frame of a press as a run of its own: its job, and which of the job's results is its (k).
export function frameRun(run, i) {
  const fm = run.frames[i], j = (run.jobs || []).find((x) => x.frames?.includes(i)) || null;
  return { ...fm, kind: run.kind, prompt: run.prompt, n: run.n, key: run.key, phase: j ? "job" : run.phase === "job" ? "prep" : run.phase, step: run.step, sending: run.sending,
    job: j, k: j ? Math.max(0, j.frames.indexOf(i)) : 0, why: run.why, net: run.net, t0: run.t0, shownLabel: "", priceLabel: "", press: run };
}

async function start(base, key = newKey()) {
  stopWatch?.(); stopWatch = null;
  const run = { ...base, n: ++seq, key, phase: "prep", step: null, job: null, why: null, net: "", stop: new AbortController(), t0: Date.now() };
  S.run = run; S.showJob = true; S.busy = true;
  renderBar(); runChanged();
  let resend = false;
  try {
    const inputs = await prepare(run);
    if (run.stop.signal.aborted) throw new DOMException("stopped", "AbortError");
    // Once the request is out it is not called back: the job may exist already.
    run.sending = true; setStep(run, { fig: "start", text: () => t("step.starting") });
    if (run.multi) { await sendPress(run, inputs); return; }
    const body = { offer: run.offer, prompt: run.prompt, settings: run.settings, inputs, pay: run.pay === "free" ? "free" : "key", surface: S.mini ? "mini_app" : "site" };
    if (run.shown != null) body.shown_usd = run.shown;
    if (run.cont) body.continues = run.cont.job;
    const job = await api("/api/jobs", { method: "POST", body, headers: { "Idempotency-Key": run.key } });
    run.phase = "job"; run.job = job; run.step = null;
    told(job);
    follow(run);
    if (run.pay === "free") refreshMe();
  } catch (err) {
    run.step = null;
    if (err.name === "AbortError") run.phase = "cancelled";
    else {
      run.phase = "refused";
      resend = err.status >= 500 || err.status === 0;
      // A press refused for one frame says it of that frame.
      const fi = run.multi ? err.body?.frame : null;
      if (fi != null && run.frames[fi]) Object.assign(run, { model: run.frames[fi].model, pay: run.frames[fi].pay, offer: run.frames[fi].offer, settings: run.frames[fi].settings });
      run.why = refusal(err, { kind: run.kind, clip: !err.sound && (err.clip || (!err.file && !!run.clip && err.code !== "price_changed")), sound: !!err.sound, name: err.file?.name, maxBytes: err.maxBytes,
        shown: run.shownLabel, pay: run.pay, lastFrame: !!run.cont, clipSecs: run.clipSecs, cut: !!run.cut });
      if (run.multi) {
        // What turns one run to a key or to a last frame does not fit a press: its frames come back to the composer instead.
        run.why.acts = run.why.acts.filter((a) => a !== "bykey" && a !== "lastframe");
        if (fi != null) run.why.text = t("fr.n", { n: fi + 1 }) + ". " + run.why.text;
      }
      // The same press again keeps its key when the server may have made the job.
      if (resend) run.retryKey = run.key;
      afterRefusal(run, err);
    }
  } finally {
    if (S.run === run) { S.busy = false; run.sending = false; renderBar(); runChanged(); }
  }
}

// The press goes as one request: each frame names its files when they are not
// the first frame's, and framy says which job makes which frame. All or none.
async function sendPress(run, ids) {
  const idOf = new Map(run.files.map((f, i) => [f, ids[i]])), of = (fm) => fm.files.map((f) => idOf.get(f));
  const first = of(run.frames[0]), same = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);
  const frames = run.frames.map((fm) => {
    // framy sets how many a job makes; a frame is one result.
    const { n: _, ...set } = fm.settings || {}, x = { offer: fm.offer, pay: fm.pay === "free" ? "free" : "key", settings: set }, mine = of(fm);
    if (!same(mine, first)) x.inputs = mine;
    if (fm.cont) x.continues = fm.cont.job;
    return x;
  });
  const body = { prompt: run.prompt, inputs: first, surface: S.mini ? "mini_app" : "site", frames };
  if (run.shown != null) body.shown_usd = run.shown;
  const r = await api("/api/batches", { method: "POST", body, headers: { "Idempotency-Key": run.key } });
  run.phase = "job"; run.batch = r.batch; run.jobs = r.jobs || []; run.job = run.jobs[0] || null; run.step = null;
  run.jobs.forEach(told);
  followAll(run);
  if (run.frames.some((fm) => fm.pay === "free")) refreshMe();
}
function followAll(run) {
  const stops = run.jobs.map((j0) => watchJob(j0.id, (j) => {
    const was = jobsOf(run).every((x) => isFinal(x.status));
    run.jobs = run.jobs.map((x) => (x.id === j.id ? j : x)); run.job = run.jobs[0];
    told(j);
    if (S.run !== run) return;
    if (!was && run.jobs.every((x) => isFinal(x.status))) ended(run);
    runChanged();
  }, () => {}, (net) => { run.net = net; netState(net); if (S.run === run) runChanged(); }));
  stopWatch = () => stops.forEach((f) => f?.());
}

// The files of a run go up: photos to framy, a clip or a sound to the bucket and then to framy.
async function prepare(run) {
  const sig = run.stop.signal, pics = run.files.filter((f) => f.kind === "image" && !f.upload);
  if (pics.length) {
    setStep(run, { fig: "up", text: () => t("step.photos", { n: pics.length }) });
    await upload(pics, sig);
  }
  const ids = [];
  for (const f of run.files) {
    if (f.kind === "image") { ids.push(f.upload); continue; }
    try { ids.push(await takeClip(f, f === run.clip ? run.cut : null, { signal: sig, step: (s) => setStep(run, s) })); }
    catch (e) { if (f.kind === "audio") e.sound = true; else e.clip = true; throw e; }
    if (f === run.clip) run.clipSecs = f.secsOf?.[ids.at(-1)] || 0;
  }
  return ids;
}
function setStep(run, s) { run.step = s; if (S.run === run) runChanged(); }

// Every new state of a job the page hears of, for the library.
const told = (j) => window.dispatchEvent(new CustomEvent("framy:job", { detail: j }));
function follow(run) {
  stopWatch = watchJob(run.job.id, (j) => {
    const was = run.job.status;
    run.job = j;
    told(j);
    if (S.run !== run) return;
    if (isFinal(j.status) && !isFinal(was)) ended(run);
    runChanged();
  }, () => {}, (net) => { run.net = net; netState(net); if (S.run === run) runChanged(); });
}
// What the end of a job changes beyond the stage: the free counts, and a key
// or the free tier found not to work.
function ended(run) {
  refreshMe();
  for (const j of jobsOf(run)) {
    if (j.status !== "failed" && j.status !== "expired") continue;
    const f = failure(j);
    if (f.closeFree) { S.closed[run.kind] = Date.now() + f.closeFree; render(); }
  }
}
function afterRefusal(run, err) {
  const b = err.body || {};
  if (err.code === "free_closed") S.closed[run.kind] = Date.parse(b.until) || 0;
  if (err.code === "price_changed") clearPrices();
  if (err.code === "free_limit" || err.code === "free_closed" || err.code === "connection_invalid") refreshMe();
  if (err.code === "sign_in") { S.signed = false; S.me = null; window.dispatchEvent(new Event("framy:me")); }
  if (err.code === "no_connection") refreshMe();
  render();
}

/* The connection: one state for the page, told once when it changes. */
let netNow = "live";
export function netState(v) {
  const off = v === "lost" || !S.online, was = netNow === "lost";
  netNow = off ? "lost" : v;
  if (off && !was) toast(t("net.lost"), { ms: 6000 });
  else if (!off && was) toast(t("net.back"));
}

/* Stopping a run that has not started. */
export async function cancel(run = S.run) {
  run = pressOfRun(run);
  if (!run) return;
  if (run.phase === "prep") { if (!run.sending) run.stop.abort(); return; }
  if (run.multi) { cancelPress(run); return; }
  if (run.phase !== "job" || run.job.status !== "queued") return;
  try {
    const j = await api(`/api/jobs/${run.job.id}/cancel`, { method: "POST" });
    run.job = j;
    told(j);
    // A run of the library is not the one the stage follows.
    if (S.run !== run) return;
    if (isFinal(j.status)) { stopWatch?.(); ended(run); }
    runChanged();
  } catch (e) {
    if (e.code === "job_running") toast(t(run.kind === "video" ? "cancel.running_video" : "cancel.running"));
    else toast(refusal(e).text);
  }
}

// A press stops what has not started: every job of it still queued.
async function cancelPress(run) {
  const qs = run.jobs.filter((j) => j.status === "queued");
  const got = await Promise.all(qs.map((j) => api(`/api/jobs/${j.id}/cancel`, { method: "POST" }).catch((e) => { if (e.code !== "job_running") toast(refusal(e).text); return null; })));
  const was = run.jobs.every((x) => isFinal(x.status));
  for (const j of got.filter(Boolean)) { run.jobs = run.jobs.map((x) => (x.id === j.id ? j : x)); told(j); }
  run.job = run.jobs[0];
  if (S.run !== run) return;
  if (!was && run.jobs.every((x) => isFinal(x.status))) { stopWatch?.(); ended(run); }
  runChanged();
}

/* What can be done with what came of a run. */

// The run, back in the composer as it was pressed: model, payment, settings, files and words.
// A run of the library brings its own files only; a model that left the
// catalog leaves the composer's.
function restore(run, pay = run.pay) {
  S.kind = run.kind;
  if (run.multi) restoreFrames(run);
  else S.frames[run.kind] = [newFrame()];
  S.scope[run.kind] = null;
  if (C.byId[run.model] && !run.multi) {
    S.model[run.kind] = run.model;
    S.pay[run.model] = pay;
    if (run.pay !== "free") Object.assign(S.wanted, run.settings);
  }
  const keep = new Set(run.files);
  // A video's files are in their slots already, and the photos beside them would not be.
  S.files = run.hist || run.kind === "video" ? [...run.files] : [...run.files, ...S.files.filter((f) => !keep.has(f) && f.kind === "image")];
  delete S.wanted.frame_images;
  S.cut = run.cut ? { ...run.cut } : null;
  S.cont = run.cont;
  $("#prompt").value = run.prompt;
  try { sessionStorage.setItem("framy.draft", run.prompt); } catch {}
}
// The frames of a press back as they were: the first is the common setup, the
// others keep what they had (the composer drops what equals the common).
function restoreFrames(run) {
  const fms = run.frames.filter((fm) => C.byId[fm.model]), first = fms[0];
  if (!first) { S.frames[run.kind] = [newFrame()]; return; }
  S.model[run.kind] = first.model;
  S.pay[first.model] = first.pay;
  if (first.pay !== "free") Object.assign(S.wanted, first.settings);
  S.frames[run.kind] = fms.map((fm) => newFrame({ model: fm.model, pay: fm.pay, wanted: fm.pay === "free" ? {} : { ...fm.settings } }));
}
export function editPrompt(run = S.run) {
  run = pressOfRun(run);
  leave(run);
  act(() => { restore(run); S.showJob = false; render(); });
  const p = $("#prompt");
  p.focus({ preventScroll: true });
  p.setSelectionRange(p.value.length, p.value.length);
}
// The same run on another model: the run back in the composer, the picker open.
export function otherModel(run = S.run) {
  run = pressOfRun(run);
  leave(run);
  act(() => { restore(run); S.showJob = false; render(); });
  openPicker();
}
export function again(run = S.run) {
  run = pressOfRun(run);
  if (S.busy || !C.byId[run.model] || run.multi && run.frames.some((fm) => !C.byId[fm.model])) return;
  leave(run);
  act(() => { restore(run); render(); });
  start(copyOf(run), run.retryKey || newKey());
}
// The model refused the sound it made: the same run without sound. Its price
// is the one known for it, else the one shown with sound, which is never lower.
export function quiet(run = S.run) {
  run = pressOfRun(run);
  const m = C.byId[run.model];
  if (S.busy || !m) return;
  leave(run);
  // A press comes back without sound in each frame, and goes on the next press.
  if (run.multi) { act(() => { restore({ ...run, frames: run.frames.map((fm) => ("generate_audio" in fm.settings ? { ...fm, settings: { ...fm.settings, generate_audio: false } } : fm)) }); S.showJob = false; render(); }); return; }
  const set = { ...run.settings, generate_audio: false };
  act(() => { restore(run); S.wanted.generate_audio = false; render(); });
  const e = run.pay === "free" ? null : known(runItem(m, run.pay, set))?.estimate;
  start(copyOf(run, { settings: set, ...(e ? { shown: shownOf(e), shownLabel: priceText(e), priceLabel: e.label || "" } : {}) }));
}
// The price went up: the person agreed to the new one.
export function takePrice(run = S.run) {
  run = pressOfRun(run);
  const e = run.why?.estimate;
  start(copyOf(run, { shown: shownOf(e), shownLabel: priceText(e), priceLabel: e?.label || "" }));
}
// A free run that could not go free: the same run on the person's own key.
export const keyFor = (run) => { const m = C.byId[run.model]; return m?.offers.map((o) => o.provider).find(keyOk) || ""; };
export function byKey(run = S.run) {
  run = pressOfRun(run);
  const p = keyFor(run), m = C.byId[run.model];
  if (!p || !m) return;
  leave(run);
  // A press comes back with its free frames on the person's keys, and goes on the next press.
  if (run.multi) { act(() => { restore({ ...run, frames: run.frames.map((fm) => (fm.pay === "free" ? { ...fm, pay: keyFor(fm) || fm.pay } : fm)) }); S.showJob = false; render(); }); return; }
  act(() => { restore(run, p); render(); });
  const r = fastOf(m, p, settings(m, p)), e = known(runItem(r.m, p, r.set))?.estimate;
  start(copyOf(run, { model: r.m.id, pay: p, offer: offerOf(r.m, p).id, settings: r.set, shown: shownOf(e), shownLabel: e ? priceText(e) : "", priceLabel: e?.label || "" }));
}
export function drop() { S.showJob = false; renderStage(); renderBar(); }
// The clip was longer than framy takes whole: its part is picked in the editor.
export function pickPart(run = S.run) { editPrompt(pressOfRun(run)); setTimeout(openCut, 120); }

export async function copyPrompt(text) {
  try { await navigator.clipboard.writeText(text); toast(t("act.copied")); }
  catch { toast(t("act.copy_failed")); }
}

// Share in the Mini App: framy saves the result as a prepared inline message,
// and Telegram opens its chat picker with it. The message lives a day, but
// the links Telegram fetches for a result never sent in a chat only an hour,
// so a press within 50 minutes takes the same message again.
const shared = new Map();
export const canShare = (res) => {
  const tg = S.tg;
  return !!tg && typeof tg.shareMessage === "function" && !!tg.isVersionAtLeast?.("8.0") && res?.content_type !== "video/webm";
};
let sharing = false;
export async function share(res, btn) {
  if (sharing || !canShare(res)) return;
  const kept = shared.get(res.id);
  if (kept && kept.until > Date.now()) return S.tg.shareMessage(kept.id);
  sharing = true; btn?.classList.add("wait");
  try {
    const p = await api(`/api/files/${res.id}/share`, { method: "POST" });
    shared.set(res.id, { id: p.id, until: Math.min(Date.parse(p.expires_at) || 0, Date.now() + 50 * 60e3) - 60e3 });
    S.tg.shareMessage(p.id);
  } catch (e) {
    toast(e.code === "not_shareable" ? t("share.no") : e.code === "network" ? t("err.network") : t("share.failed"));
  } finally { sharing = false; btn?.classList.remove("wait"); }
}

// Continue a finished video: on the model that made it, from the video
// itself where the model goes on from one (continues), else from its last
// frame as the start of a new video.
export function canContinue(j) {
  const m = C.byId[j.model], p = providerOf(j);
  return !!m && m.traits?.continue && !j.free && (p === "openrouter" || p === "gemini") && keyOk(p);
}
export async function continueFrom(run = S.run, res = outsOf(run.job)[0]) {
  const j = run.job;
  if (!res) return;
  leave(run);
  if (canContinue(j)) {
    // The model goes on from its own video; the last frame, where it cannot,
    // is the joined clip's, so what follows joins to all of it.
    const p = providerOf(j), own = j.results?.[0] || res;
    act(() => {
      S.kind = "video"; S.model.video = j.model; S.pay[j.model] = p;
      S.files = []; S.cut = null;
      S.cont = { job: j.id, model: j.model, pay: p, secs: own.seconds || 0, url: own.url, w: own.width, h: own.height, result: res.id };
      $("#prompt").value = "";
      S.showJob = false;
      render();
    });
    $("#prompt").focus({ preventScroll: true });
    return;
  }
  await fromLastFrame({ job: j.id, model: j.model, url: res.url, secs: res.seconds || 0, result: res.id });
}
// The last frame of a clip framy keeps becomes the first frame of a new video,
// on the same model when it takes one, else on the first that does.
export async function fromLastFrame(src = S.cont) {
  if (!src) return;
  let shot;
  try { shot = await api(`/api/files/${src.result}/last-frame`, { method: "POST" }); }
  catch (e) { toast(refusal(e).text, { ms: 6000 }); return; }
  const takesFrame = (m) => m && m.kind === "video" && framesOf(m) > 0 && m.offers.some((o) => photoLimit(m, o.provider) > 0);
  const id = takesFrame(C.byId[src.model]) ? src.model : (C.recommend.video || []).find((x) => takesFrame(C.byId[x]));
  act(() => {
    S.kind = "video";
    if (id) S.model.video = id;
    S.cont = null; S.cut = null;
    S.files = [{ id: "lf" + shot.id, kind: "image", slot: "start", server: true, name: "", upload: shot.id, w: shot.width, h: shot.height,
      frameOf: { url: src.url, at: Math.max(0, (src.secs || 0) - 0.05) },
      meta: { content_type: shot.content_type, bytes: shot.bytes, width: shot.width, height: shot.height } }];
    S.showJob = false;
    render();
  });
  $("#prompt").focus({ preventScroll: true });
}

/* Runs of the library: a job of the person's, from GET /api/jobs, as a run
   the actions above take. Its files are the ones framy keeps, by id. */
export function runOfJob(j) {
  const files = (j.inputs || []).filter((f) => f.url).map(fileOfInput), c = files.find((f) => f.kind === "video") || null;
  // A video's first pictures were its frames, as many as frame_images named;
  // the rest were references.
  if (j.kind === "video") {
    const fr = Array.isArray(j.settings?.frame_images) ? j.settings.frame_images : [];
    files.filter((f) => f.kind === "image").forEach((f, i) => { f.slot = fr[i] === "first_frame" ? "start" : fr[i] === "last_frame" ? "end" : "ref"; });
  }
  return { kind: j.kind, model: j.model, pay: j.free ? "free" : providerOf(j), offer: j.offer, prompt: j.prompt || "", settings: j.settings || {}, files,
    clip: c, cut: null, cont: null, shown: null, shownLabel: "", priceLabel: j.estimate?.label || "", n: 0, key: "", phase: "job", job: j, why: null, net: "", hist: true };
}
// The jobs of one press read back from the library, as the press: a frame for
// each result, a job of several results giving that many frames alike, and
// the files shared by the frames that took them. Frames gone with a deleted
// job close up.
export function runOfBatch(js) {
  const idx = [...new Set(js.flatMap((j) => j.frames || [0]))].sort((a, b) => a - b);
  const jobs = js.map((j) => ({ ...j, frames: (j.frames || [0]).map((f) => idx.indexOf(f)) })), byUp = new Map();
  const share = (fs) => fs.map((f) => { if (!byUp.has(f.upload)) byUp.set(f.upload, f); return byUp.get(f.upload); });
  const frames = idx.map((_, i) => {
    const r = runOfJob(jobs.find((j) => j.frames.includes(i))), { n: _n, ...settings } = r.settings, files = share(r.files);
    return { model: r.model, pay: r.pay, offer: r.offer, settings, files, clip: files.find((f) => f.kind === "video") || null, cut: null, cont: null };
  });
  const first = jobs.find((j) => j.frames.includes(0));
  return { ...runOfJob(first), ...frames[0], multi: true, frames, files: [...byUp.values()], jobs, job: first, batch: first.batch, priceLabel: "" };
}
function fileOfInput(f) {
  const meta = { content_type: f.content_type, bytes: f.bytes, width: f.width, height: f.height }, base = { id: "j" + f.id, server: true, name: "", upload: f.id, w: f.width, h: f.height };
  if (String(f.content_type).startsWith("video/"))
    return { ...base, kind: "video", url: f.url, poster: f.thumb?.url || "", secs: f.seconds || 0, bytes: f.bytes, token: "", cuts: {}, meta: { ...meta, seconds: f.seconds || 0 } };
  if (String(f.content_type).startsWith("audio/"))
    return { ...base, kind: "audio", slot: "ref", url: f.url, secs: f.seconds || 0, bytes: f.bytes, token: "", cuts: {}, meta: { content_type: f.content_type, bytes: f.bytes, seconds: f.seconds || 0 } };
  return { ...base, kind: "image", url: f.thumb?.url || f.url, meta };
}
// What a run of the library does before it acts in the composer: the page
// turns to it (library.js says how).
// The whole screen closes for any run; the rest is for a run of the library.
let leaveHist = () => {};
export const onLeave = (fn) => { leaveHist = fn; };
const leave = (run) => leaveHist(run);

// Deleting a job: DELETE /api/jobs/{id}. A job the stage shows goes from it too.
export async function removeJob(id) {
  try { await api(`/api/jobs/${id}`, { method: "DELETE" }); }
  catch (e) { if (e.status !== 404) throw e; }
  if (jobsOf(S.run).some((j) => j.id === id)) { stopWatch?.(); stopWatch = null; S.run = null; S.showJob = false; runChanged(); renderBar(); }
}
export { isFinal };
