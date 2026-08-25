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

// cachedResult is one saved AI-find match: enough to re-display the
// result without asking the model again. Sessions are stored by id and
// rehydrated against the current scan, so state stays truthful.
type cachedResult struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
	Count  int    `json:"count,omitempty"`
}

type askHistoryEntry struct {
	Query   string         `json:"q"`
	Model   string         `json:"model,omitempty"`
	At      time.Time      `json:"at"`
	Results []cachedResult `json:"results,omitempty"`
}

// loadAskHistory returns prior searches, most recent first, deduplicated
// by query to their latest occurrence.
func loadAskHistory(cfg config.Config) []askHistoryEntry {
	file, err := os.Open(askHistoryPath(cfg))
	if err != nil {
		return nil
	}
	defer file.Close()
	var entries []askHistoryEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry askHistoryEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Query == "" {
			continue
		}
		entries = append(entries, entry)
	}
	seen := map[string]bool{}
	var out []askHistoryEntry
	for i := len(entries) - 1; i >= 0; i-- {
		if seen[entries[i].Query] {
			continue
		}
		seen[entries[i].Query] = true
		out = append(out, entries[i])
		if len(out) == askHistoryLimit {
			break
		}
	}
	return out
}

// appendAskHistory records a completed search. Failures are silent —
// history is a convenience, never a blocker.
func appendAskHistory(cfg config.Config, entry askHistoryEntry) {
	if err := os.MkdirAll(cfg.BackupRoot, 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(askHistoryPath(cfg), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	line, _ := json.Marshal(entry)
	file.Write(append(line, '\n'))
}

// pushHistory prepends an entry to the in-memory list, deduplicating by
// query.
func pushHistory(history []askHistoryEntry, entry askHistoryEntry) []askHistoryEntry {
	out := []askHistoryEntry{entry}
	for _, prior := range history {
		if prior.Query != entry.Query && len(out) < askHistoryLimit {
			out = append(out, prior)
		}
	}
	return out
}
