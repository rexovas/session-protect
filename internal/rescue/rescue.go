// Package rescue recovers what backup could not: sessions that exist only
// as prompt history. Tier 1 exports that history as a readable artifact;
// tier 2 reconstructs a resumable session — a NEW session, explicitly
// marked, while the original stays designated lost forever. Agent history
// files are never modified.
package rescue

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

// Prompt is one recorded user prompt of a lost session.
type Prompt struct {
	Text string
	At   time.Time
}

// LostPrompts reads every prompt recorded for a session from the agent's
// history file (read-only), oldest first.
func LostPrompts(target string, sessionID string) (prompts []Prompt, project string, err error) {
	var path string
	switch target {
	case "claude":
		path = filepath.Join(targets.DetectClaude().Source, "history.jsonl")
	case "codex":
		path = filepath.Join(targets.DetectCodex().Source, "history.jsonl")
	default:
		return nil, "", fmt.Errorf("unknown target %q", target)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("no prompt history: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry struct {
			Display   string `json:"display"`
			SessionID string `json:"sessionId"`
			Project   string `json:"project"`
			Timestamp int64  `json:"timestamp"`
			// codex shape
			SessionID2 string `json:"session_id"`
			Text       string `json:"text"`
			Ts         int64  `json:"ts"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		id, text, at := entry.SessionID, entry.Display, time.UnixMilli(entry.Timestamp)
		if id == "" {
			id, text, at = entry.SessionID2, entry.Text, time.Unix(entry.Ts, 0)
		}
		if id != sessionID || text == "" {
			continue
		}
		if entry.Project != "" {
			project = entry.Project
		}
		prompts = append(prompts, Prompt{Text: text, At: at})
	}
	if len(prompts) == 0 {
		return nil, "", fmt.Errorf("no prompts recorded for session %s", sessionID)
	}
	return prompts, project, nil
}

// Export writes the lost session's prompt history as a markdown artifact
// under the backup root and records it in the audit log.
func Export(cfg config.Config, target string, sessionID string, title string) (string, error) {
	prompts, project, err := LostPrompts(target, sessionID)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Lost session — prompt history\n\n")
	if title != "" {
		fmt.Fprintf(&b, "**%s**\n\n", title)
	}
	fmt.Fprintf(&b, "- session: `%s` (%s)\n", sessionID, target)
	if project != "" {
		fmt.Fprintf(&b, "- project: `%s`\n", project)
	}
	fmt.Fprintf(&b, "- prompts: %d, %s → %s\n", len(prompts),
		prompts[0].At.Format("2006-01-02 15:04"), prompts[len(prompts)-1].At.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "\nOnly the human side survives in the agent's prompt history;\nresponses were lost with the transcript.\n")
	for _, prompt := range prompts {
		fmt.Fprintf(&b, "\n---\n\n`%s`\n\n%s\n", prompt.At.Format("2006-01-02 15:04"), prompt.Text)
	}

	dir := filepath.Join(cfg.BackupRoot, "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sessionID+".md")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	audit.Append(cfg.BackupRoot, []audit.Entry{{
		Time: time.Now(), Action: "export", Target: target, SessionID: sessionID, To: path,
	}})
	return path, nil
}

// Reconstruct builds a NEW resumable claude session from a lost session's
// prompt history: user prompts interleaved with placeholder responses,
// written under the project's slug with a fresh identity. The original
// stays lost; the audit log links the two. The line format was validated
// against a live resume before this shipped.
func Reconstruct(cfg config.Config, sessionID string, title string) (newID string, path string, err error) {
	prompts, project, err := LostPrompts("claude", sessionID)
	if err != nil {
		return "", "", err
	}
	if project == "" {
		return "", "", fmt.Errorf("session %s has no recorded project path", sessionID)
	}

	dir := filepath.Join(targets.DetectClaude().Source, "projects", targets.ClaudeSlug(project))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	newID = newUUID()
	path = filepath.Join(dir, newID+".jsonl")

	var b strings.Builder
	parent := ""
	write := func(kind string, at time.Time, message any) {
		id := newUUID()
		line := map[string]any{
			"type": kind, "sessionId": newID, "cwd": project,
			"uuid": id, "isSidechain": false, "userType": "external",
			"version":   "session-protect-reconstruct",
			"timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"),
			"message":   message,
		}
		if parent != "" {
			line["parentUuid"] = parent
		} else {
			line["parentUuid"] = nil
		}
		parent = id
		encoded, _ := json.Marshal(line)
		b.Write(encoded)
		b.WriteByte('\n')
	}

	note := "[reconstructed by session-protect — the original transcript was lost; " +
		"only the prompt history survived. The conversation continues from here.]"
	for i, prompt := range prompts {
		write("user", prompt.At, map[string]any{"role": "user", "content": prompt.Text})
		text := "[response lost]"
		if i == len(prompts)-1 {
			text = note
		}
		write("assistant", prompt.At.Add(time.Second), map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": text}},
		})
	}

	// O_EXCL: a fresh identity must never overwrite anything.
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	if _, err := out.WriteString(b.String()); err != nil {
		out.Close()
		return "", "", err
	}
	if err := out.Close(); err != nil {
		return "", "", err
	}

	audit.Append(cfg.BackupRoot, []audit.Entry{{
		Time: time.Now(), Action: "reconstruct", Target: "claude",
		SessionID: newID, From: sessionID, To: path,
	}})
	_ = title
	return newID, path, nil
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("rebuilt-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
