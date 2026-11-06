// The site's lines: the web.* keys of internal/i18n/translations.json and the
// models' notes (note.<model id>), one file per language, built by
// internal/web/build. t() fills {placeholders}.

import { FILLS } from "./intlfill.js";

let lines = {};
export let lang = "en";
let rtl = false;
// The formats of a language the browser has no data for (intlfill.js).
let fill = null;
const known = (code) => { try { return Intl.DateTimeFormat.supportedLocalesOf([code]).length > 0; } catch { return true; } };

export function setTexts(code, texts) {
  lang = code; lines = texts || {}; rtl = code === "ar"; fill = FILLS[code] && !known(code) ? FILLS[code] : null;
  document.documentElement.lang = code; document.documentElement.dir = rtl ? "rtl" : "ltr";
}

export const has = (key) => lines[key] != null;

// In Arabic a name, a number or a price put into a line keeps its own order:
// "من $0.282", not "0.282$ من", and "اختر 2.6 Flash", not "Flash 2.6". Each one
// is isolated (U+2068 ... U+2069), which shows nothing and reads nothing.
const put = (v) => (rtl ? "\u2068" + v + "\u2069" : String(v));
export function t(key, vars) {
  let s = lines[key];
  if (s == null) return key;
  if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (vars[k] != null ? put(vars[k]) : m));
  return s;
}

// Locale-aware numbers and times for what the server does not format. A
// formatter is slow to make, so each one is made once.
const fmts = new Map();
const made = (k, make) => { let f = fmts.get(k); if (!f) fmts.set(k, (f = make())); return f; };
export const fmtNum = (n, o) => made("n" + lang + JSON.stringify(o || {}), () => (fill ? fill.numFormat(o) : new Intl.NumberFormat(lang, o))).format(n);
// A list inside a sentence, as the language writes one: "Size, Quality and Background",
// "1、3" in Japanese, "A وB" in Arabic. A browser without ListFormat joins with commas.
export const fmtList = (items, style = "long") => {
  const xs = items.map(String);
  if (!Intl.ListFormat) return xs.join(", ");
  return made("l" + lang + style, () => new Intl.ListFormat(lang, { type: "conjunction", style })).format(xs);
};
export const byName = (a, b) => made("c" + lang, () => new Intl.Collator(lang)).compare(a, b);
// The first formatter of a language loads its data: done while the page is
// idle, not on the first switch.
export const warmFormats = () => { fmtNum(1); fmtNum(1.5, { maximumFractionDigits: 1 }); byName("a", "b"); };
export const fmtSecs = (s) => t("unit.s", { n: fmtNum(+s || 0, { maximumFractionDigits: 1 }) });
// "14:30".
export const fmtTime = (ms) => (fill ? fill.time(ms) : new Date(ms).toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit" }));
// "14:30" today, "5 November, 14:30" on another day, in the browser's time zone: when a free run frees up.
export function fmtAt(ms) {
  const today = new Date(ms).toDateString() === new Date().toDateString();
  if (fill) return today ? fill.time(ms) : fill.at(ms);
  return made((today ? "h" : "a") + lang, () => new Intl.DateTimeFormat(lang, today ? { hour: "numeric", minute: "2-digit" } : { day: "numeric", month: "long", hour: "numeric", minute: "2-digit" })).format(ms);
}
// "5 November", with the year when year is set.
export const fmtDay = (ms, year) => (fill ? fill.day(ms, year) : made("d" + lang + !!year, () => new Intl.DateTimeFormat(lang, { day: "numeric", month: "long", ...(year ? { year: "numeric" } : {}) })).format(ms));
// A day the catalog names ("2026-11-11", a model's shut-down day) as it is
// written, the same day in every time zone, with the year when it is not this
// one: read as midnight UTC, it is the day before west of Greenwich.
export function fmtIsoDay(iso) {
  const [y, m, d] = String(iso).split("-").map(Number);
  if (!y || !m || !d) return iso;
  return fmtDay(new Date(y, m - 1, d).getTime(), y !== new Date().getFullYear());
}
// "5 November 2026 at 14:30": when a run was made.
export const fmtWhen = (ms) => (fill ? fill.when(ms) : made("w" + lang, () => new Intl.DateTimeFormat(lang, { dateStyle: "long", timeStyle: "short" })).format(ms));
// "in 3 hours", "yesterday": n minutes, hours or days from now, in words where the language has them.
export const fmtRel = (n, unit) => (fill ? fill.rel(n, unit) : made("r" + lang, () => new Intl.RelativeTimeFormat(lang, { numeric: "auto" })).format(n, unit));
// "in 10 seconds": a short wait, in the phrase the language says it with,
// so the count takes the case it needs ("через 1 секунду").
export const fmtInSecs = (s) => made("r" + lang, () => new Intl.RelativeTimeFormat(lang, { numeric: "auto" })).format(Math.max(1, Math.round(s)), "second");
// "15 minutes": a length of time as a count of a unit, spelled out.
export const fmtUnit = (n, unit) => made("u" + lang + unit, () => new Intl.NumberFormat(lang, { style: "unit", unit, unitDisplay: "long" })).format(n);
// A sentence with more after it on its line, a button to act on it: a space
// between, but none after a full-width stop ("。"), which has its room in it.
export const spaced = (s) => (/[。！？：]$/.test(s) ? s : s + " ");
// "in 3 hours", "in 2 days": when something comes back.
export function fmtIn(ms) {
  const sec = Math.max(60, (ms - Date.now()) / 1000);
  if (sec < 3600) return fmtRel(Math.round(sec / 60), "minute");
  if (sec < 86400) return fmtRel(Math.round(sec / 3600), "hour");
  return fmtRel(Math.round(sec / 86400), "day");
}
// "US dollar", or the code where the language has no name for it.
export function fmtCurrency(code) {
  if (fill) return fill.cur(code) || code;
  try { return made("$" + lang, () => new Intl.DisplayNames([lang], { type: "currency" })).of(code) || code; } catch { return code; }
}
