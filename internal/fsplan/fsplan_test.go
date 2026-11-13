package fsplan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rexovas/session-protect/internal/targets"
)

func writeFile(t *testing.T, root string, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rels(files []File) map[string]bool {
	out := map[string]bool{}
	for _, file := range files {
		out[file.Rel] = true
	}
	return out
}

func TestBuildSelectsIncludesAndAppliesExcludes(t *testing.T) {
	source := t.TempDir()
	for _, rel := range []string{
		"projects/p1/s1.jsonl",
		"projects/p1/sub/deep.jsonl",
		"projects/tmp/hidden.jsonl", // under an excluded dir name at any depth
		"projects/auth.json",        // excluded basename at any depth
		"history.jsonl",
		"settings.json",
		"auth.json",
		"memories_a.sqlite",
		"logs_a.sqlite-wal", // excluded by glob
		"cache/c.bin",
		"unrelated.txt", // never included
	} {
		writeFile(t, source, rel)
	}

	target := targets.Target{
		Name:    "test",
		Source:  source,
		Include: []string{"projects/", "history.jsonl", "settings.json", "memories_*.sqlite"},
		Exclude: []string{"auth.json", "tmp/", "cache/", "logs*.sqlite*"},
	}

	files, err := Build(target)
	if err != nil {
		t.Fatal(err)
	}
	got := rels(files)

	for _, want := range []string{
		"projects/p1/s1.jsonl",
		"projects/p1/sub/deep.jsonl",
		"history.jsonl",
		"settings.json",
		"memories_a.sqlite",
	} {
		if !got[want] {
			t.Errorf("missing expected file %q (got %v)", want, got)
		}
	}
	for _, banned := range []string{
		"projects/tmp/hidden.jsonl",
		"projects/auth.json",
		"auth.json",
		"logs_a.sqlite-wal",
		"cache/c.bin",
		"unrelated.txt",
	} {
		if got[banned] {
			t.Errorf("unexpected file %q selected", banned)
		}
	}
}

func TestBuildMissingIncludesAreSkipped(t *testing.T) {
	source := t.TempDir()
	target := targets.Target{
		Name:    "empty",
		Source:  source,
		Include: []string{"projects/", "history.jsonl"},
	}
	files, err := Build(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty plan, got %v", files)
	}
}

func TestBuildConfigEntriesAreCappedAndExcluded(t *testing.T) {
	source := t.TempDir()
	for _, rel := range []string{
		"projects/p/synced/s.jsonl", // session under a dir named like a config exclude
		"config.toml",
		"skills/mine/SKILL.md",
		"skills/synced/theirs/SKILL.md", // config-only exclude
		"skills/cache/x.bin",            // target-wide exclude still applies
	} {
		writeFile(t, source, rel)
	}
	big := filepath.Join(source, "skills", "mine", "big.bin")
	if err := os.WriteFile(big, make([]byte, ConfigMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := Build(targets.Target{
		Name:          "test",
		Source:        source,
		Include:       []string{"projects/"},
		Exclude:       []string{"cache/"},
		Config:        []string{"config.toml", "skills/", "missing.json"},
		ConfigExclude: []string{"synced/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rels(files)
	for _, want := range []string{"projects/p/synced/s.jsonl", "config.toml", "skills/mine/SKILL.md"} {
		if !got[want] {
			t.Errorf("missing expected file %q (got %v)", want, got)
		}
	}
	for _, banned := range []string{"skills/synced/theirs/SKILL.md", "skills/cache/x.bin", "skills/mine/big.bin"} {
		if got[banned] {
			t.Errorf("unexpected file %q selected", banned)
		}
	}
}

// A config dir that links shared folders and files in from elsewhere is
// backed up as real files under its own paths; link cycles end.
func TestBuildFollowsSymlinks(t *testing.T) {
	shared := t.TempDir()
	writeFile(t, shared, "hooks/guard.py")
	writeFile(t, shared, "hooks/nested/deep.sh")
	writeFile(t, shared, "skills/tool/SKILL.md")
	writeFile(t, shared, "statusline.sh")
	source := t.TempDir()
	writeFile(t, source, "skills/own/SKILL.md")
	for link, to := range map[string]string{
		"hooks":                 "hooks",
		"skills/shared":         "skills/tool",
		"statusline-command.sh": "statusline.sh",
	} {
		if err := os.Symlink(filepath.Join(shared, to), filepath.Join(source, link)); err != nil {
			t.Fatal(err)
		}
	}
	// A cycle: a link back up to its own parent.
	if err := os.Symlink(filepath.Join(source, "skills"), filepath.Join(source, "skills", "loop")); err != nil {
		t.Fatal(err)
	}

	files, err := Build(targets.Target{Source: source, Config: []string{"hooks/", "skills/", "statusline*"}})
	if err != nil {
		t.Fatal(err)
	}
	got := rels(files)
	for _, want := range []string{"hooks/guard.py", "hooks/nested/deep.sh", "skills/own/SKILL.md", "skills/shared/SKILL.md", "statusline-command.sh"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	for _, file := range files {
		if file.Rel == "hooks" || file.Size != 1 {
			t.Errorf("a link was recorded instead of its target: %+v", file)
		}
	}
}
