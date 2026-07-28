package plan

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

type Plan struct {
	ConfigPath string           `json:"config_path"`
	BackupRoot string           `json:"backup_root"`
	Topology   string           `json:"topology"`
	Targets    []targets.Target `json:"targets"`
	Warning    string           `json:"warning,omitempty"`
}

func Build() Plan {
	cfg, err := config.Load()
	warning := ""
	if err != nil {
		cfg = config.Defaults()
		warning = fmt.Sprintf("config ignored: %v", err)
	}
	return Plan{
		ConfigPath: cfg.ConfigPath,
		BackupRoot: cfg.BackupRoot,
		Topology:   cfg.Topology,
		Targets:    cfg.ResolveTargets(),
		Warning:    warning,
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
	fmt.Fprintf(out, "Topology: %s\n", p.Topology)
	if p.Warning != "" {
		fmt.Fprintf(out, "Warning:  %s\n", p.Warning)
	}
	fmt.Fprintln(out)

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
