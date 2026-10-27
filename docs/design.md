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

  This is `internal/auth`. `GET /auth/login` keeps the state, the nonce and the PKCE verifier in a ten-minute `__Host-` cookie (a table would need a write for every visitor), and `GET /auth/callback` clears it whatever happens. A refused sign-in sets no session and returns to `/?sign_in=cancelled`, `expired` or `failed`. A session lasts 30 days from sign-in and is not extended by use (the cookie's `Max-Age` is set once), so a person signs in again once a month. `POST /auth/logout` ends one session, `POST /auth/logout/all` every session and token of the person, and a person's deletion takes theirs with it. `return_to` is a path on the site or nothing. Without `TELEGRAM_LOGIN_CLIENT_ID` and `TELEGRAM_LOGIN_CLIENT_SECRET` (and `SITE_URL`, which makes the redirect URI) the two site routes answer 503 and the rest runs.
- **Mini App.** `initData` is checked with HMAC as in `maply/internal/auth`, with `auth_date` at most an hour old. The server answers with a short-lived bearer token, because cookies in the Telegram Web iframe are unreliable. Since 20 July 2026 Mini App methods work only on the registered origin, so the site and the Mini App share one origin and one frontend.

  `POST /auth/miniapp` takes `{"init_data": "<raw string>"}` and answers `{token, token_type, expires_in, user_id}`; the token is stored as a hash like the cookie and lasts an hour, as long as the same `initData` can still be exchanged. Telegram signs `initData` only when the Mini App opens, so one left open for more than an hour has to be opened again. It needs only the bot token, not the Login client.
- **For code that needs the person.** `Auth.Person(r)` gives the Telegram user id from the cookie or from `Authorization: Bearer`, from a 30-second cache or one query on the primary key; a request with a bearer token is judged by it alone. `Auth.Require(h)` answers 401 `{"error":"sign_in"}` to nobody, and `Auth.PersonKey` is `Person` for `web.Config.Person`, so the per-person rate limits count the signed-in.
- **Signed out.** The site shows the composer, the catalog and prices. "Generate" asks to sign in.

## Providers and keys

Each run uses one **offer**: a model at a provider, paid either by the free allowance or by one of the person's connections.

| Provider | Connect | Check on save | What it unlocks |
|:--|:--|:--|:--|
| OpenRouter | "Connect" button: OAuth PKCE, no app registration; pasting a key as a fallback | `GET /api/v1/key` | 61 image models, 30 video models |
| OpenAI | Paste a key | `GET /v1/models` | `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare`, `gpt-image-2`; no video, Sora closed on 24 September 2026 |
| Gemini | Paste a key from a project with billing | `GET /v1beta/models` | Nano Banana 2.1, 3.1 Flash, 3.1 Flash Lite, 3 Pro; video only `gemini-omni-1.1-flash` |

OpenRouter OAuth (`internal/oauth`, `SITE_URL` for the callback, a KEK file for the key):

- `GET /oauth/openrouter/start` for a signed-in person makes a random `code_verifier` and `state`, keeps the verifier and a hash of the state in `oauth_state` for 10 minutes (one open flow per person; starting again replaces it), puts the state in a `__Host-` cookie too (`HttpOnly; Secure; SameSite=Lax`), and redirects to `openrouter.ai/auth` with the S256 challenge, `state`, `key_label=framy` and `callback_url=<SITE_URL>/oauth/openrouter/callback`. Nobody signed in: a redirect to `/auth/login?return_to=/oauth/openrouter/start`. It is a GET because `openLink` and the redirect after sign-in cannot post; it only opens a flow of the visitor's own, bound to their own browser by the cookie.
- The callback spends the flow of the cookie's state first, whatever follows, then needs the `state` in the URL to equal the cookie, the flow to be under 10 minutes old and the person signed in now to be the person who started it. The cookie ties the end of a flow to the browser that started it, so a state that leaked in a URL or a log finishes nothing; with the code's PKCE verifier and the person check, a callback built by someone else gets no key.
- Then it exchanges the code at `POST /api/v1/auth/keys` and saves the ordinary `sk-or-v1-…` key through `keys.Service.Save` (checked with `GET /api/v1/key`, encrypted, paid from the person's credits). It replaces a key the person had, pasted or not. No code, verifier or key is logged, or put in a URL or an error.
- Every callback ends in a redirect to the site: `/?connect=openrouter&result=<result>`, with `connected`, `cancelled`, `expired` (no flow this browser can finish: older than 10 minutes, replaced, used, or someone else's), `signin` (nobody signed in), `refused` (OpenRouter or the key check said no) or `unavailable` (try later). The frontend reads it, removes it from the address and shows the settings. Disconnecting is `keys.Service.Delete`; the person revokes the key at openrouter.ai/settings/keys, since OpenRouter has no revoke without a management key.
- In the Mini App the flow opens in the external browser through `openLink`. That browser has no session, so it passes through sign-in once and comes back to the start route.

This button is the main door for people without keys, so it gets built first and tested end to end.

A connection has a status that framy can actually back up:

| Status | Meaning |
|:--|:--|
| `valid` | The key passed the check above |
| `verified` | A generation succeeded on it |
| `needs_billing` | Gemini answered 429 with `limit: 0`: the project has no billing |
| `needs_verification` | OpenAI answered 403 about organization verification |
| `no_credits` | OpenRouter 402 or OpenAI `insufficient_quota` |
| `invalid` | 401; the key was revoked or mistyped |

The Gemini adapter proves three of them. A 429 with `limit: 0` (or a quota value of 0), and a 400 `failed_precondition` that asks to enable billing, come back as class `key` with status `needs_billing`; a 402 or "prepayment credits are depleted" as class `money` with `no_credits`; a 401 or `API_KEY_INVALID` as class `key` with `invalid`. None is retried. A 429 with a limit above 0 is a wait and is.

The check on save can say only two things. A key the provider refuses (a 401, Gemini's 400 `API_KEY_INVALID`) is not stored. A key it accepts is stored as `valid`, which claims no more than that the list of models answered: a key without billing, verification or credit lists models too. So `needs_billing`, `needs_verification` and `no_credits` come from the failure of a run, when the adapter's error says so (a Money error alone means `no_credits`, except at Gemini, where a 429 is a quota as often as a missing billing account), and `verified` from the success of one. A 403 on the list is not stored either: the key may be restricted to other calls, and the list is all the check sees. A check that reached no answer (a 429, a 5xx, the network) stores nothing. "Re-check" asks again: a refusal makes the key `invalid`, an acceptance makes any status that named a problem `valid` again and leaves `verified` alone, since the person who fixed the account presses it and the next run sets the status again if it was not fixed. A key marked `invalid` is not used until it is replaced or re-checked; the other statuses still run, since a refused run costs nothing and is how they heal.

Nobody can read the balance behind an OpenAI or Gemini key. For OpenRouter, `/api/v1/key` gives what was spent through the key and what is left of its limit, and that is all framy shows.

**Keys at rest.** Envelope encryption with the standard library: a data key per record, AES-256-GCM through `cipher.NewGCMWithRandomNonce` (Go 1.24+), the data key wrapped by a key-encryption key with the same cipher. The associated data is `user_id | provider | connection_id` for the key and `user_id | provider | connection_id | kek_version` for the wrap. The version is bound where the KEK is used: bound into the key's own ciphertext, it would make every rotation encrypt every key again. The KEK is a file secret with mode 0400 (`FRAMY_KEK_FILE`; a `FRAMY_KEK` variable is refused), not an environment variable that `docker inspect` shows, and it is kept apart from the database backups: losing it loses every stored key. The file has a line per version, `<version>:<base64 of 32 bytes>`; the highest wraps new data keys, and an older line stays while a row still uses it. Rotation rewraps the data keys only and leaves the encrypted keys as they are: add the next line, restart, and the service rewraps every data key at start and logs how many; then remove the old line. Tink is not used: its documentation has no recipe for a local KEK. The person sees the last four characters. A key is decrypted only in the worker, for the length of one request, and never reaches a browser, the Mini App or a log. There is one connection per person and provider, and a key pasted over another is a new connection, so what a run reports about a key cannot land on the one that replaced it. Deleting a connection deletes the row; revoking the key at the provider is the person's step, and the settings page says so. The adapters take their key from `keys.Service.JobKey`, which refuses a free run, since framy's key pays for it, and a key the provider has refused.

**Keys in chat.** Keys are entered on the site or in the Mini App only. A message in a DM that looks like a key (`sk-` followed by a long random run, which covers `sk-proj-` and `sk-or-v1-`, or `AIza`) is deleted at once, nothing is saved, and the bot answers in the person's language that keys go in the settings and that a key pasted into a chat is to be treated as exposed. If Telegram refuses the delete, the answer says that instead of claiming it. The link to the settings comes with the settings page (#20).

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
- **Currencies:** USD, UAH, EUR, PLN and BYN.
- **Default on the site and in the Mini App:** the country from `CF-IPCountry` maps to a currency (UA → UAH, BY → BYN, PL → PLN, a country that pays in euro → EUR), otherwise USD. "XX" and Tor's "T1" are no country.
- **In the bot:** it falls back to the language (`uk` → UAH, `be` → BYN, `pl` → PLN), otherwise USD. Russian is spoken in too many countries to say.
- **Choice:** the person changes the currency in the settings; `profile.currency` keeps it, and empty means the default.

Rates come from the NBU API, UAH per unit, and a cross rate goes through the hryvnia (`internal/fx`). The NBU has no BYN, so BYN always comes from `fawazahmed0/currency-api`, anchored on the NBU's USD of the same day, which makes a price in BYN the USD price times currency-api's rate. currency-api is read from jsDelivr and from pages.dev and the newer day wins, since jsDelivr's cache can be a day behind. It also stands in when the NBU fails, as a warning, not an error; after that the last saved rate stays. A currency-api row never replaces the NBU's for the same day.

The sync asks only when the saved rates are behind: today's rate from midnight Kyiv, and the next day's from 15:35, five minutes after the NBU sets it. A restart costs no request. A day counts as saved when it has every currency; a source that fails is asked again in 15 minutes, one a day behind in an hour. A price uses the latest rate up to today, so the rate fetched in the afternoon applies from midnight. `FX_SYNC=off` stops the sync for work without the network.

Amounts are written in the person's language (`0,89 ₴`, `$0.02`, `0.077 zł`) and never round down: two decimals from 1 up, and below 1 enough decimals, at most four, for two significant digits. The tooltip shows the rate's date. A job keeps the currency its price was shown in, `fx_rate` as units per USD and `fx_date`.

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
- **Fixed settings:** the image model at about 1K with one result; the video model at 5 s and 768p, with an optional start frame and no references. Other controls are locked with "with your key". The server holds the same line: a free job on another model, with another setting or with more files than the model takes is refused before anything is created.
- **Video edit (owner, 27 October):** the weekly video can instead be an edit of a clip up to 5 s on `flux-video-edit`, $0.15, cheaper than the generated one. Either one uses up the week. A longer clip gets the same two ways out as above **(owner)**: pick 5 s of it, or pick a model that takes more, which runs on the person's own key.
- **Payer:** framy's own OpenRouter key (`OPENROUTER_API_KEY`, or a file through `OPENROUTER_API_KEY_FILE`): one bill, no OpenAI limit of 20 images a minute. Without it a free run fails as `key` before anything is paid.
- **Where it is checked.** The jobs queue asks `internal/free` inside the transaction that creates or queues a free job, under a lock per person and then one for the budget, so two requests at once cannot both take the last slot or the last cents. A draft takes no slot until "Generate", which checks again.
- **What counts.** By the time the run was queued. A run takes a slot while it is queued or running, when it is done, and when the provider refused it for its content (`moderation`), so probing costs something. A run that failed otherwise, including one refused for a setting, was cancelled or expired takes nothing. A repeated request is the same run, not a second.
- **Two fuses:**
  - **The day's budget (owner, 27 October):** $1 a day, about 50 free images (`FREE_DAILY_BUDGET_USD`). A run is refused when what the day's free runs have cost, plus its own estimate, would pass it, and the tier stays closed until 00:00 UTC with a plain message. A run costs its estimate while it is queued or running and what the provider charged once it ended. One that may have been paid and has no cost on the books (done with none reported, expired, or failed as a timeout, an interruption or an unreadable answer) keeps its estimate.
  - **The limit on framy's OpenRouter key.** The owner sets a daily limit on the key in OpenRouter. framy reads `GET /api/v1/key` every minute and closes the tier when `limit_remaining`, less the free runs still on their way, cannot pay for the run: until 00:00 UTC for a daily limit, until the limit is raised for any other. A free run that fails as `money` closes it for 15 minutes too, which also covers an account out of credit. A reading older than five minutes is ignored, since OpenRouter's own refusal still stops a run.

  The operator chat (`OPERATOR_CHAT_ID`) is told the first time each fuse closes the tier on a UTC day, and a notice that failed is tried again by the next refusal. A refusal is a typed error that carries which limit, when a slot frees up or until when the tier is closed, and the surfaces word it.

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
- has no `WriteTimeout` on this route: `web.Stream()` removes the read and write deadlines, and the handler sets a write deadline of its own before each write, so a client that stops reading cannot hold it;
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
- **Inputs.** A job's files are found just before the paid request (`jobs.Inputs`), as links the provider can fetch during the call: presigned R2 URLs that live 15 minutes, made anew for each submit, or data URLs. A job takes only its owner's files, written to `job_input` in the transaction that creates it. A file deleted before the run fails the job as `storage` before anything is paid; storage that does not answer is asked again. A presigned link a provider echoes in its error is taken out before the error is stored or logged.

**OpenRouter images** (`internal/providers/openrouter`). `POST /api/v1/images` answers with the image in base64, so a run is one call:
- the model is the offer's OpenRouter id. Settings go as fields of the request, and pass-through ones go under `provider.options.<route>`. OpenRouter lists `seed` and `size` as booleans, meaning only that a route takes them; their values are a whole number and a size;
- references go in `input_references` as `image_url`. A video is refused, since the Images API takes only images;
- `provider.sort` is `price`, so the cheapest route goes first, as the estimate assumes, and OpenRouter may fall back to another. When only some routes take the settings or the number of references, `provider.only` names them. A run that no route takes, a value outside the catalog's or an empty prompt is refused before the call, so it costs nothing;
- `user` is a hash of the person's id (`jobs.SafetyID`), never the id itself. It is a plain SHA-256, so it hides the id from a casual reader, not from someone who tries every Telegram id;
- the actual cost is `usage.cost`. An error inside a 200 is read as well: it arrives when the provider had taken the request, so it is never retried, and its cost is kept when the answer has one.

| OpenRouter's answer | Class | Retried |
|:--|:--|:--|
| 403 with `metadata.reasons` or `flagged_input`, or `error_type` `content_policy_violation` or `refusal` | `moderation` | no |
| 402; a 403 whose `limit_source` is a budget or the key's limit | `money` | no; a 402 for the in-flight budget with `Retry-After` waits that long and goes again |
| 401; any other 403, such as a guardrail on the model | `key` | no |
| 400, 404, 413, 422, image errors such as `image_download_failed` | `refused` | no |
| 429 | `rate_limited` | after `Retry-After` |
| 502, 503, 529, a connection that was never made | `unavailable` | after `Retry-After` or 5 s × requests² |
| 408, 504, Cloudflare's 524 | `timeout` | no: the run may go on and be paid |
| 500, an answer that cannot be read | `provider` | no |

The moderation's reasons and the provider's name go into the job's error; the flagged text itself does not. OpenRouter's documentation calls a 502 a model that is down or an answer it could not read; whether a run that ended in a 502 is ever billed is not checked.

**OpenAI images** (`internal/providers/openai`). A person's own key pays, never framy's: the adapter takes its key from `keys.Service.JobKey`, which refuses a free run, and what an answer proves about the key (`APIError.KeyStatus`: a 401 is `invalid`, a 403 that asks for verification `needs_verification`) becomes the connection's status. A run is one call:
- without references `POST /v1/images/generations`; with references `POST /v1/images/edits` with a JSON body, each reference an `images[].image_url`: the presigned R2 link, which OpenAI fetches, or a data URL. The files framy keeps are PNG or JPEG with the long side at most 2048 px, well inside OpenAI's 50 MB and 16 images; a type OpenAI does not take is refused;
- `quality` is always one of the offer's levels, `low` to `max`. `auto` and no quality at all are refused before the call, since OpenAI would choose how much to spend. `size` is always `WxH` within OpenAI's rules (sides in multiples of 16, a side up to 3840, a ratio up to 3:1, 655,360 to 8,294,400 pixels): any such size goes, not only the catalog's list, which is what the composer draws. A run with no size goes at 1024×1024, the size the estimate assumes. Other settings go as fields when the offer lists them and allows the value. `moderation` stays at OpenAI's default, `auto`;
- `safety_identifier` is `jobs.SafetyID`, the same hash as OpenRouter's `user`: SHA-256 of `framy:` and the Telegram id, the first 16 bytes in hex. OpenAI asks for a stable identifier per person, so that an abusive person is told apart from the key's owner and the whole key is not blocked; the hash gives it without handing over the Telegram id. Being a plain hash of a small number it hides the id from a casual reader, not from someone who tries every id;
- the actual cost is the answer's usage tokens times the offer's own rates: text input, image input (the references) and image output tokens, the lines the estimate is made from. Input tokens the usage does not split are priced at the dearer input rate, so the cost is never below the bill. A refusal or a failure inside a 200 keeps the cost when the answer has a usage.

| OpenAI's answer | Class | Retried |
|:--|:--|:--|
| `moderation_blocked`, with `moderation_stage` (`input`, `output`) and the categories when OpenAI gives them | `moderation` | no |
| a code or type of credit or a limit: `insufficient_quota`, `credit_balance_exhausted`, `billing_hard_limit_reached`, `organization_spend_limit_exceeded`, `project_spend_limit_exceeded`, `organization_usage_limit_exceeded` | `money` | no |
| 401; 403, such as a project without the model or an organization that is not verified; 404 `model_not_found` | `key` | no |
| 403 `unsupported_country_region_territory`: framy's server is where OpenAI does not serve | `provider` | no |
| other 4xx, such as a setting or a link OpenAI cannot fetch | `refused` | no |
| 429 for the rate | `rate_limited` | after `Retry-After` |
| 503, a connection that was never made | `unavailable` | after `Retry-After` or 5 s × requests² |
| 408, 504, Cloudflare's 524 | `timeout` | no: the run may go on and be paid |
| 500, 502, an answer that cannot be read | `provider` | no |

A message OpenAI echoes with the start of a wrong key is cut of it before it is stored. `APIError.NeedsVerification` tells the 403 about an unverified organization from other 403s, for the connection's `needs_verification` status.

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
   - **For an album:** a short loop where the photos, numbered 1…N, float together in a frame (`internal/anim`).
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

### Loops and cards

`internal/anim` draws the three pictures on the CPU, without a browser, and hands the bot a file with its size and length. Every picture is 1280×720, 16:9: a chat shows it as wide as a video and lower than a square, which looked cramped. The preview, the loop and the card are the same size, so the message does not jump when one turns into the next.

- **Look.** The field colour of the Avatar proposal and white. The name in lowercase and the numbers are Sofia Sans Extra Condensed; the card's line is Sofia Sans, shaped with the person's language, because the font draws Bulgarian Cyrillic unless told it is Russian. Square corners, stripes 14 and gaps 8 (7 : 4). The viewfinder of four brackets is the frame of all three.
- **Album preview.** One to ten photos, JPEG or PNG as `internal/media` writes them, 4 s. Each photo keeps its shape in rows, with a white square and its number at its corner. The rows are the split with the biggest product of the smallest photo and the whole collage: no photo is a sliver, and five photos of mixed shapes go in two rows instead of a thin strip across the wide frame. The album floats as one piece: it rises 12 px over a soft shadow and sinks back, and no photo ever leaves its place, so the picture always reads as one album in order. It moves by whole pixels, so the photos stay as sharp as at rest and the file stays small. The loop starts at rest: that is the frame Telegram shows as its still.
- **Generating loop.** 7.2 s, no text and no language. A halftone of dots fills the viewfinder and keeps turning from one figure into the next: a blob of three balls, a wave, a heart, two mountains with a sun, a flower, a crescent, and round again. Each figure has 1.2 s; the next runs into it across the grid in 0.5 s, left to right and back by turns, with a little of every dot's own in it. While it holds, a figure moves (the balls roll, the wave runs, the heart beats, the sun bobs, the flower turns, the crescent rocks) and drifts 24 px sideways, so something moves in every frame while only the dots at its edge change. None of the figures is a logo: the first version gathered the dots into a four-pointed spark, which read as Gemini's. The loop is drawn once, kept under a key that changes with the design, and sent by `file_id`.
- **Error card.** A PNG with the cross in the viewfinder on the left and the bot's line beside it, in at most four lines. The faces cover Latin and Cyrillic, which is thirteen of the sixteen languages. For Chinese, Japanese and Arabic the card has no line (`Media.TextDrawn` is false) and the bot puts the line in the caption.
- **Encoding.** libx264, CRF 22, yuv420p, BT.709, `+faststart`, 30 fps, no sound track: what Telegram plays as an animation. ffmpeg runs with an empty environment and reads only the pipe the frames come through. Every loop is a whole number of turns, so its last frame leads into its first, and a test holds it to that. yuv420p keeps one colour for every 2×2 pixels, which costs about 2 levels on average at the edges of photos and dots whatever the quality; the test of the video checks the colours on the plain field, where a wrong matrix or range shows.
- **Measured** on 10 cores of an M1 Pro, ffmpeg included: the album of ten 2048 px photos in 0.7 s and 142 KB, the generating loop in 0.6 s and 533 KB, a card in 45 ms and 27 KB. With Go limited to one core (ffmpeg still uses all) the album takes 1.8 s and the loop 1.3 s. The ffmpeg in the image (Alpine 3.24, FFmpeg 8.1.3, on ws04) gives the same sizes within 10 KB, and the tests pass there.

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
  - Gemini: an error code of the Interactions API (`image_safety`, `prohibited_content`, `no_image`), or the older `blockReason` and `finishReason`; `NO_IMAGE`, and a finished run with words and no picture, mean the model answered in text, and its words are shown;
  - OpenRouter: 403 with `metadata.reasons`.

  A refusal is shown with its stage and category.
- **Faces.** Some models refuse photos of real people (Omni, Seedance, Veo with `personGeneration`). The picker marks them, so the refusal is not a surprise.
- **`/f` on a photo someone else sent** is allowed as it is **(owner)**: any member can reply with `/f` to any photo in the chat. The result is a reply to the command, so the chat sees who asked for it.

## Storage and retention

- **Inputs** from Telegram and the site go through one ffmpeg subprocess: HEIC, AVIF, WebP, JPEG and PNG, iPhone's tiled HEIC included. ffmpeg decodes HEIC 5–6 times faster than pure Go (0.13 s against 0.75 s for 12 MP). The steps:
  1. **Upload:** streamed to disk under `http.MaxBytesReader`.
  2. **Format:** sniffed from the first bytes, never from the MIME type: Safari sometimes converts HEIC and sometimes does not.
  3. **Check:** anything above 64 MP is refused. The size is read by framy from the header (JPEG SOF, PNG IHDR, the WebP canvas, the largest `ispe` of a HEIF) before anything decodes the file, and `ffprobe` and `ffmpeg` run with `-max_pixels` as well, so a header that lies does not get further.
  4. **Convert:** the file goes under a name framy picks, ffmpeg reads it with the demuxer of the sniffed format (never its own guess) and `-protocol_whitelist file`, with an empty environment and a timeout. It rotates by the orientation and drops all metadata with GPS (`-map_metadata -1`), and the long side becomes at most 2048 px. The output is JPEG, or PNG when there is alpha.

  HEIC cannot come through a pipe, so a temporary file is needed. A Telegram photo is up to 10 MB, width plus height at most 10,000, a ratio of at most 20. Inputs are kept with the job.

  What ffmpeg does on its own and what framy adds (`internal/media`, checked on FFmpeg 8.1 in the image and 9.0):
  - **Tiles.** An iPhone HEIC is a grid of 512 px tiles. `-vf` cannot read it; the filter graph starts from the grid, `[0:g:<n>]`, and the tiles come joined and cropped.
  - **Orientation.** JPEG's EXIF tag and HEIF's `irot`/`imir` turn the picture. An orientation written only into a HEIF's EXIF is ignored, as the HEIF rules say and macOS does.
  - **Colour.** An iPhone shoots in Display P3. ffmpeg keeps a JPEG's profile but loses it when it joins a HEIC's tiles, and models and Telegram read pixels as sRGB. framy finds Display P3 itself, by the colorants of the ICC profile (JPEG APP2, PNG `iCCP`, WebP `ICCP`, HEIF `colr`) or `nclx`/`cICP` primaries 12, and has `scale` convert the pixels (`in_primaries=smpte432`, FFmpeg 8.0 or newer) and drop the profile. Other profiles, such as Adobe RGB, are kept as they are. The result matches macOS ColorSync within 1/255.
  - **Alpha.** PNG and WebP keep it. A HEIF or AVIF stores alpha as a separate picture, which ffmpeg does not join, so such a file comes out as JPEG without it.
  - A cut JPEG is shown as far as it goes, as a browser shows it; a cut upload is refused before, by the length of the body.
- **Results** are copied into R2 when ready, since provider links expire. A result that comes as a link is fetched only from a public address (`internal/safehttp`), over HTTPS, up to 512 MB, and must be a picture or a video.
- **Keys and rows.** Uploads go under `u/<person>/in/<random>.<ext>`, results under `u/<person>/out/<job>-<n>.<ext>`, so keeping a run's results again after a crash writes the same objects and adds no rows. Every file is a row of `asset` with its size, type, SHA-256 and picture size. The object is written before its row and removed before it, and nothing cascades into `asset`: a person or a job with files cannot be deleted before them. R2 refuses a body whose SHA-256 does not match, so a cut write never becomes an object.
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
- **To providers**, input photos go as presigned R2 URLs instead of base64, where the API takes a URL (OpenRouter's `input_references`, OpenAI's `images[].image_url`).
- **The bucket's CORS** allows PUT from the site and GET for the Telegram Web origins, so `downloadFile` works. `Content-Disposition` comes through `response-content-disposition` in the presigned URL.
- **Keys:** an R2 API token limited to the two buckets, as a file secret like the KEK. The account token `framy` (27 October) has Object Read & Write on `framy` and `framy-backups` only, no expiry and no client IP filter, since the links it signs are opened from people's browsers. Its halves are `r2-access-key-id` and `r2-secret-access-key` in the stack's secrets on the home server, root-only. `go test -tags r2live ./internal/r2` checks it against the real buckets: objects, presigned links with the browsers' `Origin`, and a refusal in a third bucket.
- **Turned on:** R2 was activated on 26 October as a $0 subscription on the account that holds `ws04.cc`, with the card already on file. Usage above the free allowance is billed to that card monthly.
- **How long (owner):** until the person deletes it: one result, or the whole account from the settings. Deleting removes the objects and the rows at once; the `file_id`s go with them.
- **Accounting.** framy sums the bytes per person in `asset`, shows it in the settings and tells the operator chat (`OPERATOR_CHAT_ID`) each time the bucket passes another 10 GB, the first being where the free allowance ends. The last step told is kept in `runtime_state`; two writes crossing it at once tell it once, and a message that failed is tried again by the next write.
- **Working files** live under `WORK_DIR`, a directory per upload or result removed when its work ends, failed or not. Whatever is older than an hour is swept at start and every hour.

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

- **Headers.** One CSP for every HTML route. It has `frame-ancestors https://web.telegram.org https://webk.telegram.org https://weba.telegram.org` and no `X-Frame-Options`, which would conflict with the Mini App iframe. `img-src`, `media-src` and `connect-src` allow `https://<account>.r2.cloudflarestorage.com` for presigned files and uploads. `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff` and `Cross-Origin-Opener-Policy: same-origin` on every response, and HSTS for a year. Before launch, check that Rocket Loader, Web Analytics and email obfuscation do not inject scripts the CSP would block.
  - `internal/web` builds the policy once at start and adds it to any response that is HTML, XHTML, SVG or names no type. There is no nonce: the frontend's scripts and styles are files served from the site's own `/assets/`, and an inline block is added by hash (`web.InlineHash`, `Config.CSPExtra`). Besides the R2 origin, `script-src` has `https://telegram.org/js/telegram-web-app.js` and nothing else from outside.
- **CSRF:** `http.CrossOriginProtection` from Go 1.25 around the mux, with a bypass for `/hooks/…`. No state changes on GET. A browser that sends neither `Sec-Fetch-Site` nor `Origin` is taken for a client that is not a browser, and one that sends only `Origin` is checked against `Host`, so the tunnel must pass the original `Host` on. A route that has to take a cross-site POST is added to `csrfBypass` in `internal/web`.
- **Cache.** By default Cloudflare caches by extension, not by cookie:
  - `/api`, `/auth`, `/oauth` (the OAuth callbacks), `/events` and `/hooks` send `no-store`, whatever the handler says;
  - hashed assets send `public, max-age=31536000, immutable`: files under `/assets/` with eight or more hex digits before the extension (`app.3f9a1c2b.js`) or a hex `?v=`, on a 200, 206 or 304 only. An unhashed file there is `no-cache` and an error `no-store`. Public result images follow once they exist (`PublicCache`);
  - private results send `private, no-store` and never `public`: `public` from a handler is rewritten unless its route opted in;
  - HTML with no `Cache-Control` of its own gets `no-cache` (revalidate by ETag) and everything else `no-store`, so nothing is cached because nobody said;
  - the zone's Browser Cache TTL is "Respect Existing Headers", as maply's `?v=hash` lesson showed.
- **`downloadFile` in the Mini App:** `Content-Disposition: attachment` and `Access-Control-Allow-Origin` for the Telegram Web origins, from the bucket's CORS for files in R2 and reflected with `Vary: Origin` for anything the service serves.
- **Rate limits.** Cloudflare Free gives one rate-limit rule and 5 WAF rules, and Bot Fight Mode cannot be skipped by rules. So the limits live in framy:
  - token buckets per person and per IP (`x/time/rate`, in memory, in `internal/web`). Every request is counted in the default class by address; a route in another class is counted there too:

    | Class | Routes | Per address | Per person |
    |:--|:--|:--|:--|
    | default | everything | 30/s, burst 120 | none |
    | auth | `/auth`, `/oauth` | 20/min, burst 10 | none |
    | upload | `Limit(ClassUpload)` | 40/min, burst 40 | 20/min, burst 20 |
    | job | `Limit(ClassJob)` | 20/min, burst 20 | 10/min, burst 10 |
    | hook | `/hooks` | 20/s, burst 100 | none |
    | health | `/healthz` | 1/s, burst 10 | none |

    The numbers are guesses to be set from real traffic. An IPv6 visitor is counted by its /64. The person is whoever `Config.Person` names (`auth.PersonKey`), asked only on routes whose class has a per-person bucket; nobody signed in is counted by address alone. A refusal is a 429 with `Retry-After` and `framy_http_rate_limited_total{class}`;
  - quotas in Postgres;
  - the real IP from `CF-Connecting-IP`, believed only when the TCP peer is in `TRUSTED_PROXIES`, the addresses cloudflared connects from (its Docker network). From any other peer the header, `X-Forwarded-For` and the rest are ignored. The service must publish no port to the host, or the userland proxy hides the visitor behind the gateway address.

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

`maply/internal/anim` cannot be imported from another module, so framy copied what it needs: the canvas, the text shaping and the encoder pipe. Proposal: extract a shared module once both projects change them.

Main tables of the `framy` schema, created by a core migration with its own `framy_core` role, as maply's 018:

| Table | What it stores |
|:--|:--|
| `profile` | `user_id`, currency, default models, delivery to Telegram (the interface language is core's, shared with the other bots); the currency is there since #8, the rest comes with the settings |
| `connection` | provider, encrypted key with its wrapped data key and KEK version, `last4`, status, last success, last error class; one per person and provider |
| `catalog_model`, `catalog_offer` | models with the overlay's flags and traits; offers with their parameters, routes and prices, `expires_on`, `hidden`, `fetched_at`, `disabled_at` |
| `job` | requester (`user_id`), surface, chat and preview message, `dedupe_key`, offer, prompt, settings, `free`, status, idempotency key, River job, paid requests with `submitted_at` and `retry_at`, provider job id, the estimate with its lines, the actual cost, results, error class, the currency with `fx_rate` and `fx_date` |
| `asset` | uploads and results: owner, job, storage key, size, type, picture size, SHA-256, `file_id`, `file_unique_id` |
| `job_input` | the files a job takes, in order; an empty place is a file deleted before the run |
| `album_item` | the 48-hour album cache |
| `fx_rate` | date, code, UAH per unit, source (`nbu` or `currency-api`), fetched at |
| `oauth_state` | the hash of the OAuth state, the PKCE verifier and the person, for 10 minutes; one per person and provider |

The free allowance is counted from `job`, not stored separately.

## Avatar

Proposal: a viewfinder of four corner brackets with a round dot in the middle, the point a camera focuses on, built from rectangles and a circle, so the `brand/` rules 4–7 hold. The first proposal had a four-pointed spark inside, which reads as Gemini's logo, and was dropped on 27 October.

Colour **(owner, 27 October: blue-turquoise rather than plum)**: deep teal `#00606E`, hue about 188°, contrast with white 7.25:1 (rule 2 asks for 7:1). It is brighter and more saturated than makeitMD's petrol `#1F4A4E` (185°) and greener than searchy's steel blue `#2D5574` (206°). Two neighbours were drawn beside it: `#005C70` (191°, 7.6:1), almost the same, and `#075B85` (200°, 7.4:1), bluer and closer to Telegram's own blue, which rule 9 warns against. Plum `#5A2A5E` was the first candidate.

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

Settled on 26 October: `/f` on anyone's photo, rolling windows, every file in R2 until the person deletes it, `mai-image-2.6-flash` as the free image model. Settled on 27 October: the global daily budget of the free tier, $1 a day.

1. The free video models, and Klein for albums and as a fallback: confirm after the blind test.
2. Pre-moderation of free-tier prompts with OpenAI's endpoint while the traffic goes to OpenRouter.
3. Public result links and whether a public gallery exists.
4. A permanent domain.
5. Sign-in other than Telegram on the site: not planned now.
