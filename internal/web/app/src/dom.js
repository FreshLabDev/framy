// The page is built from plain elements. h() makes one; styles go through
// CSSOM, which the policy allows, never through a style attribute.

export const $ = (s, r = document) => r.querySelector(s);
export const $$ = (s, r = document) => [...r.querySelectorAll(s)];

export function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "style") {
      for (const [p, x] of Object.entries(v)) if (p.startsWith("--")) el.style.setProperty(p, x); else el.style[p] = x;
    } else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const k of kids.flat(4)) if (k != null && k !== false && k !== "") el.append(k.nodeType ? k : String(k));
  return el;
}

export const isPhone = () => !matchMedia("(min-width: 960px)").matches;
export const isWide = () => matchMedia("(min-width: 900px)").matches;
export const MAC = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

// Icons: one 20 grid, straight edges, filled, like the rest of Asterfield.
const ICONS = {
  image: "M1 3H19V17H1ZM3 5V15H17V5ZM4 14L8 9L11 12L13 10L16 14Z",
  video: "M1 3H19V17H1ZM3 5V15H17V5ZM8 7L13 10L8 13Z",
  bolt: "M12 1H14L11 8H17L8 19H6L9 11H3Z",
  search: "M1 8A7 7 0 1 1 15 8A7 7 0 1 1 1 8ZM3 8A5 5 0 1 0 13 8A5 5 0 1 0 3 8ZM12.6 11.2L19 17.6L17.6 19L11.2 12.6Z",
  photos: "M5 1H19V15H5ZM7 3V13H17V3ZM8 12L11 8L13 10L14 9L16 12ZM1 5H3V17H15V19H1Z",
  res: "M1 1H8V3H4.5L8 6.5L6.5 8L3 4.5V8H1ZM19 19H12V17H15.5L12 13.5L13.5 12L17 15.5V12H19ZM12 1H19V8H17V4.5L13.5 8L12 6.5L15.5 3H12ZM8 19H1V12H3V15.5L6.5 12L8 13.5L4.5 17H8Z",
  ratio: "M1 4H19V16H1ZM3 6V14H17V6Z",
  clock: "M0 0H20V20H0ZM2.5 2.5V17.5H17.5V2.5ZM9 4H11V9H15V11H9Z",
  audio: "M1 7H5L10 3V17L5 13H1ZM12 7H14V13H12ZM16 4H18V16H16Z",
  mute: "M1 7H5L10 3V17L5 13H1ZM12 6.5L13.5 5L15.5 7L17.5 5L19 6.5L17 8.5L19 10.5L17.5 12L15.5 10L13.5 12L12 10.5L14 8.5Z",
  frames: "M1 4H8V16H1ZM3 6V14H6V6ZM12 4H19V16H12ZM14 6V14H17V6ZM9 9H11V11H9Z",
  film: "M2 1H18V19H2ZM4 3V5H6V3ZM4 7V9H6V7ZM4 11V13H6V11ZM4 15V17H6V15ZM14 3V5H16V3ZM14 7V9H16V7ZM14 11V13H16V11ZM14 15V17H16V15ZM8 3V17H12V3Z",
  vector: "M1 14H6V19H1ZM14 1H19V6H14ZM6 13L13 6L14 7L7 14Z",
  checker: "M1 1H19V19H1ZM3 3V10H10V3ZM10 10V17H17V10Z",
  stack: "M5 1H19V15H5ZM7 3V13H17V3ZM1 5H3V17H15V19H1Z",
  layers: "M10 1L19 5.5L10 10L1 5.5ZM1 9.5L3 8.5L10 12L17 8.5L19 9.5L10 14ZM1 13.5L3 12.5L10 16L17 12.5L19 13.5L10 18Z",
  ticket: "M1 4H19V8H17V12H19V16H1V12H3V8H1ZM9 6V8H11V6ZM9 9V11H11V9ZM9 12V14H11V12Z",
  key: "M1 5H9V15H1ZM3 7V13H7V7ZM9 9H19V11H17V14H15V11H13V13H11V11H9Z",
  bars: "M1 13H5V19H1ZM8 8H12V19H8ZM15 2H19V19H15Z",
  plus: "M8 1H12V8H19V12H12V19H8V12H1V8H8Z",
  grid: "M1 1H8V8H1ZM12 1H19V8H12ZM1 12H8V19H1ZM12 12H19V19H12Z",
  sliders: "M1 4H10V6H1ZM15 4H19V6H15ZM10 2H15V8H10ZM1 14H4V16H1ZM9 14H19V16H9ZM4 12H9V18H4Z",
  down: "M8 1H12V9H16L10 15L4 9H8ZM1 17H19V19H1Z",
  info: "M0 0H20V20H0ZM2 2V18H18V2ZM9 8H11V15H9ZM9 4H11V6H9Z",
  play: "M4 2L18 10L4 18Z",
  stop: "M3 3H17V17H3Z",
  send: "M1 9L19 1L13 19L10 11Z",
  out: "M1 1H11V5H9V3H3V17H9V15H11V19H1ZM13 6L19 10L13 14V11H6V9H13Z",
  chev: "M7 3L14 10L7 17L5 15L10 10L5 5Z",
  chevd: "M3 7L10 14L17 7L15 5L10 10L5 5Z",
  close: "M4 1.5L10 7.5L16 1.5L18.5 4L12.5 10L18.5 16L16 18.5L10 12.5L4 18.5L1.5 16L7.5 10L1.5 4Z",
  spark: "M10 0L12.5 7.5L20 10L12.5 12.5L10 20L7.5 12.5L0 10L7.5 7.5Z",
  coin: "M3 1H17V19H3ZM5 3V17H15V3ZM9 5H11V7H13V9H9V10H13V15H11V17H9V15H7V13H11V12H7V7H9Z",
  sort: "M1 3H19V6H1ZM1 9H13V12H1ZM1 15H7V18H1Z",
  warn: "M10 1L19 18H1ZM9 7V12H11V7ZM9 14V16H11V14Z",
  expand: "M1 1H8V3H3V8H1ZM12 1H19V8H17V3H12ZM1 12H3V17H8V19H1ZM17 12H19V19H12V17H17Z",
};
const NS = "http://www.w3.org/2000/svg";
export function ic(name, cls) {
  const s = document.createElementNS(NS, "svg");
  s.setAttribute("viewBox", "0 0 20 20");
  s.setAttribute("aria-hidden", "true");
  s.setAttribute("class", "ic " + name + (cls ? " " + cls : ""));
  const p = document.createElementNS(NS, "path");
  p.setAttribute("fill-rule", "evenodd");
  p.setAttribute("d", ICONS[name] || ICONS.info);
  s.append(p);
  return s;
}
