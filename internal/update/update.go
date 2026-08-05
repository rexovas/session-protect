package update

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/version"
)

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--check" {
		return check(stdout)
	}
	if len(args) > 0 {
		fmt.Fprintf(stderr, "unknown update option: %s\n", args[0])
		return 2
	}

	if version.Channel == "release" {
		return selfUpdate(stdout, stderr)
	}
	if !sourceUpdateAvailable() {
		fmt.Fprintln(stderr, "session-protect update is not configured for this binary")
		fmt.Fprintln(stderr, "Install with scripts/install.sh first so the binary records its source checkout and install prefix.")
		return 1
	}

	installer := filepath.Join(version.SourceDir, "scripts", "install.sh")
	cmd := exec.Command(installer, "--prefix", version.InstallPrefix)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "update failed: %v\n", err)
		return 1
	}

	return 0
}

func check(stdout io.Writer) int {
	fmt.Fprintln(stdout, "SessionProtect update")
	fmt.Fprintf(stdout, "  Channel        %s\n", version.Channel)
	fmt.Fprintf(stdout, "  Version        %s\n", version.Version)
	if version.Channel == "release" {
		current, latest, newer, err := NewerAvailable()
		switch {
		case err != nil:
			fmt.Fprintf(stdout, "  Status         check failed (%v)\n", err)
		case newer:
			fmt.Fprintf(stdout, "  Status         %s available (current %s) — run: session-protect update\n", latest, current)
		default:
			fmt.Fprintf(stdout, "  Status         up to date (latest %s)\n", latest)
		}
		return 0
	}
	fmt.Fprintf(stdout, "  Source         %s\n", version.SourceDir)
	fmt.Fprintf(stdout, "  Install prefix %s\n", version.InstallPrefix)
	if sourceUpdateAvailable() {
		fmt.Fprintln(stdout, "  Status         available")
	} else {
		fmt.Fprintln(stdout, "  Status         not configured")
	}
	return 0
}

// selfUpdate replaces the running release binary with the latest one.
func selfUpdate(stdout io.Writer, stderr io.Writer) int {
	current, latest, newer, err := NewerAvailable()
	if err != nil {
		fmt.Fprintf(stderr, "update check failed: %v\n", err)
		return 1
	}
	if !newer {
		fmt.Fprintf(stdout, "Already up to date (%s).\n", current)
		return 0
	}
	fmt.Fprintf(stdout, "Updating %s → %s …\n", current, latest)
	path, err := Apply(latest)
	if err != nil {
		fmt.Fprintf(stderr, "update failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Updated %s to %s.\n", path, latest)
	return 0
}

func sourceUpdateAvailable() bool {
	if version.Channel != "source" || version.SourceDir == "" || version.SourceDir == "unknown" {
		return false
	}
	if version.InstallPrefix == "" || version.InstallPrefix == "unknown" {
		return false
	}
	installer := filepath.Join(version.SourceDir, "scripts", "install.sh")
	info, err := os.Stat(installer)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0111 != 0
}
