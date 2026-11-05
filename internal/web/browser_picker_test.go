// SPDX-License-Identifier: Apache-2.0

//go:build browser

package web

import (
	"context"
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
