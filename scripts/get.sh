#!/bin/sh
# SessionProtect installer — served at https://get.rexov.as/session-protect
#
#   curl -fsSL https://get.rexov.as/session-protect | sh
#
# Downloads the latest release for this platform, verifies it against the
# release checksums, and installs to ~/.local/bin (override with
# SESSION_PROTECT_BIN_DIR). macOS and Linux; Windows users: grab the zip
# from the releases page.
set -eu

REPO="rexovas/session-protect"
BIN_DIR="${SESSION_PROTECT_BIN_DIR:-$HOME/.local/bin}"

fail() {
  echo "get.rexov.as: $1" >&2
  exit 1
}

os=$(uname -s)
case "$os" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) fail "unsupported OS: $os (see https://github.com/$REPO/releases)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  arm64 | aarch64) arch="arm64" ;;
  *) fail "unsupported architecture: $arch" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
  sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
[ -n "$tag" ] || fail "could not determine the latest release"

asset="session-protect_${tag#v}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading session-protect $tag ($os/$arch)…"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "download failed: $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "checksums download failed"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
else
  fail "need sha256sum or shasum to verify the download"
fi
expected=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || fail "no checksum published for $asset"
[ "$actual" = "$expected" ] || fail "checksum mismatch for $asset — aborting"

tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/session-protect" ] || fail "archive did not contain the binary"

mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/session-protect" "$BIN_DIR/session-protect"
ln -sf "$BIN_DIR/session-protect" "$BIN_DIR/sp"

echo "Installed $BIN_DIR/session-protect ($tag) with sp alias."
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "Note: $BIN_DIR is not on your PATH — add it to your shell profile." ;;
esac
echo "Get started:  sp backup && sp hook install && sp browse"
