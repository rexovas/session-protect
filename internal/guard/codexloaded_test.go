package guard

import (
	"testing"

	"github.com/rexovas/session-protect/internal/guard/codextest"
)

func TestCodexLoaded(t *testing.T) {
	home := t.TempDir()
	if _, err := CodexLoaded(home); err != ErrNoCodexServer {
		t.Fatalf("no server: %v", err)
	}
	codextest.Server(t, home, []string{"t-open"})
	loaded, err := CodexLoaded(home)
	if err != nil || !loaded["t-open"] || len(loaded) != 1 {
		t.Fatalf("loaded = %v, %v", loaded, err)
	}
}

// Names are set through the home's running server once each thread reads
// back.
func TestSetCodexNames(t *testing.T) {
	home := t.TempDir()
	fake := codextest.Server(t, home, nil)
	if failures := SetCodexNames(home, map[string]string{"t1": "ONE", "t2": "TWO"}); len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	if names := fake.Names(); names["t1"] != "ONE" || names["t2"] != "TWO" {
		t.Fatalf("names = %v", names)
	}
}
