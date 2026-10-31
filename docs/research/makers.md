# What the makers' own APIs offer beyond framy's ports

A snapshot of 28 October 2026, from one audit per maker against the maker's documentation and OpenRouter's public JSON (`/api/v1/images/models`, `/api/v1/videos/models`, `/api/v1/models/{id}/endpoints`). framy reaches every model through OpenRouter, GPT Image also through OpenAI, the Gemini models and Omni also through the Gemini API. This file lists what a model can do at its maker and framy cannot ask for today, so a future direct port knows what it would gain.

Each item says whether OpenRouter passes it: **no** (no field, no pass-through), **unverified** (OpenRouter lists the input or the name but documents nothing, so only a paid run settles it) or **partly**. Facts are marked as the audit found them: *confirmed* by the maker's own page, *likely* from strong indirect evidence, *unverified* otherwise. Nothing here was run for money.

The catalog follows one rule meanwhile: a count of reference images, videos or sounds is set in `catalog/overlay.yaml` only where OpenRouter lists that kind of input for the model and the maker publishes the number. Everything else waits for a run.

## What would pay off first

1. **ByteDance (BytePlus ModelArk) for Seedance.** Editing and extending a video need `ratio: adaptive` and, for an edit, `duration: -1`, which OpenRouter does not publish; Seedance 2.5 makes 1080p only at the maker; `return_last_frame` gives the frame to chain the next clip from. These are the features that make "Edit" and "Continue" work on the cheapest video models with reference video and sound.
2. **Kling.** OpenRouter runs Kling through AtlasCloud's narrower schema: no reference images, no reference video, no multi-shot, no Elements, no 1080p or 4K. Kling 4.0 came out on 28 October 2026 and is not on OpenRouter.
3. **xAI.** Grok Imagine Video 1.5 takes 14 reference images, a last frame, 4 keyframes and 3 preset voices at xAI, and only a first frame through OpenRouter. Video 1.0 edits and extends one uploaded clip at xAI.
4. **OpenAI and Gemini, the ports framy has.** They send what OpenRouter does not: GPT Image's sizes up to 3840 px and its format; Gemini's thinking level, Search and Image Search grounding, input detail, JPEG output, video input and Omni. Left: GPT Image's mask (stage 3) and Omni's reference clips.
5. **HeyGen and MiniMax.** HeyGen Video makes 16 to 30 s and 1080p at HeyGen; MiniMax's Hailuo 3 runs at 768P for $0.08 a second against $0.13 at 2K, the only resolution OpenRouter offers.

## Alibaba (Wan, HappyHorse)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Wan 3.0 reference video and sound | 5 videos and 5 sounds, 1 to 15 s each, 15 s in all, 100 MB and 15 MB (confirmed) | unverified: listed as input kinds, no counts or doc | motion, camera and voice references; set in the overlay, a run confirms |
| Wan 3.0 document and web link inputs | one file and one link per run (confirmed) | no | product videos from a page or a brief |
| Wan 3.0 21:9, `adaptive` ratio, `duration: -1` | yes (confirmed) | no | cinematic format; output that follows the input clip |
| Wan 3.0 `prompt_extend`, `watermark` | yes | no (pass-through list empty) | turn prompt rewriting off for exact prompts |
| Wan 3.0 billing of input video | input seconds plus output seconds (confirmed) | the same: job 44 on the test bot, a 14.948 s clip and 5 s of 480p, cost (5 + 14.95) × $0.05 less 15% (confirmed 30 October 2026) | the estimate with a video attached; a 15 s reference leaves 15 s of output, since input plus output is at most 30 s |
| Wan 2.7 duration | up to 15 s for text and image to video | 10 s; routed through Atlas Cloud | longer clips |
| Wan 2.7 reference video, continuation clip, driving audio | yes | unverified: only through Atlas pass-through names (`videos`, `video`, `audio`) with no documented shape | |
| `wan2.7-videoedit` | yes | not offered | a second editor |
| Wan 2.6 reference video, 1:1, 4:3, 3:4, 2 to 15 s | yes | no: 5 s and 10 s, 16:9 and 9:16 only | |
| HappyHorse 480P, 4:5, 5:4, watermark off | yes; the watermark is on by default (confirmed) | no | a clean result: OpenRouter's output may carry the "Happy Horse" watermark (unverified) |
| `happyhorse-1.0-video-edit` | yes, 1.0 only | not offered | |

Wan 3.0 is "preview" at Alibaba, which means 14 days' notice before it retires. Prime is the same model, faster, at 1.36 to 1.4 times the list price.

## Black Forest Labs (FLUX)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| FLUX 3 Image exact width and height, `match_input` | yes | no: aspect ratio and resolution only | an edit at the photo's own size |
| FLUX 3 Image grounding switch | on by default | no, so search is always on | |
| FLUX 3 Image Klein `seed`, `steps`, `output_format` | yes | no | repeatable results |
| FLUX.2 `disable_pup`, webp, free size | yes | no | |
| FLUX.2 Flex `steps` (1 to 50), `guidance` (1.5 to 10) | Flex only (confirmed); Max, Pro and Klein 4B have neither | pass-through under `provider.options['black-forest-labs/us-3']` | the only FLUX.2 with them; offer them on Flex alone |
| FLUX 3 Video keyframes | up to 10 timed keyframes | first and last frame only | |
| FLUX 3 Video 2:1, 9:21, `auto` ratio and length, draft mode | yes | no | a cheap draft before the real run |
| FLUX 3 Video clip to continue | `v2v`, up to 15 s | unverified: OpenRouter prices a continuation SKU but documents no field | "Continue" on FLUX |
| FLUX 3 Video reference images and video | only in BFL's cookbook (`flux-3-preview-high`), "soon" in the public API | unverified | |

FLUX Video Edit and Upscale lose nothing through OpenRouter. Upscale's `creativity` is 0 or 1 at BFL, and 0.5 is refused; framy now reads it as those two values. OpenRouter's upscale prices (3.5 and 5 cents a megapixel-second) are half of BFL's own.

## ByteDance (Seedream, Seedance)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Seedance 2.x edit and extend | all four 2.x models (confirmed), not only 2.5 | unverified: by a reference video and the prompt; `previous_job_id` unverified for Seedance | "Edit" and "Continue" |
| Seedance 2.5 `ratio: adaptive`, `duration: -1` | required for first and last frame, edit and extend (confirmed) | no; safest is to send neither | without them such runs fail with `InvalidParameter.TaskTypeConstraint` |
| Seedance 2.5 1080p | yes | no: 480p and 720p | |
| Seedance `return_last_frame` | all | only 2.0 Mini | chain clips into a longer video |
| Seedance draft mode, camera control, `asset://` portrait ids | yes | no | |
| Seedance 1.5 Pro `camera_fixed` | yes | no | retires 11 November 2026 anyway |
| Seedream output format, watermark switch | yes; watermark on by default | no | |
| Seedream 5.0 layer decomposition, transparent background, png, 1.5K | Pro and Flash | no | stickers and assets with alpha |
| Seedream 5.0 Lite web search, 3K | yes | no, though the description mentions retrieval | |
| Seedream sequential `max_images` up to 15, streaming | yes | no (n up to 10) | |

Seedream 5.0 Pro and Flash take 10 references at BytePlus while OpenRouter lists 14; the overlay caps them at 10. Seedance's reference limits: 2.0, Fast and Mini 9 images, 3 videos and 3 sounds (2 to 15 s, 15 s in all); 2.5 30, 10 and 10 (2 to 30 s, 30 s in all, an edited source 4 to 30 s). A sound needs an image or a video beside it on 2.0; 2.5 takes sound alone. BytePlus does not take real faces in references.

## Google (Gemini API, Vertex)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Nano Banana `thinking_level` | Google's default `medium` on 2.1 | no on the Images API; the chat route has only high, minimal and none, default minimal | quality on hard prompts; framy's direct port has it |
| Google Search and Image Search grounding | Search on 2.1, 3.1 Flash and 3 Pro; Image Search on 2.1 and 3.1 Flash only | no | the direct port has both (`google_search`, `image_search`) and keeps the Search Suggestions Google asks to show |
| `media_resolution` | yes | no | Input detail; the direct port sends it, the composer waits for a check |
| video input, JPEG output | yes | no | the direct port has both |
| PDF input, `previous_interaction_id` for images, URI output | yes | no | not taken: framy has no PDFs, images go with `store: false`, so an edit sends the result back instead of continuing, and results come inline |
| Veo reference images | 3 asset images or 1 style image (confirmed at Google) | unverified | |
| Veo extension | +7 s, up to 37 s | no field, though the description says "20 chained clips" | |
| Veo `sampleCount` 1 to 4, `resizeMode`, `compressionQuality` | yes | no | |
| Omni | Gemini API only | not on OpenRouter | the direct port runs it: frames, 6 reference images, a source of up to 10 s, `previous_interaction_id` |
| Omni duration | `response_format.duration`, "3s" to "10s" on Vertex (likely) | not on OpenRouter | an exact price instead of "up to"; the direct port sends it, the catalog lists it after a run |
| Omni reference video | 3 clips of up to 3 s, sound in them ignored (confirmed) | not on OpenRouter | likeness of a person or object; not offered yet, since the catalog has one video role per model and Omni's is the source |
| search query billing | 5,000 queries a month, then $14 per 1,000 on Gemini 3 | no | not in framy's estimate or cost: framy cannot see the person's allowance |

Dates: Veo 3.1 preview ids closed on the Gemini API on 22 October 2026, with Omni named in their place; the Vertex ids OpenRouter runs retire "17 November 2026 or later". Gemini 3.1 Flash Image was deprecated on 6 October 2026 with no shutdown day; Gemini 2.5 Flash Image shuts on 15 March 2027. Veo with references runs 8 s only; the Gemini API docs say the same of 1080p and 4K. Omni spells 4K as `4k`.

## HeyGen

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| HeyGen Video 16 to 30 s and `auto` length | since October 2026 | no: 5 to 15 s | longer clips |
| 1080p | yes, at 3 times the 768p rate | no: 480p, 768p, 2K | |
| `aspect_ratio: adaptive` | default for reference to video | no | |
| reference videos and sounds | 3 videos sharing 15 s, 32 MB, mp4 or webm; 3 sounds; 15 files in all (confirmed) | a reference video works and is billed: job 47 on the test bot, a 14.948 s clip and 5 s of 480p, cost (5 + 14.949) × the reference rate $0.07, less 50% (30 October 2026); sounds unverified | they guide motion and style; set in the overlay, a run confirms |
| Avatar IV audio, 4:5, 5:4, webm | yes | audio unverified, the rest no | the talking head stays hidden until the composer takes a voice |

At 2K, text to video and reference to video take only 16:9 and 9:16. The first two reference images are free on OpenRouter's page ("after first 2"); framy's estimate counts them free (`price.free_images` in the overlay), unverified by a run. OpenRouter's 50% off mirrors HeyGen's launch price, which HeyGen says lasts to the end of October.

## Kling (Kuaishou)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Kling 4.0 | since 28 October 2026, Flagship plan | not listed | the newest Kling |
| Kling 3.0 Omni | since 25 February 2026: 7 reference images, 1 reference video (3 to 15.5 s, 200 MB), 4K | no id | references and editing |
| Kling 3.0 Turbo | since 17 June 2026, cheaper | not listed | |
| multi-shot (`multi_prompt`, up to 6 shots) and Elements (3) | yes | no | |
| 1080p and 4K on 3.0 | Pro makes 1080P | only "720p" listed for Pro (unverified what comes back) | |
| O1 reference images, Elements, video editing | yes | no: text and image to video only | O1 is hidden: same price as 3.0 Pro, less through OpenRouter |

OpenRouter forwards `negative_prompt` and `cfg_scale` to Kling 3.0, which neither Kling nor AtlasCloud documents for 3.0; whether they act is unverified (a refused task is not billed). OpenRouter turns sound on by default where Kling leaves it off, and the estimate assumes it on.

## Krea

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Krea 3 text to image | the same endpoint takes 0 to 8 images | at least 1 reference: an editor only | |
| K3 4K, 3:4, 5:4, 21:9, width and height, `negative_prompt`, regions, regional edit, `ultra` thinking | yes | no | |
| Krea 2 3:4 and 2.35:1 | yes | no | |
| Krea 2 style references (up to 10), moodboards, styles, creativity, `strength` | yes | names only, as pass-through, with no types or ranges; moodboards and styles belong to a Krea workspace | the single reference through OpenRouter is a start image whose `strength` defaults to 0.99, so it barely shows |

`thinking_mode` takes `fast` (default), `high` and `ultra`; the old values instant, medium and max stop working on 30 October 2026. Krea recommends Medium to start with; the family now opens on it.

## Meta

Muse Image (`muse-image-1.0`) is public on Meta's Model API since 25 August 2026 at $0.01 an image: up to 10 reference images, size, n up to 10, `reasoning_strength`, `tool_enablement`, output and response format. OpenRouter lists it on the Images API with no endpoint, as `meta/muse-image-1.0-eval-20260824`; only chat completions run it. A direct port would make it the cheapest image model framy has. Muse Video is in early preview at Meta, on no API.

## Microsoft (MAI)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| reference images | 5 on every MAI model | 1 on 2.5 and 2.5 Pro, 5 on 2.6 | |
| free width and height | at least 768 a side; 2.6 up to 2,359,296 px (about 1.5K) | aspect ratio only, pixel size undocumented | the largest picture 2.6 makes |
| `web_grounding` (Bing search) | 2.6 and 2.6 Flash | pass-through under `provider.options.azure`; framy ignores it | current events and products |
| `auto_aspect_ratio` | 2.6 only | OpenRouter lists `auto` for 2.5 too (unverified) | |
| prompt length | 32K tokens | context 4,096 | |

Microsoft's retirement schedule gave 1 October 2026 for MAI-Image-2.5, 2.5 Pro and 2.5 Flash; they still run through OpenRouter, so framy marks them deprecated with no day. MAI-Image-2.7 (27 October 2026) edits only, in preview, in Microsoft's playground, with no API.

## MiniMax (Hailuo)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Hailuo 3 at 768P | $0.08 a second | no: 2K only, $0.13 | a third cheaper |
| 4 s length, `adaptive` ratio | yes | no | |
| reference video and sound | 3 videos (2 to 15 s, 15 s in all, 50 MB, MP4 or MOV) and 3 sounds (15 MB), 12 files in all (confirmed) | unverified: listed as input kinds, no counts, no input-video SKU | MiniMax bills an input video's seconds at the output rate; whether OpenRouter does is unknown |
| Context-IR enrichment, 2K regeneration, `mm_file://` ids | yes | no | |
| Hailuo 3 Max references | yes by the API reference; the FAQ says frames only | text and image only, no reference SKU | |
| Max `prompt_expansion_mode` | yes | no | |
| Hailuo 2.3 768P (MiniMax's default), 2.3 Fast | yes | no: 1080p at 6 s and 10 s only | |

The first five reference images are free at MiniMax and on OpenRouter's page; framy's estimate counts them free. A sound alone is refused.

## OpenAI (GPT Image), a direct port of framy's

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| any size up to 3840 px and 8.29 MP | yes | 8 aspect ratios and `auto` | 2K and 4K, on the direct port only |
| `output_format` | yes | no: PNG; `output_compression` probably does nothing | |
| mask | yes, on the first image, with alpha, up to 50 MB | no | the stage 3 mask editor |
| `partial_images`, `safety_identifier`, Batch at half price | yes | no | progress previews; cheaper bulk runs |
| `moderation` | a field | only `provider.options.openai.moderation` | |
| transparent background on GPT Image 2 | preview since 20 August 2026 | auto and opaque only | |

Both ports take 16 references, n up to 10 and every quality. gpt-image-1 shut on 23 October 2026, gpt-image-1-mini shuts on 1 December 2026. `gpt-5-image` and its siblings exist only on OpenRouter, as chat models around GPT Image.

## Qwen (Alibaba)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Qwen Image 2.1 references | 10 | 4 | albums |
| 2.1 transparent PNG | native RGBA | only by asking in the prompt; whether alpha survives is unverified | |
| 3 and 3 Pro `negative_prompt` | yes | no | |
| `prompt_extend`, `prompt_extend_mode`, `enable_thinking`, `watermark`, explicit size, ratios from 1:8 to 8:1 | yes | no (pass-through list empty), so Alibaba's defaults apply: rewriting and thinking on | exact prompts |
| result as a 24-hour URL | yes | base64 only | |

2.1 is not older than 3: 3 Pro came out on 20 July 2026, 3 on 4 August, 2.1 Pro on 2 October, 2.1 Turbo on 9 October, and Alibaba sells both lines. The overlay keeps 2.1 Pro current.

## Recraft

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| curated `style`, `text_layout`, `artistic_level`, `no_text`, `negative_prompt` | V3 only (confirmed) | the pass-through list offers `style` and `text_layout` on V4 and V4.1 too, where they do nothing | if framy offers Recraft styles or text layout, only on V3, which must then not hide behind V4.1 |
| style references on V4 and V4.1 (up to 10), `style_id`, `style_match`, `random_seed` | yes | only on the V4 Styles models | |
| `strength` of image to image | required at Recraft | OpenRouter's default is not published | |
| 14 aspect ratios, exact size, `image_format`, `response_format` | yes | 5 ratios (11 on Styles) | |
| inpaint, outpaint, background endpoints | yes | no | |
| `recraftv3_vector`, utility vector models | yes | not offered | more SVG |

V4 Styles bills $0.005 for creating the style on every request through OpenRouter; at Recraft a saved `style_id` costs generation only.

## Runway

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Aleph 2 source video | `videoUri`: 2 to 30 s, up to 30 fps, 32 MB by URL, 5 MB as data | unverified: no field documented; `input_references` with `video_url` or `previous_job_id` are the candidates | the editor works only once this is known (open check 12) |
| Aleph 2 timed keyframes (up to 5) | yes | only as a pass-through with no documented shape | |
| ProRes, PNG sequence, HDR output | yes | no | |
| Gen-4.5 image-to-video ratios 1104:832, 960:960, 832:1104, 1584:672 | yes | no: 16:9 and 9:16 | |

Aleph 2's `aspect_ratio` is very probably an outpaint target, so framy sends no default. Its result likely keeps the source's length; Runway does not say so, so the estimate stays "about". Both models cap the prompt at 1000 characters.

## Sourceful (Riverflow)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| 2.5 thinking level | yes | no on the Images API; `reasoning` on the chat route | effort on Riverflow |
| scoring prompt and rubric, background colour, solid backgrounds, enhance prompt, safety checker, several results a call | yes | no | |
| 2.0 super-resolution references (up to 4, $0.20 each), transparency, webp and png, 5:4 and 4:5 | yes | no; fonts only | |

Riverflow 2 Fast makes no 4K, though OpenRouter lists it; the overlay drops it. Sourceful suggests timeouts up to 5 minutes for Fast and 10 for Pro, and OpenRouter's Images API has a 524 edge timeout.

Riverflow 2.6 Pro came out on 29 October 2026, the only model of the 2.6 lineup so far: no 2.6 Fast on OpenRouter or elsewhere by 31 October, so 2.5 Fast stays current. Beside 2.5 Pro it adds 5:4 and 4:5 and a custom `size` up to 16,384 px a side, which framy does not send, since a size would replace the aspect ratio and the resolution. It takes the same 10 references and fonts as a pass-through, and has the same price table: from $0.13 at 1K, $0.15 at 2K and $0.17 at 4K, the final price known only after the run. Sourceful had published no page of its own on 2.6 by 31 October; this is OpenRouter's listing.

## SpaceXSI (xAI)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| Grok Imagine Image 2.0 sources | 5 since August 2026 | 3 in the catalog (unverified whether a 4th is refused) | |
| Image 2.0 21:9, 5:2, `quality: auto`, n up to 10, deferred mode | yes | no; n is 1 | |
| Video 1.5 reference images | 14 (720p at most in that mode) | unverified | |
| Video 1.5 last frame, 4 keyframes, 3 preset voices | yes | no (pass-through list empty) | |
| Video 1.0 reference images | 7 (10 s and 720p at most in that mode) | likely, by OpenRouter's description; set in the overlay | |
| Video 1.0 edit (up to 8.7 s) and extend (2 to 15 s) of one mp4 | yes | unverified, probably not reachable | |
| 21:9, 5:2, `generate_audio` switch on every video model | yes | no | |

No xAI endpoint takes more than one input video: the "many videos" are images. Grok Imagine Image Quality retires on 2 November 2026 and its slug then serves 2.0 at low quality; framy hides it from 19 October by its shutdown day.

## Tencent (HY Image)

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| `use_search_tool` | yes | no | |
| `footnote` (watermark text, 16 characters) | yes | no | |
| `resize_max_pixels` | references are scaled to about 1 MP by default | no | sharper references |
| multi-turn editing (`session`, `assembled_history`) | yes | no; feed the last result back as a reference | |
| `generate_max_pixels` | yes | 4K and 1.5K map to it in an undocumented way | |

Tencent's price table counts 15,000 tokens an image at 1K and 2K and 20,000 at 4K; framy's estimate uses it. References are not billed. The API takes 20 references while Tencent's launch post says 5 work well. The old Hunyuan platform closed on 30 September 2026.

## TBC (mischief 1)

The Biological Computing Co., which writes itself TBC and the model in lower case. mischief 1 came out on 30 October 2026, on fal and OpenRouter: Wan 2.2 with an add-on of under 0.1% of its parameters, which TBC derived from measuring living neurons and says renders five times faster at Wan 2.2's quality. No independent benchmark is published (confirmed by TBC's release and fal's schema).

| Capability | At the maker | Through OpenRouter | Why framy wants it |
|:--|:--|:--|:--|
| text to video | no: a start image is required (confirmed, fal) | the generic page allows a prompt alone, but the model is image to video only | the overlay marks `frame_required` |
| `prompt_processing`: `enhanced`, the default, rewrites the prompt against the image; `original` keeps it | yes (fal) | no (pass-through list empty) | exact prompts; which mode OpenRouter uses is unverified |
| `image_transform`: `center_crop`, the default, or `resize` | yes (fal) | no | a portrait photo is cropped to 16:9 |
| reference images, last frame, sound, 9:16 | no | no | |

3 or 5 s at 16 fps on Wan's 4N+1 frame grid, so a clip runs 3.06 or 5.06 s, billed by the seconds asked. 480p (854x480 on OpenRouter, 832x480 at fal) at $0.005 a second and 720p at $0.01, so 5 s of 720p cost $0.05: on 31 October the cheapest video in framy's catalog by a factor of four. TBC's policy service screens the prompt and the image before rendering.

## Checks that only a paid run settles

The audit priced each; none was run.

| Question | Cheapest run | About |
|:--|:--|:--|
| Do Seedance 2.x, Wan 3.0, Hailuo 3 and HeyGen take reference video and sound through OpenRouter, and are the first reference images of HeyGen and Hailuo 3 free? | one short run each with one image, one 2 s video and one 2 s sound | $0.30 to $1 each |
| Does Grok Imagine Video 1.5 take reference images through OpenRouter? | 1 s at 480p with 2 references | under $0.10 |
| Does Veo 3.1 Fast take 3 references through OpenRouter? | 8 s at 720p | $0.64 |
| How does Aleph 2 get its source, and does it keep the length? | a 2 s clip, the cheap path first (a 400 is not billed) | $0.56 |
| Do the MAI 2.5 models still run? | one image on 2.5 | $0.05 |
| Seedance 2.5 with a first frame and no ratio or duration | 4 s at 480p | about $1.30 |
| Is Kling 3.0 Pro 720p or 1080p? | 3 s | $0.34 |
| Does HY Image use more than 5 references, and what does 1.5K cost? | three runs | $0.08 |
| Does Omni run through framy's adapter from words and from a start frame, and is a refusal read right? (`TestLiveOmni`, issue #33) | two 720p videos and a refused prompt | $0.60 to $2.02 |
| Does Omni keep to `response_format.duration`? | one 3 s video at 720p | about $0.30 |
| How many tokens a second does Omni bill at 360p and 1080p? Gateways list 1,931 and 8,688 (17,376 at 4K); Google publishes only 720p | one video at each, the model picks 3 to 10 s | $0.56 to $1.86 |
| Does OpenRouter bill a Seedance reference clip's seconds beside the output's, as BytePlus does? framy counts them, as OpenRouter does for Wan 3.0 and HeyGen | Seedance 2.0 Mini, a 2 s clip, 4 s of 480p out, compared with `usage.cost` | about $0.12 |
| Does Image Search bring Search Suggestions, and in what shape? (`TestLiveProbes`) | one image on Nano Banana 2.1 at 1K | about $0.034 |
| Do the Nano Banana models take `media_resolution`, and does the input token count drop? | two runs with one reference, `low` and `high` | about $0.07 |
| Does mischief 1 rewrite the prompt through OpenRouter, and how is a portrait photo cropped? | 3 s at 480p from a portrait photo | $0.015 |
| Does Riverflow 2.6 Pro make 5:4, and how far above the $0.13 floor does a run land? | one image at 1K, 5:4 | from $0.13 |
