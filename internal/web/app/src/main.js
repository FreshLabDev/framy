// framy's site and Mini App. The inline boot script has already asked for the
// catalog, the person and the lines; this module waits for them, builds the
// page and hands the skeleton over to it.
import { $, h, ic, keepFocus } from "./dom.js";
import { anim, placeSegs } from "./motion.js";
import { setTexts, t, lang, warmFormats } from "./i18n.js";
import { session, setToken, endSession } from "./api.js";
import { loadCatalog } from "./catalog.js";
import { S, initModels, useDefaults, reopenText } from "./state.js";
import { mountComposer, wireComposer, wireTuneSwipe, render, renderBar, tuneClosed } from "./composer.js";
import { mountPicker, prefetchPicker } from "./picker.js";
import { renderStage } from "./stage.js";
import { closeSheets, onSheets, toast, overOpen, closeTop, useHistory } from "./ui.js";
import { signIn, refreshMe, generate, netState, closeMini, keepPrompt } from "./run.js";
import { mountCutter, openCut } from "./clips.js";
import { mountPrefs, openPrefs, renderPrefs, prefsOpen, oauthResult, oauthBack } from "./prefs.js";
import { mountLibrary, mountViewer, mountFull, tabs, go, route, libMe } from "./library.js";
import { MARK, FATAL } from "assets:lists";

const B = window.framy;

// The lines of the page may be what did not come: these are inside app.js, in the page's language.
function fatal() {
  $("#skel").hidden = true;
  const app = $("#app"), [text, again] = FATAL[B.lang] || FATAL.en;
  app.hidden = false;
  app.replaceChildren(h("div", { class: "fatal" }, h("img", { src: MARK, alt: "", width: 48, height: 48 }), h("p", {}, text),
    h("button", { type: "button", onclick: () => location.reload() }, again)));
}

// Signed in, the person's picture opens the settings; signing out is in them.
function account() {
  const me = S.me;
  if (me) {
    const av = me.photo_url ? h("img", { class: "av", src: me.photo_url, alt: "", referrerpolicy: "no-referrer" }) : h("span", { class: "av" }, (me.name || "?").slice(0, 1).toUpperCase());
    return h("button", { type: "button", class: "me", id: "meBtn", onclick: () => openPrefs(), "aria-label": t("prefs.title"), "aria-haspopup": "dialog" }, av, h("span", { class: "t" }, t("prefs.title")));
  }
  return S.mini || S.unsure ? null : h("button", { type: "button", class: "signin", onclick: signIn }, ic("send"), h("span", {}, t("auth.signin")));
}
// The header is drawn anew on every change of view; a tab pressed with the keyboard keeps the focus.
function renderTop() {
  const top = $("#top");
  keepFocus(() => top.replaceChildren(...[h("div", { class: "logo" }, h("img", { src: MARK, alt: "" }), h("span", {}, "framy")), tabs(), h("span", { class: "sp" }), account()].filter(Boolean)));
  placeSegs(top);
}

function mount() {
  const app = $("#app");
  app.dataset.surface = "site";
  app.dataset.view = S.view;
  app.replaceChildren(
    h("div", { class: "wrap" }, h("header", { class: "top", id: "top" }),
      h("main", { class: "create", id: "create" }, mountComposer(), h("section", { class: "stage", id: "stage", "aria-label": t("stage.label") }),
        // What a screen reader hears of a run; the stage is drawn anew, so it stands beside it.
        h("p", { class: "sr", id: "stageSay", role: "status" })),
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
  let run = null, last = null;
  // The composer's button, shown only with the composer: not in the library, nor under
  // a sheet, a result on the whole screen or a question, where its run and price are out of sight.
  const sync = (nx = last) => {
    if (!nx) return;
    last = nx;
    run = nx.act === "create" ? generate : nx.act === "signin" ? signIn : nx.act === "cut" ? openCut : nx.act === "key" ? () => openPrefs(nx.provider) : nx.act === "close" ? closeMini : null;
    mb.setParams({ text: nx.label, is_active: !!nx.on, is_visible: S.view === "create" && !overOpen(), color: "#FFFFFF", text_color: "#00606E" });
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
  // Shown while anything is open over the page, the whole screen of a result included, and in the library.
  const back = () => (overOpen() || S.view === "library" ? tg.BackButton.show() : tg.BackButton.hide());
  onSheets(() => { back(); sync(); });
  addEventListener("framy:view", back);
  back();
  tg.BackButton.onClick(() => closeTop() || go("create"));
  // Telegram could not send a prepared message; closing the picker is no failure.
  tg.onEvent?.("shareMessageFailed", (e) => { if (e?.error !== "USER_DECLINED") toast(t("share.failed")); });
}

// framy did not say who is here: it is asked again, a little later each
// time, until it answers who it is or that nobody is signed in.
function findMe(wait = 2000) {
  setTimeout(async () => {
    if (!S.unsure) return;
    if (await refreshMe()) netState("live");
    else findMe(Math.min(wait * 2, 60000));
  }, wait);
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
  // Launch data framy refused cannot be traded again: the Mini App has to be opened anew.
  if (authFail?.status === 401 || authFail?.status === 400) { endSession(authFail.error); S.expired = session.dead; }
  // Only a 401 says nobody is signed in; any other failure leaves it unknown.
  try { S.me = authFail ? null : await B.me; } catch { S.me = null; S.unsure = !authFail; }
  S.signed = !!S.me;
  useDefaults();
  // The person's own language, where it is another than the one guessed for the first paint.
  const want = S.me?.language;
  if (want && want !== B.lang && B.langs[want]) {
    try { setTexts(want, await fetch(B.langs[want]).then((r) => r.json())); } catch {}
  }
  // Kept for the first paint of the next load: the account's language only, never a guess.
  if (S.me) try { localStorage.setItem("framy.language", lang); } catch {}
  await B.css;
  mount();
  renderTop();
  render();
  reveal();
  // The address names the view: /library, a run of it at /r/<id>, or the composer.
  addEventListener("framy:view", renderTop);
  useHistory(!S.mini);
  route();

  const sp = new URLSearchParams(location.search), si = sp.get("sign_in");
  if (si) { toast(t("auth." + (["cancelled", "expired", "failed"].includes(si) ? si : "failed"))); history.replaceState(null, "", location.pathname + location.hash); }
  // Back from OpenRouter: the address says how the connect ended.
  if (sp.get("connect") === "openrouter") { history.replaceState(null, "", location.pathname + location.hash); oauthResult(sp.get("result") || ""); }
  // Reloaded for a new language from the settings: they open again.
  let back = null;
  try { back = sessionStorage.getItem("framy.prefs"); sessionStorage.removeItem("framy.prefs"); } catch {}
  if (back && S.signed && !sp.get("connect")) openPrefs();
  if (authFail) toast(S.expired ? reopenText() : t("err.network"), { ms: 8000 });
  if (S.unsure) { netState("lost"); findMe(); }
  // Opened too late, with what was kept from the last time in the prompt: kept again for the next open.
  if (S.expired) keepPrompt();
  // The session ends while the Mini App is open, an hour or so after Telegram opened it:
  // the page says so once, and its button closes the Mini App to be opened again.
  addEventListener("framy:expired", (e) => {
    S.expired = e.detail; S.me = null; S.signed = false;
    keepPrompt();
    toast(reopenText(), { ms: 8000 });
    window.dispatchEvent(new Event("framy:me"));
  });

  addEventListener("framy:me", () => { renderTop(); render(); libMe(); if (prefsOpen()) { if (S.signed) renderPrefs(); else closeSheets(true); } });
  // Back on the page after a while: the free counts and the keys may have changed elsewhere.
  let hidAt = 0;
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") { hidAt = Date.now(); return; }
    // Back in Telegram from OpenRouter in the browser: the key is looked for at once.
    if (oauthBack()) return;
    if (S.signed && hidAt && Date.now() - hidAt > 30000) refreshMe();
  });
  addEventListener("online", () => { S.online = true; netState("live"); renderBar(); if (S.unsure) refreshMe(); });
  addEventListener("offline", () => { S.online = false; netState("lost"); renderBar(); });
  (window.requestIdleCallback || setTimeout)(warmFormats);
  prefetchPicker();
  B.tg.then(telegram);
}

main();
