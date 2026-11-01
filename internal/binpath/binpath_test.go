package binpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStable(t *testing.T) {
	prefix := t.TempDir()
	versioned := filepath.Join(prefix, "Caskroom", "session-protect", "1.3.1", "session-protect")
	for _, dir := range []string{filepath.Dir(versioned), filepath.Join(prefix, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(versioned, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(prefix, "bin", "session-protect")
	if err := os.Symlink(versioned, stable); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ in, want string }{
		{versioned, stable},
		{filepath.Join(prefix, "Cellar", "session-protect", "1.3.0", "bin", "session-protect"), stable},
		{"/Users/x/.local/bin/session-protect", "/Users/x/.local/bin/session-protect"},
		// No stable link to switch to: keep the path.
		{"/nowhere/Caskroom/session-protect/1.3.1/session-protect", "/nowhere/Caskroom/session-protect/1.3.1/session-protect"},
	} {
		if got := Stable(tc.in); got != tc.want {
			t.Errorf("Stable(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
