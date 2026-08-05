package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/fsplan"
)

func gitEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "test@example.invalid")
	}
	// Repo-external config must not leak into test repos (signing etc).
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func sourceFile(t *testing.T, dir string, rel string, content string) fsplan.File {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	return fsplan.File{Rel: rel, Abs: abs, Size: info.Size(), ModTime: info.ModTime()}
}

func TestEnsureSyncCommitLifecycle(t *testing.T) {
	gitEnv(t)
	repo := filepath.Join(t.TempDir(), "repo")
	src := t.TempDir()

	if IsRepo(repo) {
		t.Fatal("empty path must not be a repo")
	}
	if err := Ensure(repo); err != nil {
		t.Fatal(err)
	}
	if !IsRepo(repo) {
		t.Fatal("Ensure did not create a repo")
	}

	a := sourceFile(t, src, "s/a.jsonl", "one")
	b := sourceFile(t, src, "s/b.jsonl", "two")
	result, err := Sync(repo, "claude", []fsplan.File{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 2 || result.Updated != 0 {
		t.Fatalf("sync = %+v", result)
	}

	// Unchanged files are skipped; a content+mtime change is an update.
	result, err = Sync(repo, "claude", []fsplan.File{a, b})
	if err != nil || result.Added != 0 || result.Updated != 0 {
		t.Fatalf("idempotent sync = %+v (%v)", result, err)
	}
	time.Sleep(10 * time.Millisecond)
	a2 := sourceFile(t, src, "s/a.jsonl", "one-changed")
	result, err = Sync(repo, "claude", []fsplan.File{a2, b})
	if err != nil || result.Updated != 1 {
		t.Fatalf("update sync = %+v (%v)", result, err)
	}

	committed, err := Commit(repo, "subject", "body")
	if err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	if Head(repo) == "" {
		t.Fatal("no head after commit")
	}
	committed, err = Commit(repo, "subject", "body")
	if err != nil || committed {
		t.Fatal("clean tree must not commit again")
	}

	// A source deletion never propagates through Sync; Stale reports it
	// and RemoveFiles is the only path that deletes.
	stale := Stale(repo, "claude", []fsplan.File{a2})
	if len(stale) != 1 || filepath.Base(stale[0]) != "b.jsonl" {
		t.Fatalf("stale = %v", stale)
	}
	if _, err := os.Stat(filepath.Join(repo, "claude", "s", "b.jsonl")); err != nil {
		t.Fatal("Sync deleted a file")
	}
	if removed := RemoveFiles(repo, stale); removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if _, err := os.Stat(filepath.Join(repo, "claude", "s", "b.jsonl")); !os.IsNotExist(err) {
		t.Fatal("RemoveFiles left the file behind")
	}
	if committed, _ := Commit(repo, "deletions", ""); !committed {
		t.Fatal("deletion commit missing")
	}
}
