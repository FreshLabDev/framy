// Photos the person adds. They stay in the browser until Create: the quote
// gets what the browser read of them, the upload happens on the press.
import { S } from "./state.js";
import { act, render } from "./composer.js";
import { toast } from "./ui.js";
import { t } from "./i18n.js";
import { api } from "./api.js";

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

export async function addFiles(list) {
  const pics = list.filter((f) => f.type.startsWith("image/"));
  if (pics.length < list.length) toast(t("files.only_photos"));
  const room = MAX_FILES - S.files.length;
  if (pics.length > room) toast(t("files.too_many", { n: MAX_FILES }));
  const got = [];
  for (const file of pics.slice(0, Math.max(0, room))) {
    let d;
    try { d = await measure(file); } catch { toast(t("files.unreadable", { name: file.name })); continue; }
    got.push({ id: seq++, kind: "image", file, name: file.name, url: URL.createObjectURL(file), w: d.w, h: d.h,
      meta: { content_type: file.type, bytes: file.size, width: d.w, height: d.h }, upload: 0 });
  }
  if (got.length) act(() => { S.files.push(...got); render(); });
}

export function removeFile(f) {
  S.files = S.files.filter((x) => x !== f);
  URL.revokeObjectURL(f.url);
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

// Uploads what has not gone up yet; returns the ids in order.
export async function upload(files) {
  const ids = [];
  for (const f of files) {
    if (!f.upload) {
      const b = await body(f);
      const r = await api("/api/uploads", { method: "POST", body: b, headers: { "Content-Type": b.type || f.file.type } });
      f.upload = r.id;
    }
    ids.push(f.upload);
  }
  return ids;
}
