package project

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/human"
	"github.com/rexovas/session-protect/internal/targets"
)

type Status struct {
	ProjectPath string         `json:"project_path"`
	Targets     []TargetStatus `json:"targets"`
}

type TargetStatus struct {
	Name          string          `json:"name"`
	SourcePath    string          `json:"source_path"`
	BackupPath    string          `json:"backup_path"`
	SourceCount   int             `json:"source_count"`
	BackupCount   int             `json:"backup_count"`
	OKCount       int             `json:"ok_count"`
	MissingSource int             `json:"missing_source"`
	MissingBackup int             `json:"missing_backup"`
	StaleBackup   int             `json:"stale_backup"`
	LatestSource  string          `json:"latest_source,omitempty"`
	LatestBackup  string          `json:"latest_backup,omitempty"`
	Sessions      []SessionStatus `json:"sessions"`
}

type SessionStatus struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	SourceModified string `json:"source_modified,omitempty"`
	BackupModified string `json:"backup_modified,omitempty"`
	SourcePath     string `json:"source_path,omitempty"`
	BackupPath     string `json:"backup_path,omitempty"`
}

type sessionFile struct {
	ID      string
	Path    string
	ModTime time.Time
}

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}
	if args[0] != "status" {
		fmt.Fprintf(stderr, "unknown project command: %s\n\n", args[0])
		usage(stderr)
		return 2
	}
	path := ""
	asJSON := false
	for _, arg := range args[1:] {
		if arg == "--json" {
			asJSON = true
			continue
		}
		if path == "" {
			path = arg
			continue
		}
		fmt.Fprintf(stderr, "unexpected argument: %s\n", arg)
		return 2
	}
	status, err := Build(path)
	if err != nil {
		fmt.Fprintf(stderr, "project status failed: %v\n", err)
		return 1
	}
	return Print(stdout, status, asJSON)
}

func Build(path string) (Status, error) {
	projectPath, err := normalizeProjectPath(path)
	if err != nil {
		return Status{}, err
	}
	home, _ := os.UserHomeDir()
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Defaults()
	}
	return Status{
		ProjectPath: projectPath,
		Targets: []TargetStatus{
			claudeStatus(cfg, home, projectPath),
			codexStatus(cfg, home, projectPath),
		},
	}, nil
}

func Print(out io.Writer, status Status, asJSON bool) int {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(status); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(out, "Project session status")
	fmt.Fprintf(out, "Project  %s\n\n", status.ProjectPath)
	for _, target := range status.Targets {
		fmt.Fprintf(out, "%s\n", target.Name)
		fmt.Fprintf(out, "  source sessions   %d\n", target.SourceCount)
		fmt.Fprintf(out, "  backup sessions   %d\n", target.BackupCount)
		fmt.Fprintf(out, "  ok                %d\n", target.OKCount)
		fmt.Fprintf(out, "  missing backup    %d\n", target.MissingBackup)
		fmt.Fprintf(out, "  missing source    %d\n", target.MissingSource)
		fmt.Fprintf(out, "  stale backup      %d\n", target.StaleBackup)
		if target.LatestSource != "" {
			fmt.Fprintf(out, "  latest source     %s\n", target.LatestSource)
		}
		if target.LatestBackup != "" {
			fmt.Fprintf(out, "  latest backup     %s\n", target.LatestBackup)
		}
		if len(target.Sessions) > 0 {
			fmt.Fprintln(out, "  sessions")
			limit := len(target.Sessions)
			if limit > 12 {
				limit = 12
			}
			for _, session := range target.Sessions[:limit] {
				fmt.Fprintf(out, "    %-14s %s", session.State, session.ID)
				if session.SourceModified != "" {
					fmt.Fprintf(out, " source:%s", session.SourceModified)
				}
				if session.BackupModified != "" {
					fmt.Fprintf(out, " backup:%s", session.BackupModified)
				}
				fmt.Fprintln(out)
			}
			if len(target.Sessions) > limit {
				fmt.Fprintf(out, "    ... %d more\n", len(target.Sessions)-limit)
			}
		}
		fmt.Fprintln(out)
	}
	return 0
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect project status [path] [--json]")
}

func claudeStatus(cfg config.Config, home string, projectPath string) TargetStatus {
	slug := targets.ClaudeSlug(projectPath)
	source := filepath.Join(home, ".claude", "projects", slug)
	backup := backupDir(cfg, "claude", filepath.Join("projects", slug))
	return compareTarget("claude", source, backup, listJSONL(source), listJSONL(backup))
}

func codexStatus(cfg config.Config, home string, projectPath string) TargetStatus {
	source := filepath.Join(home, ".codex", "sessions")
	backup := backupDir(cfg, "codex", "sessions")
	return compareTarget("codex", source, backup, codexSessions(source, projectPath), codexSessions(backup, projectPath))
}

func backupDir(cfg config.Config, target string, rel string) string {
	repo, prefix := cfg.RepoFor(target)
	return filepath.Join(repo, prefix, rel)
}

func compareTarget(name string, sourcePath string, backupPath string, sourceFiles []sessionFile, backupFiles []sessionFile) TargetStatus {
	sourceByID := map[string]sessionFile{}
	backupByID := map[string]sessionFile{}
	for _, item := range sourceFiles {
		sourceByID[item.ID] = item
	}
	for _, item := range backupFiles {
		backupByID[item.ID] = item
	}

	ids := make([]string, 0, len(sourceByID)+len(backupByID))
	seen := map[string]bool{}
	for id := range sourceByID {
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range backupByID {
		if !seen[id] {
			ids = append(ids, id)
		}
	}

	sort.Slice(ids, func(i, j int) bool {
		return newest(sourceByID[ids[i]], backupByID[ids[i]]).After(newest(sourceByID[ids[j]], backupByID[ids[j]]))
	})

	status := TargetStatus{
		Name:        name,
		SourcePath:  sourcePath,
		BackupPath:  backupPath,
		SourceCount: len(sourceByID),
		BackupCount: len(backupByID),
	}
	for _, id := range ids {
		source, hasSource := sourceByID[id]
		backup, hasBackup := backupByID[id]
		session := SessionStatus{ID: id}
		if hasSource {
			session.SourcePath = source.Path
			session.SourceModified = human.Time(source.ModTime)
			if source.ModTime.After(parseDisplayTime(status.LatestSource)) {
				status.LatestSource = session.SourceModified
			}
		}
		if hasBackup {
			session.BackupPath = backup.Path
			session.BackupModified = human.Time(backup.ModTime)
			if backup.ModTime.After(parseDisplayTime(status.LatestBackup)) {
				status.LatestBackup = session.BackupModified
			}
		}
		switch {
		case hasSource && hasBackup && source.ModTime.After(backup.ModTime.Add(time.Second)):
			session.State = "STALE_BACKUP"
			status.StaleBackup++
		case hasSource && hasBackup:
			session.State = "OK"
			status.OKCount++
		case hasSource:
			session.State = "MISSING_BACKUP"
			status.MissingBackup++
		default:
			session.State = "MISSING_SOURCE"
			status.MissingSource++
		}
		status.Sessions = append(status.Sessions, session)
	}
	return status
}

func normalizeProjectPath(path string) (string, error) {
	if path == "" {
		return os.Getwd()
	}
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(wd, path)
	}
	return filepath.Clean(path), nil
}

func listJSONL(root string) []sessionFile {
	var files []sessionFile
	entries, err := os.ReadDir(root)
	if err != nil {
		return files
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			path := filepath.Join(root, entry.Name())
			files = append(files, sessionFile{ID: strings.TrimSuffix(entry.Name(), ".jsonl"), Path: path, ModTime: info.ModTime()})
		}
	}
	return files
}

func codexSessions(root string, projectPath string) []sessionFile {
	var files []sessionFile
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		id, ok := codexSessionIDForProject(path, projectPath)
		if !ok {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			files = append(files, sessionFile{ID: id, Path: path, ModTime: info.ModTime()})
		}
		return nil
	})
	return files
}

func codexSessionIDForProject(path string, projectPath string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for i := 0; i < 50 && scanner.Scan(); i++ {
		var event struct {
			Type    string `json:"type"`
			Payload struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Payload.Cwd == projectPath {
			id := event.Payload.ID
			if id == "" {
				id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
			}
			return id, true
		}
	}
	return "", false
}

func newest(a sessionFile, b sessionFile) time.Time {
	if a.ModTime.After(b.ModTime) {
		return a.ModTime
	}
	return b.ModTime
}

func parseDisplayTime(value string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04:05", value, time.Local)
	return t
}
