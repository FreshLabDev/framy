<h1 align="center">framy</h1>

<p align="center"><strong>Images and videos from OpenRouter, OpenAI and Google models, at the provider's price.</strong><br/>A website, a Telegram bot and a Mini App, built as one Go service.<br/><a href="https://framy.ws04.cc">framy.ws04.cc</a></p>

<p align="center">
  <a href="https://github.com/FreshLabDev/framy/releases"><img src="https://img.shields.io/github/v/release/FreshLabDev/framy?include_prereleases&sort=semver&label=release&color=00606E&style=for-the-badge&labelColor=0f172a" alt="release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-334155?style=for-the-badge&labelColor=0f172a" alt="license"></a>
  <a href="https://t.me/framyin_bot"><img src="https://img.shields.io/badge/telegram-%40framyin__bot-26A5E4?style=for-the-badge&logo=telegram&logoColor=white&labelColor=0f172a" alt="telegram bot"></a>
  <a href="https://github.com/orgs/FreshLabDev/projects/4/views/3"><img src="https://img.shields.io/badge/plan-project%20board-00606E?style=for-the-badge&logo=github&logoColor=white&labelColor=0f172a" alt="project board"></a>
</p>

<p align="center">
  <a href="#status">Status</a> ·
  <a href="#where-it-works">Where It Works</a> ·
  <a href="#free-and-your-keys">Free And Your Keys</a> ·
  <a href="#how-it-works">How It Works</a> ·
  <a href="#run-it-locally">Run It Locally</a> ·
  <a href="#docs">Docs</a>
</p>

---

## The Problem

New image and video models come out every month, each behind its own site,
its own credits and its own price list. Paying for three subscriptions to
compare two models is silly, and the chat where the photo is never has the
model.

framy is deliberately narrow:

| Need | framy approach |
|:--|:--|
| One place to make an image or a video | One prompt line and every model your keys unlock |
| Changing a video you already have | Attach it like a photo, uploaded or made in framy; the editing models are in the same list |
| An honest price | The provider's price before every run, in USD and in your currency; the actual cost after |
| Telegram as a first-class place | `/img` and `/edit` in any group, whole albums as input, a preview to confirm, the result in place |
| Models that change every month | The catalog is data synced from OpenRouter every hour, not code |
| Not another credit reseller | A small free allowance; everything else runs on your own keys with nothing added |
| Familiar stack | Go, the family's `tg` client, shared Asterfield `core`, one service |

---

## Status

framy is in alpha and in use: the site at
[framy.ws04.cc](https://framy.ws04.cc) and
[@framyin_bot](https://t.me/framyin_bot) run the newest pre-release, the one
on the badge above and on
[Releases](https://github.com/FreshLabDev/framy/releases). There is no stable
version yet.

What it does today:

- images and videos on the models of OpenRouter, OpenAI and Google, with the
  price before each run and the cost the provider reported after it;
- on the site and in the Mini App, several results from one press, each with
  its own model, clips that go on with Continue, a library of everything made,
  and the settings with your keys and default models;
- in Telegram, `/img` and `/edit` in groups with whole albums as input, a plain
  message in the DM, past results in any chat by `@framyin_bot` (inline mode),
  and `@framyin_bot` in reply to a photo where the bot is not a member (guest
  mode);
- the free allowance, and OpenRouter, OpenAI and Gemini on your own keys.

Still to come: the editor of a result, a mask for OpenAI edits and public links
to results. Work is tracked on the
[project board](https://github.com/orgs/FreshLabDev/projects/4/views/3).

---

## Where It Works

| Surface | What it does |
|:--|:--|
| Site | Composer, model picker with prices, history, settings, provider connections. The main interface |
| Mini App | The same frontend, opened from the bot |
| Group | `/img a prompt`: an image from scratch, or an edit when it replies to a photo or an album. `/edit a prompt` in reply to a video |
| DM | A plain message, then a choice of image or video; settings and results made on the site |
| Inline mode | `@framyin_bot` in any chat lists your past results, to send one again |
| Guest mode | `@framyin_bot a prompt` in reply to a photo, in a chat where framy is not a member |

In a group, framy first shows which photos it took and asks "Generate?". Only
after Yes does the message turn into a progress loop and then into the result.

---

## Free And Your Keys

- **Free:** 5 images in any 24 hours and 1 video in any 7 days, on cheap models
  that still edit well, paid by framy. The video can instead be an edit of a
  clip up to 5 seconds.
- **Your keys:** connect OpenRouter with one button, or paste an OpenAI or a
  Gemini key. Every run then goes to the provider at its own price, and framy
  adds nothing.
- Keys are encrypted at rest and never shown again past their last four
  characters.

---

## How It Works

framy is one Go service on the family's home server. PostgreSQL holds the
state in a `framy` schema of the shared `core-postgres`; files live in
Cloudflare R2. Telegram goes through
[`FreshLabDev/tg`](https://github.com/FreshLabDev/tg) and the family's own Bot
API server.

```text
http server -> site and Mini App, API, progress over SSE, OAuth, webhooks
bot         -> /img and /edit in groups and DMs, album cache, delivery
workers     -> jobs in Postgres, OpenRouter / OpenAI / Gemini adapters
catalog     -> hourly OpenRouter sync, descriptors, daily currency rates
```

---

## Run It Locally

Docker and a test bot of your own from [@BotFather](https://t.me/BotFather)
are enough to start:

```sh
cp .env.example .env
mkdir -p secrets    # then put the test bot's token in secrets/bot-token
docker compose up --build
```

The bot answers `/start`, the catalog syncs from OpenRouter, and the site
opens on http://localhost:8080. Making a picture needs an R2 bucket and an
OpenRouter key; `.env.example` says what each setting is for and what stays
off without it. `go test ./...` runs the tests; [AGENTS.md](AGENTS.md#verification) has
the database, media and browser checks.

---

## Docs

| Document | Purpose |
|:--|:--|
| [framy.ws04.cc](https://framy.ws04.cc) | The site itself |
| [Design](docs/design.md) | The product: surfaces, accounts, keys, catalog, prices, the free allowance, jobs, `/img` and `/edit`, storage, security |
| [Research](docs/research/README.md) | Models, providers, Telegram and the build, with sources (in Russian) |
| [Versioning](docs/versioning.md) | Pre-release and stable version line |
| [Release process](docs/releases.md) | Changelog, GitHub Release, deploying |
| [Changelog](CHANGELOG.md) | What each release changed |
| [Security](SECURITY.md) | How to report a vulnerability privately |
| [Project board](https://github.com/orgs/FreshLabDev/projects/4/views/3) | Every task and its status, on the Asterfield board's framy tab |

---

<p align="center">
  <a href="https://github.com/orgs/FreshLabDev/projects/4/views/3">Project board</a> ·
  <a href="CHANGELOG.md">Changelog</a> ·
  <a href="LICENSE">Apache-2.0</a> ·
  <a href="NOTICE">NOTICE</a>
</p>

<p align="center">
  framy is open source software by Asterfield.<br/>
  Copyright 2026 Asterfield.
</p>
