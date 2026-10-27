# Release Process

Every Asterfield repository releases the same way. This document is identical in
all of them; only the verification section is specific to framy.

See [`versioning.md`](versioning.md) for what the numbers mean and why
pre-releases are tagged on `dev` and stable versions on `main`.

## The changelog is the release notes

`CHANGELOG.md` is the source of truth for history, and the release workflow reads
it directly — the GitHub Release body is the `## <tag>` section, copied verbatim.
There is no second place to write release notes, and no step where the two can
disagree.

Which means the changelog has to be written for somebody else to read:

- Put unreleased changes under `## Unreleased`, in the section that fits:
  `Added`, `Changed`, `Fixed`, `Removed`, `Security`, `Breaking`,
  `Known Limitations`.
- Record what matters to a user, an operator, or the next person deciding
  whether to upgrade. Not every refactor.
- Say what changed and why it mattered, concretely. "Fixed a bug" tells nobody
  anything.
- Call out anything an operator must act on — a new or renamed environment
  variable, a migration, a changed deployment assumption — explicitly, in its
  own entry.
- Exactly one `## Unreleased` section, always at the top. Two of them means the
  next release renames the wrong one.

## Publishing a pre-release

A pre-release is tagged on `dev`. Nothing merges anywhere.

1. Finish the work on `dev` and run the verification below.
2. Rename `## Unreleased` to the version, and open a fresh empty `## Unreleased`
   above it:

   ```text
   ## Unreleased

   ## v1.2.3-alpha.4 - 2026-09-09
   ```

3. Commit that on `dev` and push it.
4. Tag the pushed commit and push the tag:

   ```sh
   git tag -a v1.2.3-alpha.4 -m "v1.2.3-alpha.4"
   git push origin dev
   git push origin v1.2.3-alpha.4
   ```

The tag push runs `.github/workflows/release.yml`. It runs CI (`ci.yml`) on
the tagged commit first and publishes nothing unless it passed; then it
refuses the tag if it is not on `dev` or has no changelog section, builds and
publishes the image, adds its digest to the notes, and creates the GitHub
Release marked as a pre-release.

Then point the test bot at it. A pre-release nobody ran is a pre-release that
proved nothing.

## Publishing a stable release

A stable version is tagged on `main`, on the merge commit.

1. The version being promoted should already have been through at least one
   pre-release that actually ran somewhere. If it has not, say why in the
   changelog.
2. On `dev`, rename `## Unreleased` to the stable version and push.
3. Merge into `main` with a merge commit, so the tag has something to sit on:

   ```sh
   git checkout main
   git merge --no-ff dev
   git push origin main
   ```

4. Tag the merge commit and push the tag:

   ```sh
   git tag -a v1.2.3 -m "v1.2.3"
   git push origin v1.2.3
   ```

5. Deploy it, and check the running version says what it should.

## Publishing without CI

A pushed tag publishes only after CI passed on the tagged commit. When CI could
not run (an Actions outage, a spending limit) or failed for a reason that is not
the code, and the release cannot wait, publish the existing tag by hand:

```sh
gh workflow run release.yml -f tag=v1.2.3 -f reason="why CI is skipped"
```

That run skips CI and puts the reason at the top of the GitHub Release, so the
release itself says it was not checked.

## Rolling back

Do not retag and do not delete a published release. Roll back by deploying the
previous version — the images are pinned by digest, so the previous digest is
the whole rollback — and then publish a new patch that fixes what went wrong.

A version that was published is a fact about what existed. Rewriting it makes
every other record of it wrong.

## Deploying

The host is WS04. Every stack lives in `/opt/stacks/<stack>` and is driven by the
`ws04` CLI, which exists on the operator's machine and reaches the host over the
LAN. Nothing here is built on the host any more: a stack pulls the image the
release workflow published and runs that. If you find a `build:` section in a
production manifest, that is a bug, not a shortcut.

### One deploy

```sh
ws04 deploy framy --dry-run --yes    # prints what it would do, changes nothing
ws04 deploy framy --yes
```

`deploy` snapshots the stack's compose, env and image ids into
`/opt/stacks/.ws04/deploy-snapshots/framy/<timestamp>`, pulls, brings the stack
up, waits up to ninety seconds for the container to report healthy, and **rolls
back on its own** if it does not. The snapshot is kept either way.

### Pointing the stack at a version

The image is chosen by one variable in the stack's env file on the host, not by
anything in this repository:

```sh
FRAMY_IMAGE=ghcr.io/freshlabdev/framy@sha256:<digest>
```

Pin the **digest**, not the tag. A tag can be moved; a digest names one build
that was tested, so a rollback is one line with nothing to rebuild, and
`docker inspect` on the running container answers which commit it came from. The
digest of a release is in its GitHub Release notes. To read it off the host that
will run it, pull the tag once and ask the daemon:

```sh
docker pull ghcr.io/freshlabdev/framy:<tag>
docker inspect --format '{{index .RepoDigests 0}}' ghcr.io/freshlabdev/framy:<tag>
```

That pull used to be mandatory, and it is worth saying why it no longer is,
because the reason was never the digest. Deploying straight to
`ghcr.io/freshlabdev/framy@sha256:<digest>` answered `403` on a blob, and
pulling the tag first always cleared it.

The cause was the account doing the deploying. `ws04 deploy` runs as root, and
root's stored GHCR credential could not read the private package; the
pull-by-tag only worked because it was run as the login user, whose credential
can — which left the image in the shared local store for `up` to find. The
registry says which is which: asked for a manifest digest as if it were a blob,
an account that can read the package answers `404`, one that cannot answers
`403`. A `403` here is an access problem wearing a not-found costume.

So the fix is on the host, not in the recipe: make sure the account the deploy
runs as can read the package (`sudo docker login ghcr.io`), and pinning a digest
works on its own. Pulling the tag first is still a fine way to warm the layers.

Without a shell on the host, the API answers the same question:

```sh
gh api /orgs/FreshLabDev/packages/container/framy/versions \
  --jq '.[] | select(.metadata.container.tags[]? == "<tag>") | .name'
```

The variable has no default. An unset one stops the stack with a message naming
it, rather than quietly starting something else.

### Rolling back

Set `FRAMY_IMAGE` to the previous digest and deploy again. That is the whole
rollback — the images are still on the host, and nothing is rebuilt. Then publish
a patch that fixes what went wrong; never retag or delete the bad release.

### What this stack needs to exist

| | |
|:--|:--|
| Stack | `framy` — `/opt/stacks/framy` |
| Manifest | `deploy/ws04/compose.yaml` in this repository, once the stack exists |
| Env file | `.env` on the host, never in git; file secrets beside it |
| Networks | `core_net` (core-postgres), the Cloudflare Tunnel network for `framy.ws04.cc`, the network of the self-hosted `telegram-bot-api` |
| Storage | R2 buckets `framy` (inputs and results) and `framy-backups`; a working volume shared with `telegram-bot-api` |
| Database | core migration `019_framy.sql` applied, `FRAMY_CORE_PASSWORD` in core's `.env` and framy's |
| Ports | `8080` for the tunnel, never published to the host; `9090` for `/metrics`, reachable on the Docker networks only and never routed by the tunnel |
| Proxies | `TRUSTED_PROXIES` in `.env`: the subnet of the Cloudflare Tunnel network. Without it the rate limits see cloudflared's address instead of the visitor, and the log says so once |
| Outbound | HTTPS to `openrouter.ai` for the hourly catalog sync, which needs no key |

framy talks to the family's own `telegram-bot-api` in `--local` mode. The cloud
Bot API downloads files only up to 20 MB and uploads up to 50 MB; photos sent
as files and finished videos go past that.

Files live in R2, not on the host. The database is backed up into
`framy-backups`; the bucket `framy` is itself the copy off the home server.

### Checking what is running

```sh
ws04 container list                    # health of everything
ws04 logs framy --since 1h
ws04 container inspect framy           # includes the image digest
```

The service reports its own version on `/healthz`. That and `docker inspect`
should agree; if they do not, something was deployed by hand.

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

CI additionally runs `go mod verify`, `govulncheck` and the Docker build.

For `beta`, `rc`, and stable, on the test bot: a free image on the site, in the
Mini App and by `/img` on an album in a group; a run on a pasted key and one on
an OpenRouter connection; the estimate shown before each run compared with the
cost the provider reported after it.
