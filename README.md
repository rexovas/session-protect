# SessionProtect

SessionProtect is a planned multi-platform CLI for protecting local AI-agent session state.

Current milestone: read-only discovery commands.

```bash
go run ./cmd/session-protect version
go run ./cmd/session-protect doctor
go run ./cmd/session-protect plan
go run ./cmd/session-protect plan --json
go run ./cmd/session-protect status
go run ./cmd/session-protect status --json
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

The first implementation pass does not create, modify, restore, or push backup data.
