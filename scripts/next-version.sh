#!/usr/bin/env bash
# next-version.sh — the next release tag, as a pure function.
#
# Usage: next-version.sh vX.Y.Z < levels
#
# The argument is the latest release tag, strict semver with a leading "v"
# (no leading zeros, no pre-release or build suffix). Standard input carries
# one release level per line: patch, minor, major or none. Blank lines are
# ignored. The highest level wins:
#
#   major  -> v(X+1).0.0
#   minor  -> vX.(Y+1).0
#   patch  -> vX.Y.(Z+1)
#   none   -> contributes nothing
#
# Prints the next tag, or nothing when no level contributes. Exits non-zero
# on a malformed tag or an unknown level.
set -euo pipefail

die() {
  printf 'next-version: %s\n' "$1" >&2
  exit 2
}

[ "$#" -eq 1 ] || die "usage: next-version.sh vX.Y.Z < levels"

tag=$1
num='(0|[1-9][0-9]*)'
if ! [[ $tag =~ ^v${num}\.${num}\.${num}$ ]]; then
  die "not a strict semver tag (vX.Y.Z): '$tag'"
fi
major=${BASH_REMATCH[1]}
minor=${BASH_REMATCH[2]}
patch=${BASH_REMATCH[3]}

# 0 = none, 1 = patch, 2 = minor, 3 = major
highest=0
while IFS= read -r line || [ -n "$line" ]; do
  level=${line//[[:space:]]/}
  case $level in
    "") continue ;;
    none) rank=0 ;;
    patch) rank=1 ;;
    minor) rank=2 ;;
    major) rank=3 ;;
    *) die "unknown level: '$level' (expected patch, minor, major or none)" ;;
  esac
  if [ "$rank" -gt "$highest" ]; then
    highest=$rank
  fi
done

case $highest in
  0) ;;
  1) printf 'v%s.%s.%s\n' "$major" "$minor" "$((patch + 1))" ;;
  2) printf 'v%s.%s.0\n' "$major" "$((minor + 1))" ;;
  3) printf 'v%s.0.0\n' "$((major + 1))" ;;
esac
