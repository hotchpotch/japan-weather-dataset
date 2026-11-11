#!/usr/bin/env bash
# Prints the release notes of a tag (docs/release.md): the draft
# docs/releases/HEAD.md when it has content beyond its heading, otherwise
# docs/releases/<tag>.md, without the heading line.
#
#   scripts/release-notes.sh vX.Y.Z
set -eu

if [ $# -ne 1 ]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi
dir=$(cd "$(dirname "$0")/.." && pwd)/docs/releases

body() { [ -f "$1" ] && sed '1{/^# /d}' "$1" | sed '/./,$!d'; }

notes=$(body "$dir/HEAD.md" || true)
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  notes=$(body "$dir/$1.md" || true)
fi
if [ -z "$notes" ]; then
  echo "no release notes for $1 in $dir" >&2
  exit 1
fi
printf '%s\n' "$notes"
