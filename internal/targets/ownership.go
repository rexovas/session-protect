package targets

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rexovas/session-protect/internal/sessionfile"
)

// Folder ownership: which claude home (account) a project folder belongs
// to, learned from where its sessions live — never configured.
//
//   - A folder is owned by the extra home that has sessions in it.
//   - A folder without sessions of its own inherits from the nearest owned
//     ancestor, but only when that ancestor's subtree is clean: no folder
//     under it has sessions in another home (default included). A claim on
//     a broad parent like ~/projects, which holds other accounts' work,
//     covers that folder alone.
//   - Two extra homes with sessions in one folder is a conflict.
//   - Everything else belongs to the default home, which never claims.

// Ownership is the resolved owner of one folder.
type Ownership struct {
	// Owner is the owning home; the default when nothing claims the folder.
	Owner ClaudeHome
	// ClaimedAt is the folder whose sessions make the claim: the folder
	// itself, or the ancestor it inherits from. Empty when unowned.
	ClaimedAt string
	// Conflict lists the extra homes competing for the folder.
	Conflict []string
	// BroadClaim is an ancestor claimed by an extra home whose claim does
	// not pass down because its subtree holds other accounts' work.
	BroadClaim string
	BroadHome  string
}

// Owned reports whether an extra home owns the folder.
func (o Ownership) Owned() bool { return o.ClaimedAt != "" && len(o.Conflict) == 0 }

// Inherited reports whether the claim comes from an ancestor.
func (o Ownership) Inherited(folder string) bool {
	return o.Owned() && filepath.Clean(o.ClaimedAt) != filepath.Clean(folder)
}

// FolderIndex answers the two questions ownership needs, so the explorer
// (from scanned sessions) and the launcher and hooks (from disk) resolve it
// with the same rules.
type FolderIndex interface {
	// HomesAt counts sessions per home label exactly at dir.
	HomesAt(dir string) map[string]int
	// OtherUnder reports whether a home other than label has sessions
	// strictly under dir.
	OtherUnder(dir string, label string) bool
}

// ResolveOwner applies the ownership rules to folder.
func ResolveOwner(index FolderIndex, homes []ClaudeHome, folder string) Ownership {
	folder = filepath.Clean(folder)
	defaultHome := homes[0]
	byLabel := map[string]ClaudeHome{}
	for _, home := range homes {
		byLabel[home.Label] = home
	}
	result := Ownership{Owner: defaultHome}
	for dir := folder; ; dir = filepath.Dir(dir) {
		var claimants []string
		for label, count := range index.HomesAt(dir) {
			if label != DefaultHomeLabel && count > 0 {
				if _, known := byLabel[label]; known {
					claimants = append(claimants, label)
				}
			}
		}
		sort.Strings(claimants)
		switch {
		case len(claimants) > 1:
			if dir == folder {
				result.ClaimedAt, result.Conflict = dir, claimants
				return result
			}
			// A contested ancestor passes nothing down.
			result.BroadClaim, result.BroadHome = dir, strings.Join(claimants, ", ")
			return result
		case len(claimants) == 1:
			label := claimants[0]
			if dir == folder || !index.OtherUnder(dir, label) {
				result.Owner, result.ClaimedAt = byLabel[label], dir
				return result
			}
			result.BroadClaim, result.BroadHome = dir, label
			return result
		}
		if parent := filepath.Dir(dir); parent == dir {
			return result
		}
	}
}

// FolderOwner resolves a folder's owner from the homes on disk.
func FolderOwner(folder string) Ownership {
	homes := ClaudeHomes()
	return ResolveOwner(NewDiskIndex(homes), homes, folder)
}

// DiskIndex reads ownership facts lazily from each home's projects dir:
// only the slugs on the folder's ancestor chain, and for an inheritance
// check the slugs that could lie beneath the claimed ancestor. Slugs are
// lossy, so every candidate's real path comes from its transcripts.
type DiskIndex struct {
	homes []ClaudeHome
	paths map[string]string // slug dir → real project path ("" unknown)
}

func NewDiskIndex(homes []ClaudeHome) *DiskIndex {
	return &DiskIndex{homes: homes, paths: map[string]string{}}
}

func (d *DiskIndex) HomesAt(dir string) map[string]int {
	counts := map[string]int{}
	slug := ClaudeSlug(dir)
	for _, home := range d.homes {
		slugDir := filepath.Join(home.Dir, "projects", slug)
		if n := countTranscripts(slugDir); n > 0 && d.pathOf(slugDir) == dir {
			counts[home.Label] = n
		}
	}
	return counts
}

func (d *DiskIndex) OtherUnder(dir string, label string) bool {
	for _, homes := range d.Under(dir) {
		for other, n := range homes {
			if other != label && n > 0 {
				return true
			}
		}
	}
	return false
}

// Under lists every folder strictly beneath dir that has sessions, with
// its session count per home label.
func (d *DiskIndex) Under(dir string) map[string]map[string]int {
	out := map[string]map[string]int{}
	prefix := ClaudeSlug(dir) + "-"
	if dir == string(filepath.Separator) {
		prefix = "-"
	}
	for _, home := range d.homes {
		entries, err := os.ReadDir(filepath.Join(home.Dir, "projects"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			slugDir := filepath.Join(home.Dir, "projects", entry.Name())
			n := countTranscripts(slugDir)
			if n == 0 {
				continue
			}
			if path := d.pathOf(slugDir); path != "" && isUnder(path, dir) {
				if out[path] == nil {
					out[path] = map[string]int{}
				}
				out[path][home.Label] += n
			}
		}
	}
	return out
}

func (d *DiskIndex) pathOf(slugDir string) string {
	if path, ok := d.paths[slugDir]; ok {
		return path
	}
	path := ProjectPathOf(slugDir)
	d.paths[slugDir] = path
	return path
}

// ProjectPathOf is the working directory recorded in a claude project
// dir's transcripts (the newest one that records any).
func ProjectPathOf(slugDir string) string {
	entries, err := os.ReadDir(slugDir)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		mod  int64
	}
	var files []candidate
	for _, entry := range entries {
		if entry.IsDir() || !sessionfile.IsTranscript(entry.Name()) {
			continue
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, candidate{filepath.Join(slugDir, entry.Name()), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod > files[j].mod })
	for _, file := range files {
		if cwd := TranscriptCwd(file.path); cwd != "" {
			return filepath.Clean(cwd)
		}
	}
	return ""
}

// TranscriptCwd is the first working directory a claude transcript records.
func TranscriptCwd(path string) string {
	file, err := sessionfile.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for i := 0; i < 200 && scanner.Scan(); i++ {
		line := scanner.Bytes()
		if !strings.Contains(string(line), `"cwd"`) {
			continue
		}
		var entry struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Cwd != "" {
			return entry.Cwd
		}
	}
	return ""
}

func countTranscripts(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() && sessionfile.IsTranscript(entry.Name()) {
			n++
		}
	}
	return n
}

// isUnder reports whether path lies strictly beneath dir.
func isUnder(path string, dir string) bool {
	dir = filepath.Clean(dir)
	if dir == string(filepath.Separator) {
		return path != dir
	}
	return strings.HasPrefix(filepath.Clean(path), dir+string(filepath.Separator))
}

// IsUnder is isUnder for other packages.
func IsUnder(path string, dir string) bool { return isUnder(path, dir) }
