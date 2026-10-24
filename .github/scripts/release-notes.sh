#!/usr/bin/env bash
# Prints the release notes for tag $1 (vX.Y.Z): the pinned specification
# version, then the CHANGELOG.md section headed "## [X.Y.Z]" (or "## [vX.Y.Z]").
# A tag without a section gets a pointer to the changelog instead, so the
# release still publishes; GitHub appends its generated pull-request list.
set -euo pipefail

tag="${1:?usage: release-notes.sh vX.Y.Z}"
version="${tag#v}"
spec="$(sed -n 's/^VERSION=//p' spec/VERSION)"

echo "Generated from the OpenSearch API Specification ${spec}."
echo

section="$(awk -v v="${version}" '
  /^## \[/ {
    if (found) exit
    if ($0 ~ "^## \\[v?" v "\\]") { found = 1; next }
  }
  found { print }
' CHANGELOG.md)"

if [[ -n "${section//[[:space:]]/}" ]]; then
  printf '%s\n' "${section}"
else
  echo "No CHANGELOG.md section for ${version}; see CHANGELOG.md."
fi
