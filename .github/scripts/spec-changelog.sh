#!/usr/bin/env bash
# Adds a "Changed" entry under "## [Unreleased]" in CHANGELOG.md for a
# specification update: the old and new versions and the operations added and
# removed (lists from operations.sh before and after regenerating).
#
# usage: spec-changelog.sh OLD NEW BEFORE AFTER
set -euo pipefail

old="$1" new="$2" before="$3" after="$4"
added="$(comm -13 "${before}" "${after}")"
removed="$(comm -23 "${before}" "${after}")"

list() {
  if [[ -z "$1" ]]; then
    echo "none"
  else
    paste -sd, - <<<"$1" | sed 's/,/, /g; s/\([A-Za-z0-9]*\)/`\1`/g; s/``//g'
  fi
}

entry="$(cat <<EOF
- OpenSearch API Specification ${old} → ${new}, \`opensearchapi\` regenerated.
  Operations added: $(list "${added}").
  Operations removed: $(list "${removed}").
EOF
)"

ENTRY="${entry}" awk '
  BEGIN { entry = ENVIRON["ENTRY"] }
  { print }
  /^## \[Unreleased\]/ && !done {
    print ""
    print "### Changed"
    print ""
    print entry
    done = 1
  }
' CHANGELOG.md > CHANGELOG.md.new
mv CHANGELOG.md.new CHANGELOG.md
