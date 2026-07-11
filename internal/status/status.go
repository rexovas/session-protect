package status

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rexovas/session-protect/internal/plan"
	"github.com/rexovas/session-protect/internal/targets"
)

type Status struct {
	ConfigPath string         `json:"config_path"`
	BackupRoot string         `json:"backup_root"`
	Topology   string         `json:"topology"`
	Repos      []RepoStatus   `json:"repos"`
	Targets    []TargetStatus `json:"targets"`
}

type RepoStatus struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Detected   bool   `json:"detected"`
	Clean      bool   `json:"clean"`
	SizeBytes  int64  `json:"size_bytes"`
	LastCommit string `json:"last_commit,omitempty"`
}

type TargetStatus struct {
	Name         string `json:"name"`
	Source       string `json:"source"`
	Detected     bool   `json:"detected"`
	Mode         string `json:"mode"`
	SizeBytes    int64  `json:"size_bytes"`
	SessionCount int    `json:"session_count"`
	BackupRepo   string `json:"backup_repo,omitempty"`
}

func Build() Status {
	p := plan.Build()
	repos := detectRepos(p.BackupRoot)
	return Status{
		ConfigPath: p.ConfigPath,
		BackupRoot: p.BackupRoot,
		Topology:   p.Topology,
		Repos:      repos,
		Targets:    buildTargets(p.Targets, repos),
	}
}

func Print(out io.Writer, asJSON bool) int {
	s := Build()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(s); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(out, "SessionProtect status")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Config:   %s\n", s.ConfigPath)
	fmt.Fprintf(out, "Root:     %s\n", s.BackupRoot)
	fmt.Fprintf(out, "Topology: %s\n\n", s.Topology)

	fmt.Fprintln(out, "Repositories")
	for _, repo := range s.Repos {
		state := "missing"
		if repo.Detected {
			state = "detected"
		}
		clean := "unknown"
		if repo.Detected {
			clean = "dirty"
			if repo.Clean {
				clean = "clean"
			}
		}
		fmt.Fprintf(out, "  %-8s %-8s %-7s %s\n", repo.Name, repo.Kind, state, repo.Path)
		if repo.Detected {
			fmt.Fprintf(out, "    size: %s\n", formatBytes(repo.SizeBytes))
			fmt.Fprintf(out, "    git:  %s\n", clean)
			if repo.LastCommit != "" {
				fmt.Fprintf(out, "    last: %s\n", repo.LastCommit)
			}
		}
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Targets")
	for _, target := range s.Targets {
		state := "missing"
		if target.Detected {
			state = "detected"
		}
		fmt.Fprintf(out, "  %-8s %-8s %s\n", target.Name, state, target.Source)
		fmt.Fprintf(out, "    mode:     %s\n", target.Mode)
		if target.Detected {
			fmt.Fprintf(out, "    size:     %s\n", formatBytes(target.SizeBytes))
			fmt.Fprintf(out, "    sessions: %d\n", target.SessionCount)
		}
		if target.BackupRepo != "" {
			fmt.Fprintf(out, "    backup:   %s\n", target.BackupRepo)
		}
	}

	return 0
}

func detectRepos(backupRoot string) []RepoStatus {
	home, _ := os.UserHomeDir()
	candidates := []RepoStatus{
		{Name: "all", Kind: "planned", Path: filepath.Join(backupRoot, "all")},
		{Name: "claude", Kind: "legacy", Path: filepath.Join(home, "SessionBackups", "claude")},
		{Name: "codex", Kind: "legacy", Path: filepath.Join(home, "SessionBackups", "codex")},
	}
	for i := range candidates {
		fillRepo(&candidates[i])
	}
	return candidates
}

func fillRepo(repo *RepoStatus) {
	if !isGitRepo(repo.Path) {
		return
	}
	repo.Detected = true
	repo.SizeBytes = dirSize(repo.Path)
	repo.Clean = gitClean(repo.Path)
	repo.LastCommit = gitLastCommit(repo.Path)
}

func buildTargets(detected []targets.Target, repos []RepoStatus) []TargetStatus {
	backupByName := map[string]string{}
	for _, repo := range repos {
		if repo.Detected {
			backupByName[repo.Name] = repo.Path
		}
	}

	statuses := make([]TargetStatus, 0, len(detected))
	for _, target := range detected {
		status := TargetStatus{
			Name:       target.Name,
			Source:     target.Source,
			Detected:   target.Detected,
			Mode:       target.Mode,
			BackupRepo: backupByName[target.Name],
		}
		if status.BackupRepo == "" {
			status.BackupRepo = backupByName["all"]
		}
		if target.Detected {
			status.SizeBytes = dirSize(target.Source)
			status.SessionCount = sessionCount(target)
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func sessionCount(target targets.Target) int {
	switch target.Name {
	case "claude":
		return countFiles(filepath.Join(target.Source, "projects"), ".jsonl")
	case "codex":
		return countFiles(filepath.Join(target.Source, "sessions"), ".jsonl")
	default:
		return 0
	}
}

func countFiles(root string, suffix string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), suffix) {
			count++
		}
		return nil
	})
	return count
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func gitClean(path string) bool {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	return err == nil && len(out) == 0
}

func gitLastCommit(path string) string {
	out, err := exec.Command("git", "-C", path, "log", "-1", "--format=%h %cs %s").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
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
