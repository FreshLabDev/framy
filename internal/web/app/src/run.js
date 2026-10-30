// Signing in and out, and the press of Create: the photos go up, the job
// starts (POST /api/jobs) and its events come to the stage.
import { $ } from "./dom.js";
import { t, fmtTime, fmtIn } from "./i18n.js";
import { api, watchJob, session, isFinal } from "./api.js";
import { S, M, payOf, settings, route, runItem, freesAt } from "./state.js";
import { C, offerOf, PNAME } from "./catalog.js";
import { known, priceText, clearPrices } from "./price.js";
import { render, renderBar } from "./composer.js";
import { showJob } from "./stage.js";
import { upload } from "./files.js";
import { toast, ask } from "./ui.js";

export function signIn() {
  if (S.mini) return;
  try { sessionStorage.setItem("framy.draft", $("#prompt").value); } catch {}
  location.assign("/auth/login?return_to=" + encodeURIComponent("/"));
}

export async function signOut() {
  const yes = await ask({ title: t("auth.signout_q"), yes: t("auth.signout") });
  if (!yes) return;
  try { await api("/auth/logout", { method: "POST" }); } catch {}
  S.signed = false; S.me = null; S.job = null;
  clearPrices();
  window.dispatchEvent(new Event("framy:me"));
  toast(t("auth.signed_out"));
}

export async function refreshMe() {
  try {
    const me = await api("/api/me");
    const was = S.me?.currency;
    S.me = me; S.signed = true;
    if (was && was !== me.currency) clearPrices();
  } catch (e) {
    if (e.status === 401) { S.me = null; S.signed = false; }
  }
  window.dispatchEvent(new Event("framy:me"));
}

let press = null; // the idempotency key of the press being sent, kept for a retry of the same press
let stopWatch = null;

export async function generate() {
  const m = M();
  if (!m || S.busy) return;
  const pay = payOf(m), set = settings(m, pay), o = offerOf(m, pay), prompt = $("#prompt").value.trim();
  const r = route(m, pay), files = S.files.filter((f, i) => r[i]?.go);
  const q = known(runItem(m, pay, set)), e = q?.estimate;
  S.busy = true; renderBar();
  press ||= "site-" + crypto.randomUUID();
  try {
    let inputs = [];
    if (files.length) {
      try { inputs = await upload(files); } catch (err) { throw uploadError(err); }
    }
    const body = { offer: o.id, prompt, settings: set, inputs, pay: pay === "free" ? "free" : "key", surface: S.mini ? "mini_app" : "site" };
    if (pay !== "free" && e && e.kind !== "unknown") body.shown_usd = e.kind === "up_to" ? e.max_usd : e.usd;
    const job = await api("/api/jobs", { method: "POST", body, headers: { "Idempotency-Key": press } });
    press = null;
    start(job);
    if (pay === "free") refreshMe();
  } catch (err) {
    press = err.status >= 500 || err.status === 0 ? press : null;
    refused(err, m);
  } finally {
    S.busy = false;
    renderBar();
  }
}

function start(job) {
  stopWatch?.();
  showJob(job);
  stopWatch = watchJob(job.id, (j) => {
    showJob(j);
    if (isFinal(j.status)) { refreshMe(); }
  }, () => {});
}

function uploadError(err) {
  if (err.code === "storage_unavailable") return Object.assign(err, { text: t("err.storage") });
  if (err.code === "picture_too_large" || err.code === "too_large") return Object.assign(err, { text: t("err.too_large") });
  if (err.code === "unsupported_type" || err.code === "unreadable") return Object.assign(err, { text: t("err.unreadable") });
  if (err.code === "network" || err.code === "http_429" || err.code === "busy") return err;
  return Object.assign(err, { text: t("err.upload") });
}

// A run the server did not take: say why in plain words.
function refused(err, m) {
  const b = err.body || {};
  let text = err.text;
  switch (err.code) {
    case "free_limit": text = b.frees_at ? t("err.free_limit", { when: fmtIn(Date.parse(b.frees_at)) }) : t("err.free_out"); if (S.me?.free) refreshMe(); break;
    case "free_closed": S.closed[m.kind] = Date.parse(b.until) || 0; text = t("err.free_closed", { time: fmtTime(S.closed[m.kind]) }); break;
    case "not_free": text = t("err.not_free"); break;
    case "no_connection": case "connection_invalid": text = t("err.no_key", { provider: PNAME[offerOf(m, payOf(m)).provider] || "" }); break;
    case "price_changed": text = t("err.price_changed", { p: priceText(b.estimate) }); clearPrices(); render(); break;
    case "wrong_inputs": case "unknown_input": text = t("err.wrong_inputs"); break;
    case "unknown_offer": text = t("err.unknown_offer"); break;
    case "http_429": case "busy": case "shutting_down": text = t("err.busy"); break;
    case "sign_in": S.signed = false; S.me = null; window.dispatchEvent(new Event("framy:me")); text = t("err.sign_in"); break;
    case "network": text = t("err.network"); break;
  }
  toast(text || t("err.generic", { code: err.code }), { ms: 6000 });
}
