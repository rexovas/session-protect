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

// managedHooks returns the hook entries this tool installs per event: a
// detached sync at every turn end, and the synchronous reopen guard at
// session start (it must run inline to emit its warning, and is fast).
func managedHooks() (map[string]string, error) {
	binary, err := os.Executable()
	if err == nil {
		binary, err = filepath.EvalSymlinks(binary)
	}
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Stop":         fmt.Sprintf("(%s sync >/dev/null 2>&1 &) %s", binary, marker),
		"SessionStart": fmt.Sprintf("%s guard %s", binary, marker),
	}, nil
}

func install(stdout io.Writer, stderr io.Writer) int {
	commands, err := managedHooks()
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

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	installed := 0
	for _, event := range []string{"Stop", "SessionStart"} {
		entries, _ := hooks[event].([]any)
		if managedEntryPresent(entries) {
			continue
		}
		hooks[event] = append(entries, map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": commands[event]},
			},
		})
		installed++
	}
	if installed == 0 {
		fmt.Fprintf(stdout, "hooks already installed in %s\n", path)
		return 0
	}
	settings["hooks"] = hooks

	if err := writeSettings(path, settings); err != nil {
		fmt.Fprintf(stderr, "hook install failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Installed hooks in %s\n", path)
	fmt.Fprintln(stdout, "  Stop          sync after every turn")
	fmt.Fprintln(stdout, "  SessionStart  warn when a session is already open elsewhere")
	fmt.Fprintln(stdout, "Running sessions pick hooks up on restart.")
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
	for event, raw := range hooks {
		entries, _ := raw.([]any)
		var kept []any
		for _, entry := range entries {
			if !entryIsManaged(entry) {
				kept = append(kept, entry)
			}
		}
		if len(kept) > 0 {
			hooks[event] = kept
		} else {
			delete(hooks, event)
		}
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
	for _, raw := range hooks {
		entries, _ := raw.([]any)
		if managedEntryPresent(entries) {
			return true
		}
	}
	return false
}

func managedEntryPresent(entries []any) bool {
	for _, entry := range entries {
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
