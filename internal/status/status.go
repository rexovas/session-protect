package status

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/human"
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
	LastBackup string `json:"last_backup,omitempty"`
}

type TargetStatus struct {
	Name         string `json:"name"`
	Source       string `json:"source"`
	Detected     bool   `json:"detected"`
	Mode         string `json:"mode"`
	SizeBytes    int64  `json:"size_bytes"`
	SessionCount int    `json:"session_count"`
	LastModified string `json:"last_modified,omitempty"`
	BackupRepo   string `json:"backup_repo,omitempty"`
}

func Build() Status {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Defaults()
	}
	p := plan.Build()
	repos := detectRepos(cfg)
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

	fmt.Fprintln(out, "SessionProtect")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Config      %s\n", s.ConfigPath)
	fmt.Fprintf(out, "Root        %s\n", s.BackupRoot)
	fmt.Fprintf(out, "Topology    %s\n\n", s.Topology)

	fmt.Fprintln(out, "Targets")
	for _, target := range s.Targets {
		state := "missing"
		if target.Detected {
			state = "detected"
		}
		repo := findRepo(s.Repos, target.BackupRepo)
		repoState := "missing"
		if repo != nil && repo.Detected {
			repoState = "clean"
			if !repo.Clean {
				repoState = "dirty"
			}
		}
		fmt.Fprintf(out, "  %-8s source:%-8s backup:%s\n", target.Name, state, repoState)
		if target.LastModified != "" {
			fmt.Fprintf(out, "    source modified  %s\n", target.LastModified)
		}
		if repo != nil && repo.LastBackup != "" {
			fmt.Fprintf(out, "    last backup      %s\n", repo.LastBackup)
		}
		if target.Detected {
			fmt.Fprintf(out, "    sessions         %d\n", target.SessionCount)
			fmt.Fprintf(out, "    source size      %s\n", human.Bytes(target.SizeBytes))
		}
		if repo != nil && repo.Detected {
			fmt.Fprintf(out, "    backup size      %s\n", human.Bytes(repo.SizeBytes))
			fmt.Fprintf(out, "    backup path      %s\n", repo.Path)
		} else if target.BackupRepo != "" {
			fmt.Fprintf(out, "    backup path      %s\n", target.BackupRepo)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Use `session-protect project status` for current-project session details.")

	return 0
}

func detectRepos(cfg config.Config) []RepoStatus {
	var candidates []RepoStatus
	if cfg.Topology == "per-target" {
		for _, name := range []string{"claude", "codex"} {
			candidates = append(candidates, RepoStatus{Name: name, Kind: "managed", Path: filepath.Join(cfg.BackupRoot, name)})
		}
	} else {
		candidates = append(candidates, RepoStatus{Name: "all", Kind: "managed", Path: filepath.Join(cfg.BackupRoot, "all")})
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
	repo.LastBackup = gitLastCommitDate(repo.Path)
}

func buildTargets(detected []targets.Target, repos []RepoStatus) []TargetStatus {
	managedByName := map[string]string{}
	for _, repo := range repos {
		if repo.Detected {
			managedByName[repo.Name] = repo.Path
		}
	}

	statuses := make([]TargetStatus, 0, len(detected))
	for _, target := range detected {
		status := TargetStatus{
			Name:     target.Name,
			Source:   target.Source,
			Detected: target.Detected,
			Mode:     target.Mode,
		}
		status.BackupRepo = managedByName[target.Name]
		if status.BackupRepo == "" {
			status.BackupRepo = managedByName["all"]
		}
		if target.Detected {
			status.SizeBytes = dirSize(target.Source)
			status.SessionCount = sessionCount(target)
			status.LastModified = human.Time(latestModTime(target.Source))
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func findRepo(repos []RepoStatus, path string) *RepoStatus {
	for i := range repos {
		if repos[i].Path == path {
			return &repos[i]
		}
	}
	return nil
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

func gitLastCommitDate(path string) string {
	out, err := exec.Command("git", "-C", path, "log", "-1", "--format=%cI").Output()
	if err != nil {
		return ""
	}
	return formatRFC3339(strings.TrimSpace(string(out)))
}

func latestModTime(root string) time.Time {
	var latest time.Time
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest
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

func formatRFC3339(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return human.Time(t)
}
