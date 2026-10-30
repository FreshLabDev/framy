// Prices. Every figure on the page comes from POST /api/quote; the composer
// asks for what it shows, many runs in one request, and keeps the answers.
// A price element names its run (data-q) and is filled in place when the
// answer comes, so a new price never rebuilds the form around it.
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { api } from "./api.js";

const cache = new Map(); // key -> {estimate, refusal, settings}
const waiting = new Map(); // key -> item
const listeners = new Set();
let timer = 0, inflight = 0;

export const keyOf = (it) => JSON.stringify([it.offer, it.pay || "", it.settings || {}, it.files || [], it.prompt ? it.prompt.length : 0]);
export const known = (it) => cache.get(keyOf(it));
export const onPrices = (fn) => listeners.add(fn);
export const clearPrices = () => cache.clear();

// need() returns what is known now and asks for the rest, in one request a moment later.
export function need(it) {
  const k = keyOf(it), got = cache.get(k);
  if (got) return got;
  if (!waiting.has(k)) { waiting.set(k, it); if (!timer) timer = setTimeout(flush, 40); }
  return null;
}

async function flush() {
  timer = 0;
  const batch = [...waiting.entries()].slice(0, 200);
  for (const [k] of batch) waiting.delete(k);
  if (waiting.size) timer = setTimeout(flush, 0);
  if (!batch.length) return;
  inflight++;
  try {
    const r = await api("/api/quote", { method: "POST", body: { items: batch.map(([, it]) => it) } });
    (r.items || []).forEach((x, i) => { const k = batch[i]?.[0]; if (k) cache.set(k, x); });
  } catch {
    // A price that did not come stays a question mark; the next change asks again.
  } finally {
    inflight--;
  }
  // One pass over the page, not one search per run: a batch can hold 200.
  const fresh = new Set(batch.map(([k]) => k));
  for (const el of document.querySelectorAll("[data-q]")) if (fresh.has(el.dataset.q)) paint(el, cache.get(el.dataset.q));
  for (const fn of listeners) fn();
}

/* How a price reads: the server's label in the person's currency, with its kind. */
export function priceText(e) {
  if (!e || e.kind === "unknown") return t("price.unknown");
  const p = e.label || usd(e.usd);
  return e.kind === "about" ? t("price.about", { p }) : e.kind === "up_to" ? t("price.up_to", { p }) : e.kind === "from" ? t("price.from", { p }) : p;
}
// Dollars, never rounded down (as fx.Format): the hint when the person's currency is another.
function num(a) {
  if (!(a > 0)) return "0";
  let d = 2;
  if (a < 1) d = Math.min(4, Math.max(2, 1 - Math.floor(Math.log10(a))));
  const p = 10 ** d;
  let s = (Math.ceil(a * p - 1e-9) / p).toFixed(d);
  if (d > 2) { s = s.replace(/0+$/, ""); const i = s.indexOf("."); while (s.length - i - 1 < 2) s += "0"; }
  return s;
}
export const usd = (u) => "$" + num(u);
const hint = (e) => (e && e.currency && e.currency !== "USD" && e.kind !== "unknown" ? usd(e.kind === "up_to" ? e.max_usd : e.usd) : null);

// A price element for one run: filled now if known, else when the answer comes.
export function priceEl(it, o = {}) {
  const k = keyOf(it), el = h("span", { class: "usd q" + (o.cls ? " " + o.cls : ""), "data-q": k });
  need(it);
  paint(el, cache.get(k), o);
  el._o = o;
  return el;
}
function paint(el, got, o = el._o || {}) {
  if (!got) { el.textContent = o.wait ?? "…"; el.classList.add("wait"); return; }
  el.classList.remove("wait");
  const e = got.estimate;
  el.textContent = o.text ? o.text(got) : priceText(e);
  const tip = hint(e);
  if (tip) el.title = tip; else el.removeAttribute("title");
}
export const busy = () => inflight > 0 || waiting.size > 0;

// The lowest of several runs (a family's variants, a model's offers), as "from" when they differ.
const mins = new Set();
export function lowest(items) {
  const got = items.map((it) => cache.get(keyOf(it)));
  if (got.some((g) => !g)) return null;
  const es = got.map((g) => g.estimate).filter((e) => e && e.kind !== "unknown");
  if (!es.length) return { kind: "unknown" };
  const lo = es.reduce((a, b) => (a.usd <= b.usd ? a : b));
  return es.some((e) => Math.abs(e.usd - lo.usd) > 1e-9) ? { ...lo, kind: "from" } : lo;
}
export function minEl(items, o = {}) {
  const el = h("span", { class: "usd q" + (o.cls ? " " + o.cls : "") });
  el._items = items; el._o = o;
  items.forEach(need);
  paintMin(el);
  mins.add(el);
  return el;
}
function paintMin(el) {
  const e = lowest(el._items), o = el._o;
  if (!e) { el.textContent = o.wait ?? "…"; el.classList.add("wait"); return; }
  el.classList.remove("wait");
  el.textContent = o.text ? o.text(e) : priceText(e);
  const tip = hint(e);
  if (tip) el.title = tip; else el.removeAttribute("title");
}
listeners.add(() => { for (const el of mins) { if (!el.isConnected) { mins.delete(el); continue; } paintMin(el); } });
