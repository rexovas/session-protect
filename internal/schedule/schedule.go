package schedule

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rexovas/session-protect/internal/config"
)

const label = "com.session-protect.backup"

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(stderr, "scheduling is currently implemented for macOS only")
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "schedule failed: %v\n", err)
		return 1
	}

	switch args[0] {
	case "install":
		return install(cfg, stdout, stderr)
	case "status":
		return status(stdout)
	case "uninstall":
		return uninstall(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown schedule command: %s\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect schedule install")
	fmt.Fprintln(out, "  session-protect schedule status")
	fmt.Fprintln(out, "  session-protect schedule uninstall")
}

func install(cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	binary, err := os.Executable()
	if err == nil {
		binary, err = filepath.EvalSymlinks(binary)
	}
	if err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	hour, minute, err := cfg.Schedule.Clock()
	if err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", "session-protect", "backup.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	path := plistPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(Plist(binary, hour, minute, logPath)), 0o600); err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, path).Run() // reload cleanly if present
	if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		fmt.Fprintf(stderr, "launchctl bootstrap failed: %v: %s\n", err, strings.TrimSpace(string(out)))
		return 1
	}

	fmt.Fprintf(stdout, "Installed daily backup schedule\n")
	fmt.Fprintf(stdout, "  agent   %s\n", path)
	fmt.Fprintf(stdout, "  runs    daily at %02d:%02d\n", hour, minute)
	fmt.Fprintf(stdout, "  binary  %s\n", binary)
	fmt.Fprintf(stdout, "  log     %s\n", logPath)
	return 0
}

func status(stdout io.Writer) int {
	home, _ := os.UserHomeDir()
	path := plistPath(home)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintln(stdout, "schedule: not installed")
		return 1
	}
	domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	if err := exec.Command("launchctl", "print", domain).Run(); err != nil {
		fmt.Fprintf(stdout, "schedule: agent file present but not loaded (%s)\n", path)
		return 1
	}
	fmt.Fprintf(stdout, "schedule: installed and loaded (%s)\n", path)
	return 0
}

func uninstall(stdout io.Writer, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	path := plistPath(home)
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "schedule uninstall failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Removed backup schedule")
	return 0
}

func plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// Plist renders the LaunchAgent definition.
func Plist(binary string, hour int, minute int, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>backup</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>%d</integer>
		<key>Minute</key>
		<integer>%d</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, binary, hour, minute, logPath, logPath)
}
