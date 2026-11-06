// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"net/http"
	"testing"
)

// filesHelpers drop files on the page as a person does.
const filesHelpers = `
const drop = (files, on = document.body) => {
  const dt = new DataTransfer();
  for (const f of files) dt.items.add(f);
  on.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
};
const thumbs = () => document.querySelectorAll("#files .thumb").length;
const toastText = () => document.querySelector(".toast")?.textContent || "";
`

// TestBrowserTwoDropsKeepTheLimit drops ten photos and ten more before the
// first ten are read: the composer takes sixteen, as it does from one drop,
// and says why the rest stayed out.
func TestBrowserTwoDropsKeepTheLimit(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+slotHelpers+filesHelpers+`
	const ten = async (k) => Promise.all([...Array(10)].map((_, i) => png(40 + k * 10 + i, 30)));
	const a = await ten(0), b = await ten(1);
	drop(a); drop(b);
	await wait(2000);
	return { thumbs: thumbs(), toast: toastText() };`)
	if got["thumbs"].(float64) != 16 || got["toast"] != "No more than 16 photos." {
		t.Errorf("two drops of ten: %v, want 16 files and the toast", got)
	}
	noErrors(t, ctx)
}

// TestBrowserUploadKeepsTransparency checks the pictures the browser draws
// again before they go up: a transparent WebP over 2048 px goes smaller and
// still transparent, where it went as a JPEG with its clear parts black, and
// a GIF, which the server does not take, goes as a PNG of its first frame
// whatever its size.
func TestBrowserUploadKeepsTransparency(t *testing.T) {
	runs := &fakeRuns{}
	ts := fakeSite(t, func(srv *Server) {
		runs.mount(srv)
		srv.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(w, http.StatusCreated, map[string]any{"id": 7, "content_type": r.Header.Get("Content-Type"), "bytes": 100, "width": 10, "height": 10})
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	got := js[map[string]any](t, ctx, pageHelpers+runHelpers+filesHelpers+`
	const ups = [], fetch0 = window.fetch;
	window.fetch = (u, o) => { if (String(u).endsWith("/api/uploads")) ups.push({ type: o.headers["Content-Type"], blob: o.body }); return fetch0(u, o); };
	// Clear corners, an opaque middle.
	const cv = document.createElement("canvas"); cv.width = 2400; cv.height = 1200;
	const g = cv.getContext("2d"); g.fillStyle = "#c33"; g.fillRect(600, 300, 1200, 600);
	const webp = new File([await new Promise((r) => cv.toBlob(r, "image/webp", 0.9))], "sticker.webp", { type: "image/webp" });
	// A transparent GIF of one pixel.
	const gif = new File([Uint8Array.from(atob("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"), (c) => c.charCodeAt(0))], "dot.gif", { type: "image/gif" });
	drop([webp, gif]);
	await wait(1200);
	await press("done", (s) => s.done);
	const out = [];
	for (const u of ups) {
	  const b = await createImageBitmap(u.blob), c = new OffscreenCanvas(b.width, b.height), x = c.getContext("2d");
	  x.drawImage(b, 0, 0);
	  out.push({ type: u.type, blob: u.blob.type, w: b.width, h: b.height, corner: x.getImageData(0, 0, 1, 1).data[3], middle: x.getImageData(b.width >> 1, b.height >> 1, 1, 1).data[3] });
	}
	return { ups: out };`)
	ups, _ := got["ups"].([]any)
	if len(ups) != 2 {
		t.Fatalf("uploads: %v, want two", got)
	}
	webp, gif := ups[0].(map[string]any), ups[1].(map[string]any)
	if webp["type"] != "image/webp" || webp["w"].(float64) != 2048 || webp["h"].(float64) != 1024 || webp["corner"].(float64) != 0 || webp["middle"].(float64) != 255 {
		t.Errorf("the large WebP went up as %v, want a 2048×1024 WebP still clear in its corners", webp)
	}
	if gif["type"] != "image/png" || gif["w"].(float64) != 1 || gif["corner"].(float64) != 0 {
		t.Errorf("the GIF went up as %v, want a clear PNG of one pixel", gif)
	}
	noErrors(t, ctx)
}

// TestBrowserFilesOnlyWhereSeen checks that a photo comes into the composer
// by drop or paste only where the composer is in sight. Dropped on the
// library or on the picker, it went into the composer out of sight; pasted
// in the picker's search, it took the place of the text. A drop elsewhere is
// still turned away, so the browser does not open the file instead of framy.
func TestBrowserFilesOnlyWhereSeen(t *testing.T) {
	ts := fakeAPI(t)
	const steps = `
	const paste = (on, files) => {
	  const dt = new DataTransfer();
	  for (const f of files) dt.items.add(f);
	  const e = new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true });
	  on.dispatchEvent(e);
	  return e.defaultPrevented;
	};
	const dropped = async (on) => {
	  const dt = new DataTransfer();
	  dt.items.add(await png(64, 48));
	  const e = new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt });
	  on.dispatchEvent(e);
	  await wait(900);
	  return e.defaultPrevented;
	};`
	t.Run("composer", func(t *testing.T) {
		ctx := browser(t, ts.URL, 1440, 900, false)
		got := js[map[string]any](t, ctx, pageHelpers+slotHelpers+filesHelpers+pickerHelpers+steps+`
		const out = {};
		await openPicker();
		out.pickerDrop = await dropped(document.querySelector("#pkrows"));
		out.searchPaste = paste(document.querySelector("#pksearch"), [await png(64, 48)]);
		await wait(600);
		out.underPicker = thumbs();
		document.querySelector("#picker [data-close]").click();
		await wait(700);
		out.pagePaste = paste(document.body, [await png(64, 48)]);
		await wait(900);
		out.composer = thumbs();
		return out;`)
		if got["pickerDrop"] != true || got["searchPaste"] != false || got["underPicker"].(float64) != 0 {
			t.Errorf("with the picker open: %v, want the drop turned away and the paste the search's", got)
		}
		if got["pagePaste"] != true || got["composer"].(float64) != 1 {
			t.Errorf("a paste on the composer's page: %v, want the photo taken", got)
		}
		noErrors(t, ctx)
	})
	t.Run("library", func(t *testing.T) {
		ctx := browser(t, ts.URL+"/library", 1440, 900, false)
		got := js[map[string]any](t, ctx, pageHelpers+slotHelpers+filesHelpers+steps+`
		const prevented = await dropped(document.querySelector("#library"));
		return { prevented, view: document.querySelector("#app").dataset.view, files: thumbs(), drag: document.querySelector("#composer").classList.contains("drag") };`)
		if got["view"] != "library" || got["prevented"] != true || got["files"].(float64) != 0 || got["drag"] != false {
			t.Errorf("a drop on the library: %v, want it turned away and the composer untouched", got)
		}
		noErrors(t, ctx)
	})
}
