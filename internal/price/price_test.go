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
	clip := &Video{Seconds: 5, Width: 1280, Height: 720, FPS: 24}
	// 720p and 1080p in Black Forest Labs' megapixels of 1,048,576 pixels.
	const hd, fhd = 1280.0 * 720 / (1 << 20), 1920.0 * 1080 / (1 << 20)
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
		// OpenAI's calculator: 2.6 takes 2.5's tokens times 4.2 at low and
		// 3.3 at medium, rounded, and the same from high up.
		{"gpt-image-2.6 low square", "openai:gpt-image-2.6-flare", Request{Settings: set{"size": "1024x1024", "quality": "low"}}, 823 * 3e-5, 0, About},
		{"gpt-image-2.6 medium square", "openai:gpt-image-2.6-sunburst", Request{Settings: set{"size": "1024x1024", "quality": "medium"}}, 1449 * 3e-5, 0, About},
		{"gpt-image-2.6 high square", "openai:gpt-image-2.6-sunburst", Request{Settings: set{"size": "1024x1024", "quality": "high"}}, 1756 * 3e-5, 0, About},
		{"gpt-image-2.6 on OpenRouter at 16:9", "openrouter:openai/gpt-image-2.6-flare", Request{Settings: set{"aspect_ratio": "16:9", "quality": "low"}}, 504 * 3e-5, 0, About},
		{"gpt-image-2.6 with no quality chosen", "openrouter:openai/gpt-image-2.6-sunburst", Request{Settings: set{"aspect_ratio": "1:1"}}, 823 * 3e-5, 7024 * 3e-5, UpTo},

		// Google's pricing page: Nano Banana 2.1 at 1K is $0.0336, at 4K $0.113.
		{"nano banana direct 1K", "gemini:gemini-nano-banana-2.1", Request{Settings: set{"resolution": "1K"}}, 1120 * 3e-5, 0, About},
		{"nano banana on OpenRouter 4K", "openrouter:google/gemini-nano-banana-2.1", Request{Settings: set{"resolution": "4K"}}, 3780 * 3e-5, 0, About},
		// Three upstreams; Vertex in the US asks $66 a million instead of $60.
		{"nano banana 2 at three upstreams", "openrouter:google/gemini-3.1-flash-image", Request{Settings: set{"resolution": "1K"}}, 1120 * 6e-5, 1120 * 6.6e-5, About},
		// An input picture takes 1,120 tokens at Google's default Input
		// detail on every Nano Banana, and OpenRouter bills the prompt at the
		// rate of its /api/v1/models (price.unlisted).
		{"nano banana pro with a reference", "openrouter:google/gemini-3-pro-image", Request{Settings: set{"resolution": "2K"}, Images: images(1)}, 1120*1.2e-4 + 1120*2e-6, 0, About},
		{"nano banana pro at low detail", "gemini:gemini-3-pro-image", Request{Settings: set{"resolution": "2K", "media_resolution": "low"}, Images: images(2)}, 1120*1.2e-4 + 2*280*2e-6, 0, About},
		{"nano banana direct with 14 references", "gemini:gemini-nano-banana-2.1", Request{Settings: set{"resolution": "1K"}, Images: images(14)}, 1120*3e-5 + 14*1120*1.5e-6, 0, About},
		{"nano banana on OpenRouter with a reference", "openrouter:google/gemini-nano-banana-2.1", Request{Settings: set{"resolution": "1K"}, Prompt: "кот в шляпе", Images: images(1)}, 1120*3e-5 + 1120*1.5e-6 + 6*1.5e-6, 0, About},
		{"nano banana 2 lite with frames", "openrouter:google/gemini-3.1-flash-lite-image", Request{Settings: set{"resolution": "1K"}, Images: images(3)}, 1120*3e-5 + 3*1120*2.5e-7, 0, About},
		// A second of video is a frame and its sound: 70 + 32 tokens, 280 + 32
		// at high detail; a clip is rounded up to whole seconds.
		{"nano banana from a clip", "gemini:gemini-nano-banana-2.1", Request{Settings: set{"resolution": "1K"}, Video: &Video{Seconds: 5.3, Width: 1280, Height: 720}}, 1120*3e-5 + 6*102*1.5e-6, 0, About},
		{"nano banana from a clip at high detail", "gemini:gemini-3.1-flash-image", Request{Settings: set{"resolution": "1K", "media_resolution": "high"}, Video: &Video{Seconds: 5, Width: 1280, Height: 720}}, 1120*6e-5 + 5*312*5e-7, 0, About},
		{"mai flash with a prompt", "openrouter:microsoft/mai-image-2.6-flash", Request{Settings: set{"aspect_ratio": "1:1"}, Prompt: "кот в шляпе"}, 1024*1.9e-5 + 6*1.75e-6, 0, About},
		// Measured live: a 1024x1024 photo took about 1,100 input tokens, a
		// 2048x1152 one about 2,470; the overlay sets 1,100 a megapixel.
		{"mai flash editing a square photo", "openrouter:microsoft/mai-image-2.6-flash", Request{Settings: set{"aspect_ratio": "1:1"}, Images: images(1)}, 1024*1.9e-5 + 1154*2.5e-6, 0, About},
		{"mai flash editing a wide photo", "openrouter:microsoft/mai-image-2.6-flash", Request{Settings: set{"aspect_ratio": "1:1"}, Images: []Image{{Width: 2048, Height: 1152}}}, 1024*1.9e-5 + 2596*2.5e-6, 0, About},
		// Job 53 of the shared schema, a run of the test bot: a 1280×853
		// photo took 1,080 tokens at $8 a million, beside 158 of a low
		// 1536×1024 output.
		{"gpt image flare editing a photo", "openrouter:openai/gpt-image-2.5-flare", Request{Settings: set{"aspect_ratio": "3:2", "quality": "low"}, Images: []Image{{Width: 1280, Height: 853}}}, 158*3e-5 + 1080*8e-6, 0, About},

		{"flux 3 at 1K", "openrouter:black-forest-labs/flux-3-image", Request{Settings: set{"resolution": "1K"}}, 0.048, 0, Exact},
		{"flux 3 at 4K", "openrouter:black-forest-labs/flux-3-image", Request{Settings: set{"resolution": "4K"}}, 0.607, 0, Exact},
		{"flux 2 pro by the megapixel", "openrouter:black-forest-labs/flux.2-pro", Request{Settings: set{"aspect_ratio": "1:1"}}, 0.03 * 1.048576, 0, About},
		// OpenRouter lists only the output of FLUX.2 Pro and Max; their pages
		// give the references' rate (price.unlisted).
		{"flux 2 pro editing two photos", "openrouter:black-forest-labs/flux.2-pro", Request{Settings: set{"aspect_ratio": "1:1"}, Images: []Image{{Width: 2048, Height: 1536}, {Width: 1024, Height: 1024}}}, 0.03*1.048576 + 0.015*(3.145728+1.048576), 0, About},
		{"flux 2 max editing a photo", "openrouter:black-forest-labs/flux.2-max", Request{Settings: set{"aspect_ratio": "1:1"}, Images: images(1)}, (0.07 + 0.03) * 1.048576, 0, About},
		{"flux 2 flex lists its input", "openrouter:black-forest-labs/flux.2-flex", Request{Settings: set{"aspect_ratio": "1:1"}, Images: images(1)}, 0.06 * 2 * 1.048576, 0, About},
		{"flux 2 klein bills the output alone", "openrouter:black-forest-labs/flux.2-klein-4b", Request{Settings: set{"aspect_ratio": "1:1"}, Images: images(4)}, 0.014 * 1.048576, 0, About},
		// docs/research/images.md, 5.2: 8 references at 2K cost 0.06 + 8 × 0.07.
		{"k3 edit at 2K with 8 references", "openrouter:krea/k3-edit", Request{Settings: set{"resolution": "2K"}, Images: images(8)}, 0.62, 0, Exact},
		{"k3 edit at 1K with 8 references", "openrouter:krea/k3-edit", Request{Settings: set{"resolution": "1K"}, Images: images(8)}, 0.19, 0, Exact},
		{"krea without style references", "openrouter:krea/krea-2-medium", Request{Settings: set{"resolution": "1K"}}, 0.03, 0, Exact},
		{"recraft styles once per request", "openrouter:recraft/recraft-v4-styles", Request{Images: images(1)}, 0.04, 0, Exact},
		{"grok low at 2K with a reference", "openrouter:x-ai/grok-imagine-image-2.0", Request{Settings: set{"quality": "low", "resolution": "2K"}, Images: images(1)}, 0.07, 0, Exact},
		// OpenRouter's high_resolution line is above 2.36 MP: 2K, not 1K.
		{"seedream pro at 1K", "openrouter:bytedance-seed/seedream-5-0-pro", Request{Settings: set{"resolution": "1K"}, Images: images(1)}, 0.048, 0, Exact},
		{"seedream pro at 2K", "openrouter:bytedance-seed/seedream-5-0-pro", Request{Settings: set{"resolution": "2K"}, Images: images(1)}, 0.093, 0, Exact},
		{"seedream pro with no resolution chosen", "openrouter:bytedance-seed/seedream-5-0-pro", Request{Images: images(1)}, 0.048, 0.093, UpTo},
		{"riverflow 2.5 is a floor", "openrouter:sourceful/riverflow-v2.5-pro", Request{Settings: set{"resolution": "2K"}}, 0.15, 0.15, From},
		{"riverflow 2 without super-resolution references", "openrouter:sourceful/riverflow-v2-pro", Request{Settings: set{"resolution": "1K"}, Images: images(2)}, 0.15, 0, Exact},

		// Seedance bills 24 frames a second and one more, so 5 s at 1080p is
		// 121 × 2,025 tokens: $1.89, where BytePlus's example, without the
		// extra frame, says $1.87.
		{"seedance 2.0 at 1080p", "openrouter:bytedance/seedance-2.0", Request{Settings: set{"resolution": "1080p", "aspect_ratio": "16:9", "duration": 5}}, 121 * 2025 * 7.7e-6, 0, Exact},
		// 4K is not in the overlay's sizes: the listed one, about.
		{"seedance 2.0 at 4K", "openrouter:bytedance/seedance-2.0", Request{Settings: set{"resolution": "4K", "aspect_ratio": "16:9", "duration": 5}}, 121 * 8100 * 4e-6, 0, About},
		{"seedance 2.5 square at 480p", "openrouter:bytedance/seedance-2.5", Request{Settings: set{"resolution": "480p", "aspect_ratio": "1:1", "duration": 5}}, 121 * 400 * 1.07e-5, 0, Exact},
		// BytePlus bills a reference clip's frames too, at the output's size.
		{"seedance 2.5 editing a video", "openrouter:bytedance/seedance-2.5", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5}, Video: clip}, (121 + 120) * 900 * 6.4e-6, 0, About},
		// Without sound is Seedance's plain rate, so it costs what with sound
		// does: with a clip, the rate with a video input.
		{"seedance mini silent with a clip", "openrouter:bytedance/seedance-2.0-mini", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}, Video: clip}, (121 + 120) * 900 * 2.1e-6, 0, About},
		{"seedance mini with sound and a clip", "openrouter:bytedance/seedance-2.0-mini", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": true}, Video: clip}, (121 + 120) * 900 * 2.1e-6, 0, About},
		{"seedance mini silent from text", "openrouter:bytedance/seedance-2.0-mini", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}}, 121 * 900 * 3.5e-6, 0, Exact},
		{"seedance 2.0 silent at 1080p with a clip", "openrouter:bytedance/seedance-2.0", Request{Settings: set{"resolution": "1080p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}, Video: clip}, (121 + 120) * 2025 * 4.7e-6, 0, About},
		// No sizes in the overlay for 1.5: the listed one, about.
		{"seedance 1.5 without sound", "openrouter:bytedance/seedance-1-5-pro", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": false}}, 121 * 900 * 1.2e-6, 0, About},
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
		// A reference clip's seconds are billed at the output's rate, rounded
		// up to a hundredth: job 47 of the shared schema, a 14.948 s clip and 5 s
		// of 480p on Wan 3.0, cost (5 + 14.95) × $0.05 less 15%.
		{"wan 3.0 with a reference clip", "openrouter:alibaba/wan-3.0", Request{Settings: set{"resolution": "480p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": true}, Video: &Video{Seconds: 14.948, Width: 1920, Height: 1080}}, 19.95 * 0.05, 0, About},
		{"wan 3.0 with two clips", "openrouter:alibaba/wan-3.0", Request{Settings: set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5}, Video: clip, Clips: []float64{3, 4.001}}, 12.01 * 0.1, 0, About},
		// HeyGen with a clip takes its reference rate for both: job 33.
		{"heygen with a reference clip", "openrouter:heygen/heygen-video-1", Request{Settings: set{"resolution": "480p", "aspect_ratio": "16:9", "duration": 5}, Video: &Video{Seconds: 14.948, Width: 1920, Height: 1080}}, 19.95 * 0.07, 0, About},
		{"hailuo 3 with a reference clip", "openrouter:minimax/hailuo-3", Request{Settings: set{"duration": 5}, Video: &Video{Seconds: 2, Width: 1280, Height: 720}}, 7 * 0.13, 0, About},
		{"flux 3 video at 1080p", "openrouter:black-forest-labs/flux-3-video", Request{Settings: set{"resolution": "1080p", "duration": 5}}, 1.45, 0, Exact},
		{"flux 3 video continuing a clip", "openrouter:black-forest-labs/flux-3-video", Request{Settings: set{"resolution": "1080p", "duration": 5}, Video: clip}, 2.65, 0, Exact},

		{"aleph's minimum", "openrouter:runway/aleph-2", Request{Video: &Video{Seconds: 1, Width: 1280, Height: 720}}, 0.56, 0, About},
		{"aleph on 5 s", "openrouter:runway/aleph-2", Request{Video: clip}, 1.40, 0, About},
		{"the free video edit", "openrouter:black-forest-labs/flux-video-edit", Request{Video: clip}, 0.15, 0, Exact},
		// Black Forest Labs bills the delivered video by the megapixel-second,
		// a megapixel being 1,048,576 pixels, at 24 fps and pro rata above.
		// creativity picks the rate: 0 is Precise, 1 Creative.
		{"upscale by two, precise", "openrouter:black-forest-labs/flux-video-upscale", Request{Settings: set{"upscale_factor": 2, "creativity": 0.0}, Video: clip}, hd * 4 * 5 * 0.035, 0, About},
		{"upscale by two, creative", "openrouter:black-forest-labs/flux-video-upscale", Request{Settings: set{"upscale_factor": 2, "creativity": 1.0}, Video: clip}, hd * 4 * 5 * 0.05, 0, About},
		{"upscale of a 48 fps clip", "openrouter:black-forest-labs/flux-video-upscale", Request{Settings: set{"upscale_factor": 1.5, "creativity": 0.0}, Video: &Video{Seconds: 4, Width: 1920, Height: 1080, FPS: 48}}, fhd * 2.25 * 4 * 2 * 0.035, 0, About},
		// framy keeps a clip at up to 60 frames a second, so a faster one, as
		// the browser reads it before Create, is billed at 60.
		{"upscale of a 120 fps clip", "openrouter:black-forest-labs/flux-video-upscale", Request{Settings: set{"upscale_factor": 2, "creativity": 1.0}, Video: &Video{Seconds: 2, Width: 1280, Height: 720, FPS: 120}}, hd * 4 * 2 * 2.5 * 0.05, 0, About},
		// A frame rate ffprobe did not give: up to the 60 framy keeps.
		{"upscale of a clip of no known rate", "openrouter:black-forest-labs/flux-video-upscale", Request{Settings: set{"upscale_factor": 2, "creativity": 0.0}, Video: &Video{Seconds: 5, Width: 1280, Height: 720}}, hd * 4 * 5 * 0.035, hd * 4 * 5 * 2.5 * 0.035, UpTo},
		{"upscale with nothing chosen", "openrouter:black-forest-labs/flux-video-upscale", Request{Video: clip}, hd * 1.5 * 1.5 * 5 * 0.035, hd * 9 * 5 * 0.05, UpTo},
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

// HeyGen Video bills a video over 15 s at its long SKUs, which OpenRouter's
// page calls "Video Output (over 15 s)" and leaves out of the 50% discount.
// Whether the long rate is for the seconds past 15 or for the whole video,
// no run has shown: the likely price takes the first, the most the dearer.
func TestHeyGenPastFifteenSeconds(t *testing.T) {
	m, of := offer(t, "openrouter:heygen/heygen-video-1")
	of.Routes = slices.Clone(of.Routes)
	of.Routes[0].Discount = 0.5
	for _, tc := range []struct {
		name     string
		req      Request
		usd, max float64
		list     float64
		kind     Kind
	}{
		// Check 13: 5 s of 768p was billed $0.09. The long SKU is for longer
		// videos only, so it no longer makes this a range.
		{"5 s", Request{Settings: set{"resolution": "768p", "duration": 5}}, 0.09, 0.09, 0.18, Exact},
		{"15 s", Request{Settings: set{"resolution": "768p", "duration": 15}}, 0.27, 0.27, 0.54, Exact},
		{"20 s", Request{Settings: set{"resolution": "768p", "duration": 20}}, 0.27 + 5*0.075, 20 * 0.075, 0.54 + 5*0.075, UpTo},
		{"30 s at 1080p", Request{Settings: set{"resolution": "1080p", "duration": 30}}, 0.75 + 15*0.105, 30 * 0.105, 1.5 + 15*0.105, UpTo},
		// A reference clip is billed at the plain reference rate beside the
		// output, as below 15 s.
		{"20 s with a clip", Request{Settings: set{"resolution": "480p", "duration": 20}, Video: &Video{Seconds: 5, Width: 1280, Height: 720}}, (15+5)*0.035 + 5*0.045, 20*0.045 + 5*0.035, (15+5)*0.07 + 5*0.045, UpTo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Of(m, of, tc.req)
			if !near(e.USD, tc.usd) || !near(e.MaxUSD, tc.max) || !near(e.ListUSD, tc.list) || e.Kind != tc.kind {
				t.Errorf("estimate = $%.6f up to $%.6f (listed $%.6f), %s; want $%.6f up to $%.6f (listed $%.6f), %s\nlines %+v", e.USD, e.MaxUSD, e.ListUSD, e.Kind, tc.usd, tc.max, tc.list, tc.kind, e.Lines)
			}
			if e.ListMaxUSD < e.MaxUSD {
				t.Errorf("listed most $%.6f below the most $%.6f", e.ListMaxUSD, e.MaxUSD)
			}
		})
	}

	// Without the hint nothing says when the long SKU applies: it is a
	// maybe, and every run is a range up to its rate.
	bare := m
	hints := *m.Traits.Price
	hints.Long = nil
	bare.Traits.Price = &hints
	if e := Of(bare, of, Request{Settings: set{"resolution": "768p", "duration": 5}}); e.Kind != UpTo || !near(e.MaxUSD, 5*0.075*0.5) {
		t.Errorf("without price.long = %+v", e)
	}
}

// Seedance with a clip longer than 3 s counts the clip's frames and says it
// is about; from words alone it stays exact.
func TestSeedanceWithAClipCountsItsSeconds(t *testing.T) {
	m, of := offer(t, "openrouter:bytedance/seedance-2.0-mini")
	settings := set{"resolution": "720p", "aspect_ratio": "16:9", "duration": 5}
	e := Of(m, of, Request{Settings: settings, Video: &Video{Seconds: 15, Width: 1280, Height: 720}})
	if e.Kind != About || e.Note != "" || !near(e.USD, (121+360)*900*2.1e-6) {
		t.Errorf("with a clip: $%f, %s, note %q", e.USD, e.Kind, e.Note)
	}
	if e := Of(m, of, Request{Settings: settings}); e.Kind != Exact || e.Note != "" {
		t.Errorf("from words: %s, note %q", e.Kind, e.Note)
	}
}

// The two bills of Seedance 2.0 Mini on 31 October: 4 s of 480p 16:9 with a
// 2 s clip and with a 3 s one, $0.1485246 each, 70,726 tokens of 864×496
// frames: 97 of the output and 72 of the clip.
func TestSeedanceBills(t *testing.T) {
	m, of := offer(t, "openrouter:bytedance/seedance-2.0-mini")
	settings := set{"resolution": "480p", "aspect_ratio": "16:9", "duration": 4, "generate_audio": false}
	for _, seconds := range []float64{2, 3} {
		e := Of(m, of, Request{Settings: settings, Video: &Video{Seconds: seconds, Width: 854, Height: 480}})
		if e.Kind != Exact || len(e.Lines) != 1 || e.Lines[0].Quantity != 70726 || math.Abs(e.USD-0.1485246) > 1e-9 {
			t.Errorf("with a %g s clip: $%.7f, %s, lines %+v", seconds, e.USD, e.Kind, e.Lines)
		}
	}
	// Without a clip: 97 frames at the plain rate.
	if e := Of(m, of, Request{Settings: settings}); e.Kind != Exact || math.Abs(e.USD-40594*3.5e-6) > 1e-9 {
		t.Errorf("from words: $%.7f, %s, lines %+v", e.USD, e.Kind, e.Lines)
	}
	// Two short clips: the floor is either each clip's or theirs together.
	e := Of(m, of, Request{Settings: settings, Video: &Video{Seconds: 2, Width: 854, Height: 480}, Clips: []float64{2, 2}})
	if e.Kind != About || e.Lines[0].Quantity != math.Floor((97+144)*418.5) {
		t.Errorf("two clips: %s, lines %+v", e.Kind, e.Lines)
	}
	// 9:21 is not in the overlay's sizes.
	if e := Of(m, of, Request{Settings: set{"resolution": "480p", "aspect_ratio": "9:21", "duration": 4}}); e.Kind != About {
		t.Errorf("at 9:21: %s", e.Kind)
	}
	// A reference sound: no bill with one seen, so the same tokens, "about".
	if e := Of(m, of, Request{Settings: settings, Sounds: 1}); e.Kind != About || math.Abs(e.USD-40594*3.5e-6) > 1e-9 || len(e.Unknown) != 1 {
		t.Errorf("with a sound: $%.7f, %s, unknown %v", e.USD, e.Kind, e.Unknown)
	}
}

// A clip an editor works on is its source, not a reference: its seconds are
// the output's, not billed again.
func TestASourceVideoIsNotBilledTwice(t *testing.T) {
	m, of := offer(t, "openrouter:black-forest-labs/flux-video-edit")
	if e := Of(m, of, Request{Video: &Video{Seconds: 5, Width: 1280, Height: 720}, Clips: []float64{5}}); !near(e.USD, 0.15) {
		t.Errorf("$%f", e.USD)
	}
}

// The three runs on the test bot that the estimate got wrong, with the
// discounts of that day: the estimate now covers each and is close to it.
func TestTheTestBotsRuns(t *testing.T) {
	for _, tc := range []struct {
		name, offer string
		discount    float64
		req         Request
		actual      float64
		within      float64
	}{
		{"job 47", "openrouter:alibaba/wan-3.0", 0.15, Request{Settings: set{"resolution": "480p", "aspect_ratio": "16:9", "duration": 5, "generate_audio": true}, Video: &Video{Seconds: 14.948, Width: 1920, Height: 1080}}, 0.847875, 0.001},
		{"job 33", "openrouter:heygen/heygen-video-1", 0.5, Request{Settings: set{"resolution": "480p", "aspect_ratio": "16:9", "duration": 5}, Video: &Video{Seconds: 14.948, Width: 1920, Height: 1080}}, 0.698215, 0.001},
		// No ratio was sent, so OpenAI made 1536×1024 where framy counts a
		// square, which costs more.
		{"job 53", "openrouter:openai/gpt-image-2.5-flare", 0, Request{Settings: set{"n": 1, "quality": "low"}, Prompt: strings.Repeat("я", 32), Images: []Image{{Width: 1280, Height: 853}}}, 0.01351, 0.1},
	} {
		m, of := offer(t, tc.offer)
		of.Routes = slices.Clone(of.Routes)
		for i := range of.Routes {
			of.Routes[i].Discount = tc.discount
		}
		e := Of(m, of, tc.req)
		if e.USD < tc.actual-1e-9 || e.USD > tc.actual*(1+tc.within) {
			t.Errorf("%s: estimate $%f, billed $%f\nlines %+v\nunknown %v", tc.name, e.USD, tc.actual, e.Lines, e.Unknown)
		}
	}
}

// The counts of the forum post that worked the formula out, and job 53.
func TestOpenAIInputTokens(t *testing.T) {
	for _, tc := range []struct{ w, h, want int }{
		{256, 256, 256}, {384, 384, 576}, {512, 512, 1024}, {768, 768, 1024},
		{1023, 512, 512}, {1024, 512, 512}, {1025, 512, 528}, {1536, 512, 768},
		{1537, 512, 833}, {2048, 512, 1408}, {2049, 512, 1430}, {1536, 768, 1152},
		{1536, 1536, 1521}, {2048, 1024, 1458}, {2048, 2048, 1521}, {4096, 2048, 1458},
		{1280, 853, 1080}, {853, 1280, 1080},
	} {
		if got := openAIInputTokens(tc.w, tc.h, 1536); got != tc.want {
			t.Errorf("%d×%d: %d tokens, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestPublishedPrices(t *testing.T) {
	// The figures as OpenAI, OpenRouter and Tencent print them, rounded to
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
		// OpenAI's calculator for GPT Image 2.6 at low 1024×1024: $0.02469.
		{"openai:gpt-image-2.6-sunburst", set{"size": "1024x1024", "quality": "low"}, 0.02469, 0.00001},
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
	if got := Check(broken); len(got) != 3 || !strings.Contains(got[2], "no price before the run") {
		t.Fatalf("check = %v, want the billable, the SKU and the price it leaves unknown", got)
	}
	// A GPT Image OpenRouter adds before the overlay knows it: its tokens are
	// counted by nothing, and the price would be "known after the run".
	uncounted := catalog.Snapshot{Models: []catalog.Model{{ID: "openai/gpt-image-9", Offers: []catalog.Offer{{ID: "openrouter:openai/gpt-image-9", Routes: []catalog.Route{
		{Prices: []catalog.Price{{Billable: "input_text", Unit: "token", CostUSD: 5e-6}, {Billable: "output_image", Unit: "token", CostUSD: 3e-5}}},
		{Prices: []catalog.Price{{Billable: "output_image", Unit: "token", CostUSD: 3e-5}}},
	}}}}}}
	if got := Check(uncounted); len(got) != 1 || !strings.Contains(got[0], "openrouter:openai/gpt-image-9 has no price before the run at its default settings: the tokens of the output image") {
		t.Fatalf("check = %v, want one line of the uncounted output", got)
	}
	uncounted.Models[0].Traits.Price = &catalog.PriceHints{OpenAITokens: map[string]int{"low": 16}}
	if got := Check(uncounted); len(got) != 0 {
		t.Fatalf("check with a count = %v", got)
	}
}

// A model that only shows "$?" or leaves a part out says so in the sync's
// log, before someone sees it in the picker.
func TestCheckFindsWhatGoesUnpriced(t *testing.T) {
	refs := map[string]catalog.Param{"input_references": {Type: catalog.Range, Min: ptr(0.0), Max: ptr(4.0)}}
	for _, tc := range []struct {
		name  string
		model catalog.Model
		want  []string
	}{
		// GPT Image 2.6 on 4 November: tokens of the output image and no
		// count of them in the overlay.
		{"output tokens without a count", catalog.Model{ID: "openai/gpt-image-9", Offers: []catalog.Offer{{ID: "openrouter:openai/gpt-image-9", Kind: catalog.Image, Routes: []catalog.Route{{
			Prices: []catalog.Price{{Billable: "output_image", Unit: "token", CostUSD: 3e-5}},
		}}}}}, []string{"openrouter:openai/gpt-image-9 has no price before the run at its default settings: the tokens of the output image"}},
		{"input tokens without a count", catalog.Model{ID: "google/gemini-9-image", Traits: catalog.Traits{Price: &catalog.PriceHints{Tokens: map[string]map[string]int{"output_image": {"*": 1120}}}}, Offers: []catalog.Offer{{ID: "gemini:gemini-9-image", Kind: catalog.Image, Params: refs, Routes: []catalog.Route{{
			Prices: []catalog.Price{{Billable: "output_image", Unit: "token", CostUSD: 3e-5}, {Billable: "input_image", Unit: "token", CostUSD: 1e-6}, {Billable: "output_text", Unit: "token", CostUSD: 1e-5}},
		}}}}}, []string{"gemini:gemini-9-image cannot price a reference: the tokens of the input images"}},
		{"a SKU word nobody named", catalog.Model{ID: "bfl/upscale", Traits: catalog.Traits{SourceVideo: &catalog.SourceVideo{Required: true, KeepsLength: true}}, Offers: []catalog.Offer{{ID: "openrouter:bfl/upscale", Kind: catalog.Video, Routes: []catalog.Route{{
			SKUs: map[string]string{"cents_per_megapixel_second_precise": "3.5", "cents_per_megapixel_second_creative": "5"},
		}}}}}, []string{
			"openrouter:bfl/upscale: SKU cents_per_megapixel_second_creative says creative, which the estimate does not read; name its settings in price.variants",
			"openrouter:bfl/upscale: SKU cents_per_megapixel_second_precise says precise, which the estimate does not read; name its settings in price.variants",
		}},
		{"a long SKU without its length", catalog.Model{ID: "heygen/x", Offers: []catalog.Offer{{ID: "openrouter:heygen/x", Kind: catalog.Video, Params: map[string]catalog.Param{"duration": {Type: catalog.Enum, Values: []any{5.0, 20.0}}}, Routes: []catalog.Route{{
			SKUs: map[string]string{"duration_seconds": "0.02", "long_duration_seconds": "0.045"},
		}}}}}, []string{"openrouter:heygen/x: SKU long_duration_seconds is for long videos; say from how long in price.long"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(catalog.Snapshot{Models: []catalog.Model{tc.model}})
			var want []string
			for _, w := range tc.want {
				want = append(want, "price: "+w)
			}
			if !slices.Equal(got, want) {
				t.Errorf("check = %q\nwant    %q", got, want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

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

// side reads a resolution as the side of its square, in pixels.
func TestSide(t *testing.T) {
	for res, want := range map[string]float64{"1k": 1024, "1.5k": 1536, "2k": 2048, "4k": 4096, "768": 768, "0.5k": 512} {
		if got, ok := side(res); !ok || got != want {
			t.Errorf("side(%q) = %v, %v; want %v", res, got, ok, want)
		}
	}
	for _, res := range []string{"", "k", "0k", "-1k", "big", "1080p"} {
		if got, ok := side(res); ok {
			t.Errorf("side(%q) = %v; want no side", res, got)
		}
	}
}

// A price by the megapixel follows the size the run makes. FLUX.2 on
// OpenRouter takes only an aspect ratio, and is priced at 1024×1024 for
// each; a model that also took a resolution or a size would be priced at
// it, not at the 1 MP default, so a 4K picture never comes out at the price
// of a 1K one.
func TestMegapixelsFollowTheChosenSize(t *testing.T) {
	m, of := offer(t, "openrouter:black-forest-labs/flux.2-pro")
	const rate = 0.03
	for _, ar := range []string{"1:1", "4:3", "3:4", "3:2", "2:3", "16:9", "9:16", "21:9"} {
		if e := Of(m, of, Request{Settings: set{"aspect_ratio": ar}}); !near(e.USD, rate*1.048576) || e.Kind != About {
			t.Errorf("flux 2 pro at %s = $%.6f %s, want $%.6f about", ar, e.USD, e.Kind, rate*1.048576)
		}
	}

	sized := of
	sized.Params = map[string]catalog.Param{}
	for k, v := range of.Params {
		sized.Params[k] = v
	}
	sized.Params["resolution"] = catalog.Param{Type: catalog.Enum, Values: []any{"1K", "1.5K", "2K", "4K"}}
	sized.Params["size"] = catalog.Param{Type: catalog.Enum, Values: []any{"1024x1024", "2048x1152"}}
	for _, c := range []struct {
		settings set
		pixels   float64
	}{
		{set{}, 1024 * 1024},
		{set{"resolution": "1K"}, 1024 * 1024},
		{set{"resolution": "1.5K"}, 1536 * 1536},
		{set{"resolution": "2K"}, 2048 * 2048},
		{set{"resolution": "4K"}, 4096 * 4096},
		// A size is the pixels themselves, ahead of a resolution.
		{set{"size": "2048x1152"}, 2048 * 1152},
		{set{"size": "2048X1152", "resolution": "4K"}, 2048 * 1152},
	} {
		e := Of(m, sized, Request{Settings: c.settings})
		if want := rate * c.pixels / 1e6; !near(e.USD, want) {
			t.Errorf("%v: $%.6f, want $%.6f (%.2f MP)", c.settings, e.USD, want, c.pixels/1e6)
		}
	}
}
