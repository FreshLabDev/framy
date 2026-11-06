// The formats of a language whose data the browser lacks. Chromium's ICU
// has no Belarusian (Chrome, Edge, Android's WebView and so Telegram there),
// and its Intl falls back to English: "November 5 at 4:39 PM", "in 3 hours",
// "US Dollar" amid Belarusian lines. These are CLDR's own Belarusian forms,
// as Safari gives them; i18n.js uses them only where Intl has none.

const two = (n) => String(n).padStart(2, "0");
// CLDR's plural rule for Belarusian: one, few, many, and other for a fraction.
function form(n) {
  n = Math.abs(n);
  if (!Number.isInteger(n)) return 3;
  const a = n % 10, b = n % 100;
  if (a === 1 && b !== 11) return 0;
  if (a >= 2 && a <= 4 && (b < 12 || b > 14)) return 1;
  return 2;
}

const MONTHS = ["студзеня", "лютага", "сакавіка", "красавіка", "мая", "чэрвеня", "ліпеня", "жніўня", "верасня", "кастрычніка", "лістапада", "снежня"];
// Each unit in the forms of form() above.
const UNITS = {
  minute: ["хвіліну", "хвіліны", "хвілін", "хвіліны"],
  hour: ["гадзіну", "гадзіны", "гадзін", "гадзіны"],
  day: ["дзень", "дні", "дзён", "дня"],
};
// What { numeric: "auto" } says in words.
const NAMED = { minute: { 0: "у гэту хвіліну" }, hour: { 0: "у гэту гадзіну" }, day: { "-2": "пазаўчора", "-1": "учора", 0: "сёння", 1: "заўтра", 2: "паслязаўтра" } };
const CURRENCIES = { USD: "долар ЗША", UAH: "украінская грыўня", EUR: "еўра", PLN: "польскі злоты", BYN: "беларускі рубель" };

// Belarusian writes a number as Russian does, a decimal comma and a space,
// but groups its thousands only from five digits on ("1234,5", "12 345,5").
const numFormat = (o) => new Intl.NumberFormat("ru", { useGrouping: "min2", ...o });
let plain = null;
const num = (n) => (plain ||= numFormat()).format(n);
const date = (d, year) => d.getDate() + " " + MONTHS[d.getMonth()] + (year ? " " + d.getFullYear() + " г." : "");

export const FILLS = {
  be: {
    numFormat,
    // 09:05
    time: (ms) => { const d = new Date(ms); return two(d.getHours()) + ":" + two(d.getMinutes()); },
    // 5 лістапада у 9:05
    at: (ms) => { const d = new Date(ms); return date(d) + " у " + d.getHours() + ":" + two(d.getMinutes()); },
    // 5 лістапада, or 5 лістапада 2026 г.
    day: (ms, year) => date(new Date(ms), year),
    // 5 лістапада 2026 г. у 09:05
    when: (ms) => { const d = new Date(ms); return date(d, true) + " у " + two(d.getHours()) + ":" + two(d.getMinutes()); },
    // праз 3 гадзіны, 5 дзён таму, учора: a minute, an hour or a day.
    rel: (n, unit) => {
      const named = NAMED[unit][n];
      if (named) return named;
      const w = num(Math.abs(n)) + " " + UNITS[unit][form(n)];
      return n < 0 ? w + " таму" : "праз " + w;
    },
    cur: (code) => CURRENCIES[code] || "",
  },
};
