package transplant

import (
	"fmt"
	"io"
	"strings"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/config"
)

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	var opts Options
	yes := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--copy":
			opts.Copy = true
		case "--dry-run":
			opts.DryRun = true
		case "--yes":
			yes = true
		case "--session":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--session requires a value")
				return 2
			}
			opts.SessionID = args[i]
		case "--project":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--project requires a value")
				return 2
			}
			opts.Project = args[i]
		case "--to":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--to requires a value")
				return 2
			}
			opts.To = args[i]
		case "--memory":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--memory requires a value")
				return 2
			}
			opts.Memory = args[i]
		case "help", "-h", "--help":
			usage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "unexpected argument: %s\n", args[i])
			usage(stderr)
			return 2
		}
	}
	switch opts.Memory {
	case "", "keep-both", "skip", "replace":
	default:
		fmt.Fprintf(stderr, "invalid --memory %q (want keep-both, skip, or replace)\n", opts.Memory)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "transplant failed: %v\n", err)
		return 1
	}
	plan, err := Build(cfg, opts)
	if err != nil {
		fmt.Fprintf(stderr, "transplant failed: %v\n", err)
		return 1
	}

	printPlan(stdout, plan, opts)
	if opts.DryRun {
		return 0
	}
	if !yes && !confirm(stdin, stdout, plan, opts) {
		fmt.Fprintln(stdout, "Aborted.")
		return 1
	}

	// The pre-transplant state is committed to backup before anything
	// moves, and the transplanted state immediately after — both ends of
	// the operation are durable git history, not just a mirror.
	if _, err := backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "pre-transplant"}); err != nil {
		fmt.Fprintf(stderr, "transplant refused: pre-move backup failed: %v\n", err)
		return 1
	}

	if err := Apply(cfg, plan, opts); err != nil {
		fmt.Fprintf(stderr, "transplant failed: %v\n", err)
		return 1
	}
	if _, err := backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "post-transplant"}); err != nil {
		fmt.Fprintf(stderr, "warning: post-transplant backup failed: %v\n", err)
	}
	verb := "Moved"
	if opts.Copy {
		verb = "Copied"
	}
	fmt.Fprintf(stdout, "%s %d session(s) to %s.\n", verb, len(plan.Sessions), plan.TargetPath)
	return 0
}

func printPlan(out io.Writer, plan *Plan, opts Options) {
	verb := "move"
	if opts.Copy {
		verb = "copy"
	}
	header := fmt.Sprintf("Transplant plan (%s)", verb)
	if opts.DryRun {
		header += " — dry run, nothing written"
	}
	fmt.Fprintln(out, header)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  source  %s\n", plan.SourcePath)
	fmt.Fprintf(out, "  target  %s\n", plan.TargetPath)
	if plan.CreatesDir {
		fmt.Fprintf(out, "  create  target directory does not exist — will create it (mkdir -p)\n")
	}
	fmt.Fprintln(out)
	for _, session := range plan.Sessions {
		line := "  session " + session.ID
		if session.NewID != "" {
			line += " → " + session.NewID + " (new identity)"
		}
		if session.Open {
			line += "  [open now]"
		}
		fmt.Fprintln(out, line)
	}
	switch plan.MemoryAction {
	case "none":
		fmt.Fprintln(out, "  memory  none at source")
	case "skip":
		fmt.Fprintln(out, "  memory  target already has memory — skipping (per --memory skip)")
	case "keep-both":
		fmt.Fprintf(out, "  memory  target already has memory — incoming lands at\n          %s\n", plan.MemoryDst)
	case "replace":
		fmt.Fprintln(out, "  memory  REPLACING target memory (safety copy under the backup root first)")
	default:
		fmt.Fprintf(out, "  memory  %s → %s\n", plan.MemoryAction, plan.MemoryDst)
	}
	fmt.Fprintln(out)
}

func confirm(stdin io.Reader, stdout io.Writer, plan *Plan, opts Options) bool {
	verb := "Move"
	if opts.Copy {
		verb = "Copy"
	}
	fmt.Fprintf(stdout, "%s %d session(s)? [y/N] ", verb, len(plan.Sessions))
	var answer string
	_, _ = fmt.Fscanln(stdin, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect transplant (--session ID | --project PATH) --to DIR")
	fmt.Fprintln(out, "                             [--copy] [--memory keep-both|skip|replace]")
	fmt.Fprintln(out, "                             [--dry-run] [--yes]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Relocates claude sessions (and project memory) to another directory,")
	fmt.Fprintln(out, "rewriting internal paths so resume works from the new home. Moves are")
	fmt.Fprintln(out, "copy-first: source is synced to backup, targets verified, originals")
	fmt.Fprintln(out, "removed last. Copies mint a fresh session id. Target memory is never")
	fmt.Fprintln(out, "overwritten by default (incoming lands beside it).")
}
