package browse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/guard"
	"github.com/rexovas/session-protect/internal/targets"
)

// Session is one session file, present in the live source, the backup, or
// both.
type Session struct {
	Target         string
	ID             string
	Title          string    // first prompt of the session, from agent history
	CustomName     string    // user-assigned name, from custom-title events
	LiveStatus     string    // non-empty when open in a running agent process
	LivePID        int       // process holding the session open, for jumping to it
	ProjectPath    string    // the session's working directory (resume cd target)
	Prompts        int       // for LOST sessions: prompt count from history
	LastModel      string    // most recent model seen in the transcript
	State          string    // OK | STALE_BACKUP | MISSING_BACKUP | MISSING_SOURCE | LOST (+ synthesized ACTIVE | OPEN | RESTORED)
	RestoredAt     time.Time // last restore recorded in the audit log, badge or not
	RebuiltFrom    string    // lost session this one was reconstructed from
	RebuiltAs      []string  // for LOST sessions: its reconstructions, newest first
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
	Lost        int // known only from prompt history; no transcript anywhere
	Open        int // sessions currently open in a running agent process
	Active      int // open sessions with fresh unsynced writes (mid-turn)
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

	// Sessions known only from the prompt history — lost from both live
	// and backup — surface as permanent LOST entries. The history file is
	// never modified; reconstruction (if ever) creates a new session.
	seen := map[string]bool{}
	for _, project := range byPath {
		for _, session := range project.Sessions {
			seen[session.ID] = true
		}
	}
	for id, ghost := range claudeHistorySessions() {
		if seen[id] || ghost.Project == "" {
			continue
		}
		project := byPath[ghost.Project]
		if project == nil {
			project = &Project{Path: ghost.Project}
			byPath[ghost.Project] = project
		}
		project.Sessions = append(project.Sessions, Session{
			Target:      "claude",
			ID:          id,
			Title:       ghost.Title,
			State:       "LOST",
			Modified:    ghost.Last,
			Prompts:     ghost.Count,
			ProjectPath: ghost.Project,
		})
	}
	// Codex history records no project path, so its lost sessions
	// surface under one pseudo-folder at the start root.
	for id, ghost := range codexHistorySessions() {
		if seen[id] {
			continue
		}
		const bucket = "codex · lost sessions"
		project := byPath[bucket]
		if project == nil {
			project = &Project{Path: bucket}
			byPath[bucket] = project
		}
		project.Sessions = append(project.Sessions, Session{
			Target:   "codex",
			ID:       id,
			Title:    ghost.Title,
			State:    "LOST",
			Modified: ghost.Last,
			Prompts:  ghost.Count,
		})
	}

	titles := historyTitles()
	open := guard.Live(guard.RegistryDir())
	codexIDs, codexCwds := guard.LiveCodex()
	if codexIDs == nil {
		codexIDs = map[string]string{}
	}
	for cwd, pid := range codexCwds {
		project := byPath[cwd]
		if project == nil {
			continue
		}
		bestID, bestTime := "", time.Time{}
		for _, session := range project.Sessions {
			if session.Target == "codex" && session.Modified.After(bestTime) {
				bestID, bestTime = session.ID, session.Modified
			}
		}
		if bestID != "" {
			codexIDs[bestID] = pid
		}
	}
	restoredAt := map[string]time.Time{}
	rebuiltFrom := map[string]string{}
	rebuiltInto := map[string][]string{}
	for _, entry := range audit.Read(cfg.BackupRoot) {
		if entry.Action == "restore" && entry.SessionID != "" && entry.Time.After(restoredAt[entry.SessionID]) {
			restoredAt[entry.SessionID] = entry.Time
		}
		if (entry.Action == "reconstruct" || entry.Action == "reconstruct-ai") && entry.SessionID != "" {
			rebuiltFrom[entry.SessionID] = entry.From
			if entry.From != "" {
				// Audit order is chronological; prepend for newest first.
				rebuiltInto[entry.From] = append([]string{entry.SessionID}, rebuiltInto[entry.From]...)
			}
		}
	}
	projects := make([]*Project, 0, len(byPath))
	for _, project := range byPath {
		for i := range project.Sessions {
			session := &project.Sessions[i]
			session.Title = titles[session.ID]
			// Every session knows its project. Resume, the inspector, and
			// rescue destinations all read this; leaving it to the
			// aggregated view meant folder-view sessions fell back to the
			// browse root and resumed in the wrong directory.
			if session.ProjectPath == "" && filepath.IsAbs(project.Path) {
				session.ProjectPath = project.Path
			}
			if info, ok := open[session.ID]; ok {
				session.LiveStatus = info.Status
				session.LivePID = info.PID
				project.Open++
			} else if pid, ok := codexIDs[session.ID]; ok {
				session.LiveStatus = "open"
				session.LivePID, _ = strconv.Atoi(pid)
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
			// Open but idle (and fully synced): its own state, distinct
			// from actively-writing and from closed-and-ok.
			if session.State == "OK" && session.LiveStatus != "" {
				session.State = "OPEN"
			}
			// Came back from the dead: badge until the session sees new
			// live writes, after which it is just a normal session again.
			// The audit log keeps the permanent record either way.
			if when, ok := restoredAt[session.ID]; ok {
				session.RestoredAt = when
				if session.State == "OK" && !session.Modified.After(when) {
					session.State = "RESTORED"
				}
			}
			// Reconstructed sessions carry their identity permanently:
			// the badge shows whenever the session is otherwise quiet.
			if from, ok := rebuiltFrom[session.ID]; ok {
				session.RebuiltFrom = from
				if session.State == "OK" || session.State == "MISSING_BACKUP" {
					session.State = "REBUILT"
				}
				// A reconstruction never appears in the agent's prompt
				// history, so it has no title of its own — inherit the
				// original's, which opens with the same first prompt.
				if session.Title == "" {
					session.Title = titles[from]
				}
			}
			// The original stays lost forever, but its row should say its
			// reconstructions exist — rescued, not silently still-dead.
			// The audit log alone is not enough: a deleted rebuild must
			// not leave a rescued label pointing at nothing, so each
			// target has to still be present in live or backup.
			if session.State == "LOST" {
				for _, into := range rebuiltInto[session.ID] {
					if seen[into] {
						session.RebuiltAs = append(session.RebuiltAs, into)
					}
				}
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
			case "OK", "ACTIVE", "OPEN", "RESTORED", "REBUILT": // protected states
				project.OK++
				if session.State == "ACTIVE" {
					project.Active++
				}
			case "STALE_BACKUP":
				project.Stale++
			case "MISSING_BACKUP":
				project.Unbacked++
			case "MISSING_SOURCE":
				project.RecoverOnly++
			case "LOST":
				project.Lost++
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
		path := claudeProjectPath(sessions, slug)
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
		id, cwd := targets.CodexSessionMeta(path)
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
	Created        time.Time
	FirstPrompt    string
	LastPrompt     string
	LastResponse   string
	Tokens         TokenTotals
	PerModel       map[string]TokenTotals
	Messages       int
	Models         []string
	Compactions    int
	LastCompact    string // trigger of the most recent compaction
	LastCompactPre int64  // context tokens at that compaction
	// Transcript holds the most recent text messages for the tail viewer;
	// TranscriptTotal is how many the whole session has, so the viewer
	// knows when older messages exist beyond the loaded window.
	Transcript      []TranscriptMsg
	TranscriptTotal int
}

type TranscriptMsg struct {
	Role string // user | assistant
	Text string
}

// transcriptKeep bounds how much of the tail the inspector loads at
// first; scrolling past the top reloads with a larger window.
const transcriptKeep = 300

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
	return LoadDetailKeep(session, transcriptKeep)
}

// LoadDetailKeep loads detail keeping up to keep transcript messages.
func LoadDetailKeep(session Session, keep int) Detail {
	if session.Target == "codex" {
		return loadCodexDetail(session, keep)
	}
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

	detail.PerModel = map[string]TokenTotals{}
	appendMsg := func(role string, text string) {
		detail.TranscriptTotal++
		detail.Transcript = append(detail.Transcript, TranscriptMsg{Role: role, Text: text})
		if len(detail.Transcript) > keep {
			detail.Transcript = detail.Transcript[1:]
		}
	}
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
		case "system":
			if event.Subtype == "compact_boundary" {
				detail.Compactions++
				detail.LastCompact = event.CompactMetadata.Trigger
				detail.LastCompactPre = event.CompactMetadata.PreTokens
				marker := event.CompactMetadata.Trigger
				if event.CompactMetadata.PreTokens > 0 {
					marker += " · " + humanTokens(event.CompactMetadata.PreTokens) + " tokens"
				}
				appendMsg("compact", marker)
			}
		case "user":
			detail.Messages++
			for _, result := range toolResults(event.Message.Content) {
				appendMsg("result", result)
			}
			if text := contentRaw(event.Message.Content); text != "" {
				if event.IsCompactSummary {
					// Machine-written continuation summary, not a human prompt.
					appendMsg("summary", text)
					break
				}
				if detail.FirstPrompt == "" {
					detail.FirstPrompt = text
				}
				detail.LastPrompt = text
				appendMsg("user", text)
			}
		case "assistant":
			detail.Messages++
			if text := contentRaw(event.Message.Content); text != "" {
				detail.LastResponse = text
				appendMsg("assistant", text)
			}
			for _, name := range toolUses(event.Message.Content) {
				appendMsg("tool", name)
			}
			usage := event.Message.Usage
			detail.Tokens.Input += usage.InputTokens
			detail.Tokens.Output += usage.OutputTokens
			detail.Tokens.CacheRead += usage.CacheReadInputTokens
			detail.Tokens.CacheWrite += usage.CacheCreationInputTokens
			// Harness-generated lines (model "<synthetic>") carry no usage
			// and are not real models — only register models that billed.
			hasUsage := usage.InputTokens+usage.OutputTokens+
				usage.CacheReadInputTokens+usage.CacheCreationInputTokens > 0
			if event.Message.Model != "" && hasUsage {
				perModel := detail.PerModel[event.Message.Model]
				perModel.Input += usage.InputTokens
				perModel.Output += usage.OutputTokens
				perModel.CacheRead += usage.CacheReadInputTokens
				perModel.CacheWrite += usage.CacheCreationInputTokens
				detail.PerModel[event.Message.Model] = perModel
			}
		}
	}
	for model := range detail.PerModel {
		detail.Models = append(detail.Models, model)
	}
	sort.Strings(detail.Models)
	return detail
}

type transcriptLine struct {
	Type             string `json:"type"`
	Subtype          string `json:"subtype"`
	Timestamp        string `json:"timestamp"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	CompactMetadata  struct {
		Trigger   string `json:"trigger"`
		PreTokens int64  `json:"preTokens"`
	} `json:"compactMetadata"`
	Message struct {
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
// codexLine is one line of a codex rollout file: a typed envelope with
// the details in payload.
type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// loadCodexDetail streams a codex rollout: user/agent messages and tool
// calls into the transcript, model from turn_context, cumulative usage
// from the last token_count event.
func loadCodexDetail(session Session, keep int) Detail {
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

	detail.PerModel = map[string]TokenTotals{}
	appendMsg := func(role string, text string) {
		detail.TranscriptTotal++
		detail.Transcript = append(detail.Transcript, TranscriptMsg{Role: role, Text: text})
		if len(detail.Transcript) > keep {
			detail.Transcript = detail.Transcript[1:]
		}
	}
	model := ""
	var totals struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var line codexLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if detail.Created.IsZero() && line.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, line.Timestamp); err == nil {
				detail.Created = t
			}
		}
		switch line.Type {
		case "turn_context":
			var ctx struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(line.Payload, &ctx) == nil && ctx.Model != "" && ctx.Model != model {
				model = ctx.Model
				known := false
				for _, m := range detail.Models {
					if m == model {
						known = true
					}
				}
				if !known {
					detail.Models = append(detail.Models, model)
				}
			}
		case "compacted":
			detail.Compactions++
			appendMsg("compact", "auto")
		case "event_msg":
			var event struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Info    struct {
					Total struct {
						Input  int64 `json:"input_tokens"`
						Cached int64 `json:"cached_input_tokens"`
						Output int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			}
			if json.Unmarshal(line.Payload, &event) != nil {
				continue
			}
			switch event.Type {
			case "user_message":
				if event.Message == "" {
					continue
				}
				detail.Messages++
				if detail.FirstPrompt == "" {
					detail.FirstPrompt = event.Message
				}
				detail.LastPrompt = event.Message
				appendMsg("user", event.Message)
			case "agent_message":
				if event.Message == "" {
					continue
				}
				detail.Messages++
				detail.LastResponse = event.Message
				appendMsg("assistant", event.Message)
			case "token_count":
				// Cumulative — the last one is the session total.
				totals.Input = event.Info.Total.Input
				totals.Cached = event.Info.Total.Cached
				totals.Output = event.Info.Total.Output
			}
		case "response_item":
			var item struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
				Output    string `json:"output"`
			}
			if json.Unmarshal(line.Payload, &item) != nil {
				continue
			}
			switch item.Type {
			case "function_call", "custom_tool_call":
				name := item.Name
				var args struct {
					Cmd string `json:"cmd"`
				}
				if json.Unmarshal([]byte(item.Arguments), &args) == nil && args.Cmd != "" {
					name += "(" + truncate(cleanText(args.Cmd), 80) + ")"
				}
				appendMsg("tool", name)
			case "function_call_output", "custom_tool_call_output":
				if line := firstLine(item.Output); line != "" {
					appendMsg("result", truncate(line, 120))
				}
			}
		}
	}

	// input_tokens includes the cached portion; split it out so the usage
	// table reads like the claude one.
	detail.Tokens = TokenTotals{
		Input:     max64(0, totals.Input-totals.Cached),
		CacheRead: totals.Cached,
		Output:    totals.Output,
	}
	if model != "" {
		detail.PerModel[model] = detail.Tokens
		if len(detail.Models) == 0 {
			detail.Models = []string{model}
		}
	}
	return detail
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func max64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

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

// toolUses lists tool invocations (name plus a short input detail) from a
// message's content blocks, for the "⏺ Bash: …" lines in the tail view.
func toolUses(raw json.RawMessage) []string {
	var blocks []struct {
		Type  string         `json:"type"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var uses []string
	for _, block := range blocks {
		if block.Type != "tool_use" || block.Name == "" {
			continue
		}
		use := block.Name
		if detail := toolDetail(block.Input); detail != "" {
			use += ": " + detail
		}
		uses = append(uses, use)
	}
	return uses
}

// toolResults extracts a one-line summary from each tool_result block in a
// user event, for the "⎿ …" lines under tool calls.
func toolResults(raw json.RawMessage) []string {
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var results []string
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		text := ""
		var plain string
		if json.Unmarshal(block.Content, &plain) == nil {
			text = plain
		} else {
			var inner []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(block.Content, &inner) == nil {
				for _, part := range inner {
					if part.Type == "text" && part.Text != "" {
						text = part.Text
						break
					}
				}
			}
		}
		if line, _, _ := strings.Cut(strings.TrimSpace(text), "\n"); line != "" {
			results = append(results, strings.Join(strings.Fields(line), " "))
		}
	}
	return results
}

// toolDetail picks the most informative input field for display.
func toolDetail(input map[string]any) string {
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt", "skill"} {
		if value, ok := input[key].(string); ok && value != "" {
			return strings.Join(strings.Fields(value), " ")
		}
	}
	return ""
}

// contentRaw extracts message text preserving newlines and markdown
// structure, for transcript rendering.
func contentRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		plain = strings.TrimSpace(plain)
		if strings.HasPrefix(plain, "<") {
			return ""
		}
		return plain
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
		text := strings.TrimSpace(block.Text)
		if block.Type == "text" && text != "" && !strings.HasPrefix(text, "<") {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func cleanText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if strings.HasPrefix(s, "<") {
		return "" // system/command envelope, not a human message
	}
	return s
}

// lostInfo summarizes one session's prompt history.
type lostInfo struct {
	Project string
	Title   string
	Count   int
	First   time.Time
	Last    time.Time
}

// claudeHistorySessions indexes the claude prompt history by session id.
// This is the only record of sessions whose transcripts were pruned before
// any backup existed.
// codexHistorySessions reads codex prompt history: session id, unix
// seconds, and prompt text per line.
func codexHistorySessions() map[string]lostInfo {
	sessions := map[string]lostInfo{}
	file, err := os.Open(filepath.Join(targets.DetectCodex().Source, "history.jsonl"))
	if err != nil {
		return sessions
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry struct {
			SessionID string `json:"session_id"`
			Ts        int64  `json:"ts"`
			Text      string `json:"text"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.SessionID == "" {
			continue
		}
		info := sessions[entry.SessionID]
		info.Count++
		if info.Title == "" {
			info.Title = strings.Join(strings.Fields(entry.Text), " ")
		}
		at := time.Unix(entry.Ts, 0)
		if info.First.IsZero() || at.Before(info.First) {
			info.First = at
		}
		if at.After(info.Last) {
			info.Last = at
		}
		sessions[entry.SessionID] = info
	}
	return sessions
}

// codexThreadNames reads codex's session index: the latest thread_name
// per session id, codex's own equivalent of a custom title.
func codexThreadNames() map[string]string {
	names := map[string]string{}
	file, err := os.Open(filepath.Join(targets.DetectCodex().Source, "session_index.jsonl"))
	if err != nil {
		return names
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.ID != "" && entry.ThreadName != "" {
			names[entry.ID] = entry.ThreadName
		}
	}
	return names
}

func claudeHistorySessions() map[string]lostInfo {
	sessions := map[string]lostInfo{}
	file, err := os.Open(filepath.Join(targets.DetectClaude().Source, "history.jsonl"))
	if err != nil {
		return sessions
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry struct {
			Display   string `json:"display"`
			SessionID string `json:"sessionId"`
			Project   string `json:"project"`
			Timestamp int64  `json:"timestamp"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.SessionID == "" {
			continue
		}
		info := sessions[entry.SessionID]
		info.Count++
		if info.Project == "" {
			info.Project = entry.Project
		}
		if info.Title == "" {
			info.Title = strings.Join(strings.Fields(entry.Display), " ")
		}
		at := time.UnixMilli(entry.Timestamp)
		if info.First.IsZero() || at.Before(info.First) {
			info.First = at
		}
		if at.After(info.Last) {
			info.Last = at
		}
		sessions[entry.SessionID] = info
	}
	return sessions
}

// LoadLostDetail builds inspector data for a LOST session from the prompt
// history — the user's half of the conversation is all that survives.
func LoadLostDetail(id string) Detail {
	var detail Detail
	file, err := os.Open(filepath.Join(targets.DetectClaude().Source, "history.jsonl"))
	if err != nil {
		return detail
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry struct {
			Display   string `json:"display"`
			SessionID string `json:"sessionId"`
			Timestamp int64  `json:"timestamp"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.SessionID != id || entry.Display == "" {
			continue
		}
		detail.Messages++
		if detail.FirstPrompt == "" {
			detail.FirstPrompt = entry.Display
			detail.Created = time.UnixMilli(entry.Timestamp)
		}
		detail.LastPrompt = entry.Display
		detail.Transcript = append(detail.Transcript, TranscriptMsg{Role: "user", Text: entry.Display})
		detail.TranscriptTotal++
		if len(detail.Transcript) > transcriptKeep {
			detail.Transcript = detail.Transcript[1:]
		}
	}
	return detail
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
	HomeGone    bool // the directory itself no longer exists on disk
	Depth       int  // indent level when shown expanded under a parent
	Sessions    int
	SizeBytes   int64
	Latest      time.Time
	Open        int
	Stale       int
	Unbacked    int
	RecoverOnly int
	Lost        int
	Active      int
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
	for i := range folders {
		if !folders[i].Pseudo {
			if _, err := os.Stat(folders[i].Path); err != nil {
				folders[i].HomeGone = true
			}
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Latest.After(folders[j].Latest) })
	return folders
}

func aggregate(folder *Folder, project *Project) {
	folder.Sessions += len(project.Sessions) - project.Lost
	folder.Lost += project.Lost
	folder.SizeBytes += project.SizeBytes
	if project.Latest.After(folder.Latest) {
		folder.Latest = project.Latest
	}
	folder.Open += project.Open
	folder.Active += project.Active
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

// customTitle returns the last custom-title event in the file.
func customTitle(path string) string {
	name, _ := scanFileMeta(path)
	return name
}

// scanFileMeta reads a session file once for its latest custom name and the
// last model used. Cheap byte pre-filters keep this fast on large files.
func scanFileMeta(path string) (name string, model string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer file.Close()

	titlePattern := []byte(`"custom-title"`)
	modelPattern := []byte(`"model":"claude`)
	turnPattern := []byte(`"turn_context"`)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.Contains(line, titlePattern) {
			var event struct {
				Type        string `json:"type"`
				CustomTitle string `json:"customTitle"`
			}
			if json.Unmarshal(line, &event) == nil && event.Type == "custom-title" && event.CustomTitle != "" {
				name = event.CustomTitle
			}
		}
		if idx := bytes.LastIndex(line, modelPattern); idx >= 0 {
			rest := line[idx+len(`"model":"`):]
			if end := bytes.IndexByte(rest, '"'); end > 0 {
				model = string(rest[:end])
			}
		}
		if bytes.Contains(line, turnPattern) {
			var event struct {
				Type    string `json:"type"`
				Payload struct {
					Model string `json:"model"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &event) == nil && event.Type == "turn_context" && event.Payload.Model != "" {
				model = event.Payload.Model
			}
		}
	}
	return name, model
}

// claudeProjectPath recovers the real project path by reading cwd fields
// from the session files; slugs are not reversible. A transcript can
// carry cwds from OTHER directories — claude records the process cwd per
// line, and a session that started elsewhere before cd'ing into the
// project opens with foreign paths — so a cwd only wins outright when
// claude's own slug of it matches the directory the file lives under.
// With no slug-consistent cwd anywhere, the newest first-seen cwd is
// still better than nothing.
func claudeProjectPath(sessions []Session, slug string) string {
	best, fallback := "", ""
	var bestTime, fallbackTime time.Time
	for _, session := range sessions {
		path := session.SourcePath
		if path == "" {
			path = session.BackupPath
		}
		if path == "" {
			continue
		}
		matched, first := claudeCwd(path, slug)
		if matched != "" && !newest(session).Before(bestTime) {
			best = matched
			bestTime = newest(session)
		}
		if first != "" && !newest(session).Before(fallbackTime) {
			fallback = first
			fallbackTime = newest(session)
		}
	}
	if best != "" {
		return best
	}
	// No transcript line agrees with the slug — a manual copy that was
	// never resumed here has only foreign cwds. Decoding the slug
	// against the real filesystem beats trusting a foreign path.
	if decoded := decodeClaudeSlug(slug); decoded != "" {
		return decoded
	}
	return fallback
}

// decodeClaudeSlug reverses a claude project slug by walking the
// filesystem: every non-alphanumeric character slugs to '-', so the
// mapping is lossy, but only one chain of real directories usually
// slug-matches. Returns "" when no existing path decodes.
func decodeClaudeSlug(slug string) string {
	if !strings.HasPrefix(slug, "-") {
		return ""
	}
	budget := 400 // ReadDir calls; ambiguity is rare, runaway walks are not free
	return decodeSlugStep(string(os.PathSeparator), slug[1:], 0, &budget)
}

func decodeSlugStep(dir string, rest string, depth int, budget *int) string {
	if rest == "" {
		return dir
	}
	if depth > 24 || *budget <= 0 {
		return ""
	}
	*budget--
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		isDir := entry.IsDir()
		if !isDir && entry.Type()&os.ModeSymlink != 0 {
			// Symlinked directories (like macOS's /var) count.
			if info, err := os.Stat(filepath.Join(dir, entry.Name())); err == nil && info.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		es := nameSlug(entry.Name())
		if es == rest {
			return filepath.Join(dir, entry.Name())
		}
		if strings.HasPrefix(rest, es+"-") {
			if found := decodeSlugStep(filepath.Join(dir, entry.Name()), rest[len(es)+1:], depth+1, budget); found != "" {
				return found
			}
		}
	}
	return ""
}

// nameSlug applies claude's slug mapping to a single path segment.
func nameSlug(name string) string {
	out := []byte(name)
	for i, c := range out {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			out[i] = '-'
		}
	}
	return string(out)
}

// claudeCwd scans a transcript for cwd fields: matched is the first one
// whose claude slug equals the wanted slug, first is the first cwd of
// any kind. The scan is capped — enough to get past a long foreign
// prefix without reading multi-MB transcripts end to end.
func claudeCwd(path string, slug string) (matched string, first string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for i := 0; i < 4000 && scanner.Scan(); i++ {
		var event struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Cwd == "" {
			continue
		}
		if first == "" {
			first = event.Cwd
		}
		if targets.ClaudeSlug(event.Cwd) == slug {
			return event.Cwd, first
		}
	}
	return "", first
}

func newest(session Session) time.Time {
	if session.Modified.After(session.BackupModified) {
		return session.Modified
	}
	return session.BackupModified
}
