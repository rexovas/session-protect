package retention

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/lock"
)

// Run is `session-protect retention [--dry-run] [--keep all|policy|latest]`:
// preview or apply the retention policy to the backup repos now.
func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	dryRun, keep := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--keep":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "--keep needs a value: all, policy, or latest")
				return 2
			}
			i++
			keep = args[i]
		case "help", "-h", "--help":
			usage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "unexpected argument: %s\n\n", args[i])
			usage(stderr)
			return 2
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "retention failed: %v\n", err)
		return 1
	}
	if keep == "" {
		keep = cfg.EffectiveKeep()
	}
	switch keep {
	case "all", "policy", "latest":
	default:
		fmt.Fprintf(stderr, "invalid --keep %q (want all, policy, or latest)\n", keep)
		return 2
	}
	policy := Policy{Keep: keep, Daily: cfg.Retention.Daily, Weekly: cfg.Retention.Weekly, Monthly: cfg.Retention.Monthly}

	if !dryRun {
		release, err := lock.Acquire(cfg.BackupRoot)
		if errors.Is(err, lock.ErrBusy) {
			fmt.Fprintf(stderr, "retention failed: %v\n", err)
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "retention failed: %v\n", err)
			return 1
		}
		defer release()
	}

	if dryRun {
		fmt.Fprintf(stdout, "Retention plan (%s, dry run, nothing rewritten)\n\n", keep)
	} else {
		fmt.Fprintf(stdout, "Retention (%s)\n\n", keep)
	}
	code := 0
	for _, repo := range repos(cfg) {
		plan, err := Build(repo, policy)
		if err != nil {
			fmt.Fprintf(stderr, "  %s: %v\n", repo, err)
			code = 1
			continue
		}
		fmt.Fprintf(stdout, "  %s\n    snapshots %d, keep %d, drop %d\n", repo, plan.Kept+plan.Dropped, plan.Kept, plan.Dropped)
		if dryRun || plan.Dropped == 0 {
			continue
		}
		if err := Apply(repo, plan); err != nil {
			fmt.Fprintf(stderr, "  %s: %v\n", repo, err)
			code = 1
			continue
		}
		fmt.Fprintln(stdout, "    rewritten and reclaimed")
	}
	return code
}

// repos lists the backup repos that exist under the configured topology.
func repos(cfg config.Config) []string {
	names := []string{"all"}
	if cfg.Topology == "per-target" {
		names = []string{"claude", "codex"}
	}
	var out []string
	for _, name := range names {
		path := filepath.Join(cfg.BackupRoot, name)
		if info, err := os.Stat(filepath.Join(path, ".git")); err == nil && info.IsDir() {
			out = append(out, path)
		}
	}
	return out
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect retention [--dry-run] [--keep all|policy|latest]")
}
