package transfer

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/config"
)

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	var opts Options
	dryRun, yes := false, false
	for i := 0; i < len(args); i++ {
		value := func(flag string) (string, bool) {
			i++
			if i == len(args) {
				fmt.Fprintf(stderr, "%s requires a value\n", flag)
				return "", false
			}
			return args[i], true
		}
		var ok = true
		switch args[i] {
		case "--to":
			opts.To, ok = value("--to")
		case "--from":
			opts.From, ok = value("--from")
		case "--session":
			opts.SessionID, ok = value("--session")
		case "--project":
			opts.Project, ok = value("--project")
		case "--recursive", "-r":
			opts.Recursive = true
		case "--dry-run":
			dryRun = true
		case "--yes":
			yes = true
		case "help", "-h", "--help":
			usage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "unexpected argument: %s\n", args[i])
			usage(stderr)
			return 2
		}
		if !ok {
			return 2
		}
	}
	if opts.To == "" {
		fmt.Fprintln(stderr, "--to is required")
		usage(stderr)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "transfer failed: %v\n", err)
		return 1
	}
	plan, err := Build(cfg, opts)
	if err != nil {
		fmt.Fprintf(stderr, "transfer failed: %v\n", err)
		return 1
	}
	PrintPlan(stdout, plan, dryRun)
	if dryRun {
		return 0
	}
	if !yes {
		fmt.Fprintf(stdout, "Transfer %d session(s) to %s? [y/N] ", len(plan.Sessions), plan.To.Label)
		var answer string
		_, _ = fmt.Fscanln(stdin, &answer)
		if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Aborted.")
			return 1
		}
	}

	// Both ends of the move are committed to backup history.
	if _, err := backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "pre-transfer"}); err != nil {
		fmt.Fprintf(stderr, "transfer refused: pre-move backup failed: %v\n", err)
		return 1
	}
	if err := Apply(cfg, plan); err != nil {
		fmt.Fprintf(stderr, "transfer failed: %v\n", err)
		return 1
	}
	if _, err := backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "post-transfer"}); err != nil {
		fmt.Fprintf(stderr, "warning: post-transfer backup failed: %v\n", err)
	}
	fmt.Fprintf(stdout, "Transferred %d session(s) from %s to %s.\n", len(plan.Sessions), plan.From.Label, plan.To.Label)
	return 0
}

// PrintPlan is the preview: every folder with its sessions and owner
// before → after, then memory, then anything left behind.
func PrintPlan(out io.Writer, plan *Plan, dryRun bool) {
	header := fmt.Sprintf("Transfer plan: %s → %s", plan.From.Label, plan.To.Label)
	if dryRun {
		header += " (dry run, nothing written)"
	}
	fmt.Fprintln(out, header)
	fmt.Fprintf(out, "  from  %s\n  to    %s\n\n", tilde(plan.From.Dir), tilde(plan.To.Dir))

	width := 0
	for _, folder := range plan.Folders {
		width = max(width, len(tilde(folder.Path)))
	}
	fmt.Fprintf(out, "  %-*s  %8s  %s\n", width, "FOLDER", "SESSIONS", "OWNER")
	for _, folder := range plan.Folders {
		owner := folder.Before
		if folder.After != folder.Before {
			owner = folder.Before + " → " + folder.After
		}
		line := fmt.Sprintf("  %-*s  %8d  %s", width, tilde(folder.Path), folder.Sessions, owner)
		if folder.Left > 0 {
			line += fmt.Sprintf("  (%d stay on %s)", folder.Left, plan.From.Label)
		}
		fmt.Fprintln(out, line)
	}
	for _, memory := range plan.Memory {
		verb := "moves"
		if memory.Copy {
			verb = "is copied (sessions stay behind)"
		}
		fmt.Fprintf(out, "\n  memory of %s %s to\n    %s", tilde(memory.Project), verb, tilde(memory.To))
	}
	if len(plan.Memory) > 0 {
		fmt.Fprintln(out)
	}
	if len(plan.Extras) > 0 {
		fmt.Fprintf(out, "\n  %d other item(s) in drained project dirs move along (session sidecars, notes)\n", len(plan.Extras))
	}
	for _, dir := range plan.Unresolved {
		fmt.Fprintf(out, "\n  skipped %s: no transcript says which folder it belongs to", tilde(dir))
	}
	if len(plan.Unresolved) > 0 {
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Prompt history is copied; the source history is not changed. A backup")
	fmt.Fprintln(out, "  is committed before and after. Folder paths inside sessions are unchanged.")
	fmt.Fprintln(out)
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect transfer --to LABEL (--session ID | --project PATH [--recursive])")
	fmt.Fprintln(out, "                           [--from LABEL] [--dry-run] [--yes]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Moves claude sessions to another claude config dir (account), keeping their")
	fmt.Fprintln(out, "project folder. Sessions come from the default dir unless --from names")
	fmt.Fprintln(out, "another. A folder owned by a third dir is refused, and so are open sessions.")
}

func tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}
