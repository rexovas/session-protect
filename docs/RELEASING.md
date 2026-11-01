# Releasing

Versioning is semantic (`vX.Y.Z` git tags). Releases are cut automatically
when pull requests merge to `main` in the public repository: each pull
request states its release level with a label, and the release workflow
takes the highest level among everything merged since the latest release and
publishes the next version. The tag is the single source of truth — no
version constants to edit. Do not push release tags by hand.

## Labelling a pull request

Every pull request, whoever opens it, carries exactly one of four labels:

| Label | Use it for | Release |
|-------|------------|---------|
| `semver:patch` | fixes, toolchain and dependency security bumps | `vX.Y.Z` → `vX.Y.(Z+1)` |
| `semver:minor` | new behaviour, backward compatible | `vX.Y.Z` → `vX.(Y+1).0` |
| `semver:major` | a breaking change | `vX.Y.Z` → `v(X+1).0.0` |
| `semver:none` | docs, CI, tests, housekeeping | no release |

A `semver:major` pull request must also say what breaks, on a line in its
description starting `Breaking:`. Staying within v1.x is deliberate (see
Notes), so this should stay rare.

The **semver label** check (`.github/workflows/semver-label.yml`) fails until
the pull request has exactly one `semver:*` label, listing the four and what
each means. Adding the label or editing the description re-runs it.

Dependabot labels its GitHub Actions bumps `semver:none`
(`.github/dependabot.yml`): they change CI, not the binary. Relabel one by
hand if an update does change what ships.

## How a release happens

On every push to `main`, the **plan** job of `.github/workflows/release.yml`:

1. Finds the latest release: the highest strict-semver `vX.Y.Z` tag that has
   a published GitHub release.
2. Lists the first-parent commits on `main` since that tag. Squash merges end
   their subject with the pull request number, `(#N)`.
3. Reads each pull request's `semver:*` label. A pull request merged without
   one, or a commit that names no pull request, counts as `patch`, with a
   warning in the run.
4. Passes the levels to `scripts/next-version.sh`, a pure function whose cases
   run in CI. The highest level wins; `none` contributes nothing.

| Merged since `v1.3.0` | Next release |
|-----------------------|--------------|
| `none` only, or nothing | none |
| `patch`, `none` | `v1.3.1` |
| `patch`, `minor` | `v1.4.0` |

When nothing contributes, the run ends green with the notice "No release:
nothing since vX.Y.Z changes the shipped binary"; the next merge that does
contribute picks up everything merged in between.

When the plan yields a tag, the **release** job re-runs `go vet` and the race
tests on the merged commit, tags it locally and runs GoReleaser:
cross-platform archives (darwin/linux/windows × amd64/arm64), `checksums.txt`,
the GitHub release and the Homebrew formula. GitHub creates the tag together
with the release, on the merged commit (`release.target_commitish`). The
notes are GitHub's generated list of merged pull request titles — keep those
titles descriptive, they are the changelog.

Runs go one at a time and are never cancelled midway, so each sees the
release the previous one created. A run that fails before publishing leaves
no tag behind; the next push to `main` retries. The workflow never moves or
deletes a tag: if the computed tag already exists, the run fails and the
conflict is resolved by hand.

Everything happens in one run, because a tag created with the workflow token
triggers no other workflow. The private development repository carries the
same workflow files; every job is gated to `rexovas/session-protect`, so
there they do nothing.

## How the binary knows its version

- Release binaries: GoReleaser stamps `internal/version` via ldflags.
- `go install …@vX.Y.Z` and source builds: Go itself embeds the module
  version from VCS state (tag, or pseudo-version/`+dirty`), which
  `internal/version` reads as the fallback.
- `scripts/install.sh` stamps `git describe --tags --dirty`, so source
  installs between tags read like `v1.0.0-3-gabc1234`.

`session-protect version` prints it; `--verbose` adds commit and date.

## Distribution

- **get.rexov.as/session-protect** serves `scripts/get.sh` from the
  Rexovas artifact CDN. The script resolves the latest release itself,
  so it only needs re-uploading when the script changes (maintainers:
  `aws s3 cp scripts/get.sh s3://rexovas-artifacts-public/session-protect
  --content-type text/x-shellscript`).
- **Homebrew**: GoReleaser pushes the formula to `rexovas/homebrew-tap`
  on every release, authenticating with a short-lived token minted from
  the org's GitHub App (contents-write, installed on the tap). The
  `OSS_APP_CLIENT_ID` and `OSS_APP_PRIVATE_KEY` repo secrets must exist
  before the first release.

## Notes

- Staying within v1.x long-term is deliberate: Go modules require a
  `/v2` module-path change for major ≥2, so features land as minors.
- Pre-releases are not cut by the workflow, which computes only `vX.Y.Z`.
  Should one be needed, a `vX.Y.Z-rc.N` tag can be released by hand with
  GoReleaser; the plan ignores it when counting from the latest release.
