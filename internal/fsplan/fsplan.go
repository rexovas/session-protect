package fsplan

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/targets"
)

// File is one file selected for backup, with its path relative to the target
// source root using forward slashes.
type File struct {
	Rel     string
	Abs     string
	Size    int64
	ModTime time.Time
}

// Build resolves a target's include/exclude rules into a concrete file list.
//
// Include entries are relative to the source root: "dir/" selects a subtree,
// "name" selects a single file, and entries containing glob metacharacters
// match top-level names. Exclude entries apply at any depth: "dir/" prunes any
// directory with that name, plain names and globs drop matching basenames.
func Build(target targets.Target) ([]File, error) {
	var files []File
	seen := map[string]bool{}

	add := func(rel string, info fs.FileInfo, abs string) {
		if seen[rel] || excluded(rel, target.Exclude) {
			return
		}
		seen[rel] = true
		files = append(files, File{Rel: rel, Abs: abs, Size: info.Size(), ModTime: info.ModTime()})
	}

	for _, entry := range target.Include {
		switch {
		case strings.HasSuffix(entry, "/"):
			root := filepath.Join(target.Source, filepath.FromSlash(strings.TrimSuffix(entry, "/")))
			walkTree(target.Source, root, target.Exclude, add)
		case strings.ContainsAny(entry, "*?["):
			matches, err := filepath.Glob(filepath.Join(target.Source, filepath.FromSlash(entry)))
			if err != nil {
				return nil, err
			}
			for _, abs := range matches {
				info, err := os.Stat(abs)
				if err != nil || info.IsDir() {
					continue
				}
				add(relOf(target.Source, abs), info, abs)
			}
		default:
			abs := filepath.Join(target.Source, filepath.FromSlash(entry))
			info, err := os.Stat(abs)
			if err != nil || info.IsDir() {
				continue
			}
			add(relOf(target.Source, abs), info, abs)
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, nil
}

// TotalSize sums the plan's file sizes.
func TotalSize(files []File) int64 {
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total
}

func walkTree(sourceRoot string, root string, excludes []string, add func(string, fs.FileInfo, string)) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := relOf(sourceRoot, path)
		if entry.IsDir() {
			if rel != "." && excludedDir(rel, excludes) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		add(rel, info, path)
		return nil
	})
}

func excluded(rel string, excludes []string) bool {
	base := filepath.Base(rel)
	for _, entry := range excludes {
		if strings.HasSuffix(entry, "/") {
			if underDir(rel, strings.TrimSuffix(entry, "/")) {
				return true
			}
			continue
		}
		if matched, _ := filepath.Match(entry, base); matched {
			return true
		}
	}
	return false
}

func excludedDir(rel string, excludes []string) bool {
	name := filepath.Base(rel)
	for _, entry := range excludes {
		if !strings.HasSuffix(entry, "/") {
			continue
		}
		dir := strings.TrimSuffix(entry, "/")
		if name == dir || underDir(rel, dir) {
			return true
		}
	}
	return false
}

// underDir reports whether any path segment of rel equals dir, so "tmp/"
// excludes both "tmp/x" and "sessions/tmp/x".
func underDir(rel string, dir string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if segment == dir {
			return true
		}
	}
	return false
}

func relOf(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
