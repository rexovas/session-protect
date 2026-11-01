// Package binpath names the binary that installed hooks and the backup agent
// should run.
package binpath

import (
	"os"
	"path/filepath"
	"strings"
)

// Current is the running binary as hooks and the agent should record it:
// symlinks resolved, then mapped to Homebrew's stable link when the binary
// sits in a versioned Homebrew directory (see Stable).
func Current() (string, error) {
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return "", err
	}
	return Stable(binary), nil
}

// Stable maps a binary inside a versioned Homebrew directory
// (<prefix>/Caskroom/session-protect/<version>/ or
// <prefix>/Cellar/session-protect/<version>/bin/) to Homebrew's stable link,
// <prefix>/bin/session-protect, which follows upgrades. The versioned path is
// removed by the next upgrade or cleanup, which would leave anything that
// recorded it pointing at nothing. Any other path, or one whose prefix has no
// such link, comes back unchanged.
func Stable(binary string) string {
	sep := string(os.PathSeparator)
	for _, dir := range []string{"Caskroom", "Cellar"} {
		marker := sep + dir + sep + "session-protect" + sep
		i := strings.Index(binary, marker)
		if i < 0 {
			continue
		}
		link := filepath.Join(binary[:i], "bin", "session-protect")
		if _, err := os.Lstat(link); err == nil {
			return link
		}
	}
	return binary
}
