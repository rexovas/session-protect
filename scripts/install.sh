#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/install.sh [--prefix PATH] [--no-alias] [--no-post-install-check]

Builds and installs session-protect from this source checkout.

Defaults:
  prefix: ~/.local
  binary: <prefix>/bin/session-protect
  alias:  <prefix>/bin/sp
EOF
}

prefix="${SESSION_PROTECT_PREFIX:-$HOME/.local}"
install_alias=1
post_install_check=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)
      prefix="${2:?missing value for --prefix}"
      shift 2
      ;;
    --no-alias)
      install_alias=0
      shift
      ;;
    --no-post-install-check)
      post_install_check=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$prefix" != /* ]]; then
  mkdir -p "$(dirname "$prefix")"
  prefix="$(cd "$(dirname "$prefix")" && pwd)/$(basename "$prefix")"
fi

source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin_dir="$prefix/bin"
share_dir="$prefix/share/session-protect"
binary="$bin_dir/session-protect"
alias_binary="$bin_dir/sp"

export GOCACHE="${GOCACHE:-$source_dir/.cache/go-build}"
export GOMODCACHE="${GOMODCACHE:-$source_dir/.cache/gomod}"
mkdir -p "$GOCACHE" "$GOMODCACHE"

commit="$(git -C "$source_dir" rev-parse --short HEAD 2>/dev/null || echo unknown)"
date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
# Version comes from the nearest release tag; builds past a tag or with
# local edits carry a -N-g<sha>/-dirty suffix distinguishing them from
# the released version.
# The private development repository carries public-vX.Y.Z boundary tags
# rather than vX.Y.Z; report both the same way.
described="$(git -C "$source_dir" describe --tags --dirty 2>/dev/null || echo dev)"
version="${SESSION_PROTECT_VERSION:-${described#public-}}"

ldflags=(
  "-X github.com/rexovas/session-protect/internal/version.Version=$version"
  "-X github.com/rexovas/session-protect/internal/version.Commit=$commit"
  "-X github.com/rexovas/session-protect/internal/version.Date=$date"
  "-X github.com/rexovas/session-protect/internal/version.Channel=source"
  "-X github.com/rexovas/session-protect/internal/version.SourceDir=$source_dir"
  "-X github.com/rexovas/session-protect/internal/version.InstallPrefix=$prefix"
)

mkdir -p "$bin_dir" "$share_dir"
tmp="$(mktemp "${TMPDIR:-/tmp}/session-protect.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

echo "Building session-protect from $source_dir"
# Build from the checkout: `session-protect update` runs this script from
# wherever the user happens to be.
(cd "$source_dir" && go build -ldflags="${ldflags[*]}" -o "$tmp" ./cmd/session-protect)

install -m 0755 "$tmp" "$binary"
if [[ "$install_alias" -eq 1 ]]; then
  ln -sfn "$binary" "$alias_binary"
fi

{
  echo "channel=source"
  echo "source_dir=$source_dir"
  echo "install_prefix=$prefix"
  echo "installed_at=$date"
  echo "commit=$commit"
  echo "version=$version"
} > "$share_dir/install.env"

# launchd refuses to start a LaunchAgent whose binary changed since it was
# loaded, so a replaced binary silently stops the daily backup. Re-register
# the agent if one is installed (this also starts a backup).
if [[ "$(uname -s)" == "Darwin" && -f "$HOME/Library/LaunchAgents/com.session-protect.backup.plist" ]]; then
  "$binary" schedule reload || echo "warning: could not reload the daily backup schedule; run: session-protect schedule reload" >&2
fi

echo "Installed $binary"
if [[ "$install_alias" -eq 1 ]]; then
  echo "Installed alias $alias_binary"
fi

if [[ "$post_install_check" -eq 1 ]]; then
  "$binary" version
  "$binary" update --check
fi
