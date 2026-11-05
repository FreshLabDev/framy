// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
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
