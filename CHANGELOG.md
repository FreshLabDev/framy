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

- `docs/design.md`: what framy is, the surfaces (site, Mini App, groups, DMs),
  accounts through Telegram, providers and people's keys, the model catalog,
  prices in USD and the person's currency, the free allowance, jobs, `/f` in a
  group with albums, storage in Cloudflare R2, security and operations, the
  changes framy needs in `tg`, and the stages.
- `docs/research/`: the research of 26 October 2026 behind the design, in
  Russian, with sources: image and video models, providers, Telegram, and how
  to build and run the service.
