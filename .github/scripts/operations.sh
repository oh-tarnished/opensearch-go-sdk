#!/usr/bin/env bash
# Prints the generated client's operations, one Go method name per line,
# sorted, for comparing two specification versions.
set -euo pipefail

grep -oE '^func \(c \*Client\) [A-Z][A-Za-z0-9]*\(' opensearchapi/client.gen.go \
  | sed -E 's/^func \(c \*Client\) ([A-Za-z0-9]+)\($/\1/' \
  | grep -v 'WithBody$' \
  | sort -u
