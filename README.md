# SessionProtect

**Protect, explore, and command your AI coding-agent sessions.**

Your conversations with coding agents are work artifacts: decisions,
context, half-finished threads you'll want back. Agents treat them as
disposable — sessions get pruned, compacted, or lost when a directory
moves. SessionProtect is a single-binary CLI that backs them up safely,
lets you browse and search everything you've ever discussed, and puts
you back inside any session in one keystroke.

Supports **Claude Code** and **OpenAI Codex** side by side. Local-first:
no telemetry, no accounts, and your sessions never leave your machine.
The only network call is an optional once-daily release check
(`update.check = false` turns it off).

<img src="assets/demo.gif" alt="The SessionProtect session explorer: live sessions, health states, inspector, search, and restore" width="900">

*More: [feature walkthroughs](docs/DEMOS.md) · [how recovery works](docs/RECOVERY.md).*

## Highlights

**Protect**
- Git-based backup of all session state, with realtime sync at every
  turn end via agent hooks (incremental, tens of milliseconds)
- Tracks the hand-maintained configuration around the sessions too
  (settings, instructions, rules, hooks, skills, the plugin list), but
  never generated bulk: no log databases, caches, packages or installed
  plugins, and no config file over 1 MB
- Deletion-safe by design: syncing never propagates deletions, and
  scheduled backups commit every file's final state *before* recording
  its removal — anything that ever existed is recoverable (within the
  retention window, when one is set)
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
  half-remember and a model of your choice identifies it (`ctrl+g`;
  claude, codex, or fully local via ollama — enumerated live,
  auto-detected, optional). Opt in to **semantic search** with
  `sp index` (or `ctrl+b` in the browser) and a local embedder
  (`ollama pull nomic-embed-text`): sessions are embedded once, then
  AI find retrieves by meaning, not just shared words, so a paraphrase
  still lands. Past searches replay instantly from saved results, no
  repeat model calls
- Lost-session detection: sessions recorded in agent history but missing
  from disk are surfaced permanently, so nothing disappears silently —
  and can be rescued: export their surviving prompts, or rebuild them
  into a new resumable session, optionally with an AI-written brief of
  where the work stood ([how recovery works](docs/RECOVERY.md))

**Command**
- Session groups (`w`): named, cross-project sets of sessions — snapshot
  your currently-open sessions or hand-pick them (`+`), then resume the
  whole workspace in new windows or one at a time. A reserved *recently
  open* group auto-tracks your working set so it survives a reboot, and
  *weekly sessions* keeps one group per Monday–Sunday week (e.g.
  `10/12/26 - 10/18/26`) of every session you prompted, that wrote
  output, or that was open — including weeks before you installed, from
  the agents' prompt history. Weeks clear after 30 days; `c` copies one
  into a normal group to keep it
- One-key restore of deleted sessions from backup
- `o` opens any session: jumps to its terminal window if it's running
  (Spaces included); a closed one resumes in a fresh terminal window or
  right in the current one — your choice, in the right project directory
- Transplant: move or copy sessions — project memory included — to a
  different directory, rewriting internal paths so resume continuity
  survives; copies mint a fresh session identity
- Every action lands in a permanent audit log, viewable in-app

## Install

```sh
curl -fsSL https://get.rexov.as/session-protect | sh
```

Installs the latest release (checksum-verified) to `~/.local/bin` as
`session-protect` with an `sp` alias. Or use Homebrew:

```sh
brew install rexovas/tap/session-protect
```

Binaries for macOS, Linux, and Windows are on the
[releases page](https://github.com/rexovas/session-protect/releases).
From source (requires Go 1.24+):

```sh
git clone https://github.com/rexovas/session-protect
cd session-protect
./scripts/install.sh
```

Then wire up protection:

```sh
sp backup                   # first full backup
sp hook install             # realtime sync at every turn end (Claude Code)
sp schedule install         # daily committing backup (macOS)
sp browse                   # the session explorer
```

`sp doctor` checks the environment. `sp update` updates in place:
release installs download the latest release (checksum-verified) and
swap the binary; source installs rebuild from their checkout. The
explorer also checks for new releases at launch and every six hours
while it stays open, and offers the specific version before touching
anything ("Later" skips that version for the session) — disable with
`update.check = false`.

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
| `ctrl+g` | AI find: describe a session, your chosen model ranks matches |
| `ctrl+b` | build/refresh the semantic-search index (opt-in) |
| `f` | facet filter: states, agents, accounts, models, modified window (`D` saves the account selection as the launch default) |
| `i` or `enter` | inspect: overview · usage/cost · full transcript |
| `o` | open: jump to a running session, or resume a closed one (new window or in place) |
| `r` | restore/rescue a session; on a folder row, bulk-rescue its lost sessions |
| `t` | transplant a session or project to another directory |
| `A` | transfer a folder (with subfolders) or session to another account |
| `x` | show/hide lost sessions |
| `w` | groups: named session sets — open one to browse it as a scoped tree (`/`, `tab`, all as normal); `s` snapshot open · `c` copy · `M` merge · `d` delete · weekly groups fold under *weekly sessions* |
| `+` | add the highlighted session (or all `space`-selected) to a group |
| `v` | enter multi-select mode; `space` toggles, then `+` adds all selected to a group |
| `m` | menu: stats · activity log · key reference |

Folder rows show a **HEALTH** summary of everything beneath them:
`ok`, `~N` stale, `!N` unbacked, `✝N` recoverable-from-backup, `✕N`
lost (no transcript anywhere), and `⌂!` when the project's own
directory is gone from disk (its sessions survive — `t` transplants
them somewhere that exists). Session rows use the long form in the
STATE column.

## Several Claude accounts

Claude keeps its login, settings and sessions in its config dir:
`~/.claude`, or wherever `CLAUDE_CONFIG_DIR` points. Running one dir per
account (or per client) keeps logins, history and memory apart, and
session-protect protects each dir as its own **account**:

- **Discovery by path, not name.** A dir whose `settings.json` carries the
  session-protect hooks registers itself on its first session (the hooks
  inherit `CLAUDE_CONFIG_DIR`). `sp claude-dirs add PATH` registers one up
  front and installs the hooks; `sp claude-dirs` lists them. `~/.claude`
  is always present as `default`; other labels come from the dir name
  (`~/.claude-acme` → `acme`) unless given with `--label`. A label names
  the dir's backup folder (`claude-acme`), so it is fixed once set.
- **Backups** go to one target per dir; the default keeps `claude`.
  Restores, rescues and rebuilds land back in the session's own dir.
- **The explorer** shows every dir's sessions in one tree, with an
  ACCOUNT column and an ACCOUNT section in the `f` filter whose selection
  `D` saves as the default at launch. Resuming pins the agent to the
  session's dir: other dirs run with `CLAUDE_CONFIG_DIR` set, the default
  with it removed (setting it, even to `~/.claude`, makes Claude look for
  a different login).
- `sp transplant` works inside one dir; a project with sessions in
  several asks for `--home LABEL`.

**Folder ownership is learned, never configured.** A folder belongs to the
account whose dir holds its sessions; a folder without sessions of its own
inherits from the nearest owned parent, as long as nothing under that
parent belongs to another account (so one stray session started in
`~/projects` never claims everything beneath it). Unowned folders are
default's. `sp claude-dirs which [DIR]` shows a folder's owner.

- **`claude` on the right account.** Add `eval "$(sp shell-init zsh)"`
  (or `bash`) to your shell rc: `claude` then starts on the account that
  owns the current folder. `claude --account LABEL` picks one, which is
  how a brand-new folder gets its first owner; an explicit
  `CLAUDE_CONFIG_DIR` is respected; `command claude` skips it all.
- **One account per owned folder.** The session-start hook warns when a
  session runs on another account than its folder's owner (the desktop
  app or an IDE bypasses the shell); transplants into a folder another
  account owns are refused; rebuilt lost sessions land in the owner's dir.
- **Transfers.** `sp transfer --to LABEL --project PATH --recursive` (or
  `--session ID`) moves sessions to another account without changing
  their folder: transcripts and everything keyed by their id, project
  memory, and a copy of their prompt history, with a backup committed
  before and after. The preview lists each folder with its owner before
  and after. In the explorer, folder rows show `@owner` and `⇄N` for
  sessions on another account inside an owned folder; `A` transfers.

Symlinks inside a config dir (shared hooks or skills, say) are followed,
so each dir's backup holds real files and restores as plain files.

## CLI reference

```text
sp backup [TARGET]            full backup commit (alias: save); TARGET is
                              claude, codex, or claude-LABEL
sp sync                       mirror working state without committing
sp restore [--session ID]     bring deleted sessions back from backup
sp transplant --session ID --to DIR [--copy]
                              relocate sessions + memory (also --project)
sp browse                     the session explorer (alias: ui)
sp index [--clear]            build the semantic-search index (opt-in)
sp hook install               realtime sync + session guard hooks
sp claude-dirs [add PATH]     extra claude config dirs (accounts); which DIR
sp transfer --to LABEL --project PATH [--recursive]
                              move sessions to another account
sp shell-init zsh             `claude` starts on the folder's account
sp schedule install           daily backup (macOS launchd)
sp schedule reload            re-register it after replacing the binary
sp retention [--dry-run]      bound backup history (see [retention])
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

[retention]              # how much backup history to keep
keep = ""                # "" = "policy" with git-crypt, "all" otherwise
daily = 7                # policy: newest snapshot of each of the last 7 days,
weekly = 4               #   4 weeks and 6 months, plus the latest; older
monthly = 6              #   snapshots fold into the oldest kept one

[schedule]
time = "12:00"           # daily backup, local time

[assist]                 # AI find backend
backend = "auto"         # auto | ollama | claude | codex | none
model = ""               # ollama model; empty = first installed
claude_model = "sonnet"  # claude backend model; helper runs are
                         # throwaway and never appear in your sessions
embed_model = ""         # ollama embedding model for semantic retrieval;
                         # empty auto-detects an installed embedder
                         # (nomic-embed-text, etc.); absent = keyword only

[update]
check = true             # daily new-release check + explorer prompt

[targets.codex]
enabled = true           # per-agent enable/source overrides
```

## Safety model

- **Sync never deletes.** The realtime tier only adds and updates.
- **Deletions commit last.** Backups commit the working tree first, then
  record deletions in a second commit — a deleted session's final state
  is always in Git history before its removal is.
- **Retention is bounded, and off unless encrypted.** Git stores an
  unencrypted repo's history as small deltas, so by default every
  snapshot is kept. git-crypt stores every changed file whole and
  incompressible on each snapshot, so encrypted repos default to the
  retention policy, and any repo can opt in. Kept snapshots keep their
  exact contents, dates and messages, and a deleted session's final state
  is kept with its deletion inside the window; anything older than the
  oldest kept snapshot is no longer recoverable. `sp retention --dry-run`
  shows what a policy would keep.
- **Destructive actions preview first.** Restore and transplant show
  exactly what will happen and ask for confirmation. Overwrites take
  timestamped safety copies; transplant is copy-first — sources are
  committed to backup before a move, and originals are removed only
  after verification.
- **Agent state is respected.** SessionProtect never rewrites agent
  history files; lost sessions stay marked lost even after recovery.
- **Everything is local.** Sessions never leave your machine. AI find
  talks only to your own local ollama server or your own claude or
  codex CLI.
  The sole outbound call the tool ever makes is the release check
  against GitHub (at most every six hours per machine, version numbers
  only, off with `update.check = false`).
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

[Apache-2.0](LICENSE) © Rexovas Group Technologies LLC
