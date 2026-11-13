package browse

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/targets"
	"github.com/rexovas/session-protect/internal/transfer"
)

// Account transfer in the explorer (A): move a folder's sessions (with its
// subfolders) or one session to another account. The destination list
// leads with the folder's learned owner; the preview is the CLI's plan.

type xferState struct {
	opts    transfer.Options
	source  string   // what moves, for the heading
	choices []string // destination labels
	sel     int
	plan    *transfer.Plan
	preview []string
	busy    bool
}

type transferMsg struct {
	err   error
	moved int
	to    string
}

// sessionIndex is ownership facts from scanned sessions: live transcripts
// only, as on disk.
type sessionIndex map[string]map[string]int

func newSessionIndex(projects []*Project) sessionIndex {
	index := sessionIndex{}
	for _, project := range projects {
		for _, session := range project.Sessions {
			if session.Target != "claude" || session.SourcePath == "" || !filepath.IsAbs(project.Path) {
				continue
			}
			if index[project.Path] == nil {
				index[project.Path] = map[string]int{}
			}
			index[project.Path][homeOf(session)]++
		}
	}
	return index
}

func (s sessionIndex) HomesAt(dir string) map[string]int { return s[dir] }

func (s sessionIndex) OtherUnder(dir string, label string) bool {
	for path, homes := range s {
		if !targets.IsUnder(path, dir) {
			continue
		}
		for other, n := range homes {
			if other != label && n > 0 {
				return true
			}
		}
	}
	return false
}

// ownerOf resolves a folder's owner from the scan.
func (m model) ownerOf(path string) targets.Ownership {
	return targets.ResolveOwner(m.index(), m.homes, path)
}

func (m model) index() sessionIndex {
	if m.ownIndex != nil {
		return m.ownIndex
	}
	return newSessionIndex(m.projects)
}

// misplaced counts live claude sessions at or beneath path that sit on an
// account other than their folder's owner — what a transfer would fix.
func (m model) misplaced(path string) int {
	index := m.index()
	n := 0
	for folder, homes := range index {
		if folder != path && !targets.IsUnder(folder, path) {
			continue
		}
		owner := targets.ResolveOwner(index, m.homes, folder)
		if !owner.Owned() {
			continue
		}
		for label, count := range homes {
			if label != owner.Owner.Label {
				n += count
			}
		}
	}
	return n
}

// ownerBadge is the folder row's account marker: the owner, and ⇄N for
// sessions on another account inside an owned folder.
func (m model) ownerBadge(folder Folder, plain bool) string {
	if !m.multiHome() || folder.Pseudo {
		return ""
	}
	badge := ""
	owner := m.ownerOf(folder.Path)
	switch {
	case len(owner.Conflict) > 0:
		badge += styleUnless(plain, styleUnbacked, " ⚑"+strings.Join(owner.Conflict, "+"))
	case owner.Owned():
		badge += styleUnless(plain, styleAccount, " @"+owner.Owner.Label)
	}
	if n := m.misplaced(folder.Path); n > 0 {
		badge += styleUnless(plain, styleStale, fmt.Sprintf(" ⇄%d", n))
	}
	return badge
}

// pressTransfer opens the destination chooser for the folder or session
// under the cursor.
func (m model) pressTransfer() model {
	if !m.multiHome() {
		m.notice, m.noticeErr = "only one claude dir is set up; see `session-protect claude-dirs`", true
		return m
	}
	state := &xferState{}
	from := targets.DefaultHomeLabel
	owner := targets.Ownership{}
	if m.showSessions || m.showAll || m.showHits {
		session := m.selectedSession()
		if session == nil || session.Target != "claude" || session.SourcePath == "" {
			m.notice, m.noticeErr = "only live claude sessions change accounts", true
			return m
		}
		from = homeOf(*session)
		state.opts = transfer.Options{SessionID: session.ID, From: from}
		state.source = "session " + session.ID[:min(8, len(session.ID))] + "…"
		owner = m.ownerOf(session.ProjectPath)
	} else {
		if m.fCursor >= len(m.folders) || m.folders[m.fCursor].Pseudo {
			return m
		}
		path := m.folders[m.fCursor].Path
		state.opts = transfer.Options{Project: path, Recursive: true, From: from}
		state.source = tildePath(path) + " and its subfolders"
		owner = m.ownerOf(path)
	}
	// The owner leads; then the other extra homes. The source is never a
	// destination.
	if owner.Owned() && owner.Owner.Label != from {
		state.choices = append(state.choices, owner.Owner.Label)
	}
	for _, home := range m.homes {
		if home.Label != from && !(owner.Owned() && home.Label == owner.Owner.Label) {
			state.choices = append(state.choices, home.Label)
		}
	}
	m.xfer = state
	return m
}

func (m model) handleTransferKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.xfer
	if state.busy {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "n":
		if state.plan != nil {
			state.plan, state.preview = nil, nil
		} else {
			m.xfer = nil
		}
	case "up", "k":
		if state.plan == nil && state.sel > 0 {
			state.sel--
		}
	case "down", "j":
		if state.plan == nil && state.sel < len(state.choices)-1 {
			state.sel++
		}
	case "enter", "y":
		if state.plan == nil {
			if len(state.choices) == 0 {
				break
			}
			state.opts.To = state.choices[state.sel]
			plan, err := transfer.Build(m.cfg, state.opts)
			if err != nil {
				m.xfer = nil
				m.notice, m.noticeErr = "transfer: "+err.Error(), true
				break
			}
			var out bytes.Buffer
			transfer.PrintPlan(&out, plan, false)
			state.plan, state.preview = plan, strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
			break
		}
		state.busy = true
		cfg, plan := m.cfg, state.plan
		return m, tea.Batch(spin(), func() tea.Msg {
			if _, err := backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "pre-transfer"}); err != nil {
				return transferMsg{err: fmt.Errorf("pre-move backup: %w", err)}
			}
			if err := transfer.Apply(cfg, plan); err != nil {
				return transferMsg{err: err}
			}
			_, _ = backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "post-transfer"})
			return transferMsg{moved: len(plan.Sessions), to: plan.To.Label}
		})
	}
	return m, nil
}

func (m model) transferView() string {
	state := m.xfer
	if state.plan == nil {
		body := []string{styleDim.Render("move ") + styleBold.Render(state.source) + styleDim.Render(" to:"), ""}
		for i, label := range state.choices {
			row := "  " + label
			if i == 0 && m.xferOwnerFirst() {
				row += styleDim.Render("  (owns this folder)")
			}
			if i == state.sel {
				row = styleCursor.Render("❯ " + label)
			}
			body = append(body, row)
		}
		body = append(body, "", styleDim.Render("the folder stays put; sessions, memory and history change accounts"))
		return m.inputDialog("Transfer to which account?", body, "↑/↓ choose · enter preview · esc cancel")
	}
	body := make([]string, 0, len(state.preview))
	for _, line := range state.preview {
		body = append(body, truncate(line, max(40, m.width-8)))
	}
	if state.busy {
		return m.inputDialog("Transferring…", body, "backing up, moving, backing up")
	}
	return m.inputDialog("Transfer these?", body, "enter/y transfer · esc back")
}

// xferOwnerFirst reports whether the first choice is the folder's owner.
func (m model) xferOwnerFirst() bool {
	state := m.xfer
	if len(state.choices) == 0 {
		return false
	}
	path := state.opts.Project
	if path == "" {
		if session := m.selectedSession(); session != nil {
			path = session.ProjectPath
		}
	}
	owner := m.ownerOf(path)
	return owner.Owned() && owner.Owner.Label == state.choices[0]
}
