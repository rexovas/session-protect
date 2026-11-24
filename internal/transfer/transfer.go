// Package transfer moves sessions between accounts (agent homes sharing a
// label) without changing their project folder. For claude, each
// session's transcript and everything the agent keys by its id (sidecar
// dir, file history, session env, tasks) moves to the other config dir,
// with the project's memory. For codex, each rollout moves to the same
// date path in the other codex home, with its subagent rollouts; codex
// indexes it there on its next start. Prompt history (and codex thread
// names) are copied so titles and lost-session records follow. Paths
// inside transcripts are untouched: the folder is the same.
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
	To        string // destination account label
	From      string // source account label; default "default"
	SessionID string // one session
	Project   string // or every session of a project folder
	Recursive bool   // with Project: its subfolders too
	Agent     string // "claude", "codex", or "" for both
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
	// From and To are the claude config dirs; a side without one has only
	// its label set.
	From     targets.ClaudeHome `json:"from"`
	To       targets.ClaudeHome `json:"to"`
	Sessions []Session          `json:"sessions"`
	// Lost lists lost claude sessions in scope: no transcript anywhere,
	// only prompt history, which is copied so the account holds their
	// record (rebuilds already land in the folder owner's dir).
	Lost []string `json:"lost,omitempty"`
	// CodexFrom, CodexTo and Codex are the codex side of the transfer.
	CodexFrom targets.Home `json:"codex_from,omitempty"`
	CodexTo   targets.Home `json:"codex_to,omitempty"`
	Codex     []Session    `json:"codex,omitempty"`
	Memory    []Memory     `json:"memory,omitempty"`
	// Extras is whatever else a drained project dir holds (sidecars of
	// sessions that moved projects, notes); it follows so nothing is left
	// behind in the source home.
	Extras []Move `json:"extras,omitempty"`
	// Drained lists source project dirs the transfer empties; they are
	// removed afterwards (only if nothing new appeared in them meanwhile).
	Drained []string `json:"drained,omitempty"`
	// NameWarnings lists codex threads whose name couldn't be restored in
	// the destination (filled by Apply).
	NameWarnings []string `json:"name_warnings,omitempty"`
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
	Codex    int    `json:"codex,omitempty"` // codex sessions (top-level)
	Lost     int    `json:"lost,omitempty"`  // lost sessions whose history is copied
	Before   string `json:"before"`          // owner label before
	After    string `json:"after"`           // owner label after
	Left     int    `json:"left"`            // sessions of the source home left behind here
}

// Build resolves a transfer plan without writing anything.
func Build(cfg config.Config, opts Options) (*Plan, error) {
	if (opts.SessionID == "") == (opts.Project == "") {
		return nil, fmt.Errorf("exactly one of --session or --project is required")
	}
	switch opts.Agent {
	case "", targets.AgentClaude, targets.AgentCodex:
	default:
		return nil, fmt.Errorf("unknown agent %q (want claude or codex)", opts.Agent)
	}
	fromLabel := opts.From
	if fromLabel == "" {
		fromLabel = targets.DefaultHomeLabel
	}
	if !isAccount(opts.To) {
		return nil, fmt.Errorf("no account labelled %q (see `session-protect claude-dirs` and `codex-dirs`)", opts.To)
	}
	if !isAccount(fromLabel) {
		return nil, fmt.Errorf("no account labelled %q", fromLabel)
	}
	if fromLabel == opts.To {
		return nil, fmt.Errorf("source and destination are the same account (%s)", opts.To)
	}
	project := ""
	if opts.Project != "" {
		abs, err := filepath.Abs(opts.Project)
		if err != nil {
			return nil, err
		}
		project = filepath.Clean(abs)
	}

	// A side without a claude config dir gets a placeholder that holds
	// nothing; any claude session needing it is refused below.
	from, haveFrom := targets.HomeByLabel(fromLabel)
	if !haveFrom {
		from = targets.Home{Label: fromLabel, Dir: filepath.Join(os.TempDir(), "session-protect-no-claude-dir")}
	}
	to, haveTo := targets.HomeByLabel(opts.To)
	if !haveTo {
		to = targets.Home{Label: opts.To, Dir: filepath.Join(os.TempDir(), "session-protect-no-claude-dir")}
	}
	plan := &Plan{From: from, To: to}
	fromProjects := filepath.Join(from.Dir, "projects")
	toProjects := filepath.Join(to.Dir, "projects")
	if !haveFrom || opts.Agent == targets.AgentCodex {
		fromProjects = filepath.Join(os.TempDir(), "session-protect-no-claude-dir", "projects")
	}

	// Project dirs in scope, as slug → real project path.
	scope := map[string]string{}
	if opts.Project != "" {
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
	}

	live := guard.LiveAll()
	slugs := make([]string, 0, len(scope))
	for slug := range scope {
		slugs = append(slugs, slug)
	}
	if opts.SessionID != "" {
		slugs = nil
		if slug, err := findSession(fromProjects, opts.SessionID); err == nil {
			scope[slug] = targets.TranscriptCwd(transcriptPath(fromProjects, slug, opts.SessionID))
			slugs = []string{slug}
		}
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
	if haveFrom && opts.Agent != targets.AgentCodex {
		lost := lostInScope(from, opts, project, plan.Sessions)
		// A lost session whose history the destination already holds
		// has nothing left to copy.
		for id := range historyIDs(filepath.Join(to.Dir, "history.jsonl")) {
			delete(lost, id)
		}
		for id, folder := range lost {
			plan.Lost = append(plan.Lost, id)
			found := false
			for i := range plan.Folders {
				if filepath.Clean(plan.Folders[i].Path) == folder {
					plan.Folders[i].Lost++
					found = true
				}
			}
			if !found {
				plan.Folders = append(plan.Folders, FolderChange{Path: folder, Lost: 1})
			}
		}
		sort.Strings(plan.Lost)
		sort.Slice(plan.Folders, func(i, j int) bool { return plan.Folders[i].Path < plan.Folders[j].Path })
	}
	if !haveTo && (len(plan.Sessions) > 0 || len(plan.Memory) > 0 || len(plan.Extras) > 0 || len(plan.Lost) > 0) {
		return nil, fmt.Errorf("the %s account has no claude dir, but %d claude session(s) are in scope; add one with `session-protect claude-dirs add DIR --label %s`, or pass --agent codex",
			opts.To, len(plan.Sessions), opts.To)
	}
	if opts.Agent != targets.AgentClaude {
		if err := buildCodex(plan, opts, fromLabel, project); err != nil {
			return nil, err
		}
	}
	if len(plan.Sessions) == 0 && len(plan.Memory) == 0 && len(plan.Extras) == 0 && len(plan.Codex) == 0 && len(plan.Lost) == 0 {
		if opts.SessionID != "" {
			return nil, fmt.Errorf("session %s is not in the %s account", opts.SessionID, fromLabel)
		}
		return nil, fmt.Errorf("nothing of %s's to transfer", fromLabel)
	}
	for _, session := range plan.Sessions {
		if session.Open {
			return nil, fmt.Errorf("session %s is open in a running agent; close it first", session.ID)
		}
	}

	// Ownership: a folder another extra home owns can't take this
	// account's sessions — that would mix accounts in an owned folder.
	homes := targets.Accounts()
	disk := targets.NewDiskIndex(targets.ClaudeHomes())
	for i := range plan.Folders {
		change := &plan.Folders[i]
		before := targets.FolderOwner(change.Path)
		change.Before = ownerLabel(before)
		if before.Owned() && before.Owner.Label != to.Label && before.Owner.Label != from.Label {
			return nil, fmt.Errorf("%s belongs to the %s account; its sessions can't move to %s", change.Path, before.Owner.Label, to.Label)
		}
		if len(before.Conflict) > 0 {
			return nil, fmt.Errorf("%s is claimed by several accounts (%s); resolve that first", change.Path, strings.Join(before.Conflict, ", "))
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
	codexMoved := map[string]bool{}
	for _, session := range plan.Codex {
		for _, move := range session.Moves {
			if err := relocate(move.From, move.To); err != nil {
				return fmt.Errorf("move %s: %w", move.From, err)
			}
		}
		codexMoved[session.ID] = true
		entries = append(entries, audit.Entry{
			Time: time.Now(), Action: "transfer", Target: plan.CodexTo.TargetName(),
			SessionID: session.ID, From: session.Moves[0].From, To: session.Moves[0].To,
			Detail: plan.CodexFrom.Label + " → " + plan.CodexTo.Label,
		})
	}
	if len(codexMoved) > 0 {
		for _, file := range []struct{ name, key string }{{"history.jsonl", "session_id"}, {"session_index.jsonl", "id"}} {
			if err := copyLines(filepath.Join(plan.CodexFrom.Dir, file.name), filepath.Join(plan.CodexTo.Dir, file.name), file.key, codexMoved); err != nil {
				return fmt.Errorf("codex %s: %w", file.name, err)
			}
		}
		// Codex keeps thread names in its database and indexes a moved
		// rollout without one, then auto-titles it on next use: set each
		// moved thread's latest name through the destination's codex.
		plan.NameWarnings = setCodexNames(plan.CodexTo.Dir, threadNames(filepath.Join(plan.CodexFrom.Dir, "session_index.jsonl"), codexMoved))
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
	for _, id := range plan.Lost {
		moved[id] = true
	}
	if len(moved) > 0 {
		if err := copyLines(filepath.Join(plan.From.Dir, "history.jsonl"), filepath.Join(plan.To.Dir, "history.jsonl"), "sessionId", moved); err != nil {
			return fmt.Errorf("prompt history: %w", err)
		}
	}
	audit.Append(cfg.BackupRoot, entries)
	return nil
}

// copyLines appends the JSONL lines of from whose key field names a moved
// session to the destination file; the source is never modified.
func copyLines(fromPath, toPath, key string, ids map[string]bool) error {
	src, err := os.Open(fromPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer src.Close()
	// Lines the destination already has are skipped, so a repeated
	// transfer never duplicates history.
	have := map[string]bool{}
	if existing, err := os.Open(toPath); err == nil {
		known := bufio.NewScanner(existing)
		known.Buffer(make([]byte, 256*1024), 16*1024*1024)
		for known.Scan() {
			have[known.Text()] = true
		}
		existing.Close()
	}
	var lines bytes.Buffer
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		var id string
		if json.Unmarshal(entry[key], &id) == nil && ids[id] && !have[scanner.Text()] {
			have[scanner.Text()] = true
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
	if err := os.MkdirAll(filepath.Dir(toPath), 0o700); err != nil {
		return err
	}
	dst, err := os.OpenFile(toPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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
			out[a.plan.From.Label] -= change.Sessions + change.Codex
			out[a.plan.To.Label] += change.Sessions + change.Codex
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

// isAccount reports whether a label names an account of either agent.
func isAccount(label string) bool {
	for _, home := range targets.Accounts() {
		if home.Label == label {
			return true
		}
	}
	return false
}

// buildCodex adds the codex side: rollouts in scope (by their recorded
// folder, or the one session) with the subagent rollouts they spawned,
// each moving to the same date path in the destination codex home.
func buildCodex(plan *Plan, opts Options, fromLabel string, project string) error {
	from, ok := targets.HomeByLabelOf(targets.AgentCodex, fromLabel)
	if !ok {
		return nil
	}
	rollouts := targets.CodexRollouts(from)
	picked := map[string]bool{} // rollout path
	ids := map[string]bool{}    // session ids in scope
	inScope := func(cwd string) bool {
		return cwd == project || opts.Recursive && targets.IsUnder(cwd, project)
	}
	for path, entry := range rollouts {
		if opts.SessionID != "" && entry.ID == opts.SessionID || opts.SessionID == "" && inScope(entry.Cwd) {
			picked[path] = true
			ids[entry.ID] = true
		}
	}
	// Spawned subagents follow their parent wherever they worked, down
	// every level of nesting.
	for grew := true; grew; {
		grew = false
		for path, entry := range rollouts {
			if !picked[path] && entry.Parent != "" && ids[entry.Parent] {
				picked[path], ids[entry.ID], grew = true, true, true
			}
		}
	}
	if len(picked) == 0 {
		return nil
	}
	to, ok := targets.HomeByLabelOf(targets.AgentCodex, opts.To)
	if !ok {
		return fmt.Errorf("the %s account has no codex home, but %d codex session file(s) are in scope; add one with `session-protect codex-dirs add DIR --label %s`, or pass --agent claude",
			opts.To, len(picked), opts.To)
	}
	plan.CodexFrom, plan.CodexTo = from, to

	// Codex's background app-server knows every conversation it has open,
	// whichever client opened it; a process listing alone would miss those.
	loaded, err := guard.CodexLoaded(from.Dir)
	switch {
	case errors.Is(err, guard.ErrNoCodexServer):
	case err != nil:
		return fmt.Errorf("couldn't ask codex which conversations are open (%v); quit codex and try again", err)
	default:
		for path := range picked {
			if entry := rollouts[path]; loaded[entry.ID] {
				return fmt.Errorf("codex session %s is open in codex; close it first", entry.ID)
			}
		}
	}
	liveIDs, liveCwds := guard.LiveCodex()
	paths := make([]string, 0, len(picked))
	for path := range picked {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	fromRoot := filepath.Join(from.Dir, "sessions")
	toRoot := filepath.Join(to.Dir, "sessions")
	byFolder := map[string]int{}
	counted := map[string]bool{}
	for _, path := range paths {
		entry := rollouts[path]
		if _, open := liveIDs[entry.ID]; open {
			return fmt.Errorf("codex session %s is open in a running codex; close it first", entry.ID)
		}
		if _, open := liveCwds[entry.Cwd]; open && !entry.Subagent {
			return fmt.Errorf("a codex process is running in %s; close it first", entry.Cwd)
		}
		rel, err := filepath.Rel(fromRoot, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("rollout %s is outside %s", path, fromRoot)
		}
		dest := filepath.Join(toRoot, rel)
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("%s already exists in the %s codex home", dest, to.Label)
		}
		plan.Codex = append(plan.Codex, Session{ID: entry.ID, Project: entry.Cwd, Moves: []Move{{From: path, To: dest}}})
		if !entry.Subagent && !counted[entry.ID] {
			counted[entry.ID] = true // plain + compressed copies count once
			byFolder[entry.Cwd]++
		}
	}
	// Fold the codex sessions into the per-folder preview.
	left := map[string]int{}
	leftIDs := map[string]bool{}
	for path, entry := range rollouts {
		if !picked[path] && !entry.Subagent && !ids[entry.ID] && !leftIDs[entry.ID] {
			leftIDs[entry.ID] = true
			left[entry.Cwd]++
		}
	}
	for folder, n := range byFolder {
		found := false
		for i := range plan.Folders {
			if filepath.Clean(plan.Folders[i].Path) == folder {
				plan.Folders[i].Codex += n
				plan.Folders[i].Left += left[folder]
				found = true
			}
		}
		if !found {
			plan.Folders = append(plan.Folders, FolderChange{Path: folder, Codex: n, Left: left[folder]})
		}
	}
	sort.Slice(plan.Folders, func(i, j int) bool { return plan.Folders[i].Path < plan.Folders[j].Path })
	return nil
}

// lostInScope finds lost claude sessions whose recorded folder is in
// scope: in the source home's prompt history, not being moved now, and
// with no transcript in any claude home. It maps each to its folder (the
// last one its history records).
func lostInScope(from targets.Home, opts Options, project string, moving []Session) map[string]string {
	file, err := os.Open(filepath.Join(from.Dir, "history.jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	skip := map[string]bool{}
	for _, session := range moving {
		skip[session.ID] = true
	}
	candidates := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry struct {
			SessionID string `json:"sessionId"`
			Project   string `json:"project"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.SessionID == "" || skip[entry.SessionID] {
			continue
		}
		folder := ""
		if entry.Project != "" {
			folder = filepath.Clean(entry.Project)
		}
		switch {
		case opts.SessionID != "":
			if entry.SessionID == opts.SessionID {
				candidates[entry.SessionID] = folder
			}
		case folder != "":
			if folder == project || opts.Recursive && targets.IsUnder(folder, project) {
				candidates[entry.SessionID] = folder
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	for _, home := range targets.ClaudeHomes() {
		entries, _ := os.ReadDir(filepath.Join(home.Dir, "projects"))
		for _, dir := range entries {
			if !dir.IsDir() {
				continue
			}
			names, _ := os.ReadDir(filepath.Join(home.Dir, "projects", dir.Name()))
			for _, name := range names {
				if !name.IsDir() && sessionfile.IsTranscript(name.Name()) {
					delete(candidates, sessionfile.Stem(name.Name()))
				}
			}
		}
	}
	return candidates
}

// historyIDs lists the session ids a claude prompt history mentions.
func historyIDs(path string) map[string]bool {
	ids := map[string]bool{}
	file, err := os.Open(path)
	if err != nil {
		return ids
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.SessionID != "" {
			ids[entry.SessionID] = true
		}
	}
	return ids
}

// setCodexNames is seamed so tests never start codex.
var setCodexNames = guard.SetCodexNames

// threadNames reads the latest thread name per id from a codex session
// index, for the ids given.
func threadNames(path string, ids map[string]bool) map[string]string {
	names := map[string]string{}
	latest := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return names
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			UpdatedAt  string `json:"updated_at"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || !ids[entry.ID] || entry.ThreadName == "" {
			continue
		}
		if entry.UpdatedAt >= latest[entry.ID] {
			names[entry.ID], latest[entry.ID] = entry.ThreadName, entry.UpdatedAt
		}
	}
	return names
}
