# framy: design

Draft of 26 October 2026. No code yet. The research behind it is in [research/](research/README.md) (in Russian, with sources).

framy makes images and videos with the models of OpenRouter, OpenAI and Google. The website is the main interface; almost everything it does can also be done in the Mini App and in an ordinary Telegram chat. framy pays for a small free allowance; everything beyond it runs on the person's own keys, at the provider's price, with nothing added on top.

Marks used below: **(owner)** is a decision of the owner of 26 October 2026; **Proposal** is mine and still open to change.

## Principles

| Need | framy approach |
|:--|:--|
| One place to make an image or a video | A site with one prompt line and every model the person's keys unlock |
| Honest price | The provider's price before every run, in USD and in the person's currency; the actual cost after |
| Telegram is not an afterthought | `/f` in any group, whole albums as input, a preview to confirm, the result in place |
| Models change every month | The catalog is data pulled from OpenRouter every hour, not code |
| framy is not a credit reseller | Free: 5 images a day and 1 video a week **(owner)**; the rest on the person's keys **(owner)** |
| Familiar stack | Go, the family's `tg` client, shared Asterfield `core`, one service on ws04 |

## Surfaces

| Surface | What it does | Stage |
|:--|:--|:--|
| Site | Composer, model picker, history, settings, provider connections. The main interface **(owner)** | 1 |
| Mini App | The same frontend on the same origin, opened from the bot | 1, polished in 3 |
| Group chat | `/f` with a preview, confirmation, progress and the result in the chat **(owner)** | 1 |
| DM | The same as a group, plus settings and delivery of results made on the site | 1 |
| Inline mode | The person's past results by `file_id`, a button into the Mini App | 3 |
| Guest mode | `@framyin_bot …` in reply to a photo in a chat where framy is not a member | 3 |

## Accounts

One account per Telegram user id. It is the same number in a bot update (`from.id`), in the Mini App (`initData.user.id`) and on the site (the `id` claim of Telegram Login, not `sub`), so nothing needs linking.

- **Site.** Telegram Login through OpenID Connect at `oauth.telegram.org`: authorization code with PKCE, scopes `openid profile telegram:bot_access`. The last scope lets the bot write to the person's DM, so a result made on the site can arrive in Telegram. The `id_token` is checked against the JWKS (`iss`, `aud` = client id, `exp`). The flow is a server-side redirect with `coreos/go-oidc` and `x/oauth2`, which worked against Telegram's live discovery document. Three details:
  - `AuthStyleInHeader` set explicitly;
  - `SupportedSigningAlgs: RS256` (the provider also lists ES256K);
  - the nonce and the state checked by framy itself, since `Verify` does not check the nonce.

  Telegram issues no refresh token, so framy keeps its own session: a `__Host-` cookie, `HttpOnly; Secure; SameSite=Lax`, with only a hash of the session id in the database. The popup widget is not used: it breaks under `Cross-Origin-Opener-Policy: same-origin`.
- **Mini App.** `initData` is checked with HMAC as in `maply/internal/auth`, with `auth_date` at most an hour old. The server answers with a short-lived bearer token, because cookies in the Telegram Web iframe are unreliable. Since 20 July 2026 Mini App methods work only on the registered origin, so the site and the Mini App share one origin and one frontend.
- **Signed out.** The site shows the composer, the catalog and prices. "Generate" asks to sign in.

## Providers and keys

Each run uses one **offer**: a model at a provider, paid either by the free allowance or by one of the person's connections.

| Provider | Connect | Check on save | What it unlocks |
|:--|:--|:--|:--|
| OpenRouter | "Connect" button: OAuth PKCE, no app registration; pasting a key as a fallback | `GET /api/v1/key` | 61 image models, 30 video models |
| OpenAI | Paste a key | `GET /v1/models` | `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare`, `gpt-image-2`; no video, Sora closed on 24 September 2026 |
| Gemini | Paste a key from a project with billing | `GET /v1beta/models` | Nano Banana 2.1, 3.1 Flash, 3.1 Flash Lite, 3 Pro; video only `gemini-omni-1.1-flash` |

OpenRouter OAuth: the server stores `code_verifier` and `state` for 10 minutes, the callback goes to `https://<domain>/oauth/openrouter/callback`, and the code is exchanged at `POST /api/v1/auth/keys` for an ordinary `sk-or-v1-…` key paid from the person's credits. In the Mini App the flow opens in the external browser through `openLink`. This button is the main door for people without keys, so it gets built first and tested end to end.

A connection has a status that framy can actually back up:

| Status | Meaning |
|:--|:--|
| `valid` | The key passed the check above |
| `verified` | A generation succeeded on it |
| `needs_billing` | Gemini answered 429 with `limit: 0`: the project has no billing |
| `needs_verification` | OpenAI answered 403 about organization verification |
| `no_credits` | OpenRouter 402 or OpenAI `insufficient_quota` |
| `invalid` | 401; the key was revoked or mistyped |

Nobody can read the balance behind an OpenAI or Gemini key. For OpenRouter, `/api/v1/key` gives what was spent through the key and what is left of its limit, and that is all framy shows.

**Keys at rest.** Envelope encryption with the standard library: a data key per record, AES-256-GCM through `cipher.NewGCMWithRandomNonce` (Go 1.24+), `user_id | provider | connection_id | kek_version` as associated data, the data key wrapped by a key-encryption key. The KEK is a file secret with mode 0400 (`FRAMY_KEK_FILE`), not an environment variable that `docker inspect` shows, and it is kept apart from the database backups: losing it loses every stored key. Rotation rewraps the data keys only. Tink is not used: its documentation has no recipe for a local KEK. The person sees the last four characters. A key is decrypted only in the worker, for the length of one request, and never reaches a browser, the Mini App or a log. Deleting a connection deletes the row; revoking the key at the provider is the person's step, and the settings page says so.

**Keys in chat.** Keys are entered on the site or in the Mini App only. Proposal: a message in a DM that looks like a key (`sk-or-v1-`, `sk-`, `AIza`) is deleted at once, nothing is saved, and the bot answers with a link to the settings.

Who pays in a chat: always the person who sent the command, from their free allowance or their own connections, never another member's.

## Model catalog

### Sources

- OpenRouter publishes its catalogs without a key: `GET /api/v1/images/models`, `GET /api/v1/images/models/{id}/endpoints` (the exact price per endpoint) and `GET /api/v1/videos/models`. Only `GET /api/v1/models` carries `expiration_date`, so it is read too. framy pulls all of them every hour, about 65 requests.
- Direct OpenAI and Gemini have no such catalog. Their models are described by hand in the repository in the same schema (`catalog/openai.yaml`, `catalog/gemini.yaml`).
- An overlay file (`catalog/overlay.yaml`) adds what no API says: which offers are the same model, reference limits by kind for video, which models accept `previous_job_id`, pass-through parameters with their allowed values, the face policy, display names, the effort mapping, the featured flag, the free-tier flag.

### Models and offers

A model such as Nano Banana 2.1 has offers `gemini:gemini-nano-banana-2.1` and `openrouter:google/gemini-nano-banana-2.1`. The overlay maps them explicitly; names are not guessed. A model's id is its OpenRouter id, so it stays when a direct offer joins later; a model OpenRouter does not carry, such as Gemini Omni, gets its id in the overlay.

The sync never deletes. An offer that leaves its source gets `disabled_at`, and a model with no offer left is disabled with it; one that comes back is restored. A failed listing changes nothing, a model whose endpoints fail keeps its last row, and a sync that would disable more than half of OpenRouter's offers is refused as a broken answer.

Prices of the same model at different providers are usually equal: OpenRouter charges the provider's price per request and takes its fee of about 5.5% when credits are bought. What differs is where the money comes from and which settings exist:

- only direct Gemini has `thinking_level` and Google Search grounding;
- only direct OpenAI has the mask and any `size` up to 3840 px.

The picker shows each connected offer as its own row: price, provider, and what the offer adds.

### Parameters become controls

Every OpenRouter model describes its parameters as `enum`, `range` or `boolean`, and the hand-written descriptors use the same form. The composer draws controls from them:

| Kind | Control |
|:--|:--|
| `enum` of up to 6 values | segmented control |
| longer `enum` | dropdown |
| `range` | stepper or slider within the range |
| `boolean` | toggle |

A parameter the model does not list is not shown at all, not shown disabled. When the model changes, a value the new model does not allow falls back to its default, and a short note says which.

### Effort

The owner asked for effort settings. Four models have them, each under its own name; framy shows one "Effort" control and a caption with what it becomes:

| Model | Parameter | Values | Effect on price |
|:--|:--|:--|:--|
| GPT Image 2.5 (OpenAI) | `quality` | `low`, `medium`, `high`, `xhigh`, `max` | 1024×1024 from $0.006 to $0.21 |
| Nano Banana 2.1, 3.1 Flash, Lite (Gemini direct) | `thinking_level` | `minimal`, `medium`, `high` (2.1); `minimal`, `high` (3.1) | thinking tokens are billed |
| Riverflow 2.5 | `reasoning` | `low`, `medium`, `high`; Pro also `xhigh` | dynamic price |
| Krea K3 Edit | `thinking_mode` | values not documented | +$0.02 on high |

`quality=auto` and `size=auto` of OpenAI are not offered: their price cannot be known before the run. Neither is `aspect_ratio=auto` of OpenAI's models on OpenRouter, for the same reason.

In the catalog of 27 October only GPT Image 2.5 and the direct Gemini models have effort: Riverflow's `reasoning` is on OpenRouter's Images API neither as a parameter nor as a pass-through, and Krea's values are not documented.

### Hidden

- Models with an OpenRouter `expiration_date` less than 14 days away.
- Models on the OpenAI and Google deprecation pages. Proposal: the service reads both pages weekly and posts changes to the operator chat.
- Duplicates and wrappers: `*-preview` Gemini ids, `openai/gpt-5*-image*`, `openai/gpt-image-1*`, `meta/muse-image` (an eval slug), `openrouter/auto*`.
- An offer with no route, such as `meta/muse-image`, whose endpoint list on the Images API is empty.
- `heygen/avatar-iv`, a talking head from a photo and a voice, until the composer takes a voice.

Until the weekly reader of the deprecation pages exists, the overlay hides such models by hand, `gemini-2.5-flash-image` among them.

### Featured at launch

To be reviewed after the blind tests.

| Kind | Models |
|:--|:--|
| Image | `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare`, Nano Banana 2.1, `gemini-3-pro-image`, `mai-image-2.6`, `flux-3-image`, `seedream-5-0-pro`, `hy-image-v3.5-preview` (20 references), `recraft-v4.1-vector` (SVG), `riverflow-v2.5-pro` (fonts) |
| Video | Veo 3.1 / Fast / Lite (OpenRouter only, through Vertex), `kling-v3.0-pro`, `seedance-2.0`, `seedance-2.5`, `wan-3.0`, `hailuo-3-max`, `grok-imagine-video-1.5-lite`, `heygen-video-1`, `gemini-omni-1.1-flash`, `flux-video-edit`, `aleph-2` |

The video editors and the upscaler (`runway/aleph-2`, `flux-video-edit`, `flux-video-upscale`) are in the same video list. The overlay marks them `source_video.required`: they run only on an attached video.

## Composer

One component for the site and the Mini App. On top, a large prompt line that grows with the text **(owner)**; under it, the attachments; then the model chip, the setting chips and the price next to "Generate". Results appear in a feed below.

### Model picker

Two tabs, Images and Video, with search. Featured models come first. A row shows:

- the model and its maker;
- what it can do: references, 4K, sound, last frame;
- the price for the current settings in USD and the person's currency;
- one line per connected provider.

Proposal: models that need a provider the person has not connected stay in the list, dimmed, with "Connect OpenRouter", so it is visible what a key would unlock.

With a video attached, the models that take one come first and the rest are dimmed with "takes no video". With none, the editors are dimmed with "attach a video". Nothing else changes: it is the same page and the same list.

### Images

| Control | Where it exists |
|:--|:--|
| References, 0 to the model's limit | GPT Image 16, Gemini 14, Seedream 14, Hy 20, FLUX.3 10, Riverflow Pro 10, others fewer |
| Aspect ratio | all; the set differs per model |
| Resolution: 512, 768, 1K, 1.5K, 2K, 4K | Gemini, FLUX.3, Seedream, Qwen, Riverflow, Grok, Hy, Krea; OpenAI takes `size` |
| Count | `n` where supported, otherwise parallel runs; the price multiplies either way |
| Effort | see above |
| Transparent background | OpenAI, Riverflow 2.5 |
| Format | OpenAI (png, jpeg, webp), FLUX.2, Riverflow, Recraft vector (svg) |
| Seed | FLUX.2, Seedream, Krea, Qwen, Hy |
| Model groups | Gemini search, Recraft style, Krea styles and moodboards, FLUX.2 steps and guidance |

No image model takes a negative prompt, so there is no such field. A real mask exists only at OpenAI; the mask editor is a stage 3 feature.

### Video

The mode follows from what is attached **(owner: clear start, end and reference slots)**:

| Attached | Mode | Models |
|:--|:--|:--|
| nothing | text to video | all but the editors |
| an image in "Start" | first frame | all video models but the editors |
| images in "Start" and "End" | first and last frame | Veo, Kling, Seedance, Wan 3.0, Hailuo 3, FLUX 3 Video, Omni |
| images in "References" | reference to video | Seedance 2.x, Wan 3.0, HappyHorse, Hailuo 3, HeyGen, Veo 3.1 and Fast (up to 3), Omni |
| a video: uploaded, or a finished one through "Edit" | edit the video | `flux-video-edit` (up to 15 s and 50 MiB, scaled to 720p, keeps the length and the sound), `aleph-2`, `flux-video-upscale`, Seedance 2.5, Hailuo 3, Omni (up to 10 s when uploaded); FLUX 3 Video continues it |
| video or audio in "References" | reference video or sound | Seedance 2.x, HeyGen, Omni (video only); Wan 3.0 and Hailuo 3 accept video and audio too, but what they do with them is not checked |

The other controls:

- **Duration.** Chips for discrete sets such as Veo's 4, 6 and 8 s, a slider for ranges such as Wan's 2–30 s.
- **Resolution and aspect ratio.**
- **Sound,** with the price difference next to the toggle where it changes the price: Veo, Kling, Seedance 1.5.
- **Seed.**
- **Negative prompt,** where the model takes it as a pass-through: Veo, Kling, Wan 2.6 and 2.7.

Omni has no duration control. It says "length chosen by the model, 3 to 10 s", and its price is "up to".

A video is attached like a photo: dropped into the composer, picked from history, or sent there by "Edit" on a result. On OpenRouter it goes as a presigned R2 URL in `input_references` with `type: video_url`, the only video field of the API; a video OpenRouter made can go as `previous_job_id` instead, on the same model. framy reads its length and size with `ffprobe` before the run.

A clip longer than the model takes is not simply refused **(owner, 27 October)**. The composer offers two ways out:
- pick a part of the allowed length on the clip's timeline, which framy cuts before the run;
- pick a model that takes the whole clip, on the person's own key.

In chat there is no timeline, so the preview offers "First N s", "Pick in framy" (the Mini App opens on the clip's timeline) and the models that take the whole clip. Proposal: a file above a model's size limit but within its length is re-encoded smaller instead, since FLUX Video Edit scales its input to 720p anyway.

Nobody reports progress in percent. The wait shows elapsed time and the model's typical time, measured by framy from its own runs.

### Actions on a result

- Again: the same settings, a new seed.
- Edit: the result becomes a reference; a video becomes the attached video, with the editors first.
- **Animate:** an image becomes the start frame of a video, and the video tab opens.
- Continue: only where the overlay confirms `previous_job_id` or Omni's `previous_interaction_id`.
- Download the original.
- Send to Telegram: the bot sends it to the DM, or `shareMessage` in the Mini App.
- Copy the prompt.
- Delete.

## Price and currency

The composer shows the price before the run. `internal/price` works it out from the rates of the offer's routes and the run's settings, and labels it with one of five kinds:

| Kind | Shown as | When |
|:--|:--|:--|
| `exact` | the price | the rates and the quantities are known: tables by size and quality (FLUX.3, Seedream, Krea, Qwen, Recraft, Grok, Riverflow 2), video seconds by resolution and sound, Seedance's video tokens |
| `about` | "about" | a formula or a rounding decides: OpenAI's and Gemini's tokens, FLUX.2's megapixels, the prompt's tokens, routes at different prices, two video rates that both fit (Kling's sound) |
| `up_to` | "up to" the most | a setting is not chosen yet, or the model picks it: Omni's 3 to 10 s, FLUX Video Upscale's factor, Seedream Pro's high resolution |
| `from` | "from" the least | the provider sets the price after the run: Riverflow 2.5 |
| `unknown` | "known after the run" | nothing says how many output tokens a run makes: Tencent HY Image, Omni at 1080p |

An estimate never rounds in framy's favour, and an approximate one says so. An offer with several routes is priced by the cheapest, and the dearest is the most it can cost, since OpenRouter may fall back to it. The estimate keeps its lines: each billable or SKU, its rate and the quantity.

Where OpenRouter's rates are not enough, a model's `price` in `catalog/overlay.yaml` fills in:
- `tokens`: tokens per output image by resolution (Gemini, MAI), per input image, per second of video (Omni);
- `openai_tokens`: the base of OpenAI's formula by quality;
- `variants`: the settings a dearer rate is for, such as Krea's style references, so it is not taken otherwise;
- `skip`: a billable framy never sends, with the reason, such as Riverflow 2's fonts;
- `from`: why the price is only a floor.

`picks_length` is the range of a model that picks its own length. The build warns about a hint that matches no route; every sync logs the price lines and SKUs the estimate cannot read (`catalog price not understood`), so a new kind of price shows up before someone sees it as "about".

Input images add their own price: per reference at Grok, Seedream Pro, Qwen 3 and K3 Edit, as tokens at OpenAI and Gemini. An attached video changes the price too:
- a model that keeps the source's length (`source_video.keeps_length`) is priced by that length, so the estimate is exact;
- Seedance switches to its `with_video_input` rate, FLUX 3 Video to its continuation rate;
- Aleph 2 costs at least $0.56 a run.

After the run framy records the actual cost:
- OpenRouter: `usage.cost`;
- OpenAI and Gemini: usage tokens times the rates.

A generation keeps `estimate_usd` with the estimate's lines, `actual_usd`, `fx_rate` and `fx_date`. An actual cost more than 20% off the estimate is logged: that is how a price change shows up.

**Currency** **(owner: USD and the person's currency, chosen or by IP)**:
- **Default on the site and in the Mini App:** the country from `CF-IPCountry` maps to a currency (UA → UAH, BY → BYN, PL → PLN, euro area → EUR), otherwise USD.
- **In the bot:** it falls back to the language (`uk` → UAH, `be` → BYN), otherwise USD.
- **Choice:** the person changes the currency in the settings.

Rates come from the NBU API (UAH per unit, cross rates through the hryvnia) once a day and again after 15:30 Kyiv, when the NBU publishes the next day's rate. The fallbacks are `fawazahmed0/currency-api` (jsDelivr, then pages.dev), then the last saved rate. The tooltip shows the rate's date.

Whether `CF-IPCountry` reaches the service through the Cloudflare Tunnel is not checked yet; until it is, nothing but this default depends on it.

## Free tier

**(owner)** 5 images a day and 1 video a week, on cheap OpenRouter models that still edit well.

- **Who:** any signed-in Telegram account. One allowance per Telegram user id across the site, the Mini App, DMs and groups.
- **Window (owner):** rolling. 5 images in any 24 hours and 1 video in any 7 days, as voicy counts its hour of audio. No time zones needed, and no rush at midnight.
- **Models.** The main image model is the owner's choice of 26 October; the other rows are a proposal. Prices are from OpenRouter's endpoints that day, quality from the Artificial Analysis blind arenas (Elo, 1024×1024):

  | Role | Model | Price | Elo edit / text | References | Why |
  |:--|:--|--:|:--|--:|:--|
  | Images **(owner)** | `microsoft/mai-image-2.6-flash` | ≈ $0.0195 | 1144 (6th of 76) / 1119 | 5 | Edits about as well as Nano Banana 2.1 (1151) at 58% of its price |
  | Images, more than 5 photos or a fallback | `black-forest-labs/flux-3-image-klein`, 1K | $0.009 | 1072 / 1043 | 10 | The cheapest model that edits decently; takes a whole album |
  | Video | `heygen/heygen-video-1`, 768p, 5 s | $0.18 | text to video 1157 (4th of 30) | start frame | The best rated video under $0.20 |
  | Video fallback | `x-ai/grok-imagine-video-1.5-lite`, 720p, 5 s | $0.15 | 990 | start frame | Cheaper and from another vendor, weaker |
  | Video edit **(owner)** | `black-forest-labs/flux-video-edit`, a clip up to 5 s | $0.15 | not rated | the clip | Instead of the generated video; keeps the clip's length and sound |

  MAI 2.6 Flash is billed in tokens ($19 per million output tokens), and the price per image is Artificial Analysis's figure, not OpenRouter's. HeyGen at 480p costs $0.10 for 5 s if 768p turns out too expensive. Not chosen: `gpt-image-2.5-flare` at `low` ($0.0059) and `medium` ($0.0132), since nobody has rated those levels. `meta/muse-image` (1129 for $0.01) is not in the Images API.
- **Fixed settings:** the image model at about 1K with one result; the video model at 5 s and 768p, with an optional start frame and no references. Other controls are locked with "with your key".
- **Video edit (owner, 27 October):** the weekly video can instead be an edit of a clip up to 5 s on `flux-video-edit`, $0.15, cheaper than the generated one. Either one uses up the week. A longer clip gets the same two ways out as above **(owner)**: pick 5 s of it, or pick a model that takes more, which runs on the person's own key.
- **Payer:** framy's own OpenRouter key: one bill, no OpenAI limit of 20 images a minute.
- **Two fuses:**
  - a global daily budget: when it is spent, the free tier closes until 00:00 UTC with a plain message;
  - the limit with a daily reset on framy's OpenRouter key.

  Proposal: $5 a day to start, about 250 free images, raised as people arrive. The amount is the owner's call.
- **What counts.** Proposal: a run that failed at the provider (not billed) does not count; a run blocked by moderation does, so probing costs something.

Cost per person who uses the whole allowance, per month:

| Item | Model | Per unit | Per month |
|:--|:--|--:|--:|
| 5 images a day | `mai-image-2.6-flash` | ≈ $0.0195 | ≈ $2.9 |
| | `flux-3-image-klein`, 1K | $0.009 | ≈ $1.35 |
| 1 video a week | `heygen-video-1`, 768p, 5 s | $0.18 | ≈ $0.78 |
| | `grok-imagine-video-1.5-lite`, 720p, 5 s | $0.15 | ≈ $0.65 |
| | or an edit on `flux-video-edit`, 5 s | $0.15 | ≈ $0.65 |

At most about $3.7 a month for a person who uses everything with the main models.

A blind test confirms the choice before launch:
- images: 20–30 typical prompts, half of them edits of photos with people, on MAI 2.6 Flash and FLUX.3 Klein, under $1. MAI is already chosen; the test measures its actual price per image and decides whether Klein is good enough for albums of more than 5 photos and as a fallback;
- video: 10 prompts, 5 of them from a start frame, on HeyGen and Grok Lite, about $3, and 3 edits of 5 s clips on FLUX Video Edit, $0.45.

## Jobs

Everything is a job in Postgres:

```text
draft ──Generate──> queued ──> running ──> done
  │                   │           ├──────> failed
  └──No / 10 min──> cancelled     └──────> expired
```

`draft` exists only for a chat preview waiting for "Generate".

**API.** `POST /api/jobs` answers 202 with the job id at once, and the client follows `/api/jobs/{id}/events` over SSE, with polling as the fallback. Cloudflare answers 524 when the origin sends nothing for 125 seconds, and an OpenAI image can take two minutes, so no request waits for a generation.

**SSE through the Tunnel.** cloudflared flushes every write for `text/event-stream`, and Cloudflare does not compress it. The handler:
- sends the headers at once (`Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform`);
- flushes after every event through `http.NewResponseController`;
- writes a `: ping` comment every 15–20 s;
- has no `WriteTimeout` on this route;
- resumes from `Last-Event-ID`.

`EventSource` cannot send an `Authorization` header, so the Mini App streams with `fetch`. Proposal: at most 3 open streams per person.

**Queue.** River OSS (v0.49.0, MPL-2.0) in the `framy` schema, in `internal/jobs`. A job is a row in `framy.job`, which the surfaces read, and a River job that works it, inserted in one transaction (`InsertTx`). River's migrations run at start under framy's migration lock, since they take none of their own. River's listener needs a real Postgres session, not PgBouncer in transaction mode.
- Queues with their own `MaxWorkers` per process: `openai` 4, `gemini` 4, `openrouter_image` 8, `openrouter_video` 4. `ffmpeg` and `telegram_send` come with their features.
- A repeated update starts nothing: a job keeps a unique `dedupe_key` (`tg:<update_id>`, or the request's key on the site), and "Generate" queues a draft once.
- A background run waits snoozed (`JobSnooze`), which spends no attempt and holds no worker. It is asked every 30 s and expires after an hour.
- River rescues a job whose worker died after 5 minutes; one attempt may take 4.
- A sweep every minute cancels drafts older than 10 minutes and fails a job River gave up on without working it.

Progress for SSE goes through `pg_notify('framy_events', …)` in the transaction that changes the state, with one listener per process.

**Workers.**
- **Images:** one provider call inside the worker, with a 180 s timeout.
- **Video:**
  - submit, then poll. OpenRouter `GET /api/v1/videos/{id}` every 30 s; Omni with `background: true` and `GET /interactions/{id}`.
  - The provider job id is written to the database right after the paid submit, before any polling. Otherwise a retry after a crash pays for the video twice.
  - Every OpenRouter video request carries a `callback_url` with a path nobody can guess, `/hooks/or/<job>/<hmac(job)>`.
    - **framy's own key:** the `X-OpenRouter-Signature` check, an HMAC-SHA256 of `t + "," + raw body` with a 300 s window.
    - **A person's key:** the workspace secret belongs to the person, so the webhook is only a signal, and framy rereads the status with that key.
  - Webhooks are deduplicated by `X-OpenRouter-Idempotency-Key`. Polling every 30 s stays as the fallback. Whether `callback_url` works with OAuth keys at all is not checked.

**Paid requests.** Before a paid request goes out, the job records `submitted_at`. An attempt that finds it set, with no provider job id and no result, fails as `interrupted` and sends nothing: a worker that died mid-call never pays twice. The person can run it again.

**Results.** A result is copied into framy's storage the moment it is ready. Provider links live from one hour to 48 hours, and OpenRouter's unsigned URLs need the key.

**Retries.** Every job has an idempotency key. A retry happens only on a rate-limit 429 or a 5xx before the generation started: the job goes back to `queued` until `retry_at`, the provider's `Retry-After` or 5 s × the requests sent squared, and after 5 refused requests it fails. Never after a timeout without asking the provider for the job's status, and never on money or moderation errors.

**Limits.** Two running jobs per person, counted from `framy.job` under a lock per person, since River has such limits only in Pro. The rest wait in order: a job that ends wakes the next one, and a job waiting out a refusal lets the others go ahead. Proposal: a queue per chat for Telegram sends.

**Stop.** A queued job is cancelled, and its River job with it. A running image cannot be stopped at the provider: it finishes, goes to the history and is not posted to the chat. Whether OpenRouter can cancel a video job has not been checked. A stop of the process waits for runs in progress up to `JOBS_STOP_GRACE` (3 min 30 s, longer than an image call), and the container's `stop_grace_period` is 4 minutes; a run cut short anyway fails as `interrupted`.

## `/f` in a group

### What the command does

| Message | Result |
|:--|:--|
| `/f text`, not a reply | an image from scratch **(owner)** |
| `/f text` in reply to a photo or an image document | that image edited **(owner)** |
| `/f text` in reply to a photo from an album | all photos of the album with one instruction **(owner)** |
| `/f text` in reply to a video or a video document, or as the caption of one's own video | that video edited on the person's video editor, `flux-video-edit` by default **(owner)** |
| `/f text` as the caption of one's own photo or album | those photos edited (proposal) |
| a plain reply to a framy result | the result refined; this reaches the bot even in privacy mode (proposal) |
| `/f` with no text | the preview asks what to do; the next reply to it becomes the prompt (proposal) |

Video in chat uses the same command. Proposal: the preview has an Image / Video switch, and every image result has "Animate".

### Albums

Telegram sends each photo of an album as its own message with a shared `media_group_id`, and `reply_to_message` holds only the photo that was answered. There is no method to fetch a message by id, and a bot in privacy mode does not see the other photos at all.

1. **framy as a group admin with no rights.** An admin bot receives every message. framy caches album items for 48 hours:
   - `chat_id`, `media_group_id`, `message_id`, `file_id`, `file_unique_id`, size, author and date;
   - never the text.

   When `/f` arrives together with an album or right after it, framy waits 1.5 s for the rest. framy asks to be made an admin at the first `/f` on an album, not when it is added. Privacy mode stays on: turning it off is global and makes the bot read every message of every group.
2. **Without admin rights,** the preview shows the one photo and says "I see 1 photo of the album. Make framy an admin to take whole albums."
3. **Stage 3:** a `getMessages` method patched into our own `telegram-bot-api`. The server already calls TDLib's `getMessages` for `forwardMessages`, so the patch is about 40–60 lines of C++. Whether Telegram gives a bot messages it did not see is the open part.

An album sent before framy became an admin is not in the cache. The preview always shows what was actually taken, so a wrong guess becomes the person's decision, not a silent error.

### Step by step

1. **The command arrives.** framy puts a 👀 reaction on it (`setMessageReaction`) and sends `upload_photo` (`sendChatAction`).
2. **The sources are collected:**
   - the reply, or the album from the cache;
   - downloaded through the local Bot API server (`getFile`, then read from disk);
   - normalized (see Storage);
   - checked against the model's reference limit. Extra photos are dropped, and the preview says so.
3. **A job is created** as a draft. It holds the chat, the topic, the requester, the command, the sources by `file_unique_id`, the prompt, the offer, the settings, and an expiry 10 minutes ahead.
4. **The preview is sent as a reply to the command** **(owner)**:
   - **For an album:** a short loop where the photos fly into a frame and get numbers 1…N (`internal/anim`).
   - **For one photo or none:** a drawn card.
   - **The caption:** the prompt (in an expandable quote when long), the model, what was taken ("3 photos from the album"), and the price for the requester ("free · 4 of 5 left" or "$0.034 · 1.53 ₴ · OpenRouter").
   - **The buttons:**
     - **Generate** (`style: success`) and **No** (`style: danger`);
     - **Model**, **Settings** and **Image / Video**;
     - **Open on the site**: a link to `t.me/framyin_bot/app?startapp=<signed job id>`, because `web_app` buttons work only in DMs.
5. **No:** `deleteMessage` on the preview, and the job is cancelled **(owner)**.
6. **Model or Settings:** `editMessageReplyMarkup` with the choices, and `editMessageCaption` for the new price. Nothing new is posted. Stage 3, if framy is an admin: an ephemeral panel only the requester sees.
7. **Generate:** `editMessageMedia` turns the preview into the prepared "generating" loop by its `file_id`, with no upload **(owner)**. The caption says "generating · model · usually ~40 s", and the only button is Stop.
8. **While it runs:** `sendChatAction` every 4–5 s, and the caption shows the elapsed time at most every 10 s.
9. **Done:**
   - An image ready within about 25 s: `editMessageMedia` turns the loop into the result in place **(owner)**.
   - A video, or a slower image: a new message as a reply to the command, and the loop is deleted **(owner)**. An edit makes no notification; a reply does.
   - The caption holds the prompt and the model.
   - The buttons are **Again**, **Animate**, **Original** (a full-size `sendDocument` on demand) and **Open on the site**.
   - The reaction on the command becomes ✅.
   - The result's `file_id` and `file_unique_id` are kept, and so is `(chat, message_id) → generation` for replies.
10. **Failure:** `editMessageMedia` into a static error card (media cannot turn back into text). The card gives the reason in plain words: moderation, no credits, the key, the model. The buttons are **Retry** and **Change model**.
11. **An unconfirmed preview** is deleted after 10 minutes.

In groups one run makes one result. Proposal: more than one only on the site and in DMs, since an album cannot carry buttons.

### Who can press

- Only the requester can press Generate, Model and Settings. Anyone else gets a short note from `answerCallbackQuery`.
- Proposal: group admins can also press No, to clean up.
- State changes are compare-and-set, so a double tap cannot start two runs.

### Limits of the chat

- **Message rate:** Telegram allows about 20 bot messages a minute in a group. framy queues sends per chat and keeps edits to the cadence above.
- **Video in large groups:** sending one can return `message_id = 0` while the server re-encodes. Such a message is not edited or deleted right away.
- **Media to text:** media cannot become text again. Back to text means delete and send anew.

## DM

- **The same flow.** An album arrives whole here: collect by `media_group_id` with the same 1.5 s wait.
- **Confirmation.** Proposal: in a DM the person can turn confirmation off.
- **`/start`:**
  - a panel with the free allowance left, the connected providers, the default models, the language and the currency;
  - "Open framy" as a `web_app` button.
  - The menu button opens the Mini App.
- **Results made on the site** come here when the person has allowed it (`telegram:bot_access` at sign-in, or `/start`).
- **Message effects.** A finished result can carry a message effect (DMs only).
- **Stage 3:** progress through `sendRichMessageDraft` with Stop (`can_stop`, Bot API 10.3).

## Mini App

The same frontend as the site, opened from:
- the menu button;
- the Main Mini App in the bot's profile;
- the "Open on the site" link of a group preview, which carries a signed job id;
- the inline results button.

- **Job link.** A signed `startapp` payload (HMAC, as in `maply/internal/auth/payload.go`) carries the job id and an expiry. The server opens a draft only for its requester.
- **Native controls.** Fullscreen when viewing a result, safe areas, MainButton as "Generate", BackButton, haptics.
- **Sending a result.** `savePreparedInlineMessage` and `shareMessage` send it to any chat the person picks.
- **The original:** `downloadFile`. It needs `Content-Disposition: attachment` and CORS for `https://web.telegram.org`.
- **Settings:** `CloudStorage` for interface preferences; everything else is on the server.
- **OpenRouter connect** opens the external browser through `openLink` and returns to Telegram.
- **Keys:** not kept in `SecureStorage`. A run from a group happens on the server, so the key has to be there.

## Site

Temporary domain `framy.ws04.cc`. Mobile first; on a wide screen the composer and the feed sit side by side.

| Page | What it holds | Signed in |
|:--|:--|:--|
| Create | Composer and the feed of recent results | to generate |
| History | Every generation: images or video, search by prompt, filters | yes |
| Result `/r/<id>` | One result with its settings, price and actions. Private by default; "Share" makes a public link with an OG image (proposal) | owner of the result |
| Models | The catalog with prices, what each model can do | no |
| Settings | Providers, currency, language, defaults, delivery to Telegram | yes |

- References come by drag and drop, a paste from the clipboard, or a file picker.
- Progress arrives over SSE.
- **Look (owner):** the Asterfield design system, and original rather than the usual generator layout of a chat box over a grid of pictures. One field colour and white; DIN Condensed for figures and DIN Alternate for labels, with Sofia Sans standing in on the web; stripes and gaps in the 7 : 4 proportion; square corners; no shadows, gradients or rounded panels. framy's field colour is the avatar's (see Avatar).
- The interface comes in the family's 16 languages, with `i18n` taken from maply.

## Content and other people's photos

- **Provider moderation applies everywhere:**
  - OpenAI with `moderation=auto` and `safety_identifier`, a hash of the user id;
  - Google's safety filters;
  - each OpenRouter model's own.

  framy shows a refusal as a plain reason, never as a raw error.
- **Proposal for the free tier:** prompts and input photos are checked first with `omni-moderation-latest` on framy's own key, since a block lands on framy's account. The endpoint is free and takes images up to 20 MB, with two limits:
  - for an image it scores only `sexual`, `violence`, `violence/graphic` and the `self-harm` group; `sexual/minors`, `hate` and `harassment` are scored from text only;
  - OpenAI's old FAQ allowed free use only for traffic that goes to the OpenAI API. The current page just says "free to use". The free tier runs on OpenRouter, so this is a grey area, and the owner decides whether to accept it or ask OpenAI.
- **Refusals** are told apart from failures by the response body, even at HTTP 200:
  - OpenAI: `moderation_blocked` with `moderation_stage`;
  - Gemini: `blockReason` or `finishReason` such as `IMAGE_SAFETY`; `NO_IMAGE` means the model answered in text;
  - OpenRouter: 403 with `metadata.reasons`.

  A refusal is shown with its stage and category.
- **Faces.** Some models refuse photos of real people (Omni, Seedance, Veo with `personGeneration`). The picker marks them, so the refusal is not a surprise.
- **`/f` on a photo someone else sent** is allowed as it is **(owner)**: any member can reply with `/f` to any photo in the chat. The result is a reply to the command, so the chat sees who asked for it.

## Storage and retention

- **Inputs** from Telegram and the site go through one ffmpeg subprocess: HEIC, AVIF, WebP, JPEG and PNG, iPhone's tiled HEIC included. ffmpeg decodes HEIC 5–6 times faster than pure Go (0.13 s against 0.75 s for 12 MP). The steps:
  1. **Upload:** streamed to disk under `http.MaxBytesReader`.
  2. **Format:** sniffed from the first bytes, never from the MIME type: Safari sometimes converts HEIC and sometimes does not.
  3. **Check:** `ffprobe` refuses anything above about 64 MP.
  4. **Convert:** the file goes under a name framy picks, and ffmpeg runs with `-protocol_whitelist file`. It rotates by EXIF and drops all metadata with GPS (`-map_metadata -1`), and the long side becomes at most 2048 px. The output is JPEG, or PNG when there is alpha.

  HEIC cannot come through a pipe, so a temporary file is needed. A Telegram photo is up to 10 MB, width plus height at most 10,000, a ratio of at most 20. Inputs are kept with the job.
- **Results** are copied into R2 when ready, since provider links expire.
- **For Telegram**, the photo goes as a JPEG within those limits (long side at most 2560), and the original goes as a document on demand.
- **Video** with H.264, yuv420p and AAC or no sound is only remuxed with `-c copy -movflags +faststart`. Anything else is re-encoded to H.264 High yuv420p with AAC and even sizes. `width`, `height` and `duration` come from `ffprobe` after processing, with `supports_streaming=true`. The last frame for "Continue" is `-sseof -1 … -update 1` saved as PNG, or a frame 0.2–0.5 s before the end when the clip fades out. A part of a clip is cut with `-ss` and `-t` and re-encoded, since `-c copy` cuts only at keyframes.
- **`file_id`s** are kept for as long as the generation exists. They are cheap and make inline mode and resending free.
- **Where (owner, 26 October evening):** every input and result is in Cloudflare R2, in a private bucket `framy` with the location hint `eeur`. ws04 keeps only working files: what ffmpeg is processing and what the bot is sending by local path. They are deleted once the job is done.
- **Why R2 and not the ws04 disk.** Cloudflare's policy on delivering video says that on Free, Pro and Business, traffic of a Tunnel's public hostname falls under its limit on video and on "a disproportionate amount" of large files. Files served from R2 are exempt. R2 also takes the files off the home uplink, serves `Range` requests for seeking, and gives up nothing on egress.
- **Price** ([R2 pricing](https://developers.cloudflare.com/r2/pricing/)):
  - storage: 10 GB-month free, then $0.015 per GB a month (100 GB ≈ $1.35, 1 TB ≈ $14.85);
  - writes: 1 million a month free, then $4.50 per million;
  - reads: 10 million a month free, then $0.36 per million;
  - traffic out: free.

  A free-tier user at the limit adds about 250 MB a month, so 10 GB is about 40 such months. People on their own keys add more, and that is what the price per GB covers.
- **Serving:**
  - private results go through presigned GET URLs, which framy issues per view with a short expiry. They work only on `<account>.r2.cloudflarestorage.com`, which has no Cloudflare cache, but its egress is free too;
  - public result links, if they come, use a custom domain on the bucket. It must be a first-level name such as `framy-media.ws04.cc`, since Universal SSL does not cover `media.framy.ws04.cc`.
- **Uploads from the site:**
  - photos come through the service, under Cloudflare's 100 MB per request;
  - video references go by presigned PUT straight into `incoming/`, which a lifecycle rule clears after a day. framy checks the size with HEAD and the content by its bytes before taking it.
- **To providers**, input photos go as presigned R2 URLs instead of base64, where the API takes a URL (OpenRouter's `input_references`).
- **The bucket's CORS** allows PUT from the site and GET for the Telegram Web origins, so `downloadFile` works. `Content-Disposition` comes through `response-content-disposition` in the presigned URL.
- **Keys:** an R2 API token limited to the two buckets, as a file secret like the KEK.
- **Turned on:** R2 was activated on 26 October as a $0 subscription on the account that holds `ws04.cc`, with the card already on file. Usage above the free allowance is billed to that card monthly.
- **How long (owner):** until the person deletes it: one result, or the whole account from the settings. Deleting removes the objects and the rows at once; the `file_id`s go with them.
- **Accounting.** framy sums the bytes per person in `asset`, shows it in the settings and tells the operator chat when the bucket passes 10 GB, where the free allowance ends.

## Telegram setup

- **Bot:** register `@framyin_bot` (`framybot` and `framy_bot` are taken).
- **BotFather:**
  - the `/f` command, also through `setMyCommands` for `all_group_chats`;
  - inline mode with feedback;
  - Main Mini App;
  - the Login Widget client with the site's URLs, which gives the client id and secret;
  - later, guest mode.
- **`allowed_updates`:** `message`, `edited_message`, `callback_query`, `inline_query`, `chosen_inline_result`, `my_chat_member`; later `guest_message` and `stopped_message_generation`.
- **Server:** our `telegram-bot-api` in `--local` mode. framy and the server share a volume, so a result is sent by a path on that volume (`InputFileLocal`) instead of an upload. Uploads up to 2,000 MB and downloads of any size.

## Changes to tg

`tg` keeps the protocol and leaves the product to the bot (`tg/AGENTS.md`). A method moves into `methods.go` when a second bot needs it. framy is that second bot for several things maply does on its own today (`maply/internal/telegram`): the `web_app` button, `sendAnimation` through an upload, prepared inline messages, the menu button, profile descriptions, `editMessageCaption`. They move into `tg`, and maply drops its copies: maply issues [#80](https://github.com/FreshLabDev/maply/issues/80) (the `web_app` button), [#81](https://github.com/FreshLabDev/maply/issues/81) (animations and the second HTTP client), [#82](https://github.com/FreshLabDev/maply/issues/82) (captions, menu button, profile texts), [#83](https://github.com/FreshLabDev/maply/issues/83) (prepared inline messages).

Every change below is an issue in FreshLabDev/tg, [#2](https://github.com/FreshLabDev/tg/issues/2) to [#18](https://github.com/FreshLabDev/tg/issues/18), on the framy board ([projects/3](https://github.com/orgs/FreshLabDev/projects/3), view "tg changes"). The maply and framy issues that wait for them are marked as blocked by them.

### Stage 1

| Change | Why |
|:--|:--|
| `Message`: `media_group_id`, `live_photo`, `external_reply`, `has_media_spoiler` | albums; a live photo also fills `photo`. `Animation` and `Update.EditedMessage` are already in v0.1.1-alpha.1 |
| `SendAnimation` and `InputMediaAnimation` | the preview loop and the generating loop; maply now uploads a document beside `tg` with its own HTTP client |
| `EditMessageMedia` returns the `Message` | the `file_id` of an uploaded result |
| `EditMessageMedia` takes HTML captions, `show_caption_above_media`, `has_spoiler` | captions in the same call |
| `EditMessageCaption` | the price and the elapsed time |
| `SetMessageReaction`, `DeleteMessageReaction` | 👀 and ✅ on the command |
| `DeleteMessages` | cleaning up expired previews |
| `InlineKeyboardButton`: `web_app`, `login_url`, `copy_text` | "Open framy" in DMs, sign-in to the site from the bot, copy the prompt |
| `SetChatMenuButton`, `SetMyDescription`, `SetMyShortDescription` | moved from maply |
| `probeSafe`: `sendAnimation`, `setMessageReaction`, `deleteMessageReaction`, `deleteMessages` | Preflight can require them; `editMessageCaption` is already there |
| Downloads emit an `Event` | downloads are the slowest thing framy does |

### Stage 2

- `SavePreparedInlineMessage` with photo, video and `mpeg4_gif` results, moved from maply: `shareMessage` in the Mini App.
- `VideoOptions`: `cover`, `start_timestamp`, `show_caption_above_media`. `SendMessage` options for `disable_notification`, `protect_content` and `message_effect_id`. `docs/limitations.md` already says the next variant should become an option rather than a fourth method.

### Stage 3

- Inline:
  - `InlineQueryResultCachedPhoto`, `CachedVideo`, `CachedMpeg4Gif`;
  - `InlineAnswer.Button` (`InlineQueryResultsButton` with `web_app`);
  - `EditMessageText` and `EditMessageMedia` by `inline_message_id`.
- `CallbackQuery.Message` becomes a pointer, and `inline_message_id` is added. A callback from an inline or guest message has no message. This is a breaking change: a minor bump before v1.0.0, and every consumer updates.
- `AnswerGuestQuery` and the `guest_message` update.
- Ephemeral media and `EditEphemeralMessageMedia`; today `ephemeral.go` has text and rich only.
- `SendMessageDraft` and `SendRichMessageDraft` with `can_stop` and `keep_on_stop`, and the `stopped_message_generation` update.
- `BotCommandScope` with a chat id and a user id.
- `GetMessages`, only if the server patch lands. `Preflight` already refuses a server without it.

Uploads are held in memory whole (`docs/limitations.md`). framy sends large files by path through the shared volume, so this does not block it.

## Security and operations

Details and sources in [research/build.md](research/build.md).

- **Headers.** One CSP for every HTML route. It has `frame-ancestors https://web.telegram.org https://webk.telegram.org https://weba.telegram.org` and no `X-Frame-Options`, which would conflict with the Mini App iframe. `img-src`, `media-src` and `connect-src` allow `https://<account>.r2.cloudflarestorage.com` for presigned files and uploads. `Referrer-Policy: no-referrer` on pages with a token in the URL. HSTS. Before launch, check that Rocket Loader, Web Analytics and email obfuscation do not inject scripts the CSP would block.
- **CSRF:** `http.CrossOriginProtection` from Go 1.25 around the mux, with a bypass for `/hooks/…`. No state changes on GET.
- **Cache.** By default Cloudflare caches by extension, not by cookie:
  - `/api`, `/auth`, `/events` and `/hooks` send `no-store`;
  - hashed assets and public result images send `public, max-age=31536000, immutable`;
  - private results send `private, no-store` and never `public`;
  - the zone's Browser Cache TTL is "Respect Existing Headers", as maply's `?v=hash` lesson showed.
- **`downloadFile` in the Mini App:** `Content-Disposition: attachment` and `Access-Control-Allow-Origin` for the Telegram Web origins, from the bucket's CORS for files in R2 and reflected with `Vary: Origin` for anything the service serves.
- **Rate limits.** Cloudflare Free gives one rate-limit rule and 5 WAF rules, and Bot Fight Mode cannot be skipped by rules. So the limits live in framy:
  - token buckets per person and per IP (`x/time/rate`), stricter on sign-in, uploads and job creation;
  - quotas in Postgres;
  - the real IP from `CF-Connecting-IP`, trusted only because the service publishes no port to the host.

  Bot Fight Mode stays off for the framy hostname, or OpenRouter's webhook POSTs get challenged.
- **Uploads** through Cloudflare are at most 100 MB per request. Photos come to the service; videos go by presigned PUT straight into R2 (see "Storage and retention").
- **Outgoing requests** to URLs from providers go through a dialer that refuses private addresses (SSRF).
- **Backups:** a daily `pg_dump -Fc -n framy`, encrypted with `age`, into the R2 bucket `framy-backups` with a 30-day lifecycle, and a monthly restore test. Objects in `framy` are not backed up: R2 is their copy off the home server. The KEK is stored apart from the dumps. The files of `telegram-bot-api` need cleaning by age; how much it keeps is not checked.
- **Metrics** on an internal port:
  - `framy_job_cost_usd_total{provider,model,byok}`;
  - `framy_job_duration_seconds`;
  - `framy_job_failures_total{reason}` with `moderation`, `provider_error`, `timeout`, `quota`;
  - the queue length and the open streams.

  Each job keeps a snapshot of the price it was estimated with. An alert fires on `limit_remaining` of framy's OpenRouter key.
- **Deleting an account** removes the rows, the files and the encrypted keys at once.

## Architecture

```text
framy (Go, one service on ws04)
  ├─ HTTP: site and Mini App statics, API, SSE, OAuth callbacks, provider webhooks
  ├─ bot: long polling through tg, /f, album cache, delivery
  ├─ workers: jobs in Postgres, provider adapters (openrouter, openai, gemini), result download
  ├─ catalog: hourly OpenRouter sync, local descriptors and overlay, daily FX rates
  ├─ price: the estimate before a run, from the catalog's rates
  └─ renderer: loops and previews (internal/anim, from maply), error and preview cards

telegram-bot-api (--local), sharing a volume with framy
core-postgres: framy schema; identity and language from core
Cloudflare R2: bucket framy (inputs and results), bucket framy-backups
Cloudflare Tunnel: framy.ws04.cc
```

`maply/internal/anim` cannot be imported from another module. Proposal: copy what framy needs at first, and extract a shared module once both projects change it.

Main tables of the `framy` schema, created by a core migration with its own `framy_core` role, as maply's 018:

| Table | What it stores |
|:--|:--|
| `profile` | `user_id`, currency, default models, delivery to Telegram (the interface language is core's, shared with the other bots) |
| `connection` | provider, encrypted key, `last4`, status, last success, last error class |
| `catalog_model`, `catalog_offer` | models with the overlay's flags and traits; offers with their parameters, routes and prices, `expires_on`, `hidden`, `fetched_at`, `disabled_at` |
| `job` | requester (`user_id`), surface, chat and preview message, `dedupe_key`, offer, prompt, settings, `free`, status, idempotency key, River job, paid requests with `submitted_at` and `retry_at`, provider job id, the estimate with its lines, the actual cost, results, error class; FX comes with the currency (#8) |
| `asset` | inputs and results: storage key, size, type, `file_id`, `file_unique_id` |
| `album_item` | the 48-hour album cache |
| `fx_rate` | date, code, UAH per unit, source |
| `oauth_state` | PKCE verifier and state for 10 minutes |

The free allowance is counted from `job`, not stored separately.

## Avatar

Proposal: a viewfinder of four corner brackets with a four-pointed spark inside, built from rectangles and 45° diamonds, so the `brand/` rules 4–7 hold. Candidate colour: deep plum `#5A2A5E`, hue about 295°, contrast with white about 10.8:1. It sits between quoto's plum-violet (260°) and voicy's wine (340°) and needs checking next to them on a dark theme.

## Name

framy **(owner)**: a frame is both a still and a shot. Written in lowercase everywhere. Username `@framyin_bot`, like `@voicyin_bot` and `@maplyin_bot`; it had no t.me page on 26 October.

## Stages

1. **Images.**
   - The site: sign-in, composer, catalog sync, picker with prices and currency, history, settings.
   - OpenRouter connect and pasted OpenAI and Gemini keys.
   - The free images.
   - `/f` in groups and DMs: preview, Generate and No, the loop, the result, the album cache through admin rights.
2. **Video.**
   - Editing an attached video, from an upload, a result or a reply in chat; continuing one.
   - Start, end and reference slots, duration, sound, the wait.
   - Animate, the weekly free video, delivery to Telegram.
   - OpenRouter polling and webhooks, Omni.
3. **Telegram in depth.**
   - Inline history, the ephemeral settings panel, guest mode.
   - The `getMessages` patch.
   - DM drafts with Stop.
   - The mask editor.

## Before code

Live tests on a test bot and the real tunnel (details in [research/telegram.md](research/telegram.md), section 10.4):

1. **Albums:** an admin with no rights receives every photo of an album (T2).
2. **`editMessageMedia` in a group:** photo → animation → photo, and → video (T6).
3. **Ephemeral command** in reply to a photo: does `reply_to_message` come (T5).
4. **Guest mode:** a reply to a photo, edits of the answer, callback buttons (T9).
5. **`CF-IPCountry`** behind our tunnel.
6. **OpenRouter OAuth** end to end, from the Mini App too.
7. **Prices:** estimate against `usage.cost` on 10 runs per pricing kind.
8. **Blind tests** of the free-tier candidates.
9. **Files:** HEIC from real iPhones (Portrait, Live Photo, HDR) through the ffmpeg in framy's image; the final MP4s on iOS, Android, Desktop and Web.
10. **OpenRouter webhooks:** `callback_url` with an OAuth key, and whether Bot Fight Mode lets the POST through.
11. **Cloudflare:** "Respect Existing Headers" on Free, checked with `curl -I`; whether `downloadFile` follows a redirect.
12. **Video editing:** one short clip through each editor and Seedance 2.5, as `input_references` and as `previous_job_id`; which field each one honours, whether Aleph 2 keeps the length, about $2.

## Open questions

Settled on 26 October: `/f` on anyone's photo, rolling windows, every file in R2 until the person deletes it, `mai-image-2.6-flash` as the free image model.

1. The free video models, and Klein for albums and as a fallback: confirm after the blind test.
2. The global daily budget of the free tier.
3. Pre-moderation of free-tier prompts with OpenAI's endpoint while the traffic goes to OpenRouter.
4. Public result links and whether a public gallery exists.
5. A permanent domain.
6. Sign-in other than Telegram on the site: not planned now.
