package restore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/lock"
	"github.com/rexovas/session-protect/internal/project"
)

type Options struct {
	Target    string
	Project   string
	SessionID string
	DryRun    bool
	Yes       bool
	Overwrite bool
}

// Item is one session file selected for restore.
type Item struct {
	Target      string `json:"target"`
	SessionID   string `json:"session_id"`
	State       string `json:"state"`
	From        string `json:"from"`
	To          string `json:"to"`
	Overwriting bool   `json:"overwriting"`
	SafetyCopy  string `json:"safety_copy,omitempty"`
}

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	var opts Options
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			opts.DryRun = true
		case "--yes":
			opts.Yes = true
		case "--overwrite":
			opts.Overwrite = true
		case "--missing":
			// restoring missing sessions is the default; accepted for clarity
		case "--target":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--target requires a value")
				return 2
			}
			opts.Target = args[i]
		case "--project":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--project requires a value")
				return 2
			}
			opts.Project = args[i]
		case "--session":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--session requires a value")
				return 2
			}
			opts.SessionID = args[i]
		case "help", "-h", "--help":
			usage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "unexpected argument: %s\n", args[i])
			usage(stderr)
			return 2
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "restore failed: %v\n", err)
		return 1
	}

	items, err := Plan(opts)
	if err != nil {
		fmt.Fprintf(stderr, "restore failed: %v\n", err)
		return 1
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "Nothing to restore.")
		return 0
	}

	printPlan(stdout, items, opts)
	if opts.DryRun {
		return 0
	}

	if !opts.Yes {
		if !confirm(stdin, stdout, len(items)) {
			fmt.Fprintln(stdout, "Aborted.")
			return 1
		}
	}

	release, err := lock.Acquire(cfg.BackupRoot)
	if errors.Is(err, lock.ErrBusy) {
		fmt.Fprintf(stderr, "restore failed: %v\n", err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "restore failed: %v\n", err)
		return 1
	}
	defer release()

	restored, err := Apply(cfg, items)
	if err != nil {
		fmt.Fprintf(stderr, "restore failed after %d file(s): %v\n", restored, err)
		return 1
	}
	fmt.Fprintf(stdout, "Restored %d session file(s).\n", restored)
	return 0
}

// Plan selects backed-up sessions to restore for the project. By default it
// selects sessions missing from the live source; --session selects one
// specific session and --overwrite permits replacing a live file with the
// backed-up copy.
func Plan(opts Options) ([]Item, error) {
	status, err := project.Build(opts.Project)
	if err != nil {
		return nil, err
	}

	var items []Item
	for _, target := range status.Targets {
		if opts.Target != "" && target.Name != opts.Target {
			continue
		}
		for _, session := range target.Sessions {
			if opts.SessionID != "" && session.ID != opts.SessionID {
				continue
			}
			if session.BackupPath == "" {
				continue // nothing to restore from
			}
			overwriting := session.SourcePath != ""
			if overwriting && !opts.Overwrite {
				if opts.SessionID != "" {
					return nil, fmt.Errorf("session %s exists in the live source; pass --overwrite to replace it", session.ID)
				}
				continue
			}

			dest := session.SourcePath
			if dest == "" {
				rel, relErr := filepath.Rel(target.BackupPath, session.BackupPath)
				if relErr != nil || strings.HasPrefix(rel, "..") {
					return nil, fmt.Errorf("session %s: backup path %s escapes %s", session.ID, session.BackupPath, target.BackupPath)
				}
				dest = filepath.Join(target.SourcePath, rel)
			}

			items = append(items, Item{
				Target:      target.Name,
				SessionID:   session.ID,
				State:       session.State,
				From:        session.BackupPath,
				To:          dest,
				Overwriting: overwriting,
			})
		}
	}
	if opts.SessionID != "" && len(items) == 0 {
		return nil, fmt.Errorf("session %s not found in backup for this project", opts.SessionID)
	}
	return items, nil
}

// Apply copies the planned files back into the live source directories,
// making a timestamped safety copy before any overwrite and logging each
// operation under the backup root.
func Apply(cfg config.Config, items []Item) (int, error) {
	stamp := time.Now().Format("20060102-150405")
	restored := 0
	for i := range items {
		item := &items[i]
		if item.Overwriting {
			safety := filepath.Join(cfg.BackupRoot, "restore-safety", stamp, item.Target, filepath.Base(item.To))
			if err := copyFile(item.To, safety); err != nil {
				return restored, fmt.Errorf("safety copy for %s: %w", item.SessionID, err)
			}
			item.SafetyCopy = safety
		}
		if err := copyFile(item.From, item.To); err != nil {
			return restored, fmt.Errorf("restore %s: %w", item.SessionID, err)
		}
		restored++
	}
	logRestore(cfg, items)
	return restored, nil
}

func printPlan(out io.Writer, items []Item, opts Options) {
	if opts.DryRun {
		fmt.Fprintln(out, "Restore plan (dry run, nothing written)")
	} else {
		fmt.Fprintln(out, "Restore plan")
	}
	fmt.Fprintln(out)
	for _, item := range items {
		action := "restore"
		if item.Overwriting {
			action = "overwrite"
		}
		fmt.Fprintf(out, "  %-9s %-7s %s\n", action, item.Target, item.SessionID)
		fmt.Fprintf(out, "    from  %s\n", item.From)
		fmt.Fprintf(out, "    to    %s\n", item.To)
	}
	fmt.Fprintln(out)
}

func confirm(stdin io.Reader, stdout io.Writer, count int) bool {
	fmt.Fprintf(stdout, "Restore %d session file(s)? [y/N] ", count)
	var answer string
	_, _ = fmt.Fscanln(stdin, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func copyFile(src string, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dest, info.ModTime(), info.ModTime())
}

// logRestore records each restored file in the audit log.
func logRestore(cfg config.Config, items []Item) {
	now := time.Now()
	entries := make([]audit.Entry, 0, len(items))
	for _, item := range items {
		entries = append(entries, audit.Entry{
			Time:       now,
			Action:     "restore",
			Target:     item.Target,
			SessionID:  item.SessionID,
			From:       item.From,
			To:         item.To,
			Overwrote:  item.Overwriting,
			SafetyCopy: item.SafetyCopy,
		})
	}
	audit.Append(cfg.BackupRoot, entries)
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect restore [--missing] [--target claude|codex] [--project PATH]")
	fmt.Fprintln(out, "                          [--session ID] [--overwrite] [--dry-run] [--yes]")
}
