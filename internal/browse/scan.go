package browse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/guard"
	"github.com/rexovas/session-protect/internal/targets"
)

// Session is one session file, present in the live source, the backup, or
// both.
type Session struct {
	Target         string
	ID             string
	Title          string // first prompt of the session, from agent history
	CustomName     string // user-assigned name, from custom-title events
	LiveStatus     string // non-empty when open in a running agent process
	ProjectPath    string // set on aggregated (AllUnder) sessions for display
	State          string // OK | STALE_BACKUP | MISSING_BACKUP | MISSING_SOURCE
	Modified       time.Time
	BackupModified time.Time
	Size           int64
	SourcePath     string
	BackupPath     string
}

// Project groups sessions that belong to one working directory.
type Project struct {
	NamesLoaded bool   // custom names are loaded lazily on first open
	Path        string // real project path when recoverable, else the slug
	Slug        string
	Sessions    []Session
	Latest      time.Time
	SizeBytes   int64
	OK          int
	Stale       int
	Unbacked    int // live sessions with no backup copy
	RecoverOnly int // backed-up sessions missing from the live source
	Open        int // sessions currently open in a running agent process
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
	open := guard.Live(guard.RegistryDir())
	projects := make([]*Project, 0, len(byPath))
	for _, project := range byPath {
		for i := range project.Sessions {
			session := &project.Sessions[i]
			session.Title = titles[session.ID]
			if info, ok := open[session.ID]; ok {
				session.LiveStatus = info.Status
				project.Open++
			}
			// An open session with fresh writes is expected to run ahead
			// of its mirror until the next turn-end sync; that is
			// activity, not staleness. The mtime GAP between live and
			// mirror is not the right signal — the mirror keeps source
			// mtimes, so an idle-then-resumed session shows an hours-wide
			// gap seconds after its first new write. Recency of the
			// latest write is: recent unsynced writes are normal, old
			// unsynced writes mean a sync was missed.
			if session.State == "STALE_BACKUP" && session.LiveStatus != "" &&
				time.Since(session.Modified) < 30*time.Minute {
				session.State = "ACTIVE"
			}
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
			case "OK", "ACTIVE": // active counts as protected; ▶ already shows it
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

// Detail is on-demand deep information about one session, loaded when the
// user inspects it.
type Detail struct {
	Created      time.Time
	FirstPrompt  string
	LastPrompt   string
	LastResponse string
	Tokens       TokenTotals
	Messages     int
	Models       []string
}

// TokenTotals sums per-message usage across a session.
type TokenTotals struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

func (t TokenTotals) Zero() bool {
	return t.Input == 0 && t.Output == 0 && t.CacheRead == 0 && t.CacheWrite == 0
}

// LoadDetail streams the whole session file once: creation time and first
// prompt from the head, latest prompt/response as it goes, and token usage
// summed from assistant messages. Formats vary per agent and version, so
// extraction is best-effort.
func LoadDetail(session Session) Detail {
	var detail Detail
	path := session.SourcePath
	if path == "" {
		path = session.BackupPath
	}
	if path == "" {
		return detail
	}

	file, err := os.Open(path)
	if err != nil {
		return detail
	}
	defer file.Close()

	models := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var event transcriptLine
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if detail.Created.IsZero() && event.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
				detail.Created = t
			}
		}
		switch event.Type {
		case "user":
			detail.Messages++
			if text := contentText(event.Message.Content); text != "" {
				if detail.FirstPrompt == "" {
					detail.FirstPrompt = text
				}
				detail.LastPrompt = text
			}
		case "assistant":
			detail.Messages++
			if text := contentText(event.Message.Content); text != "" {
				detail.LastResponse = text
			}
			usage := event.Message.Usage
			detail.Tokens.Input += usage.InputTokens
			detail.Tokens.Output += usage.OutputTokens
			detail.Tokens.CacheRead += usage.CacheReadInputTokens
			detail.Tokens.CacheWrite += usage.CacheCreationInputTokens
			if event.Message.Model != "" {
				models[event.Message.Model] = true
			}
		}
	}
	for model := range models {
		detail.Models = append(detail.Models, model)
	}
	sort.Strings(detail.Models)
	return detail
}

type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// contentText extracts human text from a message content field that may be a
// plain string or a list of typed blocks. Tool results and system-tagged
// content are skipped.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return cleanText(plain)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return cleanText(strings.Join(parts, " "))
}

func cleanText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if strings.HasPrefix(s, "<") {
		return "" // system/command envelope, not a human message
	}
	return s
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

// Folder is one child directory of the current view root, aggregating every
// session anywhere beneath it.
type Folder struct {
	Name        string
	Path        string
	Pseudo      bool // unresolved project key, not a real filesystem path
	Sessions    int
	SizeBytes   int64
	Latest      time.Time
	Open        int
	Stale       int
	Unbacked    int
	RecoverOnly int
}

// ChildrenOf groups projects under root into immediate child folders with
// recursive aggregates. Projects whose key is not an absolute path (rare:
// unrecoverable cwd) surface as pseudo-folders only at the start root.
func ChildrenOf(projects []*Project, root string, start string) []Folder {
	byPath := map[string]*Folder{}
	for _, project := range projects {
		if !filepath.IsAbs(project.Path) {
			if root == start {
				folder := &Folder{Name: project.Path, Path: project.Path, Pseudo: true}
				aggregate(folder, project)
				byPath[project.Path] = folder
			}
			continue
		}
		if project.Path == root {
			continue
		}
		rel, err := filepath.Rel(root, project.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		name := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		childPath := filepath.Join(root, name)
		folder := byPath[childPath]
		if folder == nil {
			folder = &Folder{Name: name, Path: childPath}
			byPath[childPath] = folder
		}
		aggregate(folder, project)
	}

	folders := make([]Folder, 0, len(byPath))
	for _, folder := range byPath {
		folders = append(folders, *folder)
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Latest.After(folders[j].Latest) })
	return folders
}

func aggregate(folder *Folder, project *Project) {
	folder.Sessions += len(project.Sessions)
	folder.SizeBytes += project.SizeBytes
	if project.Latest.After(folder.Latest) {
		folder.Latest = project.Latest
	}
	folder.Open += project.Open
	folder.Stale += project.Stale
	folder.Unbacked += project.Unbacked
	folder.RecoverOnly += project.RecoverOnly
}

// AllUnder returns every session at or beneath root, newest first, paired
// with its project path for display.
func AllUnder(projects []*Project, root string) []Session {
	var all []Session
	for _, project := range projects {
		if !filepath.IsAbs(project.Path) {
			continue
		}
		if project.Path != root {
			rel, err := filepath.Rel(root, project.Path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
		}
		for _, session := range project.Sessions {
			session.ProjectPath = project.Path
			all = append(all, session)
		}
	}
	sort.Slice(all, func(i, j int) bool { return newest(all[i]).After(newest(all[j])) })
	return all
}

// ProjectAt returns the project whose sessions live exactly at root.
func ProjectAt(projects []*Project, root string) *Project {
	for _, project := range projects {
		if project.Path == root {
			return project
		}
	}
	return nil
}

// NearestRoot climbs from start toward the filesystem root until some
// sessions exist at or beneath the directory, so launching from a
// session-free directory still shows something useful.
func NearestRoot(projects []*Project, start string) string {
	dir := start
	for {
		for _, project := range projects {
			if !filepath.IsAbs(project.Path) {
				continue
			}
			if project.Path == dir {
				return dir
			}
			rel, err := filepath.Rel(dir, project.Path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// LoadCustomNames fills in user-assigned session names by scanning each
// session file for custom-title events (appended when a session is renamed).
// It runs lazily per project because it reads whole files.
func LoadCustomNames(project *Project) {
	if project.NamesLoaded {
		return
	}
	project.NamesLoaded = true
	for i := range project.Sessions {
		session := &project.Sessions[i]
		path := session.SourcePath
		if path == "" {
			path = session.BackupPath
		}
		if path == "" {
			continue
		}
		if name := customTitle(path); name != "" {
			session.CustomName = name
		}
	}
}

// customTitle returns the last custom-title event in the file. A cheap byte
// pre-filter keeps this fast on large transcripts.
func customTitle(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	name := ""
	pattern := []byte(`"custom-title"`)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, pattern) {
			continue
		}
		var event struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "custom-title" && event.CustomTitle != "" {
			name = event.CustomTitle
		}
	}
	return name
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
