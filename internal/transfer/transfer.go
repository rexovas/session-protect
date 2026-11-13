// Package transfer moves claude sessions between claude homes (accounts)
// without changing their project folder: each session's transcript and
// everything the agent keys by its id (sidecar dir, file history, session
// env, tasks) moves to the other home, along with the project's memory,
// and its prompt history is copied so titles and lost-session records
// follow. Paths inside transcripts are untouched: the folder is the same.
package transfer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/guard"
	"github.com/rexovas/session-protect/internal/lock"
	"github.com/rexovas/session-protect/internal/sessionfile"
	"github.com/rexovas/session-protect/internal/targets"
)

type Options struct {
	To        string // destination home label
	From      string // source home label; default "default"
	SessionID string // one session
	Project   string // or every session of a project folder
	Recursive bool   // with Project: its subfolders too
}

// Move is one file or directory changing homes.
type Move struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Session is one session's part of a transfer.
type Session struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Moves   []Move `json:"moves"` // transcript first, then its id-keyed extras
	Open    bool   `json:"open,omitempty"`
}

// Memory is one project's memory dir following its sessions.
type Memory struct {
	Project string `json:"project"`
	Move           // destination may be a keep-both subdir
	Copy    bool   `json:"copy,omitempty"` // sessions stay behind: copy, keep the source
}

// Plan is everything a transfer will do.
type Plan struct {
	From     targets.ClaudeHome `json:"from"`
	To       targets.ClaudeHome `json:"to"`
	Sessions []Session          `json:"sessions"`
	Memory   []Memory           `json:"memory,omitempty"`
	// Extras is whatever else a drained project dir holds (sidecars of
	// sessions that moved projects, notes); it follows so nothing is left
	// behind in the source home.
	Extras []Move `json:"extras,omitempty"`
	// Drained lists source project dirs the transfer empties; they are
	// removed afterwards (only if nothing new appeared in them meanwhile).
	Drained []string `json:"drained,omitempty"`
	// Unresolved lists project dirs that might be in scope but whose folder
	// can't be determined (no transcripts, folder gone); they are skipped.
	Unresolved []string `json:"unresolved,omitempty"`
	// Folders lists each project folder touched with its owner before and
	// after, for the preview.
	Folders []FolderChange `json:"folders"`
}

// FolderChange is the ownership effect of a transfer on one folder.
type FolderChange struct {
	Path     string `json:"path"`
	Sessions int    `json:"sessions"`
	Before   string `json:"before"` // owner label before
	After    string `json:"after"`  // owner label after
	Left     int    `json:"left"`   // sessions of the source home left behind here
}

// Build resolves a transfer plan without writing anything.
func Build(cfg config.Config, opts Options) (*Plan, error) {
	if (opts.SessionID == "") == (opts.Project == "") {
		return nil, fmt.Errorf("exactly one of --session or --project is required")
	}
	to, ok := targets.HomeByLabel(opts.To)
	if !ok {
		return nil, fmt.Errorf("no claude dir labelled %q (see `session-protect claude-dirs`)", opts.To)
	}
	fromLabel := opts.From
	if fromLabel == "" {
		fromLabel = targets.DefaultHomeLabel
	}
	from, ok := targets.HomeByLabel(fromLabel)
	if !ok {
		return nil, fmt.Errorf("no claude dir labelled %q", fromLabel)
	}
	if from.Label == to.Label {
		return nil, fmt.Errorf("source and destination are the same claude dir (%s)", to.Label)
	}

	plan := &Plan{From: from, To: to}
	fromProjects := filepath.Join(from.Dir, "projects")
	toProjects := filepath.Join(to.Dir, "projects")

	// Project dirs in scope, as slug → real project path.
	scope := map[string]string{}
	if opts.Project != "" {
		project, err := filepath.Abs(opts.Project)
		if err != nil {
			return nil, err
		}
		project = filepath.Clean(project)
		var onDisk map[string]string // slug → folder, for dirs with no transcripts
		entries, _ := os.ReadDir(fromProjects)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			slug := entry.Name()
			exact := slug == targets.ClaudeSlug(project)
			if !exact && !(opts.Recursive && strings.HasPrefix(slug, targets.ClaudeSlug(project)+"-")) {
				continue
			}
			path := targets.ProjectPathOf(filepath.Join(fromProjects, slug))
			if path == "" {
				// No transcript says where it belongs: match it to a real
				// folder by slug, which is only trustworthy when exact or
				// when exactly one folder under the project has that slug.
				switch {
				case exact:
					path = project
				default:
					if onDisk == nil {
						onDisk = slugsUnder(project)
					}
					path = onDisk[slug]
				}
				if path == "" {
					plan.Unresolved = append(plan.Unresolved, filepath.Join(fromProjects, slug))
					continue
				}
			}
			if path == project || opts.Recursive && targets.IsUnder(path, project) {
				scope[slug] = path
			}
		}
		if len(scope) == 0 {
			return nil, fmt.Errorf("nothing of %s's for %s", from.Label, project)
		}
	}

	live := guard.LiveAll()
	slugs := make([]string, 0, len(scope))
	for slug := range scope {
		slugs = append(slugs, slug)
	}
	if opts.SessionID != "" {
		slug, err := findSession(fromProjects, opts.SessionID)
		if err != nil {
			return nil, fmt.Errorf("session %s is not in the %s claude dir", opts.SessionID, from.Label)
		}
		scope[slug] = targets.TranscriptCwd(transcriptPath(fromProjects, slug, opts.SessionID))
		slugs = []string{slug}
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		srcDir := filepath.Join(fromProjects, slug)
		dstDir := filepath.Join(toProjects, slug)
		names, _ := os.ReadDir(srcDir)
		moved, total := 0, 0
		for _, entry := range names {
			if entry.IsDir() || !sessionfile.IsTranscript(entry.Name()) {
				continue
			}
			total++
			id := sessionfile.Stem(entry.Name())
			if opts.SessionID != "" && id != opts.SessionID {
				continue
			}
			session := Session{ID: id, Project: scope[slug]}
			session.Moves = append(session.Moves, Move{filepath.Join(srcDir, entry.Name()), filepath.Join(dstDir, entry.Name())})
			for _, extra := range []struct{ from, to string }{
				{filepath.Join(srcDir, id), filepath.Join(dstDir, id)},
				{filepath.Join(from.Dir, "file-history", id), filepath.Join(to.Dir, "file-history", id)},
				{filepath.Join(from.Dir, "session-env", id), filepath.Join(to.Dir, "session-env", id)},
				{filepath.Join(from.Dir, "tasks", id), filepath.Join(to.Dir, "tasks", id)},
			} {
				if _, err := os.Lstat(extra.from); err == nil {
					session.Moves = append(session.Moves, Move{extra.from, extra.to})
				}
			}
			if info, ok := live[id]; ok && info.Home == from.Label {
				session.Open = true
			}
			for _, move := range session.Moves {
				if _, err := os.Lstat(move.To); err == nil {
					return nil, fmt.Errorf("session %s already has %s in the %s claude dir", id, move.To, to.Label)
				}
			}
			plan.Sessions = append(plan.Sessions, session)
			moved++
		}
		drained := moved == total
		if mem := filepath.Join(srcDir, "memory"); isDir(mem) && (moved > 0 || drained) {
			memory := Memory{Project: scope[slug], Move: Move{From: mem, To: filepath.Join(dstDir, "memory")}, Copy: moved < total}
			if isDir(memory.To) {
				memory.To = filepath.Join(memory.To, fmt.Sprintf("transferred-from-%s-%s", from.Label, time.Now().Format("20060102-150405")))
			}
			plan.Memory = append(plan.Memory, memory)
		}
		extras := 0
		if drained {
			plan.Drained = append(plan.Drained, srcDir)
			covered := map[string]bool{filepath.Join(srcDir, "memory"): true}
			for _, session := range plan.Sessions {
				for _, move := range session.Moves {
					covered[move.From] = true
				}
			}
			for _, entry := range names {
				from := filepath.Join(srcDir, entry.Name())
				if covered[from] {
					continue
				}
				to := filepath.Join(dstDir, entry.Name())
				if _, err := os.Lstat(to); err == nil {
					return nil, fmt.Errorf("%s already exists in the %s claude dir", to, plan.To.Label)
				}
				plan.Extras = append(plan.Extras, Move{From: from, To: to})
				extras++
			}
		}
		if moved > 0 || extras > 0 || drained && isDir(filepath.Join(srcDir, "memory")) {
			plan.Folders = append(plan.Folders, FolderChange{Path: scope[slug], Sessions: moved, Left: total - moved})
		}
	}
	if len(plan.Sessions) == 0 && len(plan.Memory) == 0 && len(plan.Extras) == 0 {
		return nil, fmt.Errorf("nothing to transfer")
	}
	for _, session := range plan.Sessions {
		if session.Open {
			return nil, fmt.Errorf("session %s is open in a running agent; close it first", session.ID)
		}
	}

	// Ownership: a folder another extra home owns can't take this
	// account's sessions — that would mix accounts in an owned folder.
	homes := targets.ClaudeHomes()
	disk := targets.NewDiskIndex(homes)
	for i := range plan.Folders {
		change := &plan.Folders[i]
		before := targets.FolderOwner(change.Path)
		change.Before = ownerLabel(before)
		if before.Owned() && before.Owner.Label != to.Label && before.Owner.Label != from.Label {
			return nil, fmt.Errorf("%s belongs to the %s claude dir; its sessions can't move to %s", change.Path, before.Owner.Label, to.Label)
		}
		if len(before.Conflict) > 0 {
			return nil, fmt.Errorf("%s is claimed by several claude dirs (%s); resolve that first", change.Path, strings.Join(before.Conflict, ", "))
		}
		change.After = ownerLabel(targets.ResolveOwner(afterIndex{plan: plan, disk: disk}, homes, change.Path))
	}
	return plan, nil
}

func ownerLabel(o targets.Ownership) string {
	if len(o.Conflict) > 0 {
		return "conflict: " + strings.Join(o.Conflict, ", ")
	}
	return o.Owner.Label
}

// Apply executes the plan. Callers back up before and after (as the CLI
// does), so both states land in the backup history.
func Apply(cfg config.Config, plan *Plan) error {
	release, err := lock.Acquire(cfg.BackupRoot)
	if err != nil {
		return err
	}
	defer release()

	var entries []audit.Entry
	moved := map[string]bool{}
	for _, session := range plan.Sessions {
		for _, move := range session.Moves {
			if err := relocate(move.From, move.To); err != nil {
				return fmt.Errorf("move %s: %w", move.From, err)
			}
		}
		moved[session.ID] = true
		entries = append(entries, audit.Entry{
			Time: time.Now(), Action: "transfer", Target: plan.To.TargetName(),
			SessionID: session.ID, From: session.Moves[0].From, To: session.Moves[0].To,
			Detail: plan.From.Label + " → " + plan.To.Label,
		})
	}
	for _, extra := range plan.Extras {
		if err := relocate(extra.From, extra.To); err != nil {
			return fmt.Errorf("move %s: %w", extra.From, err)
		}
	}
	for _, memory := range plan.Memory {
		var err error
		if memory.Copy {
			err = copyTree(memory.From, memory.To)
		} else {
			err = relocate(memory.From, memory.To)
		}
		if err != nil {
			return fmt.Errorf("memory %s: %w", memory.From, err)
		}
	}
	for _, dir := range plan.Drained {
		_ = os.Remove(dir) // fails, harmlessly, unless empty
	}
	if err := copyHistory(plan.From, plan.To, moved); err != nil {
		return fmt.Errorf("prompt history: %w", err)
	}
	audit.Append(cfg.BackupRoot, entries)
	return nil
}

// copyHistory appends the moved sessions' prompt-history lines to the
// destination's history; the source history is never modified.
func copyHistory(from, to targets.ClaudeHome, ids map[string]bool) error {
	src, err := os.Open(filepath.Join(from.Dir, "history.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer src.Close()
	var lines bytes.Buffer
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && ids[entry.SessionID] {
			lines.Write(scanner.Bytes())
			lines.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if lines.Len() == 0 {
		return nil
	}
	dst, err := os.OpenFile(filepath.Join(to.Dir, "history.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := dst.Write(lines.Bytes()); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// relocate moves a file or tree, renaming when both ends share a volume
// and otherwise copying, verifying, then removing the original.
func relocate(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s already exists", to)
	}
	err := os.Rename(from, to)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyTree(from, to); err != nil {
		return err
	}
	return os.RemoveAll(from)
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(to, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, dest)
		}
		return copyFile(path, dest)
	})
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	n, err := io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != info.Size() {
		err = fmt.Errorf("short copy of %s", from)
	}
	return err
}

func findSession(projects string, id string) (string, error) {
	entries, err := os.ReadDir(projects)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, name := range []string{id + ".jsonl", id + ".jsonl.zst"} {
			if _, err := os.Stat(filepath.Join(projects, entry.Name(), name)); err == nil {
				return entry.Name(), nil
			}
		}
	}
	return "", os.ErrNotExist
}

func transcriptPath(projects, slug, id string) string {
	plain := filepath.Join(projects, slug, id+".jsonl")
	if _, err := os.Stat(plain); err == nil {
		return plain
	}
	return plain + ".zst"
}

// slugsUnder maps the slug of every folder beneath project (a few levels,
// skipping hidden and dependency dirs) to its path. Slugs two folders share
// map to "", so an ambiguous dir is never guessed.
func slugsUnder(project string) map[string]string {
	out := map[string]string{}
	base := strings.Count(project, string(filepath.Separator))
	_ = filepath.WalkDir(project, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if path != project && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
			return filepath.SkipDir
		}
		if strings.Count(path, string(filepath.Separator))-base > 5 {
			return filepath.SkipDir
		}
		slug := targets.ClaudeSlug(path)
		if _, seen := out[slug]; seen {
			out[slug] = ""
		} else {
			out[slug] = path
		}
		return nil
	})
	return out
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// --- ownership after the move ------------------------------------------------

// afterIndex is the disk's ownership facts with the plan applied, so the
// preview shows each folder's owner once the transfer is done.
type afterIndex struct {
	plan *Plan
	disk *targets.DiskIndex
}

// delta is the plan's change in session counts at one folder.
func (a afterIndex) apply(path string, counts map[string]int) map[string]int {
	out := map[string]int{}
	for label, n := range counts {
		out[label] = n
	}
	for _, change := range a.plan.Folders {
		if filepath.Clean(change.Path) == filepath.Clean(path) {
			out[a.plan.From.Label] -= change.Sessions
			out[a.plan.To.Label] += change.Sessions
		}
	}
	return out
}

func (a afterIndex) HomesAt(dir string) map[string]int {
	return a.apply(dir, a.disk.HomesAt(dir))
}

func (a afterIndex) OtherUnder(dir string, label string) bool {
	for path, counts := range a.disk.Under(dir) {
		for other, n := range a.apply(path, counts) {
			if other != label && n > 0 {
				return true
			}
		}
	}
	return false
}
