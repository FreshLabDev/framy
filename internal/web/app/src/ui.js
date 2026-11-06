// Sheets, the toast and the one question dialog.
import { $, $$, h, isWide, markOf, findMark } from "./dom.js";
import { anim, EASE, OUT, DUR } from "./motion.js";
import { t } from "./i18n.js";

/* Back. What is open over the page (a sheet, a result on the whole screen,
   a question) is closed by the back of the browser or of the phone, the top
   one first, rather than the page going back under it. On the site it has an
   entry in the history at the page's own address, one for all of them, put
   back while one is still open; the Mini App keeps no history and shows
   Telegram's back button instead (main.js). The viewer has an address of
   its own on the site (/r/<id>), so there it is no layer. */
let backHook = () => {};
// A wide sheet stands at the end of the line, the right or, in Arabic, the left, and comes in from that side.
const away = (wide) => (wide ? `translateX(${document.dir === "rtl" ? -64 : 64}px)` : "translateY(100%)");
export const onSheets = (fn) => { backHook = fn; };
const tops = new Map(); // key -> what closes it, the top last
let hist = false;       // the site, once main.js says so; never the Mini App
let armed = false;      // the history's current entry is the layers' own
let backing = false;    // a back of the page's own is on its way
const later = [];       // changes of the history that wait for it
// On for the site. A reload on the layers' entry finds nothing open over the
// page, so the entry goes, and a back later is not spent on it.
export function useHistory(on) { hist = on; armed = on && !!history.state?.over; settle(); }
// layer says what is open over the page: close closes it, null that it closed.
export function layer(key, close) {
  if (close) { tops.delete(key); tops.set(key, close); } else if (!tops.delete(key)) return;
  trap();
  settle();
  backHook();
}
/* The keyboard stays in what is open on top: the question, else the result
   on the whole screen, else the sheet. The rest of the page is inert
   meanwhile, so Tab goes round inside it and a screen reader reads only it.
   The scrim is left out: a press on it closes the sheet. */
function trap() {
  const top = tops.has("ask") ? $("#ask") : tops.has("full") ? $("#full") : open[open.length - 1] || null;
  for (const el of $$("#app > :not(.scrim)")) el.inert = !!top && el !== top;
}
export const overOpen = () => tops.size > 0 || open.length > 0;
// Closes the top layer, then a sheet that is no layer: false when nothing was open.
export function closeTop() {
  const close = [...tops.values()].pop();
  if (close) { close(); return true; }
  if (open.length) { closeSheets(); return true; }
  return false;
}
function settle() {
  if (!hist || backing) return;
  if (tops.size && !armed) { history.pushState({ ...history.state, over: 1 }, ""); armed = true; }
  else if (!tops.size && armed) { armed = false; goBack(); }
}
// A back of the page's own: what changes the history meanwhile waits for it to land.
export function goBack() { backing = true; history.back(); }
export function inHistory(fn) { if (backing) later.push(fn); else fn(); }
// A new address pushed while the layers have the entry: it takes the entry's place.
export function takeEntry() { if (!armed) return false; armed = false; return true; }
// Registered before the library's handler of the address, so it runs first.
addEventListener("popstate", () => {
  if (!hist) return;
  if (backing) {
    backing = false;
    while (!backing && later.length) later.shift()();
    settle();
    return;
  }
  // Back from the layers' entry: the top one closes; settle puts the entry back for the others.
  if (armed && !history.state?.over) { armed = false; [...tops.values()].pop()?.(); settle(); }
  // Forward onto the entry of layers closed since: nothing is open over the
  // page, so it goes back again rather than wait to spend the next back.
  else if (!armed && !tops.size && history.state?.over) goBack();
});

const open = [];
export const sheetOpen = () => open.length > 0;
// Whether an open sheet hides el whole. The scrim beside a sheet only dims
// what is under it, and a sheet does not hide what is in it.
export function underSheet(el) {
  if (!open.length) return false;
  const r = el.getBoundingClientRect();
  return open.some((s) => {
    if (s.contains(el)) return false;
    const q = s.getBoundingClientRect();
    return q.left <= r.left + 1 && q.right >= r.right - 1 && q.top <= r.top + 1 && q.bottom >= r.bottom - 1;
  });
}

// o.back: a sheet opened from another one goes back to it when it closes;
// back is what opens that one again. o.focus: what has the focus when it
// opens. o.own: the sheet has an address of its own, so it is no layer.
export function openSheet(sel, o = {}) {
  const el = $(sel), scrim = $("#scrim");
  if (!el.hidden) return;
  const had = open.length > 0;
  // A sheet another one replaces is closed as any other.
  for (const s of open.splice(0)) { s.hidden = true; s.dispatchEvent(new Event("closed")); }
  el.hidden = false; scrim.hidden = false;
  el._back = o.back || null;
  open.push(el);
  const wide = isWide();
  anim(el, [{ transform: away(wide), opacity: wide ? 0 : 1 }, { transform: "none", opacity: 1 }], { duration: DUR.l, easing: EASE });
  if (!had) anim(scrim, [{ opacity: 0 }, { opacity: 1 }], { duration: DUR.s });
  const from = document.activeElement;
  el._focus = from; el._mark = markOf(from);
  // Where the sheet begins for the keyboard: its field to type in where that
  // raises no keyboard on the screen (with a mouse, or opened from the
  // keyboard), else its first button, so a tap does not cover the list.
  const typing = matchMedia("(hover: hover) and (pointer: fine)").matches || !!from?.matches?.(":focus-visible");
  const first = (o.focus && el.querySelector(o.focus)) || (typing && el.querySelector("[data-first]")) || el.querySelector("button");
  // Focus after the first frame: now it would lay the sheet out in the middle
  // of this task.
  requestAnimationFrame(() => setTimeout(() => { if (open.includes(el)) first?.focus({ preventScroll: true }); }));
  if (o.own) layer("sheet", null); else layer("sheet", () => closeSheets());
  trap();
  backHook();
}

export function closeSheets(quick) {
  // Done, Escape or Back on a sheet opened from another one: that one again.
  // Closing everything at once (quick) does not go back.
  const back = !quick && open.length === 1 && open[0]._back;
  if (back) { open[0]._back = null; back(); return; }
  const scrim = $("#scrim");
  const els = open.splice(0);
  if (!els.length) return;
  // What was under them shows again: the dots on the stage move at once (halftone.js).
  dispatchEvent(new Event("framy:sheets"));
  trap();
  const wide = isWide();
  for (const el of els) {
    const a = quick ? null : anim(el, [{ transform: "none", opacity: 1 }, { transform: away(wide), opacity: wide ? 0 : 1 }], { duration: DUR.s, easing: OUT, fill: "forwards" });
    const done = () => { el.hidden = true; a?.cancel(); el.dispatchEvent(new Event("closed")); };
    if (a) a.finished.then(done, done); else done();
    back0(el);
  }
  const b = quick ? null : anim(scrim, [{ opacity: 1 }, { opacity: 0 }], { duration: DUR.s, fill: "forwards" });
  const sd = () => { scrim.hidden = true; b?.cancel(); };
  if (b) b.finished.then(sd, sd); else sd();
  layer("sheet", null);
  backHook();
}

// The focus goes back to what opened the sheet, or to what was drawn in its
// place meanwhile (the model's card, once a model was chosen in the picker).
function back0(el) {
  const f = el._focus?.isConnected ? el._focus : findMark(el._mark);
  el._focus = null; el._mark = null;
  f?.focus?.({ preventScroll: true });
}

document.addEventListener("keydown", (e) => { if (e.key === "Escape" && open.length) { e.preventDefault(); closeSheets(); } });

let toastT = 0;
export function toast(text, o = {}) {
  $(".toast")?.remove();
  clearTimeout(toastT);
  const el = h("div", { class: "toast", role: "status" }, text, o.action ? h("button", { type: "button", class: "link-t", onclick: () => { el.remove(); o.action.run(); } }, o.action.label) : null);
  document.body.append(el);
  anim(el, [{ opacity: 0, transform: "translate(-50%, 12px)" }, { opacity: 1, transform: "translate(-50%, 0)" }], { duration: DUR.m });
  toastT = setTimeout(() => {
    const a = anim(el, [{ opacity: 1 }, { opacity: 0 }], { duration: DUR.s, fill: "forwards" });
    if (a) a.finished.then(() => el.remove(), () => el.remove()); else el.remove();
  }, o.ms || 4200);
}

// One question with two answers, or one when it only tells; resolves true for
// yes. It starts on the answer that changes nothing, so an Enter pressed too
// soon deletes nothing, and once answered the focus goes back where it was
// (or to what was drawn in its place), unless the answer moved it on.
export function ask({ title, text, yes, no = t("cancel"), one = false }) {
  const box = $("#ask");
  return new Promise((done) => {
    const from = document.activeElement, mark = markOf(from);
    const end = (v) => {
      if (box.hidden) return;
      box.hidden = true; box.replaceChildren(); layer("ask", null);
      const f = from?.isConnected ? from : findMark(mark);
      if (!document.activeElement || document.activeElement === document.body) f?.focus?.({ preventScroll: true });
      done(v);
    };
    box.replaceChildren(h("div", { class: "box" },
      h("h2", { id: "askT" }, title), text ? h("p", { id: "askD" }, text) : null,
      h("div", { class: "acts" + (one ? " one" : "") }, one ? null : h("button", { type: "button", class: "x no", onclick: () => end(false) }, no),
        h("button", { type: "button", class: "x yes", onclick: () => end(true) }, yes))));
    if (text) box.setAttribute("aria-describedby", "askD"); else box.removeAttribute("aria-describedby");
    box.hidden = false;
    layer("ask", () => end(false));
    anim(box.firstChild, [{ opacity: 0, transform: "translateY(16px)" }, { opacity: 1, transform: "none" }], { duration: DUR.m });
    box.querySelector(one ? ".yes" : ".no").focus();
    // Escape answers the question only, not the sheet under it.
    box.onkeydown = (e) => { if (e.key === "Escape") { e.stopPropagation(); end(false); } };
  });
}

/* A figure's hint, such as the dollars behind a price in another currency: one small box over the
   figure, on hover with a mouse, on a tap, and on focus from the keyboard, since a title shows to a
   mouse only. A screen reader has it as the figure's description. hint(el, "") takes it away. */
let tipBox = null, tipOf = null, tipWait = 0;
function tipHide() {
  clearTimeout(tipWait);
  if (!tipOf) return;
  tipBox.hidden = true;
  tipOf = null;
}
function tipShow(el) {
  clearTimeout(tipWait);
  if (!el._hint || !el.isConnected) return tipHide();
  if (!tipBox) document.body.append((tipBox = h("div", { class: "tip", "aria-hidden": "true", hidden: true })));
  const was = tipOf;
  tipBox.textContent = el._hint;
  tipBox.hidden = false;
  tipOf = el;
  // Over the figure, from its start; under it when there is no room above.
  const r = el.getBoundingClientRect(), w = tipBox.offsetWidth, th = tipBox.offsetHeight;
  const start = getComputedStyle(el).direction === "rtl" ? r.right - w : r.left;
  tipBox.style.left = Math.max(8, Math.min(start, innerWidth - w - 8)) + "px";
  tipBox.style.top = (r.top - th - 8 >= 8 ? r.top - th - 8 : r.bottom + 8) + "px";
  if (was !== el) anim(tipBox, [{ opacity: 0, transform: "translateY(4px)" }, { opacity: 1, transform: "none" }], { duration: DUR.xs });
}
export function hint(el, text) {
  if (!el._desc) {
    el._desc = h("span", { id: el.id + "-hint", hidden: true });
    el.append(el._desc);
    let by = "";
    el.addEventListener("pointerenter", (e) => { if (e.pointerType === "mouse") tipWait = setTimeout(() => tipShow(el), 300); });
    el.addEventListener("pointerleave", (e) => { if (e.pointerType === "mouse" && !el.matches(":focus-visible")) tipHide(); });
    el.addEventListener("pointerdown", (e) => { by = e.pointerType; });
    // A tap shows it and the next one hides it; a mouse has it already from hovering.
    el.addEventListener("click", () => { if (by === "mouse") tipShow(el); else if (tipOf === el) tipHide(); else tipShow(el); by = ""; });
    el.addEventListener("focus", () => { if (el.matches(":focus-visible")) tipShow(el); });
    el.addEventListener("blur", tipHide);
    el.addEventListener("keydown", (e) => {
      if (!el._hint) return;
      if (e.key === "Escape" && tipOf === el) tipHide();
      else if (e.key === "Enter" || e.key === " ") { e.preventDefault(); if (tipOf === el) tipHide(); else tipShow(el); }
    });
  }
  el._hint = text || "";
  if (el._desc.textContent !== el._hint) el._desc.textContent = el._hint;
  el.classList.toggle("hint", !!text);
  if (text) { el.tabIndex = 0; el.setAttribute("role", "button"); el.setAttribute("aria-describedby", el._desc.id); }
  else { el.removeAttribute("tabindex"); el.removeAttribute("role"); el.removeAttribute("aria-describedby"); }
  if (tipOf === el) tipShow(el);
}
document.addEventListener("pointerdown", (e) => { if (tipOf && !tipOf.contains(e.target)) tipHide(); }, true);
addEventListener("scroll", () => tipHide(), { passive: true, capture: true });
addEventListener("resize", () => tipHide());
