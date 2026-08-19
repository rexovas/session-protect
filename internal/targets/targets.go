package targets

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

type Target struct {
	Name     string   `json:"name"`
	Source   string   `json:"source"`
	Detected bool     `json:"detected"`
	Mode     string   `json:"mode"`
	Include  []string `json:"include"`
	Exclude  []string `json:"exclude"`
	Notes    []string `json:"notes,omitempty"`
}

func DetectAll() []Target {
	return []Target{
		DetectClaude(),
		DetectCodex(),
	}
}

func DetectClaude() Target {
	home := homeDir()
	source := filepath.Join(home, ".claude")
	return Target{
		Name:     "claude",
		Source:   source,
		Detected: dirExists(source),
		Mode:     "sessions",
		Include: []string{
			"projects/",
			"history.jsonl",
			"settings.json",
		},
		Exclude: []string{
			"auth.json",
			"cache/",
			"tmp/",
		},
	}
}

func DetectCodex() Target {
	source := os.Getenv("CODEX_HOME")
	if source == "" {
		source = filepath.Join(homeDir(), ".codex")
	}
	return Target{
		Name:     "codex",
		Source:   source,
		Detected: dirExists(source),
		Mode:     "safe-default",
		Include: []string{
			"sessions/",
			"session_index.jsonl",
			"history.jsonl",
			"memories/",
			"memories_*.sqlite",
		},
		Exclude: []string{
			"auth.json",
			".tmp/",
			"tmp/",
			"cache/",
			"shell_snapshots/",
			"logs*.sqlite*",
		},
		Notes: []string{
			"Codex defaults are intentionally narrower than a full CODEX_HOME mirror.",
		},
	}
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ClaudeSlug is the agent's project-path encoding: every character
// outside [a-zA-Z0-9] becomes a dash. Slugs are lossy — recover real
// paths from session cwd fields, never by decoding slugs.
func ClaudeSlug(path string) string {
	slug := []byte(filepath.Clean(path))
	for i, c := range slug {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			slug[i] = '-'
		}
	}
	return string(slug)
}

// CodexSessionMeta reads a codex rollout's identity from its leading
// session_meta payload: the session id and working directory.
func CodexSessionMeta(path string) (id string, cwd string) {
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
