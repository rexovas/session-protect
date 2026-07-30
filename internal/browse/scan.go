package browse

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

// Session is one session file, present in the live source, the backup, or
// both.
type Session struct {
	Target         string
	ID             string
	Title          string // first prompt of the session, from agent history
	State          string // OK | STALE_BACKUP | MISSING_BACKUP | MISSING_SOURCE
	Modified       time.Time
	BackupModified time.Time
	Size           int64
	SourcePath     string
	BackupPath     string
}

// Project groups sessions that belong to one working directory.
type Project struct {
	Path        string // real project path when recoverable, else the slug
	Slug        string
	Sessions    []Session
	Latest      time.Time
	SizeBytes   int64
	OK          int
	Stale       int
	Unbacked    int // live sessions with no backup copy
	RecoverOnly int // backed-up sessions missing from the live source
	ClaudeCount int
	CodexCount  int
}

// Scan builds the global project/session inventory for both targets, merging
// live sources with backup mirrors so deleted-but-recoverable sessions stay
// visible.
func Scan(cfg config.Config) []*Project {
	byPath := map[string]*Project{}

	scanClaude(cfg, byPath)
	scanCodex(cfg, byPath)

	titles := historyTitles()
	projects := make([]*Project, 0, len(byPath))
	for _, project := range byPath {
		for i := range project.Sessions {
			project.Sessions[i].Title = titles[project.Sessions[i].ID]
		}
		sort.Slice(project.Sessions, func(i, j int) bool {
			return newest(project.Sessions[i]).After(newest(project.Sessions[j]))
		})
		for _, session := range project.Sessions {
			if latest := newest(session); latest.After(project.Latest) {
				project.Latest = latest
			}
			project.SizeBytes += session.Size
			switch session.State {
			case "OK":
				project.OK++
			case "STALE_BACKUP":
				project.Stale++
			case "MISSING_BACKUP":
				project.Unbacked++
			case "MISSING_SOURCE":
				project.RecoverOnly++
			}
			if session.Target == "claude" {
				project.ClaudeCount++
			} else {
				project.CodexCount++
			}
		}
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Latest.After(projects[j].Latest) })
	return projects
}

func scanClaude(cfg config.Config, byPath map[string]*Project) {
	target := targets.DetectClaude()
	repo, prefix := cfg.RepoFor("claude")
	sourceRoot := filepath.Join(target.Source, "projects")
	backupRoot := filepath.Join(repo, prefix, "projects")

	slugs := map[string]bool{}
	for _, root := range []string{sourceRoot, backupRoot} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				slugs[entry.Name()] = true
			}
		}
	}

	for slug := range slugs {
		source := listJSONL(filepath.Join(sourceRoot, slug))
		backup := listJSONL(filepath.Join(backupRoot, slug))
		sessions := merge("claude", source, backup)
		if len(sessions) == 0 {
			continue
		}
		path := claudeProjectPath(sessions)
		if path == "" {
			path = slug
		}
		project := byPath[path]
		if project == nil {
			project = &Project{Path: path, Slug: slug}
			byPath[path] = project
		}
		project.Slug = slug
		project.Sessions = append(project.Sessions, sessions...)
	}
}

func scanCodex(cfg config.Config, byPath map[string]*Project) {
	target := targets.DetectCodex()
	repo, prefix := cfg.RepoFor("codex")
	sourceRoot := filepath.Join(target.Source, "sessions")
	backupRoot := filepath.Join(repo, prefix, "sessions")

	source := listCodex(sourceRoot)
	backup := listCodex(backupRoot)

	byProject := map[string][]fileInfo{}
	group := func(files []fileInfo) {
		for _, file := range files {
			byProject[file.Project] = append(byProject[file.Project], file)
		}
	}
	group(source)
	group(backup)

	for path := range byProject {
		var sourceFiles, backupFiles []fileInfo
		for _, file := range byProject[path] {
			if file.FromBackup {
				backupFiles = append(backupFiles, file)
			} else {
				sourceFiles = append(sourceFiles, file)
			}
		}
		sessions := merge("codex", sourceFiles, backupFiles)
		if len(sessions) == 0 {
			continue
		}
		project := byPath[path]
		if project == nil {
			project = &Project{Path: path}
			byPath[path] = project
		}
		project.Sessions = append(project.Sessions, sessions...)
	}

	_ = backupRoot // grouped via FromBackup flag above
}

type fileInfo struct {
	ID         string
	Path       string
	Project    string
	Size       int64
	ModTime    time.Time
	FromBackup bool
}

func merge(target string, source []fileInfo, backup []fileInfo) []Session {
	sourceByID := map[string]fileInfo{}
	backupByID := map[string]fileInfo{}
	for _, file := range source {
		sourceByID[file.ID] = file
	}
	for _, file := range backup {
		backupByID[file.ID] = file
	}

	var sessions []Session
	seen := map[string]bool{}
	handle := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		src, hasSource := sourceByID[id]
		bak, hasBackup := backupByID[id]
		session := Session{Target: target, ID: id}
		if hasSource {
			session.Modified = src.ModTime
			session.Size = src.Size
			session.SourcePath = src.Path
		}
		if hasBackup {
			session.BackupModified = bak.ModTime
			session.BackupPath = bak.Path
			if !hasSource {
				session.Size = bak.Size
			}
		}
		switch {
		case hasSource && hasBackup && src.ModTime.After(bak.ModTime.Add(time.Second)):
			session.State = "STALE_BACKUP"
		case hasSource && hasBackup:
			session.State = "OK"
		case hasSource:
			session.State = "MISSING_BACKUP"
		default:
			session.State = "MISSING_SOURCE"
		}
		sessions = append(sessions, session)
	}
	for id := range sourceByID {
		handle(id)
	}
	for id := range backupByID {
		handle(id)
	}
	return sessions
}

func listJSONL(root string) []fileInfo {
	var files []fileInfo
	entries, err := os.ReadDir(root)
	if err != nil {
		return files
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			ID:      strings.TrimSuffix(entry.Name(), ".jsonl"),
			Path:    filepath.Join(root, entry.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return files
}

func listCodex(root string) []fileInfo {
	var files []fileInfo
	fromBackup := strings.Contains(root, string(filepath.Separator)+"all"+string(filepath.Separator)) ||
		!strings.Contains(root, string(filepath.Separator)+".codex"+string(filepath.Separator))
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		id, cwd := codexMeta(path)
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		}
		if cwd == "" {
			cwd = "(unknown project)"
		}
		files = append(files, fileInfo{
			ID:         id,
			Path:       path,
			Project:    cwd,
			Size:       info.Size(),
			ModTime:    info.ModTime(),
			FromBackup: fromBackup,
		})
		return nil
	})
	return files
}

// historyTitles maps session ids to the first prompt recorded for them in the
// agents' history files — a cheap title source that avoids opening every
// session file.
func historyTitles() map[string]string {
	titles := map[string]string{}
	claude := targets.DetectClaude()
	codex := targets.DetectCodex()
	for _, path := range []string{
		filepath.Join(claude.Source, "history.jsonl"),
		filepath.Join(codex.Source, "history.jsonl"),
	} {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
		for scanner.Scan() {
			var entry struct {
				Display    string `json:"display"`
				SessionID  string `json:"sessionId"`
				Text       string `json:"text"`
				SessionID2 string `json:"session_id"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			id, text := entry.SessionID, entry.Display
			if id == "" {
				id, text = entry.SessionID2, entry.Text
			}
			if id == "" || text == "" {
				continue
			}
			if _, seen := titles[id]; !seen {
				titles[id] = strings.Join(strings.Fields(text), " ")
			}
		}
		file.Close()
	}
	return titles
}

// codexMeta pulls the session id and working directory from the first lines
// of a codex session file.
func codexMeta(path string) (id string, cwd string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for i := 0; i < 50 && scanner.Scan(); i++ {
		var event struct {
			Payload struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Payload.Cwd != "" {
			return event.Payload.ID, event.Payload.Cwd
		}
	}
	return "", ""
}

// claudeProjectPath recovers the real project path by reading the cwd field
// from the newest session file's first lines; slugs are not reversible.
func claudeProjectPath(sessions []Session) string {
	best := ""
	var bestTime time.Time
	for _, session := range sessions {
		path := session.SourcePath
		if path == "" {
			path = session.BackupPath
		}
		if path == "" || newest(session).Before(bestTime) {
			continue
		}
		if cwd := claudeCwd(path); cwd != "" {
			best = cwd
			bestTime = newest(session)
		}
	}
	return best
}

func claudeCwd(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for i := 0; i < 20 && scanner.Scan(); i++ {
		var event struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Cwd != "" {
			return event.Cwd
		}
	}
	return ""
}

func newest(session Session) time.Time {
	if session.Modified.After(session.BackupModified) {
		return session.Modified
	}
	return session.BackupModified
}
