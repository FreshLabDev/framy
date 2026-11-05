// Sheets, the toast and the one question dialog.
import { $, h, isWide, markOf, findMark } from "./dom.js";
import { anim, EASE, OUT } from "./motion.js";
import { t } from "./i18n.js";

/* Back. What is open over the page (a sheet, a result on the whole screen,
   a question) is closed by the back of the browser or of the phone, the top
   one first, rather than the page going back under it. On the site it has an
   entry in the history at the page's own address, one for all of them, put
   back while one is still open; the Mini App keeps no history and shows
   Telegram's back button instead (main.js). The viewer has an address of
   its own on the site (/r/<id>), so there it is no layer. */
let backHook = () => {};
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
  settle();
  backHook();
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

// o.back: a sheet opened from another one goes back to it when it closes;
// back is what opens that one again. o.own: the sheet has an address of its
// own, so it is no layer.
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
  anim(el, [{ transform: wide ? "translateX(64px)" : "translateY(100%)", opacity: wide ? 0 : 1 }, { transform: "none", opacity: 1 }], { duration: 420, easing: EASE });
  if (!had) anim(scrim, [{ opacity: 0 }, { opacity: 1 }], { duration: 260 });
  el._focus = document.activeElement; el._mark = markOf(el._focus);
  // Focus after the first frame: now it would lay the sheet out in the middle
  // of this task.
  const first = el.querySelector("[autofocus], input, button");
  requestAnimationFrame(() => setTimeout(() => { if (open.includes(el)) first?.focus({ preventScroll: true }); }));
  if (o.own) layer("sheet", null); else layer("sheet", () => closeSheets());
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
  const wide = isWide();
  for (const el of els) {
    const a = quick ? null : anim(el, [{ transform: "none", opacity: 1 }, { transform: wide ? "translateX(64px)" : "translateY(100%)", opacity: wide ? 0 : 1 }], { duration: 260, easing: OUT, fill: "forwards" });
    const done = () => { el.hidden = true; a?.cancel(); el.dispatchEvent(new Event("closed")); };
    if (a) a.finished.then(done, done); else done();
    back0(el);
  }
  const b = quick ? null : anim(scrim, [{ opacity: 1 }, { opacity: 0 }], { duration: 220, fill: "forwards" });
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
  anim(el, [{ opacity: 0, transform: "translate(-50%, 12px)" }, { opacity: 1, transform: "translate(-50%, 0)" }], { duration: 300 });
  toastT = setTimeout(() => {
    const a = anim(el, [{ opacity: 1 }, { opacity: 0 }], { duration: 200, fill: "forwards" });
    if (a) a.finished.then(() => el.remove(), () => el.remove()); else el.remove();
  }, o.ms || 4200);
}

// One question with two answers, or one when it only tells; resolves true for yes.
export function ask({ title, text, yes, no = t("cancel"), one = false }) {
  const box = $("#ask");
  return new Promise((done) => {
    const end = (v) => { if (box.hidden) return; box.hidden = true; box.replaceChildren(); layer("ask", null); done(v); };
    box.replaceChildren(h("div", { class: "box" },
      h("h2", { id: "askT" }, title), text ? h("p", { id: "askD" }, text) : null,
      h("div", { class: "acts" + (one ? " one" : "") }, one ? null : h("button", { type: "button", class: "x", onclick: () => end(false) }, no),
        h("button", { type: "button", class: "x yes", onclick: () => end(true) }, yes))));
    box.hidden = false;
    layer("ask", () => end(false));
    anim(box.firstChild, [{ opacity: 0, transform: "translateY(16px)" }, { opacity: 1, transform: "none" }], { duration: 320 });
    box.querySelector(".yes").focus();
    // Escape answers the question only, not the sheet under it.
    box.onkeydown = (e) => { if (e.key === "Escape") { e.stopPropagation(); end(false); } };
  });
}
