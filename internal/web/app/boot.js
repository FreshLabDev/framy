// The first script of the page, inline: it starts everything the composer
// waits for at once, while the skeleton paints and app.js downloads. The
// catalog, the person, the lines of their language and the stylesheet come in
// parallel; app.js picks the promises up from window.framy.
// TEXTS (language -> URL of its lines) is filled in by the build.
(() => {
  const langs = TEXTS;
  // In the Mini App Telegram hands the signed launch data in the fragment.
  const frag = new URLSearchParams(location.hash.slice(1));
  let initData = frag.get("tgWebAppData") || "";
  try {
    if (initData) sessionStorage.setItem("framy.init", initData);
    else initData = sessionStorage.getItem("framy.init") || "";
  } catch {}
  const mini = !!initData;
  let tgUser = null;
  try { tgUser = JSON.parse(new URLSearchParams(initData).get("user") || "null"); } catch {}

  // The language: the one the person chose here, else Telegram's, else the browser's.
  const pick = (code) => { code = String(code || "").toLowerCase().split(/[-_]/)[0]; return langs[code] ? code : ""; };
  let saved = "";
  try { saved = localStorage.getItem("framy.lang") || ""; } catch {}
  const lang = pick(saved) || (tgUser && pick(tgUser.language_code)) || (navigator.languages || [navigator.language]).map(pick).find(Boolean) || "en";
  document.documentElement.lang = lang;
  if (lang === "ar") document.documentElement.dir = "rtl";

  const json = (r) => (r.ok ? r.json() : Promise.reject(r));
  const texts = fetch(langs[lang]).then(json);
  const catalog = fetch("/api/catalog").then(json);

  // The site signs in with a cookie; the Mini App trades its launch data for a
  // short bearer token first.
  const auth = mini
    ? fetch("/auth/miniapp", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ init_data: initData }) })
        .then((r) => (r.ok ? r.json() : r.json().then((e) => Promise.reject({ status: r.status, error: e.error }), () => Promise.reject({ status: r.status }))))
    : Promise.resolve(null);
  const me = auth.then((a) => fetch("/api/me", { headers: a ? { Authorization: "Bearer " + a.token } : {} }))
    .then((r) => (r.status === 401 ? null : json(r)));

  // The stylesheet downloads without blocking the skeleton and applies once it is here.
  const css = new Promise((done) => {
    const l = document.getElementById("css");
    const on = () => { l.media = "all"; done(); };
    if (l.sheet) on(); else { l.addEventListener("load", on); l.addEventListener("error", on); }
  });

  // Telegram's own script only where it is needed: inside Telegram.
  const tg = mini ? new Promise((done) => {
    const s = document.createElement("script");
    s.src = "https://telegram.org/js/telegram-web-app.js";
    s.onload = s.onerror = () => done(window.Telegram && window.Telegram.WebApp);
    document.head.append(s);
  }) : Promise.resolve(null);

  for (const p of [texts, catalog, auth, me]) p.catch(() => {});
  window.framy = { lang, langs, mini, initData, texts, catalog, auth, me, css, tg };
})();
