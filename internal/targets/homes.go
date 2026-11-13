package targets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A ClaudeHome is one claude config dir. The agent reads its config dir
// from CLAUDE_CONFIG_DIR (default ~/.claude), and each dir keeps its own
// login, settings and sessions, so people run several to separate
// accounts or clients. ~/.claude is always present as "default"; others
// are registered by path, whatever they are named.
type ClaudeHome struct {
	Label string `json:"label"`
	Dir   string `json:"dir"`
}

// DefaultHomeLabel names ~/.claude.
const DefaultHomeLabel = "default"

// IsDefault reports whether this is ~/.claude.
func (h ClaudeHome) IsDefault() bool { return h.Label == DefaultHomeLabel }

// TargetName is the backup target (and repo prefix) for the home: the
// default keeps the historical "claude" so existing backups are unchanged.
func (h ClaudeHome) TargetName() string {
	if h.IsDefault() {
		return "claude"
	}
	return "claude-" + h.Label
}

// Env is the environment entry that makes the agent use this home, or ""
// for the default (which must run with CLAUDE_CONFIG_DIR unset).
func (h ClaudeHome) Env() string {
	if h.IsDefault() {
		return ""
	}
	return "CLAUDE_CONFIG_DIR=" + h.Dir
}

// DefaultClaudeDir is the agent's config dir when CLAUDE_CONFIG_DIR is unset.
func DefaultClaudeDir() string {
	return filepath.Join(homeDir(), ".claude")
}

// ActiveClaudeDir is the config dir an agent started from this process's
// environment would use.
func ActiveClaudeDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return cleanDir(dir)
	}
	return DefaultClaudeDir()
}

// homesFile lists the registered extra homes. It sits beside the config
// file so a test (or a second install) pointing SESSION_PROTECT_CONFIG
// elsewhere gets its own list.
func homesFile() string {
	if override := os.Getenv("SESSION_PROTECT_CONFIG"); override != "" {
		return filepath.Join(filepath.Dir(override), "claude-dirs.json")
	}
	return filepath.Join(homeDir(), ".config", "session-protect", "claude-dirs.json")
}

func loadRegistered() []ClaudeHome {
	data, err := os.ReadFile(homesFile())
	if err != nil {
		return nil
	}
	var homes []ClaudeHome
	if json.Unmarshal(data, &homes) != nil {
		return nil
	}
	return homes
}

func saveRegistered(homes []ClaudeHome) error {
	sort.Slice(homes, func(i, j int) bool { return homes[i].Label < homes[j].Label })
	data, err := json.MarshalIndent(homes, "", "  ")
	if err != nil {
		return err
	}
	path := homesFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClaudeHomes lists every home: the default first, then registered ones by
// label. Registered homes stay listed after their dir is removed, so their
// backups remain browsable and restorable.
func ClaudeHomes() []ClaudeHome {
	homes := []ClaudeHome{{Label: DefaultHomeLabel, Dir: DefaultClaudeDir()}}
	defaultDir := DefaultClaudeDir()
	for _, home := range loadRegistered() {
		if home.Label == "" || home.Label == DefaultHomeLabel || cleanDir(home.Dir) == defaultDir {
			continue
		}
		home.Dir = cleanDir(home.Dir)
		homes = append(homes, home)
	}
	sort.SliceStable(homes[1:], func(i, j int) bool { return homes[1+i].Label < homes[1+j].Label })
	return homes
}

// HomeByLabel finds a home by its label.
func HomeByLabel(label string) (ClaudeHome, bool) {
	for _, home := range ClaudeHomes() {
		if home.Label == label {
			return home, true
		}
	}
	return ClaudeHome{}, false
}

// HomeForDir finds the home whose dir is dir.
func HomeForDir(dir string) (ClaudeHome, bool) {
	dir = cleanDir(dir)
	for _, home := range ClaudeHomes() {
		if home.Dir == dir {
			return home, true
		}
	}
	return ClaudeHome{}, false
}

// HomeFor finds a home by label, falling back to the default.
func HomeFor(label string) ClaudeHome {
	if home, ok := HomeByLabel(label); ok {
		return home
	}
	return ClaudeHomes()[0]
}

// RegisterClaudeDir adds dir as a home. An empty label is derived from the
// dir's name. Registering a dir already known returns its existing entry.
// A label is fixed once set: it names the home's backup folder. It
// reports whether anything changed.
func RegisterClaudeDir(dir string, label string) (ClaudeHome, bool, error) {
	dir = cleanDir(dir)
	if dir == "" {
		return ClaudeHome{}, false, fmt.Errorf("no directory given")
	}
	if dir == DefaultClaudeDir() {
		return ClaudeHomes()[0], false, nil
	}
	if label != "" {
		if err := validLabel(label); err != nil {
			return ClaudeHome{}, false, err
		}
	}
	registered := loadRegistered()
	for _, home := range registered {
		if cleanDir(home.Dir) != dir {
			continue
		}
		if label == "" || label == home.Label {
			return home, false, nil
		}
		return ClaudeHome{}, false, fmt.Errorf("%s is already registered as %q (labels name the backup folder, so they don't change)", dir, home.Label)
	}
	if label == "" {
		label = uniqueLabel(registered, DeriveLabel(dir))
	} else if labelTaken(registered, label, -1) {
		return ClaudeHome{}, false, fmt.Errorf("label %q is already used", label)
	}
	home := ClaudeHome{Label: label, Dir: dir}
	return home, true, saveRegistered(append(registered, home))
}

// UnregisterClaudeDir forgets a home by label or dir. Its files and its
// backups are left alone.
func UnregisterClaudeDir(labelOrDir string) (ClaudeHome, error) {
	if labelOrDir == DefaultHomeLabel || cleanDir(labelOrDir) == DefaultClaudeDir() {
		return ClaudeHome{}, fmt.Errorf("the default claude dir cannot be removed")
	}
	registered := loadRegistered()
	for i, home := range registered {
		if home.Label == labelOrDir || cleanDir(home.Dir) == cleanDir(labelOrDir) {
			return home, saveRegistered(append(registered[:i], registered[i+1:]...))
		}
	}
	return ClaudeHome{}, fmt.Errorf("no registered claude dir %q", labelOrDir)
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

// DeriveLabel names a dir from its base name with the agent's name and
// separators stripped: ".claude-work" → "work", "acme-claude" → "acme".
// A dir named just "claude" takes its parent's name.
func DeriveLabel(dir string) string {
	dir = cleanDir(dir)
	base := strings.ToLower(filepath.Base(dir))
	label := sanitizeLabel(strings.ReplaceAll(base, "claude", ""))
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
		return fmt.Errorf("%q is reserved for ~/.claude", label)
	}
	if label != sanitizeLabel(label) {
		return fmt.Errorf("label %q must be lowercase letters, digits and dashes", label)
	}
	return nil
}

func labelTaken(homes []ClaudeHome, label string, except int) bool {
	for i, home := range homes {
		if i != except && home.Label == label {
			return true
		}
	}
	return false
}

func uniqueLabel(homes []ClaudeHome, label string) string {
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
