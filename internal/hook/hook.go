package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// marker identifies entries this tool manages inside the agent's hook config,
// independent of the binary's install path or name.
const marker = "# session-protect-hook"

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}

	target := "claude"
	if len(args) > 1 {
		target = args[1]
	}
	if target != "claude" {
		fmt.Fprintf(stderr, "hook support is currently implemented for claude only\n")
		return 1
	}

	switch args[0] {
	case "install":
		return install(stdout, stderr)
	case "status":
		return status(stdout)
	case "uninstall":
		return uninstall(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown hook command: %s\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect hook install [claude]")
	fmt.Fprintln(out, "  session-protect hook status [claude]")
	fmt.Fprintln(out, "  session-protect hook uninstall [claude]")
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// hookCommand runs a detached sync so the agent is never blocked, even if a
// sync is slow or the backup root is temporarily unavailable.
func hookCommand() (string, error) {
	binary, err := os.Executable()
	if err == nil {
		binary, err = filepath.EvalSymlinks(binary)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(%s sync >/dev/null 2>&1 &) %s", binary, marker), nil
}

func install(stdout io.Writer, stderr io.Writer) int {
	command, err := hookCommand()
	if err != nil {
		fmt.Fprintf(stderr, "hook install failed: %v\n", err)
		return 1
	}

	path := settingsPath()
	settings, err := loadSettings(path)
	if err != nil {
		fmt.Fprintf(stderr, "hook install failed: %v\n", err)
		return 1
	}

	if hasManagedHook(settings) {
		fmt.Fprintf(stdout, "hook already installed in %s\n", path)
		return 0
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	stop, _ := hooks["Stop"].([]any)
	stop = append(stop, map[string]any{
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	})
	hooks["Stop"] = stop
	settings["hooks"] = hooks

	if err := writeSettings(path, settings); err != nil {
		fmt.Fprintf(stderr, "hook install failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Installed Stop hook in %s\n", path)
	fmt.Fprintln(stdout, "New agent sessions sync after every turn; running sessions pick it up on restart.")
	return 0
}

func status(stdout io.Writer) int {
	settings, err := loadSettings(settingsPath())
	if err == nil && hasManagedHook(settings) {
		fmt.Fprintln(stdout, "hook: installed")
		return 0
	}
	fmt.Fprintln(stdout, "hook: not installed")
	return 1
}

func uninstall(stdout io.Writer, stderr io.Writer) int {
	path := settingsPath()
	settings, err := loadSettings(path)
	if err != nil || !hasManagedHook(settings) {
		fmt.Fprintln(stdout, "hook: not installed")
		return 0
	}

	hooks := settings["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	var kept []any
	for _, entry := range stop {
		if !entryIsManaged(entry) {
			kept = append(kept, entry)
		}
	}
	if len(kept) > 0 {
		hooks["Stop"] = kept
	} else {
		delete(hooks, "Stop")
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}

	if err := writeSettings(path, settings); err != nil {
		fmt.Fprintf(stderr, "hook uninstall failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed Stop hook from %s\n", path)
	return 0
}

func loadSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return settings, nil
}

// writeSettings replaces the settings file atomically, preserving all
// settings this tool does not manage.
func writeSettings(path string, settings map[string]any) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".session-protect.tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hasManagedHook(settings map[string]any) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	for _, entry := range stop {
		if entryIsManaged(entry) {
			return true
		}
	}
	return false
}

func entryIsManaged(entry any) bool {
	entryMap, _ := entry.(map[string]any)
	inner, _ := entryMap["hooks"].([]any)
	for _, hook := range inner {
		hookMap, _ := hook.(map[string]any)
		command, _ := hookMap["command"].(string)
		// Also match unmarked commands invoking this tool's sync, so entries
		// added by hand are adopted rather than duplicated.
		if strings.Contains(command, marker) || strings.Contains(command, "session-protect sync") {
			return true
		}
	}
	return false
}
