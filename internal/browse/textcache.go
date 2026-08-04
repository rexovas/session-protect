package browse

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rexovas/session-protect/internal/config"
)

// The text cache holds one plain-text extract per session under
// <backup root>/.session-text/: conversation text only — user prompts and
// assistant responses, no tool dumps. An index keyed by session id records
// the source file's mtime so refreshes re-extract only what changed.

const textCacheDir = ".session-text"

type textIndexEntry struct {
	Mtime int64  `json:"mtime"`
	Src   string `json:"src"`
}

// Hit is one session's content-search result.
type Hit struct {
	Session Session
	Count   int
	Snippet string
}

// ContentSearch refreshes the text cache incrementally, then counts
// case-insensitive occurrences of query in each session's conversation
// text. Results are sorted by hit count — the session that mentions a
// term fifty times is the primary session for it — with recency breaking
// ties.
func ContentSearch(cfg config.Config, sessions []Session, query string) []Hit {
	refreshTextCache(cfg, sessions)
	dir := filepath.Join(cfg.BackupRoot, textCacheDir)
	lowerQuery := strings.ToLower(query)
	var hits []Hit
	for _, session := range sessions {
		data, err := os.ReadFile(filepath.Join(dir, session.ID+".txt"))
		if err != nil || len(data) == 0 {
			continue
		}
		text := string(data)
		lower := strings.ToLower(text)
		count := strings.Count(lower, lowerQuery)
		if count == 0 {
			continue
		}
		hits = append(hits, Hit{
			Session: session,
			Count:   count,
			Snippet: snippetAround(text, lower, lowerQuery),
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Count != hits[j].Count {
			return hits[i].Count > hits[j].Count
		}
		return hits[i].Session.Modified.After(hits[j].Session.Modified)
	})
	return hits
}

// refreshTextCache re-extracts sessions whose source files changed since
// their cached extract was written.
func refreshTextCache(cfg config.Config, sessions []Session) {
	dir := filepath.Join(cfg.BackupRoot, textCacheDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	indexPath := filepath.Join(dir, "index.json")
	index := map[string]textIndexEntry{}
	if data, err := os.ReadFile(indexPath); err == nil {
		_ = json.Unmarshal(data, &index)
	}

	type job struct {
		id    string
		src   string
		mtime int64
	}
	var jobs []job
	for _, session := range sessions {
		src := session.SourcePath
		if src == "" {
			src = session.BackupPath
		}
		if src == "" {
			continue // lost sessions have no transcript to extract
		}
		info, err := os.Stat(src)
		if err != nil {
			continue
		}
		mtime := info.ModTime().UnixNano()
		if entry, ok := index[session.ID]; ok && entry.Mtime == mtime && entry.Src == src {
			continue
		}
		jobs = append(jobs, job{session.ID, src, mtime})
	}
	if len(jobs) == 0 {
		return
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, item := range jobs {
		wg.Add(1)
		go func(item job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			text := extractText(item.src)
			if os.WriteFile(filepath.Join(dir, item.id+".txt"), []byte(text), 0o600) == nil {
				mu.Lock()
				index[item.id] = textIndexEntry{Mtime: item.mtime, Src: item.src}
				mu.Unlock()
			}
		}(item)
	}
	wg.Wait()

	if data, err := json.Marshal(index); err == nil {
		_ = os.WriteFile(indexPath, data, 0o600)
	}
}

// extractText pulls the conversation text out of a session transcript:
// human prompts and assistant responses only. Tool invocations, tool
// results, and machine-written compaction summaries are excluded — they
// are bulk, not conversation.
func extractText(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	var b strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var event transcriptLine
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "event_msg" {
			// codex rollout: conversation text lives in event payloads.
			var line codexLine
			if json.Unmarshal(scanner.Bytes(), &line) == nil {
				var payload struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}
				if json.Unmarshal(line.Payload, &payload) == nil && payload.Message != "" &&
					(payload.Type == "user_message" || payload.Type == "agent_message") {
					b.WriteString(payload.Message)
					b.WriteByte('\n')
				}
			}
			continue
		}
		switch event.Type {
		case "user":
			if event.IsCompactSummary {
				continue
			}
			if text := contentRaw(event.Message.Content); text != "" {
				b.WriteString(text)
				b.WriteByte('\n')
			}
		case "assistant":
			if text := contentRaw(event.Message.Content); text != "" {
				b.WriteString(text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// snippetAround returns the line containing the first match, center-trimmed
// around the hit when the line is long.
func snippetAround(text string, lower string, lowerQuery string) string {
	idx := strings.Index(lower, lowerQuery)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndexByte(text[:idx], '\n') + 1
	end := idx + len(lowerQuery)
	if nl := strings.IndexByte(text[end:], '\n'); nl >= 0 {
		end += nl
	} else {
		end = len(text)
	}
	line := text[start:end]
	if len(line) > 160 {
		rel := idx - start
		from := max(0, rel-50)
		to := min(len(line), rel+110)
		line = "…" + line[from:to] + "…"
	}
	return strings.ToValidUTF8(strings.Join(strings.Fields(line), " "), "")
}
