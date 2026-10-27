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
- CI on every push and weekly: modules, gofmt, build, vet, race tests,
  database tests on PostgreSQL 17, govulncheck, the image build and the local
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
