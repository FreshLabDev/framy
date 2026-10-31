// The motion kit. Everything moves by transform and opacity only, through the
// Web Animations API, so the browser composites it and the layout is
// computed once per change, not on every frame.
import { h } from "./dom.js";

const reduced = matchMedia("(prefers-reduced-motion: reduce)");
export const motion = () => !reduced.matches;
export const EASE = "cubic-bezier(.2,.8,.2,1)";
export const SPRING = "cubic-bezier(.34,1.42,.55,1)";
export const SNAP = "cubic-bezier(.3,1.3,.5,1)";
export const OUT = "cubic-bezier(.45,0,.9,.5)";

export const anim = (el, kf, o = {}) => (el && motion() ? el.animate(kf, { duration: 280, easing: EASE, ...o }) : null);
export const rise = (els, o = {}) =>
  [...els].forEach((el, i) =>
    anim(el, [{ opacity: 0, transform: `translate(${o.x || 0}px, ${o.y ?? 8}px)` }, { opacity: 1, transform: "none" }],
      { duration: o.dur || 340, delay: (o.delay || 0) + i * (o.step ?? 40), easing: o.ease || EASE, fill: "backwards" }));
export const spin = (el, from, to, o = {}) =>
  anim(el, [{ transform: `rotate(${from}deg)` }, { transform: `rotate(${to}deg)` }], { duration: 380, easing: SPRING, ...o });
export const fadeIn = (el, o = {}) => anim(el, [{ opacity: 0 }, { opacity: 1 }], { duration: 240, fill: "backwards", ...o });

// A cubic Bézier easing as a function, for keyframes sampled by hand.
function bezier(css) {
  const m = css.match(/cubic-bezier\(([^)]+)\)/);
  const [x1, y1, x2, y2] = m ? m[1].split(",").map(Number) : [0.25, 0.1, 0.25, 1];
  const cx = 3 * x1, bx = 3 * (x2 - x1) - cx, ax = 1 - cx - bx;
  const cy = 3 * y1, by = 3 * (y2 - y1) - cy, ay = 1 - cy - by;
  const X = (t) => ((ax * t + bx) * t + cx) * t, Y = (t) => ((ay * t + by) * t + cy) * t;
  const dX = (t) => (3 * ax * t + 2 * bx) * t + cx;
  return (x) => {
    let t = x;
    for (let i = 0; i < 8; i++) { const e = X(t) - x, d = dX(t); if (Math.abs(e) < 1e-5 || Math.abs(d) < 1e-6) break; t -= e / d; }
    return Y(Math.min(1, Math.max(0, t)));
  };
}
const curves = new Map();
const curve = (css) => { if (!curves.has(css)) curves.set(css, bezier(css)); return curves.get(css); };

/* A choice: the white block travels from the old option to the new one. Under it lies a field-coloured
   copy of the group moved the other way, so the labels change colour exactly at the block's edges.
   The block keeps its new size and travels by translate and scale; the copy undoes the scale frame by
   frame, so the labels under it never stretch. */
const SEGPOS = new Map();
let segLead = null, segChain = 0;
const segRO = new ResizeObserver((es) => {
  const gs = [];
  for (const e of es) {
    const g = e.target;
    if (!g.isConnected) { segRO.unobserve(g); continue; }
    const w = Math.round(e.contentRect.width);
    if (g._w === undefined) { g._w = w; continue; }
    if (w !== g._w) { g._w = w; gs.push(g); }
  }
  fitSegs(gs);
  for (const draw of gs.map((g) => measureSeg(g, true))) draw?.(false);
});
export function placeSeg(g, animate = true, force = false) { measureSeg(g, force)?.(animate); }
// measureSeg only reads, and gives back the writes: many groups then share
// one layout instead of one each.
function measureSeg(g, force = false) {
  if (!g.isConnected || !g.offsetParent) return null;
  const on = g.querySelector(':scope > [aria-checked="true"], :scope > [aria-current="page"]');
  if (!force && on && g._on === on && g.querySelector(":scope > .ind")) return null;
  const key = g.dataset.seg;
  if (!on) return () => { g._on = on; g.querySelector(":scope > .ind")?.remove(); SEGPOS.delete(key); };
  const r = { x: on.offsetLeft, y: on.offsetTop, w: on.offsetWidth, h: on.offsetHeight }, cw = g.clientWidth;
  return (animate) => drawSeg(g, on, key, r, cw, animate);
}
function drawSeg(g, on, key, r, cw, animate) {
  g._on = on;
  g.querySelector(":scope > .ind")?.remove();
  const mirror = g.cloneNode(true);
  for (const a of ["data-seg", "role", "aria-label", "id", "data-dur"]) mirror.removeAttribute(a);
  mirror.classList.add("mirror");
  mirror.setAttribute("aria-hidden", "true");
  mirror.inert = true;
  mirror.style.width = cw + "px";
  const ind = h("span", { class: "ind", "aria-hidden": "true" }, mirror);
  ind.style.width = r.w + "px";
  ind.style.height = r.h + "px";
  ind.style.transform = `translate(${r.x}px, ${r.y}px)`;
  mirror.style.transform = `translate(${-r.x}px, ${-r.y}px)`;
  g.append(ind);
  if (!g._ro) { g._ro = 1; segRO.observe(g); }
  const v = on.dataset.v ?? on.textContent, prev = SEGPOS.get(key);
  SEGPOS.set(key, { v, r });
  if (!animate || !prev || prev.v === v || !motion()) return;
  const p = prev.r, dur = +(g.dataset.dur || 320), ease = curve(g.dataset.ease || SPRING);
  const delay = !segLead || key === segLead ? 0 : 80 * ++segChain;
  const N = Math.max(12, Math.round(dur / 16)), a = [], b = [];
  for (let i = 0; i <= N; i++) {
    const e = ease(i / N), X = p.x + (r.x - p.x) * e, Y = p.y + (r.y - p.y) * e;
    const sx = (p.w + (r.w - p.w) * e) / r.w, sy = (p.h + (r.h - p.h) * e) / r.h;
    a.push({ transform: `translate(${X}px, ${Y}px) scale(${sx}, ${sy})` });
    b.push({ transform: `scale(${1 / sx}, ${1 / sy}) translate(${-X}px, ${-Y}px)` });
  }
  const o = { duration: dur, easing: "linear", delay, fill: "backwards" };
  ind.animate(a, o);
  mirror.animate(b, o);
}
export function placeSegs(root = document) {
  const gs = [...root.querySelectorAll("[data-seg]")];
  fitSegs(gs);
  for (const draw of gs.map((g) => measureSeg(g))) draw?.(true);
  segLead = null; segChain = 0;
}
// A row of priced tiles that cannot keep every name and price on one line
// stacks all of them, not just the long one. All the groups are measured in
// one go; it gives back the groups that changed.
export function fitSegs(gs) {
  gs = [...gs].filter((g) => g.offsetParent && g.querySelector(":scope > button > small"));
  const was = gs.map((g) => g.classList.contains("stack"));
  for (const g of gs) g.classList.remove("stack");
  const rg = document.createRange();
  const need = (b) => { rg.selectNodeContents(b); return rg.getBoundingClientRect().width + 2 * Math.max(12, parseFloat(getComputedStyle(b).paddingLeft) || 0); };
  const stack = gs.map((g) => [...g.children].some((b) => b.tagName === "BUTTON" && need(b) > b.clientWidth));
  gs.forEach((g, i) => g.classList.toggle("stack", stack[i]));
  return gs.filter((g, i) => stack[i] !== was[i]);
}
// An option of a choice. key names the group, so the pressed group leads and the ones that follow it come a beat later.
export function opt(o, ...kids) {
  return h("button", {
    type: "button", class: o.cls, role: "radio", "aria-checked": String(!!o.on), "data-v": String(o.v), disabled: o.disabled, title: o.title,
    onclick: () => { if (o.on) return; segLead = o.key; o.pick(); },
  }, ...kids);
}
export const leadSeg = (key) => { segLead = key; };

/* Things that keep their key (data-k) move from where they were to where they are now; new ones come in
   and gone ones leave, each place says how. Moves are translate; a box that also changes size (the frame
   on the stage) scales from its old size where o.box allows it. */
export function morph(root, mutate, o = {}) { const fin = snap(root, o); mutate(); fin(); }
const keyed = (root) => [...root.querySelectorAll("[data-k]")].filter((el) => !el.closest(".mirror"));
export function snap(root, o = {}) {
  if (!root || !motion()) return () => {};
  const old = new Map();
  const chainOf = (el) => { const c = []; for (let a = el.parentElement; a && a !== root; a = a.parentElement) c.push(a.getAttribute("class") || ""); return c; };
  for (const el of keyed(root)) old.set(el.dataset.k, { el, r: el.getBoundingClientRect(), chain: chainOf(el) });
  const r0 = root.getBoundingClientRect();
  return () => {
    const r1 = root.getBoundingClientRect(), seen = new Set(), news = [], moved = new Map(), now = keyed(root);
    const rm = o.rel ? { dx: r0.left - r1.left, dy: r0.top - r1.top } : { dx: 0, dy: 0 };
    const at = new Map(now.map((el) => [el, el.getBoundingClientRect()]));
    const leaving = [...old.keys()].some((k) => !now.some((el) => el.dataset.k === k)), wait = leaving ? o.after || 0 : 0;
    for (const el of now) {
      const k = el.dataset.k, p = old.get(k), b = at.get(el);
      seen.add(k);
      if (!p || !p.r.width || !p.r.height) { news.push(el); continue; }
      const up = el.parentElement?.closest("[data-k]"), pm = up && root.contains(up) ? moved.get(up) : null;
      moved.set(el, { dx: p.r.left - b.left, dy: p.r.top - b.top });
      if (el.dataset.km === "stay") continue;
      const resized = o.box && b.width && b.height && (Math.abs(p.r.width - b.width) > 1 || Math.abs(p.r.height - b.height) > 1);
      const dx = p.r.left - b.left - (pm || rm).dx, dy = p.r.top - b.top - (pm || rm).dy;
      if (Math.abs(dx) < 1 && Math.abs(dy) < 1 && !resized) continue;
      const from = resized
        ? { transformOrigin: "0 0", transform: `translate(${dx}px, ${dy}px) scale(${p.r.width / b.width}, ${p.r.height / b.height})` }
        : { transform: `translate(${dx}px, ${dy}px)` };
      el.animate([from, { transformOrigin: "0 0", transform: "none" }], { duration: o.dur || 380, easing: o.ease || EASE, delay: wait, fill: "backwards" });
    }
    news.forEach((el, i) => o.enter?.(el, i, wait));
    if (!o.leave) return;
    for (const [k, p] of old) {
      // One that had no box (hidden, as the empty row of files) was never seen and leaves without a trace.
      if (seen.has(k) || !p.r.width || !p.r.height) continue;
      const g = p.el;
      const pos = { position: "absolute", left: p.r.left - r1.left + root.scrollLeft + "px", top: p.r.top - r1.top + root.scrollTop + "px",
        width: p.r.width + "px", height: p.r.height + "px", margin: "0", pointerEvents: "none" };
      g.removeAttribute("data-k"); g.setAttribute("aria-hidden", "true"); g.inert = true;
      let host = g;
      if (p.chain.length) {
        // Wrappers with the old parents' classes and no boxes of their own keep the look those gave it.
        host = h("div", { "aria-hidden": "true" });
        let cur = host;
        for (const cls of p.chain.slice().reverse()) { const w = h("div", { class: cls }); w.style.display = "contents"; cur.append(w); cur = w; }
        cur.append(g);
        Object.assign(host.style, pos);
        Object.assign(g.style, { position: "static", width: pos.width, height: pos.height, margin: "0" });
      } else Object.assign(g.style, pos);
      root.append(host);
      const done = () => { g.remove(); host.remove(); };
      const a = o.leave(g);
      if (a) a.finished.then(done, done); else done();
    }
  };
}

// A name that changes rolls: the old one leaves upward, the new one comes from below (or the other way).
export function ticker(box, oldText, dir = 1) {
  if (!box || !motion() || !oldText) return;
  const cur = box.firstElementChild;
  if (!cur || cur.textContent === oldText) return;
  box.querySelectorAll(":scope > .out").forEach((x) => x.remove());
  const out = h("span", { class: "out", "aria-hidden": "true" }, oldText);
  box.append(out);
  box.classList.add("rolling");
  out.animate([{ transform: "none", opacity: 1 }, { transform: `translateY(${-dir * 45}%)`, opacity: 0 }],
    { duration: 150, easing: "cubic-bezier(.4,0,1,1)", fill: "forwards" }).finished.then(() => out.remove(), () => out.remove());
  const a = cur.animate([{ transform: `translateY(${dir * 45}%)`, opacity: 0 }, { transform: "none", opacity: 1 }], { duration: 340, delay: 150, easing: EASE, fill: "backwards" });
  const done = () => { if (!box.querySelector(":scope > .out")) box.classList.remove("rolling"); };
  a.finished.then(done, done);
}

// The price is an odometer: only the characters that change roll, right to left, up when it grows.
export function roll(el, text, dir) {
  const old = el.dataset.t || "";
  el.dataset.t = text;
  el.setAttribute("aria-label", text);
  el.replaceChildren(...[...text].map((c) => h("span", { class: "ch", "aria-hidden": "true" }, h("span", {}, c))));
  if (!motion() || !old || old === text) return;
  dir = dir || 1;
  const shape = (s) => (/\d/.test(s) ? s.replace(/\d/g, "") : null);
  if (shape(old) === null || shape(old) !== shape(text)) {
    // "Free" and "≈ $0.04" have nothing in common: the old line leaves whole, the new one comes in whole.
    const g = h("span", { class: "whole", "aria-hidden": "true" }, old);
    el.append(g);
    g.animate([{ transform: "none", opacity: 1 }, { transform: `translateY(${-dir * 60}%)`, opacity: 0 }],
      { duration: 150, easing: "cubic-bezier(.4,0,1,1)", fill: "forwards" }).finished.then(() => g.remove(), () => g.remove());
    for (const col of el.children) if (col !== g) col.firstChild.animate([{ transform: `translateY(${dir * 60}%)`, opacity: 0 }, { transform: "none", opacity: 1 }], { duration: 340, delay: 150, easing: SNAP, fill: "backwards" });
    return;
  }
  const a = [...old], b = [...text], cols = el.children;
  let k = 0;
  for (let i = b.length - 1; i >= 0; i--) {
    const oc = a[a.length - (b.length - i)];
    if (oc === b[i]) continue;
    const d = 28 * k++, col = cols[i];
    col.firstChild.animate([{ transform: `translateY(${dir * 95}%)`, opacity: 0 }, { transform: "none", opacity: 1 }], { duration: 380, easing: SNAP, delay: d, fill: "backwards" });
    if (oc && oc.trim()) {
      const g = h("span", { class: "o", "aria-hidden": "true" }, oc);
      col.append(g);
      g.animate([{ transform: "none", opacity: 1 }, { transform: `translateY(${-dir * 95}%)`, opacity: 0 }], { duration: 240, easing: OUT, delay: d, fill: "forwards" })
        .finished.then(() => g.remove(), () => g.remove());
    }
  }
}

// A chosen model's mark flies from the list into the composer, along a small arc.
export function fly(src, r0, dst) {
  if (!motion() || !src || !dst || !r0?.width) return;
  const r1 = dst.getBoundingClientRect();
  if (!r1.width) return;
  const c = src.cloneNode(true);
  Object.assign(c.style, { position: "fixed", left: r0.left + "px", top: r0.top + "px", width: r0.width + "px", height: r0.height + "px", zIndex: 60, margin: "0", pointerEvents: "none", transformOrigin: "0 0" });
  document.body.append(c);
  dst.style.visibility = "hidden";
  const dx = r1.left - r0.left, dy = r1.top - r0.top, s = r1.width / r0.width;
  const done = () => { c.remove(); dst.style.visibility = ""; anim(dst, [{ transform: "scale(1.14)" }, { transform: "none" }], { duration: 300, easing: SPRING }); };
  c.animate([{ transform: "none" }, { transform: `translate(${dx * 0.5}px, ${dy * 0.5 - 48}px) scale(${((1 + s) / 2) * 1.1})`, offset: 0.5 }, { transform: `translate(${dx}px, ${dy}px) scale(${s})` }],
    { duration: 560, easing: "cubic-bezier(.45,0,.2,1)" }).finished.then(done, done);
}

// Blocks of a column glide to where they now stand when one above them grows or shrinks.
export function follow(host) {
  const was = new Map();
  if (!host || !motion()) return () => {};
  for (const el of host.children) if (el.offsetParent) was.set(el, el.getBoundingClientRect().top);
  return () => {
    for (const [el, t] of was) {
      if (!el.offsetParent || el.getAnimations().length) continue;
      const dy = t - el.getBoundingClientRect().top;
      if (Math.abs(dy) > 1) anim(el, [{ transform: `translateY(${dy}px)` }, { transform: "none" }], { duration: 380, easing: EASE });
    }
  };
}
