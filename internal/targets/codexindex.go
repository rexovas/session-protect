package targets

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/rexovas/session-protect/internal/sessionfile"
)

// Codex files sessions by date, not by project, and records each one's
// folder only inside the rollout. Ownership needs folder → session counts
// per codex home on every launch, so a small cache remembers each rollout's
// folder; only rollouts not seen before are opened.

// CodexEntry is one rollout's identity.
type CodexEntry struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd"`
	Subagent bool   `json:"subagent,omitempty"`
	Parent   string `json:"parent,omitempty"` // spawning thread, for spawned subagents
}

var codexIndexMu sync.Mutex

func codexIndexFile() string {
	return filepath.Join(filepath.Dir(homesFile(AgentCodex)), "codex-rollouts.json")
}

// codexParent is the thread that spawned a subagent rollout, from its
// session_meta source ({"subagent":{"thread_spawn":{"parent_thread_id":…}}}).
func codexParent(path string) string {
	file, err := sessionfile.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for i := 0; i < 50 && scanner.Scan(); i++ {
		var event struct {
			Payload struct {
				Cwd    string `json:"cwd"`
				Source struct {
					Subagent struct {
						ThreadSpawn struct {
							ParentThreadID string `json:"parent_thread_id"`
						} `json:"thread_spawn"`
					} `json:"subagent"`
				} `json:"source"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Payload.Cwd != "" {
			return event.Payload.Source.Subagent.ThreadSpawn.ParentThreadID
		}
	}
	return ""
}

// CodexRollouts maps every rollout under a codex home's sessions dir (by
// path) to its session id, folder and subagent flag.
func CodexRollouts(home Home) map[string]CodexEntry {
	codexIndexMu.Lock()
	defer codexIndexMu.Unlock()
	cache := map[string]CodexEntry{}
	if data, err := os.ReadFile(codexIndexFile()); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	out := map[string]CodexEntry{}
	changed := false
	root := filepath.Join(home.Dir, "sessions")
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !sessionfile.IsTranscript(entry.Name()) {
			return nil
		}
		known, ok := cache[path]
		if !ok {
			id, cwd, subagent := CodexSessionMeta(path)
			if cwd == "" {
				return nil // not readable yet (being written); try next time
			}
			known = CodexEntry{ID: id, Cwd: filepath.Clean(cwd), Subagent: subagent, Parent: codexParent(path)}
			cache[path] = known
			changed = true
		}
		out[path] = known
		return nil
	})
	// Forget rollouts that are gone from this home.
	for path := range cache {
		if _, live := out[path]; !live && isUnder(path, root) {
			delete(cache, path)
			changed = true
		}
	}
	if changed {
		if data, err := json.Marshal(cache); err == nil {
			_ = os.MkdirAll(filepath.Dir(codexIndexFile()), 0o700)
			tmp := codexIndexFile() + ".tmp"
			if os.WriteFile(tmp, data, 0o600) == nil {
				_ = os.Rename(tmp, codexIndexFile())
			}
		}
	}
	return out
}

// codexFolders counts a codex home's top-level sessions per folder. A
// session can have two rollouts (plain beside a compressed copy); it
// counts once.
func codexFolders(home Home) map[string]int {
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, entry := range CodexRollouts(home) {
		if !entry.Subagent && !seen[entry.ID] {
			seen[entry.ID] = true
			counts[entry.Cwd]++
		}
	}
	return counts
}
