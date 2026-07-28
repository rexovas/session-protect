# SessionProtect

SessionProtect is a multi-platform CLI for protecting local AI-agent session
state (Claude Code, Codex).

Current milestone: read-only discovery commands plus a local Git backup backend.

```bash
go run ./cmd/session-protect version
go run ./cmd/session-protect doctor
go run ./cmd/session-protect plan
go run ./cmd/session-protect plan --json
go run ./cmd/session-protect status
go run ./cmd/session-protect status --json
go run ./cmd/session-protect project status
go run ./cmd/session-protect project status --json
go run ./cmd/session-protect backup --dry-run
go run ./cmd/session-protect backup [claude|codex] [--json]
go run ./cmd/session-protect save
go run ./cmd/session-protect sync [claude|codex]
```

`backup` mirrors each detected target's planned files into a local Git
repository under the backup root and records one commit per run. Deleted
sessions leave the working tree but remain recoverable from Git history.
`save` is an alias for `backup`. `sync` mirrors without committing — a cheap
operation suitable for high-frequency triggers such as agent hooks; history
commits still come from `backup`. Backup and sync share a lock under the
backup root; a concurrent `sync` exits quietly instead of failing.

Encryption is off by default: local backups mirror data the agents already
store unencrypted on the same disk. Set `encryption.mode = "git-crypt"` to
opt in — the first backup then initializes git-crypt in the destination repo
and exports a recovery key to `encryption.key_path` (default
`~/.config/session-protect/git-crypt.key`); store that key securely. A
destination that already exists unencrypted fails closed under git-crypt mode
unless `--allow-unencrypted` is passed.

Configuration is read from `~/.config/session-protect/config.toml`
(override with `SESSION_PROTECT_CONFIG`). All settings are optional:

```toml
backup_root = "~/backups/sessions"
topology = "combined" # combined | per-target

[encryption]
mode = "none" # none | git-crypt
key_path = "~/.config/session-protect/git-crypt.key"

[targets.claude]
enabled = true

[targets.codex]
enabled = true
```

Local dogfood install:

```bash
scripts/install.sh
session-protect update --check
session-protect update
```

The installer defaults to `~/.local/bin/session-protect` and also creates an `sp`
alias. Use `--prefix PATH` to install somewhere else. The current update command
supports the local source channel: it rebuilds and reinstalls from the same
checkout that created the installed binary.

Not yet implemented: `init`, `restore`, remote push, scheduling, TUI.
