package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/rexovas/session-protect/internal/targets"
)

// Config is the effective configuration: platform defaults overlaid with the
// user's config file when one exists. All paths are absolute after Load.
type Config struct {
	ConfigPath string `toml:"-" json:"config_path"`
	BackupRoot string `toml:"backup_root" json:"backup_root"`
	Topology   string `toml:"topology" json:"topology"`

	Encryption Encryption              `toml:"encryption" json:"encryption"`
	Schedule   Schedule                `toml:"schedule" json:"schedule"`
	Assist     Assist                  `toml:"assist" json:"assist"`
	Update     Update                  `toml:"update" json:"update"`
	Targets    map[string]TargetConfig `toml:"targets" json:"targets"`
}

// Assist configures the optional AI find backend. No vendor is required
// or bundled: auto probes a local ollama server first, then a claude CLI
// on PATH, and the feature hides entirely when neither exists.
type Assist struct {
	Backend string `toml:"backend" json:"backend"` // auto | ollama | claude | codex | none
	// Model is the ollama model to use; empty picks the first installed.
	Model string `toml:"model" json:"model,omitempty"`
	// ClaudeModel is passed to the claude CLI backend; empty means a
	// cheap capable default (sonnet), never the user's session default.
	ClaudeModel string `toml:"claude_model" json:"claude_model,omitempty"`
	// URL is the ollama server address.
	URL string `toml:"url" json:"url,omitempty"`
}

// Update controls the launch-time new-version check. It is the one
// outbound network call the tool ever makes (GitHub's releases API) and
// can be disabled entirely.
type Update struct {
	Check *bool `toml:"check" json:"check,omitempty"` // default true
}

// CheckEnabled reports whether the update check may run.
func (u Update) CheckEnabled() bool {
	return u.Check == nil || *u.Check
}

type Schedule struct {
	// Time is the local HH:MM at which the daily scheduled backup runs.
	Time string `toml:"time" json:"time"`
}

type Encryption struct {
	Mode string `toml:"mode" json:"mode"` // git-crypt | none
	// KeyPath is where the git-crypt recovery key is exported on first
	// initialization and looked for by doctor.
	KeyPath string `toml:"key_path" json:"key_path,omitempty"`
}

type TargetConfig struct {
	Enabled *bool    `toml:"enabled" json:"enabled,omitempty"`
	Source  string   `toml:"source" json:"source,omitempty"`
	Mode    string   `toml:"mode" json:"mode,omitempty"`
	Include []string `toml:"include" json:"include,omitempty"`
	Exclude []string `toml:"exclude" json:"exclude,omitempty"`
}

// Path returns the config file location without loading it.
func Path() string {
	if override := os.Getenv("SESSION_PROTECT_CONFIG"); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "session-protect", "config.toml")
}

// Load returns defaults overlaid with the config file when present. A missing
// file is not an error; a malformed file is.
func Load() (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(cfg.ConfigPath)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Defaults(), fmt.Errorf("parse %s: %w", cfg.ConfigPath, err)
	}
	cfg.BackupRoot = expandHome(cfg.BackupRoot)
	cfg.Encryption.KeyPath = expandHome(cfg.Encryption.KeyPath)
	for name, target := range cfg.Targets {
		target.Source = expandHome(target.Source)
		cfg.Targets[name] = target
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		ConfigPath: Path(),
		BackupRoot: defaultBackupRoot(home),
		Topology:   "combined",
		// Local backups default to unencrypted because they mirror data the
		// agents already store unencrypted on the same disk. Encryption
		// matters once backups leave the machine; git-crypt is opt-in.
		Encryption: Encryption{
			Mode:    "none",
			KeyPath: filepath.Join(home, ".config", "session-protect", "git-crypt.key"),
		},
		Schedule: Schedule{Time: "12:00"},
		Assist:   Assist{Backend: "auto", URL: "http://localhost:11434"},
		Targets:  map[string]TargetConfig{},
	}
}

// ResolveTargets applies config overrides to the built-in target definitions
// and drops targets the user disabled.
func (c Config) ResolveTargets() []targets.Target {
	resolved := make([]targets.Target, 0, 2)
	for _, target := range targets.DetectAll() {
		override, ok := c.Targets[target.Name]
		if ok {
			if override.Enabled != nil && !*override.Enabled {
				continue
			}
			if override.Source != "" {
				target.Source = override.Source
				target.Detected = dirExists(target.Source)
			}
			if override.Mode != "" {
				target.Mode = override.Mode
			}
			if len(override.Include) > 0 {
				target.Include = override.Include
			}
			if len(override.Exclude) > 0 {
				target.Exclude = override.Exclude
			}
		}
		resolved = append(resolved, target)
	}
	return resolved
}

// RepoFor returns the local backup repo path and the prefix inside it for a
// target, following the destination topology.
func (c Config) RepoFor(target string) (repo string, prefix string) {
	if c.Topology == "per-target" {
		return filepath.Join(c.BackupRoot, target), ""
	}
	return filepath.Join(c.BackupRoot, "all"), target
}

func (c Config) validate() error {
	switch c.Topology {
	case "combined", "per-target":
	default:
		return fmt.Errorf("invalid topology %q (want combined or per-target)", c.Topology)
	}
	switch c.Encryption.Mode {
	case "git-crypt", "none":
	default:
		return fmt.Errorf("invalid encryption.mode %q (want git-crypt or none)", c.Encryption.Mode)
	}
	if _, _, err := c.Schedule.Clock(); err != nil {
		return err
	}
	switch c.Assist.Backend {
	case "auto", "ollama", "claude", "codex", "none", "":
	default:
		return fmt.Errorf("invalid assist.backend %q (want auto, ollama, claude, codex, or none)", c.Assist.Backend)
	}
	return nil
}

// Clock parses the schedule time into hour and minute.
func (s Schedule) Clock() (hour int, minute int, err error) {
	if _, scanErr := fmt.Sscanf(s.Time, "%d:%d", &hour, &minute); scanErr != nil {
		return 0, 0, fmt.Errorf("invalid schedule.time %q (want HH:MM)", s.Time)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid schedule.time %q (want HH:MM)", s.Time)
	}
	return hour, minute, nil
}

func defaultBackupRoot(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "SessionProtect")
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "SessionProtect")
		}
		return filepath.Join(home, "AppData", "Local", "SessionProtect")
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "session-protect")
		}
		return filepath.Join(home, ".local", "share", "session-protect")
	}
}

func expandHome(path string) string {
	if path == "~" || len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
