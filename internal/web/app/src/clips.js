// Clips: what the browser reads of one before Create, the editor that picks
// the part a run takes, and how a clip goes up on the press. A clip goes by a
// presigned PUT straight into the bucket (POST /api/videos/uploads), framy
// then takes it or cuts the part (POST /api/videos), which the page follows
// (GET /api/videos/{task}); a clip framy keeps already is only cut.
import { $, h, ic } from "./dom.js";
import { t, fmtNum } from "./i18n.js";
import { api, ApiError } from "./api.js";
import { S, M, payOf, clip, maxSecs, minSecs } from "./state.js";
import { opt, placeSeg } from "./motion.js";
import { openSheet, closeSheets } from "./ui.js";
import { act, render } from "./composer.js";

const TYPES = /^video\/(mp4|quicktime|webm|x-matroska|x-m4v)$/, EXT = /\.(mp4|m4v|mov|webm|mkv)$/i;
const BY_EXT = { mp4: "video/mp4", m4v: "video/mp4", mov: "video/quicktime", webm: "video/webm", mkv: "video/x-matroska" };
// The most a clip may weigh before it goes up; POST /api/videos/uploads says the real limit.
export const MAX_CLIP = 512 * 1048576;

export const looksLikeClip = (f) => f.type.startsWith("video/") || (!f.type && EXT.test(f.name));
export const clipTypeOk = (f) => TYPES.test(f.type) || EXT.test(f.name);
const typeOf = (f) => (TYPES.test(f.type) ? f.type : BY_EXT[(f.name.match(EXT)?.[1] || "").toLowerCase()] || f.type || "video/mp4");

// What the browser can read of a clip: its length, its size and a frame for
// the thumbnail. A clip it cannot play (some MKV and HEVC) still goes, with
// what is known; framy reads it on Create.
export function readClip(file) {
  const url = URL.createObjectURL(file);
  const f = { kind: "video", file, name: file.name, url, poster: "", w: 0, h: 0, secs: 0, bytes: file.size, upload: 0, token: "", cuts: {} };
  const done = () => {
    f.meta = { content_type: typeOf(file), bytes: file.size, width: f.w, height: f.h, seconds: f.secs };
    return f;
  };
  return new Promise((ok) => {
    const v = document.createElement("video");
    let end = setTimeout(() => ok(done()), 8000);
    const stop = () => { clearTimeout(end); end = 0; v.removeAttribute("src"); v.load(); ok(done()); };
    v.muted = true; v.playsInline = true; v.preload = "auto";
    v.onloadedmetadata = () => {
      f.secs = isFinite(v.duration) ? Math.round(v.duration * 1000) / 1000 : 0;
      f.w = v.videoWidth; f.h = v.videoHeight;
      if (!f.w) return stop();
      v.currentTime = Math.min(0.1, f.secs / 2);
    };
    v.onseeked = () => {
      try {
        const k = 160 / Math.max(f.w, f.h), c = document.createElement("canvas");
        c.width = Math.max(1, Math.round(f.w * k)); c.height = Math.max(1, Math.round(f.h * k));
        c.getContext("2d").drawImage(v, 0, 0, c.width, c.height);
        f.poster = c.toDataURL("image/jpeg", 0.75);
      } catch {}
      stop();
    };
    v.onerror = stop;
    v.src = url;
  });
}

// A thumbnail of a clip or of a file framy keeps: the poster read before, a
// frame of the clip itself, or a photo's link.
export function thumbMedia(f) {
  if (f.poster) return h("img", { src: f.poster, alt: "" });
  if (f.frameOf) return frameEl(f.frameOf.url, f.frameOf.at);
  if (f.kind === "video" && f.url) return frameEl(f.url, 0.1);
  return f.url ? h("img", { src: f.url, alt: "" }) : h("span", { class: "noth" }, ic(f.kind === "video" ? "film" : "image"));
}
// One frame of a clip, as a still: a video that is never played.
export function frameEl(url, at) {
  const v = h("video", { muted: true, playsinline: true, preload: "metadata", "aria-hidden": "true" });
  v.muted = true;
  v.src = url + "#t=" + Math.max(0, at).toFixed(2);
  return v;
}

/* Going up. */

const MB = (n) => fmtNum(n / 1048576, { maximumFractionDigits: n < 10 * 1048576 ? 1 : 0 });
const pause = (ms, sig) => new Promise((ok, no) => {
  const tm = setTimeout(ok, ms);
  sig?.addEventListener("abort", () => { clearTimeout(tm); no(new DOMException("stopped", "AbortError")); }, { once: true });
});

// One PUT of the clip to the bucket, with how far it has got.
function put(url, file, type, progress, sig) {
  return new Promise((ok, no) => {
    const x = new XMLHttpRequest();
    x.open("PUT", url);
    x.setRequestHeader("Content-Type", type);
    x.upload.onprogress = (e) => progress(e.loaded, e.total || file.size);
    x.onload = () => (x.status >= 200 && x.status < 300 ? ok() : no(new ApiError(503, { error: "storage_unavailable" })));
    x.onerror = () => no(new ApiError(0, { error: "network" }));
    x.onabort = () => no(new DOMException("stopped", "AbortError"));
    sig?.addEventListener("abort", () => x.abort(), { once: true });
    x.send(file);
  });
}

// Starts taking or cutting a clip and follows the task to its file. framy
// takes a few clips at a time: a busy answer is waited out with a countdown.
async function task(body, sig, step, text, fig) {
  let tk;
  for (;;) {
    try { tk = await api("/api/videos", { method: "POST", body, signal: sig }); break; }
    catch (e) {
      if (e.code !== "busy") throw e;
      const until = Date.now() + Math.max(2, e.retryAfter || 3) * 1000;
      step({ fig: "wait", until, text: () => t("step.clip_wait", { left: clockLeft(until) }), tick: true });
      await pause(until - Date.now(), sig);
      step({ fig, text });
    }
  }
  for (let n = 0; ; n++) {
    await pause(n < 5 ? 700 : 1500, sig);
    let r;
    try { r = await api(`/api/videos/${tk.task}`, { signal: sig }); }
    catch (e) { if (e.code === "network") continue; throw e; }
    if (r.status === "done" && r.file) return r.file;
  }
}
const clockLeft = (until) => { const s = Math.max(0, Math.ceil((until - Date.now()) / 1000)); return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0"); };

// The id of the clip a run takes: the whole one, or the part cut of it.
// step(s) tells the stage what is happening now.
export async function takeClip(f, cut, { signal, step, again = false }) {
  if (f.upload && !cut) return f.upload;
  const key = cut ? cut.start.toFixed(3) + "+" + cut.seconds.toFixed(3) : "";
  if (cut && f.cuts[key]) return f.cuts[key];
  const part = cut ? { start: cut.start, seconds: cut.seconds } : {};
  const cutText = () => t("step.cutting", { a: fmtNum(cut.start, { maximumFractionDigits: 1 }), b: fmtNum(cut.start + cut.seconds, { maximumFractionDigits: 1 }) });
  const text = cut ? cutText : () => t("step.clip_prep"), fig = cut ? "cut" : "strip";
  let body;
  if (f.upload) body = { file: f.upload, ...part };
  else {
    if (!f.token) {
      const up = await api("/api/videos/uploads", { method: "POST", signal });
      if (up.max_bytes && f.bytes > up.max_bytes) throw Object.assign(new ApiError(413, { error: "too_large" }), { maxBytes: up.max_bytes });
      let sent = 0;
      step({ fig: "fill", prog: () => sent / f.bytes, text: () => t("step.clip_up", { a: MB(sent), b: MB(f.bytes) }), tick: true });
      await put(up.url, f.file, f.meta.content_type, (n) => { sent = n; }, signal);
      f.token = up.token;
    }
    body = { upload: f.token, ...part };
  }
  step({ fig, text });
  let file;
  try { file = await task(body, signal, step, text, fig); }
  catch (e) {
    // A clip longer than framy takes whole stays under its token for a part;
    // anything else it refused is gone, and a new try uploads it again.
    if (e.code === "video_too_long" && e.body?.seconds) { f.secs = e.body.seconds; f.meta.seconds = f.secs; }
    else if (body.upload && e.code !== "busy" && e.code !== "network" && e.name !== "AbortError") f.token = "";
    // The upload was taken already, by a press stopped while framy worked on
    // it: the clip goes up once more.
    if (e.code === "no_upload" && body.upload && !again) return takeClip(f, cut, { signal, step, again: true });
    throw e;
  }
  if (body.upload) f.token = "";
  if (cut) f.cuts[key] = file.id; else f.upload = file.id;
  if (!f.secs && file.seconds) { f.secs = file.seconds; f.meta.seconds = file.seconds; }
  // How long the clip framy keeps came out, to tell a part a frame too long.
  if (file.seconds) (f.secsOf ||= {})[file.id] = file.seconds;
  return file.id;
}

/* The editor: the clip's timeline with a window of the length the model takes. */

const ED = {};
const fmtT = (s, d = 1) => t("unit.s", { n: fmtNum(s, { minimumFractionDigits: d, maximumFractionDigits: d }) });

export function mountCutter() {
  const el = h("section", { class: "sheet cutter", id: "cutter", hidden: true, role: "dialog", "aria-modal": "true", "aria-labelledby": "cutT" },
    h("header", {}, h("h3", { id: "cutT" }), h("button", { type: "button", class: "x", "data-close": "" }, t("cancel"))),
    h("div", { class: "body", id: "cutBody" }));
  el.addEventListener("closed", () => { stopPlay(); ED.view?.removeAttribute("src"); });
  return el;
}

export function openCut() {
  const c = clip(), m = M();
  if (!c || !m || !c.secs) return;
  const pay = payOf(m), fps = c.fps || 30, total = Math.max(1, Math.round(c.secs * fps));
  // framy cuts on the clip's own frames and rounds the end up to one, and
  // the frame rate is not known here: a part a twentieth of a second under
  // the limit keeps the cut within it at any rate from 20 frames a second.
  const lim = maxSecs(m, pay), maxF = Math.min(total, lim < c.secs ? Math.floor((lim - 0.05) * fps) : total), minF = Math.max(1, Math.round(Math.max(0.1, minSecs(m, pay)) * fps));
  const start = S.cut ? Math.round(S.cut.start * fps) : 0;
  Object.assign(ED, { c, fps, total, maxF, minF, start, end: Math.min(total, S.cut ? start + Math.round(S.cut.seconds * fps) : start + maxF), edge: "start", playing: false });
  if (ED.end - ED.start > maxF) ED.end = ED.start + maxF;
  $("#cutT").textContent = t("cut.title", { max: fmtT(maxF / fps, 0), len: fmtT(c.secs, 0) });
  const view = h("video", { src: c.url, muted: true, playsinline: true, preload: "auto" });
  view.muted = true;
  ED.view = view;
  ED.tc = h("span", { class: "tc" });
  ED.play = h("button", { type: "button", class: "ib", "aria-label": t("cut.play"), title: t("cut.play"), onclick: togglePlay }, ic("play"));
  ED.edges = h("div", { class: "seg edges", role: "radiogroup", "aria-label": t("cut.edge"), "data-seg": "edge", "data-dur": 300 });
  const tl = h("div", { class: "timeline" });
  ED.tl = tl;
  const n = matchMedia("(min-width: 600px)").matches ? 12 : 8, r = (c.w || 16) / (c.h || 9);
  const cvs = Array.from({ length: n }, () => h("canvas", { width: 120, height: Math.max(1, Math.round(120 / r)) }));
  tl.append(...cvs);
  ED.l = h("div", { class: "shade" }); ED.r = h("div", { class: "shade" }); ED.ph = h("div", { class: "ph" });
  ED.win = h("div", { class: "win", role: "group", "aria-label": t("cut.part") }, ["a", "b", "c", "d"].map((k) => h("i", { class: k })),
    h("span", { class: "hl", "data-edge": "start" }), h("span", { class: "hr", "data-edge": "end" }));
  tl.append(ED.l, ED.r, ED.win, ED.ph);
  drag(tl);
  ED.read = h("p", { class: "readout" });
  ED.take = h("button", { type: "button", class: "take", onclick: take }, ic("film"), t("cut.take"));
  $("#cutBody").replaceChildren(
    h("div", { class: "viewer", style: { aspectRatio: `${c.w || 16} / ${c.h || 9}` } }, view, ED.tc),
    h("div", { class: "transport" },
      h("button", { type: "button", class: "ib", "aria-label": t("cut.prev"), title: t("cut.prev"), onclick: () => step(-1) }, ic("prev")), ED.play,
      h("button", { type: "button", class: "ib", "aria-label": t("cut.next"), title: t("cut.next"), onclick: () => step(1) }, ic("next")), ED.edges), tl, ED.read,
    h("p", { class: "note" }, t("cut.keys")), ED.take);
  openSheet("#cutter");
  requestAnimationFrame(() => { layout(); show(ED.start); thumbs(c, cvs); });
}

function take() {
  stopPlay();
  const { fps, start, end, c } = ED;
  const s = Math.floor((start / fps) * 1000) / 1000, sec = Math.min(Math.floor(((end - start) / fps) * 1000) / 1000, c.secs - s);
  closeSheets();
  act(() => { S.cut = { start: s, seconds: sec }; render(); });
}

function layout() {
  const { fps, total, start, end } = ED, a = (start / total) * 100, b = (end / total) * 100;
  Object.assign(ED.win.style, { left: a + "%", width: b - a + "%" });
  Object.assign(ED.l.style, { left: "0", width: a + "%" });
  Object.assign(ED.r.style, { left: b + "%", right: "0" });
  ED.edges.replaceChildren(...[["start", t("cut.start"), start], ["end", t("cut.end"), end]].map(([k, label, f]) =>
    opt({ key: "edge", v: k, on: ED.edge === k, pick: () => setEdge(k) }, label, h("small", {}, fmtT(f / fps, 2)))));
  placeSeg(ED.edges, false, true);
  ED.read.replaceChildren(t("cut.part") + " ", h("b", {}, fmtT(start / fps, 1) + " – " + fmtT(end / fps, 1)), " · " + fmtT((end - start) / fps, 1));
}
function setEdge(e) { stopPlay(); ED.edge = e; layout(); show(e === "start" ? ED.start : ED.end - 1); }
function show(f) {
  const { fps, total } = ED;
  ED.cur = Math.max(0, Math.min(total - 1, f));
  if (!ED.playing) { try { ED.view.currentTime = (ED.cur + 0.5) / fps; } catch {} }
  ED.tc.textContent = fmtT(ED.cur / fps, 2);
  ED.ph.style.transform = `translateX(${((ED.cur + 0.5) / total) * ED.tl.clientWidth}px)`;
}
function step(d) {
  stopPlay();
  const { total, maxF, minF } = ED;
  if (ED.edge === "start") { ED.start = Math.max(0, Math.min(ED.end - minF, ED.start + d)); if (ED.end - ED.start > maxF) ED.end = ED.start + maxF; }
  else { ED.end = Math.min(total, Math.max(ED.start + minF, ED.end + d)); if (ED.end - ED.start > maxF) ED.start = ED.end - maxF; }
  layout(); show(ED.edge === "start" ? ED.start : ED.end - 1);
}
function togglePlay() { if (ED.playing) stopPlay(); else startPlay(); }
function startPlay() {
  ED.playing = true;
  ED.play.replaceChildren(ic("pause"));
  ED.view.currentTime = ED.start / ED.fps;
  // Played on a press, the part is heard; stepping through frames stays silent.
  ED.view.muted = false;
  ED.view.play().catch(() => { ED.view.muted = true; ED.view.play().catch(() => stopPlay()); });
  const tick = () => {
    if (!ED.playing) return;
    const f = Math.floor(ED.view.currentTime * ED.fps);
    if (f >= ED.end) { stopPlay(); show(ED.start); return; }
    show(f);
    ED.raf = requestAnimationFrame(tick);
  };
  ED.raf = requestAnimationFrame(tick);
}
function stopPlay() {
  if (!ED.playing) return;
  ED.playing = false;
  cancelAnimationFrame(ED.raf);
  ED.view.pause();
  ED.view.muted = true;
  ED.play.replaceChildren(ic("play"));
}
// The window moves when dragged, an edge when its handle is, and a press
// elsewhere on the timeline puts the window's middle there.
function drag(tl) {
  let d = null;
  const at = (x) => { const r = tl.getBoundingClientRect(); return Math.round(((x - r.left) / r.width) * ED.total); };
  tl.addEventListener("pointerdown", (e) => {
    stopPlay();
    const edge = e.target.dataset?.edge, f = at(e.clientX);
    if (edge) { ED.edge = edge; d = { edge }; layout(); }
    else {
      if (e.target !== ED.win) { const len = ED.end - ED.start; ED.start = Math.max(0, Math.min(ED.total - len, f - Math.round(len / 2))); ED.end = ED.start + len; layout(); show(ED.start); }
      d = { move: true, f, start: ED.start, end: ED.end };
    }
    tl.setPointerCapture(e.pointerId);
  });
  tl.addEventListener("pointermove", (e) => {
    if (!d) return;
    const f = at(e.clientX), { total, maxF, minF } = ED;
    if (d.move) { const len = d.end - d.start; ED.start = Math.max(0, Math.min(total - len, d.start + f - d.f)); ED.end = ED.start + len; layout(); show(ED.start); return; }
    if (d.edge === "start") { ED.start = Math.max(0, Math.min(ED.end - minF, f)); if (ED.end - ED.start > maxF) ED.end = ED.start + maxF; layout(); show(ED.start); }
    else { ED.end = Math.min(total, Math.max(ED.start + minF, f)); if (ED.end - ED.start > maxF) ED.start = ED.end - maxF; layout(); show(ED.end - 1); }
  });
  const up = () => { d = null; };
  tl.addEventListener("pointerup", up);
  tl.addEventListener("pointercancel", up);
}
// The timeline's frames, one seek at a time, while the sheet is open.
async function thumbs(c, cvs) {
  const v = h("video", { muted: true, preload: "auto", playsinline: true });
  v.muted = true;
  v.src = c.url;
  await new Promise((ok) => { v.onloadeddata = ok; v.onerror = ok; setTimeout(ok, 6000); });
  for (let i = 0; i < cvs.length; i++) {
    if ($("#cutter").hidden) break;
    await new Promise((ok) => { v.onseeked = ok; v.onerror = ok; setTimeout(ok, 3000); v.currentTime = ((i + 0.5) / cvs.length) * c.secs; });
    try { cvs[i].getContext("2d").drawImage(v, 0, 0, cvs[i].width, cvs[i].height); } catch {}
  }
  v.removeAttribute("src"); v.load();
}
addEventListener("keydown", (e) => {
  if ($("#cutter")?.hidden !== false) return;
  if (e.key === "ArrowLeft" || e.key === "ArrowRight") { step((e.key === "ArrowLeft" ? -1 : 1) * (e.shiftKey ? ED.fps : 1)); e.preventDefault(); }
  if (e.key === " " && e.target.tagName !== "BUTTON") { togglePlay(); e.preventDefault(); }
});
