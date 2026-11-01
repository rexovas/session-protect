package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func settingsFor(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestInstallStatusUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Existing unrelated settings must survive the round trip.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := []byte(`{"model":"opus","permissions":{"allow":["WebSearch"]}}`)
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), seed, 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"status"}, &out, &errOut); code != 1 {
		t.Fatalf("expected status=1 before install, got %d", code)
	}

	if code := Run([]string{"install"}, &out, &errOut); code != 0 {
		t.Fatalf("install failed: %s", errOut.String())
	}
	settings := settingsFor(t, home)
	if settings["model"] != "opus" {
		t.Fatalf("unrelated settings lost: %+v", settings)
	}
	if !hasManagedHook(settings) {
		t.Fatal("managed hook missing after install")
	}

	// Idempotent: a second install adds nothing.
	if code := Run([]string{"install"}, &out, &errOut); code != 0 {
		t.Fatal("second install failed")
	}
	hooks := settingsFor(t, home)["hooks"].(map[string]any)
	for _, event := range []string{"Stop", "SessionStart"} {
		if entries := hooks[event].([]any); len(entries) != 1 {
			t.Fatalf("expected 1 %s entry after reinstall, got %d", event, len(entries))
		}
	}

	if code := Run([]string{"status"}, &out, &errOut); code != 0 {
		t.Fatal("expected status=0 after install")
	}

	if code := Run([]string{"uninstall"}, &out, &errOut); code != 0 {
		t.Fatalf("uninstall failed: %s", errOut.String())
	}
	settings = settingsFor(t, home)
	if hasManagedHook(settings) {
		t.Fatal("hook still present after uninstall")
	}
	if settings["model"] != "opus" {
		t.Fatalf("unrelated settings lost on uninstall: %+v", settings)
	}
	if _, ok := settings["hooks"]; ok {
		t.Fatalf("empty hooks table should be removed: %+v", settings)
	}
}

func TestInstallCreatesSettingsWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var out, errOut bytes.Buffer
	if code := Run([]string{"install"}, &out, &errOut); code != 0 {
		t.Fatalf("install failed: %s", errOut.String())
	}
	if !hasManagedHook(settingsFor(t, home)) {
		t.Fatal("managed hook missing")
	}
}

func TestRepairRepointsBrokenHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A Homebrew prefix with the cask's versioned binary and stable link.
	prefix := filepath.Join(home, "brew")
	versioned := filepath.Join(prefix, "Caskroom", "session-protect", "1.3.1", "session-protect")
	stable := filepath.Join(prefix, "bin", "session-protect")
	working := filepath.Join(home, "other", "session-protect")
	for _, file := range []string{versioned, working} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versioned, stable); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(home, "deleted", "session-protect")

	settings := map[string]any{"hooks": map[string]any{
		"Stop": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "(" + versioned + " sync >/dev/null 2>&1 &) " + marker}}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo unrelated"}}},
		},
		"SessionStart": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": gone + " guard " + marker}}},
		},
		"PreCompact": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": working + " guard " + marker}}},
		},
	}}
	current := filepath.Join(home, "current", "session-protect")
	if n := repoint(settings, current); n != 2 {
		t.Fatalf("repointed %d commands, want 2", n)
	}

	command := func(event string, i int) string {
		entries := settings["hooks"].(map[string]any)[event].([]any)
		return entries[i].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	}
	if got, want := command("Stop", 0), "("+stable+" sync >/dev/null 2>&1 &) "+marker; got != want {
		t.Errorf("versioned brew binary: got %q, want %q", got, want)
	}
	if got := command("Stop", 1); got != "echo unrelated" {
		t.Errorf("unmanaged hook changed: %q", got)
	}
	if got, want := command("SessionStart", 0), current+" guard "+marker; got != want {
		t.Errorf("missing binary: got %q, want %q", got, want)
	}
	if got, want := command("PreCompact", 0), working+" guard "+marker; got != want {
		t.Errorf("working install must be left alone: got %q", got)
	}
}
