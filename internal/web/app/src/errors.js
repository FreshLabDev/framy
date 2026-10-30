// Why a run did not give a result, in the person's words: a job that failed
// (its error.class, docs/design.md, Jobs), and a press the server refused
// before any job existed. Each says what happened to the money and which
// buttons fit. The English message of a job is shown only where it is the
// provider's own reason.
import { t, fmtTime, fmtIn, fmtSecs, fmtNum } from "./i18n.js";
import { C, PNAME, vname } from "./catalog.js";
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

// The provider's words without framy's wrapping: "refused: openrouter 400:
// HTTP 400: {json}" reads as the message inside the JSON, without its request id.
export function ownWords(s) {
  let m = String(s || "").replace(/<link>/g, "").trim()
    .replace(/^(?:refused|moderation|provider):\s*/i, "").replace(/^(?:openrouter|openai|gemini)\s+\d{3}:\s*/i, "").replace(/^HTTP \d{3}:\s*/i, "");
  if (m.startsWith("{")) {
    try { const e = JSON.parse(m), x = e.error || e; m = String(x.message || x.code || m); } catch { /* not JSON after all */ }
  }
  return m.replace(/\s*Request id:\s*\S+\s*$/i, "").trim();
}
// A model that takes no real people refuses a file with one: "video" or
// "image", which file it was. The server says it as class faces with the
// provider's words ("the input video … may contain real person"); a job from
// before that is a refusal with the provider's code in its message.
const facesIn = (s) => (/Input(Video|Image)SensitiveContentDetected\.PrivacyInformation/i.exec(s || "")?.[1] || "").toLowerCase();
function faceOf(j, cls) {
  const s = j.error?.message || "";
  if (cls !== "faces") return (cls === "refused" || cls === "moderation") && facesIn(s);
  const w = /\binput (video|image)\b/i.exec(s)?.[1];
  return w ? w.toLowerCase() : (j.inputs || []).some((f) => String(f.content_type).startsWith("video/")) ? "video" : "image";
}

// failure says why job j failed: label (on the frame), said (the provider's
// own reason, on the frame under it), text (only where the label does not say
// it all), note (the money), zero (a run on a key that cost nothing: its price
// shows as 0 instead of a line saying so) and acts, the buttons that fit.
export function failure(j) {
  const cls = j.error?.class || "", p = providerOf(j), P = PNAME[p] || p, free = j.free, vid = j.kind === "video";
  const back = t("fail.free_back"), none = free ? { note: back } : { note: "", zero: true };
  // The most the run could have cost: the label of the estimate, which for "up to" is its top.
  const up = j.estimate && j.estimate.kind !== "unknown" ? j.estimate.label : "";
  const may = free ? back : up ? t("fail.may_charge", { p: up, provider: P }) : t("fail.may_charge_some", { provider: P });
  const msg = OWNWORDS.has(cls) && j.error?.message ? ownWords(j.error.message) : "";
  const said = msg ? t("fail.said", { provider: P, text: msg }) : "";
  const again = ["retry", "edit"];
  if (j.status === "cancelled") return { cls: "cancelled", label: t("stage.cancelled"), text: "", ...none, acts: again };
  const face = faceOf(j, cls), m = C.byId[j.model];
  if (face) return { cls: "faces", label: t(face === "video" ? "fail.faces_clip_l" : "fail.faces_photo_l"), text: t("fail.faces", { model: m ? vname(m) : P }),
    ...(cls === "moderation" && free ? { note: t("fail.free_spent") } : none), acts: ["model", "edit"] };
  switch (cls) {
    case "moderation": return { cls, label: t("fail.moderation_l"), text: "", ...(free ? { note: t("fail.free_spent") } : none), said, acts: ["edit"] };
    case "money": return free
      ? { cls, label: t("fail.free_closed_l"), text: t("fail.free_money"), note: t("fail.free_kept"), acts: ["bykey"], closeFree: 15 * 60e3 }
      : { cls, label: t("fail.money_l"), text: "", ...none, acts: ["topup"], provider: p };
    case "key": return free
      ? { cls, label: t("fail.free_off_l"), text: t("fail.free_key"), note: t("fail.free_kept"), acts: ["bykey"] }
      : { cls, label: t("fail.key_l"), text: t("fail.key", { provider: P }), ...none, acts: [], keyGone: true };
    case "refused": return { cls, label: t("fail.refused_l"), text: t("fail.refused", { provider: P }), ...none, said, acts: ["edit"] };
    case "rate_limited": case "unavailable": return { cls, label: t("fail.no_answer_l"), text: "", ...none, note: (free ? back + " " : "") + t("fail.later"), acts: again };
    case "timeout": return { cls, label: t("fail.timeout_l"), text: "", note: may, acts: again };
    case "provider": return { cls, label: t("fail.provider_l"), text: "", note: may, said, acts: again };
    case "interrupted": return { cls, label: t("fail.interrupted_l"), text: t("fail.interrupted"), note: may, acts: again };
    case "storage": return { cls, label: t("fail.storage_l"), text: t(vid ? "fail.storage_video" : "fail.storage"), note: t(free ? "fail.free_back" : "fail.charged"), acts: again };
    case "no_adapter": return { cls, label: t("stage.failed"), text: t("fail.no_adapter", { provider: P }), ...none, acts: ["edit"] };
    // framy did not send it: a setting the offer lacks, so nothing reached the provider.
    case "invalid_request": return { cls, label: t("fail.invalid_l"), text: t("fail.invalid"), ...none, acts: ["edit"] };
    case "draft_expired": return { cls, label: t("stage.expired"), text: t("fail.draft_expired"), ...none, acts: again };
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
    case "wrong_inputs": case "unknown_input": {
      // The clip came out longer than the model takes: say so, with its part to pick.
      const mx = b.inputs?.videos?.secs?.max;
      if (mx && ctx.clipSecs > mx) return R(t("ref.part_long_l"), t("ref.part_long", { len: t("unit.s", { n: fmtNum(ctx.clipSecs, { minimumFractionDigits: 2, maximumFractionDigits: 2 }) }), max: fmtSecs(mx) }), ctx.cut ? ["cut"] : []);
      return R(t("ref.files_l"), t("ref.wrong_inputs"));
    }
    case "unknown_offer": return R(t("ref.model_l"), t("err.unknown_offer"));
    case "unknown_setting": return R(t("fail.invalid_l"), t("fail.invalid"));
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
