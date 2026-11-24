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
		case "--agent":
			opts.Agent, ok = value("--agent")
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
		fmt.Fprintf(stdout, "Transfer %s to %s? [y/N] ", count(plan), plan.To.Label)
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
	fmt.Fprintf(stdout, "Transferred %s from %s to %s.\n", count(plan), plan.From.Label, plan.To.Label)
	for _, warning := range plan.NameWarnings {
		fmt.Fprintf(stderr, "warning: thread name not restored: %s\n", warning)
	}
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
	if len(plan.Sessions) > 0 || len(plan.Memory) > 0 || len(plan.Extras) > 0 {
		fmt.Fprintf(out, "  claude  %s → %s\n", tilde(plan.From.Dir), tilde(plan.To.Dir))
	}
	if len(plan.Codex) > 0 {
		fmt.Fprintf(out, "  codex   %s → %s\n", tilde(plan.CodexFrom.Dir), tilde(plan.CodexTo.Dir))
	}
	fmt.Fprintln(out)

	width := 0
	for _, folder := range plan.Folders {
		width = max(width, len(tilde(folder.Path)))
	}
	if len(plan.Folders) > 0 {
		fmt.Fprintf(out, "  %-*s  %6s  %5s  %4s  %s\n", width, "FOLDER", "CLAUDE", "CODEX", "LOST", "OWNER")
	}
	for _, folder := range plan.Folders {
		owner := folder.Before
		if folder.After != folder.Before {
			owner = folder.Before + " → " + folder.After
		}
		line := fmt.Sprintf("  %-*s  %6d  %5d  %4d  %s", width, tilde(folder.Path), folder.Sessions, folder.Codex, folder.Lost, owner)
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
	if len(plan.Lost) > 0 {
		fmt.Fprintf(out, "\n  LOST: %d session(s) with only their prompt history left; it is copied to %s\n", len(plan.Lost), plan.To.Label)
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
	if subagents := len(plan.Codex) - codexTopLevel(plan); subagents > 0 {
		fmt.Fprintf(out, "\n  %d codex subagent session(s) move with the sessions that spawned them\n", subagents)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Prompt history (and codex thread names) is copied; the source is not")
	fmt.Fprintln(out, "  changed. A backup is committed before and after. Folder paths inside")
	fmt.Fprintln(out, "  sessions are unchanged; codex indexes moved sessions on its next start.")
	fmt.Fprintln(out)
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect transfer --to LABEL (--session ID | --project PATH [--recursive])")
	fmt.Fprintln(out, "                           [--from LABEL] [--agent claude|codex] [--dry-run] [--yes]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Moves sessions to another account (its claude config dir and codex home),")
	fmt.Fprintln(out, "keeping their project folder. Both agents' sessions move unless --agent")
	fmt.Fprintln(out, "picks one. Sessions come from the default account unless --from names")
	fmt.Fprintln(out, "another. A folder owned by a third account is refused, and so are open sessions.")
}

func tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

// count describes what moves, e.g. "70 claude + 4 codex session(s)".
func count(plan *Plan) string {
	codex := codexTopLevel(plan)
	if len(plan.Sessions) == 0 && codex == 0 && len(plan.Lost) > 0 {
		return fmt.Sprintf("%d lost session record(s)", len(plan.Lost))
	}
	switch {
	case codex == 0:
		return fmt.Sprintf("%d session(s)", len(plan.Sessions))
	case len(plan.Sessions) == 0:
		return fmt.Sprintf("%d codex session(s)", codex)
	}
	return fmt.Sprintf("%d claude + %d codex session(s)", len(plan.Sessions), codex)
}

func codexTopLevel(plan *Plan) int {
	n := 0
	for _, folder := range plan.Folders {
		n += folder.Codex
	}
	return n
}
