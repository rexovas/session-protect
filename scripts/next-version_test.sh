#!/usr/bin/env bash
# Tests for next-version.sh. Plain bash, no framework: run with
#   bash scripts/next-version_test.sh
set -uo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/next-version.sh"
failures=0
count=0

# expect <name> <tag> <levels> <want-output>
expect() {
  local name=$1 tag=$2 levels=$3 want=$4 got status
  count=$((count + 1))
  got=$(printf '%b' "$levels" | bash "$script" "$tag" 2>/dev/null)
  status=$?
  if [ "$status" -ne 0 ] || [ "$got" != "$want" ]; then
    printf 'FAIL %s: got %q (exit %d), want %q\n' "$name" "$got" "$status" "$want"
    failures=$((failures + 1))
  fi
}

# expect_error <name> <tag> <levels>
expect_error() {
  local name=$1 tag=$2 levels=$3 got status
  count=$((count + 1))
  got=$(printf '%b' "$levels" | bash "$script" "$tag" 2>/dev/null)
  status=$?
  if [ "$status" -eq 0 ]; then
    printf 'FAIL %s: want non-zero exit, got 0 with %q\n' "$name" "$got"
    failures=$((failures + 1))
  elif [ -n "$got" ]; then
    printf 'FAIL %s: want no output on error, got %q\n' "$name" "$got"
    failures=$((failures + 1))
  fi
}

# Each level alone.
expect "patch" v1.2.3 'patch\n' v1.2.4
expect "minor" v1.2.3 'minor\n' v1.3.0
expect "major" v1.2.3 'major\n' v2.0.0
expect "none" v1.2.3 'none\n' ""

# Mixes: the highest wins, order does not matter.
expect "patch+minor" v1.2.3 'patch\nminor\npatch\n' v1.3.0
expect "minor+patch" v1.2.3 'minor\npatch\n' v1.3.0
expect "all four" v1.2.3 'none\npatch\nmajor\nminor\n' v2.0.0
expect "none+patch" v1.2.3 'none\npatch\nnone\n' v1.2.4
expect "repeated patch" v1.2.3 'patch\npatch\npatch\n' v1.2.4

# Nothing contributes.
expect "none only" v1.2.3 'none\nnone\n' ""
expect "empty input" v1.2.3 '' ""
expect "blank lines" v1.2.3 '\n\n' ""
expect "no trailing newline" v1.2.3 'minor' v1.3.0

# 0.x and carries.
expect "0.x major" v0.2.3 'major\n' v1.0.0
expect "0.x minor" v0.2.3 'minor\n' v0.3.0
expect "0.0.0 patch" v0.0.0 'patch\n' v0.0.1
expect "multi-digit" v10.99.199 'patch\n' v10.99.200
expect "multi-digit minor" v10.99.199 'minor\n' v10.100.0

# Malformed tags.
expect_error "no v" 1.2.3 'patch\n'
expect_error "two parts" v1.2 'patch\n'
expect_error "four parts" v1.2.3.4 'patch\n'
expect_error "leading zero major" v01.2.3 'patch\n'
expect_error "leading zero minor" v1.02.3 'patch\n'
expect_error "leading zero patch" v1.2.03 'patch\n'
expect_error "pre-release" v1.2.3-rc.1 'patch\n'
expect_error "build metadata" v1.2.3+abc 'patch\n'
expect_error "capital V" V1.2.3 'patch\n'
expect_error "empty tag" "" 'patch\n'
expect_error "letters" vx.y.z 'patch\n'
expect_error "trailing space" "v1.2.3 " 'patch\n'

# Malformed levels.
expect_error "unknown level" v1.2.3 'feature\n'
expect_error "unknown among valid" v1.2.3 'patch\nbogus\n'
expect_error "label form" v1.2.3 'semver:patch\n'

# Usage.
count=$((count + 1))
if bash "$script" </dev/null >/dev/null 2>&1; then
  echo "FAIL no argument: want non-zero exit"
  failures=$((failures + 1))
fi
count=$((count + 1))
if bash "$script" v1.2.3 v1.2.4 </dev/null >/dev/null 2>&1; then
  echo "FAIL two arguments: want non-zero exit"
  failures=$((failures + 1))
fi

if [ "$failures" -ne 0 ]; then
  printf '%d of %d cases failed\n' "$failures" "$count"
  exit 1
fi
printf 'next-version: %d cases passed\n' "$count"
