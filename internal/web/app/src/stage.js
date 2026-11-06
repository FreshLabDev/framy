// The stage: the frame of what Create will make, in its shape, and once a
// run starts, that run: its files going up, the wait, a retry, the result,
// or why there is none, with the buttons that fit.
import { $, $$, h, ic, isPhone, docked, inFlow, keepFocus } from "./dom.js";
import { anim, rise, snap, EASE, motion, DUR } from "./motion.js";
import { t, fmtNum, fmtSecs } from "./i18n.js";
import { C, vname, famLogo, lg, ratioNum, ratioOfSize, PNAME, provMark, show, SICON } from "./catalog.js";
import { S, M, payOf, settings, sourceRatio, connection, keyToConnect, FR, MAXFR, scopeFr, plan } from "./state.js";
import { isFinal, outsOf, POLL_MS } from "./api.js";
import { failure, waiting, left, BILLING } from "./errors.js";
import { priceText, sumOf } from "./price.js";
import { setScope, addFrame, removeFrame, openTune, framesNote, miniShape } from "./composer.js";
import { openFull } from "./library.js";
import { halftone } from "./halftone.js";
import { grounding, groundRoom } from "./ground.js";
import { player, fmtClock } from "./player.js";
import { openPrefs } from "./prefs.js";
import { again, quiet, editPrompt, otherModel, cancel, byKey, keyFor, takePrice, drop, pickPart, copyPrompt, continueFrom, fromLastFrame, changePicture, animate, canShare, share, jobsOf, frameRun } from "./run.js";

// The shape of a run: its ratio, or the photo's for "auto", else square.
export function ratioOf(set, m) {
  if (set.size && /x/.test(set.size)) return ratioNum(ratioOfSize(set.size));
  if (set.aspect_ratio && set.aspect_ratio !== "auto") return ratioNum(set.aspect_ratio);
  return sourceRatio() || (m?.kind === "video" ? 16 / 9 : 1);
}
function ratioLabel(set) {
  if (set.size && /x/.test(set.size)) return ratioOfSize(set.size);
  if (set.aspect_ratio && set.aspect_ratio !== "auto") return set.aspect_ratio;
  return sourceRatio() ? t("val.photo") : t("val.auto");
}
// The room a frame has on the stage, with room under it for what a run says.
function space(under = 0) {
  const st = $("#stage"), phone = isPhone();
  const W = Math.max(160, phone ? innerWidth - 32 : st.clientWidth);
  // In a low window the stage is scrolled to when a run starts, past the header and the composer, and has the window.
  const dock = docked() ? $("#composer").offsetHeight : 0, top = inFlow() ? 0 : $("#top")?.offsetHeight || 0;
  // A short phone with the files row in the dock leaves little; the frames shrink rather than go under it.
  const H = Math.max(phone ? 96 : 160, (phone ? innerHeight - dock - top - 92 : innerHeight - top - 120) - under);
  return { W, H };
}
// The largest box of ratio r that fits the stage.
function fit(r, under = 0) {
  const { W, H } = space(under);
  let w = W, hh = W / r;
  if (hh > H) { hh = H; w = H * r; }
  return { w: Math.round(w), h: Math.round(hh) };
}
/* Frames of several shapes stand in rows of one height, so the same shape is
   always the same size. Of the ways to split them into rows, the one with the
   most room for the frames wins, a little for the full width and fewer rows.
   pos is where each stands in the box of w by h. */
const GAP = 2;
export function board(rs, under = 0) {
  const { W, H } = space(under), n = rs.length;
  let best = null;
  for (let mask = (1 << (n - 1)) - 1; mask >= 0; mask--) {
    const rows = [];
    let cur = [0];
    for (let i = 1; i < n; i++) { if (mask & (1 << (i - 1))) { rows.push(cur); cur = [i]; } else cur.push(i); }
    rows.push(cur);
    const sum = (r) => r.reduce((a, i) => a + rs[i], 0);
    const h0 = Math.min(...rows.map((r) => (W - GAP * (r.length - 1)) / sum(r)));
    const hh = h0 * Math.min(1, (H - GAP * (rows.length - 1)) / (h0 * rows.length));
    const wide = Math.max(...rows.map((r) => sum(r) * hh + GAP * (r.length - 1))) / W;
    const score = rows.flat().reduce((a, i) => a + Math.sqrt(rs[i]) * hh, 0) * wide ** 0.2;
    if (!best || score > best.score * 1.001 || (score > best.score * 0.999 && rows.length < best.rows.length)) best = { score, rows, hh };
  }
  const hh = Math.floor(best.hh), widths = best.rows.map((r) => r.reduce((a, i) => a + Math.floor(rs[i] * hh), 0) + GAP * (r.length - 1));
  const w = Math.max(...widths), pos = [];
  best.rows.forEach((r, k) => {
    let x = (w - widths[k]) / 2;
    for (const i of r) { const cw = Math.floor(rs[i] * hh); pos[i] = { x: Math.round(x), y: k * (hh + GAP), w: cw, h: hh }; x += cw + GAP; }
  });
  return { w, h: best.rows.length * (hh + GAP) - GAP, pos };
}
// The cells of a board stand where its layout puts them.
function lay(box, L) {
  box.style.width = L.w + "px";
  box.style.height = L.h + "px";
  [...box.querySelectorAll(":scope > .cell")].forEach((c, i) => {
    const p = L.pos[i];
    if (!p) return;
    Object.assign(c.style, { left: p.x + "px", top: p.y + "px", width: p.w + "px", height: p.h + "px" });
    c.classList.toggle("narrow", p.w < 150);
  });
}

// A frame that comes grows out of its place; one removed shrinks away.
// In the phone's strip the roll is the motion: a card there only shows up, since a
// moving or growing card would make the strip snap to the one beside it.
const CELLS = { box: true, dur: DUR.l,
  enter: (el, i) => el.classList.contains("cell") && anim(el, el.parentElement?.classList.contains("strip") ? [{ opacity: 0 }, { opacity: 1 }] : [{ opacity: 0, transform: "scale(.9)" }, { opacity: 1, transform: "none" }],
    { duration: DUR.m, delay: 60 + i * 40, easing: EASE, fill: "backwards" }),
  leave: (g) => (g.classList.contains("cell") ? anim(g, [{ opacity: 1, transform: "none" }, { opacity: 0, transform: "scale(.9)" }], { duration: DUR.s, easing: EASE, fill: "forwards" }) : null) };
export function stageFrom() { return snap($("#stage"), CELLS); }

// A run has ended: refused, stopped before the job, or every job of it final.
export const runOver = (run) => !!run && (run.phase === "refused" || run.phase === "cancelled" || (run.phase === "job" && jobsOf(run).every((j) => isFinal(j.status))));

/* A result on the stage stays the same element while the stage is drawn
   again, as it is for a new state of the account (the free counts after a
   run, the settings opened, the page back from another tab): a video goes on
   where it was, paused, sought or with its sound, and a picture is not loaded
   and shown again. Only what the stage draws now is kept. */
let kept = new Map(), keeping = new Map();
const keepMedia = (key, make) => { const el = kept.get(key) || make(); keeping.set(key, el); return el; };

export function renderStage() {
  keeping = new Map();
  try { keepFocus(drawStage); } finally {
    // A video the stage no longer draws is no longer watched for the screen.
    for (const [k, el] of kept) if (!keeping.has(k) && el.tagName === "VIDEO") onScreen?.unobserve(el);
    kept = keeping;
  }
}
function drawStage() {
  const st = $("#stage");
  if (!st) return;
  const run = S.run;
  if (run && S.showJob) { if (run.multi) renderPress(st, run); else renderRun(st, run); ticking(); return; }
  ticking();
  const m = M();
  if (!m) return;
  const P = plan(), phone = isPhone(), many = P.length > 1;
  // Over the frames: the frame being set up, its cross back to all, and the button for one more.
  const full = P.length >= MAXFR[S.kind], fr = scopeFr(), fi = fr ? FR().indexOf(fr) : -1;
  const add = h("button", { type: "button", class: "link addf", "data-k": "pv-add", disabled: full, title: full ? t("fr.max", { n: MAXFR[S.kind] }) : t("fr.add_sub"), onclick: () => addFrame() }, ic("plus"), t("fr.add_short"));
  const tip = fr && !phone ? h("span", { class: "scopetip", "data-k": "pv-tip" }, miniShape(ratioOf(P[fi].set, P[fi].m), 14), h("span", { class: "t" }, t("fr.tune", { n: fi + 1 })),
    h("button", { type: "button", "aria-label": t("fr.to_all"), title: t("fr.to_all"), onclick: () => setScope(null) }, ic("close"))) : null;
  // Under the frames on a phone: the dots of a strip, or the way back to a run; one empty frame needs no room there.
  const head = h("div", { class: "shead", "data-k": "pv-head" }, tip, add), under = phone ? (many || run ? 48 : 0) : 40;
  let main;
  if (!many) {
    const pay = payOf(m), set = settings(m, pay), r = ratioOf(set, m), b = fit(r, under), f = C.famOf[m.id];
    main = h("div", { class: "ghost", "data-k": "pv", style: { width: b.w + "px", height: b.h + "px" } },
      h("i"), h("i"), h("i"), h("i"),
      h("div", { class: "c" }, ic(m.kind), h("b", {}, ratioLabel(set)),
        h("small", {}, h("span", { class: "it" }, lg(famLogo(f), f.maker, "s"), vname(m)))));
    main._r = r; main._under = under;
  } else main = phone ? stripOf(P) : boardOf(P, under);
  const note = many && !phone ? framesNote(P, false) : null;
  const kids = [head, h("div", { class: "preview", "data-k": "pvbox" }, main, phone && many ? dotsOf(main, P.length) : null, note)];
  if (run) kids.push(h("button", { type: "button", class: "link back", "data-k": "pv-back", onclick: () => { S.showJob = true; renderStage(); } },
    ic(runOver(run) ? "image" : "clock"), t(runOver(run) ? "stage.last" : "stage.current")));
  // A strip drawn anew stands at the card the old one stood at.
  const s0 = $("#stage .strip"), at = s0 ? s0._at ?? stripAt(s0) : 0;
  st.replaceChildren(...kids.filter(Boolean));
  const s1 = $("#stage .strip");
  if (s1 && at) { s1._at = at; toCard(s1, at); }
}

/* The frames before a press: each in its shape, saying what it does that the others do not. */
// The values that differ between the frames: once one frame differs, every frame shows its own.
function diffDims(P) {
  const differs = (f) => new Set(P.map(f)).size > 1, dims = [];
  if (differs((it) => it.m.id)) dims.push("model");
  if (differs((it) => it.pay)) dims.push("pay");
  const names = new Set(P.flatMap((it) => (it.pay === "free" ? [] : Object.keys(it.set))));
  for (const k of names) if (!["aspect_ratio", "size", "frame_images"].includes(k) && differs((it) => (it.pay === "free" || !(k in it.set) ? "" : String(it.set[k])))) dims.push(k);
  return dims;
}
function ghostCell(it, dims) {
  const { fr, m, pay, set, i } = it, on = S.scope[S.kind] === fr.id, own = fr.own, f = C.famOf[m.id];
  const chip = (mine, icon, text) => h("span", { class: "chip" + (mine ? " own" : ""), title: text }, icon, h("span", {}, text));
  const chips = dims.map((k) => (k === "model" ? chip(!!own.model, lg(famLogo(f), f.maker, "s"), vname(m))
    : k === "pay" ? chip(!!own.pay, pay === "free" ? ic("ticket") : provMark(pay), pay === "free" ? t("pay.free") : PNAME[pay] || pay)
    : pay === "free" || !(k in set) ? null : chip(k in own.wanted, ic(SICON[k] || "sliders"), show(k, set[k])))).filter(Boolean);
  if (it.short) chips.push(h("span", { class: "chip warn" }, ic("ticket"), h("span", {}, t("fr.short"))));
  // A press sets up that frame: on a phone in the sheet, on a computer in the form beside it.
  const pick = () => (isPhone() ? openTune(fr.id) : setScope(on ? null : fr.id));
  /* The frame holds two buttons side by side, one that picks it and one that
     removes it, and is no button itself: a button inside a button is lost to a
     screen reader. The first fills the frame under what it says, and a press
     anywhere on the frame but the cross picks it. */
  const said = chips.length ? "pvo-" + fr.id : null;
  return h("div", { class: "ghost cell" + (on ? " on" : ""), "data-k": "pv-" + fr.id, onclick: pick },
    h("button", { type: "button", class: "fhit", "aria-pressed": String(on), "aria-label": t("fr.n", { n: i + 1 }), "aria-describedby": said }),
    h("i"), h("i"), h("i"), h("i"),
    h("div", { class: "c" }, h("b", {}, ratioLabel(set)), h("small", {}, t("fr.n", { n: i + 1 })), said ? h("span", { class: "fown", id: said }, chips) : null),
    h("button", { type: "button", class: "frm", "aria-label": t("fr.remove_n", { n: i + 1 }), title: t("fr.remove"), onclick: (e) => { e.stopPropagation(); removeFrame(fr); } }, ic("close")));
}
function boardOf(P, under) {
  const dims = diffDims(P), rs = P.map((it) => ratioOf(it.set, it.m));
  const box = h("div", { class: "board", "data-k": "pv" }, P.map((it) => ghostCell(it, dims)));
  box._fit = () => lay(box, board(rs, under));
  box._fit();
  return box;
}
/* The phone: the frames stand in a strip, each nearly the strip's width so
   the next one peeks in; the button for one more stays over it. */
function stripOf(P) {
  const dims = diffDims(P), rs = P.map((it) => ratioOf(it.set, it.m));
  const strip = h("div", { class: "strip", "data-k": "pv" }, P.map((it) => { const c = ghostCell(it, dims); c.dataset.km = "stay"; return c; }));
  strip._fit = () => fitStrip(strip, rs);
  strip._fit();
  // Where it stands once a swipe or a roll has settled: the card it stays at when the cards change size.
  let rest = 0;
  strip.addEventListener("scroll", () => { clearTimeout(rest); rest = setTimeout(() => (strip._at = stripAt(strip)), 160); }, { passive: true });
  return strip;
}
// The card the strip stands at: the last one once it can roll no further.
// Right to left, the cards go leftwards from the first and the strip rolls
// to negative scrollLeft: a card's distance from the first and the roll have
// the same sign either way.
function stripAt(s) {
  const cs = [...s.querySelectorAll(":scope > .cell")], x = s.scrollLeft;
  // On a short phone the cards are small and several stand whole: the one the strip was sent to stays the one.
  const r = s.getBoundingClientRect(), b = cs[s._at]?.getBoundingClientRect();
  if (b && b.left >= r.left - 1 && b.right <= r.right + 1) return s._at;
  if (x !== 0 && Math.abs(x) >= s.scrollWidth - s.clientWidth - 2) return cs.length - 1;
  let k = 0, d = Infinity;
  cs.forEach((c, i) => { const e = Math.abs(c.offsetLeft - cs[0].offsetLeft - x); if (e < d) { d = e; k = i; } });
  return k;
}
function toCard(s, at, smooth = false) {
  const cs = [...s.querySelectorAll(":scope > .cell")], c = cs[at];
  if (!c) return;
  s.scrollTo({ left: c.offsetLeft - cs[0].offsetLeft, behavior: smooth && motion() ? "smooth" : "auto" });
}
function fitStrip(strip, rs) {
  const { W, H } = space(48), cw = Math.round(W * 0.86), cards = [...strip.querySelectorAll(":scope > .cell")];
  cards.forEach((c, i) => {
    let w = cw, hh = w / rs[i];
    if (hh > H) { hh = H; w = hh * rs[i]; }
    Object.assign(c.style, { width: Math.round(w) + "px", height: Math.round(hh) + "px" });
    c.classList.toggle("narrow", w < 150);
  });
  // Cards of another size would leave the strip between two of them.
  if (strip.isConnected && strip._at) toCard(strip, strip._at);
}
// The dots under the strip follow the frame at its start.
function dotsOf(strip, n) {
  const dots = h("div", { class: "dots", "aria-hidden": "true", "data-k": "pv-dots" }, Array.from({ length: n }, () => h("i")));
  const upd = () => {
    const k = stripAt(strip);
    [...dots.children].forEach((e, i) => e.classList.toggle("on", i === k));
  };
  strip.addEventListener("scroll", upd, { passive: true });
  requestAnimationFrame(upd);
  return dots;
}
// The strip rolls to the frame being set up, as the sheet goes from frame to frame.
export function syncStrip(id) {
  const s = $("#stage .strip"), el = id != null && s?.querySelector(`[data-k="pv-${id}"]`);
  if (!el) return;
  s._at = [...s.querySelectorAll(":scope > .cell")].indexOf(el);
  // A frame later: a strip just drawn is put back where it stood first, and that jump would stop the roll.
  requestAnimationFrame(() => toCard(s, s._at, true));
}

function runRatio(run, res = outsOf(run.job)[0]) {
  if (res?.width && res?.height) return res.width / res.height;
  const m = C.byId[run.model], set = run.settings || {};
  if (set.size && /x/.test(set.size)) return ratioNum(ratioOfSize(set.size));
  if (set.aspect_ratio && set.aspect_ratio !== "auto") return ratioNum(set.aspect_ratio);
  const f = run.files[0];
  return f?.w && f?.h ? f.w / f.h : m?.kind === "video" ? 16 / 9 : 1;
}

// The tag of a run read every few seconds, since its stream was refused.
const pollTag = () => { const every = fmtSecs(POLL_MS / 1000); return h("span", { class: "jtag", title: t("net.poll_tip", { every }) }, t("net.poll", { every })); };
// What a run that goes on says while the page cannot follow it: no connection, nobody signed
// in, or the Mini App's session over.
const NETLINE = { lost: "net.lost_line", signin: "net.signin_line", reopen: "net.reopen_line" };

// A line that counts down: its text is asked again every second.
const live = (text, cls = "") => { const el = h("small", { class: ("tk " + cls).trim() }, text()); el._text = text; return el; };
// The wait: how long since the press, or since the job was made, and how long
// the model usually takes, as framy measured its own runs. Never a percentage:
// no provider reports one.
const typicalOf = (j) => C.byId[j.model]?.offers.find((o) => o.id === j.offer)?.typical_seconds || 0;
function tookEl(run) {
  const since = run.t0 || Date.parse(run.job.created_at) || Date.now(), u = typicalOf(run.job);
  return live(() => { const s = fmtClock((Date.now() - since) / 1000); return u > 0 ? t("stage.usually", { t: s, u: fmtClock(u) }) : s; }, "took");
}

// What a run being made says in place of Cancel. A video takes minutes, so it
// also says the person can leave and where the video will come: the DM for a
// run made there, or one made on the site while its results go to the chat
// (Settings); the library for the rest of the site's and the Mini App's. A
// run of a group or of guest mode comes where it was asked for.
function busyEl(run, j) {
  let k = run.kind === "video" ? "cancel.running_video" : "cancel.running";
  if (run.kind === "video") {
    const s = j?.surface;
    if (s === "dm" || (s === "site" && S.me?.deliver_to_telegram !== false)) k = "cancel.running_video_chat";
    else if (s === "site" || s === "mini_app") k = "cancel.running_video_library";
  }
  return h("span", { class: "busy" }, ic("info"), t(k));
}

// What the stage says about run: the frame's status, the reason under it and
// the buttons. The library's viewer shows a run with it too: o.res is which
// result, o.again makes the button that runs it again.
export function view(run, o = {}) {
  const j = run.job;
  // ht is the figure of the halftone in the frame (halftone.js).
  const spin = (label, sub, ht) => ({ status: h("div", { class: "c run" }, h("b", {}, label), sub), ht });
  const stop = h("button", { type: "button", onclick: () => cancel(run) }, ic("stop"), t("act.cancel"));
  if (run.phase === "prep") {
    const s = run.step;
    return { ...spin(t("stage.starting"), s ? live(s.text) : null, s?.fig ? { fig: s.fig, until: s.until, prog: s.prog } : { fig: "start" }), acts: run.sending ? [] : [stop] };
  }
  if (run.phase === "refused") {
    const w = run.why;
    return { fail: true, status: failEl(w.label), why: [w.text], acts: refusalActs(run, w.acts), ht: { fig: w.acts.includes("cut") ? "cut" : REFFIG[w.code] || "bang", still: true } };
  }
  if (run.phase === "cancelled") {
    const free = run.pay === "free";
    return { fail: true, status: failEl(t("stage.cancelled")), why: [free ? t("fail.free_back") : ""], zero: !free, acts: againActs(run), ht: { fig: "line", still: true } };
  }
  const st = j.status;
  if (st === "queued") {
    const w = waiting(j);
    if (w) return { ...spin(t("stage.retry"), live(() => w.line(left(w.at))), { fig: "wait", until: w.at }), acts: [stop] };
    return { ...spin(t("stage.queued"), tookEl(run), { fig: "queue" }), acts: [stop] };
  }
  if (st === "running") {
    return { ...spin(t("stage.running"), tookEl(run), { fig: "loop" }), acts: [busyEl(run, j)] };
  }
  const res = outsOf(j)[o.res || 0];
  if (st === "done" && res) return { done: true, res, acts: doneActs(run, j, res, o) };
  const f = failure(j);
  return { fail: true, status: failEl(f.label, f.said), why: [f.text, f.note], zero: f.zero, acts: failActs(run, f, o), ht: { fig: FAILFIG[f.cls] || "cross", still: true } };
}
// The provider's own words stand small under the label, two lines of them; a press shows the rest.
export const failEl = (label, said) => h("div", { class: "c fail" }, h("b", {}, label),
  said ? h("small", { class: "said", title: said, onclick: (e) => { e.currentTarget.classList.toggle("open"); caption(e.currentTarget.parentElement._box); } }, said) : null);
// The words of a frame stand under its figure, in it, while the dots keep at least half of the frame
// above them (halftone.js leaves 22 px over the dots and 4 px under them). A frame too small for both,
// as on a short phone or one on its side, has its words under it instead, clear of the dots.
function caption(box) {
  const c = box?._c;
  if (!c || !box.isConnected) return;
  if (c.parentElement !== box) { c.classList.remove("under"); box.append(c); }
  if (c.offsetHeight + 26 <= box.clientHeight / 2) return;
  c.classList.add("under");
  box.after(c);
}
// A price of 0 in the person's currency, from a label of the run's own price.
const zeroOf = (label) => (label || "$0").replace(/\d[\d.,]*/, "0");
// The figure of each reason a run gave nothing; the rest show a cross.
const FAILFIG = { moderation: "ban", refused: "ban", faces: "ban", sound: "ban", region: "ban", money: "coin", key: "key", timeout: "clock", draft_expired: "clock", rate_limited: "hush", unavailable: "hush", interrupted: "link", cancelled: "line" };
const REFFIG = { price_changed: "coin", not_free: "coin", free_limit: "clock", free_closed: "clock", no_connection: "key", connection_invalid: "key", key_in_prompt: "key", sign_in: "key",
  init_data_expired: "clock", init_data_invalid: "key",
  network: "link", shutting_down: "link", http_429: "hush", busy: "hush", wrong_inputs: "bang", unknown_input: "bang", unknown_offer: "bang", cannot_continue: "bang",
  not_a_video: "cross", unsupported_type: "cross", unreadable: "cross", frame_too_large: "cross", too_large: "cross", picture_too_large: "cross", storage_unavailable: "cross" };

// The actions of a run in a row. The square ones that stand together go as one group, which a phone
// keeps on one line, beside the buttons with words or under them; a square on its own goes with the
// button before it, so it never stands alone on a line. Where Again says its price (the viewer), it is
// a button with words, not a square.
export function actRow(items, priced = false) {
  const out = [];
  let g = null;
  for (const b of items) {
    if (!b) continue;
    if (!(b.classList.contains("sq") || (!priced && b.classList.contains("sq2")))) { g = null; out.push(b); continue; }
    if (!g) {
      const prev = out.at(-1), one = prev?.matches("button, a") ? h("span", { class: "sqs pair" }, prev) : null;
      if (one) out.pop();
      out.push((g = one || h("span", { class: "sqs" })));
    } else if (g.classList.contains("pair")) {
      // A second square: the button with words stands on its own again, and the squares go together.
      out.splice(out.length - 1, 1, g.firstElementChild, (g = h("span", { class: "sqs" }, g.lastElementChild)));
    }
    g.append(b);
  }
  return out;
}
// The buttons of a run, the first of them the one that helps most.
// A square one shows only its icon on a phone, so each says its name.
export const B =(icon, label, fn, cls = "") => h("button", { type: "button", class: cls || null, "aria-label": cls.startsWith("sq") ? label : null, onclick: fn }, ic(icon), h("span", { class: "t" }, label));
const againActs = (run) => [B("again", t("act.retry"), () => again(run), "primary"), B("edit", t("act.edit"), () => editPrompt(run))];
// The run again on the person's key; with no key that works, the way to
// connect the one it would go on.
function keyBtn(run) {
  const p = keyFor(run), m = C.byId[run.model];
  if (p) return B("key", t("act.bykey", { provider: PNAME[p] }), () => byKey(run), "primary");
  return m ? keyFix(keyToConnect(m), "primary") : null;
}
const againBtn = (run, o, label, cls) => (o.again ? o.again(label, cls) : B("again", label, () => again(run), cls));
function failActs(run, f, o = {}) {
  const out = [];
  for (const a of f.acts) {
    if (a === "retry") out.push(againBtn(run, o, t("act.retry"), out.length ? "" : "primary"));
    else if (a === "edit") out.push(B("edit", t("act.edit"), () => editPrompt(run), out.length ? "" : "primary"));
    else if (a === "bykey") out.push(keyBtn(run));
    else if (a === "topup") out.push(topup(f.provider));
    else if (a === "rekey") out.push(keyFix(f.provider, out.length ? "" : "primary"));
    else if (a === "model") out.push(B("grid", t("act.other_model"), () => otherModel(run), out.length ? "" : "primary"));
    else if (a === "quiet") out.push(B("mute", t("act.no_sound"), () => quiet(run), out.length ? "" : "primary"));
  }
  if (!f.acts.includes("edit")) out.push(B("edit", t("act.edit"), () => editPrompt(run)));
  return out;
}
// To the settings, at the key the run needs: connect it, or replace one that stopped working.
const keyFix = (p, cls) => B("key", t(connection(p) ? "go.rekey" : "go.connect", { provider: PNAME[p] || p }), () => openPrefs(p), cls);
function topup(p) {
  const url = BILLING[p];
  if (!url) return null;
  if (S.tg?.openLink) return B("coin", t("act.topup", { provider: PNAME[p] || p }), () => S.tg.openLink(url), "primary");
  return h("a", { class: "primary", href: url, target: "_blank", rel: "noopener" }, ic("coin"), h("span", { class: "t" }, t("act.topup", { provider: PNAME[p] || p })));
}
function refusalActs(run, acts) {
  const out = [];
  for (const a of acts) {
    const first = out.length ? "" : "primary";
    if (a === "takeprice") out.push(B("spark", t("act.takeprice", { p: priceText(run.why.estimate) }), () => takePrice(run), first));
    else if (a === "drop") out.push(B("close", t("cancel"), () => drop(), first));
    else if (a === "bykey") out.push(keyBtn(run));
    else if (a === "cut") out.push(B("film", t("act.pick_part"), () => pickPart(run), first));
    else if (a === "lastframe") out.push(B("next", t("act.lastframe"), () => fromLastFrame(run.cont), first));
    else if (a === "again") out.push(B("again", t("act.retry"), () => again(run), first));
    else if (a === "key") out.push(keyFix(run.why.provider, first));
  }
  if (!acts.includes("drop")) out.push(B("edit", t("act.edit"), () => editPrompt(run)));
  return out.filter(Boolean);
}
function doneActs(run, j, res, o = {}) {
  const vid = res.content_type?.startsWith("video/");
  let sb = null;
  if (canShare(res)) { sb = B("send", t("act.share"), () => share(res, sb), "sq"); sb.title = t("act.share_tip"); }
  // The joined clip shows; the part this run made saves on its own too.
  const part = res === j.joined ? j.results?.[0] : null;
  return [res.url ? download(j, res, o.res || 0) : null,
    part?.url ? download(j, part, 0, { label: t("act.part"), cls: "sq" }) : null,
    againBtn(run, o, t("act.again"), "sq2"),
    B("edit", t("act.edit"), () => editPrompt(run), "sq"),
    // A picture goes on as the photo of the next run or the first frame of a video; a clip goes on from its end.
    !vid && res.url ? B("image", t("act.change"), () => changePicture(run, res), "sq") : null,
    !vid && res.url ? B("video", t("act.animate"), () => animate(run, res), "sq") : null,
    vid && res.url ? B("next", t("act.continue"), () => continueFrom(run, res), "sq") : null, sb];
}

/* A press of several frames: each result stands in place of its frame, in
   its own shape. A frame that gave nothing leaves, and the note under the
   frames says which and why; the others go on. */
function cellsOf(run) {
  return run.frames.map((fm, i) => {
    const sub = frameRun(run, i), v = view(sub, { res: sub.k });
    return { i, fm, sub, v, r: runRatio(sub, v.done ? v.res : null) };
  });
}
/* A video of a press plays in its frame without sound, over and over, while
   the frame is on the screen; its button stops it and starts it again. With
   reduced motion it waits for the button. One the person stopped stays
   stopped, wherever the page goes. */
const onScreen = typeof IntersectionObserver === "function" ? new IntersectionObserver((es) => {
  for (const e of es) { const v = e.target; if (!e.isIntersecting) v.pause(); else if (!v._held) v.play().catch(() => {}); }
}) : null;
function cellVideo(res) {
  const v = h("video", { src: res.url, poster: res.thumb?.url || null, loop: true, playsinline: true, autoplay: motion(), preload: "metadata" });
  v.muted = true;
  v._held = !motion();
  onScreen?.observe(v);
  return v;
}
function playBtn(v) {
  const sync = () => {
    const on = !v.paused, label = t(on ? "player.pause" : "player.play");
    b.replaceChildren(ic(on ? "pause" : "play")); b.setAttribute("aria-label", label); b.title = label;
    b.classList.toggle("still", !on);
  };
  const b = h("button", { type: "button", class: "tpp", onclick: (e) => {
    e.stopPropagation();
    v._held = !v.paused;
    if (v._held) v.pause(); else v.play().catch(() => {});
    sync();
  } });
  v.addEventListener("play", sync); v.addEventListener("pause", sync);
  sync();
  return b;
}
function runCell(run, c, mixed, items) {
  const { v, sub, i } = c, j = sub.job, going = sub.phase === "job" && !isFinal(j.status), id = `p${run.n}-${i}`;
  let media = null, pp = null;
  if (v.done) {
    const res = v.res, vid = res.content_type?.startsWith("video/"), key = `p${run.n}-${i}-${res.id}`;
    media = keepMedia(key, () => (vid ? cellVideo(res) : h("img", { src: res.url, alt: run.prompt || "", decoding: "async" })));
    if (vid) pp = keepMedia(key + "-pp", () => playBtn(media));
  }
  if (going && NETLINE[run.net] && v.status) {
    const lost = h("small", { class: "lost" }, t(NETLINE[run.net])), sub2 = v.status.querySelector("small");
    if (sub2) sub2.replaceWith(lost); else v.status.append(lost);
  }
  const at = items.findIndex((x) => x.i === i), open = () => at >= 0 && openFull(at, items);
  const dl = v.done && v.res.url ? download(j, v.res, sub.k) : null;
  if (dl) { dl.className = "sq tdl"; dl.setAttribute("aria-label", t("stage.download")); dl.title = t("stage.download"); dl.addEventListener("click", (e) => e.stopPropagation()); }
  const f = C.famOf[sub.model], m = C.byId[sub.model];
  // As a frame before the press: the button that opens the result lies over it, beside Download and Play, not around them.
  const cell = h("div", { class: "jbox cell" + (v.done ? " done" : ""), "data-k": "pc-" + i, onclick: v.done ? open : null },
    v.done ? media : [h("i"), h("i"), h("i"), h("i")], v.done ? null : v.status || null,
    v.done ? h("button", { type: "button", class: "fhit", "aria-label": t("view.result", { n: fmtNum(i + 1) }) }) : null,
    h("span", { class: "num" }, String(i + 1)), mixed && f && m ? h("span", { class: "chip tm" }, lg(famLogo(f), f.maker, "s"), h("span", {}, vname(m))) : null, dl, pp);
  if (v.ht) halftone(cell, v.ht.fig, { ...v.ht, n: run.n, id, lost: going && !!NETLINE[run.net] });
  else if (v.done) halftone(cell, "reveal", { n: run.n, id });
  return cell;
}
// What came of the press, in order, for the whole screen and Download all.
const doneItems = (cells) => cells.filter((c) => c.v.done && c.v.res.url).map((c) => ({ i: c.i, j: c.sub.job, res: c.v.res, run: c.sub }));
function renderPress(st, run) {
  // Refused before any job, or stopped while the files went up: one frame says it for all.
  if (run.phase === "refused" || run.phase === "cancelled") return renderRun(st, run);
  const cells = cellsOf(run), live = cells.filter((c) => !c.v.fail), failed = cells.filter((c) => c.v.fail);
  const models = [...new Set(run.frames.map((fm) => fm.model))], mixed = models.length > 1, items = doneItems(cells);
  // Nothing came: the first frame's reason stands for the press, and Again runs all of it.
  if (!live.length) return renderRun(st, failed[0].sub);
  // Google's suggestions and the sources of each grounded job go under the results and their
  // buttons, once a job (frames alike go as one), with the numbers of its frames when the press
  // has others.
  const gj = new Map();
  for (const c of live) { const j = c.sub.job; if (j?.grounding) { if (!gj.has(j.id)) gj.set(j.id, { j, ns: [] }); gj.get(j.id).ns.push(fmtNum(c.i + 1)); } }
  const gr = [...gj.values()].map(({ j, ns }) => grounding(j, ns.length < live.length ? ns.join(", ") : ""));
  const under = UNDER + groundRoom([...gj.values()].map((x) => x.j));
  const rs = live.map((c) => c.r), box = h("div", { class: "board run", "data-k": "pv" }, live.map((c) => runCell(run, c, mixed, items)));
  const size = live.length > 1 ? (u) => { const L = board(rs, u); lay(box, L); return L.h; }
    : (u) => { const x = fit(rs[0], u); lay(box, { w: x.w, h: x.h, pos: [{ x: 0, y: 0, w: x.w, h: x.h }] }); return x.h; };
  size(under);
  const js = jobsOf(run), going = run.phase === "prep" || js.some((j) => !isFinal(j.status)), over = runOver(run);
  // Once it is over, what each paid job cost: its bill, or nothing when it failed before any charge.
  const paidJobs = js.filter((j) => !j.free), zero = (j) => cells.some((c) => c.sub.job === j && c.v.zero);
  const parts = over ? paidJobs.map((j) => (j.cost?.label ? { ...j.cost, kind: "exact" } : zero(j) ? { kind: "exact", usd: 0, max_usd: 0, amount: 0, max_amount: 0, currency: j.estimate?.currency || "USD", label: zeroOf(j.estimate?.label) } : null)) : [];
  const cost = parts.length && parts.every(Boolean) ? sumOf(parts).label
    : paidJobs.length || run.frames.some((fm) => fm.pay !== "free") ? run.shownLabel : t("bar.free");
  const line = h("p", { class: "jline", "data-k": "j-line" }, [models.map((id) => (C.byId[id] ? vname(C.byId[id]) : "")).filter(Boolean).join(", "), cost].filter(Boolean).join(" · "),
    going && run.net === "poll" ? pollTag() : null);
  const why = failed.length ? h("div", { class: "jwhy", "data-k": "j-why" }, failed.map((c) => h("p", {}, t("fr.failed", { n: c.i + 1, why: c.v.status?.querySelector("b")?.textContent || "" })))) : null;
  const queued = run.phase === "prep" ? !run.sending : js.some((j) => j.status === "queued");
  const acts = going
    ? [queued ? B("stop", t("act.cancel"), () => cancel(run)) : busyEl(run, run.job)]
    : [items.length ? allDown(items) : null, B("again", t("act.again"), () => again(run), "sq2"), B("edit", t("act.edit"), () => editPrompt(run), "sq"),
      h("button", { type: "button", class: "sq", "aria-label": t("stage.next"), title: t("stage.next"), onclick: () => drop() }, ic("plus"), h("span", { class: "t" }, t("stage.next")))];
  const prompt = run.prompt ? h("p", { class: "jprompt", "data-k": "j-prompt", hidden: samePrompt(run) }, h("span", { class: "tx" }, run.prompt),
    h("button", { type: "button", class: "cp", "aria-label": t("act.copy"), title: t("act.copy"), onclick: () => copyPrompt(run.prompt) }, ic("copy"))) : null;
  st.replaceChildren(h("div", { class: "preview", "data-k": "pvbox" }, box), h("div", { class: "jfoot", "data-k": "j-foot" }, line, why, h("div", { class: "jacts acts", "data-k": "j-acts" }, actRow(acts)), gr, prompt));
  box._fit = () => roomUnder(st, size, under);
  box._fit();
}
// Every result of the press saved, one after another: a browser asks once to allow them, Telegram asks for each.
// They are the cells right above it, so its label gives no count.
function allDown(items) {
  return B("down", t("act.download_all"), () => {
    const links = items.map((it) => download(it.j, it.res, outsOf(it.j).indexOf(it.res))), tg = S.tg;
    if (links[0]?.tagName === "BUTTON" && tg?.downloadFile) {
      const one = (k) => { if (k < items.length) tg.downloadFile({ url: items[k].res.download, file_name: links[k]._name }, () => one(k + 1)); };
      return one(0);
    }
    links.forEach((a, k) => setTimeout(() => a.click(), k * 250));
  }, "primary");
}

/* A run's frame keeps one size for its shape in every state: while it is made,
   once it is done and once it failed, the same room is left under it, and
   nothing on the stage moves but what changed. A failure's reason with the
   buttons that help can need more: then, and only then, the frame gives up
   the room that is missing, so they are seen without scrolling, and never
   behind the dock on a phone. The prompt under them is not counted: it shows
   while the composer holds another, and the frame does not shrink as the
   person types; Google's sources under a grounded result are (under).
   size(under) lays the frame out and gives its height. */
const UNDER = 70;
// On a phone a result's row is Download and a group of squares (a picture's has five). When the
// word of Download pushes the squares to a line of their own, Download goes square too (app.css,
// .tight): the row stays one line over the dock, so the frame keeps the size it was made at.
// Whether the word fits depends on the language and the width, so it is measured, not guessed.
function tighten(st) {
  const acts = st.querySelector(":scope > .jfoot > .jacts");
  if (!acts) return;
  acts.classList.remove("tight");
  const first = acts.firstElementChild, last = acts.lastElementChild;
  if (!isPhone() || first === last || !first?.classList.contains("primary") || !last?.classList.contains("sqs")) return;
  if (last.offsetTop > first.offsetTop + 4) acts.classList.add("tight");
}
function roomUnder(st, size, under = UNDER) {
  const h0 = size(under), acts = st.querySelector(":scope > .jfoot > .jacts");
  // A stage out of sight has nothing to measure; it is drawn again when it comes back.
  if (!acts?.getClientRects().length) return;
  // Where the stage stands with the page at its top (on a computer it sticks once scrolled).
  const phone = docked(), top0 = (phone ? st : st.parentElement).getBoundingClientRect().top + scrollY;
  const edge = innerHeight - (phone ? $("#composer").offsetHeight : 0) - 24;
  const over = Math.ceil(acts.getBoundingClientRect().bottom - st.getBoundingClientRect().top + top0 - edge);
  if (over > 0) size(under + space(under).H - h0 + over);
}

function renderRun(st, run) {
  const j = run.job, v = view(run), r = runRatio(run), under = UNDER + groundRoom([j]), b = fit(r, under), m = C.byId[run.model];
  let media = null;
  if (v.done) {
    const res = v.res;
    media = keepMedia(`r${run.n}-${res.id}`, () => {
      const el = res.content_type?.startsWith("video/") ? player(res) : h("img", { src: res.url, alt: run.prompt || "", decoding: "async" });
      const pic = el.tagName === "IMG" ? el : el.querySelector("video");
      pic.addEventListener(el === pic ? "load" : "loadeddata", () => anim(pic, [{ opacity: 0 }, { opacity: 1 }], { duration: DUR.l }), { once: true });
      return el;
    });
  }
  // While the page cannot follow the run, the frame says why; it hears again by itself once it can.
  const going = run.phase === "job" && !isFinal(j.status);
  if (going && NETLINE[run.net] && v.status) {
    const lost = h("small", { class: "lost" }, t(NETLINE[run.net])), sub = v.status.querySelector("small");
    if (sub) sub.replaceWith(lost); else v.status.append(lost);
  }
  const box = h("div", { class: "jbox" + (v.done ? " done" : "") + (v.fail ? " failed" : ""), "data-k": "pv", style: { width: b.w + "px", height: b.h + "px" } },
    v.done ? media : [h("i"), h("i"), h("i"), h("i")], v.status || null);
  if (v.status) { box._c = v.status; v.status._box = box; }
  // The dots: a figure for the state, or the result showing through them as it arrives.
  if (v.ht) halftone(box, v.ht.fig, { ...v.ht, n: run.n, lost: going && !!NETLINE[run.net] });
  else if (v.done) halftone(box, "reveal", { n: run.n });
  const cost = j?.cost?.label ? j.cost.label : v.zero ? zeroOf(j?.estimate?.label || run.priceLabel)
    : j?.free || (run.pay === "free" && !v.fail) ? t("bar.free") : run.shownLabel && !v.fail ? run.shownLabel : "";
  const line = h("p", { class: "jline", "data-k": "j-line" }, [m ? vname(m) : "", cost].filter(Boolean).join(" · "),
    going && run.net === "poll" ? pollTag() : null);
  const [text, note] = v.why || [];
  const why = text || note
    ? h("div", { class: "jwhy", "data-k": "j-why" }, h("p", {}, text, text && note ? " " : "", note ? h("span", { class: "note" }, note) : null))
    : null;
  const prompt = run.prompt ? h("p", { class: "jprompt", "data-k": "j-prompt", hidden: samePrompt(run) }, h("span", { class: "tx" }, run.prompt),
    h("button", { type: "button", class: "cp", "aria-label": t("act.copy"), title: t("act.copy"), onclick: () => copyPrompt(run.prompt) }, ic("copy"))) : null;
  const acts = h("div", { class: "jacts acts", "data-k": "j-acts" }, actRow([...v.acts,
    runOver(run) ? h("button", { type: "button", class: "sq", "aria-label": t("stage.next"), title: t("stage.next"), onclick: () => drop() }, ic("plus"), h("span", { class: "t" }, t("stage.next"))) : null]));
  st.replaceChildren(h("div", { class: "preview", "data-k": "pvbox" }, box), h("div", { class: "jfoot", "data-k": "j-foot" }, line, why, acts, grounding(j), prompt));
  box._fit = () => { tighten(st); roomUnder(st, (u) => { const f = fit(r, u); box.style.width = f.w + "px"; box.style.height = f.h + "px"; return f.h; }, under); caption(box); };
  box._fit();
}

// The composer holds the run's prompt still: the stage does not say it twice.
const samePrompt = (run) => ($("#prompt")?.value || "").trim() === (run.prompt || "").trim();
addEventListener("input", (e) => {
  const p = e.target?.id === "prompt" && S.run && $("#stage .jprompt");
  if (p) p.hidden = samePrompt(S.run);
});

// Download saves the original. res.download is a link that asks for an
// attachment, which a browser saves where it is and Telegram's downloadFile
// takes; res.url only shows the file, and a browser ignores the download
// attribute on another site's link, so without the first it opens in a tab.
const EXT = { "image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/avif": "avif", "image/gif": "gif", "image/svg+xml": "svg", "video/mp4": "mp4", "video/webm": "webm", "video/quicktime": "mov" };
export function download(j, res, i = 0, o = {}) {
  // The name the server gives the download, for Telegram, which asks for one.
  const name = `framy-${j.id}-${res === j.joined ? "joined" : i + 1}.${EXT[res.content_type] || (res.content_type || "").split("/")[1] || "bin"}`;
  const label = o.label || t("stage.download"), cls = o.cls || "primary", aria = cls.startsWith("sq") ? label : null;
  const kids = [ic("down"), h("span", { class: "t" }, label)], tg = S.tg;
  if (res.download && tg?.downloadFile && tg.isVersionAtLeast?.("8.0")) {
    const b = h("button", { type: "button", class: cls, "aria-label": aria, onclick: () => tg.downloadFile({ url: res.download, file_name: name }) }, ...kids);
    b._name = name;
    return b;
  }
  if (res.download) return h("a", { class: cls, "aria-label": aria, href: res.download, download: name }, ...kids);
  return h("a", { class: cls, "aria-label": aria, href: res.url, download: name, target: "_blank", rel: "noopener" }, ...kids);
}

// What the stage shows of the run, to tell a new state from the same one again.
let shownSig = "", shownN = 0, shownStep = null;
const sigOf = (run) => [run.n, run.phase, jobsOf(run).map((j) => [j.status, j.results?.length || 0, j.joined?.id || 0, j.error?.class || "", j.retry_at || "", j.cost?.label || ""].join(",")).join(";"),
  run.net, run.sending ? 1 : 0, S.showJob ? 1 : 0].join("|");
// The run changed: the stage shows its new state, the frame glides to its new shape.
export function runChanged() {
  const run = S.run;
  if (!run) { shownSig = ""; renderStage(); say(""); return; }
  const sig = sigOf(run);
  if (sig === shownSig && run.step === shownStep) return;
  const fresh = run.n !== shownN;
  shownSig = sig; shownN = run.n; shownStep = run.step;
  const d = stageFrom();
  renderStage();
  d();
  say(phaseOf(run));
  if (fresh && S.showJob) anim($("#stage [data-k=pv]"), [{ transform: "scale(.96)", opacity: 0.6 }, { transform: "none", opacity: 1 }], { duration: DUR.l, easing: EASE });
  else if (motion() && (run.multi ? !runOver(run) : run.phase !== "job" || !isFinal(run.job?.status))) rise($$("#stage .jbox .c, #stage .c.under"), { y: 6 });
}

// What a screen reader hears of the run: each phase once as it comes (getting
// ready, in the queue, making it, ready, or why it gave nothing), never the
// clock, and while the next one is set up too.
let said = "";
function say(text) {
  const el = $("#stageSay");
  if (!el || text === said) return;
  said = text;
  el.textContent = text;
}
const labelOf = (v) => v.status?.querySelector("b")?.textContent || "";
const withWhy = (label, why) => (why ? t("stage.why", { label, why }) : label);
function phaseOf(run) {
  if (run.multi && run.phase === "job") {
    const js = jobsOf(run);
    if (js.some((j) => j.status === "running")) return t("stage.running");
    if (js.some((j) => !isFinal(j.status))) return t("stage.queued");
    const cells = cellsOf(run), failed = cells.filter((c) => c.v.fail);
    if (failed.length === cells.length) return phaseOf(failed[0].sub);
    return withWhy(t("stage.done"), failed.map((c) => t("fr.failed", { n: c.i + 1, why: labelOf(c.v) })).join(" "));
  }
  const v = view(run);
  return v.done ? t("stage.done") : withWhy(labelOf(v), v.fail ? v.why.filter(Boolean).join(" ") : "");
}

// The countdowns on the stage and in the viewer, once a second while there are any.
let tickT = 0;
export function ticking() {
  const any = () => $$(".tk").length > 0;
  if (any() && !tickT) tickT = setInterval(() => {
    if (!any()) { clearInterval(tickT); tickT = 0; return; }
    for (const el of $$(".tk")) { const x = el._text?.(); if (x != null && el.textContent !== x) el.textContent = x; }
  }, 1000);
}

// A new window size or a taller dock only resizes the frame: the picture and a playing video stay.
// On a phone the stage takes the room over the dock and keeps the frame in
// the middle of it; the room under the frame waits for the result's line.
function room() {
  const st = $("#stage");
  if (!st) return;
  if (!docked()) { st.style.minHeight = ""; return; }
  const top = st.getBoundingClientRect().top + scrollY;
  st.style.minHeight = Math.max(0, Math.floor(innerHeight - $("#composer").offsetHeight - top - 24)) + "px";
}
// In a low window the stage stands under the composer: a run that starts brings it into view.
export function showStage() {
  if (inFlow()) requestAnimationFrame(() => $("#stage").scrollIntoView({ block: "start", behavior: motion() ? "smooth" : "auto" }));
}
export function refit() {
  room();
  const b = $("#stage [data-k=pv]");
  if (b?._fit) { b._fit(); return; }
  if (!b?._r) return;
  const f = fit(b._r, b._under || 0);
  b.style.width = f.w + "px";
  b.style.height = f.h + "px";
  caption(b);
}
addEventListener("resize", refit);
