// Signing in and out, and a press of Create from the files going up to the
// result: the steps before the job, the job and its events, what the server
// refused, cancelling, and the actions on what came of it.
import { $ } from "./dom.js";
import { t } from "./i18n.js";
import { api, watchJob, isFinal } from "./api.js";
import { S, M, payOf, settings, fastOf, going, clip, contFits, keyOk, runItem } from "./state.js";
import { C, offerOf, PNAME, framesOf, photoLimit } from "./catalog.js";
import { known, priceText, clearPrices } from "./price.js";
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
  try {
    const me = await api("/api/me");
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
// A run again as it was: the same model, settings, files and words.
const copyOf = (r, more = {}) => ({ kind: r.kind, model: r.model, pay: r.pay, offer: r.offer, prompt: r.prompt, settings: r.settings, files: r.files,
  clip: r.clip, cut: r.cut, cont: r.cont, shown: r.shown, shownLabel: r.shownLabel, priceLabel: r.priceLabel, ...more });

export function generate() {
  if (!M() || S.busy) return;
  start(fromComposer());
}

async function start(base, key = newKey()) {
  stopWatch?.(); stopWatch = null;
  const run = { ...base, n: ++seq, key, phase: "prep", step: null, job: null, why: null, net: "", stop: new AbortController() };
  S.run = run; S.showJob = true; S.busy = true;
  renderBar(); runChanged();
  let resend = false;
  try {
    const inputs = await prepare(run);
    if (run.stop.signal.aborted) throw new DOMException("stopped", "AbortError");
    // Once the request is out it is not called back: the job may exist already.
    run.sending = true; setStep(run, { fig: "start", text: () => t("step.starting") });
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
      run.why = refusal(err, { kind: run.kind, clip: err.clip || (!err.file && !!run.clip && err.code !== "price_changed"), name: err.file?.name, maxBytes: err.maxBytes,
        shown: run.shownLabel, pay: run.pay, lastFrame: !!run.cont, clipSecs: run.clipSecs, cut: !!run.cut });
      // The same press again keeps its key when the server may have made the job.
      if (resend) run.retryKey = run.key;
      afterRefusal(run, err);
    }
  } finally {
    if (S.run === run) { S.busy = false; run.sending = false; renderBar(); runChanged(); }
  }
}

// The files of a run go up: photos to framy, a clip to the bucket and then to framy.
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
    catch (e) { e.clip = true; throw e; }
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
  const j = run.job;
  refreshMe();
  if (j.status !== "failed" && j.status !== "expired") return;
  const f = failure(j);
  if (f.closeFree) { S.closed[run.kind] = Date.now() + f.closeFree; render(); }
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
  if (!run) return;
  if (run.phase === "prep") { if (!run.sending) run.stop.abort(); return; }
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

/* What can be done with what came of a run. */

// The run, back in the composer as it was pressed: model, payment, settings, files and words.
// A run of the library brings its own files only; a model that left the
// catalog leaves the composer's.
function restore(run, pay = run.pay) {
  S.kind = run.kind;
  if (C.byId[run.model]) {
    S.model[run.kind] = run.model;
    S.pay[run.model] = pay;
    if (run.pay !== "free") Object.assign(S.wanted, run.settings);
  }
  const keep = new Set(run.files);
  S.files = run.hist ? [...run.files] : [...run.files, ...S.files.filter((f) => !keep.has(f) && f.kind !== "video")];
  S.cut = run.cut ? { ...run.cut } : null;
  S.cont = run.cont;
  $("#prompt").value = run.prompt;
  try { sessionStorage.setItem("framy.draft", run.prompt); } catch {}
}
export function editPrompt(run = S.run) {
  leave(run);
  act(() => { restore(run); S.showJob = false; render(); });
  const p = $("#prompt");
  p.focus({ preventScroll: true });
  p.setSelectionRange(p.value.length, p.value.length);
}
// The same run on another model: the run back in the composer, the picker open.
export function otherModel(run = S.run) {
  leave(run);
  act(() => { restore(run); S.showJob = false; render(); });
  openPicker();
}
export function again(run = S.run) {
  if (S.busy || !C.byId[run.model]) return;
  leave(run);
  act(() => { restore(run); render(); });
  start(copyOf(run), run.retryKey || newKey());
}
// The price went up: the person agreed to the new one.
export function takePrice(run = S.run) {
  const e = run.why?.estimate;
  start(copyOf(run, { shown: shownOf(e), shownLabel: priceText(e), priceLabel: e?.label || "" }));
}
// A free run that could not go free: the same run on the person's own key.
export const keyFor = (run) => { const m = C.byId[run.model]; return m?.offers.map((o) => o.provider).find(keyOk) || ""; };
export function byKey(run = S.run) {
  const p = keyFor(run), m = C.byId[run.model];
  if (!p || !m) return;
  leave(run);
  act(() => { restore(run, p); render(); });
  const r = fastOf(m, p, settings(m, p)), e = known(runItem(r.m, p, r.set))?.estimate;
  start(copyOf(run, { model: r.m.id, pay: p, offer: offerOf(r.m, p).id, settings: r.set, shown: shownOf(e), shownLabel: e ? priceText(e) : "", priceLabel: e?.label || "" }));
}
export function drop() { S.showJob = false; renderStage(); renderBar(); }
// The clip was longer than framy takes whole: its part is picked in the editor.
export function pickPart(run = S.run) { editPrompt(run); setTimeout(openCut, 120); }

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
export async function continueFrom(run = S.run, res = run.job?.results?.[0]) {
  const j = run.job;
  if (!res) return;
  leave(run);
  if (canContinue(j)) {
    const p = providerOf(j);
    act(() => {
      S.kind = "video"; S.model.video = j.model; S.pay[j.model] = p;
      S.files = []; S.cut = null;
      S.cont = { job: j.id, model: j.model, pay: p, secs: res.seconds || 0, url: res.url, w: res.width, h: res.height, result: res.id };
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
    S.files = [{ id: "lf" + shot.id, kind: "image", server: true, name: "", upload: shot.id, w: shot.width, h: shot.height,
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
  return { kind: j.kind, model: j.model, pay: j.free ? "free" : providerOf(j), offer: j.offer, prompt: j.prompt || "", settings: j.settings || {}, files,
    clip: c, cut: null, cont: null, shown: null, shownLabel: "", priceLabel: j.estimate?.label || "", n: 0, key: "", phase: "job", job: j, why: null, net: "", hist: true };
}
function fileOfInput(f) {
  const meta = { content_type: f.content_type, bytes: f.bytes, width: f.width, height: f.height }, base = { id: "j" + f.id, server: true, name: "", upload: f.id, w: f.width, h: f.height };
  if (String(f.content_type).startsWith("video/"))
    return { ...base, kind: "video", url: f.url, poster: f.thumb?.url || "", secs: f.seconds || 0, bytes: f.bytes, token: "", cuts: {}, meta: { ...meta, seconds: f.seconds || 0 } };
  return { ...base, kind: "image", url: f.thumb?.url || f.url, meta };
}
// What a run of the library does before it acts in the composer: the page
// turns to it (library.js says how).
let leaveHist = () => {};
export const onLeave = (fn) => { leaveHist = fn; };
const leave = (run) => { if (run?.hist) leaveHist(); };

// Deleting a job: DELETE /api/jobs/{id}. A job the stage shows goes from it too.
export async function removeJob(id) {
  try { await api(`/api/jobs/${id}`, { method: "DELETE" }); }
  catch (e) { if (e.status !== 404) throw e; }
  if (S.run?.job?.id === id) { stopWatch?.(); stopWatch = null; S.run = null; S.showJob = false; runChanged(); renderBar(); }
}
export { isFinal };
