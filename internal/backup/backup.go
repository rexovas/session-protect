package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rexovas/session-protect/internal/backend/gitrepo"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/encryption"
	"github.com/rexovas/session-protect/internal/fsplan"
	"github.com/rexovas/session-protect/internal/lock"
	"github.com/rexovas/session-protect/internal/targets"
	"github.com/rexovas/session-protect/internal/version"
)

type Result struct {
	Target      string `json:"target"`
	Repo        string `json:"repo"`
	Prefix      string `json:"prefix,omitempty"`
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
	Added       int    `json:"added"`
	Updated     int    `json:"updated"`
	Removed     int    `json:"removed"`
	Committed   bool   `json:"committed"`
	Commit      string `json:"commit,omitempty"`
	DryRun      bool   `json:"dry_run"`
	SyncOnly    bool   `json:"sync_only,omitempty"`
	Skipped     string `json:"skipped,omitempty"`
	KeyExported string `json:"key_exported,omitempty"`
}

type Options struct {
	Target           string
	DryRun           bool
	AllowUnencrypted bool
	// SyncOnly mirrors files into the repo working tree without committing.
	SyncOnly bool
}

// Run handles both `backup` and `sync`. In sync mode a held lock is a quiet
// no-op so high-frequency triggers never fail loudly or queue up.
func Run(args []string, stdout io.Writer, stderr io.Writer, syncOnly bool) int {
	opts := Options{SyncOnly: syncOnly}
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.DryRun = true
		case "--allow-unencrypted":
			opts.AllowUnencrypted = true
		case "--json":
			asJSON = true
		case "claude", "codex":
			opts.Target = arg
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

	if !opts.DryRun {
		release, err := lock.Acquire(cfg.BackupRoot)
		if errors.Is(err, lock.ErrBusy) {
			if opts.SyncOnly {
				return 0
			}
			fmt.Fprintf(stderr, "backup failed: %v\n", err)
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "backup failed: %v\n", err)
			return 1
		}
		defer release()
	}

	results, err := Execute(cfg, opts)
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
	printResults(stdout, results, opts)
	return 0
}

// Execute plans and (unless dry-run) syncs each requested target, then
// commits unless in sync-only mode. With combined topology all targets share
// one repo and one commit.
func Execute(cfg config.Config, opts Options) ([]Result, error) {
	targetFilter, dryRun := opts.Target, opts.DryRun
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
		result := Result{Target: target.Name, Repo: repo, Prefix: prefix, DryRun: dryRun, SyncOnly: opts.SyncOnly}

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

		keyExported, err := ensureEncryption(cfg, repo, opts.AllowUnencrypted)
		if err != nil {
			return nil, err
		}
		result.KeyExported = keyExported
		if err := gitrepo.Ensure(repo); err != nil {
			return nil, err
		}
		sync, err := gitrepo.Sync(repo, prefix, files)
		if err != nil {
			return nil, fmt.Errorf("sync %s: %w", target.Name, err)
		}
		result.Added, result.Updated, result.Removed = sync.Added, sync.Updated, sync.Removed
		if !opts.SyncOnly {
			repos[repo] = append(repos[repo], len(results))
		}
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

// ensureEncryption enforces the configured encryption mode before any file is
// staged. A brand-new destination is created as a git-crypt repo with the
// recovery key exported to the configured key path (returned so callers can
// surface it). Existing repos must already match the configured mode; a
// locked git-crypt repo or a plain repo under git-crypt mode fails closed.
func ensureEncryption(cfg config.Config, repo string, allowUnencrypted bool) (keyExported string, err error) {
	if cfg.Encryption.Mode == "none" || allowUnencrypted {
		return "", nil
	}

	if gitrepo.IsRepo(repo) {
		if !gitrepo.IsGitCrypt(repo) {
			return "", fmt.Errorf("encryption.mode is git-crypt but %s is not a git-crypt repo; "+
				"rerun with --allow-unencrypted or set encryption.mode = \"none\"", repo)
		}
		if !encryption.Installed() {
			return "", fmt.Errorf("%s is a git-crypt repo but git-crypt is not installed", repo)
		}
		if !encryption.Unlocked(repo) {
			return "", fmt.Errorf("%s is locked; run: cd %s && git-crypt unlock %s", repo, repo, cfg.Encryption.KeyPath)
		}
		return "", nil
	}

	if !encryption.Installed() {
		return "", fmt.Errorf("git-crypt is not installed; install it or rerun with --allow-unencrypted")
	}
	if err := gitrepo.Ensure(repo); err != nil {
		return "", err
	}
	if err := encryption.Setup(repo, cfg.Encryption.KeyPath); err != nil {
		return "", fmt.Errorf("git-crypt setup for %s: %w", repo, err)
	}
	return cfg.Encryption.KeyPath, nil
}

func printResults(out io.Writer, results []Result, opts Options) {
	switch {
	case opts.DryRun:
		fmt.Fprintln(out, "Backup plan (dry run, nothing written)")
	case opts.SyncOnly:
		fmt.Fprintln(out, "Sync (working tree only, no commit)")
	default:
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
		if !opts.DryRun {
			fmt.Fprintf(out, "    changes   +%d ~%d -%d\n", r.Added, r.Updated, r.Removed)
			if !opts.SyncOnly {
				if r.Committed {
					fmt.Fprintf(out, "    commit    %s\n", r.Commit)
				} else {
					fmt.Fprintln(out, "    commit    none (no changes)")
				}
			}
		}
		if r.KeyExported != "" {
			fmt.Fprintln(out)
			fmt.Fprintf(out, "  Initialized git-crypt and exported the recovery key to:\n    %s\n", r.KeyExported)
			fmt.Fprintln(out, "  This key is required to decrypt backups from a fresh clone.")
			fmt.Fprintln(out, "  Store a copy in a secure password manager. Never commit it.")
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
	fmt.Fprintln(out, "  session-protect sync [claude|codex] [--allow-unencrypted] [--json]")
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
