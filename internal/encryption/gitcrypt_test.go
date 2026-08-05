package encryption

import (
	"testing"
)

func TestUnlockedOnPlainDir(t *testing.T) {
	if Unlocked(t.TempDir()) {
		t.Fatal("a plain directory must not report as git-crypt unlocked")
	}
}

func TestInstalledProbe(t *testing.T) {
	// Answer depends on the machine; the probe itself must not panic and
	// must be consistent across calls.
	if Installed() != Installed() {
		t.Fatal("Installed is not stable")
	}
	t.Setenv("PATH", t.TempDir())
	if Installed() {
		t.Fatal("empty PATH must report git-crypt absent")
	}
}
