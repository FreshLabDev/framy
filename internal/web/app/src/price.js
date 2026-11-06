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
let timer = 0, inflight = 0, fails = 0;

// The page keeps the latest answers, not every one: a session that walks the picker and the settings
// for hours would otherwise grow without end. A Map keeps the order of its keys, so reading an
// answer moves it to the back, and what is on screen (painted again with every batch) stays.
export const CACHE_MAX = 1000;
const remember = (map, k, v) => { map.delete(k); map.set(k, v); if (map.size > CACHE_MAX) map.delete(map.keys().next().value); };
const recall = (map, k) => { const v = map.get(k); if (v !== undefined) { map.delete(k); map.set(k, v); } return v; };
// A price that did not come because the network or the server was busy is asked again a little
// later, a few times; a refusal (a request the server will not price) is not.
const RETRY = [2000, 4000, 8000, 15000, 15000];
const retryable = (e) => e?.code === "network" || e?.status === 429 || e?.status >= 500;

// Everything the server prices by is in the key: a price for a run without its
// files must not stand for one with them (files framy keeps go as inputs).
export const keyOf = (it) => JSON.stringify([it.offer, it.pay || "", it.settings || {}, it.files || [], it.inputs || [], it.continues || 0, it.prompt ? it.prompt.length : 0]);
export const known = (it) => recall(cache, keyOf(it));
export const onPrices = (fn) => listeners.add(fn);
export const clearPrices = () => { cache.clear(); totals.clear(); };

// need() returns what is known now and asks for the rest, in one request a moment later.
export function need(it) {
  const k = keyOf(it), got = recall(cache, k);
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
    (r.items || []).forEach((x, i) => { const k = batch[i]?.[0]; if (k) remember(cache, k, x); });
    fails = 0;
  } catch (e) {
    // A price that did not come stays a question mark. When it is the network or a busy server,
    // it is asked again in a moment, so the question mark goes by itself; else the next change asks.
    if (retryable(e) && fails < RETRY.length) {
      for (const [k, it] of batch) if (!waiting.has(k) && !cache.has(k)) waiting.set(k, it);
      clearTimeout(timer);
      timer = setTimeout(flush, RETRY[fails++]);
    } else fails = 0;
  } finally {
    inflight--;
  }
  // One pass over the page, not one search per run: a batch can hold 200.
  const fresh = new Set(batch.map(([k]) => k));
  for (const el of document.querySelectorAll("[data-q]")) if (fresh.has(el.dataset.q)) paint(el, recall(cache, el.dataset.q));
  for (const fn of listeners) fn();
}

/* How a price reads: the server's label in the person's currency, with its kind. */
export function priceText(e) {
  if (!e || e.kind === "unknown") return t("price.unknown");
  const p = e.label || usd(e.usd);
  return e.kind === "about" ? t("price.about", { p }) : e.kind === "up_to" ? t("price.up_to", { p }) : e.kind === "from" ? t("price.from", { p }) : p;
}
// Dollars, never rounded down (as fx.Format, which browser_money_test.go holds this to): the hint
// when the person's currency is another. Nothing, a negative, NaN and infinity are all "0".
function num(a) {
  if (!(a > 0) || !Number.isFinite(a)) return "0";
  let d = 2;
  if (a < 1) d = Math.min(4, Math.max(2, 1 - Math.floor(Math.log10(a))));
  const p = 10 ** d;
  let s = (Math.ceil(a * p - 1e-9) / p).toFixed(d);
  if (d > 2) { s = s.replace(/0+$/, ""); const i = s.indexOf("."); while (s.length - i - 1 < 2) s += "0"; }
  return s;
}
export const usd = (u) => "$" + num(u);
// The server's label in the person's currency with another amount in it, written as the label
// writes its own (a decimal comma where it has one): a sum, the least of an "up to".
const relabel = (lab, amount) => lab.replace(/\d+(?:[.,]\d+)?/, (x) => (x.includes(",") ? num(amount).replace(".", ",") : num(amount)));
// The price of several runs at once: the sum of each, read the loosest way any
// of them reads (from, then up to, then about). One price that is not known
// makes the sum unknown. The label is the first one's, with the sum in it.
const RANK = { exact: 0, about: 1, up_to: 2, from: 3 };
export function sumOf(es) {
  if (es.length === 1) return es[0];
  if (!es.length || es.some((e) => !e || e.kind === "unknown")) return { kind: "unknown" };
  const kind = es.reduce((k, e) => (RANK[e.kind] > RANK[k] ? e.kind : k), "exact");
  const add = (f) => es.reduce((a, e) => a + (f(e) || 0), 0), top = (e) => (e.kind === "up_to" ? e.max_usd ?? e.usd : e.usd);
  const out = { kind, usd: add((e) => e.usd), max_usd: add(top), currency: es[0].currency };
  const cur = es.every((e) => e.currency === out.currency && e.amount != null);
  if (cur) { out.amount = add((e) => e.amount); out.max_amount = add((e) => (e.kind === "up_to" ? e.max_amount ?? e.amount : e.amount)); }
  if (kind === "up_to") out.usd = out.max_usd;
  const shown = kind === "up_to" ? out.max_amount : out.amount, lab = es.find((e) => e.label)?.label;
  out.label = cur && lab && /\d/.test(lab) ? relabel(lab, shown) : usd(out.usd);
  if (!cur) out.currency = "USD";
  return out;
}
const hint = (e) => (e && e.currency && e.currency !== "USD" && e.kind !== "unknown" ? usd(e.kind === "up_to" ? e.max_usd : e.usd) : null);

// A price element for one run: filled now if known, else when the answer comes.
export function priceEl(it, o = {}) {
  const k = keyOf(it), el = h("span", { class: "usd q" + (o.cls ? " " + o.cls : ""), "data-q": k });
  need(it);
  paint(el, recall(cache, k), o);
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

/* The price of a press of several frames: the server sums the frames that are
   not free ("batch": true) and says which of them go as one job. */
const totals = new Map(); // key -> {total, jobs}
const askedT = new Set();
const totalKey = (items) => items.map(keyOf).join("\n");
export const knownTotal = (items) => recall(totals, totalKey(items));
export function needTotal(items) {
  const k = totalKey(items), got = recall(totals, k);
  if (got || askedT.has(k) || !items.length) return got || null;
  askedT.add(k);
  api("/api/quote", { method: "POST", body: { items, batch: true } }).then((r) => {
    remember(totals, k, { total: r.total || null, jobs: r.jobs || [] });
    (r.items || []).forEach((x, i) => { if (items[i]) remember(cache, keyOf(items[i]), x); });
    for (const fn of listeners) fn();
  }, () => {}).finally(() => askedT.delete(k));
  return null;
}

// The lowest of several runs (a family's variants, a model's offers), as "from" when they differ.
// "From" is the least any of them costs. That of an "up to" is its usd, while its label is the
// most it can cost, so the label is written again from its least.
const mins = new Set();
export function lowest(items) {
  const got = items.map((it) => recall(cache, keyOf(it)));
  if (got.some((g) => !g)) return null;
  const es = got.map((g) => g.estimate).filter((e) => e && e.kind !== "unknown");
  if (!es.length) return { kind: "unknown" };
  const lo = es.reduce((a, b) => (a.usd <= b.usd ? a : b));
  if (!es.some((e) => Math.abs(e.usd - lo.usd) > 1e-9)) return lo;
  if (lo.kind !== "up_to") return { ...lo, kind: "from" };
  const mine = lo.amount != null && lo.label && /\d/.test(lo.label);
  return { ...lo, kind: "from", max_usd: lo.usd, max_amount: lo.amount, label: mine ? relabel(lo.label, lo.amount) : usd(lo.usd), currency: mine ? lo.currency : "USD" };
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
