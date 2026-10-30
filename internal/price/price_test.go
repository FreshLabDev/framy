// SPDX-License-Identifier: Apache-2.0
package price

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	catalogfiles "github.com/FreshLabDev/framy/catalog"
	"github.com/FreshLabDev/framy/internal/catalog"
)

// The catalog's fixtures: OpenRouter's answers of 27 October 2026.
const fixtures = "../catalog/testdata/openrouter"

var (
	snapOnce sync.Once
	snap     catalog.Snapshot
	snapErr  error
)

// fixtureCatalog builds the catalog from the fixtures and the embedded
// overlay, as the sync does.
func fixtureCatalog(t *testing.T) catalog.Snapshot {
	t.Helper()
	snapOnce.Do(func() {
		listing := map[string]string{"/api/v1/models": "models.json", "/api/v1/images/models": "images.json", "/api/v1/videos/models": "videos.json"}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if name, ok := listing[r.URL.Path]; ok {
				http.ServeFile(w, r, filepath.Join(fixtures, name))
				return
			}
			id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/images/models/"), "/endpoints")
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(fixtures, "endpoints", strings.ReplaceAll(id, "/", "_")+".json"))
		}))
		defer srv.Close()
		var up catalog.Upstream
		up, snapErr = (&catalog.OpenRouterSource{Base: srv.URL + "/api/v1", HTTP: srv.Client(), Parallel: 4}).Fetch(context.Background())
		if snapErr != nil {
			return
		}
		var files catalog.Files
		if files, snapErr = catalog.LoadFiles(catalogfiles.FS); snapErr != nil {
			return
		}
		snap = catalog.Build(up, files, time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC))
	})
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	return snap
}

func offer(t *testing.T, id string) (catalog.Model, catalog.Offer) {
	t.Helper()
	for _, m := range fixtureCatalog(t).Models {
		for _, of := range m.Offers {
			if of.ID == id {
				return m, of
			}
		}
	}
	t.Fatalf("offer %s is not in the catalog", id)
	return catalog.Model{}, catalog.Offer{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

type set = map[string]any

func images(n int) []Image { return slices.Repeat([]Image{{Width: 1024, Height: 1024}}, n) }

func TestEstimates(t *testing.T) {
	clip := &Video{Seconds: 5, Width: 1280, Height: 720}
	for _, tc := range []struct {
		name     string
		offer    string
		req      Request
		usd, max float64
		kind     Kind
	}{
		// OpenAI's table for gpt-image-2: $0.006, $0.053 and $0.211 square;
		// $0.005, $0.041 and $0.165 at 1024×1536.
		{"gpt-image-2 low square", "openai:gpt-image-2", Request{Settings: set{"size": "1024x1024", "quality": "low"}}, 196 * 3e-5, 0, About},
		{"gpt-image-2 medium square", "openai:gpt-image-2", Request{Settings: set{"size": "1024x1024", "quality": "medium"}}, 1756 * 3e-5, 0, About},
		{"gpt-image-2 high square", "openai:gpt-image-2", Request{Settings: set{"size": "1024x1024", "quality": "high"}}, 7024 * 3e-5, 0, About},
		{"gpt-image-2 low portrait", "openai:gpt-image-2", Request{Settings: set{"size": "1024x1536", "quality": "low"}}, 158 * 3e-5, 0, About},
		{"gpt-image-2 medium portrait", "openai:gpt-image-2", Request{Settings: set{"size": "1024x1536", "quality": "medium"}}, 1372 * 3e-5, 0, About},
		{"gpt-image-2 high portrait", "openai:gpt-image-2", Request{Settings: set{"size": "1024x1536", "quality": "high"}}, 5488 * 3e-5, 0, About},
		// OpenRouter's example: high at 16:9 is 1536×864 and $0.13.
		{"gpt-image-2 on OpenRouter at 16:9", "openrouter:openai/gpt-image-2", Request{Settings: set{"aspect_ratio": "16:9", "quality": "high"}}, 4312 * 3e-5, 0, About},
		{"gpt-image-2.5 with no quality chosen", "openai:gpt-image-2.5-sunburst", Request{Settings: set{"size": "1024x1024"}}, 196 * 3e-5, 7024 * 3e-5, UpTo},

		// Google's pricing page: Nano Banana 2.1 at 1K is $0.0336, at 4K $0.113.
		{"nano banana direct 1K", "gemini:gemini-nano-banana-2.1", Request{Settings: set{"resolution": "1K"}}, 1120 * 3e-5, 0, About},
		{"nano banana on OpenRouter 4K", "openrouter:google/gemini-nano-banana-2.1", Request{Settings: set{"resolution": "4K"}}, 3780 * 3e-5, 0, About},
		// Three upstreams; Vertex in the US asks $66 a million instead of $60.
		{"nano banana 2 at three upstreams", "openrouter:google/gemini-3.1-flash-image", Request{Settings: set{"resolution": "1K"}}, 1120 * 6e-5, 1120 * 6.6e-5, About},
		{"nano banana pro with a reference", "openrouter:google/gemini-3-pro-image", Request{Settings: set{"resolution": "2K"}, Images: images(1)}, 1120*1.2e-4 + 560*2e-6, 0, About},
		{"mai flash with a prompt", "openrouter:microsoft/mai-image-2.6-flash", Request{Settings: set{"aspect_ratio": "1:1"}, Prompt: "кот в шляпе"}, 1024*1.9e-5 + 6*1.75e-6, 0, About},
		// Measured live: a 1024x1024 photo took about 1,100 input tokens, a
		// 2048x1152 one about 2,470; the overlay sets 1,100 a megapixel.
		{"mai flash editing a square photo", "openrouter:microsoft/mai-image-2.6-flash", Request{Settings: set{"aspect_ratio": "1:1"}, Images: images(1)}, 1024*1.9e-5 + 1154*2.5e-6, 0, About},
		{"mai flash editing a wide photo", "openrouter:microsoft/mai-image-2.6-flash", Request{Settings: set{"aspect_ratio": "1:1"}, Images: []Image{{Width: 2048, Height: 1152}}}, 1024*1.9e-5 + 2596*2.5e-6, 0, About},

		{"flux 3 at 1K", "openrouter:black-forest-labs/flux-3-image", Request{Settings: set{"resolution": "1K"}}, 0.048, 0, Exact},
		{"flux 3 at 4K", "openrouter:black-forest-labs/flux-3-image", Request{Settings: set{"resolution": "4K"}}, 0.607, 0, Exact},
		{"flux 2 pro by the megapixel", "openrouter:black-forest-labs/flux.2-pro", Request{Settings: set{"aspect_ratio": "1:1"}}, 0.03 * 1.048576, 0, About},
		// docs/research/images.md, 5.2: 8 references at 2K cost 0.06 + 8 × 0.07.
		{"k3 edit at 2K with 8 references", "openrouter:krea/k3-edit", Request{Settings: set{"resolution": "2K"}, Images: images(8)}, 0.62, 0, Exact},
		{"k3 edit at 1K with 8 references", "openrouter:krea/k3-edit", Request{Settings: set{"resolution": "1K"}, Images: images(8)}, 0.19, 0, Exact},
		{"krea without style references", "openrouter:krea/krea-2-medium", Request{Settings: set{"resolution": "1K"}}, 0.03, 0, Exact},
		{"recraft styles once per request", "openrouter:recraft/recraft-v4-styles", Request{Images: images(1)}, 0.04, 0, Exact},
		{"grok low at 2K with a reference", "openrouter:x-ai/grok-imagine-image-2.0", Request{Settings: set{"quality": "low", "resolution": "2K"}, Images: images(1)}, 0.07, 0, Exact},
		// What high_resolution is, Seedream does not say.
		{"seedream pro", "openrouter:bytedance-seed/seedream-5-0-pro", Request{Settings: set{"resolution": "1K"}, Images: images(1)}, 0.048, 0.093, UpTo},
		{"riverflow 2.5 is a floor", "openrouter:sourceful/riverflow-v2.5-pro", Request{Settings: set{"resolution": "2K"}}, 0.15, 0.15, From},
		{"riverflow 2 without super-resolution references", "openrouter:sourceful/riverflow-v2-pro", Request{Settings: set{"resolution": "1K"}, Images: images(2)}, 0.15, 0, Exact},

		// BytePlus's example: Seedance 2.0 at 1080p for 5 s is $1.87.
		{"seedance 2.0 at 1080p", "openrouter:bytedance/seedance-2.0", Request{Settings: set{"resolution": "1080p", "aspect_ratio": "16:9", "duration": 5}}, 1920 * 1080 * 24 * 5 / 1024 * 7.7e-6, 0, Exact},
		{"seedance 2.0 at 4K", "openrouter:bytedance/seedance-2.0", Request{Settings: set{"resolution": "4K", "aspect_ratio": "16:9", "duration": 5}}, 3840 * 2160 * 24 * 5 / 1024 * 4e-6, 0, Exact},
		{"seedance 2.5 square at 480p keeps the area", "openrouter:bytedance/seedance-2.5", Request{Settings: set{"resolution": "480p", "aspect_ratio": "1:1", "duration": 5}}, 640 * 640 * 24 * 5 / 1024 * 1.07e-5, 0, Exact},
		// BytePlus bills the source video's seconds too; until OpenRouter is
		// checked, a clip makes the price a floor whose top counts them.
		{"seedance 2.5 editing a video", "openrouter:bytedance/seedance-2.5", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5}, Video: clip}, 1280 * 720 * 24 * 5 / 1024 * 6.4e-6, 1280 * 720 * 24 * 10 / 1024 * 6.4e-6, From},
		// Without sound is Seedance's plain rate, so it costs what with sound
		// does: with a clip, the rate with a video input.
		{"seedance mini silent with a clip", "openrouter:bytedance/seedance-2.0-mini", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}, Video: clip}, 1280 * 720 * 24 * 5 / 1024 * 2.1e-6, 1280 * 720 * 24 * 10 / 1024 * 2.1e-6, From},
		{"seedance mini with sound and a clip", "openrouter:bytedance/seedance-2.0-mini", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": true}, Video: clip}, 1280 * 720 * 24 * 5 / 1024 * 2.1e-6, 1280 * 720 * 24 * 10 / 1024 * 2.1e-6, From},
		{"seedance mini silent from text", "openrouter:bytedance/seedance-2.0-mini", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}}, 1280 * 720 * 24 * 5 / 1024 * 3.5e-6, 0, Exact},
		{"seedance 2.0 silent at 1080p with a clip", "openrouter:bytedance/seedance-2.0", Request{Settings: set{"resolution": "1080p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}, Video: clip}, 1920 * 1080 * 24 * 5 / 1024 * 4.7e-6, 1920 * 1080 * 24 * 10 / 1024 * 4.7e-6, From},
		{"seedance 1.5 without sound", "openrouter:bytedance/seedance-1-5-pro", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}}, 1280 * 720 * 24 * 5 / 1024 * 1.2e-6, 0, Exact},
		{"veo fast 720p with sound", "openrouter:google/veo-3.1-fast", Request{Settings: set{"resolution": "720p", "duration": 8, "generate_audio": true}}, 0.80, 0, Exact},
		{"veo fast 1080p without sound", "openrouter:google/veo-3.1-fast", Request{Settings: set{"resolution": "1080p", "duration": 8, "generate_audio": false}}, 0.80, 0, Exact},
		{"veo fast 4K with sound", "openrouter:google/veo-3.1-fast", Request{Settings: set{"resolution": "4K", "duration": 8, "generate_audio": true}}, 2.40, 0, Exact},
		// Kling's sound is +50%, whatever the mode and the resolution: the
		// mode's SKUs are its plain rate.
		{"kling std with sound", "openrouter:kwaivgi/kling-v3.0-std", Request{Settings: set{"resolution": "720p", "duration": 5, "generate_audio": true}}, 0.63, 0, Exact},
		{"kling pro with sound from a frame", "openrouter:kwaivgi/kling-v3.0-pro", Request{Settings: set{"resolution": "1080p", "duration": 5, "generate_audio": true}, Frames: 1}, 0.84, 0, Exact},
		{"kling std without sound", "openrouter:kwaivgi/kling-v3.0-std", Request{Settings: set{"resolution": "720p", "duration": 5, "generate_audio": false}}, 0.42, 0, Exact},
		{"wan 2.6 from a frame at 1080p", "openrouter:alibaba/wan-2.6", Request{Settings: set{"resolution": "1080p", "duration": 5}, Frames: 1}, 0.75, 0, Exact},
		{"wan 2.6 from text at 720p", "openrouter:alibaba/wan-2.6", Request{Settings: set{"resolution": "720p", "duration": 10}}, 0.80, 0, Exact},
		{"heygen at 768p", "openrouter:heygen/heygen-video-1", Request{Settings: set{"resolution": "768p", "duration": 5}}, 0.18, 0, Exact},
		// The first two reference images are free.
		{"heygen with two references", "openrouter:heygen/heygen-video-1", Request{Settings: set{"resolution": "768p", "duration": 5}, Images: images(2)}, 0.50, 0, Exact},
		{"heygen with three references", "openrouter:heygen/heygen-video-1", Request{Settings: set{"resolution": "768p", "duration": 5}, Images: images(3)}, 0.54, 0, Exact},
		{"grok lite from a frame", "openrouter:x-ai/grok-imagine-video-1.5-lite", Request{Settings: set{"resolution": "720p", "duration": 5}, Frames: 1}, 0.16, 0, Exact},
		// The first five reference images are free.
		{"hailuo 3 with a reference", "openrouter:minimax/hailuo-3", Request{Settings: set{"duration": 5}, Images: images(1)}, 0.65, 0, Exact},
		{"hailuo 3 with seven references", "openrouter:minimax/hailuo-3", Request{Settings: set{"duration": 5}, Images: images(7)}, 0.73, 0, Exact},
		{"flux 3 video at 1080p", "openrouter:black-forest-labs/flux-3-video", Request{Settings: set{"resolution": "1080p", "duration": 5}}, 1.45, 0, Exact},
		{"flux 3 video continuing a clip", "openrouter:black-forest-labs/flux-3-video", Request{Settings: set{"resolution": "1080p", "duration": 5}, Video: clip}, 2.65, 0, Exact},

		{"aleph's minimum", "openrouter:runway/aleph-2", Request{Video: &Video{Seconds: 1, Width: 1280, Height: 720}}, 0.56, 0, About},
		{"aleph on 5 s", "openrouter:runway/aleph-2", Request{Video: clip}, 1.40, 0, About},
		{"the free video edit", "openrouter:black-forest-labs/flux-video-edit", Request{Video: clip}, 0.15, 0, Exact},
		{"upscale by two", "openrouter:black-forest-labs/flux-video-upscale", Request{Settings: set{"upscale_factor": 2}, Video: clip}, 0.9216 * 4 * 5 * 0.035, 0.9216 * 4 * 5 * 0.05, UpTo},
		// Omni picks 3 to 10 s at 5,792 tokens a second of 720p.
		{"omni at 720p", "gemini:gemini-omni-1.1-flash", Request{Settings: set{"resolution": "720p"}}, 5792 * 3 * 1.75e-5, 5792 * 10 * 1.75e-5, UpTo},
		// Extending a 5 s clip may come back, and be billed, as 15 s.
		{"omni extending a clip", "gemini:gemini-omni-1.1-flash", Request{Settings: set{"resolution": "720p"}, Video: clip}, 5792 * 3 * 1.75e-5, 5792 * 15 * 1.75e-5, UpTo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, of := offer(t, tc.offer)
			e := Of(m, of, tc.req)
			want := tc.max
			if want == 0 {
				want = tc.usd
			}
			if !near(e.USD, tc.usd) || !near(e.MaxUSD, want) || e.Kind != tc.kind {
				t.Errorf("estimate = $%.6f up to $%.6f, %s; want $%.6f up to $%.6f, %s\nlines %+v\nunknown %v", e.USD, e.MaxUSD, e.Kind, tc.usd, want, tc.kind, e.Lines, e.Unknown)
			}
		})
	}
}

// Seedance with a clip is a floor until OpenRouter's bill of the clip's
// seconds is checked, and says why; from words alone it stays exact.
func TestSeedanceWithAClipSaysWhyItIsAFloor(t *testing.T) {
	m, of := offer(t, "openrouter:bytedance/seedance-2.0-mini")
	settings := set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5}
	e := Of(m, of, Request{Settings: settings, Video: &Video{Seconds: 15, Width: 1280, Height: 720}})
	if e.Kind != From || !strings.Contains(e.Note, "source video") {
		t.Errorf("with a clip: %s, note %q", e.Kind, e.Note)
	}
	if e := Of(m, of, Request{Settings: settings}); e.Kind != Exact || e.Note != "" {
		t.Errorf("from words: %s, note %q", e.Kind, e.Note)
	}
}

func TestPublishedPrices(t *testing.T) {
	// The figures as OpenAI, OpenRouter and BytePlus print them, rounded to
	// a tenth of a cent or to a cent: the estimate has to round to them.
	for _, tc := range []struct {
		offer     string
		set       set
		usd, step float64
	}{
		{"openai:gpt-image-2", set{"size": "1024x1024", "quality": "low"}, 0.006, 0.001},
		{"openai:gpt-image-2", set{"size": "1024x1024", "quality": "medium"}, 0.053, 0.001},
		{"openai:gpt-image-2", set{"size": "1024x1024", "quality": "high"}, 0.211, 0.001},
		{"openai:gpt-image-2", set{"size": "1024x1536", "quality": "low"}, 0.005, 0.001},
		{"openai:gpt-image-2", set{"size": "1024x1536", "quality": "medium"}, 0.041, 0.001},
		{"openai:gpt-image-2", set{"size": "1536x1024", "quality": "high"}, 0.165, 0.001},
		{"openrouter:openai/gpt-image-2", set{"aspect_ratio": "16:9", "quality": "high"}, 0.13, 0.01},
		{"openrouter:bytedance/seedance-2.0", set{"resolution": "1080p", "aspect_ratio": "16:9", "duration": 5}, 1.87, 0.01},
		// Tencent's 20,000 tokens at 4K at OpenRouter's rate.
		{"openrouter:tencent/hy-image-v3.5-preview", set{"resolution": "4K"}, 0.032, 0.001},
	} {
		m, of := offer(t, tc.offer)
		if e := Of(m, of, Request{Settings: tc.set}); math.Abs(e.USD-tc.usd) > tc.step/2 {
			t.Errorf("%s %v = $%.4f, published $%g", tc.offer, tc.set, e.USD, tc.usd)
		}
	}
}

func TestUnknownOutput(t *testing.T) {
	// Omni has no count for 1080p.
	m, of := offer(t, "gemini:gemini-omni-1.1-flash")
	if e := Of(m, of, Request{Settings: set{"resolution": "1080p"}}); e.Kind != Unknown {
		t.Errorf("omni at 1080p = %+v", e)
	}
	// An editor without a video has nothing to price.
	m, of = offer(t, "openrouter:black-forest-labs/flux-video-edit")
	if e := Of(m, of, Request{}); e.Kind != Unknown {
		t.Errorf("flux video edit with no video = %+v", e)
	}
}

func TestEstimateKeepsTheRatesItUsed(t *testing.T) {
	m, of := offer(t, "openrouter:x-ai/grok-imagine-video-1.5-lite")
	e := Of(m, of, Request{Settings: set{"resolution": "720p", "duration": 5}, Frames: 1})
	want := []Line{
		{Billable: "cents_per_video_output_second_720p", Unit: "second", Quantity: 5, RateUSD: 0.03, USD: 0.15},
		{Billable: "cents_per_image_input", Unit: "image", Quantity: 1, RateUSD: 0.01, USD: 0.01},
	}
	if len(e.Lines) != len(want) {
		t.Fatalf("lines = %+v", e.Lines)
	}
	for i, l := range e.Lines {
		w := want[i]
		if l.Billable != w.Billable || l.Unit != w.Unit || !near(l.Quantity, w.Quantity) || !near(l.RateUSD, w.RateUSD) || !near(l.USD, w.USD) {
			t.Errorf("line %d = %+v, want %+v", i, l, w)
		}
	}
}

func TestCheckReadsTheWholeCatalog(t *testing.T) {
	if got := Check(fixtureCatalog(t)); len(got) != 0 {
		t.Fatalf("the estimate cannot read:\n%s", strings.Join(got, "\n"))
	}
	broken := catalog.Snapshot{Models: []catalog.Model{{ID: "a/b", Offers: []catalog.Offer{{ID: "openrouter:a/b", Routes: []catalog.Route{{
		Prices: []catalog.Price{{Billable: "output_hologram", Unit: "image", CostUSD: 1}},
		SKUs:   map[string]string{"cents_per_vibe": "3"},
	}}}}}}}
	if got := Check(broken); len(got) != 2 {
		t.Fatalf("check = %v, want the billable and the SKU", got)
	}
}

// Every offer people can pick gets an estimate for its first settings,
// whatever they are, and only the known gaps have no price before the run.
func TestEveryOfferHasAnEstimate(t *testing.T) {
	gaps := map[string]bool{}
	for _, m := range fixtureCatalog(t).Models {
		if m.Hidden != "" {
			continue
		}
		for _, of := range m.Offers {
			if of.Hidden != "" {
				continue
			}
			req := Request{Settings: set{}, Prompt: "a fox in the snow"}
			for name, p := range of.Params {
				switch {
				case p.Type == catalog.Enum && name == "resolution" && slices.ContainsFunc(p.Values, func(v any) bool { return fmt.Sprint(v) == "720p" }):
					req.Settings[name] = "720p"
				case p.Type == catalog.Enum && len(p.Values) > 0:
					req.Settings[name] = p.Values[0]
				case p.Type == catalog.Range && p.Min != nil:
					req.Settings[name] = *p.Min
				}
			}
			if s := m.Traits.SourceVideo; s != nil && s.Required {
				req.Video = &Video{Seconds: 5, Width: 1280, Height: 720}
			}
			e := Of(m, of, req)
			if math.IsNaN(e.USD) || e.USD < 0 || e.MaxUSD < e.USD {
				t.Errorf("%s: %+v", of.ID, e)
			}
			if (e.Kind == Unknown) != gaps[of.ID] {
				t.Errorf("%s: kind %s, unknown %v", of.ID, e.Kind, e.Unknown)
			}
		}
	}
}

func TestDiscountComesOffTheTotal(t *testing.T) {
	m, of := offer(t, "openrouter:heygen/heygen-video-1")
	req := Request{Settings: set{"resolution": "768p", "duration": 5}}
	listed := Of(m, of, req)
	if listed.Discount != 0 || listed.ListUSD != 0 || !near(listed.USD, 0.18) {
		t.Fatalf("without a discount = %+v", listed)
	}

	of.Routes = slices.Clone(of.Routes)
	of.Routes[0].Discount = 0.5
	e := Of(m, of, req)
	if !near(e.USD, 0.09) || !near(e.MaxUSD, 0.09) || !near(e.ListUSD, 0.18) || !near(e.ListMaxUSD, 0.18) || e.Discount != 0.5 || e.Kind != Exact {
		t.Fatalf("with 50%% off = %+v", e)
	}
	// The lines keep the listed rate, which is what the catalog publishes.
	if len(e.Lines) != 1 || !near(e.Lines[0].RateUSD, 0.036) || !near(e.Lines[0].USD, 0.18) {
		t.Fatalf("lines = %+v", e.Lines)
	}

	// A broken answer is no discount.
	of.Routes[0].Discount = 1.5
	if e := Of(m, of, req); e.Discount != 0 || !near(e.USD, 0.18) {
		t.Fatalf("a discount of 150%% was believed: %+v", e)
	}
}

func TestDiscountPicksTheCheapestRoute(t *testing.T) {
	m, of := offer(t, "openrouter:google/gemini-3.1-flash-image")
	req := Request{Settings: set{"resolution": "1K", "aspect_ratio": "1:1"}}
	before := Of(m, of, req)
	of.Routes = slices.Clone(of.Routes)
	dear := -1
	for i, r := range of.Routes {
		if r.Provider != before.Route {
			dear = i
			break
		}
	}
	if dear < 0 {
		t.Fatal("no second route")
	}
	of.Routes[dear].Discount = 0.9
	e := Of(m, of, req)
	if e.Route != of.Routes[dear].Provider || e.Discount != 0.9 || e.USD >= before.USD {
		t.Fatalf("a route at 90%% off was not taken: %+v, before %+v", e, before)
	}
	if e.ListMaxUSD < e.MaxUSD {
		t.Fatalf("listed most %v below the most %v", e.ListMaxUSD, e.MaxUSD)
	}
}
