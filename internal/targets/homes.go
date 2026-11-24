package targets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A Home is one agent home dir: a claude config dir (CLAUDE_CONFIG_DIR,
// default ~/.claude) or a codex home (CODEX_HOME, default ~/.codex). Each
// keeps its own login, settings and sessions, so people run several to
// separate accounts or clients. The default dir is always present as
// "default"; others are registered by path, whatever they are named.
//
// A label names an account across agents: the "acme" claude dir and the
// "acme" codex home are the same account, and folder ownership (see
// ownership.go) is learned from either agent's sessions.
type Home struct {
	Agent string `json:"agent,omitempty"` // "claude" (empty in older files) or "codex"
	Label string `json:"label"`
	Dir   string `json:"dir"`
}

// ClaudeHome is a claude config dir.
type ClaudeHome = Home

const (
	// DefaultHomeLabel names the default dir of either agent.
	DefaultHomeLabel = "default"
	AgentClaude      = "claude"
	AgentCodex       = "codex"
)

// IsDefault reports whether this is the agent's default dir.
func (h Home) IsDefault() bool { return h.Label == DefaultHomeLabel }

func (h Home) agent() string {
	if h.Agent == "" {
		return AgentClaude
	}
	return h.Agent
}

// TargetName is the backup target (and repo prefix) for the home: the
// defaults keep the historical "claude" and "codex", so existing backups
// are unchanged.
func (h Home) TargetName() string {
	if h.IsDefault() {
		return h.agent()
	}
	return h.agent() + "-" + h.Label
}

// EnvVar is the variable that points the agent at a home.
func (h Home) EnvVar() string { return EnvVarFor(h.agent()) }

// EnvVarFor is the home variable of an agent.
func EnvVarFor(agent string) string {
	if agent == AgentCodex {
		return "CODEX_HOME"
	}
	return "CLAUDE_CONFIG_DIR"
}

// Env is the environment entry that makes the agent use this home, or ""
// for the default, which runs with the variable unset: claude keys its
// stored login on whether CLAUDE_CONFIG_DIR is set at all.
func (h Home) Env() string {
	if h.IsDefault() {
		return ""
	}
	return h.EnvVar() + "=" + h.Dir
}

// Pin is how to start the agent on this home whatever the environment:
// the variable set to the home, or for the default removed (claude) or set
// only when the default is itself a CODEX_HOME override (codex). Unset
// means remove the variable.
func (h Home) Pin() (value string, unset bool) {
	if !h.IsDefault() {
		return h.Dir, false
	}
	if h.agent() == AgentCodex && h.Dir != filepath.Join(homeDir(), ".codex") {
		return h.Dir, false
	}
	return "", true
}

// DefaultClaudeDir is claude's config dir when CLAUDE_CONFIG_DIR is unset.
func DefaultClaudeDir() string {
	return filepath.Join(homeDir(), ".claude")
}

// DefaultCodexDir is codex's home when CODEX_HOME is unset, or the
// CODEX_HOME a user sets for everything. A CODEX_HOME naming a registered
// extra home is that home, not the default (it is set by our launcher).
func DefaultCodexDir() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		dir = cleanDir(dir)
		registered := false
		for _, home := range loadRegistered(AgentCodex) {
			if cleanDir(home.Dir) == dir {
				registered = true
			}
		}
		if !registered {
			return dir
		}
	}
	return filepath.Join(homeDir(), ".codex")
}

func defaultDir(agent string) string {
	if agent == AgentCodex {
		return DefaultCodexDir()
	}
	return DefaultClaudeDir()
}

// ActiveClaudeDir is the config dir a claude started from this process's
// environment would use.
func ActiveClaudeDir() string { return ActiveDir(AgentClaude) }

// ActiveDir is the home an agent started from this environment would use.
func ActiveDir(agent string) string {
	if dir := os.Getenv(EnvVarFor(agent)); dir != "" {
		return cleanDir(dir)
	}
	return defaultDir(agent)
}

// homesFile lists one agent's registered extra homes. It sits beside the
// config file so a test (or a second install) pointing
// SESSION_PROTECT_CONFIG elsewhere gets its own list.
func homesFile(agent string) string {
	dir := filepath.Join(homeDir(), ".config", "session-protect")
	if override := os.Getenv("SESSION_PROTECT_CONFIG"); override != "" {
		dir = filepath.Dir(override)
	}
	return filepath.Join(dir, agent+"-dirs.json")
}

func loadRegistered(agent string) []Home {
	data, err := os.ReadFile(homesFile(agent))
	if err != nil {
		return nil
	}
	var homes []Home
	if json.Unmarshal(data, &homes) != nil {
		return nil
	}
	for i := range homes {
		homes[i].Agent = agent
	}
	return homes
}

func saveRegistered(agent string, homes []Home) error {
	sort.Slice(homes, func(i, j int) bool { return homes[i].Label < homes[j].Label })
	stored := make([]Home, len(homes))
	for i, home := range homes {
		stored[i] = Home{Label: home.Label, Dir: home.Dir}
		if agent != AgentClaude {
			stored[i].Agent = agent
		}
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	path := homesFile(agent)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// HomesOf lists an agent's homes: the default first, then registered ones
// by label. Registered homes stay listed after their dir is removed, so
// their backups remain browsable and restorable.
func HomesOf(agent string) []Home {
	defaultHome := Home{Agent: agent, Label: DefaultHomeLabel, Dir: defaultDir(agent)}
	homes := []Home{defaultHome}
	for _, home := range loadRegistered(agent) {
		if home.Label == "" || home.Label == DefaultHomeLabel || cleanDir(home.Dir) == defaultHome.Dir {
			continue
		}
		home.Dir = cleanDir(home.Dir)
		homes = append(homes, home)
	}
	sort.SliceStable(homes[1:], func(i, j int) bool { return homes[1+i].Label < homes[1+j].Label })
	return homes
}

// ClaudeHomes lists the claude config dirs.
func ClaudeHomes() []Home { return HomesOf(AgentClaude) }

// CodexHomes lists the codex homes.
func CodexHomes() []Home { return HomesOf(AgentCodex) }

// Accounts lists one home per account label across agents, the default
// first: the label set ownership resolves against.
func Accounts() []Home {
	accounts := ClaudeHomes()
	seen := map[string]bool{}
	for _, home := range accounts {
		seen[home.Label] = true
	}
	for _, home := range CodexHomes() {
		if !seen[home.Label] {
			seen[home.Label] = true
			accounts = append(accounts, home)
		}
	}
	sort.SliceStable(accounts[1:], func(i, j int) bool { return accounts[1+i].Label < accounts[1+j].Label })
	return accounts
}

// HomeByLabelOf finds an agent's home by label.
func HomeByLabelOf(agent string, label string) (Home, bool) {
	for _, home := range HomesOf(agent) {
		if home.Label == label {
			return home, true
		}
	}
	return Home{}, false
}

// HomeByLabel finds a claude home by label.
func HomeByLabel(label string) (Home, bool) { return HomeByLabelOf(AgentClaude, label) }

// HomeForDirOf finds the agent's home whose dir is dir.
func HomeForDirOf(agent string, dir string) (Home, bool) {
	dir = cleanDir(dir)
	for _, home := range HomesOf(agent) {
		if home.Dir == dir {
			return home, true
		}
	}
	return Home{}, false
}

// HomeForDir finds the claude home whose dir is dir.
func HomeForDir(dir string) (Home, bool) { return HomeForDirOf(AgentClaude, dir) }

// HomeForOf finds an agent's home by label, falling back to the default.
func HomeForOf(agent string, label string) Home {
	if home, ok := HomeByLabelOf(agent, label); ok {
		return home
	}
	return HomesOf(agent)[0]
}

// HomeFor finds a claude home by label, falling back to the default.
func HomeFor(label string) Home { return HomeForOf(AgentClaude, label) }

// RegisterDir adds dir as one of an agent's homes. An empty label is
// derived from the dir's name. Registering a dir already known returns its
// existing entry. A label is fixed once set: it names the home's backup
// folder. It reports whether anything changed.
func RegisterDir(agent string, dir string, label string) (Home, bool, error) {
	dir = cleanDir(dir)
	if dir == "" {
		return Home{}, false, fmt.Errorf("no directory given")
	}
	if dir == defaultDir(agent) {
		return HomesOf(agent)[0], false, nil
	}
	if label != "" {
		if err := validLabel(label); err != nil {
			return Home{}, false, err
		}
	}
	registered := loadRegistered(agent)
	for _, home := range registered {
		if cleanDir(home.Dir) != dir {
			continue
		}
		if label == "" || label == home.Label {
			return home, false, nil
		}
		return Home{}, false, fmt.Errorf("%s is already registered as %q (labels name the backup folder, so they don't change)", dir, home.Label)
	}
	if label == "" {
		label = uniqueLabel(registered, DeriveLabel(dir))
	} else if labelTaken(registered, label, -1) {
		return Home{}, false, fmt.Errorf("label %q is already used", label)
	}
	home := Home{Agent: agent, Label: label, Dir: dir}
	return home, true, saveRegistered(agent, append(registered, home))
}

// RegisterClaudeDir adds a claude config dir (see RegisterDir).
func RegisterClaudeDir(dir string, label string) (Home, bool, error) {
	return RegisterDir(AgentClaude, dir, label)
}

// UnregisterDir forgets one of an agent's homes by label or dir. Its files
// and its backups are left alone.
func UnregisterDir(agent string, labelOrDir string) (Home, error) {
	if labelOrDir == DefaultHomeLabel || cleanDir(labelOrDir) == defaultDir(agent) {
		return Home{}, fmt.Errorf("the default %s dir cannot be removed", agent)
	}
	registered := loadRegistered(agent)
	for i, home := range registered {
		if home.Label == labelOrDir || cleanDir(home.Dir) == cleanDir(labelOrDir) {
			return home, saveRegistered(agent, append(registered[:i], registered[i+1:]...))
		}
	}
	return Home{}, fmt.Errorf("no registered %s dir %q", agent, labelOrDir)
}

// UnregisterClaudeDir forgets a claude config dir (see UnregisterDir).
func UnregisterClaudeDir(labelOrDir string) (Home, error) {
	return UnregisterDir(AgentClaude, labelOrDir)
}

// NoteActiveClaudeDir registers the config dir of the agent session that
// invoked us (hooks inherit CLAUDE_CONFIG_DIR), so a dir with our hooks
// installed is picked up on its first session whatever it is called.
func NoteActiveClaudeDir() {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" || !dirExists(dir) {
		return
	}
	if _, known := HomeForDir(dir); known {
		return
	}
	_, _, _ = RegisterClaudeDir(dir, "")
}

// DeriveLabel names a dir from its base name with the agents' names and
// separators stripped: ".claude-work" → "work", "acme-codex" → "acme".
// A dir named just "claude" or "codex" takes its parent's name.
func DeriveLabel(dir string) string {
	dir = cleanDir(dir)
	base := strings.ToLower(filepath.Base(dir))
	base = strings.ReplaceAll(strings.ReplaceAll(base, "claude", ""), "codex", "")
	label := sanitizeLabel(base)
	if label == "" {
		label = sanitizeLabel(strings.ToLower(filepath.Base(filepath.Dir(dir))))
	}
	if label == "" || label == DefaultHomeLabel {
		label = "home"
	}
	return label
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func validLabel(label string) error {
	if label == DefaultHomeLabel {
		return fmt.Errorf("%q is reserved for the default dir", label)
	}
	if label != sanitizeLabel(label) {
		return fmt.Errorf("label %q must be lowercase letters, digits and dashes", label)
	}
	return nil
}

func labelTaken(homes []Home, label string, except int) bool {
	for i, home := range homes {
		if i != except && home.Label == label {
			return true
		}
	}
	return false
}

func uniqueLabel(homes []Home, label string) string {
	candidate := label
	for n := 2; labelTaken(homes, candidate, -1); n++ {
		candidate = fmt.Sprintf("%s-%d", label, n)
	}
	return candidate
}

func cleanDir(dir string) string {
	if dir == "" {
		return ""
	}
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(homeDir(), dir[2:])
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Clean(dir)
}
