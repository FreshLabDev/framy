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

// A photo's type as the browser names it, else as its name does: Windows and
// Linux name none for a HEIC from an iPhone.
const PEXT = /\.(jpe?g|png|webp|gif|avif|heic|heif)$/i;
const PBY_EXT = { jpg: "image/jpeg", jpeg: "image/jpeg", png: "image/png", webp: "image/webp", gif: "image/gif", avif: "image/avif", heic: "image/heic", heif: "image/heif" };
export const looksLikePhoto = (f) => f.type.startsWith("image/") || (!f.type && PEXT.test(f.name));
const photoType = (f) => (f.type.startsWith("image/") ? f.type : PBY_EXT[(f.name.match(PEXT)?.[1] || "").toLowerCase()] || "image/jpeg");

// What the quote needs of a photo: its type and its size as it is shown. A
// photo the browser cannot draw, a HEIC in Chrome or Firefox, is read from
// its header instead and goes up as it is: the server converts it
// (docs/design.md, Attachments).
async function measure(file) {
  try {
    return { ...(await drawn(file)), type: photoType(file) };
  } catch (e) {
    const d = await heifHeader(file).catch(() => null);
    if (!d) throw e;
    return { ...d, asIs: true };
  }
}

async function drawn(file) {
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

// The type and size of a HEIC or AVIF, read as the server reads them
// (internal/media, header.go): the ftyp box's brands, then, in the meta box,
// the primary item's ispe, turned by its irot. Null for anything else.
async function heifHeader(file) {
  const read = async (a, b) => new DataView(await file.slice(a, b).arrayBuffer());
  const name = (v, o) => String.fromCharCode(v.getUint8(o), v.getUint8(o + 1), v.getUint8(o + 2), v.getUint8(o + 3));
  // The boxes in v from a to b: their type and where their payload lies.
  const boxes = (v, a, b) => {
    const out = [];
    while (a + 8 <= b) {
      let n = v.getUint32(a), head = 8;
      if (n === 1) { if (a + 16 > b) break; n = Number(v.getBigUint64(a + 8)); head = 16; } else if (n === 0) n = b - a;
      if (n < head || a + n > b) break;
      out.push({ typ: name(v, a + 4), a: a + head, b: a + n });
      a += n;
    }
    return out;
  };
  let type = "", meta = null;
  for (let off = 0; off + 8 <= file.size && !meta;) {
    const v = await read(off, off + 16);
    let n = v.getUint32(0), head = 8;
    if (n === 1) { n = Number(v.getBigUint64(8)); head = 16; } else if (n === 0) n = file.size - off;
    if (n < head || off + n > file.size) return null;
    const typ = name(v, 4);
    if (off === 0) {
      if (typ !== "ftyp" || n > 4096) return null;
      const f = await read(off + head, off + n), brands = [name(f, 0)];
      for (let i = 8; i + 4 <= f.byteLength; i += 4) brands.push(name(f, i));
      type = brands.some((x) => x === "avif" || x === "avis") ? "image/avif"
        : brands.some((x) => /^(heic|heix|heim|heis|hevc|hevx|mif1|msf1)$/.test(x)) ? "image/heic" : "";
      if (!type) return null;
    } else if (typ === "meta") {
      if (n - head > 16 << 20) return null;
      meta = await read(off + head, off + n);
    }
    off += n;
  }
  if (!meta) return null;
  // meta is a full box: its children come after the version and flags.
  const top = boxes(meta, 4, meta.byteLength), one = (list, typ) => list.find((x) => x.typ === typ);
  const pitm = one(top, "pitm"), iprp = one(top, "iprp");
  if (!iprp) return null;
  const inner = boxes(meta, iprp.a, iprp.b), ipco = one(inner, "ipco"), ipma = one(inner, "ipma");
  if (!ipco) return null;
  const props = boxes(meta, ipco.a, ipco.b);
  const size = (p) => (p.b - p.a >= 12 ? [meta.getUint32(p.a + 4), meta.getUint32(p.a + 8)] : null);
  // The primary item's properties, by their index in ipco from 1.
  let mine = null;
  if (pitm && ipma) {
    const id = meta.getUint8(pitm.a) ? meta.getUint32(pitm.a + 4) : meta.getUint16(pitm.a + 4);
    const ver = meta.getUint8(ipma.a), wide = meta.getUint8(ipma.a + 3) & 1;
    let o = ipma.a + 4;
    for (let k = meta.getUint32(o), i = 0, at = o + 4; i < k && at < ipma.b; i++) {
      const item = ver ? meta.getUint32(at) : meta.getUint16(at);
      at += ver ? 4 : 2;
      const m = meta.getUint8(at++), idx = [];
      for (let j = 0; j < m; j++, at += wide ? 2 : 1) idx.push(wide ? meta.getUint16(at) & 0x7fff : meta.getUint8(at) & 0x7f);
      if (item === id) { mine = idx.map((x) => props[x - 1]).filter(Boolean); break; }
    }
  }
  let wh = mine && one(mine, "ispe") && size(one(mine, "ispe"));
  // Without a primary item, the largest picture it declares, as the server takes.
  if (!wh) for (const p of props) { const s = p.typ === "ispe" && size(p); if (s && (!wh || s[0] * s[1] > wh[0] * wh[1])) wh = s; }
  if (!wh || !wh[0] || !wh[1]) return null;
  const rot = mine && one(mine, "irot");
  const turned = rot && meta.getUint8(rot.a) & 1;
  return { type, w: turned ? wh[1] : wh[0], h: turned ? wh[0] : wh[1] };
}

// slot is where in a video the files go ("start", "end" or "ref"), else
// where they fit (state.js, placeFiles). Files are added one batch at a
// time: a second drop while the first is still being read counts the files
// the first one took.
let adding = Promise.resolve();
export function addFiles(list, slot = "") {
  const run = adding.then(() => addNow(list, slot));
  adding = run.catch(() => {});
  return run;
}
async function addNow(list, slot) {
  // A frame is one picture, and it takes the place of the one there.
  const frame = slot === "start" || slot === "end";
  if (frame) list = list.filter(looksLikePhoto).slice(0, 1);
  const roomNow = () => MAX_FILES - S.files.length + (frame ? inSlot(slot).length : 0), room = roomNow();
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
    if (!looksLikePhoto(file)) { toast(t("files.only_these")); continue; }
    let d;
    try { d = await measure(file); } catch { toast(t("files.unreadable", { name: file.name })); continue; }
    // A photo the browser cannot draw has no picture of its own on its cell.
    got.push({ id: seq++, kind: "image", file, name: file.name, url: d.asIs ? "" : URL.createObjectURL(file), w: d.w, h: d.h, asIs: !!d.asIs,
      meta: { content_type: d.type, bytes: file.size, width: d.w, height: d.h }, upload: 0 });
  }
  // A run brought back from the library may have filled the composer while these were read.
  const over = got.splice(Math.max(0, roomNow()));
  if (over.length) { toast(t("files.too_many", { n: MAX_FILES })); for (const f of over) if (f.url?.startsWith("blob:")) URL.revokeObjectURL(f.url); }
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

// The photo as it goes up: as it is, or drawn smaller when a side is too
// long. One the browser cannot draw, or fails to, goes as it is, and the
// server brings it down instead.
async function body(f) {
  if (f.asIs || Math.max(f.w, f.h) <= MAX_SIDE) return f.file;
  const k = MAX_SIDE / Math.max(f.w, f.h), w = Math.round(f.w * k), hh = Math.round(f.h * k);
  try {
    const bmp = await createImageBitmap(f.file, { resizeWidth: w, resizeHeight: hh, resizeQuality: "high" });
    const c = new OffscreenCanvas(w, hh);
    c.getContext("2d").drawImage(bmp, 0, 0);
    bmp.close?.();
    return await c.convertToBlob({ type: f.meta.content_type === "image/png" ? "image/png" : "image/jpeg", quality: 0.92 });
  } catch { return f.file; }
}

// Uploads the photos that have not gone up yet. An error names the file it
// came from.
export async function upload(files, signal) {
  for (const f of files) {
    if (f.upload) continue;
    try {
      const b = await body(f);
      const r = await api("/api/uploads", { method: "POST", body: b, headers: { "Content-Type": b.type || f.meta.content_type }, signal });
      f.upload = r.id;
    } catch (e) { e.file = f; throw e; }
  }
}
