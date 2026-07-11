package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/targets"
)

type Plan struct {
	ConfigPath string           `json:"config_path"`
	BackupRoot string           `json:"backup_root"`
	Topology   string           `json:"topology"`
	Targets    []targets.Target `json:"targets"`
}

func Build() Plan {
	home, _ := os.UserHomeDir()
	return Plan{
		ConfigPath: filepath.Join(home, ".config", "session-protect", "config.toml"),
		BackupRoot: defaultBackupRoot(home),
		Topology:   "combined",
		Targets:    targets.DetectAll(),
	}
}

func Print(out io.Writer, asJSON bool) int {
	p := Build()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(p); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(out, "Config:   %s\n", p.ConfigPath)
	fmt.Fprintf(out, "Root:     %s\n", p.BackupRoot)
	fmt.Fprintf(out, "Topology: %s\n\n", p.Topology)

	for _, target := range p.Targets {
		state := "detected"
		if !target.Detected {
			state = "missing"
		}
		fmt.Fprintf(out, "%s (%s)\n", target.Name, state)
		fmt.Fprintf(out, "  source:  %s\n", target.Source)
		fmt.Fprintf(out, "  mode:    %s\n", target.Mode)
		fmt.Fprintln(out, "  include:")
		for _, item := range target.Include {
			fmt.Fprintf(out, "    - %s\n", item)
		}
		fmt.Fprintln(out, "  exclude:")
		for _, item := range target.Exclude {
			fmt.Fprintf(out, "    - %s\n", item)
		}
		for _, note := range target.Notes {
			fmt.Fprintf(out, "  note:    %s\n", note)
		}
		fmt.Fprintln(out)
	}
	return 0
}

func defaultBackupRoot(home string) string {
	switch runtimeGOOS() {
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
