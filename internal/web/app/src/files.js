// Photos and clips the person adds. They stay in the browser until Create:
// the quote gets what the browser read of them, the upload happens on the
// press. A clip means video: the composer turns to it and to a model that
// takes one.
import { S, M, clips, needsCut, inSlot, placeFiles, settleSlots, refuses, misfit } from "./state.js";
import { C, takesVideo, refVideo } from "./catalog.js";
import { act, render, setModel } from "./composer.js";
import { toast } from "./ui.js";
import { t } from "./i18n.js";
import { api } from "./api.js";
import { looksLikeClip, clipTypeOk, readClip, MAX_CLIP, openCut, looksLikeSound, soundTypeOk, readSound } from "./clips.js";
import { motion } from "./motion.js";

let seq = 1;
const MAX_FILES = 16;
// A side longer than this is drawn smaller before it goes up (docs/design.md, Attachments).
const MAX_SIDE = 2048;

async function measure(file) {
  try {
    const b = await createImageBitmap(file);
    const out = { w: b.width, h: b.height };
    b.close?.();
    return out;
  } catch {
    const url = URL.createObjectURL(file);
    try {
      const img = new Image();
      img.src = url;
      await img.decode();
      return { w: img.naturalWidth, h: img.naturalHeight };
    } finally { URL.revokeObjectURL(url); }
  }
}

// slot is where in a video the files go ("start", "end" or "ref"), else
// where they fit (state.js, placeFiles).
export async function addFiles(list, slot = "") {
  // A frame is one picture, and it takes the place of the one there.
  const frame = slot === "start" || slot === "end";
  if (frame) list = list.filter((f) => f.type.startsWith("image/")).slice(0, 1);
  const room = MAX_FILES - S.files.length + (frame ? inSlot(slot).length : 0);
  if (list.length > room) toast(t("files.too_many", { n: MAX_FILES }));
  const got = [];
  for (const file of list.slice(0, Math.max(0, room))) {
    if (looksLikeClip(file)) {
      if (!clipTypeOk(file)) { toast(t("files.clip_type")); continue; }
      if (file.size > MAX_CLIP) { toast(t("files.clip_big", { n: 512 })); continue; }
      const f = await readClip(file);
      f.id = seq++;
      got.push(f);
      continue;
    }
    if (looksLikeSound(file)) {
      if (!soundTypeOk(file)) { toast(t("files.sound_type")); continue; }
      const f = await readSound(file);
      f.id = seq++;
      got.push(f);
      continue;
    }
    if (!file.type.startsWith("image/")) { toast(t("files.only_these")); continue; }
    let d;
    try { d = await measure(file); } catch { toast(t("files.unreadable", { name: file.name })); continue; }
    got.push({ id: seq++, kind: "image", file, name: file.name, url: URL.createObjectURL(file), w: d.w, h: d.h,
      meta: { content_type: file.type, bytes: file.size, width: d.w, height: d.h }, upload: 0 });
  }
  if (!got.length) return;
  const clip0 = got.find((f) => f.kind === "video"), snd0 = got.some((f) => f.kind === "audio"), kind0 = S.kind;
  act(() => {
    if (slot) {
      // Slots are a video's: the files go there as they were put.
      S.kind = "video";
      if (frame) for (const f of inSlot(slot)) removeFile(f, false);
      for (const f of got) f.slot = f.kind === "audio" ? "ref" : slot;
    } else if (clip0 || snd0) {
      // A sound is a video's reference.
      if (clip0) toClip();
      else { S.kind = "video"; S.cont = null; }
      // A clip or a sound goes with references, never with frames.
      for (const f of S.files) if (f.kind === "image" && f.slot !== "ref") f.slot = "ref";
    }
    S.files.push(...got);
    placeFiles(); settleSlots(); fitModel();
    render();
  });
  if (S.kind !== kind0) window.dispatchEvent(new CustomEvent("framy:kind", { detail: S.kind }));
  // A clip longer than the model takes: the editor opens on it.
  if (clip0 && needsCut()) setTimeout(openCut, motion() ? 420 : 0);
}

// A clip means video, unless the picture model takes a clip as a reference.
// A model that takes one clip keeps the new one only.
function toClip() {
  const m = M();
  if (S.kind === "image" && m && takesVideo(m)) return;
  S.kind = "video";
  if (!takesVideo(M())) {
    const id = [...(C.recommend.video_edit || []), ...(C.recommend.video || [])].find((x) => C.byId[x] && takesVideo(C.byId[x]));
    if (id) S.model.video = id;
  }
  if (!refVideo(M())) { for (const f of clips()) removeFile(f); }
  S.cont = null;
}

// settle false keeps the slots as they are, for a frame about to be replaced.
export function removeFile(f, settle = true) {
  if (f === clips()[0]) S.cut = null;
  S.files = S.files.filter((x) => x !== f);
  if (f.file && f.url?.startsWith("blob:")) URL.revokeObjectURL(f.url);
  if (settle) settleSlots();
}

// The video model follows the slots: one that cannot take what is in them
// gives way to the nearest that can, its own family first, then framy's
// suggestions, then the rest.
export function fitModel() {
  const m = M();
  if (!m || S.kind !== "video" || !refuses(m)) return;
  const f = C.famOf[m.id];
  const ids = [...(f ? f.cur.map((v) => v.id) : []), ...(clips().length ? C.recommend.video_edit || [] : []), ...(C.recommend.video || []),
    ...C.FAM.filter((x) => x.kind === "video").flatMap((x) => x.cur.map((v) => v.id))];
  const ok = ids.map((id) => C.byId[id]).filter((x) => x && C.famOf[x.id] && !refuses(x));
  const best = ok.find((x) => !misfit(x)) || ok[0];
  if (best) setModel(best.id);
}

// The photo as it goes up: as it is, or drawn smaller when a side is too long.
async function body(f) {
  if (Math.max(f.w, f.h) <= MAX_SIDE) return f.file;
  const k = MAX_SIDE / Math.max(f.w, f.h), w = Math.round(f.w * k), hh = Math.round(f.h * k);
  const bmp = await createImageBitmap(f.file, { resizeWidth: w, resizeHeight: hh, resizeQuality: "high" });
  const c = new OffscreenCanvas(w, hh);
  c.getContext("2d").drawImage(bmp, 0, 0);
  bmp.close?.();
  return c.convertToBlob({ type: f.file.type === "image/png" ? "image/png" : "image/jpeg", quality: 0.92 });
}

// Uploads the photos that have not gone up yet. An error names the file it
// came from.
export async function upload(files, signal) {
  for (const f of files) {
    if (f.upload) continue;
    try {
      const b = await body(f);
      const r = await api("/api/uploads", { method: "POST", body: b, headers: { "Content-Type": b.type || f.file.type }, signal });
      f.upload = r.id;
    } catch (e) { e.file = f; throw e; }
  }
}
