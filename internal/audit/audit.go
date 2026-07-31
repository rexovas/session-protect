// Package audit is the permanent record of every change the tool makes on
// the user's behalf. Actions append JSONL entries to <backup root>/audit.log;
// the browser reads the log back for the activity view and restored badges.
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Entry is one recorded action.
type Entry struct {
	Time       time.Time `json:"time"`
	Action     string    `json:"action"` // restore | ...
	Target     string    `json:"target,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	Overwrote  bool      `json:"overwrote,omitempty"`
	SafetyCopy string    `json:"safety_copy,omitempty"`
}

func logPath(root string) string { return filepath.Join(root, "audit.log") }

// Append records entries. Logging failures never fail the action itself,
// so errors are deliberately dropped.
func Append(root string, entries []Entry) {
	file, err := os.OpenFile(logPath(root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	for _, entry := range entries {
		_ = enc.Encode(entry)
	}
}

// Read returns all recorded entries, oldest first. Damaged lines are
// skipped so one bad record never hides the rest of the log.
func Read(root string) []Entry {
	file, err := os.Open(logPath(root))
	if err != nil {
		return nil
	}
	defer file.Close()
	var out []Entry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Action != "" {
			out = append(out, entry)
		}
	}
	return out
}
