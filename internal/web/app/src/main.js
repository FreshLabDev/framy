// framy's site and Mini App. The inline boot script has already asked for the
// catalog, the person and the lines; this module waits for them, builds the
// page and hands the skeleton over to it.
import { $, h, ic } from "./dom.js";
import { anim, placeSegs } from "./motion.js";
import { setTexts, t, lang, warmFormats } from "./i18n.js";
import { session, setToken } from "./api.js";
import { loadCatalog } from "./catalog.js";
import { S, initModels } from "./state.js";
import { mountComposer, wireComposer, render, renderBar, tuneClosed } from "./composer.js";
import { mountPicker, prefetchPicker } from "./picker.js";
import { renderStage } from "./stage.js";
import { closeSheets, onSheets, toast, sheetOpen } from "./ui.js";
import { signIn, signOut, refreshMe, generate } from "./run.js";
import { clearPrices } from "./price.js";

const B = window.framy;

function fatal() {
  $("#skel").hidden = true;
  const app = $("#app");
  app.hidden = false;
  app.replaceChildren(h("div", { class: "fatal" }, h("p", {}, "framy could not load. Check the connection and try again."),
    h("button", { type: "button", onclick: () => location.reload() }, "Try again")));
}

function account() {
  const me = S.me;
  if (me) {
    const av = me.photo_url ? h("img", { class: "av", src: me.photo_url, alt: "", referrerpolicy: "no-referrer" }) : h("span", { class: "av" }, (me.name || "?").slice(0, 1).toUpperCase());
    return h("button", { type: "button", class: "me", onclick: signOut, title: t("auth.signout") }, av, h("span", { class: "t" }, me.name || t("auth.you")));
  }
  return h("button", { type: "button", class: "signin", onclick: signIn }, ic("send"), t("auth.signin"));
}
function renderTop() { $("#top").replaceChildren(h("div", { class: "logo" }, "framy"), h("span", { class: "sp" }), S.mini ? null : account()); }

function mount() {
  const app = $("#app");
  app.dataset.surface = "site";
  app.replaceChildren(
    h("div", { class: "wrap" }, h("header", { class: "top", id: "top" }),
      h("main", { class: "create", id: "create" }, mountComposer(), h("section", { class: "stage", id: "stage", "aria-label": t("stage.label") }))),
    h("div", { class: "scrim", id: "scrim", hidden: true, onclick: () => closeSheets() }),
    mountPicker(),
    h("section", { class: "sheet half", id: "tune", hidden: true, "aria-label": t("tune.title"), role: "dialog", "aria-modal": "true" },
      h("header", {}, h("h3", {}, t("tune.title")), h("button", { type: "button", class: "x", "data-close": "" }, t("pick.done"))),
      h("div", { class: "body", id: "tuneBody" })),
    h("div", { class: "ask", id: "ask", hidden: true, role: "alertdialog", "aria-modal": "true", "aria-labelledby": "askT" }));
  app.addEventListener("click", (e) => { if (e.target.closest("[data-close]")) closeSheets(); });
  $("#tune").addEventListener("closed", tuneClosed);
  wireComposer();
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
    run = nx.act === "create" ? generate : nx.act === "signin" ? signIn : null;
    mb.setParams({ text: nx.label, is_active: !!nx.on, is_visible: true, color: "#FFFFFF", text_color: "#00606E" });
  };
  addEventListener("framy:button", (e) => sync(e.detail));
  mb.onClick(() => run?.());
  renderBar();
  onSheets((open) => { if (open) tg.BackButton.show(); else tg.BackButton.hide(); });
  tg.BackButton.onClick(() => closeSheets());
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

  const sp = new URLSearchParams(location.search), si = sp.get("sign_in");
  if (si) { toast(t("auth." + (["cancelled", "expired", "failed"].includes(si) ? si : "failed"))); history.replaceState(null, "", location.pathname + location.hash); }
  if (authFail) toast(t(authFail.error === "init_data_expired" ? "auth.mini_expired" : "auth.mini_failed"), { ms: 8000 });

  addEventListener("framy:me", () => { renderTop(); render(); });
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible" && S.signed) refreshMe(); });
  addEventListener("online", () => { S.online = true; renderBar(); });
  addEventListener("offline", () => { S.online = false; renderBar(); });
  (window.requestIdleCallback || setTimeout)(warmFormats);
  prefetchPicker();
  B.tg.then(telegram);
}

main();
