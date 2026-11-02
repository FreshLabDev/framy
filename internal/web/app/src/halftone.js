// The halftone on the stage: the bot's generating loop
// (internal/anim/generating.go) drawn live in the frame while a job runs, and
// a figure of its own for every other state of a run: the files going up, the
// clip being cut, the queue, a wait, the start and each way a run can fail.
// The dots flow from one figure to the next as the run moves on; a result
// shows through the dots as they swell until they touch, and settles in.
// One canvas serves every render of a frame, so the motion never restarts;
// several frames of one press have one each (o.id names it).
import { h } from "./dom.js";
import { motion } from "./motion.js";

const BG = [0, 69, 79];
const HAZE = BG.map((v) => Math.round(v + (255 - v) * 0.62));
const SHADES = Array.from({ length: 9 }, (_, i) => `rgb(${HAZE.map((v) => Math.round(v + (255 - v) * i / 8)).join(",")})`);
const BRACKET = "#D9E7E9";

/* Shapes, as signed distances around the middle of a figure's field (negative inside). */
const clamp01 = (x) => Math.min(1, Math.max(0, x));
const smooth = (a, b, x) => { const t = clamp01((x - a) / (b - a)); return t * t * (3 - 2 * t); };
const smin = (a, b, k) => { const q = clamp01(0.5 + 0.5 * (b - a) / k); return b + (a - b) * q - k * q * (1 - q); };
const frac = (x) => x - Math.floor(x);
const TAU = 2 * Math.PI;
const seg = (x, y, ax, ay, bx, by, r) => {
  const px = x - ax, py = y - ay, ex = bx - ax, ey = by - ay, k = clamp01((px * ex + py * ey) / (ex * ex + ey * ey));
  return Math.hypot(px - ex * k, py - ey * k) - r;
};
const rect = (x, y, hw, hh) => { const dx = Math.abs(x) - hw, dy = Math.abs(y) - hh; return Math.hypot(Math.max(dx, 0), Math.max(dy, 0)) + Math.min(Math.max(dx, dy), 0); };
const ring = (x, y, R, w) => Math.abs(Math.hypot(x, y) - R) - w;
const tri = (x, y, a, b, c) => {
  const edge = (p, q) => { const ex = q[0] - p[0], ey = q[1] - p[1], wx = x - p[0], wy = y - p[1], k = clamp01((wx * ex + wy * ey) / (ex * ex + ey * ey)), dx = wx - ex * k, dy = wy - ey * k; return [dx * dx + dy * dy, ex * wy - ey * wx]; };
  const [d1, s1] = edge(a, b), [d2, s2] = edge(b, c), [d3, s3] = edge(c, a), d = Math.sqrt(Math.min(d1, d2, d3));
  return (s1 >= 0) === (s2 >= 0) && (s2 >= 0) === (s3 >= 0) ? -d : d;
};
const grain = (i, j) => { let q = (Math.imul(i, 73856093) ^ Math.imul(j, 19349663)) >>> 0; q ^= q >>> 13; q = Math.imul(q, 0x5bd1e995) >>> 0; q ^= q >>> 15; return (q & 0xffff) / 0xffff; };
// A change sweeps across the frame, a dot at a time, the way the loop goes from
// figure to figure; along is how far across the frame the dot stands, 0 to 1.
const sweep = (s, i, j, along, back) => smooth(0, 1, (s - 0.45 * (0.8 * (back ? 1 - along : along) + 0.2 * grain(i, j))) / 0.55);

// The generating loop: six figures, each for 2.4 s, the next one sweeping in over a second.
const LOOP = (() => {
  const figs = [
    (x, y, th) => { let d = 0; for (let k = 0; k < 3; k++) { const a = th * 2 + k * TAU / 3, ball = Math.hypot(x - 170 * Math.cos(a), y - 70 * Math.sin(a)) - [140, 115, 95][k]; d = k ? smin(d, ball, 70) : ball; } return d; },
    (x, y, th) => Math.max(Math.abs(y - 80 * Math.sin(x * TAU / 440 + th * 6)) - 52, Math.abs(x) - 470),
    (x, y, th) => { const s = 290 * (1 + 0.05 * Math.sin(th * 12)), px = Math.abs(x) / s, py = -y / s + 0.55;
      if (px + py > 1) return (Math.hypot(px - 0.25, py - 0.75) - Math.SQRT2 / 4) * s; const a = Math.hypot(px, py - 1), b = Math.max(px + py, 0) * 0.5;
      return Math.sqrt(Math.min(a * a, (px - b) ** 2 + (py - b) ** 2)) * Math.sign(px - py || 1) * s; },
    (x, y, th) => Math.min(tri(x, y, [-330, 210], [-80, -110], [170, 210]), tri(x, y, [20, 210], [210, -10], [400, 210]), Math.hypot(x - 250, y + 150 - 18 * Math.sin(th * 6)) - 68),
    (x, y, th) => Math.hypot(x, y) - 230 * (0.55 + 0.45 * Math.abs(Math.cos(2.5 * (Math.atan2(y, x) + th)))),
    (x, y, th) => { const a = 0.25 * Math.sin(th * 6), sn = Math.sin(a), cs = Math.cos(a), X = cs * x + sn * y, Y = -sn * x + cs * y; return Math.max(Math.hypot(X, Y) - 220, -(Math.hypot(X - 110, Y + 40) - 185)); },
  ];
  const SHAPE = 2.4, MORPH = 1.0, N = figs.length, DUR = SHAPE * N;
  const at = (k, x, y, th) => { const mid = TAU * (k + (SHAPE + MORPH) / (2 * SHAPE)) / N; let dx = 24 * Math.sin(th - mid); if (k % 2) dx = -dx; return figs[k](x - dx, y, th); };
  return (i, j, x, y, t, along) => {
    const u = frac(t / DUR), th = TAU * u, pos = u * N, k = Math.floor(pos) % N;
    const m = sweep((pos - Math.floor(pos)) * SHAPE / MORPH, i, j, along, k % 2);
    let d = at(k, x, y, th);
    if (m < 1) { const was = at((k + N - 1) % N, x, y, th); d = was + (d - was) * m; }
    return d;
  };
})();

// A strip of film running left: rails with sprocket holes and the frames between them.
const film = (x, y, t) => {
  const X = x + t * 140, hole = rect(frac(X / 100) * 100 - 50, Math.abs(y) - 122, 19, 17), win = rect(frac(X / 270) * 270 - 135, y, 104, 82);
  return Math.max(rect(x, y, 640, 160), -hole, -win);
};
const link = (x, y, cx) => Math.abs(seg(x, y, cx - 50, 0, cx + 50, 0, 0) - 84) - 34;

// Every figure takes (x, y) in its field, t in seconds, the scene, the dot's
// place (i, j) and how far across the frame it stands.
const FIGS = {
  // The job runs: the loop of the bot.
  loop: (x, y, t, s, i, j, along) => LOOP(i, j, x, y, t + s.phase, along),
  // The press goes to framy: a disc that beats, a ring going out of it.
  start: (x, y, t) => { const u = frac(t / 1.3); return Math.min(Math.hypot(x, y) - 92 - 16 * Math.sin(t * 5), ring(x, y, 110 + 230 * u, 32) + 44 * u); },
  // Waiting in the queue: a ring with a bright comet running round it.
  queue: (x, y, t) => { let d = Math.atan2(y, x) - t * 3.2; d -= TAU * Math.floor((d + Math.PI) / TAU); const tail = d < 0 ? Math.exp(d * 1.4) : Math.exp(-d * d * 30); return Math.abs(Math.hypot(x, y) - 190) - 20 - 46 * tail; },
  // A wait with a known end: the wedge of what is left of it, in its dial.
  wait: (x, y, t, s) => {
    const f = s.left(), R = 150, r = Math.hypot(x, y), A = f * TAU; let a = Math.atan2(x, -y); if (a < 0) a += TAU;
    const edges = Math.min(seg(x, y, 0, 0, 0, -R, 0), seg(x, y, 0, 0, R * Math.sin(A), -R * Math.cos(A), 0));
    return Math.min(f <= 0 ? 1e4 : a <= A ? Math.max(r - R, -edges) : edges, ring(x, y, R + 62, 24));
  },
  // Photos going up: arrows rising from a tray.
  up: (x, y, t) => {
    let d = seg(x, y, -240, 222, 240, 222, 34);
    for (const k of [0, 0.5]) {
      const u = frac(t / 1.6 + k), o = 100 - 260 * u;
      const a = Math.min(seg(x, y, 0, 120 + o, 0, -20 + o, 46), tri(x, y, [-150, 10 + o], [150, 10 + o], [0, -190 + o]));
      d = Math.min(d, a + 260 * Math.max(smooth(0.65, 1, u), 1 - smooth(0, 0.3, u)));
    }
    return d;
  },
  // A clip going up: the frame fills to how much of it has gone, with a wave on top.
  fill: (x, y, t, s) => {
    const p = clamp01(s.prog?.() || 0), L = 118 - 236 * p + 14 * Math.sin(x / 50 + t * 3.5);
    return Math.min(Math.abs(rect(x, y, 270, 180)) - 22, Math.max(rect(x, y, 214, 124), L - y));
  },
  // framy takes the clip: the film runs.
  strip: (x, y, t) => film(x, y, t),
  // framy cuts a part of it: the film runs through a cut that opens and closes, marked above and below.
  cut: (x, y, t) => {
    const g = 26 + 30 * (0.5 + 0.5 * Math.sin(t * 4));
    return Math.min(Math.max(film(x, y, t), g - Math.abs(x)), tri(x, y, [-62, -272], [62, -272], [0, -192]), tri(x, y, [-62, 272], [62, 272], [0, 192]));
  },
  // Why a run gave nothing.
  cross: (x, y) => Math.min(seg(x, y, -165, -165, 165, 165, 54), seg(x, y, 165, -165, -165, 165, 54)),
  ban: (x, y) => Math.min(ring(x, y, 200, 44), seg(x, y, -140, -140, 140, 140, 44)),
  coin: (x, y) => Math.max(Math.hypot(x, y) - 215, -ring(x, y, 150, 16), -rect(x, y, 20, 74)),
  clock: (x, y) => Math.min(ring(x, y, 205, 36), seg(x, y, 0, 0, 0, -130, 36), seg(x, y, 0, 0, 100, 0, 36)),
  key: (x, y) => Math.min(ring(x + 175, y, 92, 44), rect(x - 85, y, 172, 46), rect(x - 170, y - 80, 30, 52), rect(x - 240, y - 64, 28, 36)),
  link: (x, y) => Math.min(link(x, y, -195), link(x, y, 195), seg(x, y, -30, -130, -62, -200, 26), seg(x, y, 30, 130, 62, 200, 26)),
  hush: (x, y) => Math.min(Math.hypot(x + 190, y) - 72, Math.hypot(x, y) - 72, Math.hypot(x - 190, y) - 72),
  bang: (x, y) => Math.min(seg(x, y, 0, -210, 0, 40, 56), Math.hypot(x, y - 162) - 62),
  line: (x, y) => seg(x, y, -280, 0, 280, 0, 44),
};
// The field a figure is laid out in, fitted to the frame: the loop and the
// film run across a wide one, as in the bot; the others stand in the middle.
const FIELD = { loop: [1000, 560], strip: [1000, 600], cut: [1000, 600] }, SIGN = [760, 640];
// The figure fig on the frame laid out as L: its distance from a dot at (px, py), in pixels.
const sizeOf = (fig, L) => { const [w, hh] = FIELD[fig] || SIGN; return Math.min(L.aw / w, L.ah / hh) * 0.95; };
const distPx = (fig, sc, L, px, py, t, i, j) => { const s = sizeOf(fig, L); return FIGS[fig]((px - L.cx) / s, (py - L.cy) / s, t, sc, i, j, px / L.W) * s; };

/* The canvases and what each shows. "stage" is the one of a lone frame. */
const ALL = new Map();
const inst = (id) => { let s = ALL.get(id); if (!s) ALL.set(id, (s = { id, cv: null, scene: null, from: null, at: 0, born: 0, lay: null, lostAt: 0, n: 0, img: null })); return s; };
let raf = 0, last = 0;
const BOOT = 900, MORPH = 900, REVEAL = 1800;

// halftone puts the stage's canvas into box with the figure fig. o: n, the
// run it belongs to; still, a figure that stays where it is; until, the end of
// a wait; prog, how far an upload has got; lost, framy cannot be reached.
// The figure "reveal" shows the result in box through the dots; it needs a
// figure on screen before it, and without one the canvas stays out.
export function halftone(box, fig, o = {}) {
  const ST = inst(o.id || "stage"), now = performance.now(), showing = !!ST.cv?.isConnected && !!ST.scene;
  if (fig === "reveal" && !(showing || ST.scene?.fig === "reveal")) { ST.scene = null; return false; }
  if (!ST.cv) ST.cv = h("canvas", { class: "ht", "aria-hidden": "true" });
  const key = fig + "|" + (o.until || "") + "|" + (o.n || 0), was = ST.scene;
  if (!was || was.key !== key) {
    if (showing && o.n === ST.n) { ST.from = was; ST.at = now; }
    else { ST.from = null; ST.born = now; ST.lay = null; }
    const sc = ST.scene = { key, fig, still: !!o.still, until: o.until || 0, prog: o.prog, t0: now, phase: was?.phase ?? Math.random() * 14.4, revealAt: 0 };
    // What is left of a wait, from when it showed to its end (the clock of the page, which until is on too).
    const wall = Date.now();
    sc.left = () => (sc.until > wall ? clamp01((sc.until - Date.now()) / (sc.until - wall)) : 0);
    if (fig === "reveal") ST.img = null;
  } else ST.scene.prog = o.prog;
  ST.n = o.n || 0;
  ST.lostAt = o.lost ? ST.lostAt || now : 0;
  box.classList.add("ht");
  box.prepend(ST.cv);
  if (!raf) raf = requestAnimationFrame(frame);
  return true;
}

function frame(ts) {
  raf = 0;
  const on = [];
  for (const ST of ALL.values()) {
    if (ST.cv?.isConnected && ST.scene) on.push(ST);
    // A frame's canvas that left the page is forgotten; the stage's keeps its scene for the next render.
    else if (ST.id !== "stage") ALL.delete(ST.id);
  }
  if (!on.length) return;
  raf = requestAnimationFrame(frame);
  // 30 frames a second, as the bot's loop; with reduced motion only what changes.
  if (ts - last < (motion() ? 33 : 500)) return;
  last = ts;
  const now = performance.now();
  for (const ST of on) paint(ST, now);
}

function paint(ST, now) {
  const cv = ST.cv, box = cv.parentElement, W = box.clientWidth, H = box.clientHeight, sc = ST.scene;
  if (!W || !H) return;
  const d = Math.min(2, devicePixelRatio || 1);
  if (cv.width !== Math.round(W * d) || cv.height !== Math.round(H * d)) { cv.width = Math.round(W * d); cv.height = Math.round(H * d); ST.lay = sc.fig === "reveal" ? ST.lay : null; }
  const x = cv.getContext("2d");
  x.setTransform(d, 0, 0, d, 0, 0);
  if (sc.fig === "reveal") return reveal(ST, x, box, W, H, now);
  // The line under the figure keeps its place: the dots stay above it.
  const c = box.querySelector(".c"), bottom = H - Math.max(22, (c?.offsetHeight || 0) + 4);
  const L = layout(ST, W, H, bottom);
  x.fillStyle = `rgb(${BG.join(",")})`;
  x.fillRect(0, 0, W, H);
  dots(ST, x, L, sc, now);
}

// Where the dots stand: a grid inside the corners and above the line, the figures in its middle.
function layout(ST, W, H, bottom) {
  const k = W + "x" + H + "x" + bottom;
  if (ST.lay?.k === k) return ST.lay;
  const m = Math.min(W, H), pitch = Math.max(7, m / 30), top = 22, ah = Math.max(40, bottom - top), aw = W - 44;
  const cols = Math.max(1, Math.floor(aw / pitch) + 1), rows = Math.max(1, Math.floor(ah / pitch) + 1);
  const cx = W / 2, cy = top + ah / 2;
  return (ST.lay = { k, W, H, aw, ah, pitch, cols, rows, cx, cy, x0: cx - (cols - 1) * pitch / 2, y0: cy - (rows - 1) * pitch / 2 });
}

// The time a figure is drawn at: still figures and reduced motion stay at their start, and a lost connection freezes it.
const timeOf = (ST, sc, now) => (ST.lostAt ? ST.lostAt : sc.still || !motion() ? sc.t0 : now) / 1000;

// The dots of the scene sc (and of the one it is leaving), each in one of nine shades.
function dots(ST, x, L, sc, now) {
  const { pitch, cols, rows, x0, y0, W, H } = L, rMax = pitch * 0.42, rMin = pitch * 0.08, half = Math.hypot(W, H) / 2;
  const t = timeOf(ST, sc, now), from = ST.from, tf = from && timeOf(ST, from, now);
  const mor = from && motion() ? (now - ST.at) / MORPH : 1;
  if (mor >= 1) ST.from = null;
  const boot = motion() ? Math.min(1, (now - ST.born) / BOOT) : 1;
  // A figure that stays shimmers a little, so the frame is not dead.
  const still = sc.still && motion() && !ST.lostAt, ts = now / 1000;
  const paths = SHADES.map(() => new Path2D());
  for (let j = 0; j < rows; j++) for (let i = 0; i < cols; i++) {
    const px = x0 + i * pitch, py = y0 + j * pitch;
    const wake = boot >= 1 ? 1 : smooth(0, 1, boot * 1.9 - Math.hypot(px - W / 2, py - H / 2) / half);
    if (wake <= 0) continue;
    let dd = distPx(sc.fig, sc, L, px, py, t, i, j);
    if (mor < 1) { const was = distPx(from.fig, from, L, px, py, tf, i, j), m = sweep(mor, i, j, px / W, false); dd = was + (dd - was) * m; }
    let v = smooth(-pitch * 0.4, pitch * 1.1, -dd);
    if (still) v *= 0.9 + 0.1 * Math.sin(ts * 1.6 - (px + py) / (pitch * 6));
    const r = Math.max(rMin, rMax * v) * wake, p = paths[Math.round(smooth(0, 0.35, v) * 8)];
    p.moveTo(px + r, py);
    p.arc(px, py, r, 0, TAU);
  }
  x.globalAlpha = ST.lostAt ? 0.4 : 1;
  paths.forEach((p, k) => { x.fillStyle = SHADES[k]; x.fill(p); });
  x.globalAlpha = 1;
}

// The result is ready: the dots of the figure before it swell, from the
// figure outward, until they touch, showing the result through them; the
// corners open out to the edges and the sharp result settles in.
function reveal(ST, x, box, W, H, now) {
  const sc = ST.scene, from = ST.from || { fig: "loop", phase: sc.phase, t0: sc.t0, still: false };
  // The figure stays where it stood; a frame of another shape lays it out again.
  const L = ST.lay?.W === W && ST.lay?.H === H ? ST.lay : layout(ST, W, H, H - 22);
  const media = box.querySelector("img, video"), ready = media && (media.tagName === "IMG" ? media.complete && media.naturalWidth > 0 : media.readyState >= 2);
  x.fillStyle = `rgb(${BG.join(",")})`;
  x.fillRect(0, 0, W, H);
  if (!sc.revealAt) {
    // Until the result has loaded, the figure before it goes on.
    if (!ready) { ST.from = null; dots(ST, x, L, from, now); brackets(x, W, H, 0); ST.from = from; return; }
    sc.revealAt = now;
  }
  const p = motion() ? Math.min(1, (now - sc.revealAt) / REVEAL) : 1;
  if (p >= 1) { finish(ST); return; }
  const img = picture(ST, media, W, H);
  const { pitch, cols, rows, x0, y0 } = L, rMax = pitch * 0.42, rMin = pitch * 0.08, full = pitch * 0.74, far = Math.hypot(W, H) / 2;
  const t = timeOf(ST, from, now);
  const shade = SHADES.map(() => new Path2D()), shown = [0, 1, 2, 3].map(() => new Path2D());
  // The grid runs past the corners to the edges, so the swollen dots cover the whole frame.
  const i0 = -Math.ceil(x0 / pitch) - 1, i1 = Math.ceil((W - x0) / pitch) + 1, j0 = -Math.ceil(y0 / pitch) - 1, j1 = Math.ceil((H - y0) / pitch) + 1;
  for (let j = j0; j < j1; j++) for (let i = i0; i < i1; i++) {
    const px = x0 + i * pitch, py = y0 + j * pitch, inner = i >= 0 && i < cols && j >= 0 && j < rows;
    const dn = distPx(from.fig, from, L, px, py, t, i, j);
    const v = inner ? smooth(-pitch * 0.4, pitch * 1.1, -dn) : 0, r0 = inner ? Math.max(rMin, rMax * v) : 0;
    const g = smooth(0, 1, (p - 0.06 - 0.5 * Math.min(1, Math.max(0, dn) / far)) / 0.3), r = r0 + (full - r0) * g;
    if (r < 0.3) continue;
    const path = g > 0.02 && img ? shown[Math.min(3, Math.floor(g * 4))] : shade[Math.round(smooth(0, 0.35, v) * 8)];
    path.moveTo(px + r, py);
    path.arc(px, py, r, 0, TAU);
  }
  shade.forEach((q, k) => { x.fillStyle = SHADES[k]; x.fill(q); });
  if (img) shown.forEach((q, k) => {
    x.fillStyle = SHADES[8];
    x.fill(q);
    x.save(); x.clip(q); x.globalAlpha = (k + 1) / 4; x.drawImage(img, 0, 0, W, H); x.restore();
  });
  const settle = smooth(0.82, 1, p);
  if (img && settle > 0) { x.globalAlpha = settle; x.drawImage(img, 0, 0, W, H); x.globalAlpha = 1; }
  brackets(x, W, H, smooth(0.42, 0.86, p));
}

// The result, drawn once at the frame's size; a video is drawn as it plays.
function picture(ST, media, W, H) {
  if (!media) return null;
  if (media.tagName === "VIDEO") return media.readyState >= 2 ? media : ST.img;
  const k = W + "x" + H;
  if (ST.img?.k === k) return ST.img;
  if (!(media.complete && media.naturalWidth)) return ST.img;
  const d = Math.min(2, devicePixelRatio || 1), c = h("canvas", { width: Math.round(W * d), height: Math.round(H * d) });
  c.getContext("2d").drawImage(media, 0, 0, c.width, c.height);
  c.k = k;
  return (ST.img = c);
}

// The frame's corners as CSS draws them (10px in, 14% of the width up to 40px, 3px thick), opening out by o.
function brackets(x, W, H, o) {
  if (o >= 1) return;
  const arm = Math.min(40, Math.max(10, W * 0.14)), st = 3, b = 10 * (1 - o) - o * st;
  x.globalAlpha = 1 - o;
  x.fillStyle = BRACKET;
  for (const [X, Y, sx, sy] of [[b, b, 1, 1], [W - b, b, -1, 1], [b, H - b, 1, -1], [W - b, H - b, -1, -1]]) {
    x.fillRect(sx > 0 ? X : X - arm, sy > 0 ? Y : Y - st, arm, st);
    x.fillRect(sx > 0 ? X : X - st, sy > 0 ? Y : Y - arm, st, arm);
  }
  x.globalAlpha = 1;
}

function finish(ST) {
  ST.cv?.parentElement?.classList.remove("ht");
  ST.cv?.remove();
  ST.scene = ST.from = ST.img = null;
}
