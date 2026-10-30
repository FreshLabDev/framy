// Why a run did not give a result, in the person's words: a job that failed
// (its error.class, docs/design.md, Jobs), and a press the server refused
// before any job existed. Each says what happened to the money and which
// buttons fit. The English message of a job is shown only where it is the
// provider's own reason.
import { t, fmtTime, fmtIn, fmtSecs } from "./i18n.js";
import { PNAME } from "./catalog.js";
import { priceText } from "./price.js";

// The provider may have charged for these: the request reached it.
export const MAYPAY = new Set(["timeout", "provider", "interrupted", "storage"]);
// The provider's own words, worth showing; the other classes carry framy's.
const OWNWORDS = new Set(["moderation", "refused", "provider"]);
// Where a key's balance is topped up.
export const BILLING = {
  openrouter: "https://openrouter.ai/settings/credits",
  openai: "https://platform.openai.com/settings/organization/billing/overview",
  gemini: "https://console.cloud.google.com/billing",
};

export const providerOf = (j) => (j.free ? "openrouter" : String(j.offer || "").split(":")[0]);

// failure says why job j failed: label (on the frame), text, note (the money)
// and msg (the provider's reason), and acts, the buttons that fit.
export function failure(j) {
  const cls = j.error?.class || "", p = providerOf(j), P = PNAME[p] || p, free = j.free, vid = j.kind === "video";
  const back = t(free ? "fail.free_back" : "fail.nothing");
  // The most the run could have cost: the label of the estimate, which for "up to" is its top.
  const up = j.estimate && j.estimate.kind !== "unknown" ? j.estimate.label : "";
  const may = free ? back : up ? t("fail.may_charge", { p: up, provider: P }) : t("fail.may_charge_some", { provider: P });
  const msg = OWNWORDS.has(cls) && j.error?.message ? j.error.message.replace(/<link>/g, "").trim() : "";
  const again = ["retry", "edit"];
  if (j.status === "cancelled") return { cls: "cancelled", label: t("stage.cancelled"), text: "", note: back, acts: again };
  switch (cls) {
    case "moderation": return { cls, label: t("fail.moderation_l"), text: t("fail.moderation"), note: t(free ? "fail.free_spent" : "fail.nothing"), msg, acts: ["edit"] };
    case "money": return free
      ? { cls, label: t("fail.free_closed_l"), text: t("fail.free_money"), note: t("fail.free_kept"), acts: ["bykey"], closeFree: 15 * 60e3 }
      : { cls, label: t("fail.money_l"), text: t("fail.money", { provider: P }), note: t("fail.nothing"), acts: ["topup"], provider: p };
    case "key": return free
      ? { cls, label: t("fail.free_off_l"), text: t("fail.free_key"), note: t("fail.free_kept"), acts: ["bykey"] }
      : { cls, label: t("fail.key_l"), text: t("fail.key", { provider: P }), note: t("fail.nothing"), acts: [], keyGone: true };
    case "refused": return { cls, label: t("fail.refused_l"), text: t("fail.refused", { provider: P }), note: back, msg, acts: ["edit"] };
    case "rate_limited": case "unavailable": return { cls, label: t("fail.no_answer_l"), text: t("fail.no_answer", { provider: P }), note: back + " " + t("fail.later"), acts: again };
    case "timeout": return { cls, label: t("fail.timeout_l"), text: t("fail.timeout"), note: may, acts: again };
    case "provider": return { cls, label: t("fail.provider_l"), text: t("fail.provider", { provider: P }), note: may, msg, acts: again };
    case "interrupted": return { cls, label: t("fail.interrupted_l"), text: t("fail.interrupted"), note: may, acts: again };
    case "storage": return { cls, label: t("fail.storage_l"), text: t(vid ? "fail.storage_video" : "fail.storage"), note: t(free ? "fail.free_back" : "fail.charged"), acts: again };
    case "no_adapter": return { cls, label: t("stage.failed"), text: t("fail.no_adapter", { provider: P }), note: back, acts: ["edit"] };
    case "draft_expired": return { cls, label: t("stage.expired"), text: t("fail.draft_expired"), note: back, acts: again };
    default: return { cls: cls || "unknown", label: t(j.status === "expired" ? "stage.expired" : "stage.failed"), text: t("fail.unknown"), note: "", acts: again };
  }
}

// waiting says why a queued job waits to go again: a rate limit or a
// provider that did not answer, until retry_at.
export function waiting(j) {
  const cls = j.error?.class;
  if (j.status !== "queued" || !j.retry_at || (cls !== "rate_limited" && cls !== "unavailable")) return null;
  const P = PNAME[providerOf(j)] || "";
  return { at: Date.parse(j.retry_at), line: (left) => t(cls === "rate_limited" ? "wait.rate" : "wait.down", { provider: P, left }) };
}

// refusal says why the server refused a press before any job: the text, the
// label on the frame and the buttons. ctx is what the press was: kind, the
// file a code names, and the price shown.
export function refusal(err, ctx = {}) {
  const b = err.body || {}, vid = ctx.kind === "video", code = err.code;
  const R = (label, text, acts = [], more = {}) => ({ code, label, text, acts, ...more });
  switch (code) {
    case "price_changed": return R(t("ref.price_l"), t("ref.price", { p: priceText(b.estimate), was: ctx.shown || "" }), ["takeprice", "drop"], { estimate: b.estimate });
    case "free_limit": return R(t("ref.free_out_l"), b.frees_at ? t(vid ? "ref.free_taken_video" : "ref.free_taken", { when: fmtIn(Date.parse(b.frees_at)) }) : t("err.free_out"), ["bykey"]);
    case "free_closed": return R(t("ref.free_closed_l"), t("ref.free_closed", { time: fmtTime(Date.parse(b.until) || Date.now()) }), ["bykey"], { until: Date.parse(b.until) || 0 });
    case "not_free": return R(t("ref.not_free_l"), t("err.not_free"));
    case "no_connection": case "connection_invalid": return R(t("ref.key_l"), t("err.no_key", { provider: PNAME[b.provider] || ctx.provider || "" }));
    case "wrong_inputs": case "unknown_input": return R(t("ref.files_l"), t("ref.wrong_inputs"));
    case "unknown_offer": return R(t("ref.model_l"), t("err.unknown_offer"));
    case "cannot_continue": return R(t("ref.cont_l"), t("ref.cannot_continue"), ctx.lastFrame ? ["lastframe"] : []);
    case "video_too_long": return R(t("ref.too_long_l"), t("ref.too_long", { len: clock(b.seconds) }), ["cut"], { seconds: b.seconds });
    case "not_a_video": return R(t("ref.not_clip_l"), t("ref.not_a_video"));
    case "unsupported_type": return ctx.clip ? R(t("ref.clip_bad_l"), t("files.clip_type")) : R(t("ref.file_bad_l"), t("ref.not_a_picture", { name: ctx.name || "" }));
    case "unreadable": return ctx.clip ? R(t("ref.clip_bad_l"), t("ref.clip_unreadable")) : R(t("ref.file_bad_l"), t("err.unreadable"));
    case "frame_too_large": return R(t("ref.clip_bad_l"), t("ref.frame_too_large"));
    case "too_large": case "picture_too_large": return ctx.clip ? R(t("ref.clip_bad_l"), t("files.clip_big", { n: Math.round((ctx.maxBytes || 536870912) / 1048576) })) : R(t("ref.file_bad_l"), t("err.too_large"));
    case "storage_unavailable": return R(t("ref.storage_l"), t("ref.storage"), ["again"]);
    case "shutting_down": return R(t("ref.restart_l"), t("ref.restart"), ["again"]);
    case "network": return R(t("ref.offline_l"), t("err.network"), ["again"]);
    case "http_429": case "busy": return R(t("ref.busy_l"), t("err.busy"), ["again"]);
    case "sign_in": return R(t("ref.signin_l"), t("err.sign_in"));
    default: return R(t("stage.failed"), t("err.generic", { code }), ["again"]);
  }
}

// 134 seconds as 2:14.
export const clock = (s) => { const n = Math.max(0, Math.round(+s || 0)); return Math.floor(n / 60) + ":" + String(n % 60).padStart(2, "0"); };
export const left = (ms) => clock(Math.max(0, Math.ceil((ms - Date.now()) / 1000)));
export { fmtSecs };
