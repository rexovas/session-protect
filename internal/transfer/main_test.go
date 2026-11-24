package transfer

import (
	"os"
	"testing"
)

// TestMain keeps tests from starting codex: restoring thread names talks
// to a codex app-server. Tests that check names install their own seam.
func TestMain(m *testing.M) {
	setCodexNames = func(string, map[string]string) []string { return nil }
	os.Exit(m.Run())
}
