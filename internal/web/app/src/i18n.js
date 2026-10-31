// The site's lines: the web.* keys of internal/i18n/translations.json, one
// file per language, built by internal/web/build. t() fills {placeholders}.

let lines = {};
export let lang = "en";

export function setTexts(code, texts) { lang = code; lines = texts || {}; document.documentElement.lang = code; document.documentElement.dir = code === "ar" ? "rtl" : "ltr"; }

export function t(key, vars) {
  let s = lines[key];
  if (s == null) return key;
  if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (vars[k] != null ? String(vars[k]) : m));
  return s;
}

// Locale-aware numbers and times for what the server does not format. A
// formatter is slow to make, so each one is made once.
const fmts = new Map();
const made = (k, make) => { let f = fmts.get(k); if (!f) fmts.set(k, (f = make())); return f; };
export const fmtNum = (n, o) => made("n" + lang + JSON.stringify(o || {}), () => new Intl.NumberFormat(lang, o)).format(n);
export const byName = (a, b) => made("c" + lang, () => new Intl.Collator(lang)).compare(a, b);
// The first formatter of a language loads its data: done while the page is
// idle, not on the first switch.
export const warmFormats = () => { fmtNum(1); fmtNum(1.5, { maximumFractionDigits: 1 }); byName("a", "b"); };
export const fmtSecs = (s) => t("unit.s", { n: fmtNum(+s || 0, { maximumFractionDigits: 1 }) });
export const fmtTime = (ms) => new Date(ms).toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit" });
// "in 3 hours", "in 2 days": when something comes back.
export function fmtIn(ms) {
  const sec = Math.max(60, (ms - Date.now()) / 1000), rtf = made("r" + lang, () => new Intl.RelativeTimeFormat(lang, { numeric: "auto" }));
  if (sec < 3600) return rtf.format(Math.round(sec / 60), "minute");
  if (sec < 86400) return rtf.format(Math.round(sec / 3600), "hour");
  return rtf.format(Math.round(sec / 86400), "day");
}
