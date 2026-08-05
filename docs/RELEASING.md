# Releasing

Versioning is semantic (`vX.Y.Z` git tags) with a human-decided bump:
breaking changes bump major, features minor, fixes patch. The tag is the
single source of truth — no version constants to edit.

## How a release happens

1. Make sure `main` is green.
2. Tag it and push the tag:

   ```sh
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

3. The `Release` workflow runs GoReleaser: cross-platform archives
   (darwin/linux/windows × amd64/arm64), `checksums.txt`, and a GitHub
   release with notes auto-generated from merged PR titles — keep those
   titles descriptive, they are the changelog.

## How the binary knows its version

- Release binaries: GoReleaser stamps `internal/version` via ldflags.
- `go install …@vX.Y.Z` and source builds: Go itself embeds the module
  version from VCS state (tag, or pseudo-version/`+dirty`), which
  `internal/version` reads as the fallback.
- `scripts/install.sh` stamps `git describe --tags --dirty`, so source
  installs between tags read like `v1.0.0-3-gabc1234`.

`session-protect version` prints it; `--verbose` adds commit and date.

## Distribution

- **get.rexov.as** serves `scripts/get.sh` (a Cloudflare redirect to the
  raw file on `main`); the script resolves the latest release itself, so
  it needs no per-release maintenance.
- **Homebrew**: GoReleaser pushes the formula to `rexovas/homebrew-tap`
  on every release. The `HOMEBREW_TAP_TOKEN` repo secret (a fine-grained
  PAT with contents-write on the tap) must exist before the first
  release.

## Notes

- Staying within v1.x long-term is deliberate: Go modules require a
  `/v2` module-path change for major ≥2, so features land as minors.
- Pre-releases: tag `vX.Y.Z-rc.1` — GoReleaser marks them pre-release
  automatically.
