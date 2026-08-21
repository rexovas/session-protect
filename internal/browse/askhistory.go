package browse

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/rexovas/session-protect/internal/config"
)

// askHistoryLimit caps how many prior AI-find queries are kept and shown.
const askHistoryLimit = 50

func askHistoryPath(cfg config.Config) string {
	return filepath.Join(cfg.BackupRoot, "ask-history.jsonl")
}

type askHistoryEntry struct {
	Query string    `json:"q"`
	Model string    `json:"model,omitempty"`
	At    time.Time `json:"at"`
}

// loadAskHistory returns prior queries, most recent first, deduplicated
// to their latest occurrence.
func loadAskHistory(cfg config.Config) []string {
	file, err := os.Open(askHistoryPath(cfg))
	if err != nil {
		return nil
	}
	defer file.Close()
	var queries []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry askHistoryEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Query == "" {
			continue
		}
		queries = append(queries, entry.Query)
	}
	seen := map[string]bool{}
	var out []string
	for i := len(queries) - 1; i >= 0; i-- {
		if seen[queries[i]] {
			continue
		}
		seen[queries[i]] = true
		out = append(out, queries[i])
		if len(out) == askHistoryLimit {
			break
		}
	}
	return out
}

// appendAskHistory records a fired query. Failures are silent — history
// is a convenience, never a blocker.
func appendAskHistory(cfg config.Config, query string, model string) {
	if err := os.MkdirAll(cfg.BackupRoot, 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(askHistoryPath(cfg), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	line, _ := json.Marshal(askHistoryEntry{Query: query, Model: model, At: time.Now()})
	file.Write(append(line, '\n'))
}

// pushHistory prepends a query to the in-memory list, deduplicating.
func pushHistory(history []string, query string) []string {
	out := []string{query}
	for _, prior := range history {
		if prior != query && len(out) < askHistoryLimit {
			out = append(out, prior)
		}
	}
	return out
}
