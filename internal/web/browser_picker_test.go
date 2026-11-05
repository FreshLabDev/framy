// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// catalogInit opens the page in a language with the catalog changed by
// shape, a page function of the catalog's JSON.
func catalogInit(lang, shape string) string {
	return `
try { localStorage.setItem("framy.lang", ` + jsString(lang) + `); } catch (e) {}
{
  const shape = ` + shape + `, fetch0 = window.fetch;
  window.fetch = async (u, o) => {
    const r = await fetch0(u, o);
    if (!String(u).endsWith("/api/catalog")) return r;
    const b = shape(await r.json());
    return new Response(JSON.stringify(b), { status: r.status, headers: { "Content-Type": "application/json" } });
  };
}
`
}

// pickerHelpers choose a family in the picker by its name.
const pickerHelpers = `
const openPicker = async () => {
  [...document.querySelectorAll("#how .mhead, #dtools .chipbtn:not(.set)")].find(vis).click();
  await wait(700);
};
const famRow = (name) => [...document.querySelectorAll("#pkrows .frow")].find((r) => r.querySelector(".nm").textContent === name);
const chooseFam = async (name) => {
  await openPicker();
  famRow(name).querySelector(".fhit").click();
  await wait(300);
  const choose = [...document.querySelectorAll("#picker .det .choose")].find(vis);
  if (choose && !choose.disabled) choose.click();
  await wait(900);
};
const toVideo = async () => { document.querySelectorAll("#kind > button")[1].click(); await wait(800); };
`

// TestBrowserShutDownDay checks the day a model shuts down reads as the
// maker wrote it in every time zone: in New York, midnight UTC of the 11th
// is still the 10th, and the composer said the 10th. A day of another year
// says its year.
func TestBrowserShutDownDay(t *testing.T) {
	ts := fakeAPI(t)
	for _, tc := range []struct {
		name, day, want string
	}{
		{"this year", `Y + "-11-11"`, "Shuts down on November 11"},
		{"next year", `(Y + 1) + "-02-01"`, "Shuts down on February 1, {Y+1}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := browserInit(t, ts.URL, 1440, 900, false, catalogInit("en", `(c) => {
			  const Y = new Date().getUTCFullYear();
			  for (const m of c.models) if (m.deprecated) m.shuts_down = `+tc.day+`;
			  return c;
			}`))
			err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
				_, err := cdp.Call(ctx, tg, emulation.SetTimezoneOverride, emulation.SetTimezoneOverrideParams{TimezoneID: "America/New_York"})
				return err
			}))
			if err != nil {
				t.Fatal(err)
			}
			got := js[map[string]any](t, ctx, pageHelpers+pickerHelpers+`
			await toVideo();
			await chooseFam("Veo");
			return { note: document.querySelector("#how .mnote")?.textContent || "", year: new Date().getUTCFullYear() };`)
			want := strings.ReplaceAll(tc.want, "{Y+1}", strconv.Itoa(int(got["year"].(float64))+1))
			if note, _ := got["note"].(string); !strings.HasPrefix(note, want) {
				t.Errorf("the composer says %q, want it to start with %q", note, want)
			}
			noErrors(t, ctx)
		})
	}
}

// TestBrowserShutDownSentence checks the line of a model the maker closes
// ends as its language ends a sentence: Chinese and Japanese with "。", which
// the code once followed with a Latin full stop, and Russian with one full
// stop when the date ends in its own ("2027 г."). In the composer it is the
// sentence and the button to the successor, with no space after "。"; in the
// picker the successor is named in it.
func TestBrowserShutDownSentence(t *testing.T) {
	ts := fakeAPI(t)
	for _, tc := range []struct {
		lang, day                  string
		composer, picker, stranger string
	}{
		{"en", `new Date().getUTCFullYear() + "-11-11"`, `^Shuts down on November 11\.$`, `^Shuts down on November 11\. Gemini Omni 1\.1 Flash replaces it\.$`, ``},
		{"en", `undefined`, `^Google is closing this model\.$`, `^Google is closing this model\. Gemini Omni 1\.1 Flash replaces it\.$`, ``},
		{"zh", `new Date().getUTCFullYear() + "-11-11"`, `^将于 11月11日 停用。$`, `^将于 11月11日 停用，由 Gemini Omni 1\.1 Flash 取代。$`, `停用\.`},
		{"ja", `undefined`, `^Google はこのモデルを終了します。$`, `^Google はこのモデルを終了します。Gemini Omni 1\.1 Flash に置き換わります。$`, `す\.`},
		{"ru", `(new Date().getUTCFullYear() + 1) + "-02-01"`, `^Отключат 1 февраля \d{4} г\.$`, `^Отключат 1 февраля \d{4} г\., замена: Gemini Omni 1\.1 Flash\.$`, `\.\.`},
	} {
		t.Run(tc.lang+" "+map[bool]string{true: "day", false: "no day"}[tc.day != "undefined"], func(t *testing.T) {
			ctx := browserInit(t, ts.URL, 1440, 900, false, catalogInit(tc.lang, `(c) => {
			  for (const m of c.models) if (m.deprecated) m.shuts_down = `+tc.day+`;
			  return c;
			}`))
			got := js[map[string]string](t, ctx, pageHelpers+pickerHelpers+`
			await toVideo();
			await chooseFam("Veo");
			const note = document.querySelector("#how .mnote > span");
			const raw = [...note.childNodes].filter((n) => n.nodeType === 3).map((n) => n.data).join(""), composer = raw.trim();
			await openPicker();
			return { raw, composer, button: note.querySelector("button")?.textContent || "", picker: document.querySelector("#pkd .dwarn")?.textContent || "" };`)
			if strings.HasSuffix(got["raw"], "。 ") {
				t.Errorf("the composer puts a space after %q, before the button", got["composer"])
			}
			for _, c := range []struct{ name, got, want string }{{"composer", got["composer"], tc.composer}, {"picker", got["picker"], tc.picker}} {
				if !regexp.MustCompile(c.want).MatchString(c.got) {
					t.Errorf("%s says %q, want %s", c.name, c.got, c.want)
				}
				if tc.stranger != "" && regexp.MustCompile(tc.stranger).MatchString(c.got) {
					t.Errorf("%s ends a sentence with a stray full stop: %q", c.name, c.got)
				}
			}
			if got["button"] == "" {
				t.Errorf("the composer has no button to the successor")
			}
			noErrors(t, ctx)
		})
	}
}

// quoteByOffer changes the estimates the quote answers with: shape is a page
// function of an estimate and the offer it is for.
func quoteByOffer(lang, shape string) string {
	return `
try { localStorage.setItem("framy.lang", ` + jsString(lang) + `); } catch (e) {}
{
  const shape = ` + shape + `, fetch0 = window.fetch;
  window.fetch = async (u, o) => {
    const r = await fetch0(u, o);
    if (!String(u).endsWith("/api/quote")) return r;
    const b = await r.json();
    for (const it of b.items || []) it.estimate = shape(it.estimate, it.offer);
    return new Response(JSON.stringify(b), { status: r.status, headers: { "Content-Type": "application/json" } });
  };
}
`
}

// TestBrowserPickerFromUpTo checks a family whose cheapest variant is priced
// "up to": its row says "from" the least that variant costs, not the most,
// in dollars and in the person's currency alike.
func TestBrowserPickerFromUpTo(t *testing.T) {
	ts := fakeAPI(t)
	for _, tc := range []struct {
		lang, shape, want string
	}{
		{"en", `(e, offer) => !offer.includes("qwen/") ? e : offer.endsWith("/qwen-image-3")
		  ? { kind: "up_to", usd: 0.09, max_usd: 0.19, currency: "USD", amount: 0.09, max_amount: 0.19, label: "$0.19" }
		  : { kind: "about", usd: 0.30, max_usd: 0.30, currency: "USD", amount: 0.30, max_amount: 0.30, label: "$0.30" }`, "from $0.09"},
		{"uk", `(e, offer) => !offer.includes("qwen/") ? e : offer.endsWith("/qwen-image-3")
		  ? { kind: "up_to", usd: 0.09, max_usd: 0.19, currency: "UAH", amount: 3.735, max_amount: 7.885, label: "7,89 ₴" }
		  : { kind: "about", usd: 0.30, max_usd: 0.30, currency: "UAH", amount: 12.45, max_amount: 12.45, label: "12,45 ₴" }`, "від 3,74 ₴"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			ctx := browserInit(t, ts.URL, 1440, 900, false, quoteByOffer(tc.lang, tc.shape))
			got := js[map[string]string](t, ctx, pageHelpers+pickerHelpers+`
			await openPicker();
			const row = famRow("Qwen Image"), b = row.querySelector(".fprice b > .q");
			for (let i = 0; i < 40 && b.classList.contains("wait"); i++) await wait(100);
			return { price: b.textContent, hint: b.title || "" };`)
			if got["price"] != tc.want {
				t.Errorf("the row says %q, want %q", got["price"], tc.want)
			}
			if tc.lang == "uk" && got["hint"] != "$0.09" {
				t.Errorf("the dollars behind it are %q, want $0.09", got["hint"])
			}
			noErrors(t, ctx)
		})
	}
}

// TestBrowserPickerKeyboard checks the picker from the keyboard: it opens
// with the search in focus; the card on the right follows the row and the
// variant in focus, as it follows the mouse; a screen reader hears a row
// with its facts and price and a variant by its whole name with its price;
// and a model chosen gives the focus back to the model in the composer.
func TestBrowserPickerKeyboard(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 1440, 900, false)
	js[bool](t, ctx, pageHelpers+`document.querySelector("#how .mhead").focus(); return true;`)
	enter(t, ctx)
	got := js[map[string]any](t, ctx, pageHelpers+pickerHelpers+`
	await wait(700);
	const at = document.activeElement.id;
	const row = famRow("FLUX"), hit = row.querySelector(".fhit");
	hit.focus();
	await wait(100);
	const card = document.querySelector("#pkd h4")?.textContent || "";
	for (let i = 0; i < 40 && row.querySelector(".q.wait"); i++) await wait(100);
	const said = (el) => (el.getAttribute("aria-describedby") || "").split(" ").filter(Boolean).map((id) => document.getElementById(id)?.textContent.trim() || "?" + id).join(" | ");
	const pill = [...row.querySelectorAll(".pill")].find((p) => p.getAttribute("aria-pressed") !== "true" && p.textContent.trim() !== "3");
	pill.focus();
	await wait(100);
	const chosen = document.querySelector("#pkd .vlist [aria-checked=true] b")?.textContent || "";
	return { at, card, hit: said(hit), pill: pill.textContent, pillName: pill.getAttribute("aria-label") || "", pillSaid: said(pill), chosen };`)
	if got["at"] != "pksearch" {
		t.Errorf("the picker opens with the focus on %q, want the search", got["at"])
	}
	if got["card"] != "FLUX" {
		t.Errorf("with the focus on FLUX's row the card shows %q", got["card"])
	}
	if s, _ := got["hit"].(string); !regexp.MustCompile(`·.*\|.*\$\d`).MatchString(s) {
		t.Errorf("FLUX's row is described as %q, want its facts and its price", s)
	}
	if got["pillName"] != "FLUX "+got["pill"].(string) {
		t.Errorf("the variant %q is named %q, want its whole name", got["pill"], got["pillName"])
	}
	if s, _ := got["pillSaid"].(string); !regexp.MustCompile(`\$\d|^Free$`).MatchString(s) {
		t.Errorf("the variant %q is described as %q, want its price", got["pill"], s)
	}
	if got["chosen"] != got["pill"] {
		t.Errorf("with the focus on the variant %q the card shows %q", got["pill"], got["chosen"])
	}
	// An outdated variant, shown when the list goes by price, says so to a
	// screen reader as its title says to the mouse, and its price.
	old := js[map[string]any](t, ctx, pageHelpers+`
	document.querySelector("#pksort [data-v=price]").click();
	await wait(900);
	const p = document.querySelector("#pkrows .pill.old");
	if (!p) return { none: true };
	p.focus();
	await wait(100);
	for (let i = 0; i < 40 && document.querySelector("#pkrows .q.wait"); i++) await wait(100);
	const said = (p.getAttribute("aria-describedby") || "").split(" ").map((id) => document.getElementById(id)?.textContent.trim() || "?" + id).join(" | ");
	const out = { title: p.title, said };
	document.querySelector("#pksort [data-v=maker]").click();
	await wait(900);
	return out;`)
	if old["none"] == true {
		t.Fatal("no outdated variant in the list by price")
	}
	if s, _ := old["said"].(string); !strings.HasPrefix(s, old["title"].(string)+" | ") || !regexp.MustCompile(`\$\d|Free$`).MatchString(s) {
		t.Errorf("an outdated variant is described as %q, want %q and its price", s, old["title"])
	}
	js[bool](t, ctx, pageHelpers+pickerHelpers+`famRow("FLUX").querySelector(".fhit").focus(); return true;`)
	enter(t, ctx)
	back := js[map[string]any](t, ctx, pageHelpers+`
	await wait(900);
	const a = document.activeElement;
	return { open: !document.querySelector("#picker").hidden, mhead: a.matches("#how .mhead") && a.isConnected, name: document.querySelector("#how .mname")?.textContent || "" };`)
	if back["open"].(bool) || !strings.HasPrefix(back["name"].(string), "FLUX") {
		t.Fatalf("Enter on FLUX's row did not choose it: %v", back)
	}
	if !back["mhead"].(bool) {
		t.Errorf("after a model is chosen the focus is not on the model in the composer")
	}
	noErrors(t, ctx)
}

// TestBrowserPickerPhoneNoKeyboard checks the picker on a touch screen does
// not open with the search in focus, which would bring up the keyboard over
// the list.
func TestBrowserPickerPhoneNoKeyboard(t *testing.T) {
	ts := fakeAPI(t)
	ctx := browser(t, ts.URL, 390, 844, true)
	got := js[string](t, ctx, pageHelpers+pickerHelpers+`await openPicker(); await wait(300); return document.activeElement.tagName;`)
	if got == "INPUT" {
		t.Errorf("the picker opens on a phone with a field in focus")
	}
	noErrors(t, ctx)
}

// TestBrowserPickerDescriptionInEnglishOnly checks the provider's
// description of a model, which is English, is shown in the English page
// and not in a page in another language.
func TestBrowserPickerDescriptionInEnglishOnly(t *testing.T) {
	ts := fakeAPI(t)
	for _, tc := range []struct {
		lang string
		want bool
	}{{"en", true}, {"ru", false}, {"ja", false}} {
		t.Run(tc.lang, func(t *testing.T) {
			ctx := browserInit(t, ts.URL, 1440, 900, false, catalogInit(tc.lang, `(c) => c`))
			got := js[map[string]any](t, ctx, pageHelpers+pickerHelpers+`
			await openPicker();
			famRow("FLUX").querySelector(".fhit").focus();
			await wait(100);
			return { card: document.querySelector("#pkd h4")?.textContent || "", desc: !!document.querySelector("#pkd .ddesc") };`)
			if got["card"] != "FLUX" {
				t.Fatalf("the card shows %v, not FLUX", got["card"])
			}
			if got["desc"].(bool) != tc.want {
				t.Errorf("the card has the provider's description: %v, want %v", got["desc"], tc.want)
			}
			noErrors(t, ctx)
		})
	}
}
