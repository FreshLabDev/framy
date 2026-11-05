// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// keys focuses the element sel names and presses the keys on it, as a
// keyboard does, a moment apart; a sel of "" presses them where the focus is.
func keys(t *testing.T, ctx context.Context, sel string, ks ...string) {
	t.Helper()
	if sel != "" && !js[bool](t, ctx, `const el = document.querySelector(`+jsString(sel)+`); if (!el) return false; el.focus(); return document.activeElement === el;`) {
		t.Fatalf("cannot focus %s", sel)
	}
	for _, k := range ks {
		if err := chromedp.Do(ctx, chromedp.KeyEvent(k), chromedp.Sleep(450*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
}

// focused says what has the focus now: its group and value for an option,
// else its id, else its tag and label.
const focused = `const a = document.activeElement;
if (!a || a === document.body) return "body";
const seg = a.parentElement && a.parentElement.dataset.seg;
if (seg && a.dataset.v != null) return seg + "=" + a.dataset.v + (a.getAttribute("aria-checked") === "true" || a.getAttribute("aria-current") === "page" ? " on" : "");
return a.id ? "#" + a.id : a.localName + ":" + (a.getAttribute("aria-label") || a.textContent.trim());`

// TestBrowserKeyboardChoices checks that a choice made with the keyboard
// leaves the focus on the control it was made with, though the blocks around
// it are drawn anew: Image and Video, how to pay, the shape, a step of the
// resolution slider after another, a model chosen in the picker (back on the
// model's card), a tab of the header and a filter of the library.
func TestBrowserKeyboardChoices(t *testing.T) {
	f := newFakeLibrary()
	ts := fakeSite(t, f.mount)
	ctx := browser(t, ts.URL, 1440, 900, false)
	at := func(want string) {
		t.Helper()
		if got := js[string](t, ctx, focused); got != want {
			t.Errorf("focus on %q, want %q", got, want)
		}
	}

	keys(t, ctx, `#kind [data-v=video]`, " ")
	at("kind=video on")
	keys(t, ctx, `#kind [data-v=image]`, kb.Enter)
	at("kind=image on")

	// The free run takes no settings; on the key they show, and the focus stays on the key.
	keys(t, ctx, `#how [data-seg=pay] [data-v=openrouter]`, " ")
	at("pay=openrouter on")
	tile := js[string](t, ctx, `const b = [...document.querySelectorAll("#how .ratios > .rt[data-v]")].find((b) => b.getAttribute("aria-checked") !== "true"); return b ? b.dataset.v : "";`)
	if tile == "" {
		t.Fatal("no shape to choose")
	}
	keys(t, ctx, `#how .ratios > .rt[data-v="`+tile+`"]`, " ")
	at("set-aspect_ratio=" + tile + " on")

	// A model chosen in the picker with Enter: the picker closes and the focus is on the model's card again.
	keys(t, ctx, "#mhead", kb.Enter)
	js[bool](t, ctx, pageHelpers+`const q = document.querySelector("#pksearch"); q.value = "klein"; q.dispatchEvent(new Event("input")); await wait(500); return true;`)
	keys(t, ctx, `#pkrows .frow .fhit`, kb.Enter)
	at("#mhead")
	if js[bool](t, ctx, `return !document.querySelector("#picker").hidden;`) {
		t.Error("the picker did not close")
	}

	// The resolution slider goes on step by step from the keyboard.
	v0 := js[float64](t, ctx, `const r = document.querySelector("#how .steps input[type=range]"); return r ? +r.value : -1;`)
	if v0 < 0 {
		t.Fatal("no resolution slider on FLUX.3 Klein")
	}
	keys(t, ctx, `#how .steps input[type=range]`, kb.ArrowRight, kb.ArrowRight)
	if v := js[float64](t, ctx, `return +document.activeElement.value;`); v != v0+2 {
		t.Errorf("two steps right of %v went to %v", v0, v)
	}
	at("input:Resolution")

	keys(t, ctx, `#top [data-v=library]`, kb.Enter)
	at("view=library on")
	js[bool](t, ctx, libHelpers+`return !!(await settled());`)
	keys(t, ctx, `#library [data-seg=lib] [data-v=image]`, " ")
	at("lib=image on")
	keys(t, ctx, `#library [data-seg=lib] [data-v=video]`, kb.Enter)
	at("lib=video on")
	noErrors(t, ctx)
}
