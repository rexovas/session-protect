// Package transplant relocates agent sessions — and the project memory
// they rely on — to a different project directory, keeping resume
// continuity. Copy-first everywhere: sources are synced to backup before
// anything moves, verification happens before any original is removed,
// and target memory is never overwritten.
package transplant

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/guard"
	"github.com/rexovas/session-protect/internal/lock"
)

type Options struct {
	SessionID string // single-session scope
	Project   string // project scope: relocate every session of this path
	To        string // target project directory (may not exist yet)
	Copy      bool   // duplicate instead of move
	Memory    string // keep-both | skip | replace
	DryRun    bool
}

// SessionPlan is one session's part of the plan.
type SessionPlan struct {
	ID      string `json:"id"`
	NewID   string `json:"new_id,omitempty"` // copies get a fresh identity
	SrcFile string `json:"src_file"`
	DstFile string `json:"dst_file"`
	SrcDir  string `json:"src_dir,omitempty"` // sidecar dir (subagents etc.)
	DstDir  string `json:"dst_dir,omitempty"`
	Open    bool   `json:"open,omitempty"` // live in a running agent
}

// Plan is everything a transplant will do, computable without writing.
type Plan struct {
	SourcePath   string
	TargetPath   string
	SourceSlug   string
	TargetSlug   string
	Sessions     []SessionPlan
	MemorySrc    string // source memory dir ("" when none)
	MemoryDst    string // where incoming memory lands
	MemoryAction string // move | copy | keep-both | skip | replace | none
	Emptied      bool   // the move drains the source project entirely
}

// claudeSlug replicates the agent's project-path encoding: every character
// outside [a-zA-Z0-9] becomes a dash.
func claudeSlug(path string) string {
	var b strings.Builder
	for _, c := range path {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func projectsRoot(cfg config.Config) string {
	for _, target := range cfg.ResolveTargets() {
		if target.Name == "claude" {
			return filepath.Join(target.Source, "projects")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// Build resolves the source sessions and memory and lays out the plan.
func Build(cfg config.Config, opts Options) (*Plan, error) {
	if opts.To == "" {
		return nil, fmt.Errorf("--to is required")
	}
	if (opts.SessionID == "") == (opts.Project == "") {
		return nil, fmt.Errorf("exactly one of --session or --project is required")
	}
	target, err := filepath.Abs(opts.To)
	if err != nil {
		return nil, err
	}
	root := projectsRoot(cfg)

	plan := &Plan{TargetPath: target, TargetSlug: claudeSlug(target)}

	if opts.Project != "" {
		source, err := filepath.Abs(opts.Project)
		if err != nil {
			return nil, err
		}
		plan.SourcePath = source
		plan.SourceSlug = claudeSlug(source)
	} else {
		slug, err := findSessionSlug(root, opts.SessionID)
		if err != nil {
			return nil, err
		}
		plan.SourceSlug = slug
		plan.SourcePath = sessionCwd(filepath.Join(root, slug, opts.SessionID+".jsonl"))
	}
	if plan.SourceSlug == plan.TargetSlug {
		return nil, fmt.Errorf("source and target are the same project")
	}

	srcDir := filepath.Join(root, plan.SourceSlug)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("no agent state for source project: %w", err)
	}
	dstDir := filepath.Join(root, plan.TargetSlug)
	live := guard.Live(guard.RegistryDir())

	remaining := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if opts.SessionID != "" && id != opts.SessionID {
			remaining++
			continue
		}
		session := SessionPlan{
			ID:      id,
			SrcFile: filepath.Join(srcDir, name),
			DstFile: filepath.Join(dstDir, name),
		}
		if _, open := live[id]; open {
			session.Open = true
		}
		if opts.Copy {
			session.NewID = newUUID()
			session.DstFile = filepath.Join(dstDir, session.NewID+".jsonl")
		}
		if info, err := os.Stat(filepath.Join(srcDir, id)); err == nil && info.IsDir() {
			session.SrcDir = filepath.Join(srcDir, id)
			session.DstDir = filepath.Join(dstDir, id)
			if session.NewID != "" {
				session.DstDir = filepath.Join(dstDir, session.NewID)
			}
		}
		plan.Sessions = append(plan.Sessions, session)
	}
	if len(plan.Sessions) == 0 {
		return nil, fmt.Errorf("no sessions to transplant")
	}
	for _, session := range plan.Sessions {
		if session.Open && !opts.Copy {
			return nil, fmt.Errorf("session %s is open in a running agent; close it first (copy is allowed)", session.ID)
		}
	}
	plan.Emptied = !opts.Copy && remaining == 0

	// Memory: project scope always carries it; session scope copies it
	// (the source project still needs it) unless the move drains the
	// source, in which case it moves.
	if info, err := os.Stat(filepath.Join(srcDir, "memory")); err == nil && info.IsDir() {
		plan.MemorySrc = filepath.Join(srcDir, "memory")
		targetHasMemory := false
		if info, err := os.Stat(filepath.Join(dstDir, "memory")); err == nil && info.IsDir() {
			targetHasMemory = true
		}
		memory := opts.Memory
		if memory == "" {
			memory = "keep-both"
		}
		switch {
		case targetHasMemory && memory == "skip":
			plan.MemoryAction = "skip"
		case targetHasMemory && memory == "replace":
			plan.MemoryAction = "replace"
			plan.MemoryDst = filepath.Join(dstDir, "memory")
		case targetHasMemory: // keep-both: land beside, never overwrite
			plan.MemoryAction = "keep-both"
			tail := plan.SourceSlug
			if len(tail) > 40 {
				tail = tail[len(tail)-40:]
			}
			plan.MemoryDst = filepath.Join(dstDir, "memory",
				fmt.Sprintf("transplanted-%s-from%s", time.Now().Format("20060102-150405"), tail))
		default:
			if plan.Emptied {
				plan.MemoryAction = "move"
			} else {
				plan.MemoryAction = "copy"
			}
			plan.MemoryDst = filepath.Join(dstDir, "memory")
		}
	} else {
		plan.MemoryAction = "none"
	}
	return plan, nil
}

// Apply executes the plan: copy + rewrite, verify, then (for moves) remove
// originals. Callers are expected to have synced the source to backup.
func Apply(cfg config.Config, plan *Plan, opts Options) error {
	release, err := lock.Acquire(cfg.BackupRoot)
	if err != nil {
		return err
	}
	defer release()

	if err := os.MkdirAll(filepath.Dir(plan.Sessions[0].DstFile), 0o700); err != nil {
		return err
	}

	for i := range plan.Sessions {
		session := &plan.Sessions[i]
		if _, err := os.Stat(session.DstFile); err == nil {
			return fmt.Errorf("target already has session file %s", filepath.Base(session.DstFile))
		}
		if err := rewriteSession(session.SrcFile, session.DstFile, plan.SourcePath, plan.TargetPath, session.ID, session.NewID); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
		if err := verifyLineCount(session.SrcFile, session.DstFile); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
		if session.SrcDir != "" {
			if err := copyTree(session.SrcDir, session.DstDir); err != nil {
				return fmt.Errorf("session %s sidecar: %w", session.ID, err)
			}
		}
	}

	switch plan.MemoryAction {
	case "copy", "move", "keep-both":
		if err := copyTree(plan.MemorySrc, plan.MemoryDst); err != nil {
			return fmt.Errorf("memory: %w", err)
		}
	case "replace":
		safety := filepath.Join(cfg.BackupRoot, "transplant-safety",
			time.Now().Format("20060102-150405"), plan.TargetSlug, "memory")
		if err := copyTree(plan.MemoryDst, safety); err != nil {
			return fmt.Errorf("memory safety copy: %w", err)
		}
		if err := os.RemoveAll(plan.MemoryDst); err != nil {
			return err
		}
		if err := copyTree(plan.MemorySrc, plan.MemoryDst); err != nil {
			return fmt.Errorf("memory: %w", err)
		}
	}

	// Copies leave every original in place; moves remove them only now,
	// after every destination is verified — and the pre-move state is in
	// backup regardless.
	if !opts.Copy {
		for _, session := range plan.Sessions {
			if err := os.Remove(session.SrcFile); err != nil {
				return fmt.Errorf("remove %s: %w", session.SrcFile, err)
			}
			if session.SrcDir != "" {
				if err := os.RemoveAll(session.SrcDir); err != nil {
					return err
				}
			}
		}
		if plan.MemoryAction == "move" {
			if err := os.RemoveAll(plan.MemorySrc); err != nil {
				return err
			}
		}
	}

	logTransplant(cfg, plan, opts)
	return nil
}

// rewriteSession streams the transcript line by line, retargeting cwd
// fields under the source path and (for copies) the session identity.
// Lines are decoded with UseNumber so numeric fields survive untouched;
// anything unparseable passes through verbatim.
func rewriteSession(src string, dst string, fromPath string, toPath string, oldID string, newID string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(out)

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		rewritten, changed := rewriteLine(line, fromPath, toPath, oldID, newID)
		if changed {
			line = rewritten
		}
		if _, err := writer.Write(line); err != nil {
			out.Close()
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			out.Close()
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		out.Close()
		return err
	}
	if err := writer.Flush(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func rewriteLine(line []byte, fromPath string, toPath string, oldID string, newID string) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var event map[string]any
	if decoder.Decode(&event) != nil {
		return nil, false
	}
	changed := false
	if fromPath != "" {
		if cwd, ok := event["cwd"].(string); ok {
			if cwd == fromPath {
				event["cwd"] = toPath
				changed = true
			} else if strings.HasPrefix(cwd, fromPath+string(os.PathSeparator)) {
				event["cwd"] = toPath + cwd[len(fromPath):]
				changed = true
			}
		}
	}
	if newID != "" {
		if id, ok := event["sessionId"].(string); ok && id == oldID {
			event["sessionId"] = newID
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func verifyLineCount(src string, dst string) error {
	a, err := countLines(src)
	if err != nil {
		return err
	}
	b, err := countLines(dst)
	if err != nil {
		return err
	}
	if a != b {
		return fmt.Errorf("verification failed: %d source lines vs %d written", a, b)
	}
	return nil
}

func countLines(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		count++
	}
	return count, scanner.Err()
}

func copyTree(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

// findSessionSlug locates which project dir holds a session id.
func findSessionSlug(root string, id string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, entry.Name(), id+".jsonl")); err == nil {
			return entry.Name(), nil
		}
	}
	return "", fmt.Errorf("session %s not found in claude storage", id)
}

// sessionCwd recovers the session's real project path from its cwd fields
// (slugs are lossy).
func sessionCwd(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var event struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Cwd != "" {
			return event.Cwd
		}
	}
	return ""
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("transplant-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func logTransplant(cfg config.Config, plan *Plan, opts Options) {
	action := "transplant"
	if opts.Copy {
		action = "transplant-copy"
	}
	now := time.Now()
	var entries []audit.Entry
	for _, session := range plan.Sessions {
		entries = append(entries, audit.Entry{
			Time:      now,
			Action:    action,
			Target:    "claude",
			SessionID: session.ID,
			From:      session.SrcFile,
			To:        session.DstFile,
		})
	}
	if plan.MemoryAction != "none" && plan.MemoryAction != "skip" {
		entries = append(entries, audit.Entry{
			Time:   now,
			Action: "transplant-memory-" + plan.MemoryAction,
			Target: "claude",
			From:   plan.MemorySrc,
			To:     plan.MemoryDst,
		})
	}
	audit.Append(cfg.BackupRoot, entries)
}
