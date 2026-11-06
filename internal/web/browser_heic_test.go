// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
)

// TestBrowserHEIC attaches an iPhone photo in a browser that cannot draw
// HEIC, as Chrome and Firefox cannot (docs/design.md, Attachments). It is
// taken whether or not the system names its type, with its size read from
// the header and turned as the iPhone shot it, a plain cell for a
// thumbnail, and it goes up as it is for the server to convert. A file
// that only says it is a HEIC is still refused by name.
func TestBrowserHEIC(t *testing.T) {
	heic, err := os.ReadFile("../media/testdata/iphone.heic")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu   sync.Mutex
		got  []byte
		kind string
	)
	runs := &fakeRuns{}
	ts := fakeSite(t, func(srv *Server) {
		runs.mount(srv)
		srv.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got, kind = b, r.Header.Get("Content-Type")
			mu.Unlock()
			writeTestJSON(w, http.StatusCreated, map[string]any{"id": 7, "content_type": "image/jpeg", "bytes": 100, "width": 1200, "height": 1600})
		})
	})
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, `
	window.__quotes = []; window.__sent = [];
	const fetch0 = window.fetch;
	window.fetch = (u, o) => {
	  const s = String(u);
	  if (s.endsWith("/api/quote")) __quotes.push(JSON.parse(o.body));
	  if (s.endsWith("/api/jobs") && o?.method === "POST") __sent.push(JSON.parse(o.body));
	  return fetch0(u, o);
	};
	return true;`)
	helpers := pageHelpers + slotHelpers + fmt.Sprintf(`
	const heic = (name, type) => new File([Uint8Array.from(atob(%q), (c) => c.charCodeAt(0))], name, { type });
	const drop = async (files) => {
	  const dt = new DataTransfer();
	  for (const f of files) dt.items.add(f);
	  document.body.dispatchEvent(new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: dt }));
	  await wait(1200);
	};
	const toastText = () => document.querySelector(".toast")?.textContent || "";
	const cells = () => [...document.querySelectorAll("#files .thumb")].map((c) => c.querySelector("img") ? "img" : c.querySelector(".noth") ? "plain" : "?").join(",");
	const sent = () => { const f = own()?.files?.[0]; return f ? [f.content_type, f.bytes, f.width, f.height].join(" ") : ""; };
	`, base64.StdEncoding.EncodeToString(heic))
	s := js[map[string]any](t, ctx, helpers+`
	const out = { accept: document.querySelector("#file").accept };
	// A start frame: the slot takes a photo the system named no type for.
	document.querySelectorAll("#kind > button")[1].click();
	await wait(800);
	await dropOn("start", [heic("IMG_0001.HEIC", "")]);
	out.frame = groups(); out.frameSent = sent(); out.frameToast = toastText();
	document.querySelector('#files [data-k="g-start"] .thumb .rm')?.click();
	await wait(700);
	document.querySelectorAll("#kind > button")[0].click();
	await wait(800);
	// A picture to change, dropped anywhere.
	await drop([heic("IMG_0001.HEIC", "")]);
	out.toast = toastText(); out.cells = cells(); out.sent = sent();
	// A file that only says it is a HEIC.
	await drop([new File([new Uint8Array(64)], "IMG_0002.HEIC", { type: "image/heic" })]);
	out.broken = toastText(); out.cells2 = cells();
	const p = document.querySelector("#prompt");
	p.value = "warmer"; p.dispatchEvent(new Event("input"));
	await wait(400);
	document.querySelector("#go").click();
	for (let i = 0; i < 60 && !__sent.length; i++) await wait(100);
	out.inputs = JSON.stringify(__sent[0]?.inputs || null);
	return out;`)
	accept := fmt.Sprint(s["accept"])
	for _, ext := range []string{".heic", ".heif", "image/heic"} {
		if !bytes.Contains([]byte(accept), []byte(ext)) {
			t.Errorf("the file dialog takes %q, without %s", accept, ext)
		}
	}
	// The fixture is stored 1600 by 1200 and turned a quarter by irot.
	const want = "image/heic 4305 1200 1600"
	if s["frame"] != "start:1 end:0+" || s["frameSent"] != want || s["frameToast"] != "" {
		t.Errorf("a HEIC as a start frame: %v, quoted %q, toast %q", s["frame"], s["frameSent"], s["frameToast"])
	}
	if s["toast"] != "" || s["cells"] != "plain" || s["sent"] != want {
		t.Errorf("a HEIC to change: toast %q, cells %v, quoted %q", s["toast"], s["cells"], s["sent"])
	}
	if s["broken"] != "Could not read IMG_0002.HEIC." || s["cells2"] != "plain" {
		t.Errorf("a broken HEIC: toast %q, cells %v", s["broken"], s["cells2"])
	}
	mu.Lock()
	defer mu.Unlock()
	if s["inputs"] != "[7]" || !bytes.Equal(got, heic) || kind != "image/heic" {
		t.Errorf("the run took %v; the upload was %d bytes of %q, the photo as it is: %v", s["inputs"], len(got), kind, bytes.Equal(got, heic))
	}
	noErrors(t, ctx)
}
