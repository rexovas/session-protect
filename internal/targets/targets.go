package targets

import (
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
