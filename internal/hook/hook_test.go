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
