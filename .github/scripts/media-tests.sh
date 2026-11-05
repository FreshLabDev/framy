#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Runs the media tests inside an image: photos are normalized, clips encoded
# and the loops drawn by the ffmpeg framy ships, not the runner's, so the
# tests run there and must not skip (FRAMY_MEDIA_TESTS=1).
#
#   .github/scripts/media-tests.sh <image>
#
# Needs Go and FRAMY_TEST_DATABASE_URL, a PostgreSQL the container reaches on
# the host's network, for the storage tests. Run from the repository's root.
set -euo pipefail

image="${1:?usage: media-tests.sh <image>}"
: "${FRAMY_TEST_DATABASE_URL:?FRAMY_TEST_DATABASE_URL must name a test database}"

bin="${RUNNER_TEMP:-$(mktemp -d)}/media-tests"
mkdir -p "$bin"
CGO_ENABLED=0 go test -c -o "$bin/media.test" ./internal/media
CGO_ENABLED=0 go test -c -tags=integration -o "$bin/storage.test" ./internal/storage
CGO_ENABLED=0 go test -c -o "$bin/anim.test" ./internal/anim
# The API's are here for a photo brought over HTTP, which the database job,
# with no ffmpeg of the image's, skips.
CGO_ENABLED=0 go test -c -tags=integration -o "$bin/api.test" ./internal/api
for pkg in media storage anim api; do
  docker run --rm --network host --entrypoint "/t/$pkg.test" \
    -e FRAMY_MEDIA_TESTS=1 -e FRAMY_TEST_DATABASE_URL \
    -v "$bin:/t:ro" -v "$PWD:/src:ro" -w "/src/internal/$pkg" \
    "$image" -test.v -test.count=1
done
