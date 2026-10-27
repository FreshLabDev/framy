# Changelog

All notable framy changes are documented here.

The `## <tag>` section of this file *is* the GitHub Release body: the release
workflow copies it verbatim and refuses a tag that has no section. Write it
for whoever has to decide whether to upgrade.

See [`docs/versioning.md`](docs/versioning.md) for what the numbers mean and
[`docs/releases.md`](docs/releases.md) for how a release is published.

## Unreleased

Nothing is released yet. The repository holds the design, the research it
was written from, and the skeleton of the service.

### Added

- The Go service skeleton. Configuration comes from the environment, and any
  secret can instead come from a file named by `NAME_FILE`, as Docker secrets
  do. PostgreSQL is used in the `framy` schema, with migrations embedded in
  the binary. Long polling records every sender, and every group framy is in,
  in `core` through `core.touch`. `Preflight` runs before polling starts.
  `/healthz` reports the version, the database and how fresh polling is.
  SIGTERM stops polling and the HTTP server within 20 seconds.
- `/start` and the family's panels, in the sixteen languages of the family
  and the shape every sibling bot uses (`core/docs/panels.md`): home, help,
  about and the language picker in a direct chat; in a group `/start` is
  ephemeral, so the panel reaches only the one who asked, with the group's
  language for its admins. The language is core's, shared with the other bots:
  a pick writes a manual observation and Follow Telegram withdraws it. The
  command menu and the profile descriptions are published per language at
  start, and a description is written only when it changed.
- `/metrics` with framy's counters in the Prometheus text format, on its own
  listener (`METRICS_ADDR`, `:9090` by default), apart from the public one.
- The model catalog. Every hour framy reads OpenRouter's public catalogs: the
  image models with the price of every upstream route, the video models, and
  their expiration dates. OpenAI's and Gemini's models are written by hand in
  `catalog/openai.yaml` and `catalog/gemini.yaml`, and `catalog/overlay.yaml`
  joins the offers of one model, so Nano Banana 2.1 comes from Gemini and from
  OpenRouter. The overlay also gives display names, the featured and free
  flags, the effort setting, values not to offer such as OpenAI's `auto`,
  pass-through settings, reference counts, what a model needs from an
  attached video, continuation and the face policy. The video editors and the
  upscaler (Aleph 2, FLUX Video Edit, FLUX Video Upscale) are in the video
  list and run only on an attached video; FLUX Video Edit takes up to 15 s and
  50 MiB. Hidden: models within 14 days of leaving, Google's preview ids,
  wrappers, closed OpenAI models and the talking-head model, which needs a
  voice the composer does not take yet. A model gone
  upstream gets `disabled_at` and comes back when it returns. A failed sync
  changes nothing, and one that would disable more than half of OpenRouter's
  offers is refused. `CATALOG_SYNC_INTERVAL` (`1h`, or `off`) and
  `OPENROUTER_API_BASE` configure it.
- The price estimate. Before a run framy works out its price from the
  catalog's rates and the run's settings: OpenRouter's price lines and video
  SKUs, OpenAI's token formula, Gemini's tokens per size, Seedance's video
  tokens, per-reference charges and Aleph's minimum. Each estimate says
  whether it is exact, about, up to, from, or known only after the run, and
  keeps the rates and quantities it used. It agrees with OpenAI's published
  gpt-image-2 prices, OpenRouter's 16:9 example and BytePlus's Seedance 2.0
  example. The overlay's `price` hints fill in what the rates leave out, and
  every sync logs the price lines the estimate cannot read.
- The job queue, on River in the `framy` schema. A job is a row in
  `framy.job` and a River job, created in one transaction, and it moves
  through draft, queued, running and done, failed, cancelled or expired. The
  rules about money: the mark that a paid request is out is written before the
  request goes, so a run cut short by a crash is never sent again; a
  provider's job id is saved the moment it comes back, so a retry asks
  instead of paying; only a 429 or a 5xx before the run started is retried,
  after the provider's wait. A repeated Telegram update starts nothing, one
  person runs at most two jobs at once and the rest wait in order, and a job
  keeps its estimate next to what the provider charged. A stop waits for runs
  in progress up to `JOBS_STOP_GRACE` (3m30s).
- The OpenRouter adapter for images, on `POST /api/v1/images`. A job sends
  its settings, its pass-through settings under the routes' own options and
  its reference images, and goes only to the routes that take them, the
  cheapest first. What no route takes is refused before the call, so it costs
  nothing. `usage.cost` becomes the job's actual cost. A moderated prompt ends
  as `moderation` with its reasons, apart from money, key, refused, timeout
  and provider failures, and an error inside a 200 is read too. Only a 429, a
  provider that is down or overloaded, and a connection never made are
  retried. framy's own key for the free allowance is `OPENROUTER_API_KEY`
  (or `OPENROUTER_API_KEY_FILE`).
- The OpenAI adapter for images, for a person's own key. A run is one call:
  `POST /v1/images/generations`, or `/v1/images/edits` when it has reference
  images, which go as the presigned R2 link OpenAI fetches. `quality` is
  always one of the model's levels, `low` to `max`, and never `auto`; `size`
  is a concrete WxH, any size up to 3840 px that OpenAI's rules allow. A run
  framy can tell is wrong is refused before the call, so it costs nothing.
  `safety_identifier` is a hash of the person's id, never the id. A
  `moderation_blocked` answer ends the run as `moderation` with its stage and
  categories, apart from money, key, refused, timeout and provider failures;
  only a 429 for the rate, a 503 and a connection never made are retried. The
  actual cost is the usage tokens times the catalog's rates for text, image
  input and image output.
- The Gemini adapter for images, for a person's own key. Nano Banana 2.1,
  3.1 Flash, 3.1 Flash Lite and 3 Pro run through the Interactions API, with
  `thinking_level` and Google Search grounding, which OpenRouter does not
  pass. References are fetched from their links and sent inline, so no link
  reaches Google, and the run is sent with `store: false`. A block or a
  `NO_IMAGE` answer is a refusal, not a failure, and the model's own words are
  kept for the person. A key from a project without billing (a 429 with
  `limit: 0`) fails at once as `key` with the status `needs_billing` and is
  never retried; a spent prepaid balance and a wrong key are told apart too.
  The actual cost is the usage tokens times the catalog's rates.
- The free allowance: 5 images in any 24 hours and 1 video in any 7 days per
  Telegram user id, across every surface, counted from the job table. Free
  runs go through framy's own OpenRouter key with fixed settings: MAI 2.6
  Flash with one result, FLUX.3 Klein at 1K for albums of more than 5 photos,
  and the weekly video models. Any other model, setting or number of files is
  refused. A failed run does not count; a run refused for its content does.
  The check and the insert are one transaction under a lock per person and
  one for the budget, so two requests at once cannot both take the last slot.
  Two fuses close the tier for everyone: a budget of `FREE_DAILY_BUDGET_USD`
  ($1 by default) a day, until 00:00 UTC, and the limit on framy's OpenRouter
  key, read every minute from `GET /api/v1/key`. A free run that fails for
  lack of money pauses the tier for 15 minutes. The operator chat is told
  once a day per fuse. A refusal is a typed error that says when the slot
  frees up or until when the tier is closed, for the bot and the site to
  word.
- Provider keys that people paste in: OpenAI, Gemini and OpenRouter. A key is
  checked with the provider when it is saved (the list of models, which costs
  nothing) and kept only encrypted: a new data key for every record,
  AES-256-GCM with the person, the provider and the connection as associated
  data, and the data key wrapped by a key-encryption key read from a file
  (`FRAMY_KEK_FILE`), apart from the database and its backups. A new line in
  that file starts a rotation at the next start: only the data keys are
  rewrapped, the encrypted keys stay as they are. A connection shows its last
  four characters and a status: valid, verified, needs billing, needs
  verification, no credits or invalid. The list of models cannot tell the
  last three apart from a key that works, so those are set by what a run
  shows. A run on OpenRouter, OpenAI or Gemini pays from the person's own
  connection and never falls back to framy's key.
- A key pasted into a direct chat is deleted at once and the bot says that
  keys go in the settings, in all sixteen languages. If Telegram refuses the
  delete, the bot says the message stayed.
- Storage in Cloudflare R2. A photo a person brings goes through one ffmpeg
  subprocess: its format is read from its bytes (JPEG, PNG, WebP, HEIC,
  AVIF), its size from its header, so anything above 64 MP is refused before
  it is decoded, and ffmpeg reads only local files under a name framy picks.
  It joins the tiles of an iPhone HEIC, turns the picture by its orientation,
  drops EXIF and GPS, converts Display P3 to sRGB and brings the long side
  down to 2048 px, keeping PNG for a picture with alpha. Results are copied
  into the bucket the moment a run ends, so they outlive the provider's link,
  and a result fetched from a link goes only to public addresses. Inputs go
  to providers as presigned links that live 15 minutes; an input deleted
  before the run fails the job as `storage` before anything is paid, a job
  takes only its owner's files, and a provider's error that echoes a link
  is stored and logged without it. Deleting a file removes its object and
  its row. The host keeps working files only while their work lasts, and
  stray ones are swept every hour. The operator chat hears each time the
  bucket passes another 10 GB. `R2_ACCOUNT_ID` (or `R2_ENDPOINT`),
  `R2_BUCKET`, `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY` (both also as
  `_FILE`), `WORK_DIR` and `OPERATOR_CHAT_ID` configure it; the image ships
  ffmpeg 8.1. `go test -tags r2live ./internal/r2` checks the real buckets
  and their token by hand.
- Prices in the person's currency: USD, UAH, EUR, PLN or BYN. Rates come
  from the NBU every day, and the next day's after 15:35 Kyiv, with
  `fawazahmed0/currency-api` behind it and for BYN, which the NBU does not
  list; when both fail the last saved rate stays. The sync asks only when the
  saved rates are behind, so a restart costs no request. The default currency
  comes from the country on the site and from the language in the bot, and a
  person's choice is kept in `profile`. An amount is written the way the
  person's language writes it and never rounds down, with the rate's date
  beside it, and a job keeps the rate its price was shown with. `FX_SYNC=off`
  stops the sync.
- The pictures framy puts in a chat before and instead of a result, drawn on
  the CPU without a browser: the album preview, where the photos (one to ten),
  numbered 1 to N, float together over a soft shadow and never leave their
  places; the loop shown while a job runs, where a halftone of dots keeps
  turning from one figure into the next (a blob, a wave, a heart, mountains
  with a sun, a flower, a crescent); and a card for a job that failed, with
  the bot's line beside the cross in the person's language. All three are
  1280×720, 16:9, so a chat shows them as wide as a video. The loops are
  H.264 with no sound track, which Telegram plays as animations, and each
  ends on its own first frame, so they have no seam. A ten-photo album
  renders in under a second and is about 140 KB; the loop takes 0.6 s and
  530 KB. The loop and the cards are kept on disk under a key that changes
  with the design, so each is drawn and uploaded once. The card has no line
  in Chinese, Japanese and Arabic, whose letters the bundled fonts lack.
- The public listener's foundation, in `internal/web`: the site, the API, the
  event streams, the OAuth callbacks and the providers' webhooks all register
  on one server that gives them the same rules. Every HTML response carries
  one CSP that lets only Telegram Web frame the site and lets files in and out
  of the R2 endpoint; there is no X-Frame-Options, and nosniff, no-referrer,
  COOP same-origin and HSTS go on every response. A POST from another site is
  refused (`http.CrossOriginProtection`), except under `/hooks/`. `/api`,
  `/auth`, `/oauth`, `/events` and `/hooks` are never stored; only a hashed
  file under `/assets/` is cached for a year; a person's page is never
  `public`. Cloudflare Free allows one rate-limit rule, so the limits are in
  the service: a bucket per address and per signed-in person, stricter
  for sign-in, uploads and job creation, answering 429 with `Retry-After`.
  The visitor's address is read from `CF-Connecting-IP` only when the request
  comes from `TRUSTED_PROXIES`, the addresses cloudflared connects from. An
  event stream or a slow upload asks for longer deadlines per route. `/` is a
  placeholder page until the frontend lands.
- Sign-in, in `internal/auth`. On the site it is Telegram Login, Telegram's
  OpenID Connect with PKCE: framy checks the id token's signature, issuer,
  audience, expiry, nonce and state, and takes the account from the `id`
  claim, the same number a bot update carries. The session is framy's own,
  since Telegram gives no refresh token: a `__Host-` cookie that lasts 30
  days from sign-in, with only its hash in the database. A refused sign-in
  returns to `/?sign_in=cancelled`, `expired` or `failed`. In the Mini App
  `POST /auth/miniapp` checks `initData` (at most an hour old) and answers
  with a bearer token for an hour, because cookies in the Telegram Web frame
  are unreliable. It has a rate limit of its own, 120 a minute per address,
  since everyone who opens the Mini App calls it and a carrier puts many of
  them behind one address. Every sign-in keeps the name and photo Telegram
  gave (migration 012), since framy may not read names from `core`; the
  photo stays Telegram's link, and only links on `t.me` and Telegram's CDN
  are kept, the only Telegram hosts the CSP lets images come from. Sign out ends one
  session; sign out everywhere ends all of a person's sessions and tokens. The per-person rate limits now count the
  signed-in person. Without `TELEGRAM_LOGIN_CLIENT_ID` and
  `TELEGRAM_LOGIN_CLIENT_SECRET` the site's sign-in answers 503 and the rest
  runs.
- Connect OpenRouter, the way in for people without a key to paste:
  OpenRouter's OAuth with PKCE (S256). `GET /oauth/openrouter/start` sends a
  signed-in person to OpenRouter (anyone else signs in first and comes back);
  the callback checks a `__Host-` cookie against the state, so a flow can be
  finished only in the browser that started it, by the same person, within
  ten minutes, and once. The key OpenRouter issues is checked and encrypted
  like a pasted one. Every outcome returns to
  `/?connect=openrouter&result=connected`, `cancelled`, `expired`, `signin`,
  `refused` or `unavailable`; the code, the verifier and the key are in no
  URL, log line or error. It needs `SITE_URL` and the KEK.
- The job API the site and the Mini App will use, in `internal/api`, for
  the signed-in person. `GET /api/me` says who is signed in: the name and
  photo of the last sign-in, the language, and the currency prices are shown
  in. `POST /api/uploads` takes one photo through the storage pipeline. `POST /api/jobs` starts a job and answers 202 at once
  with its price in the person's currency; a required `Idempotency-Key`
  makes a repeated request return the same job. `GET /api/jobs/{id}` shows a
  job to its owner only, and `GET /api/jobs/{id}/events` follows it over
  SSE: the state now, then every change, a ping every 15 s, a resume from
  `Last-Event-ID`, at most three streams per person. No request waits for a
  generation, so Cloudflare's 125-second limit never cuts one. Changes come
  from triggers on the job row (`pg_notify`), so every path that changes a
  job is announced and a rolled-back change is not.
- Video through OpenRouter (`POST /api/v1/videos`), with start and end
  frames and references sent as presigned links. The provider's job id is
  saved before the first poll, so a retry never pays twice; results are
  downloaded with the key from `/videos/{id}/content`, never from links in
  the answer. A poll every 30 s finishes every video. With `HOOK_PATH_SECRET`
  set and an https `SITE_URL`, a request also carries a webhook address with
  a path nobody can guess, and the webhook only brings the next poll
  forward: it never carries a result. On framy's own key its signature is
  checked when `OPENROUTER_WEBHOOK_SECRET` is set, and a repeated delivery
  does nothing twice. A finished video may be at most 128 MiB, since it is
  held in memory while it is copied to R2. The poll that downloads it gets
  5 minutes and the copy 3 more, where an image's call keeps its 3, so a
  large clip on a slow link is not cut off after it was paid. The free video
  plans (HeyGen, Grok Lite, FLUX Video Edit) run through it.
- The album cache that `/f` in reply to one photo of an album will read.
  Telegram sends every photo of an album as a message of its own, and a reply
  names only one, so framy keeps the photos, videos and image or video
  documents of the albums it sees (migration 013, table `album_item`): where
  they are and the ids Telegram needs to hand each file over again, never the
  text or the caption. A DM is always seen whole. A group is seen only while
  framy is an admin, which needs no right at all, so privacy mode stays on.
  Items are deleted 48 hours after framy first saw them, checked every hour.
  `Bot.Album` returns an album once no new photo has come for 1.5 s, at most
  a cap the caller sets, and `Bot.SeesEveryMessage` asks Telegram on every
  call whether framy is an admin of the chat, so the preview can say how much
  of the album it took. framy now also asks Telegram for `edited_message`,
  since an edit can swap the photo of an item. Nothing sends an album to a
  model yet: `/f` is the next change.
- CI on every push and weekly: modules, gofmt, build, vet, race tests,
  database tests on PostgreSQL 17, govulncheck, the image build, the photo
  and storage tests inside the image, on the ffmpeg it ships, and the local
  Compose file. A tag publishes the image to GHCR and the GitHub Release only
  after CI passed on the tagged commit. Dependabot opens grouped updates
  against `dev`.

- `docs/design.md`: what framy is, the surfaces (site, Mini App, groups, DMs),
  accounts through Telegram, providers and people's keys, the model catalog,
  prices in USD and the person's currency, the free allowance, jobs, `/f` in a
  group with albums, storage in Cloudflare R2, security and operations, the
  changes framy needs in `tg`, and the stages.
- `docs/research/`: the research of 26 October 2026 behind the design, in
  Russian, with sources: image and video models, providers, Telegram, and how
  to build and run the service.
