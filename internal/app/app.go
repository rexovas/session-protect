package app

import (
	"fmt"
	"io"
	"os"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/browse"
	"github.com/rexovas/session-protect/internal/doctor"
	"github.com/rexovas/session-protect/internal/hook"
	"github.com/rexovas/session-protect/internal/plan"
	"github.com/rexovas/session-protect/internal/project"
	"github.com/rexovas/session-protect/internal/restore"
	"github.com/rexovas/session-protect/internal/schedule"
	"github.com/rexovas/session-protect/internal/status"
	"github.com/rexovas/session-protect/internal/update"
	"github.com/rexovas/session-protect/internal/version"
)

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stdout)
		return 0
	}

	switch args[0] {
	case "backup", "save":
		return backup.Run(args[1:], stdout, stderr, false)
	case "sync":
		return backup.Run(args[1:], stdout, stderr, true)
	case "restore":
		return restore.Run(args[1:], os.Stdin, stdout, stderr)
	case "schedule":
		return schedule.Run(args[1:], stdout, stderr)
	case "hook":
		return hook.Run(args[1:], stdout, stderr)
	case "browse", "ui":
		return browse.Run(args[1:], stdout, stderr)
	case "version":
		if hasFlag(args[1:], "--verbose") {
			fmt.Fprintf(stdout, "session-protect version %s\n", version.Version)
			fmt.Fprintf(stdout, "  commit         %s\n", version.Commit)
			fmt.Fprintf(stdout, "  date           %s\n", version.Date)
			fmt.Fprintf(stdout, "  channel        %s\n", version.Channel)
			fmt.Fprintf(stdout, "  source         %s\n", version.SourceDir)
			fmt.Fprintf(stdout, "  install prefix %s\n", version.InstallPrefix)
			return 0
		}
		fmt.Fprintf(stdout, "session-protect version %s\n", version.Version)
		return 0
	case "doctor":
		return doctor.Run(stdout)
	case "plan":
		return plan.Print(stdout, hasFlag(args[1:], "--json"))
	case "project":
		return project.Run(args[1:], stdout, stderr)
	case "status":
		return status.Print(stdout, hasFlag(args[1:], "--json"))
	case "update":
		return update.Run(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "SessionProtect")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect backup [claude|codex] [--dry-run] [--allow-unencrypted] [--json]")
	fmt.Fprintln(out, "  session-protect save [claude|codex]   (alias for backup)")
	fmt.Fprintln(out, "  session-protect sync [claude|codex]   (mirror without committing)")
	fmt.Fprintln(out, "  session-protect restore [--missing] [--target claude|codex] [--project PATH]")
	fmt.Fprintln(out, "                          [--session ID] [--overwrite] [--dry-run] [--yes]")
	fmt.Fprintln(out, "  session-protect schedule install|status|uninstall")
	fmt.Fprintln(out, "  session-protect hook install|status|uninstall [claude]")
	fmt.Fprintln(out, "  session-protect browse   (interactive session browser; alias: ui)")
	fmt.Fprintln(out, "  session-protect version [--verbose]")
	fmt.Fprintln(out, "  session-protect doctor")
	fmt.Fprintln(out, "  session-protect plan [--json]")
	fmt.Fprintln(out, "  session-protect project status [path] [--json]")
	fmt.Fprintln(out, "  session-protect status [--json]")
	fmt.Fprintln(out, "  session-protect update [--check]")
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}
