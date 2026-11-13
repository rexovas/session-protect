package browse

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the package's tests off the machine's own session-protect
// state: the registered claude dirs live beside the config file, so tests
// that don't set up their own config would otherwise see the real ones.
func TestMain(m *testing.M) {
	if os.Getenv("SESSION_PROTECT_CONFIG") == "" {
		dir, err := os.MkdirTemp("", "sp-browse-test")
		if err == nil {
			os.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(dir, "config.toml"))
			code := m.Run()
			os.RemoveAll(dir)
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}
