package gitrepo

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rexovas/session-protect/internal/fsplan"
)

// Ensure makes path a git repository, creating and initializing it when
// missing. It fails closed if path exists as a non-empty directory that is not
// already a repository, so backups never adopt unknown data.
func Ensure(path string) error {
	if IsRepo(path) {
		return nil
	}
	entries, err := os.ReadDir(path)
	if err == nil && len(entries) > 0 {
		return fmt.Errorf("%s exists and is not a git repository; refusing to initialize over existing data", path)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return run(path, "init", "--quiet")
}

func IsRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

// IsGitCrypt reports whether the repository has git-crypt configured for any
// tracked path.
func IsGitCrypt(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, ".gitattributes"))
	return err == nil && strings.Contains(string(data), "filter=git-crypt")
}

// SyncResult describes what Sync changed inside the repo working tree.
type SyncResult struct {
	Added   int
	Updated int
}

// Sync mirrors the planned files into repo/prefix: copies new and changed
// files (compared by size and mtime), preserving source mtimes. It never
// removes anything — deletions are handled separately (see Stale and
// RemoveFiles) so a file's final state can be committed before its removal
// is recorded.
func Sync(repo string, prefix string, files []fsplan.File) (SyncResult, error) {
	var result SyncResult
	root := filepath.Join(repo, filepath.FromSlash(prefix))
	for _, file := range files {
		dest := filepath.Join(root, filepath.FromSlash(file.Rel))
		info, err := os.Stat(dest)
		switch {
		case err == nil && info.Size() == file.Size && info.ModTime().Equal(file.ModTime):
			continue
		case err == nil:
			result.Updated++
		default:
			result.Added++
		}
		if err := copyFile(file.Abs, dest, file); err != nil {
			return result, fmt.Errorf("copy %s: %w", file.Rel, err)
		}
	}
	return result, nil
}

// Stale lists working-tree files under repo/prefix that are no longer in the
// plan (deleted or pruned at the source).
func Stale(repo string, prefix string, files []fsplan.File) []string {
	root := filepath.Join(repo, filepath.FromSlash(prefix))
	planned := make(map[string]bool, len(files))
	for _, file := range files {
		planned[file.Rel] = true
	}

	var stale []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if filepath.Base(path) == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if !planned[filepath.ToSlash(rel)] {
			stale = append(stale, path)
		}
		return nil
	})
	return stale
}

// RemoveFiles deletes the given files and prunes any directories left empty,
// stopping at the repo root.
func RemoveFiles(repo string, paths []string) int {
	removed := 0
	for _, path := range paths {
		if os.Remove(path) != nil {
			continue
		}
		removed++
		for dir := filepath.Dir(path); dir != repo; dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil {
				break // not empty (or root reached)
			}
		}
	}
	return removed
}

// Commit stages everything and commits with the given subject and body.
// It returns false without error when there is nothing to commit.
func Commit(repo string, subject string, body string) (bool, error) {
	if err := run(repo, "add", "-A"); err != nil {
		return false, err
	}
	out, err := output(repo, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if len(strings.TrimSpace(out)) == 0 {
		return false, nil
	}
	args := []string{"commit", "--quiet", "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}
	if err := run(repo, args...); err != nil {
		return false, err
	}
	return true, nil
}

// Head returns the short hash of the current commit, or "" for an empty repo.
func Head(repo string) string {
	out, err := output(repo, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func copyFile(src string, dest string, file fsplan.File) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dest, file.ModTime, file.ModTime)
}

func run(repo string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func output(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out), nil
}
