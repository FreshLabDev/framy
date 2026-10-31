// A video in the page's own look, the same in every browser and in Telegram:
// play, a bar to seek on, the time, the sound and the whole screen. A joined
// clip's bar is cut into its parts (res.segments, where each part starts, in
// seconds), and the part playing stands taller than the others.
import { h, ic } from "./dom.js";
import { t, fmtNum } from "./i18n.js";
import { anim } from "./motion.js";

export const fmtClock = (s) => {
  const n = Math.max(0, Math.floor(+s || 0));
  return fmtNum(Math.floor(n / 60)) + ":" + fmtNum(n % 60, { minimumIntegerDigits: 2 });
};

// The parts of a clip of dur seconds: [start, end] each, from its starts.
export function partsOf(segs, dur) {
  const a = (Array.isArray(segs) ? segs : []).filter((x) => Number.isFinite(x) && x > 0 && x < dur).sort((x, y) => x - y);
  a.unshift(0);
  return a.map((s, i) => [s, i + 1 < a.length ? a[i + 1] : dur]).filter(([s, e]) => e - s > 0.01);
}

const fsNow = () => document.fullscreenElement || document.webkitFullscreenElement || null;
const fsOk = (el) => !!(document.fullscreenEnabled || document.webkitFullscreenEnabled) && !!(el.requestFullscreen || el.webkitRequestFullscreen);
const onFs = () => { for (const p of document.querySelectorAll(".player")) p._fs?.(); };
document.addEventListener("fullscreenchange", onFs);
document.addEventListener("webkitfullscreenchange", onFs);

const STEP = 5; // seconds an arrow key moves
const IDLE = 2500; // how long the bar stays after the last move while playing

export function player(res, o = {}) {
  const v = h("video", { src: res.url, poster: res.thumb?.url || null, playsinline: true, loop: true, muted: true, autoplay: o.autoplay !== false, preload: "metadata" });
  v.muted = true; // the attribute alone does not mute everywhere, and a sound would stop autoplay
  const B = (icon, label, fn, cls) => h("button", { type: "button", class: "ib" + (cls ? " " + cls : ""), "aria-label": label, title: label, onclick: (e) => { e.stopPropagation(); fn(); wake(); } }, ic(icon));
  const pp = B("play", t("player.play"), toggle, "pp");
  const track = h("div", { class: "ptrack", role: "slider", tabindex: "0", "aria-label": t("player.seek"), "aria-valuemin": "0" });
  const time = h("span", { class: "ptime" });
  const snd = B("mute", t("player.sound_on"), () => { v.muted = !v.muted; if (!v.muted && v.volume === 0) v.volume = 1; });
  const full = B("expand", t("player.full"), fullscreen, "pfull");
  const bar = h("div", { class: "pbar", dir: "ltr" }, pp, track, time, snd, full);
  const el = h("div", { class: "player", "data-k": o.k || null }, v, bar);
  if (!fsOk(el) && !v.webkitEnterFullscreen) full.hidden = true;

  let dur = +res.seconds || 0, parts = [], segs = [], fills = [], cur = -1, shownSec = -1, scrub = null, raf = 0, hideT = 0, idle = false, touch = false;
  function build() {
    parts = dur > 0 ? partsOf(res.segments, dur) : [];
    segs = []; fills = [];
    track.replaceChildren(...parts.map(([a, b]) => {
      const fill = h("i"), seg = h("span", { class: "pseg", style: { flex: `${b - a} 1 0px` } }, fill);
      segs.push(seg); fills.push(fill);
      return seg;
    }));
    track.classList.toggle("many", parts.length > 1);
    track.setAttribute("aria-valuemax", String(Math.floor(dur)));
    cur = -1; shownSec = -1;
    paint();
  }
  const pos = () => (scrub != null ? scrub : v.currentTime || 0);
  function paint() {
    const now = pos();
    for (let i = 0; i < parts.length; i++) {
      const [a, b] = parts[i];
      fills[i].style.transform = `scaleX(${Math.min(1, Math.max(0, (now - a) / (b - a)))})`;
    }
    let k = parts.findIndex(([, b]) => now < b);
    if (k < 0) k = parts.length - 1;
    if (k !== cur) { segs[cur]?.classList.remove("on"); segs[k]?.classList.add("on"); cur = k; }
    const sec = Math.floor(now);
    if (sec !== shownSec) {
      shownSec = sec;
      time.textContent = fmtClock(now) + " / " + fmtClock(dur);
      track.setAttribute("aria-valuenow", String(sec));
      track.setAttribute("aria-valuetext", t("view.of", { i: fmtClock(now), n: fmtClock(dur) }));
    }
  }
  const loop = () => { raf = 0; if (!el.isConnected) return; paint(); if (!v.paused) raf = requestAnimationFrame(loop); };
  const setPP = () => {
    const on = !v.paused, label = t(on ? "player.pause" : "player.play");
    pp.replaceChildren(ic(on ? "pause" : "play")); pp.setAttribute("aria-label", label); pp.title = label;
  };
  const setSnd = () => {
    const label = t(v.muted ? "player.sound_on" : "player.sound_off");
    snd.replaceChildren(ic(v.muted ? "mute" : "audio")); snd.setAttribute("aria-label", label); snd.title = label;
  };
  el._fs = () => {
    const on = fsNow() === el, label = t(on ? "player.exit" : "player.full");
    el.classList.toggle("fs", on);
    full.replaceChildren(ic(on ? "shrink" : "expand")); full.setAttribute("aria-label", label); full.title = label;
  };

  // The bar fades while the clip plays untouched, and comes back on any move.
  function setIdle(on) {
    if (idle === on) return;
    idle = on;
    el.classList.toggle("idle", on);
    anim(bar, [{ opacity: on ? 1 : 0 }, { opacity: on ? 0 : 1 }], { duration: on ? 320 : 160 });
  }
  function wake() {
    setIdle(false);
    clearTimeout(hideT);
    if (!v.paused && scrub == null) hideT = setTimeout(() => { if (bar.contains(document.activeElement)) wake(); else if (!v.paused && scrub == null) setIdle(true); }, IDLE);
  }
  function toggle() { if (v.paused) v.play().catch(() => {}); else v.pause(); }
  function seekTo(s) { v.currentTime = Math.min(dur, Math.max(0, s)); paint(); }
  function fullscreen() {
    if (fsNow() === el) { (document.exitFullscreen || document.webkitExitFullscreen)?.call(document)?.catch?.(() => {}); return; }
    if (fsOk(el)) { (el.requestFullscreen || el.webkitRequestFullscreen).call(el)?.catch?.(() => {}); return; }
    // An iPhone shows a video on the whole screen only in its own player.
    try { v.webkitEnterFullscreen?.(); } catch {}
  }

  v.addEventListener("loadedmetadata", () => { if (Number.isFinite(v.duration) && v.duration > 0 && Math.abs(v.duration - dur) > 0.05) { dur = v.duration; build(); } });
  v.addEventListener("play", () => { setPP(); if (!raf) raf = requestAnimationFrame(loop); wake(); });
  v.addEventListener("pause", () => { setPP(); wake(); paint(); });
  v.addEventListener("seeked", paint);
  v.addEventListener("volumechange", setSnd);
  // A press on the picture plays or pauses it; on a touch screen the first
  // press while the bar is hidden only brings the bar back.
  v.addEventListener("pointerdown", (e) => { touch = e.pointerType === "touch"; });
  v.addEventListener("click", () => { if (touch && idle) { wake(); return; } toggle(); });
  el.addEventListener("pointermove", (e) => { if (e.pointerType !== "touch") wake(); });
  el.addEventListener("focusin", wake);

  // Seeking: a press on the bar goes there and a drag follows the finger; the
  // gaps between parts count as the start of the next one.
  const at = (x) => {
    for (let i = 0; i < segs.length; i++) {
      const r = segs[i].getBoundingClientRect();
      if (x <= r.right || i === segs.length - 1) { const [a, b] = parts[i]; return a + Math.min(1, Math.max(0, (x - r.left) / r.width)) * (b - a); }
    }
    return 0;
  };
  track.addEventListener("pointerdown", (e) => {
    if (!parts.length || e.button > 0) return;
    e.preventDefault(); e.stopPropagation();
    try { track.setPointerCapture(e.pointerId); } catch {}
    scrub = at(e.clientX);
    v.currentTime = scrub;
    paint(); wake();
  });
  track.addEventListener("pointermove", (e) => {
    if (scrub == null) return;
    scrub = at(e.clientX);
    if (v.fastSeek) v.fastSeek(scrub); else v.currentTime = scrub;
    paint();
  });
  const drop = () => { if (scrub == null) return; v.currentTime = scrub; scrub = null; paint(); wake(); };
  track.addEventListener("pointerup", drop);
  track.addEventListener("pointercancel", drop);
  track.addEventListener("click", (e) => e.stopPropagation());
  // Arrows move five seconds, Page keys go between parts, Home and End to the ends.
  track.addEventListener("keydown", (e) => {
    const now = pos(), k = Math.max(0, cur);
    let to = null;
    if (e.key === "ArrowRight" || e.key === "ArrowUp") to = now + STEP;
    else if (e.key === "ArrowLeft" || e.key === "ArrowDown") to = now - STEP;
    else if (e.key === "Home") to = 0;
    else if (e.key === "End") to = dur;
    else if (e.key === "PageDown") to = parts[k + 1]?.[0] ?? dur;
    else if (e.key === "PageUp") to = now - parts[k][0] > 1 || k === 0 ? parts[k][0] : parts[k - 1][0];
    if (to == null || !parts.length) return;
    e.preventDefault(); e.stopPropagation();
    seekTo(to); wake();
  });
  el.addEventListener("keydown", (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const k = e.key.toLowerCase();
    if ((k === " " && e.target.tagName !== "BUTTON") || k === "k") toggle();
    else if (k === "m") v.muted = !v.muted;
    else if (k === "f" && !full.hidden) fullscreen();
    else return;
    e.preventDefault(); e.stopPropagation(); wake();
  });

  setPP(); setSnd(); el._fs(); build(); wake();
  return el;
}
