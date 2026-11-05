// The composer as it was, across a load the page makes itself: signing in
// (Telegram's page, or the Mini App's launch data again), connecting
// OpenRouter, a new language. The words are kept as they are typed
// (framy.draft); the rest is kept just before the load and put back after it:
// the kind, the models the person picked, the payments, the settings, the
// frames, the part of a clip, the video a run goes on from and the files. A
// file framy keeps goes by its id; one the browser has waits in IndexedDB for
// the load, up to KEEP in all. What could not be kept is said, so nobody
// presses Create on less than they put together.
import { S, MAXFR, newFrame, autoModel, ownDefault, holdLinks } from "./state.js";
import { C } from "./catalog.js";

const KEY = "framy.compose", MARK = "framy.held", DB = "framy-draft", STORE = "files";
const KEEP = 100 * 1048576; // the most of the person's files kept for a load
const FOR = 30 * 60e3; // how long a kept composer waits for the page to come back
const STALE = 60 * 60e3; // files of a load that never came back go after this

function open() {
  return new Promise((ok, no) => {
    const r = indexedDB.open(DB, 1);
    r.onupgradeneeded = () => r.result.createObjectStore(STORE);
    r.onsuccess = () => ok(r.result);
    r.onerror = () => no(r.error);
    r.onblocked = () => no(new Error("blocked"));
  });
}
const ended = (tx) => new Promise((ok, no) => { tx.oncomplete = () => ok(); tx.onerror = tx.onabort = () => no(tx.error); });
const within = (p, ms) => Promise.race([p, new Promise((_, no) => setTimeout(() => no(new Error("timeout")), ms))]);
const got = (r) => new Promise((ok, no) => { r.onsuccess = () => ok(r.result); r.onerror = () => no(r.error); });

// The model of a kind the page picks by itself: the person's default, else framy's.
const ownChoice = (k) => ownDefault(k) || autoModel(k);

/* Before the load. It waits for the files to be written, a few seconds at
   most: a load that cannot wait longer goes without them, and says so. */
export async function keepDraft() {
  try {
    const files = [], blobs = [];
    let room = KEEP, lost = 0;
    for (const f of S.files) {
      const { file, url, ...x } = f;
      if (!file) { files.push({ ...x, url }); continue; }
      if (file.size > room) { lost++; continue; }
      room -= file.size;
      x.blob = crypto.randomUUID();
      x.fname = file.name; x.ftype = file.type; x.fat = file.lastModified;
      blobs.push([x.blob, file]);
      files.push(x);
    }
    // A model the page picks anyway is left out: after signing in, the person's default comes first.
    const model = {};
    for (const k of ["image", "video"]) if (S.model[k] && S.model[k] !== ownChoice(k)) model[k] = S.model[k];
    const d = { v: 1, at: Date.now(), kind: S.kind, model, pay: S.pay, wanted: S.wanted,
      frames: { image: S.frames.image.map((fr) => fr.own), video: S.frames.video.map((fr) => fr.own) },
      cut: S.cut, cont: S.cont, files, lost };
    sessionStorage.setItem(KEY, JSON.stringify(d));
    if (!blobs.length) return;
    try { localStorage.setItem(MARK, "1"); } catch {}
    const db = await within(open(), 1500);
    try {
      const tx = db.transaction(STORE, "readwrite"), st = tx.objectStore(STORE);
      for (const [id, file] of blobs) st.put({ at: d.at, file }, id);
      await within(ended(tx), 4000);
    } finally { db.close(); }
  } catch {}
}

/* After the load, before the composer is drawn: the state at once, the files
   once read back. It answers how many files did not come back, or -1 when
   there was nothing to put back. */
export async function takeDraft() {
  let d = null;
  try { d = JSON.parse(sessionStorage.getItem(KEY) || "null"); sessionStorage.removeItem(KEY); } catch {}
  if (!d || d.v !== 1 || !(Date.now() - d.at < FOR)) { sweep(); return -1; }
  if (d.kind === "image" || d.kind === "video") S.kind = d.kind;
  // A model that left the catalog leaves the page's own choice.
  for (const k of ["image", "video"]) { const id = d.model?.[k]; if (id && C.byId[id] && C.famOf[id]) S.model[k] = id; }
  if (d.pay && typeof d.pay === "object") Object.assign(S.pay, d.pay);
  if (d.wanted && typeof d.wanted === "object") Object.assign(S.wanted, d.wanted);
  for (const k of ["image", "video"]) {
    const owns = Array.isArray(d.frames?.[k]) ? d.frames[k].slice(0, MAXFR[k]) : [];
    if (owns.length) S.frames[k] = owns.map((o) => newFrame({ wanted: {}, ...o }));
  }
  // The video a run goes on from is the person's own: it stays for a person signed in.
  if (d.cont && S.signed) S.cont = d.cont;
  const want = (d.files || []).filter((x) => x.blob), back = new Map();
  if (want.length) {
    try {
      const db = await within(open(), 1500);
      try {
        const tx = db.transaction(STORE, "readwrite"), st = tx.objectStore(STORE);
        // Each is read and then deleted, in the order asked.
        const reads = want.map((x) => { const r = got(st.get(x.blob)); st.delete(x.blob); return r; });
        const vs = await within(Promise.all(reads), 3000);
        want.forEach((x, i) => { if (vs[i]?.file) back.set(x.blob, vs[i].file); });
        await within(ended(tx), 3000);
      } finally { db.close(); }
    } catch {}
  }
  let lost = d.lost || 0, n = 0;
  for (const x of d.files || []) {
    const { blob, fname, ftype, fat, ...f } = x;
    if (blob) {
      const b = back.get(blob);
      if (!b) { lost++; continue; }
      f.file = b instanceof File ? b : new File([b], fname || "", { type: ftype || b.type, lastModified: fat || Date.now() });
      f.url = URL.createObjectURL(f.file);
    }
    f.id = "d" + ++n;
    S.files.push(f);
  }
  holdLinks(S.files);
  // The part was of the first clip: with no clip there is no part.
  if (d.cut && S.files.some((f) => f.kind === "video")) S.cut = d.cut;
  sweep();
  return lost;
}

/* The load did not happen: the browser brought the page back as it was, as
   the back button from Telegram's page does. What was kept for it goes, or
   a later reload of the tab would bring back the composer of that moment. */
export function dropDraft() {
  let d = null;
  try { d = JSON.parse(sessionStorage.getItem(KEY) || "null"); sessionStorage.removeItem(KEY); } catch {}
  const ids = (d?.files || []).map((x) => x.blob).filter(Boolean);
  if (!ids.length) return;
  (async () => {
    const db = await open();
    try {
      const tx = db.transaction(STORE, "readwrite"), st = tx.objectStore(STORE);
      for (const id of ids) st.delete(id);
      await ended(tx);
    } finally { db.close(); }
  })().catch(() => {});
}

// Files a load left and never took back, as from a tab closed on Telegram's page.
function sweep() {
  try { if (!localStorage.getItem(MARK)) return; } catch { return; }
  (async () => {
    const db = await open();
    try {
      const tx = db.transaction(STORE, "readwrite"), st = tx.objectStore(STORE);
      let left = 0;
      st.openCursor().onsuccess = (e) => {
        const c = e.target.result;
        if (!c) return;
        if (Date.now() - (c.value?.at || 0) < STALE) left++; else c.delete();
        c.continue();
      };
      await ended(tx);
      if (!left) localStorage.removeItem(MARK);
    } finally { db.close(); }
  })().catch(() => {});
}
