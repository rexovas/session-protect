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
