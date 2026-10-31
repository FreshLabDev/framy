// Sheets, the toast and the one question dialog.
import { $, h, isWide } from "./dom.js";
import { anim, EASE, OUT } from "./motion.js";
import { t } from "./i18n.js";

let backHook = () => {};
export const onSheets = (fn) => { backHook = fn; };
const open = [];
export const sheetOpen = () => open.length > 0;

export function openSheet(sel) {
  const el = $(sel), scrim = $("#scrim");
  if (!el.hidden) return;
  // A sheet another one replaces is closed as any other.
  for (const s of open.splice(0)) { s.hidden = true; s.dispatchEvent(new Event("closed")); }
  el.hidden = false; scrim.hidden = false;
  open.push(el);
  const wide = isWide();
  anim(el, [{ transform: wide ? "translateX(64px)" : "translateY(100%)", opacity: wide ? 0 : 1 }, { transform: "none", opacity: 1 }], { duration: 420, easing: EASE });
  anim(scrim, [{ opacity: 0 }, { opacity: 1 }], { duration: 260 });
  el._focus = document.activeElement;
  // Focus after the first frame: now it would lay the sheet out in the middle
  // of this task.
  const first = el.querySelector("[autofocus], input, button");
  requestAnimationFrame(() => setTimeout(() => { if (open.includes(el)) first?.focus({ preventScroll: true }); }));
  backHook(true);
}

export function closeSheets(quick) {
  const scrim = $("#scrim");
  const els = open.splice(0);
  if (!els.length) return;
  const wide = isWide();
  for (const el of els) {
    const a = quick ? null : anim(el, [{ transform: "none", opacity: 1 }, { transform: wide ? "translateX(64px)" : "translateY(100%)", opacity: wide ? 0 : 1 }], { duration: 260, easing: OUT, fill: "forwards" });
    const done = () => { el.hidden = true; a?.cancel(); el.dispatchEvent(new Event("closed")); };
    if (a) a.finished.then(done, done); else done();
    el._focus?.focus?.({ preventScroll: true });
  }
  const b = quick ? null : anim(scrim, [{ opacity: 1 }, { opacity: 0 }], { duration: 220, fill: "forwards" });
  const sd = () => { scrim.hidden = true; b?.cancel(); };
  if (b) b.finished.then(sd, sd); else sd();
  backHook(false);
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
    const end = (v) => { box.hidden = true; box.replaceChildren(); done(v); };
    box.replaceChildren(h("div", { class: "box" },
      h("h2", { id: "askT" }, title), text ? h("p", { id: "askD" }, text) : null,
      h("div", { class: "acts" + (one ? " one" : "") }, one ? null : h("button", { type: "button", class: "x", onclick: () => end(false) }, no),
        h("button", { type: "button", class: "x yes", onclick: () => end(true) }, yes))));
    box.hidden = false;
    anim(box.firstChild, [{ opacity: 0, transform: "translateY(16px)" }, { opacity: 1, transform: "none" }], { duration: 320 });
    box.querySelector(".yes").focus();
    // Escape answers the question only, not the sheet under it.
    box.onkeydown = (e) => { if (e.key === "Escape") { e.stopPropagation(); end(false); } };
  });
}
