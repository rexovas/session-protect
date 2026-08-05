# SessionProtect

**Protect, explore, and command your AI coding-agent sessions.**

Your conversations with coding agents are work artifacts: decisions,
context, half-finished threads you'll want back. Agents treat them as
disposable — sessions get pruned, compacted, or lost when a directory
moves. SessionProtect is a single-binary CLI that backs them up safely,
lets you browse and search everything you've ever discussed, and puts
you back inside any session in one keystroke.

Supports **Claude Code** and **OpenAI Codex** side by side. Local-only:
no telemetry, no accounts, no external services.

## Highlights

**Protect**
- Git-based backup of all session state, with realtime sync at every
  turn end via agent hooks (incremental, tens of milliseconds)
- Deletion-safe by design: syncing never propagates deletions, and
  scheduled backups commit every file's final state *before* recording
  its removal — anything that ever existed is recoverable
- Optional git-crypt encryption; daily scheduled backups
- A concurrent-session guard warns when the same session is opened twice

**Explore**
- Full-screen session browser (`sp browse`): navigate your projects as a
  tree, see every session's health at a glance — backed up, stale,
  unbacked, recoverable, lost — with live sessions highlighted as they
  run
- Inspector with token usage and offline cost estimates per model, and a
  transcript viewer that renders markdown, tool calls, and compaction
  boundaries the way the agent itself does — scrollable back to the
  very first message
- Three tiers of search: instant filtering (`/`), full-transcript search
  ranked by hit count (`ctrl+s`), and AI find — describe the session you
  half-remember and a local model identifies it (`ctrl+g`, via ollama or
  the claude CLI; optional, auto-detected)
- Lost-session detection: sessions recorded in agent history but missing
  from disk are surfaced permanently, so nothing disappears silently

**Command**
- One-key restore of deleted sessions from backup
- `o` opens any session: jumps to its terminal window if it's running
  (Spaces included), or resumes it in a fresh window if it's closed
- Transplant: move or copy sessions — project memory included — to a
  different directory, rewriting internal paths so resume continuity
  survives; copies mint a fresh session identity
- Every action lands in a permanent audit log, viewable in-app

## Install

Requires Go 1.24+ and git.

```sh
git clone https://github.com/rexovas/session-protect
cd session-protect
./scripts/install.sh        # installs to ~/.local/bin as session-protect + sp
```

Then wire up protection:

```sh
sp backup                   # first full backup
sp hook install             # realtime sync at every turn end (Claude Code)
sp schedule install         # daily committing backup (macOS)
sp browse                   # the session explorer
```

`sp doctor` checks the environment; `sp update` rebuilds from the
current source checkout.

## The explorer

`sp browse` (alias `sp ui`) opens the full-screen browser. The footer
shows the essentials; the complete reference lives under `m` → Keys.

| Key | Action |
| --- | --- |
| `↑/↓` `enter` `←` | move · open · back |
| `tab` / `ctrl+a` | switch folder/session panes · all sessions beneath |
| `ctrl+e` | expand/collapse the whole folder tree in place |
| `/` | filter the current pane as you type |
| `ctrl+s` | search transcripts for the query, ranked by hits |
| `ctrl+g` | AI find: describe a session, a local model ranks matches |
| `i` or `enter` | inspect: overview · usage/cost · full transcript |
| `o` | open: jump to a running session, or resume a closed one |
| `r` | restore a deleted-but-backed-up session |
| `t` | transplant a session or project to another directory |
| `x` | show/hide lost sessions |
| `m` | menu: stats · activity log · key reference |

## CLI reference

```text
sp backup [claude|codex]      full backup commit (alias: save)
sp sync                       mirror working state without committing
sp restore [--session ID]     bring deleted sessions back from backup
sp transplant --session ID --to DIR [--copy]
                              relocate sessions + memory (also --project)
sp browse                     the session explorer (alias: ui)
sp hook install               realtime sync + session guard hooks
sp schedule install           daily backup (macOS launchd)
sp status | project status    machine-wide / per-project protection state
sp plan | doctor | version    what would back up · env checks · build info
```

Most commands accept `--dry-run`, `--json`, or both.

## Configuration

Optional, at `~/.config/session-protect/config.toml` (override the path
with `SESSION_PROTECT_CONFIG`). Defaults are sensible; the file exists
for overrides:

```toml
backup_root = "~/Library/Application Support/SessionProtect"
topology = "combined"    # combined | per-target

[encryption]
mode = "none"            # or "git-crypt": first backup initializes the
                         # repo and exports a recovery key to key_path

[schedule]
time = "12:00"           # daily backup, local time

[assist]                 # AI find backend
backend = "auto"         # auto | ollama | claude | none
model = ""               # ollama model; empty = first installed

[targets.codex]
enabled = true           # per-agent enable/source overrides
```

## Safety model

- **Sync never deletes.** The realtime tier only adds and updates.
- **Deletions commit last.** Backups commit the working tree first, then
  record deletions in a second commit — a deleted session's final state
  is always in Git history before its removal is.
- **Destructive actions preview first.** Restore and transplant show
  exactly what will happen and ask for confirmation. Overwrites take
  timestamped safety copies; transplant is copy-first — sources are
  committed to backup before a move, and originals are removed only
  after verification.
- **Agent state is respected.** SessionProtect never rewrites agent
  history files; lost sessions stay marked lost even after recovery.
- **Everything is local.** Sessions never leave your machine. AI find
  talks only to your own local ollama server or your own claude CLI.
- **Encryption is opt-in** because local backups mirror data the agents
  already store unencrypted on the same disk; enable git-crypt when
  backups will leave the machine.

## Platform support

macOS is the primary platform (scheduling, window jumping, and resume
are macOS-native today). Backup, restore, transplant, and the explorer
run on Linux as well; Windows support is partial. Contributions toward
deeper Linux and Windows parity are welcome.

## Development

```sh
go test ./...        # full suite (race-enabled in CI)
go vet ./...
```

CI gates every change: tests with the race detector, gitleaks, and
shellcheck. See [docs/RELEASING.md](docs/RELEASING.md) for how versions
and releases work.

## License

See [LICENSE](LICENSE).
