<h1 align="center">framy</h1>

<p align="center"><strong>Images and videos from OpenRouter, OpenAI and Google models, at the provider's price.</strong><br/>A website, a Telegram bot and a Mini App, built as one Go service.</p>

<p align="center">
  <a href="#status"><img src="https://img.shields.io/badge/status-in%20development-5A2A5E?style=for-the-badge&labelColor=0f172a" alt="status"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-334155?style=for-the-badge&labelColor=0f172a" alt="license"></a>
  <a href="https://t.me/framyin_bot"><img src="https://img.shields.io/badge/telegram-%40framyin__bot-26A5E4?style=for-the-badge&logo=telegram&logoColor=white&labelColor=0f172a" alt="telegram bot"></a>
  <a href="https://github.com/orgs/FreshLabDev/projects/3"><img src="https://img.shields.io/badge/plan-project%20board-5A2A5E?style=for-the-badge&logo=github&logoColor=white&labelColor=0f172a" alt="project board"></a>
</p>

<p align="center">
  <a href="#where-it-works">Where It Works</a> ·
  <a href="#free-and-your-keys">Free And Your Keys</a> ·
  <a href="#how-it-works">How It Works</a> ·
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
| An honest price | The provider's price before every run, in USD and in your currency; the actual cost after |
| Telegram as a first-class place | `/f` in any group, whole albums as input, a preview to confirm, the result in place |
| Models that change every month | The catalog is data synced from OpenRouter every hour, not code |
| Not another credit reseller | A small free allowance; everything else runs on your own keys with nothing added |
| Familiar stack | Go, the family's `tg` client, shared Asterfield `core`, one service |

---

## Status

| Channel | Version | Meaning |
|:--|:--|:--|
| Stable | — | Nothing released yet |
| Next | `v0.1.0-alpha.1` | The service, the site, the free images and `/f` |

The design and the research behind it are here. The Go service connects to
the database as `framy_core`, polls Telegram, records people and groups in
`core`, answers `/start` with the family's panels (home, help, about, the
shared language) in sixteen languages, and keeps the model catalog in step with
OpenRouter every hour. It makes nothing yet. Work is tracked on the
[project board](https://github.com/orgs/FreshLabDev/projects/3).

---

## Where It Works

| Surface | What it does |
|:--|:--|
| Site | Composer, model picker with prices, history, settings, provider connections. The main interface |
| Mini App | The same frontend, opened from the bot |
| Group | `/f a prompt`: an image from scratch, or an edit when it replies to a photo or an album |
| DM | The same as a group, plus settings and results made on the site |

In a group, framy first shows which photos it took and asks "Generate?". Only
after Yes does the message turn into a progress loop and then into the result.

---

## Free And Your Keys

- **Free:** 5 images in any 24 hours and 1 video in any 7 days, on cheap models
  that still edit well, paid by framy.
- **Your keys:** connect OpenRouter with one button, or paste an OpenAI or a
  Gemini key. Every run then goes to the provider at its own price, and framy
  adds nothing.
- Keys are encrypted at rest and never shown again past their last four
  characters.

---

## How It Works

framy will be one Go service on the family's home server. PostgreSQL holds the
state in a `framy` schema of the shared `core-postgres`; files live in
Cloudflare R2. Telegram goes through
[`FreshLabDev/tg`](https://github.com/FreshLabDev/tg) and the family's own Bot
API server.

```text
http server -> site and Mini App, API, progress over SSE, OAuth, webhooks
bot         -> /f in groups and DMs, album cache, delivery
workers     -> jobs in Postgres, OpenRouter / OpenAI / Gemini adapters
catalog     -> hourly OpenRouter sync, descriptors, daily currency rates
```

---

## Docs

| Document | Purpose |
|:--|:--|
| [Design](docs/design.md) | The product: surfaces, accounts, keys, catalog, prices, the free allowance, jobs, `/f`, storage, security |
| [Research](docs/research/README.md) | Models, providers, Telegram and the build, with sources (in Russian) |
| [Versioning](docs/versioning.md) | Pre-release and stable version line |
| [Release process](docs/releases.md) | Changelog, GitHub Release, deploying |
| [Project board](https://github.com/orgs/FreshLabDev/projects/3) | Every task and its status |

---

<p align="center">
  <a href="https://github.com/orgs/FreshLabDev/projects/3">Project board</a> ·
  <a href="CHANGELOG.md">Changelog</a> ·
  <a href="LICENSE">Apache-2.0</a> ·
  <a href="NOTICE">NOTICE</a>
</p>

<p align="center">
  framy is open source software by Asterfield.<br/>
  Copyright 2026 Asterfield.
</p>
