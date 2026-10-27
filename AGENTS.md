# AGENTS.md

This file is for coding agents working on framy. Keep it honest about money,
careful with people's keys, and production-minded.

## Project Shape

- framy makes images and videos with the models of OpenRouter, OpenAI and
  Google. The website is the main interface; almost everything it does also
  works in the Mini App and in an ordinary Telegram chat. The product is in
  [`docs/design.md`](docs/design.md), the research behind it (in Russian, with
  sources) in [`docs/research/`](docs/research/README.md). Read the design
  before changing behavior.
- The Go service so far: configuration from the environment and file
  secrets (`NAME_FILE`), PostgreSQL with embedded migrations, Telegram long
  polling that records people and chats in `core`, `Preflight`, `/healthz`,
  `/metrics` on its own listener, the family's panels on `/start`, the hourly
  model catalog sync, the price estimate, the currency rates, the job queue
  on River, the free allowance, the encrypted store of people's keys, the
  OpenRouter, OpenAI and Gemini image adapters, storage in R2 with the ffmpeg
  photo pipeline, the renderer of the album preview, the generating loop and
  the error card (`internal/anim`), the public HTTP server's foundation
  (`internal/web`: security headers, CSRF, cache rules, rate limits), and a
  clean stop on SIGTERM. Nothing reaches people yet: no surface starts a job.
- The service will be one Go binary (module `github.com/FreshLabDev/framy`):
  the HTTP server (site, Mini App, API, SSE, OAuth callbacks, provider
  webhooks), the bot, the job workers, the hourly catalog sync and the
  renderer of loops and cards, all from one process behind a Cloudflare Tunnel
  at `framy.ws04.cc`.
- PostgreSQL holds all state. Domain tables live in a `framy` schema on the
  shared `core-postgres`, reached as `framy_core`. Telegram identity, presence
  and language are delegated to `core` through `core.touch` and
  `core.effective_language`, never by writing `core.*` tables directly.
- Files, inputs and results alike, live in the private R2 bucket `framy` and
  are served through presigned links. The host keeps only working files.
- Telegram goes through `github.com/FreshLabDev/tg` against the family's own
  `telegram-bot-api` in `--local` mode. What `tg` lacks is listed in the
  design under "Changes to tg" and tracked as `tg` issues; call it through
  `Client.Call` until typed methods land. A `tg` release needs separate
  permission. Long polling only.
- `Preflight` runs before the bot starts and names the methods framy cannot
  work without.

## One Of The Family

framy is a sibling of vido, searchy, quoto, branchy, makeitMD, voicy and maply,
and a person who knows one of them should recognise framy. Before adding a
screen, a command or a table, check how the siblings do it.

- **One database.** framy's tables live in the `framy` schema of the shared
  `core-postgres`, created by core migration `019_framy.sql` and reached as
  `framy_core`. Identity, presence and language are core's: call `core.touch`
  for every update with a sender, read the language with
  `core.effective_language`, write it with `core.set_language` and
  `core.clear_language`. framy holds `REFERENCES` on `core.person` and
  `core.chat` and no `SELECT`: it learns who a person is from the update or
  the Telegram Login claims. A new grant is a core migration that revokes
  `PUBLIC` in the same file.
- **Panels.** Every screen follows `core/docs/panels.md` (R1 to R9): title,
  hint and quote from one helper; "Back" to go up; Close only in a group, as
  Danger; at most one Primary; Success only on the chosen option; `◉`/`◎` and
  `■`/`□` marks; the sixteen languages in the family's order; no emoji; the
  About card exactly as written there; Follow Telegram on the language screen.
  Callbacks are `m:<owner>:<action>`, and only the owner's press acts.
- **Groups.** `/start` there is an ephemeral command and the panel an
  ephemeral reply; an ordinary `/start` in a group stays unanswered, and only
  an ephemeral panel is addressable by a callback. Admin rights are asked of
  Telegram on every use, never cached.
- **Profile.** The command menu, the description and the short description
  are published per language at start, a description only when it changed.
  The direct chat's menu has `/start` only; the other tabs answer when typed.
- **Version and metrics.** The About card shows the version from the build's
  ldflags, never a commit or a date. Counters are `framy_*_total` in the
  Prometheus text format on `METRICS_ADDR`, never on the public listener.
- **Writing.** No em dash anywhere a person reads; the i18n tests check the
  translations and the strings in Go code.

## Money And Keys

- A run is paid either by the free allowance or by one of the person's own
  connections, and the person sees which before it starts. Never run one
  person's job on another person's key, and never fall back from a person's
  key to framy's.
- framy adds nothing to the provider's price. The estimate before a run and the
  cost after it come from the provider's pricing and its reported usage. Never
  round in framy's favour, and always label an estimate as one.
- The free allowance **(owner)**: 5 images in any 24 hours and 1 video in any
  7 days per Telegram user id, across every surface, on the models the design
  names. The video can instead be an edit of a clip up to 5 s on FLUX Video
  Edit. It is counted from `job`, not stored separately, and a global daily
  budget ($1, `FREE_DAILY_BUDGET_USD`) and the limit on framy's OpenRouter key
  close it. `internal/free` checks it inside the transaction that creates or
  queues a free job. Changing the counts, the windows or the models is the
  owner's call.
- People's keys are encrypted at rest with AES-GCM under a KEK read from a file.
  Only the last four characters are ever shown. A key never appears in a log
  line, an error, a metric label, a job row or a message.
- A message in a DM that looks like a key is deleted at once and nothing from
  it is saved.
- An adapter gets its key from `keys.Service.JobKey` and runs behind
  `Service.Track`, which writes what a run showed about the key as the
  connection's status. An adapter's error can say more than its class by
  having `KeyStatus() string` (`invalid`, `needs_billing`,
  `needs_verification`, `no_credits`); without it only a Money error moves a
  status, to `no_credits`.

## Product Boundaries

- One account per Telegram user id: `from.id` in an update,
  `initData.user.id` in the Mini App, the `id` claim of Telegram Login on the
  site.
- `/f` in a group: without a reply it makes an image from scratch; in reply to
  a photo it edits that photo; in reply to a photo of an album it takes every
  photo of the album with one instruction. Allowed on anyone's photo
  **(owner)**. Nothing is generated until the requester presses Generate, and
  only the requester can press the buttons of a preview.
- The catalog is data: synced from OpenRouter every hour, plus hand-written
  descriptors for OpenAI and Gemini in `catalog/`, joined and annotated by
  `catalog/overlay.yaml`. A new model must not need a code change. A model's
  id is its OpenRouter id, every hand-written offer is listed under a model in
  the overlay, and catalog rows are disabled, never deleted.
- Interface text lives in `i18n`, never inline. framy speaks the family's
  sixteen languages, and they ship together; a key missing in one language is
  a bug.
- Bot names are written in lowercase everywhere, including headings and
  images.

## Site Look

- The site and the Mini App follow the Asterfield design system **(owner)**:
  one field colour and white, DIN Condensed for figures and DIN Alternate for
  labels with Sofia Sans standing in on the web, stripes and gaps in the 7 : 4
  proportion, square corners, no shadows, gradients or rounded panels. The
  owner wants it original, not the usual generator layout of a chat box over a
  grid of pictures.
- Plain HTML, CSS and JavaScript without a bundler. Fonts and libraries are
  served by the service itself; only `telegram-web-app.js` comes from outside.

## Data And Security

- Every route of the public listener is registered on the `web.Server`
  (`site.Handle`), never on a bare mux: it gives the route the CSP, the cache
  rules, the CSRF check and the rate limits. A new class of route is a rate
  class in `internal/web/ratelimit.go`; a route that must take a cross-site
  POST is a deliberate entry in `csrfBypass`. `CF-Connecting-IP` is believed
  only from `TRUSTED_PROXIES`.
- Never log the bot token, provider keys, the KEK, R2 credentials, OAuth codes
  and verifiers, a database URL with its password, or full Bot API URLs.
- Connect with `search_path=framy` and call `core.touch` before domain writes
  that reference `core.person`.
- Secrets live in `.env` and in file secrets, both outside git.
- Outgoing requests to URLs that a provider or a person gave go through the
  dialer that refuses private addresses.
- Results are private by default. A presigned link goes only to the owner of a
  result and lives minutes, not days.
- `callback_data` and the `startapp` payload are a contract with messages
  already sent. Do not change either without keeping the old form working.

## Code Style

- Prefer small packages and simple interfaces.
- Use the standard library unless a dependency materially reduces complexity.
- Keep SQL explicit and close to the store method that uses it.
- Avoid broad refactors while changing behavior.
- Add comments only when they explain non-obvious money, key handling,
  concurrency, or protocol behavior.

## Migrations

- Add new SQL migrations under `migrations/` with the next numeric prefix.
- Migrations must be safe to run once and tracked by `schema_migrations`.
- Do not edit a migration after it may have been applied.
- Grants for `framy_core` belong to the `core` repository, in the same
  migration that revokes `PUBLIC`.

## Versioning

- Work on `dev`. Pre-releases (`-alpha.N`, `-beta.N`, `-rc.N`) are tagged on
  `dev`; stable versions are tagged on `main`, on the merge commit from `dev`.
  The test bot runs `dev`, the production bot runs `main`.
- Follow `docs/versioning.md`. The first line starts at `v0.1.0-alpha.1`.
- Treat the surface listed in `docs/versioning.md` under "Breaking-sensitive
  surface" as breaking before `v1.0.0` too.

## Changelog And Releases

- Keep notable changes under `## Unreleased` in `CHANGELOG.md`, in the same
  work that makes them. Exactly one `## Unreleased`, always at the top.
- Follow `docs/releases.md` for changelog sections and the release workflow.
- Mark `alpha`, `beta`, and `rc` GitHub Releases as pre-releases.
- Do not tag or publish a release without explicit permission, even when the
  work is done and everything builds.

## Planning

- Tasks and their status live in GitHub issues on the
  [framy project board](https://github.com/orgs/FreshLabDev/projects/3), not in
  files. The board's Stage field follows the stages of the design, and Area
  says which part of the service a task touches. Repository labels stay the
  GitHub defaults, as in every family repository. When the plan changes,
  change the issues in the same step.
- A new issue says what to do in its title, and its body ends with
  "Done when …": the check that closes it.
- Everything public is written in English: issues, the board, docs, code
  comments, commit messages. The research notes in `docs/research/` are the
  exception, kept in Russian as they were written. The interface is in the
  family's sixteen languages.

## Working In This Checkout

- Other agents may work in the same checkout. Stage files by name; never
  `git add -A` or `git add .`.
- Do not commit or push unless asked.
- Commit messages and pull request descriptions carry only a
  `Co-Authored-By` line for attribution. No session links.

## Verification

```sh
gofmt -l .
go vet ./...
go test -race ./...
```

The database tests need a real PostgreSQL and a disposable database whose name
contains `test`; they refuse to run anywhere else:

```sh
FRAMY_TEST_DATABASE_URL=postgres://framy:framy@localhost:5432/framy_test?sslmode=disable \
  go test -tags=integration -p 1 ./...
```

The media tests need FFmpeg 8.0 or newer and skip without it; CI runs them
inside the image with `FRAMY_MEDIA_TESTS=1`, which makes a skip a failure.
`internal/media/testdata/make.swift` regenerates the iPhone fixtures on macOS.

The live R2 check runs by hand against the real buckets, where the token is:
`go test -tags r2live -run Live ./internal/r2` with `R2_ACCOUNT_ID`,
`R2_ACCESS_KEY_ID_FILE` and `R2_SECRET_ACCESS_KEY_FILE`. It writes a few bytes
under `incoming/` and removes them. Set `R2_FOREIGN_BUCKET` to a throwaway
bucket of the same account to check the token is refused there. It prints no
credential and no presigned URL; keep it that way.

Verify the site and the Mini App in a browser at phone width (375×812 and
320×568) and on a wide screen, with and without the Telegram emulation hash
(`#tgWebAppData=…&tgWebAppVersion=9.0&tgWebAppPlatform=ios`), and check the
console has no errors.

## License

Apache-2.0. Preserve `LICENSE` and `NOTICE`. Every `.go`, `.sql`, YAML file,
`go.mod` and the `Dockerfile` starts with `SPDX-License-Identifier: Apache-2.0`.

## Deploying

Do not invent a deploy. [`docs/releases.md`](docs/releases.md) has a
**Deploying** section describing this stack: which host directory it lives in,
which env file names the image, which networks it needs, and how to roll back.
Read it before touching anything on the host. Deploy only with explicit
permission.

Two rules that hold everywhere and are easy to get wrong:

- **Nothing is built on the host.** A production stack pulls the image the
  release workflow published. A `build:` section in a production manifest is a
  bug.
- **Pin the digest, not the tag.** A tag moves; a digest names one build that
  was tested, and a rollback becomes one line with nothing to rebuild.
