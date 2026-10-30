// The stage: the frame of what Create will make, in its shape, and once a
// run starts, that run: its files going up, the wait, a retry, the result,
// or why there is none, with the buttons that fit.
import { $, $$, h, ic, isPhone } from "./dom.js";
import { anim, rise, snap, EASE, motion } from "./motion.js";
import { t } from "./i18n.js";
import { C, vname, famLogo, lg, ratioNum, ratioOfSize, PNAME } from "./catalog.js";
import { S, M, payOf, settings, sourceRatio } from "./state.js";
import { isFinal } from "./api.js";
import { failure, waiting, left, BILLING } from "./errors.js";
import { priceText } from "./price.js";
import { again, editPrompt, cancel, byKey, keyFor, takePrice, drop, pickPart, copyPrompt, continueFrom, fromLastFrame, canShare, share } from "./run.js";

// The shape of a run: its ratio, or the photo's for "auto", else square.
function ratioOf(set, m) {
  if (set.size && /x/.test(set.size)) return ratioNum(ratioOfSize(set.size));
  if (set.aspect_ratio && set.aspect_ratio !== "auto") return ratioNum(set.aspect_ratio);
  return sourceRatio() || (m?.kind === "video" ? 16 / 9 : 1);
}
function ratioLabel(set) {
  if (set.size && /x/.test(set.size)) return ratioOfSize(set.size);
  if (set.aspect_ratio && set.aspect_ratio !== "auto") return set.aspect_ratio;
  return sourceRatio() ? t("val.photo") : t("val.auto");
}
// The largest box of ratio r that fits the stage, with room under it for
// what a run says (more for the reason it failed).
function fit(r, under = 0) {
  const st = $("#stage"), phone = isPhone();
  const W = Math.max(160, phone ? innerWidth - 32 : st.clientWidth);
  const dock = phone ? $("#composer").offsetHeight : 0, top = $("#top")?.offsetHeight || 0;
  const H = Math.max(160, (phone ? innerHeight - dock - top - 92 : innerHeight - top - 120) - under);
  let w = W, hh = W / r;
  if (hh > H) { hh = H; w = H * r; }
  return { w: Math.round(w), h: Math.round(hh) };
}

export function stageFrom() { return snap($("#stage"), { box: true, dur: 440 }); }

// A run has ended: refused, stopped before the job, or a job that is final.
export const runOver = (run) => !!run && (run.phase === "refused" || run.phase === "cancelled" || (run.phase === "job" && isFinal(run.job.status)));

export function renderStage() {
  const st = $("#stage");
  if (!st) return;
  const run = S.run;
  if (run && S.showJob) { renderRun(st, run); ticking(); return; }
  ticking();
  const m = M();
  if (!m) return;
  const pay = payOf(m), set = settings(m, pay), r = ratioOf(set, m), b = fit(r), f = C.famOf[m.id];
  const ghost = h("div", { class: "ghost", "data-k": "pv", style: { width: b.w + "px", height: b.h + "px" } },
    h("i"), h("i"), h("i"), h("i"),
    h("div", { class: "c" }, ic(m.kind), h("b", {}, ratioLabel(set)),
      h("small", {}, h("span", { class: "it" }, lg(famLogo(f), f.maker, "s"), vname(m)))));
  ghost._r = r;
  const kids = [h("div", { class: "preview", "data-k": "pvbox" }, ghost)];
  if (run) kids.push(h("button", { type: "button", class: "link back", "data-k": "pv-back", onclick: () => { S.showJob = true; renderStage(); } },
    ic(runOver(run) ? "image" : "clock"), t(runOver(run) ? "stage.last" : "stage.current")));
  st.replaceChildren(...kids);
}

function runRatio(run) {
  const res = run.job?.results?.[0];
  if (res?.width && res?.height) return res.width / res.height;
  const m = C.byId[run.model], set = run.settings || {};
  if (set.size && /x/.test(set.size)) return ratioNum(ratioOfSize(set.size));
  if (set.aspect_ratio && set.aspect_ratio !== "auto") return ratioNum(set.aspect_ratio);
  const f = run.files[0];
  return f?.w && f?.h ? f.w / f.h : m?.kind === "video" ? 16 / 9 : 1;
}

// A line that counts down: its text is asked again every second.
const live = (text, cls = "") => { const el = h("small", { class: ("tk " + cls).trim() }, text()); el._text = text; return el; };

// What the stage says about run: the frame's status, the reason under it and the buttons.
function view(run) {
  const m = C.byId[run.model], j = run.job, kind = run.kind;
  const name = m ? vname(m) : "";
  const spin = (label, sub) => ({ status: h("div", { class: "c run" }, h("span", { class: "spin", "aria-hidden": "true" }), h("b", {}, label), sub) });
  const stop = h("button", { type: "button", onclick: () => cancel(run) }, ic("stop"), t("act.cancel"));
  if (run.phase === "prep") {
    const s = run.step;
    return { ...spin(t("stage.starting"), s ? live(s.text) : h("small", {}, name)), acts: run.sending ? [] : [stop] };
  }
  if (run.phase === "refused") {
    const w = run.why;
    return { fail: true, status: failEl(w.label), why: [w.text], acts: refusalActs(run, w.acts) };
  }
  if (run.phase === "cancelled") {
    return { fail: true, status: failEl(t("stage.cancelled")), why: [t(run.pay === "free" ? "fail.free_back" : "fail.nothing")], acts: againActs(run) };
  }
  const st = j.status;
  if (st === "queued") {
    const w = waiting(j);
    if (w) return { ...spin(t("stage.retry"), live(() => w.line(left(w.at)))), acts: [stop] };
    return { ...spin(t("stage.queued"), h("small", {}, name)), acts: [stop] };
  }
  if (st === "running") {
    return { ...spin(t("stage.running"), h("small", {}, name)), acts: [h("span", { class: "busy" }, ic("info"), t(kind === "video" ? "cancel.running_video" : "cancel.running"))] };
  }
  const res = j.results?.[0];
  if (st === "done" && res) return { done: true, res, acts: doneActs(run, j, res) };
  const f = failure(j);
  return { fail: true, status: failEl(f.label), why: [f.text, f.note], msg: f.msg, provider: f.provider, acts: failActs(run, f) };
}
const failEl = (label) => h("div", { class: "c fail" }, ic("warn"), h("b", {}, label));

// The buttons of a run, the first of them the one that helps most.
// A square one shows only its icon on a phone, so each says its name.
const B = (icon, label, fn, cls = "") => h("button", { type: "button", class: cls || null, "aria-label": cls.startsWith("sq") ? label : null, onclick: fn }, ic(icon), h("span", { class: "t" }, label));
const againActs = (run) => [B("again", t("act.retry"), () => again(run), "primary"), B("edit", t("act.edit"), () => editPrompt(run))];
function keyBtn(run) {
  const p = keyFor(run);
  return p ? B("key", t("act.bykey", { provider: PNAME[p] }), () => byKey(run), "primary") : null;
}
function failActs(run, f) {
  const out = [];
  for (const a of f.acts) {
    if (a === "retry") out.push(B("again", t("act.retry"), () => again(run), out.length ? "" : "primary"));
    else if (a === "edit") out.push(B("edit", t("act.edit"), () => editPrompt(run), out.length ? "" : "primary"));
    else if (a === "bykey") out.push(keyBtn(run));
    else if (a === "topup") out.push(topup(f.provider));
  }
  if (!f.acts.includes("edit")) out.push(B("edit", t("act.edit"), () => editPrompt(run)));
  return out;
}
function topup(p) {
  const url = BILLING[p];
  if (!url) return null;
  if (S.tg?.openLink) return B("coin", t("act.topup", { provider: PNAME[p] || p }), () => S.tg.openLink(url), "primary");
  return h("a", { class: "primary", href: url, target: "_blank", rel: "noopener" }, ic("coin"), h("span", { class: "t" }, t("act.topup", { provider: PNAME[p] || p })));
}
function refusalActs(run, acts) {
  const out = [];
  for (const a of acts) {
    const first = out.length ? "" : "primary";
    if (a === "takeprice") out.push(B("spark", t("act.takeprice", { p: priceText(run.why.estimate) }), () => takePrice(run), first));
    else if (a === "drop") out.push(B("close", t("cancel"), () => drop(), first));
    else if (a === "bykey") out.push(keyBtn(run));
    else if (a === "cut") out.push(B("film", t("act.pick_part"), () => pickPart(run), first));
    else if (a === "lastframe") out.push(B("next", t("act.lastframe"), () => fromLastFrame(run.cont), first));
    else if (a === "again") out.push(B("again", t("act.retry"), () => again(run), first));
  }
  if (!acts.includes("drop")) out.push(B("edit", t("act.edit"), () => editPrompt(run)));
  return out.filter(Boolean);
}
function doneActs(run, j, res) {
  const vid = res.content_type?.startsWith("video/");
  let sb = null;
  if (canShare(res)) { sb = B("send", t("act.share"), () => share(res, sb), "sq"); sb.title = t("act.share_tip"); }
  return [download(j, res),
    B("again", t("act.again"), () => again(run), "sq2"),
    B("edit", t("act.edit"), () => editPrompt(run), "sq"),
    vid ? B("next", t("act.continue"), () => continueFrom(run), "sq") : null, sb];
}

function renderRun(st, run) {
  const v = view(run), r = runRatio(run), b = fit(r, v.fail ? 150 : 70), j = run.job, m = C.byId[run.model];
  let media = null;
  if (v.done) {
    const res = v.res;
    media = res.content_type?.startsWith("video/")
      ? h("video", { src: res.url, controls: true, playsinline: true, loop: true, muted: true, autoplay: true })
      : h("img", { src: res.url, alt: run.prompt || "", decoding: "async" });
    media.addEventListener(media.tagName === "IMG" ? "load" : "loadeddata", () => anim(media, [{ opacity: 0 }, { opacity: 1 }], { duration: 420 }), { once: true });
  }
  // While the page cannot reach framy, the frame says so; it hears again by itself.
  const going = run.phase === "job" && !isFinal(j.status);
  if (going && run.net === "lost" && v.status) v.status.querySelector("small")?.replaceWith(h("small", { class: "lost" }, t("net.lost_line")));
  const box = h("div", { class: "jbox" + (v.done ? " done" : "") + (v.fail ? " failed" : ""), "data-k": "pv", style: { width: b.w + "px", height: b.h + "px" } },
    v.done ? media : [h("i"), h("i"), h("i"), h("i")], v.status || null);
  box._r = r; box._under = v.fail ? 150 : 70;
  const cost = j?.cost?.label ? j.cost.label : j?.free || (run.pay === "free" && !v.fail) ? t("bar.free") : run.shownLabel && !v.fail ? run.shownLabel : "";
  const line = h("p", { class: "jline", "data-k": "j-line" }, [m ? vname(m) : "", cost].filter(Boolean).join(" · "),
    going && run.net === "poll" ? h("span", { class: "jtag", title: t("net.poll_tip") }, t("net.poll")) : null);
  const why = v.why?.filter(Boolean).length || v.msg
    ? h("div", { class: "jwhy", "data-k": "j-why" }, v.why.filter(Boolean).map((x) => h("p", {}, x)),
        v.msg ? h("p", { class: "said" }, ic("info"), h("span", {}, t("fail.said", { provider: PNAME[v.provider] || PNAME[run.pay] || "", text: v.msg }))) : null)
    : null;
  const prompt = run.prompt ? h("p", { class: "jprompt", "data-k": "j-prompt" }, h("span", { class: "tx" }, run.prompt),
    h("button", { type: "button", class: "cp", "aria-label": t("act.copy"), title: t("act.copy"), onclick: () => copyPrompt(run.prompt) }, ic("copy"))) : null;
  const acts = h("div", { class: "jacts acts", "data-k": "j-acts" }, v.acts.filter(Boolean),
    runOver(run) ? h("button", { type: "button", class: "sq", "aria-label": t("stage.next"), title: t("stage.next"), onclick: () => drop() }, ic("plus"), h("span", { class: "t" }, t("stage.next"))) : null);
  st.replaceChildren(h("div", { class: "preview", "data-k": "pvbox" }, box), h("div", { class: "jfoot", "data-k": "j-foot" }, line, why, acts, prompt));
}

// Download saves the original. res.download is a link that asks for an
// attachment, which a browser saves where it is and Telegram's downloadFile
// takes; res.url only shows the file, and a browser ignores the download
// attribute on another site's link, so without the first it opens in a tab.
const EXT = { "image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/avif": "avif", "image/gif": "gif", "video/mp4": "mp4", "video/webm": "webm", "video/quicktime": "mov" };
function download(j, res) {
  // The name the server gives the download, for Telegram, which asks for one.
  const name = `framy-${j.id}-1.${EXT[res.content_type] || (res.content_type || "").split("/")[1] || "bin"}`;
  const kids = [ic("down"), h("span", { class: "t" }, t("stage.download"))], tg = S.tg;
  if (res.download && tg?.downloadFile && tg.isVersionAtLeast?.("8.0"))
    return h("button", { type: "button", class: "primary", onclick: () => tg.downloadFile({ url: res.download, file_name: name }) }, ...kids);
  if (res.download) return h("a", { class: "primary", href: res.download, download: name }, ...kids);
  return h("a", { class: "primary", href: res.url, download: name, target: "_blank", rel: "noopener" }, ...kids);
}

// What the stage shows of the run, to tell a new state from the same one again.
let shownSig = "", shownN = 0, shownStep = null;
const sigOf = (run) => [run.n, run.phase, run.job?.status || "", run.job?.results?.length || 0, run.job?.error?.class || "", run.job?.retry_at || "",
  run.job?.cost?.label || "", run.net, run.sending ? 1 : 0, S.showJob ? 1 : 0].join("|");
// The run changed: the stage shows its new state, the frame glides to its new shape.
export function runChanged() {
  const run = S.run;
  if (!run) { shownSig = ""; renderStage(); return; }
  const sig = sigOf(run);
  if (sig === shownSig && run.step === shownStep) return;
  const fresh = run.n !== shownN;
  shownSig = sig; shownN = run.n; shownStep = run.step;
  const d = stageFrom();
  renderStage();
  d();
  if (fresh && S.showJob) anim($("#stage .jbox"), [{ transform: "scale(.96)", opacity: 0.6 }, { transform: "none", opacity: 1 }], { duration: 420, easing: EASE });
  else if (motion()) { const c = $("#stage .jbox .c"); if (c && (run.phase !== "job" || !isFinal(run.job?.status))) rise([c], { y: 6 }); }
}

// The countdowns on the stage, once a second while there are any.
let tickT = 0;
function ticking() {
  const any = () => $$("#stage .tk").length > 0;
  if (any() && !tickT) tickT = setInterval(() => {
    if (!any()) { clearInterval(tickT); tickT = 0; return; }
    for (const el of $$("#stage .tk")) { const x = el._text?.(); if (x != null && el.textContent !== x) el.textContent = x; }
  }, 1000);
}

// A new window size or a taller dock only resizes the frame: the picture and a playing video stay.
// On a phone the stage takes the room over the dock and keeps the frame in
// the middle of it; the room under the frame waits for the result's line.
function room() {
  const st = $("#stage");
  if (!st) return;
  if (!isPhone()) { st.style.minHeight = ""; return; }
  const top = st.getBoundingClientRect().top + scrollY;
  st.style.minHeight = Math.max(0, Math.floor(innerHeight - $("#composer").offsetHeight - top - 24)) + "px";
}
export function refit() {
  room();
  const b = $("#stage [data-k=pv]");
  if (!b?._r) return;
  const f = fit(b._r, b._under || 0);
  b.style.width = f.w + "px";
  b.style.height = f.h + "px";
}
addEventListener("resize", refit);
