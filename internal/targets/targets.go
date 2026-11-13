package targets

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/sessionfile"
)

type Target struct {
	Name string `json:"name"`
	// Agent is the agent that writes the target ("claude" or "codex");
	// Home is the claude home label (see ClaudeHome), empty for codex.
	Agent    string   `json:"agent"`
	Home     string   `json:"home,omitempty"`
	Source   string   `json:"source"`
	Detected bool     `json:"detected"`
	Mode     string   `json:"mode"`
	Include  []string `json:"include"`
	Exclude  []string `json:"exclude"`
	// Config selects the small, hand-maintained configuration around the
	// sessions (settings, instructions, rules, skills) so changes to it are
	// tracked too. Entries match like Include, but only files up to
	// fsplan.ConfigMaxBytes are taken, and ConfigExclude applies on top of
	// Exclude: generated bulk (logs, caches, packages) never belongs here.
	Config        []string `json:"config,omitempty"`
	ConfigExclude []string `json:"config_exclude,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

func DetectAll() []Target {
	var all []Target
	for _, home := range ClaudeHomes() {
		all = append(all, DetectClaudeHome(home))
	}
	return append(all, DetectCodex())
}

// DetectClaude is the default claude home, ~/.claude.
func DetectClaude() Target {
	return DetectClaudeHome(ClaudeHomes()[0])
}

// DetectClaudeTargets lists one target per claude home.
func DetectClaudeTargets() []Target {
	var all []Target
	for _, home := range ClaudeHomes() {
		all = append(all, DetectClaudeHome(home))
	}
	return all
}

// DetectClaudeHome is the backup target for one claude home.
func DetectClaudeHome(home ClaudeHome) Target {
	source := home.Dir
	return Target{
		Name:     home.TargetName(),
		Agent:    "claude",
		Home:     home.Label,
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
		Config: []string{
			"settings.local.json",
			"CLAUDE.md",
			"keybindings.json",
			"statusline*",
			"agents/",
			"commands/",
			"hooks/",
			"output-styles/",
			"skills/",
			"plugins/installed_plugins.json",
			"plugins/known_marketplaces.json",
			"plugins/blocklist.json",
		},
		ConfigExclude: []string{
			"synced/", // skills synced down from claude.ai, not maintained here
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
		Agent:    "codex",
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
		Config: []string{
			"config.toml",
			"AGENTS.md",
			"AGENTS.override.md",
			"rules/",
			"prompts/",
			"skills/",
		},
		ConfigExclude: []string{
			".system/", // skills bundled with Codex, replaced on every update
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
func CodexSessionMeta(path string) (id string, cwd string, subagent bool) {
	file, err := sessionfile.Open(path)
	if err != nil {
		return "", "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for i := 0; i < 50 && scanner.Scan(); i++ {
		var event struct {
			Payload struct {
				ID     string          `json:"id"`
				Cwd    string          `json:"cwd"`
				Source json.RawMessage `json:"source"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Payload.Cwd != "" {
			// A top-level session's source is a plain string
			// ("cli"/"vscode"/"exec"); a subagent's is an object with a
			// "subagent" key (guardian auto-review, thread_spawn workers).
			return event.Payload.ID, event.Payload.Cwd, isSubagentSource(event.Payload.Source)
		}
	}
	return "", "", false
}

func isSubagentSource(raw json.RawMessage) bool {
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	_, ok := obj["subagent"]
	return ok
}
