// The stage: the frame of what Create will make, in its shape, and once a
// run starts, that run: waiting, then the result. The full stage (every
// status, errors with their actions, several results) is a later step.
import { $, h, ic, isPhone } from "./dom.js";
import { anim, rise, snap, EASE, motion } from "./motion.js";
import { t, fmtSecs } from "./i18n.js";
import { C, vname, famLogo, lg, ratioNum, ratioOfSize } from "./catalog.js";
import { S, M, payOf, settings, sourceRatio } from "./state.js";
import { isFinal } from "./api.js";

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
// The largest box of ratio r that fits the stage.
function fit(r) {
  const st = $("#stage"), phone = isPhone();
  const W = Math.max(160, phone ? innerWidth - 32 : st.clientWidth);
  const dock = phone ? $("#composer").offsetHeight : 0, top = $("#top")?.offsetHeight || 0;
  const H = Math.max(160, phone ? innerHeight - dock - top - 92 : innerHeight - top - 120);
  let w = W, hh = W / r;
  if (hh > H) { hh = H; w = H * r; }
  return { w: Math.round(w), h: Math.round(hh) };
}

export function stageFrom() { return snap($("#stage"), { box: true, dur: 440 }); }

export function renderStage() {
  const st = $("#stage");
  if (!st) return;
  const j = S.job;
  if (j && S.showJob) return renderJob(st, j);
  const m = M();
  if (!m) return;
  const pay = payOf(m), set = settings(m, pay), r = ratioOf(set, m), b = fit(r), f = C.famOf[m.id];
  const ghost = h("div", { class: "ghost", "data-k": "pv", style: { width: b.w + "px", height: b.h + "px" } },
    h("i"), h("i"), h("i"), h("i"),
    h("div", { class: "c" }, ic(m.kind), h("b", {}, ratioLabel(set)),
      h("small", {}, h("span", { class: "it" }, lg(famLogo(f), f.maker, "s"), vname(m)))));
  ghost._r = r;
  const kids = [h("div", { class: "preview", "data-k": "pvbox" }, ghost)];
  if (j) kids.push(h("button", { type: "button", class: "link back", "data-k": "pv-back", onclick: () => { S.showJob = true; renderStage(); } }, ic("image"), t("stage.last")));
  st.replaceChildren(...kids);
}

function jobRatio(j) {
  const res = j.results?.[0];
  if (res?.width && res?.height) return res.width / res.height;
  return ratioOf(j.settings || {}, C.byId[j.model]);
}

function renderJob(st, j) {
  const r = jobRatio(j), b = fit(r), m = C.byId[j.model], res = j.results?.[0];
  const done = j.status === "done" && res, failed = isFinal(j.status) && !done;
  let media;
  if (done) {
    media = res.content_type?.startsWith("video/")
      ? h("video", { src: res.url, controls: true, playsinline: true, loop: true, muted: true, autoplay: true })
      : h("img", { src: res.url, alt: j.prompt || "", decoding: "async" });
    media.addEventListener(media.tagName === "IMG" ? "load" : "loadeddata", () => anim(media, [{ opacity: 0 }, { opacity: 1 }], { duration: 420 }), { once: true });
  }
  const status = failed
    ? h("div", { class: "c fail" }, ic("warn"), h("b", {}, t(j.status === "cancelled" ? "stage.cancelled" : j.status === "expired" ? "stage.expired" : "stage.failed")),
        j.error?.message ? h("small", {}, j.error.message) : null)
    : !done ? h("div", { class: "c run" }, h("span", { class: "spin", "aria-hidden": "true" }), h("b", {}, t(j.status === "running" ? "stage.running" : "stage.queued")),
        m ? h("small", {}, vname(m)) : null)
    : null;
  const box = h("div", { class: "jbox" + (done ? " done" : ""), "data-k": "pv", style: { width: b.w + "px", height: b.h + "px" } },
    done ? media : [h("i"), h("i"), h("i"), h("i")], status);
  box._r = r;
  const cost = j.cost?.label ? j.cost.label : j.free ? t("bar.free") : "";
  const acts = h("div", { class: "jacts", "data-k": "j-acts" },
    done ? h("a", { class: "link", href: res.url, download: "", target: "_blank", rel: "noopener" }, ic("down"), t("stage.download")) : null,
    isFinal(j.status) ? h("button", { type: "button", class: "link", onclick: () => { S.showJob = false; renderStage(); } }, ic("plus"), t("stage.next")) : null);
  const line = h("p", { class: "jline", "data-k": "j-line" }, [m ? vname(m) : "", cost].filter(Boolean).join(" · "));
  st.replaceChildren(h("div", { class: "preview", "data-k": "pvbox" }, box), line, acts);
  if (status && !failed && motion()) rise([status], { y: 6 });
}

// A run's new state on the stage: the frame takes its shape, the result fades in.
export function showJob(j) {
  const was = S.job;
  S.job = j;
  S.showJob = true;
  if (was && was.id === j.id && was.status === j.status && (was.results?.length || 0) === (j.results?.length || 0)) return;
  const d = stageFrom();
  renderStage();
  d();
  if (!was || was.id !== j.id) anim($("#stage .jbox"), [{ transform: "scale(.96)", opacity: 0.6 }, { transform: "none", opacity: 1 }], { duration: 420, easing: EASE });
}

// A new window size or a taller dock only resizes the frame: the picture and a playing video stay.
export function refit() {
  const b = $("#stage [data-k=pv]");
  if (!b?._r) return;
  const f = fit(b._r);
  b.style.width = f.w + "px";
  b.style.height = f.h + "px";
}
addEventListener("resize", refit);
