package schedule

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlist(t *testing.T) {
	plist := Plist("/usr/local/bin/session-protect", 12, 30, "/tmp/backup.log")
	for _, want := range []string{
		"<string>com.session-protect.backup</string>",
		"<string>/usr/local/bin/session-protect</string>",
		"<string>backup</string>",
		"<integer>12</integer>",
		"<integer>30</integer>",
		"<string>/tmp/backup.log</string>",
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %s", want)
		}
	}
}

func TestPlistPath(t *testing.T) {
	got := plistPath("/Users/x")
	if !strings.HasSuffix(got, "Library/LaunchAgents/com.session-protect.backup.plist") {
		t.Fatalf("plistPath = %s", got)
	}
}

func TestRunArgHandling(t *testing.T) {
	var out, errOut strings.Builder
	if code := Run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatal("help should exit 0")
	}
	// Non-darwin platforms refuse before subcommand dispatch.
	want := 2
	if runtime.GOOS != "darwin" {
		want = 1
	}
	if code := Run([]string{"bogus"}, &out, &errOut); code != want {
		t.Fatalf("unknown subcommand exit = %d, want %d", code, want)
	}
}

func TestParseLastExit(t *testing.T) {
	for _, tc := range []struct {
		printed string
		code    int
		ok      bool
	}{
		{"\truns = 47\n\tlast exit code = 78: EX_CONFIG\n", 78, true},
		{"\tlast exit code = 0\n", 0, true},
		{"\tlast exit code = (never exited)\n", 0, false},
		{"\tstate = not running\n", 0, false},
	} {
		code, ok := parseLastExit(tc.printed)
		if code != tc.code || ok != tc.ok {
			t.Errorf("parseLastExit(%q) = %d, %v; want %d, %v", tc.printed, code, ok, tc.code, tc.ok)
		}
	}
}

func TestReloadWithoutAgentDoesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installed, err := Reload()
	if err != nil || installed {
		t.Fatalf("Reload with no agent = %v, %v; want false, nil", installed, err)
	}
	if agent := Inspect(); agent.Installed || agent.Loaded {
		t.Fatalf("Inspect with no agent = %+v", agent)
	}
}

// brewPrefix lays out a Homebrew prefix with session-protect installed as a
// cask at version 1.3.1 and returns it.
func brewPrefix(t *testing.T) string {
	t.Helper()
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
	if err := os.Symlink(versioned, filepath.Join(prefix, "bin", "session-protect")); err != nil {
		t.Fatal(err)
	}
	return prefix
}

func TestRepairBrewBinary(t *testing.T) {
	prefix := brewPrefix(t)
	versioned := filepath.Join(prefix, "Caskroom", "session-protect", "1.3.0", "session-protect")
	path := filepath.Join(t.TempDir(), "agent.plist")
	if err := os.WriteFile(path, []byte(Plist(versioned, 12, 0, "/tmp/backup.log")), 0o600); err != nil {
		t.Fatal(err)
	}
	if repaired, err := repairBrewBinary(path); err != nil || !repaired {
		t.Fatalf("repairBrewBinary = %v, %v; want true, nil", repaired, err)
	}
	data, _ := os.ReadFile(path)
	if got, want := plistBinary(string(data)), filepath.Join(prefix, "bin", "session-protect"); got != want {
		t.Fatalf("repaired binary = %q, want %q", got, want)
	}

	// An agent already pointing elsewhere is left byte for byte.
	local := Plist("/Users/x/.local/bin/session-protect", 12, 0, "/tmp/backup.log")
	if err := os.WriteFile(path, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	if repaired, err := repairBrewBinary(path); err != nil || repaired {
		t.Fatalf("repairBrewBinary = %v, %v; want false, nil", repaired, err)
	}
	if data, _ := os.ReadFile(path); string(data) != local {
		t.Fatal("non-brew agent was rewritten")
	}
}

func TestIdentityAndStaleness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binary := filepath.Join(home, "session-protect")
	if err := os.WriteFile(binary, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "agent.plist")
	if err := os.WriteFile(plist, []byte(Plist(binary, 12, 0, "/tmp/backup.log")), 0o600); err != nil {
		t.Fatal(err)
	}

	before := identity(plist)
	if before == "" {
		t.Fatal("identity of an existing binary is empty")
	}
	recordLoaded(plist)
	if recorded, _ := os.ReadFile(stampPath(home)); string(recorded) != before {
		t.Fatalf("recorded %q, want %q", recorded, before)
	}

	// Replacing the binary changes its identity, which is what makes
	// ReloadIfStale reload.
	if err := os.WriteFile(binary, []byte("v2 longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if identity(plist) == before {
		t.Fatal("identity unchanged after the binary was replaced")
	}

	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if identity(plist) != "" {
		t.Fatal("identity of a missing binary must be empty")
	}
}

func TestReloadIfStaleWithoutAgentDoesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if reloaded, err := ReloadIfStale(); reloaded || err != nil {
		t.Fatalf("ReloadIfStale with no agent = %v, %v", reloaded, err)
	}
}
