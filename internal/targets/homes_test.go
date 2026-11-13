package targets

import (
	"os"
	"path/filepath"
	"testing"
)

// homesEnv isolates the home dir and the registry file.
func homesEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(home, "cfg", "config.toml"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeriveLabel(t *testing.T) {
	for dir, want := range map[string]string{
		"/u/.claude-work":        "work",
		"/u/.claude_Globex":      "globex",
		"/u/work/acme-claude":    "acme",
		"/u/clients/acme/claude": "acme",
		"/u/cfg-for-Big Co":      "cfg-for-big-co",
		"/u/.claude-default":     "home", // "default" is reserved
	} {
		if got := DeriveLabel(dir); got != want {
			t.Errorf("DeriveLabel(%s) = %q, want %q", dir, got, want)
		}
	}
}

// Dirs register by path whatever their name; the default always leads;
// labels stay unique and fixed; removal forgets only the registration.
func TestRegisterAndListHomes(t *testing.T) {
	home := homesEnv(t)
	if homes := ClaudeHomes(); len(homes) != 1 || !homes[0].IsDefault() || homes[0].Dir != filepath.Join(home, ".claude") {
		t.Fatalf("fresh homes = %+v", homes)
	}
	zeta := mkdir(t, filepath.Join(home, "work", "zeta-config"))
	acme := mkdir(t, filepath.Join(home, "clients", "acme", "claude"))
	other := mkdir(t, filepath.Join(home, "other", "acme", "claude"))

	if h, changed, err := RegisterClaudeDir(zeta, ""); err != nil || !changed || h.Label != "zeta-config" {
		t.Fatalf("register zeta = %+v %v %v", h, changed, err)
	}
	if _, _, err := RegisterClaudeDir(acme, ""); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := RegisterClaudeDir(other, ""); h.Label != "acme-2" {
		t.Errorf("a clashing derived label was not made unique: %q", h.Label)
	}
	if _, changed, err := RegisterClaudeDir(acme, ""); err != nil || changed {
		t.Errorf("re-registering changed something: %v %v", changed, err)
	}
	if _, _, err := RegisterClaudeDir(acme, "renamed"); err == nil {
		t.Error("a registered dir was relabelled")
	}
	if _, _, err := RegisterClaudeDir(mkdir(t, filepath.Join(home, "x")), "default"); err == nil {
		t.Error("the reserved label was accepted")
	}
	if _, changed, _ := RegisterClaudeDir(filepath.Join(home, ".claude"), ""); changed {
		t.Error("the default dir was registered as an extra home")
	}

	homes := ClaudeHomes()
	var labels []string
	for _, h := range homes {
		labels = append(labels, h.Label)
	}
	if got := filepath.Join(labels...); got != filepath.Join("default", "acme", "acme-2", "zeta-config") {
		t.Errorf("homes = %v", labels)
	}
	acmeHome, ok := HomeForDir(acme + "/")
	if !ok || acmeHome.TargetName() != "claude-acme" || acmeHome.Env() != "CLAUDE_CONFIG_DIR="+acme {
		t.Errorf("acme home = %+v %v", acmeHome, ok)
	}
	if homes[0].TargetName() != "claude" || homes[0].Env() != "" {
		t.Errorf("default home = %+v", homes[0])
	}

	if _, err := UnregisterClaudeDir("acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := HomeByLabel("acme"); ok {
		t.Error("removed home still listed")
	}
	if _, err := os.Stat(acme); err != nil {
		t.Error("removing a home touched its files")
	}
	if _, err := UnregisterClaudeDir("default"); err == nil {
		t.Error("the default home was removable")
	}
}

// A hook run inside a session registers that session's config dir.
func TestNoteActiveClaudeDir(t *testing.T) {
	home := homesEnv(t)
	NoteActiveClaudeDir() // unset: nothing
	if len(ClaudeHomes()) != 1 {
		t.Fatal("registered something with CLAUDE_CONFIG_DIR unset")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "missing"))
	NoteActiveClaudeDir()
	if len(ClaudeHomes()) != 1 {
		t.Fatal("registered a dir that does not exist")
	}
	dir := mkdir(t, filepath.Join(home, "cfg-client"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	NoteActiveClaudeDir()
	NoteActiveClaudeDir()
	if h, ok := HomeForDir(dir); !ok || h.Label != "cfg-client" || len(ClaudeHomes()) != 2 {
		t.Fatalf("active dir not registered once: %+v", ClaudeHomes())
	}
	if ActiveClaudeDir() != dir {
		t.Errorf("ActiveClaudeDir = %s", ActiveClaudeDir())
	}
	targets := DetectAll()
	if len(targets) != 3 || targets[1].Name != "claude-cfg-client" || targets[1].Agent != "claude" || targets[2].Agent != "codex" {
		t.Errorf("DetectAll = %+v", targets)
	}
}
