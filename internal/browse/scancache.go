package browse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rexovas/session-protect/internal/targets"
)

// The browser rescans every few seconds for its whole lifetime, over a
// dataset that can run to thousands of transcripts and a multi-MB prompt
// history. Session and history files are append-only, so the per-file work
// here is memoized for the life of the process: an unchanged file is never
// re-read, and a grown one is parsed only from where the last read stopped.

// readAppended calls fn for each line of path from offset on and returns
// the offset just past the last complete (newline-terminated) line. A
// trailing partial line — one still being written — is passed to fn only
// when partial is set, and never advances the offset, so the next call
// sees it again once complete; callers that set partial must tolerate
// seeing a line twice. ok is false when the file is shorter than offset
// (truncated or replaced), telling the caller to start over from zero.
func readAppended(path string, offset int64, partial bool, fn func(line []byte)) (next int64, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return offset, true
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return offset, true
	}
	if info.Size() < offset {
		return 0, false
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, false
	}
	reader := bufio.NewReaderSize(file, 256*1024)
	next = offset
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			next += int64(len(line))
			fn(line[:len(line)-1])
		} else if len(line) > 0 && partial {
			fn(line)
		}
		if err != nil {
			return next, true
		}
	}
}

// stamp identifies a file version cheaply: append-only files change size
// whenever they change content.
type stamp struct {
	size int64
	mod  time.Time
}

func stampOf(path string) (stamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return stamp{}, false
	}
	return stamp{size: info.Size(), mod: info.ModTime()}, true
}

// historyCache incrementally indexes one agent's prompt history: the
// sessions it mentions (for lost-session detection) and their titles.
// Maps are copied before new lines are applied, so a map handed to one
// scan is never mutated under it by another.
type historyCache struct {
	mu       sync.Mutex
	path     string
	seen     stamp
	offset   int64
	sessions map[string]lostInfo
	titles   map[string]string
	apply    func(line []byte, sessions map[string]lostInfo, titles map[string]string)
}

func (c *historyCache) load(path string) (map[string]lostInfo, map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, exists := stampOf(path)
	if !exists {
		c.path, c.seen, c.offset, c.sessions, c.titles = path, stamp{}, 0, nil, nil
		return map[string]lostInfo{}, map[string]string{}
	}
	if c.path == path && c.sessions != nil && st == c.seen {
		return c.sessions, c.titles
	}
	sessions, titles := map[string]lostInfo{}, map[string]string{}
	start := int64(0)
	if c.path == path && c.sessions != nil && st.size >= c.offset {
		start = c.offset
		for k, v := range c.sessions {
			sessions[k] = v
		}
		for k, v := range c.titles {
			titles[k] = v
		}
	}
	next, ok := readAppended(path, start, false, func(line []byte) { c.apply(line, sessions, titles) })
	if !ok {
		sessions, titles = map[string]lostInfo{}, map[string]string{}
		next, _ = readAppended(path, 0, false, func(line []byte) { c.apply(line, sessions, titles) })
	}
	c.path, c.seen, c.offset, c.sessions, c.titles = path, st, next, sessions, titles
	return sessions, titles
}

// setTitle applies the title rule shared by both histories: a slash
// command ("/model", "/clear") is a title only when nothing substantive
// ever follows it.
func setTitle(titles map[string]string, id, text string) {
	if id == "" || text == "" {
		return
	}
	if current, seen := titles[id]; !seen || isSlashCommand(current) && !isSlashCommand(text) {
		titles[id] = strings.Join(strings.Fields(text), " ")
	}
}

var claudeHistory = &historyCache{apply: func(line []byte, sessions map[string]lostInfo, titles map[string]string) {
	var entry struct {
		Display   string `json:"display"`
		SessionID string `json:"sessionId"`
		Project   string `json:"project"`
		Timestamp int64  `json:"timestamp"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.SessionID == "" {
		return
	}
	setTitle(titles, entry.SessionID, entry.Display)
	info := sessions[entry.SessionID]
	info.Count++
	if info.Project == "" {
		info.Project = entry.Project
	}
	if info.Title == "" || isSlashCommand(info.Title) && !isSlashCommand(entry.Display) {
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
}}

var codexHistory = &historyCache{apply: func(line []byte, sessions map[string]lostInfo, titles map[string]string) {
	var entry struct {
		SessionID string `json:"session_id"`
		Ts        int64  `json:"ts"`
		Text      string `json:"text"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.SessionID == "" {
		return
	}
	setTitle(titles, entry.SessionID, entry.Text)
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
}}

// claudeHistorySessions indexes the claude prompt history by session id —
// the only record of sessions whose transcripts were pruned before any
// backup existed. The returned map is shared: callers must not modify it.
func claudeHistorySessions() map[string]lostInfo {
	sessions, _ := claudeHistory.load(filepath.Join(targets.DetectClaude().Source, "history.jsonl"))
	return sessions
}

// codexHistorySessions indexes every session the codex prompt history
// mentions. The returned map is shared: callers must not modify it.
func codexHistorySessions() map[string]lostInfo {
	sessions, _ := codexHistory.load(filepath.Join(targets.DetectCodex().Source, "history.jsonl"))
	return sessions
}

// historyTitles maps session ids to the first substantive prompt recorded
// for them in the agents' history files — a cheap title source that avoids
// opening every session file.
func historyTitles() map[string]string {
	_, claude := claudeHistory.load(filepath.Join(targets.DetectClaude().Source, "history.jsonl"))
	_, codex := codexHistory.load(filepath.Join(targets.DetectCodex().Source, "history.jsonl"))
	titles := make(map[string]string, len(claude)+len(codex))
	for id, title := range claude {
		titles[id] = title
	}
	for id, title := range codex {
		if _, ok := titles[id]; !ok {
			titles[id] = title
		}
	}
	return titles
}

// fileMeta is the incremental state of scanFileMeta for one transcript.
type fileMeta struct {
	seen        stamp
	offset      int64
	name, model string
}

var fileMetaMemo sync.Map // path -> fileMeta

// scanFileMeta returns a session file's latest custom name and the last
// model used. A grown transcript is read only from where the last call
// stopped; an unchanged one is not read at all.
func scanFileMeta(path string) (name string, model string) {
	st, exists := stampOf(path)
	if !exists {
		return "", ""
	}
	start := int64(0)
	if cached, ok := fileMetaMemo.Load(path); ok {
		prev := cached.(fileMeta)
		if prev.seen == st {
			return prev.name, prev.model
		}
		if st.size >= prev.offset {
			start, name, model = prev.offset, prev.name, prev.model
		}
	}
	next, ok := readAppended(path, start, true, func(line []byte) { applyFileMeta(line, &name, &model) })
	if !ok {
		name, model = "", ""
		next, _ = readAppended(path, 0, true, func(line []byte) { applyFileMeta(line, &name, &model) })
	}
	fileMetaMemo.Store(path, fileMeta{seen: st, offset: next, name: name, model: model})
	return name, model
}

var (
	titlePattern = []byte(`"custom-title"`)
	modelPattern = []byte(`"model":"claude`)
	turnPattern  = []byte(`"turn_context"`)
)

// applyFileMeta folds one transcript line into the running name/model.
// Later lines win, so re-applying a line is harmless.
func applyFileMeta(line []byte, name, model *string) {
	if bytes.Contains(line, titlePattern) {
		var event struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "custom-title" && event.CustomTitle != "" {
			*name = event.CustomTitle
		}
	}
	if idx := bytes.LastIndex(line, modelPattern); idx >= 0 {
		rest := line[idx+len(`"model":"`):]
		if end := bytes.IndexByte(rest, '"'); end > 0 {
			*model = string(rest[:end])
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
			*model = event.Payload.Model
		}
	}
}

// headMemo caches a result read from the head of a transcript. Once the
// result is final (found, or the bounded head fully read), appends cannot
// change it, so it survives growth.
type headMemo[T any] struct {
	seen  stamp
	found bool
	value T
}

var (
	cwdMemo       sync.Map // path + "\x00" + slug -> headMemo[[2]string]
	codexMetaMemo sync.Map // path -> headMemo[codexMeta]
)

type codexMeta struct {
	id, cwd  string
	subagent bool
}

// memoHead returns the cached head result for key when the file is
// unchanged, or when a result was found and the file has only grown.
func memoHead[T any](memo *sync.Map, key string, st stamp, compute func() (T, bool)) T {
	if cached, ok := memo.Load(key); ok {
		prev := cached.(headMemo[T])
		if prev.seen == st || prev.found && st.size >= prev.seen.size {
			return prev.value
		}
	}
	value, found := compute()
	memo.Store(key, headMemo[T]{seen: st, found: found, value: value})
	return value
}

// cachedClaudeCwd is claudeCwd memoized per transcript.
func cachedClaudeCwd(path string, slug string) (matched string, first string) {
	st, exists := stampOf(path)
	if !exists {
		return "", ""
	}
	pair := memoHead(&cwdMemo, path+"\x00"+slug, st, func() ([2]string, bool) {
		m, f, final := claudeCwd(path, slug)
		return [2]string{m, f}, final
	})
	return pair[0], pair[1]
}

// cachedCodexSessionMeta is targets.CodexSessionMeta memoized per rollout;
// the session_meta line opens the file, so a found result is permanent.
func cachedCodexSessionMeta(path string, st stamp) (id string, cwd string, subagent bool) {
	meta := memoHead(&codexMetaMemo, path, st, func() (codexMeta, bool) {
		id, cwd, subagent := targets.CodexSessionMeta(path)
		return codexMeta{id: id, cwd: cwd, subagent: subagent}, cwd != ""
	})
	return meta.id, meta.cwd, meta.subagent
}

// slugDecodeTTL bounds how long a filesystem walk decoding a claude slug
// is trusted; directories appear and vanish rarely, and the walk is not
// free (up to hundreds of ReadDir calls).
const slugDecodeTTL = 5 * time.Minute

type slugDecode struct {
	at   time.Time
	path string
}

var slugDecodeMemo sync.Map // slug -> slugDecode

func cachedDecodeClaudeSlug(slug string) string {
	if cached, ok := slugDecodeMemo.Load(slug); ok {
		prev := cached.(slugDecode)
		if time.Since(prev.at) < slugDecodeTTL {
			return prev.path
		}
	}
	path := decodeClaudeSlug(slug)
	slugDecodeMemo.Store(slug, slugDecode{at: time.Now(), path: path})
	return path
}
