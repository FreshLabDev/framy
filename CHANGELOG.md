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
  (or `OPENROUTER_API_KEY_FILE`). OpenAI and Gemini have no adapter yet.
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
