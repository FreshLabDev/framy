// framy's site and Mini App. The inline boot script has already asked for the
// catalog, the person and the lines; this module waits for them, builds the
// page and hands the skeleton over to it.
import { $, h, ic } from "./dom.js";
import { anim, placeSegs } from "./motion.js";
import { setTexts, t, lang, warmFormats } from "./i18n.js";
import { session, setToken } from "./api.js";
import { loadCatalog } from "./catalog.js";
import { S, initModels, useDefaults } from "./state.js";
import { mountComposer, wireComposer, wireTuneSwipe, render, renderBar, tuneClosed } from "./composer.js";
import { mountPicker, prefetchPicker } from "./picker.js";
import { renderStage } from "./stage.js";
import { closeSheets, onSheets, toast, sheetOpen } from "./ui.js";
import { signIn, refreshMe, generate, netState } from "./run.js";
import { mountCutter, openCut } from "./clips.js";
import { mountPrefs, openPrefs, renderPrefs, prefsOpen, oauthResult, oauthBack } from "./prefs.js";
import { mountLibrary, mountViewer, mountFull, fullOpen, closeFull, tabs, go, route, libMe } from "./library.js";

const B = window.framy;

function fatal() {
  $("#skel").hidden = true;
  const app = $("#app");
  app.hidden = false;
  app.replaceChildren(h("div", { class: "fatal" }, h("p", {}, "framy could not load. Check the connection and try again."),
    h("button", { type: "button", onclick: () => location.reload() }, "Try again")));
}

// Signed in, the person's picture opens the settings; signing out is in them.
function account() {
  const me = S.me;
  if (me) {
    const av = me.photo_url ? h("img", { class: "av", src: me.photo_url, alt: "", referrerpolicy: "no-referrer" }) : h("span", { class: "av" }, (me.name || "?").slice(0, 1).toUpperCase());
    return h("button", { type: "button", class: "me", id: "meBtn", onclick: () => openPrefs(), "aria-label": t("prefs.title"), "aria-haspopup": "dialog" }, av, h("span", { class: "t" }, t("prefs.title")));
  }
  return S.mini ? null : h("button", { type: "button", class: "signin", onclick: signIn }, ic("send"), t("auth.signin"));
}
function renderTop() {
  const top = $("#top");
  top.replaceChildren(...[h("div", { class: "logo" }, "framy"), tabs(), h("span", { class: "sp" }), account()].filter(Boolean));
  placeSegs(top);
}

function mount() {
  const app = $("#app");
  app.dataset.surface = "site";
  app.dataset.view = S.view;
  app.replaceChildren(
    h("div", { class: "wrap" }, h("header", { class: "top", id: "top" }),
      h("main", { class: "create", id: "create" }, mountComposer(), h("section", { class: "stage", id: "stage", "aria-label": t("stage.label") })),
      mountLibrary()),
    h("div", { class: "scrim", id: "scrim", hidden: true, onclick: () => closeSheets() }),
    mountPicker(),
    h("section", { class: "sheet half", id: "tune", hidden: true, "aria-label": t("tune.title"), role: "dialog", "aria-modal": "true" },
      h("header", {}, h("h3", { id: "tuneTitle" }, t("tune.title")), h("button", { type: "button", class: "x", "data-close": "" }, t("pick.done"))),
      h("div", { class: "body", id: "tuneBody" })),
    mountCutter(),
    mountPrefs(),
    mountViewer(),
    mountFull(),
    h("div", { class: "ask", id: "ask", hidden: true, role: "alertdialog", "aria-modal": "true", "aria-labelledby": "askT" }));
  app.addEventListener("click", (e) => { if (e.target.closest("[data-close]")) closeSheets(); });
  $("#tune").addEventListener("closed", tuneClosed);
  wireComposer();
  wireTuneSwipe();
}

function reveal() {
  const sk = $("#skel"), app = $("#app");
  app.hidden = false;
  placeSegs(app);
  renderStage();
  sk.hidden = true;
  anim(app, [{ opacity: 0 }, { opacity: 1 }], { duration: 260 });
}

// In Telegram: its colours, its main button for Create and its back button for sheets.
function telegram(tg) {
  if (!tg) return;
  S.tg = tg;
  $("#app").dataset.surface = "mini";
  try {
    tg.ready(); tg.expand();
    tg.setHeaderColor?.("#00606E"); tg.setBackgroundColor?.("#00606E"); tg.setBottomBarColor?.("#00606E");
    tg.disableVerticalSwipes?.();
  } catch {}
  const mb = tg.MainButton;
  let run = null;
  const sync = (nx) => {
    if (!nx) return;
    run = nx.act === "create" ? generate : nx.act === "signin" ? signIn : nx.act === "cut" ? openCut : nx.act === "key" ? () => openPrefs(nx.provider) : null;
    mb.setParams({ text: nx.label, is_active: !!nx.on, is_visible: S.view === "create", color: "#FFFFFF", text_color: "#00606E" });
  };
  addEventListener("framy:button", (e) => sync(e.detail));
  addEventListener("framy:view", () => renderBar());
  mb.onClick(() => run?.());
  renderBar();
  // Telegram's own Settings in the menu of the Mini App.
  if (tg.SettingsButton && tg.isVersionAtLeast?.("7.0")) {
    tg.SettingsButton.onClick(() => openPrefs());
    const sb = () => (S.signed ? tg.SettingsButton.show() : tg.SettingsButton.hide());
    sb(); addEventListener("framy:me", sb);
  }
  const back = () => (sheetOpen() || S.view === "library" ? tg.BackButton.show() : tg.BackButton.hide());
  onSheets(back);
  addEventListener("framy:view", back);
  back();
  tg.BackButton.onClick(() => (fullOpen() ? closeFull() : sheetOpen() ? closeSheets() : go("create")));
  // Telegram could not send a prepared message; closing the picker is no failure.
  tg.onEvent?.("shareMessageFailed", (e) => { if (e?.error !== "USER_DECLINED") toast(t("share.failed")); });
}

async function main() {
  session.mini = B.mini;
  session.initData = B.initData;
  S.mini = B.mini;
  let texts, cat;
  try { [texts, cat] = await Promise.all([B.texts, B.catalog]); } catch { fatal(); return; }
  setTexts(B.lang, texts);
  loadCatalog(cat);
  initModels();
  let authFail = null;
  try { const a = await B.auth; if (a) setToken(a); } catch (e) { authFail = e; }
  try { S.me = authFail ? null : await B.me; } catch { S.me = null; }
  S.signed = !!S.me;
  useDefaults();
  // The person's own language, where it is another than the one guessed for the first paint.
  const want = S.me?.language;
  if (want && want !== B.lang && B.langs[want]) {
    try { setTexts(want, await fetch(B.langs[want]).then((r) => r.json())); } catch {}
  }
  try { localStorage.setItem("framy.lang", lang); } catch {}
  await B.css;
  mount();
  renderTop();
  render();
  reveal();
  // The address names the view: /library, a run of it at /r/<id>, or the composer.
  addEventListener("framy:view", renderTop);
  route();

  const sp = new URLSearchParams(location.search), si = sp.get("sign_in");
  if (si) { toast(t("auth." + (["cancelled", "expired", "failed"].includes(si) ? si : "failed"))); history.replaceState(null, "", location.pathname + location.hash); }
  // Back from OpenRouter: the address says how the connect ended.
  if (sp.get("connect") === "openrouter") { history.replaceState(null, "", location.pathname + location.hash); oauthResult(sp.get("result") || ""); }
  // Reloaded for a new language from the settings: they open again.
  let back = null;
  try { back = sessionStorage.getItem("framy.prefs"); sessionStorage.removeItem("framy.prefs"); } catch {}
  if (back && S.signed && !sp.get("connect")) openPrefs();
  if (authFail) toast(t(authFail.error === "init_data_expired" ? "auth.mini_expired" : "auth.mini_failed"), { ms: 8000 });

  addEventListener("framy:me", () => { renderTop(); render(); libMe(); if (prefsOpen()) { if (S.signed) renderPrefs(); else closeSheets(true); } });
  // Back on the page after a while: the free counts and the keys may have changed elsewhere.
  let hidAt = 0;
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") { hidAt = Date.now(); return; }
    // Back in Telegram from OpenRouter in the browser: the key is looked for at once.
    if (oauthBack()) return;
    if (S.signed && hidAt && Date.now() - hidAt > 30000) refreshMe();
  });
  addEventListener("online", () => { S.online = true; netState("live"); renderBar(); });
  addEventListener("offline", () => { S.online = false; netState("lost"); renderBar(); });
  (window.requestIdleCallback || setTimeout)(warmFormats);
  prefetchPicker();
  B.tg.then(telegram);
}

main();
