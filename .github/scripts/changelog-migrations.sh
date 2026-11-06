#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Fails when a migration added since the last release is not named in the
# changelog above that release's section: under ## Unreleased while the work
# goes on, and in the new version's section once it is cut. The operator
# learns from the release notes what a deploy changes in the schema the test
# bot and production share (docs/releases.md).
#
#   .github/scripts/changelog-migrations.sh [<commit>]
#
# A migration counts as named by its file name (`030_batch.sql`) or by its
# number after the word migration ("migration 030", "migrations 029 and
# 030"). It reads the commit's own files, HEAD by default, and needs the
# history and the tags.
set -euo pipefail

commit="${1:-HEAD}"
# The release before this commit: for a tagged commit, the one before it.
if ! base="$(git describe --tags --abbrev=0 --match 'v*' "${commit}^" 2>/dev/null)"; then
  echo "no release before ${commit}: nothing to compare with"
  exit 0
fi

added="$(git diff --name-only --diff-filter=A "${base}" "${commit}" -- 'migrations/*.sql')"
if [ -z "${added}" ]; then
  echo "no migration added since ${base}"
  exit 0
fi

# What the changelog says above the release's own section, on one line.
notes="$(git show "${commit}:CHANGELOG.md" | awk -v ver="## ${base}" '
  index($0, ver) == 1 && (length($0) == length(ver) || substr($0, length(ver) + 1, 1) == " ") { exit }
  { print }
' | tr '\n' ' ' | tr -s ' ')"

missing=0
for path in ${added}; do
  file="$(basename "${path}")"
  number="${file%%_*}"
  if grep -qF -- "${file}" <<<"${notes}" ||
    grep -qE -- "[Mm]igrations? ([0-9]{3}(,| and| to)? )*${number}([^0-9]|$)" <<<"${notes}"; then
    echo "${file}: named"
  else
    echo "${file} was added since ${base}, and CHANGELOG.md above ${base} does not name it"
    missing=1
  fi
done
if [ "${missing}" -ne 0 ]; then
  echo "Name each migration in its own entry, as docs/releases.md asks: the operator has to know a deploy changes the shared schema."
fi
exit "${missing}"
