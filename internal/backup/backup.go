package backup

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/rexovas/session-protect/internal/backend/gitrepo"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/fsplan"
	"github.com/rexovas/session-protect/internal/targets"
	"github.com/rexovas/session-protect/internal/version"
)

type Result struct {
	Target    string `json:"target"`
	Repo      string `json:"repo"`
	Prefix    string `json:"prefix,omitempty"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Added     int    `json:"added"`
	Updated   int    `json:"updated"`
	Removed   int    `json:"removed"`
	Committed bool   `json:"committed"`
	Commit    string `json:"commit,omitempty"`
	DryRun    bool   `json:"dry_run"`
	Skipped   string `json:"skipped,omitempty"`
}

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	var targetFilter string
	dryRun := false
	allowUnencrypted := false
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			dryRun = true
		case "--allow-unencrypted":
			allowUnencrypted = true
		case "--json":
			asJSON = true
		case "claude", "codex":
			targetFilter = arg
		default:
			fmt.Fprintf(stderr, "unexpected argument: %s\n", arg)
			usage(stderr)
			return 2
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "backup failed: %v\n", err)
		return 1
	}

	results, err := Execute(cfg, targetFilter, dryRun, allowUnencrypted)
	if err != nil {
		fmt.Fprintf(stderr, "backup failed: %v\n", err)
		return 1
	}

	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return 0
	}
	printResults(stdout, results, dryRun)
	return 0
}

// Execute plans and (unless dryRun) syncs and commits each requested target.
// With combined topology all targets share one repo and one commit.
func Execute(cfg config.Config, targetFilter string, dryRun bool, allowUnencrypted bool) ([]Result, error) {
	selected := make([]targets.Target, 0, 2)
	for _, target := range cfg.ResolveTargets() {
		if targetFilter != "" && target.Name != targetFilter {
			continue
		}
		selected = append(selected, target)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no targets selected (filter %q)", targetFilter)
	}

	var results []Result
	repos := map[string][]int{} // repo path -> indexes into results needing commit

	for _, target := range selected {
		repo, prefix := cfg.RepoFor(target.Name)
		result := Result{Target: target.Name, Repo: repo, Prefix: prefix, DryRun: dryRun}

		if !target.Detected {
			result.Skipped = "source not found"
			results = append(results, result)
			continue
		}

		files, err := fsplan.Build(target)
		if err != nil {
			return nil, fmt.Errorf("plan %s: %w", target.Name, err)
		}
		result.Files = len(files)
		result.Bytes = fsplan.TotalSize(files)

		if dryRun {
			results = append(results, result)
			continue
		}

		if err := checkEncryption(cfg, repo, allowUnencrypted); err != nil {
			return nil, err
		}
		if err := gitrepo.Ensure(repo); err != nil {
			return nil, err
		}
		sync, err := gitrepo.Sync(repo, prefix, files)
		if err != nil {
			return nil, fmt.Errorf("sync %s: %w", target.Name, err)
		}
		result.Added, result.Updated, result.Removed = sync.Added, sync.Updated, sync.Removed
		repos[repo] = append(repos[repo], len(results))
		results = append(results, result)
	}

	for repo, indexes := range repos {
		scope := "all"
		if len(indexes) == 1 {
			scope = results[indexes[0]].Target
		}
		committed, err := gitrepo.Commit(repo, subject(scope), body(scope))
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", repo, err)
		}
		head := gitrepo.Head(repo)
		for _, i := range indexes {
			results[i].Committed = committed
			if committed {
				results[i].Commit = head
			}
		}
	}
	return results, nil
}

// checkEncryption fails closed when the configured mode is git-crypt but the
// destination repo is not (yet) a git-crypt repo, since the Go CLI cannot
// initialize git-crypt itself in this milestone.
func checkEncryption(cfg config.Config, repo string, allowUnencrypted bool) error {
	if cfg.Encryption.Mode == "none" || allowUnencrypted {
		return nil
	}
	if gitrepo.IsRepo(repo) && gitrepo.IsGitCrypt(repo) {
		if _, err := exec.LookPath("git-crypt"); err != nil {
			return fmt.Errorf("%s is a git-crypt repo but git-crypt is not installed", repo)
		}
		return nil
	}
	return fmt.Errorf("encryption.mode is git-crypt but %s is not a git-crypt repo; "+
		"git-crypt initialization is not implemented yet — rerun with --allow-unencrypted "+
		"or set encryption.mode = \"none\" to back up unencrypted", repo)
}

func printResults(out io.Writer, results []Result, dryRun bool) {
	if dryRun {
		fmt.Fprintln(out, "Backup plan (dry run, nothing written)")
	} else {
		fmt.Fprintln(out, "Backup")
	}
	fmt.Fprintln(out)
	for _, r := range results {
		fmt.Fprintf(out, "  %s\n", r.Target)
		if r.Skipped != "" {
			fmt.Fprintf(out, "    skipped   %s\n", r.Skipped)
			continue
		}
		fmt.Fprintf(out, "    files     %d (%s)\n", r.Files, formatBytes(r.Bytes))
		fmt.Fprintf(out, "    repo      %s\n", r.Repo)
		if !dryRun {
			fmt.Fprintf(out, "    changes   +%d ~%d -%d\n", r.Added, r.Updated, r.Removed)
			if r.Committed {
				fmt.Fprintf(out, "    commit    %s\n", r.Commit)
			} else {
				fmt.Fprintln(out, "    commit    none (no changes)")
			}
		}
	}
}

func subject(scope string) string {
	return fmt.Sprintf("session-protect: manual %s backup %s", scope, time.Now().Format(time.RFC3339))
}

func body(scope string) string {
	return fmt.Sprintf("action: backup\ntarget: %s\ntool-version: %s", scope, version.Version)
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect backup [claude|codex] [--dry-run] [--allow-unencrypted] [--json]")
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}
