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
	Targets    map[string]TargetConfig `toml:"targets" json:"targets"`
}

type Encryption struct {
	Mode string `toml:"mode" json:"mode"` // git-crypt | none
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
		Encryption: Encryption{Mode: "git-crypt"},
		Targets:    map[string]TargetConfig{},
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
	return nil
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
