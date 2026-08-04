package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/transplant"
	"github.com/rexovas/session-protect/internal/version"
)

var (
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7D79F6"})
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#999999", Dark: "#666666"})
	styleCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#FFFFFF"}).Background(lipgloss.AdaptiveColor{Light: "#E8E8FF", Dark: "#333355"})
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0A8754", Dark: "#3DDC97"})
	styleActive   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9A7B0A", Dark: "#FFD700"})
	styleStale    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C05621", Dark: "#FFA657"})
	styleUnbacked = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C0392B", Dark: "#FF6B6B"})
	styleRecover  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8E44AD", Dark: "#C39BD3"})
	styleFooter   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#999999"})
	styleUserMsg  = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#E8E8E8", Dark: "#262626"}).Foreground(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#E6E6E6"})
	styleBold     = lipgloss.NewStyle().Bold(true)
)

type model struct {
	cfg      config.Config
	projects []*Project

	start string   // where the shell launched us
	root  string   // directory currently viewed
	trail []string // roots we descended through, for going back up

	folders []Folder
	here    *Project  // project whose sessions live exactly at root
	visible []Session // here's sessions, filtered by the lost toggle

	// expandAll renders every folder's subtree indented in place; one
	// toggle for the whole view rather than per-folder state.
	expandAll bool

	// query filters the current pane: the folder tree prunes to matching
	// branches (auto-expanded down to each match), session panes to
	// matching names/titles/ids. searching is true while / input is live.
	query     string
	searching bool

	// Transplant (t) is a two-stage page: type a target dir, enter plans,
	// enter again applies. tab toggles move/copy. Session scope from
	// session panes, project scope from folder rows.
	showTransplant bool
	tpSessionID    string // "" = project scope
	tpSource       string // source label (id or project path)
	tpProject      string
	tpInput        string
	tpCopy         bool
	tpPlan         *transplant.Plan
	tpBusy         bool

	// AI find (ctrl+g) is its own page: a free-text prompt that queries
	// on enter. askInput persists so a follow-up refines the last ask.
	showAsk    bool
	askInput   string
	askBackend string

	// Content search (ctrl+s on a query) counts transcript hits per
	// session under the current root and shows them as their own pane.
	// AI find results reuse the pane with hitsMode "ask": ranked
	// matches whose snippet line is the model's reasoning.
	showHits  bool
	hitsBusy  bool
	hitsMode  string // "hits" | "ask"
	hitsNote  string // backend name for ask results
	hitsQuery string
	hits      []Hit
	hCursor   int
	hOffset   int

	// showLost reveals sessions known only from prompt history. Hidden by
	// default so losses don't crowd the living sessions.
	showLost bool

	// One pane is visible at a time; tab switches folders/sessions and
	// ctrl+a opens the recursive all-sessions pane. Each pane keeps its
	// own cursor so switching does not lose the user's place.
	showSessions bool
	showAll      bool
	allSessions  []Session
	detail       *Session
	detailData   Detail
	detailTab    int // 0 overview · 1 usage · 2 tail
	tailLines    []string
	tailWidth    int
	tailOffset   int // lines scrolled up from the bottom
	// The menu pane collapses the non-navigation views behind one key:
	// tab-switchable stats / activity (audit log, newest first) / keys.
	showMenu  bool
	menuTab   int // 0 stats · 1 activity · 2 keys
	stats     *Stats
	activity  []audit.Entry
	actOffset int

	// confirmRestore holds the recoverable session awaiting a y/enter before
	// its backup copy is written back into the live tree; notice is the
	// one-line outcome shown until the next keypress.
	confirmRestore *Session
	notice         string
	noticeErr      bool
	fCursor        int
	fOffset        int
	sCursor        int
	sOffset        int
	aCursor        int
	aOffset        int

	scanning bool // a background rescan is in flight

	width  int
	height int
}

type rescanMsg []*Project

type hitsMsg []Hit

type transplantMsg struct {
	err    error
	moved  int
	target string
	copied bool
}

type askMsg struct {
	hits    []Hit
	backend string
	err     error
}

type tickMsg time.Time

// refreshEvery is the live-update cadence; rescans run asynchronously so
// the UI never blocks on them.
const refreshEvery = 5 * time.Second

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func newModel(cfg config.Config) model {
	projects := Scan(cfg)
	start, err := os.Getwd()
	if err != nil {
		start = string(os.PathSeparator)
	}
	m := model{cfg: cfg, projects: projects, start: start, width: 100, height: 32}
	m.root = NearestRoot(projects, start)
	m.rebuild()
	return m
}

func (m *model) rebuild() {
	if m.query != "" {
		m.folders = m.filteredTree(ChildrenOf(m.projects, m.root, m.start), 0)
	} else {
		m.folders = m.treeRows(ChildrenOf(m.projects, m.root, m.start), 0)
	}
	m.here = ProjectAt(m.projects, m.root)
	m.visible = nil
	if m.here != nil {
		LoadCustomNames(m.here)
		m.visible = m.filterQuery(m.filterLost(m.here.Sessions))
	}
	if m.showAll {
		m.allSessions = m.filterQuery(m.filterLost(AllUnder(m.projects, m.root)))
	}
	// Show the pane that has content when the other is empty — but never
	// while a query is active: a search with no matches yet must not yank
	// the user out of the pane they are searching.
	if m.query == "" {
		if len(m.folders) == 0 && m.sessionCount() > 0 {
			m.showSessions = true
		}
		if m.sessionCount() == 0 && len(m.folders) > 0 {
			m.showSessions = false
		}
	}
	m.setCursor(m.currentCursor())
}

// treeRows flattens the folder tree for display, descending everywhere
// when the expand-all toggle is on.
func (m *model) treeRows(folders []Folder, depth int) []Folder {
	var out []Folder
	for _, folder := range folders {
		folder.Depth = depth
		out = append(out, folder)
		if m.expandAll {
			out = append(out, m.treeRows(ChildrenOf(m.projects, folder.Path, m.start), depth+1)...)
		}
	}
	return out
}

// filteredTree prunes the folder tree to branches whose names match the
// query (case-insensitive), auto-expanded down to each match; ancestors
// of a match stay visible as path context.
func (m *model) filteredTree(folders []Folder, depth int) []Folder {
	query := strings.ToLower(m.query)
	var out []Folder
	for _, folder := range folders {
		children := m.filteredTree(ChildrenOf(m.projects, folder.Path, m.start), depth+1)
		if len(children) == 0 && !strings.Contains(strings.ToLower(folder.Name), query) {
			continue
		}
		folder.Depth = depth
		out = append(out, folder)
		out = append(out, children...)
	}
	return out
}

// filterQuery keeps sessions whose custom name, title, or id contains the
// query, case-insensitively.
func (m model) filterQuery(sessions []Session) []Session {
	if m.query == "" {
		return sessions
	}
	query := strings.ToLower(m.query)
	var out []Session
	for _, session := range sessions {
		if strings.Contains(strings.ToLower(session.CustomName), query) ||
			strings.Contains(strings.ToLower(session.Title), query) ||
			strings.Contains(strings.ToLower(session.ID), query) {
			out = append(out, session)
		}
	}
	return out
}

func (m model) sessionCount() int {
	return len(m.visible)
}

// filterLost hides history-only sessions unless the toggle is on. An
// active search overrides the toggle: a lost session is the one you most
// need to be able to find.
func (m model) filterLost(sessions []Session) []Session {
	if m.showLost || m.query != "" {
		return sessions
	}
	var out []Session
	for _, session := range sessions {
		if session.State != "LOST" {
			out = append(out, session)
		}
	}
	return out
}

func (m model) itemCount() int {
	switch {
	case m.showHits:
		return len(m.hits)
	case m.showAll:
		return len(m.allSessions)
	case m.showSessions:
		return m.sessionCount()
	default:
		return len(m.folders)
	}
}

func (m model) currentCursor() int {
	switch {
	case m.showHits:
		return m.hCursor
	case m.showAll:
		return m.aCursor
	case m.showSessions:
		return m.sCursor
	default:
		return m.fCursor
	}
}

// Init starts the refresh loop and an immediate full-name scan; launch
// itself stays fast (names arrive from cache or the first background pass).
func (m model) Init() tea.Cmd {
	cfg := m.cfg
	return tea.Batch(tick(), func() tea.Msg { return rescanMsg(ScanNamed(cfg)) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.detail != nil {
			m.buildTailLines()
		}
		return m, nil
	case tickMsg:
		if m.scanning {
			return m, tick()
		}
		m.scanning = true
		cfg := m.cfg
		return m, tea.Batch(tick(), func() tea.Msg { return rescanMsg(ScanNamed(cfg)) })
	case rescanMsg:
		m.scanning = false
		m.projects = msg
		m.rebuild()
		return m, nil
	case hitsMsg:
		m.hitsBusy = false
		m.hitsMode = "hits"
		m.hits = msg
		m.showHits = true
		m.hCursor, m.hOffset = 0, 0
		return m, nil
	case transplantMsg:
		m.tpBusy = false
		m.showTransplant = false
		m.tpPlan = nil
		if msg.err != nil {
			m.notice, m.noticeErr = "transplant failed: "+msg.err.Error(), true
			return m, nil
		}
		verb := "moved"
		if msg.copied {
			verb = "copied"
		}
		m.notice = fmt.Sprintf("%s %d session(s) to %s", verb, msg.moved, msg.target)
		m.scanning = true
		cfg := m.cfg
		return m, func() tea.Msg { return rescanMsg(ScanNamed(cfg)) }
	case askMsg:
		m.hitsBusy = false
		if msg.err != nil {
			m.notice, m.noticeErr = "ai find failed: "+msg.err.Error(), true
			return m, nil
		}
		m.hitsMode = "ask"
		m.hitsNote = msg.backend
		m.hits = msg.hits
		m.showHits = true
		m.hCursor, m.hOffset = 0, 0
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.detail != nil {
				m.scrollTail(3)
			} else if m.showMenu {
				m.actOffset = max(0, m.actOffset-3)
			} else {
				m.setCursor(m.currentCursor() - 3)
			}
		case tea.MouseButtonWheelDown:
			if m.detail != nil {
				m.scrollTail(-3)
			} else if m.showMenu {
				m.actOffset = min(max(0, len(m.activity)-m.pageSize()), m.actOffset+3)
			} else {
				m.setCursor(m.currentCursor() + 3)
			}
		}
		return m, nil
	}
	return m, nil
}

// scrollTail moves the tail viewport; positive is toward earlier lines.
func (m *model) scrollTail(delta int) {
	if m.detailTab != 2 {
		return
	}
	m.tailOffset = max(0, min(m.tailOffset+delta, max(0, len(m.tailLines)-3)))
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.detail != nil {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "i", "esc", "q":
			m.detail = nil
			m.detailTab, m.tailOffset = 0, 0
			m.tailLines = nil
		case "tab", "right", "l":
			m.detailTab = (m.detailTab + 1) % 3
		case "left", "h":
			m.detailTab = (m.detailTab + 2) % 3
		case "up", "k":
			m.scrollTail(1)
		case "down", "j":
			m.scrollTail(-1)
		case "pgup":
			m.scrollTail(10)
		case "pgdown":
			m.scrollTail(-10)
		}
		return m, nil
	}
	if m.showMenu {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "m", "esc", "q", "backspace", "enter":
			m.showMenu = false
			m.actOffset = 0
		case "tab", "right", "l":
			m.menuTab = (m.menuTab + 1) % 3
			m.actOffset = 0
		case "left", "h":
			m.menuTab = (m.menuTab + 2) % 3
			m.actOffset = 0
		case "up", "k":
			m.actOffset = max(0, m.actOffset-1)
		case "down", "j":
			m.actOffset = min(max(0, len(m.activity)-m.pageSize()), m.actOffset+1)
		case "pgup":
			m.actOffset = max(0, m.actOffset-m.pageSize())
		case "pgdown":
			m.actOffset = min(max(0, len(m.activity)-m.pageSize()), m.actOffset+m.pageSize())
		}
		return m, nil
	}
	if m.confirmRestore != nil {
		session := *m.confirmRestore
		m.confirmRestore = nil
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "y", "enter":
			dest, err := RestoreSession(m.cfg, session)
			if err != nil {
				m.notice, m.noticeErr = "restore failed: "+err.Error(), true
			} else {
				m.notice, m.noticeErr = "restored "+session.ID+" → "+dest, false
			}
			m.scanning = true
			cfg := m.cfg
			return m, func() tea.Msg { return rescanMsg(ScanNamed(cfg)) }
		}
		return m, nil
	}
	if m.showTransplant {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.tpPlan != nil {
				m.tpPlan = nil
			} else if !m.tpBusy {
				m.showTransplant = false
			}
		case "tab":
			if !m.tpBusy {
				m.tpCopy = !m.tpCopy
				m.tpPlan = nil
			}
		case "enter":
			if m.tpBusy {
				break
			}
			if m.tpPlan != nil {
				return m.fireTransplant()
			}
			if strings.TrimSpace(m.tpInput) != "" {
				plan, err := transplant.Build(m.cfg, m.transplantOptions())
				if err != nil {
					m.notice, m.noticeErr = err.Error(), true
					break
				}
				m.notice = ""
				m.tpPlan = plan
			}
		case "backspace":
			if m.tpPlan == nil {
				if runes := []rune(m.tpInput); len(runes) > 0 {
					m.tpInput = string(runes[:len(runes)-1])
				}
			}
		default:
			if m.tpPlan == nil && (msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace) {
				m.tpInput += msg.String()
			}
		}
		return m, nil
	}
	if m.showAsk {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "ctrl+g":
			m.showAsk = false
		case "enter":
			if strings.TrimSpace(m.askInput) != "" && !m.hitsBusy {
				m.showAsk = false
				return m.fireAsk()
			}
		case "backspace":
			if runes := []rune(m.askInput); len(runes) > 0 {
				m.askInput = string(runes[:len(runes)-1])
			}
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.askInput += msg.String()
			}
		}
		return m, nil
	}
	if m.searching {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.searching = false
			m.query = ""
			m.rebuild()
		case "enter":
			m.searching = false
		case "ctrl+s":
			if m.query != "" && !m.hitsBusy {
				m.searching = false
				return m.fireContentSearch()
			}
		case "ctrl+g":
			m.searching = false
			return m.openAsk()
		case "backspace":
			if runes := []rune(m.query); len(runes) > 0 {
				m.query = string(runes[:len(runes)-1])
			}
			m.rebuild()
			m.setCursor(0)
		case "up":
			m.setCursor(m.currentCursor() - 1)
		case "down":
			m.setCursor(m.currentCursor() + 1)
		case "pgup":
			m.setCursor(m.currentCursor() - m.pageSize())
		case "pgdown":
			m.setCursor(m.currentCursor() + m.pageSize())
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.query += msg.String()
				m.rebuild()
				m.setCursor(0)
			}
		}
		return m, nil
	}
	m.notice = ""

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "m", "s", "a":
		m.stats = LoadStats()
		entries := audit.Read(m.cfg.BackupRoot)
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
		m.activity = entries
		m.actOffset = 0
		switch msg.String() {
		case "s":
			m.menuTab = 0
		case "a":
			m.menuTab = 1
		}
		m.showMenu = true
	case "x":
		m.showLost = !m.showLost
		m.rebuild()
	case "i":
		(&m).openDetail()
	case "r":
		if session := m.selectedSession(); session != nil && session.State == "MISSING_SOURCE" {
			copied := *session
			m.confirmRestore = &copied
		}
	case "ctrl+r":
		m.scanning = true
		cfg := m.cfg
		return m, func() tea.Msg { return rescanMsg(ScanNamed(cfg)) }
	case "tab":
		if m.showHits {
			break
		}
		if m.showAll {
			m.showAll = false
			m.showSessions = len(m.folders) == 0 && m.sessionCount() > 0
		} else if len(m.folders) > 0 && m.sessionCount() > 0 {
			m.showSessions = !m.showSessions
		}
		m.setCursor(m.currentCursor())
	case "ctrl+a":
		if m.showHits {
			break
		}
		if len(m.folders) == 0 {
			break // sessions here ARE all nested sessions; nothing to add
		}
		m.showAll = !m.showAll
		if m.showAll {
			m.allSessions = m.filterLost(AllUnder(m.projects, m.root))
			m.aCursor, m.aOffset = 0, 0
		}
		m.setCursor(m.currentCursor())
	case "ctrl+e":
		m.expandAll = !m.expandAll
		m.rebuild()
	case "/":
		if m.showHits {
			m.showHits = false
		}
		m.searching = true
		m.query = ""
		m.rebuild()
	case "ctrl+s":
		if m.query != "" && !m.hitsBusy {
			return m.fireContentSearch()
		}
	case "ctrl+g":
		return m.openAsk()
	case "t":
		if m.showSessions || m.showAll || m.showHits {
			session := m.selectedSession()
			if session == nil {
				break
			}
			if session.Target != "claude" {
				m.notice, m.noticeErr = "transplant supports claude sessions for now", true
				break
			}
			if session.State == "LOST" {
				m.notice, m.noticeErr = "lost sessions have no transcript to transplant", true
				break
			}
			m.tpSessionID = session.ID
			m.tpSource = "session " + session.ID
		} else {
			if m.fCursor >= len(m.folders) || m.folders[m.fCursor].Pseudo {
				break
			}
			m.tpSessionID = ""
			m.tpProject = m.folders[m.fCursor].Path
			m.tpSource = "project " + tildePath(m.tpProject)
		}
		m.tpInput = m.root + string(os.PathSeparator)
		m.tpPlan = nil
		m.tpCopy = false
		m.showTransplant = true
	case "up", "k":
		m.setCursor(m.currentCursor() - 1)
	case "down", "j":
		m.setCursor(m.currentCursor() + 1)
	case "pgup":
		m.setCursor(m.currentCursor() - m.pageSize())
	case "pgdown":
		m.setCursor(m.currentCursor() + m.pageSize())
	case "g":
		m.setCursor(0)
	case "G":
		m.setCursor(1 << 30)
	case "enter", "l", "right":
		if m.showHits || m.showSessions || m.showAll {
			(&m).openDetail()
		} else if m.fCursor < len(m.folders) {
			m.trail = append(m.trail, m.root)
			m.root = m.folders[m.fCursor].Path
			m.resetPanes()
			m.rebuild()
		}
	case "esc", "backspace", "h", "left":
		if m.showHits {
			m.showHits = false
			return m, nil
		}
		if m.query != "" {
			m.query = ""
			m.rebuild()
			return m, nil
		}
		// Leave the current pane first, but never land on an empty pane:
		// at a leaf folder (sessions only) back means up.
		if m.showAll {
			m.showAll = false
			m.showSessions = len(m.folders) == 0 && m.sessionCount() > 0
			m.setCursor(m.currentCursor())
			return m, nil
		}
		if m.showSessions && len(m.folders) > 0 {
			m.showSessions = false
			m.setCursor(m.currentCursor())
			return m, nil
		}
		m.goUp()
	}
	return m, nil
}

func (m model) selectedSession() *Session {
	switch {
	case m.showHits:
		if m.hCursor < len(m.hits) {
			return &m.hits[m.hCursor].Session
		}
	case m.showAll:
		if m.aCursor < len(m.allSessions) {
			return &m.allSessions[m.aCursor]
		}
	case m.showSessions:
		if m.sCursor < len(m.visible) {
			return &m.visible[m.sCursor]
		}
	}
	return nil
}

func (m *model) resetPanes() {
	m.fCursor, m.fOffset, m.sCursor, m.sOffset, m.aCursor, m.aOffset = 0, 0, 0, 0, 0, 0
	m.showSessions = false
	m.showAll = false
}

func (m *model) goUp() {
	target := ""
	if len(m.trail) > 0 {
		target = m.trail[len(m.trail)-1]
		m.trail = m.trail[:len(m.trail)-1]
	} else if parent := parentDir(m.root); parent != m.root {
		target = parent
	}
	if target == "" {
		return
	}
	m.root = target
	m.resetPanes()
	m.rebuild()
}

func parentDir(path string) string {
	if !strings.HasPrefix(path, string(os.PathSeparator)) {
		return path // pseudo root; only the trail can leave it
	}
	parent := path
	if idx := strings.LastIndex(path, string(os.PathSeparator)); idx > 0 {
		parent = path[:idx]
	} else if idx == 0 && path != string(os.PathSeparator) {
		parent = string(os.PathSeparator)
	}
	return parent
}

func (m *model) setCursor(position int) {
	if position < 0 {
		position = 0
	}
	if position > m.itemCount()-1 {
		position = max(0, m.itemCount()-1)
	}
	switch {
	case m.showHits:
		m.hCursor = position
		m.hOffset = clampOffset(m.hOffset, position, m.pageSize())
	case m.showAll:
		m.aCursor = position
		m.aOffset = clampOffset(m.aOffset, position, m.pageSize())
	case m.showSessions:
		m.sCursor = position
		m.sOffset = clampOffset(m.sOffset, position, m.pageSize())
	default:
		m.fCursor = position
		m.fOffset = clampOffset(m.fOffset, position, m.pageSize())
	}
}

func (m model) pageSize() int { return max(4, m.height-6) }

// shortCommit is the installed build's revision, shown faintly for
// dogfooding traceability.
func shortCommit() string {
	commit := version.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if commit == "unknown" {
		return ""
	}
	return commit
}

// nameWidth is the flexible folder-name column: everything the fixed
// columns don't use.
func (m model) nameWidth() int { return max(24, m.width-46) }

// titleWidth is the flexible session-title column; the all pane reserves
// room for the IN column.
func (m model) titleWidth() int {
	width := m.width - 54 // state, agent, model, size, modified columns
	if m.showAll {
		width -= 31
	}
	return max(24, width)
}

// displayModel compacts a model id for column display.
func displayModel(model string) string {
	if model == "" {
		return "-"
	}
	model = strings.TrimPrefix(model, "claude-")
	if idx := strings.LastIndex(model, "-20"); idx > 0 && len(model)-idx >= 9 {
		model = model[:idx]
	}
	return model
}

func clampOffset(offset int, cursor int, page int) int {
	if cursor < offset {
		return cursor
	}
	if cursor >= offset+page {
		return cursor - page + 1
	}
	return offset
}

func (m model) View() string {
	if m.detail != nil {
		return m.detailView()
	}
	if m.showMenu {
		return m.menuView()
	}
	if m.showTransplant {
		return m.transplantView()
	}
	if m.showAsk {
		return m.askView()
	}
	if m.showHits {
		return m.hitsView()
	}
	var b strings.Builder

	total := 0
	for _, folder := range m.folders {
		total += folder.Sessions
	}
	total += m.sessionCount()
	name := m.root
	if idx := strings.LastIndex(m.root, string(os.PathSeparator)); idx >= 0 && len(m.root) > idx+1 {
		name = m.root[idx+1:]
	}
	beneath := fmt.Sprintf("  total nested sessions %d ", total)
	countStyle := styleDim
	if m.showAll {
		countStyle = styleCursor // the all pane is exactly this set
	}
	left := styleHeader.Render("Session Explorer ▸ "+truncate(name, 40)) + countStyle.Render(beneath)
	right := m.tabBar()
	if pad := m.width - lipgloss.Width(left) - lipgloss.Width(right); pad > 0 {
		left += strings.Repeat(" ", pad)
	}
	b.WriteString(left + right + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n")

	cursor := m.currentCursor()
	switch {
	case m.showAll:
		b.WriteString(styleDim.Render(fmt.Sprintf(" %-11s  %-*s %-7s %-12s %8s  %-8s %s",
			"STATE", m.titleWidth(), "TITLE", "AGENT", "MODEL", "SIZE", "MODIFIED", "IN")) + "\n")
		end := min(len(m.allSessions), m.aOffset+m.pageSize())
		for i := m.aOffset; i < end; i++ {
			b.WriteString(m.allSessionRow(m.allSessions[i], i == cursor) + "\n")
		}
	case m.showSessions:
		b.WriteString(styleDim.Render(fmt.Sprintf(" %-11s  %-*s %-7s %-12s %8s  %s",
			"STATE", m.titleWidth(), "TITLE", "AGENT", "MODEL", "SIZE", "MODIFIED")) + "\n")
		end := min(m.sessionCount(), m.sOffset+m.pageSize())
		for i := m.sOffset; i < end; i++ {
			b.WriteString(m.sessionRow(m.visible[i], i == cursor) + "\n")
		}
	default:
		b.WriteString(styleDim.Render(fmt.Sprintf("   %-*s  %8s  %8s  %-9s%s",
			m.nameWidth(), "FOLDER", "SESSIONS", "SIZE", "LAST USED", " HEALTH")) + "\n")
		end := min(len(m.folders), m.fOffset+m.pageSize())
		for i := m.fOffset; i < end; i++ {
			b.WriteString(m.folderRow(m.folders[i], i == cursor) + "\n")
		}
	}
	if m.itemCount() == 0 {
		empty := "  nothing here"
		if m.query != "" {
			empty = "  no matches for /" + m.query + " in this pane"
		}
		b.WriteString(styleDim.Render(empty) + "\n")
	}
	if m.confirmRestore != nil {
		b.WriteString("\n" + m.confirmRestoreBox(*m.confirmRestore) + "\n")
	} else if m.notice != "" {
		noticeStyle := styleOK
		if m.noticeErr {
			noticeStyle = styleUnbacked
		}
		b.WriteString("\n " + noticeStyle.Render(truncate(m.notice, m.width-2)) + "\n")
	}

	help := "↑/↓ move · enter open · ← back · tab panes · ctrl+a all · m menu · q quit"
	if m.searching {
		help = "/" + m.query + "▌   enter keep · ctrl+s transcripts · ctrl+g ai find · esc cancel"
	} else if m.query != "" {
		help = "/" + m.query + "   enter open · ctrl+s transcripts · ctrl+g ai find · esc clear"
	}
	if m.hitsBusy {
		help = "searching transcripts for /" + m.hitsQuery + " … (first run builds the text index)"
		if m.hitsMode == "ask" {
			help = "asking the local model about \"" + m.hitsQuery + "\" …"
		}
	}
	if m.confirmRestore != nil {
		help = "y/enter restore · esc cancel"
	}
	return m.pinBottom(b.String(), help)
}

// fireContentSearch kicks the transcript search off in the background;
// the first run also builds the extracted-text cache.
func (m model) fireContentSearch() (tea.Model, tea.Cmd) {
	m.hitsBusy = true
	m.hitsQuery = m.query
	cfg, query := m.cfg, m.query
	scope := AllUnder(m.projects, m.root)
	return m, func() tea.Msg { return hitsMsg(ContentSearch(cfg, scope, query)) }
}

func (m model) transplantOptions() transplant.Options {
	return transplant.Options{
		SessionID: m.tpSessionID,
		Project:   m.tpProject,
		To:        strings.TrimSpace(m.tpInput),
		Copy:      m.tpCopy,
	}
}

// fireTransplant commits the pre-move state to backup, applies the plan,
// then commits the transplanted state — both ends land in git history.
func (m model) fireTransplant() (tea.Model, tea.Cmd) {
	m.tpBusy = true
	cfg, plan, opts := m.cfg, m.tpPlan, m.transplantOptions()
	return m, func() tea.Msg {
		if _, err := backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "pre-transplant"}); err != nil {
			return transplantMsg{err: fmt.Errorf("pre-move backup: %w", err)}
		}
		if err := transplant.Apply(cfg, plan, opts); err != nil {
			return transplantMsg{err: err}
		}
		if _, err := backup.Execute(cfg, backup.Options{AllowUnencrypted: true, Action: "post-transplant"}); err != nil {
			return transplantMsg{err: fmt.Errorf("moved, but post-transplant backup failed: %w", err)}
		}
		return transplantMsg{moved: len(plan.Sessions), target: plan.TargetPath, copied: opts.Copy}
	}
}

// openAsk shows the ai find page, seeding the prompt with an active
// filter query when the prompt is empty. The backend is probed once here,
// never per frame.
func (m model) openAsk() (tea.Model, tea.Cmd) {
	backend := assist.Detect(m.cfg.Assist)
	if backend == nil {
		m.notice = "ai find needs ollama running or the claude CLI on PATH (assist.backend in config)"
		m.noticeErr = true
		return m, nil
	}
	m.askBackend = backend.Name()
	if m.askInput == "" {
		m.askInput = m.query
	}
	m.showAsk = true
	return m, nil
}

// fireAsk asks the configured local model which sessions match the
// description, grounded on keyword-scored candidates with excerpts.
func (m model) fireAsk() (tea.Model, tea.Cmd) {
	backend := assist.Detect(m.cfg.Assist)
	if backend == nil {
		m.notice = "ai find needs ollama running or the claude CLI on PATH (assist.backend in config)"
		m.noticeErr = true
		return m, nil
	}
	m.hitsBusy = true
	m.hitsMode = "ask"
	m.hitsQuery = m.askInput
	cfg, query := m.cfg, m.askInput
	scope := AllUnder(m.projects, m.root)
	return m, func() tea.Msg {
		candidates := BuildCandidates(cfg, scope, query)
		matches, err := backend.Rank(query, candidates)
		if err != nil {
			return askMsg{backend: backend.Name(), err: err}
		}
		byID := map[string]Session{}
		for _, session := range scope {
			byID[session.ID] = session
		}
		var hits []Hit
		for _, match := range matches {
			if session, ok := byID[match.ID]; ok {
				hits = append(hits, Hit{Session: session, Snippet: match.Reason})
			}
		}
		return askMsg{hits: hits, backend: backend.Name()}
	}
}

// openDetail loads the inspector for the selected session.
func (m *model) openDetail() {
	session := m.selectedSession()
	if session == nil {
		return
	}
	m.detail = session
	if session.State == "LOST" {
		m.detailData = LoadLostDetail(session.ID)
	} else {
		m.detailData = LoadDetail(*session)
	}
	m.buildTailLines()
}

// menuView is the tabbed home of everything that is not navigation:
// usage stats, the activity log, and the full key reference.
func (m model) menuView() string {
	tab := func(label string, index int) string {
		if m.menuTab == index {
			return styleCursor.Render(" " + label + " ")
		}
		return styleDim.Render(" " + label + " ")
	}
	left := styleHeader.Render("Session Explorer ▸ menu")
	right := tab("Stats", 0) + tab("Activity", 1) + tab("Keys", 2)
	if pad := m.width - lipgloss.Width(left) - lipgloss.Width(right); pad > 0 {
		left += strings.Repeat(" ", pad)
	}

	var b strings.Builder
	b.WriteString(left + right + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n")

	help := "tab sections · esc close"
	switch m.menuTab {
	case 1:
		m.activityBody(&b)
		help = "↑/↓/wheel scroll · tab sections · esc close"
	case 2:
		m.keysBody(&b)
	default:
		m.statsBody(&b)
	}
	return m.pinBottom(b.String(), help)
}

// statsBody shows the protection summary plus global usage statistics from
// the agent's own stats cache.
func (m model) statsBody(b *strings.Builder) {
	var ok, stale, unbacked, recoverable, lost, restored int
	for _, project := range m.projects {
		for _, session := range project.Sessions {
			switch session.State {
			case "OK", "ACTIVE", "OPEN":
				ok++
			case "RESTORED":
				ok++
				restored++
			case "STALE_BACKUP":
				stale++
			case "MISSING_BACKUP":
				unbacked++
			case "MISSING_SOURCE":
				recoverable++
			case "LOST":
				lost++
			}
		}
	}
	protection := "  " + styleOK.Render(fmt.Sprintf("● %d ok", ok)) +
		styleStale.Render(fmt.Sprintf("   ~ %d stale", stale)) +
		styleUnbacked.Render(fmt.Sprintf("   ! %d unbacked", unbacked)) +
		styleRecover.Render(fmt.Sprintf("   ✝ %d recoverable", recoverable)) +
		styleDim.Render(fmt.Sprintf("   ✕ %d lost", lost))
	if restored > 0 {
		protection += styleRecover.Render(fmt.Sprintf("   ✚ %d restored", restored))
	}
	b.WriteString(styleDim.Render("  PROTECTION") + "\n" + protection + "\n")
	restores, restoredIDs, lastRestore := 0, map[string]bool{}, time.Time{}
	for _, entry := range m.activity {
		if entry.Action == "restore" {
			restores++
			restoredIDs[entry.SessionID] = true
			if entry.Time.After(lastRestore) {
				lastRestore = entry.Time
			}
		}
	}
	if restores > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf(
			"  %d restore(s) across %d session(s) all-time · last %s · press a for the activity log",
			restores, len(restoredIDs), ago(lastRestore))) + "\n")
	}
	b.WriteString("\n")

	if m.stats == nil {
		b.WriteString(styleDim.Render("  no stats available (claude stats cache not found)") + "\n")
		return
	}

	b.WriteString(styleDim.Render(fmt.Sprintf("  %-28s %10s %10s %12s %12s %10s",
		"MODEL", "INPUT", "OUTPUT", "CACHE READ", "CACHE WRITE", "COST")) + "\n")
	var total ModelStats
	for _, model := range m.stats.Models {
		b.WriteString(fmt.Sprintf("  %-28s %10s %10s %12s %12s %10s\n",
			truncate(model.Model, 28), humanTokens(model.Input), humanTokens(model.Output),
			humanTokens(model.CacheRead), humanTokens(model.CacheWrite),
			fmt.Sprintf("$%.2f", model.CostUSD)))
		total.Input += model.Input
		total.Output += model.Output
		total.CacheRead += model.CacheRead
		total.CacheWrite += model.CacheWrite
		total.CostUSD += model.CostUSD
	}
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-28s %10s %10s %12s %12s %10s",
		"total", humanTokens(total.Input), humanTokens(total.Output),
		humanTokens(total.CacheRead), humanTokens(total.CacheWrite),
		fmt.Sprintf("$%.2f", total.CostUSD))) + "\n")

	if len(m.stats.Daily) > 0 {
		b.WriteString("\n" + styleDim.Render(fmt.Sprintf("  %-12s %10s %10s %11s",
			"DAY", "MESSAGES", "SESSIONS", "TOOL CALLS")) + "\n")
		days := m.stats.Daily
		if len(days) > 10 {
			days = days[:10]
		}
		for _, day := range days {
			b.WriteString(fmt.Sprintf("  %-12s %10d %10d %11d\n",
				day.Date, day.Messages, day.Sessions, day.ToolCalls))
		}
	}
	b.WriteString("\n" + styleDim.Render(fmt.Sprintf(
		"  %d sessions all-time · source: claude stats cache, computed %s",
		m.stats.TotalSessions, m.stats.LastComputed)) + "\n")
}

// transplantView is the two-stage transplant page: target input, then a
// plan preview that applies on enter.
func (m model) transplantView() string {
	var b strings.Builder
	mode := "move"
	if m.tpCopy {
		mode = "copy"
	}
	b.WriteString(styleHeader.Render("Session Explorer ▸ transplant") +
		styleDim.Render("  "+m.tpSource+" ") + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n\n")

	width := max(min(m.width, 110), 40)
	inner := width - 6
	modeLine := " mode: " + styleBold.Render(mode) + styleDim.Render("  (tab toggles move/copy)")
	if m.tpCopy {
		modeLine += styleDim.Render("  · copies mint a fresh session id")
	}
	b.WriteString(modeLine + "\n\n")
	b.WriteString(styleDim.Render(" target directory") + "\n")
	prompt := m.tpInput
	if m.tpPlan == nil {
		prompt += "▌"
	}
	b.WriteString(detailBox(width).Render(styleActive.Render("❯ ")+truncate(prompt, inner)) + "\n")

	help := "enter plan · tab mode · esc close"
	if m.tpPlan != nil {
		plan := m.tpPlan
		var lines []string
		lines = append(lines, styleBold.Render(fmt.Sprintf("%d session(s)", len(plan.Sessions)))+
			styleDim.Render("  "+tildePath(plan.SourcePath)+" → "+tildePath(plan.TargetPath)))
		for i, session := range plan.Sessions {
			if i == 4 && len(plan.Sessions) > 5 {
				lines = append(lines, styleDim.Render(fmt.Sprintf("  … and %d more", len(plan.Sessions)-i)))
				break
			}
			line := "  " + session.ID
			if session.NewID != "" {
				line += styleDim.Render(" → " + session.NewID)
			}
			lines = append(lines, line)
		}
		switch plan.MemoryAction {
		case "none":
			lines = append(lines, styleDim.Render("memory: none at source"))
		case "keep-both":
			lines = append(lines, styleStale.Render("memory: target already has memory — incoming lands beside it"))
		case "replace":
			lines = append(lines, styleUnbacked.Render("memory: REPLACES target memory (safety copy kept)"))
		default:
			lines = append(lines, styleDim.Render("memory: "+plan.MemoryAction+"s with the sessions"))
		}
		b.WriteString("\n" + styleDim.Render(" plan") + "\n")
		b.WriteString(detailBox(width).Render(strings.Join(lines, "\n")) + "\n")
		help = "enter " + mode + " · esc back · tab mode"
	}
	if m.tpBusy {
		help = "transplanting… (source synced to backup first)"
	}
	return m.pinBottomBare(b.String(), help)
}

// askView is the ai find page: describe the session in free text, enter
// asks the configured backend.
func (m model) askView() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("Session Explorer ▸ ai find") +
		styleDim.Render("  backend: "+m.askBackend+" ") + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n\n")

	b.WriteString(styleDim.Render(" describe the session you're looking for — project names, topics,"+
		" anything you remember") + "\n")
	width := max(min(m.width, 110), 40)
	inner := width - 6
	prompt := m.askInput + "▌"
	b.WriteString(detailBox(width).Render(styleActive.Render("❯ ")+
		strings.Join(wrapPreserve(prompt, inner, 8), "\n  ")) + "\n")
	b.WriteString(styleDim.Render(" matches are grounded in your transcripts and ranked with a short"+
		" reason each") + "\n")
	return m.pinBottomBare(b.String(), "enter ask · esc close")
}

// hitsView lists content-search results ranked by hit count; the
// selected row's best matching line shows above the footer.
func (m model) hitsView() string {
	var b strings.Builder
	heading := "Session Explorer ▸ transcript hits"
	note := fmt.Sprintf("  /%s · %d session(s) ", m.hitsQuery, len(m.hits))
	if m.hitsMode == "ask" {
		heading = "Session Explorer ▸ ai find"
		note = fmt.Sprintf("  \"%s\" · %d match(es) via %s ", m.hitsQuery, len(m.hits), m.hitsNote)
	}
	b.WriteString(styleHeader.Render(heading) + styleDim.Render(note) + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n")

	if len(m.hits) == 0 {
		empty := "  no transcript matches for /" + m.hitsQuery
		if m.hitsMode == "ask" {
			empty = "  the model found no likely sessions for that description"
		}
		b.WriteString(styleDim.Render(empty) + "\n")
		return m.pinBottomBare(b.String(), "esc back · / new search · ctrl+c quit")
	}

	countHeader := "HITS"
	if m.hitsMode == "ask" {
		countHeader = "RANK"
	}
	b.WriteString(styleDim.Render(fmt.Sprintf("  %6s  %-*s %-7s %s",
		countHeader, m.titleWidth(), "TITLE", "AGENT", "IN")) + "\n")
	end := min(len(m.hits), m.hOffset+m.pageSize())
	for i := m.hOffset; i < end; i++ {
		b.WriteString(m.hitRow(m.hits[i], i, i == m.hCursor) + "\n")
	}
	if m.hCursor < len(m.hits) {
		if snippet := m.hits[m.hCursor].Snippet; snippet != "" {
			prefix := "❯ "
			if m.hitsMode == "ask" {
				prefix = "why: "
			}
			b.WriteString("\n " + styleDim.Render(prefix+truncate(snippet, m.width-4-len(prefix))) + "\n")
		}
	}
	return m.pinBottomBare(b.String(), "↑/↓ move · enter open · esc back · / new search")
}

func (m model) hitRow(hit Hit, index int, active bool) string {
	title := hit.Session.CustomName
	if title == "" {
		title = hit.Session.Title
	}
	if title == "" {
		title = hit.Session.ID
	}
	count := hit.Count
	if m.hitsMode == "ask" {
		count = index + 1
	}
	in := tildePath(hit.Session.ProjectPath)
	row := fmt.Sprintf("  %6d  %-*s %-7s %s",
		count, m.titleWidth(), truncate(title, m.titleWidth()),
		hit.Session.Target,
		styleUnless(active, styleDim, truncate(in, max(10, m.width-m.titleWidth()-22))))
	if active {
		return styleCursor.Render(fmt.Sprintf("%-*s", m.width, row))
	}
	return row
}

// activityBody lists the audit log — every action the tool has taken on
// the user's behalf — newest first.
func (m model) activityBody(b *strings.Builder) {
	if len(m.activity) == 0 {
		b.WriteString(styleDim.Render("  no recorded actions yet — restores and repairs will appear here") + "\n")
		return
	}

	b.WriteString(styleDim.Render(fmt.Sprintf("  %-17s %-9s %-7s %-37s %s",
		"WHEN", "ACTION", "AGENT", "SESSION", "DETAIL")) + "\n")
	end := min(len(m.activity), m.actOffset+m.pageSize())
	for _, entry := range m.activity[m.actOffset:end] {
		style := styleDim
		if entry.Action == "restore" {
			style = styleRecover
		}
		detail := tildePath(entry.To)
		if entry.Overwrote {
			detail += " · overwrote, safety copy kept"
		}
		b.WriteString(fmt.Sprintf("  %-17s %s %-7s %-37s %s\n",
			entry.Time.Format("2006-01-02 15:04"),
			style.Render(fmt.Sprintf("%-9s", entry.Action)),
			entry.Target,
			truncate(entry.SessionID, 37),
			styleDim.Render(truncate(detail, max(10, m.width-78)))))
	}
	b.WriteString(styleDim.Render(fmt.Sprintf("\n  %d action(s) recorded · %s",
		len(m.activity), tildePath(filepath.Join(m.cfg.BackupRoot, "audit.log")))) + "\n")
}

// keysBody is the full key reference; the footer stays minimal because
// everything beyond basic navigation lives here.
func (m model) keysBody(b *strings.Builder) {
	section := func(title string) {
		b.WriteString("\n" + styleDim.Render("  "+title) + "\n")
	}
	key := func(keys string, what string) {
		b.WriteString("  " + styleBold.Render(fmt.Sprintf("%-14s", keys)) + what + "\n")
	}
	section("NAVIGATE")
	key("↑/↓ · j/k", "move (mouse wheel scrolls)")
	key("enter", "open folder / session details")
	key("← · esc", "back — leave pane, then go up a folder")
	key("tab", "switch between folders and sessions")
	key("ctrl+a", "all sessions beneath this folder")
	key("ctrl+e", "expand / collapse the whole folder tree in place")
	key("/", "filter the current pane — folder tree or session list")
	key("ctrl+s", "search transcripts for the query, ranked by hit count")
	key("ctrl+g", "ai find page: describe a session, enter asks a local model")
	key("g · G", "jump to top / bottom")
	section("SESSIONS")
	key("i", "session details (overview · usage · transcript)")
	key("r", "restore a ✝ recover session, with confirmation")
	key("t", "transplant: move/copy a session or project to another dir")
	key("x", "show or hide ✕ lost sessions")
	section("GLOBAL")
	key("m", "this menu · s and a jump straight to stats / activity")
	key("ctrl+r", "rescan now (automatic every 5s)")
	key("q · ctrl+c", "quit")
}

// humanTokens renders token counts compactly (1.2k, 3.4M, 1.1B).
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// pinBottomBare pads the content so a separator and the key help hug the
// window bottom — no path or legend, for views that are not file lists.
func (m model) pinBottomBare(content string, help string) string {
	footer := []string{
		styleFooter.Render(strings.Repeat("─", max(m.width, 10))),
		styleFooter.Render(" " + help),
	}
	used := strings.Count(content, "\n")
	if pad := m.height - used - len(footer); pad > 0 {
		content += strings.Repeat("\n", pad)
	}
	return content + strings.Join(footer, "\n")
}

// pinBottom pads the content so the footer block hugs the window bottom:
// path (left) with the activity legend (right), a separator, then key help
// on the very last line.
func (m model) pinBottom(content string, help string) string {
	legend := styleOK.Render("●") + styleDim.Render(" <1h  ") +
		styleStale.Render("●") + styleDim.Render(" today  ") + styleDim.Render("○ older  ") +
		styleActive.Render("▶") + styleDim.Render(" open now ")
	pathLine := styleDim.Render(" " + truncate(m.root, m.width-lipgloss.Width(legend)-3))
	if pad := m.width - lipgloss.Width(pathLine) - lipgloss.Width(legend); pad > 0 {
		pathLine += strings.Repeat(" ", pad)
	}
	helpLine := styleFooter.Render(" " + help)
	if commit := shortCommit(); commit != "" {
		revision := styleDim.Render("version: " + commit + " ")
		if pad := m.width - lipgloss.Width(helpLine) - lipgloss.Width(revision); pad > 0 {
			helpLine += strings.Repeat(" ", pad)
		}
		helpLine += revision
	}
	footer := []string{
		pathLine + legend,
		styleFooter.Render(strings.Repeat("─", max(m.width, 10))),
		helpLine,
	}
	used := strings.Count(content, "\n")
	if pad := m.height - used - len(footer); pad > 0 {
		content += strings.Repeat("\n", pad)
	}
	return content + strings.Join(footer, "\n")
}

// tabBar renders the pane switcher; the active pane is highlighted. The all
// pane has no segment — the "sessions beneath" count highlights instead.
func (m model) tabBar() string {
	segment := func(label string, active bool) string {
		if active {
			return styleCursor.Render(label)
		}
		return styleDim.Render(label)
	}
	return segment(fmt.Sprintf(" Folders %d ", len(m.folders)), !m.showSessions && !m.showAll) +
		segment(fmt.Sprintf(" Sessions %d ", m.sessionCount()), m.showSessions && !m.showAll)
}

// allSessionRow is a session row whose trailing column shows where the
// session lives relative to the current root.
func (m model) allSessionRow(session Session, active bool) string {
	state, style := sessionState(session.State)
	gold := session.LiveStatus != "" && !active
	plain := active || gold
	live := " "
	if session.LiveStatus != "" {
		live = styleUnless(plain, styleActive, "▶")
	}
	row := fmt.Sprintf("%s%s  %s %-7s %-12s %8s  %-8s %s",
		live, styleUnless(plain, style, state), sessionTitle(session, m.titleWidth(), plain), session.Target,
		truncate(displayModel(session.LastModel), 12), formatBytes(session.Size), ago(session.Modified),
		styleUnless(plain, styleDim, truncate(m.relOfRoot(session), 30)))
	if active {
		return styleCursor.Render(fmt.Sprintf("%-*s", m.width, row))
	}
	if gold {
		return styleActive.Render(row)
	}
	return row
}

// detailView is the tabbed inspector for one session (i to open/close).
func (m model) detailView() string {
	session := *m.detail

	name := session.CustomName
	if name == "" {
		name = truncate(session.Title, 60)
	}
	if name == "" {
		name = session.ID
	}

	tab := func(label string, index int) string {
		if m.detailTab == index {
			return styleCursor.Render(" " + label + " ")
		}
		return styleDim.Render(" " + label + " ")
	}
	left := styleHeader.Render("▸ " + name)
	right := tab("Overview", 0) + tab("Usage", 1) + tab("Transcript", 2)
	if pad := m.width - lipgloss.Width(left) - lipgloss.Width(right); pad > 0 {
		left += strings.Repeat(" ", pad)
	}

	var b strings.Builder
	b.WriteString(left + right + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n")

	switch m.detailTab {
	case 1:
		m.usageTab(&b)
		return m.pinBottomBare(b.String(), "tab switch · i/esc close · ctrl+c quit")
	case 2:
		m.renderTail(&b, max(4, m.height-6))
		return m.pinBottomBare(b.String(), "↑/↓/wheel scroll · tab switch · i/esc close")
	}
	m.overviewTab(&b, session)
	return m.pinBottomBare(b.String(), "tab switch · i/esc close · ctrl+c quit")
}

func (m model) overviewTab(b *strings.Builder, session Session) {
	data := m.detailData
	width := max(min(m.width-2, 110), 40)
	inner := width - 4

	state, style := sessionState(session.State)
	liveNote := ""
	if session.LiveStatus != "" {
		liveNote = styleActive.Render("  ▶ open now (" + session.LiveStatus + ")")
	}
	kv := func(key string, value string) {
		b.WriteString(styleDim.Render(fmt.Sprintf(" %-10s", key)) + value + "\n")
	}
	kv("state", strings.TrimSpace(style.Render(state))+liveNote)
	kv("agent", session.Target)
	kv("session", session.ID)
	project := session.ProjectPath
	if project == "" {
		project = m.root
	}
	kv("project", tildePath(project))
	stamp := formatBytes(session.Size)
	if !data.Created.IsZero() {
		stamp += styleDim.Render("  ·  started ") + ago(data.Created)
	}
	stamp += styleDim.Render("  ·  modified ") + ago(session.Modified)
	if session.BackupModified.IsZero() {
		stamp += styleDim.Render("  ·  backup ") + styleUnbacked.Render("never")
	} else {
		stamp += styleDim.Render("  ·  backup ") + ago(session.BackupModified)
	}
	kv("size", stamp)
	if data.Messages > 0 {
		messages := fmt.Sprintf("%d", data.Messages)
		if len(data.Models) > 0 {
			messages += styleDim.Render("  ·  " + strings.Join(data.Models, ", "))
		}
		kv("messages", messages)
	}
	if data.Compactions > 0 {
		note := fmt.Sprintf("%d", data.Compactions)
		if data.LastCompact != "" {
			extra := data.LastCompact
			if data.LastCompactPre > 0 {
				extra += " @ " + humanTokens(data.LastCompactPre) + " tokens"
			}
			note += styleDim.Render("  ·  last " + extra)
		}
		kv("compactions", note)
	}
	if !session.RestoredAt.IsZero() {
		kv("restored", styleRecover.Render("✚ ")+session.RestoredAt.Format("2006-01-02 15:04")+
			styleDim.Render("  ·  from backup ("+ago(session.RestoredAt)+")"))
	}
	kvPath := func(key string, path string) {
		if path == "" {
			return
		}
		lines := chunk(tildePath(path), m.width-13)
		kv(key, lines[0])
		for _, line := range lines[1:] {
			b.WriteString(strings.Repeat(" ", 11) + line + "\n")
		}
	}
	kvPath("source", session.SourcePath)
	kvPath("backup", session.BackupPath)

	first := data.FirstPrompt
	if first == "" {
		first = session.Title
	}
	if first != "" {
		// Same highlighted bar the transcript tab uses for user input.
		var lines []string
		for i, line := range wrapPreserve(first, inner-4, 6) {
			prefix := "  "
			if i == 0 {
				prefix = "❯ "
			}
			lines = append(lines, styleUserMsg.Render(fmt.Sprintf(" %s%-*s ", prefix, inner-4, line)))
		}
		b.WriteString("\n" + styleDim.Render(" initial prompt") + "\n")
		b.WriteString(detailBox(width).Render(strings.Join(lines, "\n")) + "\n")
	}
}

// usageTab mirrors the agent's own usage panel: per-model tokens and an
// offline cost estimate from the local pricing table.
func (m model) usageTab(b *strings.Builder) {
	data := m.detailData
	if data.Tokens.Zero() {
		b.WriteString(styleDim.Render("  no usage data in this transcript") + "\n")
		return
	}

	b.WriteString("\n" + styleDim.Render(fmt.Sprintf("  %-28s %10s %10s %12s %12s %10s",
		"MODEL", "INPUT", "OUTPUT", "CACHE READ", "CACHE WRITE", "COST")) + "\n")

	var totalCost float64
	costKnown := true
	for _, model := range data.Models {
		usage := data.PerModel[model]
		costText := styleDim.Render("         —")
		if cost, ok := costUSD(model, usage); ok {
			costText = fmt.Sprintf("%10s", fmt.Sprintf("$%.2f", cost))
			totalCost += cost
		} else {
			costKnown = false
		}
		b.WriteString(fmt.Sprintf("  %-28s %10s %10s %12s %12s %s\n",
			truncate(model, 28), humanTokens(usage.Input), humanTokens(usage.Output),
			humanTokens(usage.CacheRead), humanTokens(usage.CacheWrite), costText))
	}

	total := data.Tokens
	totalText := fmt.Sprintf("$%.2f", totalCost)
	if !costKnown {
		totalText += "+"
	}
	b.WriteString(styleHeader.Render(fmt.Sprintf("  %-28s %10s %10s %12s %12s %10s",
		"total", humanTokens(total.Input), humanTokens(total.Output),
		humanTokens(total.CacheRead), humanTokens(total.CacheWrite), totalText)) + "\n")

	b.WriteString("\n" + styleDim.Render("  cost estimated locally from published per-token prices"+
		" (cache read 0.1×, cache write 1.25× input rate); no external calls") + "\n")
	if data.Messages > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  %d messages in transcript", data.Messages)) + "\n")
	}
}

// confirmRestoreBox previews exactly what a pending restore will write, so
// the y/enter lands on a visible plan rather than an implied one.
func (m model) confirmRestoreBox(session Session) string {
	dest, _, err := restoreDest(m.cfg, session)
	if err != nil {
		dest = "unresolvable: " + err.Error()
	}
	title := session.CustomName
	if title == "" {
		title = session.Title
	}
	if title == "" {
		title = session.ID
	}
	width := min(m.width, 100)
	inner := width - 12
	body := styleRecover.Render("✝ ") + styleBold.Render("restore ") + truncate(title, inner) + "\n" +
		styleDim.Render("from  ") + truncate(session.BackupPath, inner) + "\n" +
		styleDim.Render("to    ") + truncate(dest, inner)
	return detailBox(width).Render(body)
}

// detailBox is the rounded frame used by the inspector's sections.
func detailBox(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "#BBBBBB", Dark: "#555555"}).
		Padding(0, 1).
		Width(width - 2)
}

// wrapPreserve word-wraps text while keeping its newline structure.
func wrapPreserve(text string, width int, maxLines int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
		} else {
			out = append(out, wrapText(line, width, 1000)...)
		}
		if len(out) > maxLines {
			return append(out[:maxLines], styleDim.Render("… (scroll the tail for the full transcript)"))
		}
	}
	return out
}

// renderTail writes a scrollable window over the rendered transcript tail.
func (m model) renderTail(b *strings.Builder, page int) {
	if len(m.tailLines) == 0 {
		b.WriteString(styleDim.Render("  no messages extracted") + "\n")
		return
	}
	start := max(0, len(m.tailLines)-page-m.tailOffset)
	end := min(len(m.tailLines), start+page)
	if start > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf(" ↑ %d earlier lines", start)) + "\n")
	}
	for _, line := range m.tailLines[start:end] {
		b.WriteString(line + "\n")
	}
}

func (m *model) buildTailLines() {
	m.tailWidth = m.width
	width := max(min(m.width-6, 110), 40)
	style := styles.DarkStyleConfig
	margin := uint(0)
	style.Document.Margin = &margin
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)

	var lines []string
	for _, msg := range m.detailData.Transcript {
		if msg.Role == "compact" {
			lines = append(lines, "", styleDim.Render("── conversation compacted ("+msg.Text+") ──"), "")
			continue
		}
		if msg.Role == "summary" {
			for i, line := range wrapPreserve(msg.Text, width-2, 12) {
				prefix := "  "
				if i == 0 {
					prefix = styleDim.Render("⧉ ")
				}
				lines = append(lines, prefix+styleDim.Render(line))
			}
			lines = append(lines, "")
			continue
		}
		if msg.Role == "result" {
			lines = append(lines, styleDim.Render("    ⎿ "+truncate(msg.Text, max(20, width-8))))
			continue
		}
		if msg.Role == "tool" {
			name, detail, hasDetail := strings.Cut(msg.Text, ": ")
			line := "  " + styleOK.Render("●") + " " + styleBold.Render(name)
			if hasDetail {
				line += styleDim.Render("(" + truncate(detail, width-len(name)-8) + ")")
			}
			lines = append(lines, line)
			continue
		}
		if msg.Role == "user" {
			// Full-width highlighted bar, like the agent's own prompt echo.
			for i, line := range wrapPreserve(msg.Text, width-4, 400) {
				prefix := "  "
				if i == 0 {
					prefix = "❯ "
				}
				lines = append(lines, styleUserMsg.Render(fmt.Sprintf(" %s%-*s ", prefix, width-4, line)))
			}
			lines = append(lines, "")
			continue
		}
		rendered := msg.Text
		if err == nil {
			if out, renderErr := renderer.Render(msg.Text); renderErr == nil {
				rendered = out
			}
		}
		first := true
		for _, line := range strings.Split(strings.Trim(rendered, "\n"), "\n") {
			if first && strings.TrimSpace(line) != "" {
				lines = append(lines, "● "+line)
				first = false
			} else {
				lines = append(lines, "  "+line)
			}
		}
		lines = append(lines, "")
	}
	m.tailLines = lines
	m.tailOffset = 0
}

// tildePath abbreviates the home directory for display.
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home); ok {
		return "~" + rest
	}
	return path
}

// chunk hard-wraps an unbroken string (like a path) into width-sized pieces.
func chunk(s string, width int) []string {
	if width < 10 {
		width = 10
	}
	var out []string
	for len(s) > width {
		out = append(out, s[:width])
		s = s[width:]
	}
	return append(out, s)
}

// wrapText breaks text into at most maxLines lines of the given width,
// ellipsizing the remainder.
func wrapText(text string, width int, maxLines int) []string {
	if width < 10 {
		width = 10
	}
	words := strings.Fields(text)
	var lines []string
	current := ""
	for _, word := range words {
		if current != "" && len(current)+1+len(word) > width {
			lines = append(lines, current)
			if len(lines) == maxLines {
				lines[maxLines-1] = truncate(lines[maxLines-1]+" …", width)
				return lines
			}
			current = word
			continue
		}
		if current == "" {
			current = word
		} else {
			current += " " + word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func (m model) relOfRoot(session Session) string {
	if rel, ok := strings.CutPrefix(session.ProjectPath, m.root+string(os.PathSeparator)); ok {
		return rel
	}
	return "."
}

// styleUnless returns text styled normally, or plain when the row is
// selected — nested colors would break the full-row cursor highlight.
func styleUnless(active bool, style lipgloss.Style, text string) string {
	if active {
		return text
	}
	return style.Render(text)
}

func (m model) folderRow(folder Folder, active bool) string {
	// A folder holding open sessions renders whole-row gold; the cursor
	// highlight still wins when selected.
	gold := folder.Open > 0 && !active
	plain := active || gold
	glyph, glyphStyle := activityGlyph(folder.Latest)
	if folder.Open > 0 {
		// Open now supersedes mtime age — an idle-but-open folder showing
		// the "older" dot would contradict its ▶ badge.
		glyph, glyphStyle = "▶", styleActive
	}
	indent := strings.Repeat("  ", folder.Depth)
	name := indent + truncate(folder.Name, m.nameWidth()-1-len(indent)) + "/"
	if folder.Pseudo {
		name = indent + truncate(folder.Name, m.nameWidth()-len(indent))
	}

	// Severity badges keep their own colors even inside gold rows — a
	// lost or unbacked count must never read as "live". Only the cursor
	// highlight flattens them.
	health := ""
	if folder.Open > 0 {
		health += styleUnless(active, styleActive, fmt.Sprintf(" ▶%d", folder.Open))
	}
	if folder.Stale > 0 {
		health += styleUnless(active, styleStale, fmt.Sprintf(" ~%d", folder.Stale))
	}
	if folder.Unbacked > 0 {
		health += styleUnless(active, styleUnbacked, fmt.Sprintf(" !%d", folder.Unbacked))
	}
	if folder.RecoverOnly > 0 {
		health += styleUnless(active, styleRecover, fmt.Sprintf(" ✝%d", folder.RecoverOnly))
	}
	if folder.Lost > 0 {
		health += styleUnless(active, styleDim, fmt.Sprintf(" ✕%d", folder.Lost))
	}
	if health == "" {
		health = styleUnless(active, styleOK, " ok")
	}

	row := fmt.Sprintf(" %s %-*s  %8d  %8s  %-9s%s",
		styleUnless(plain, glyphStyle, glyph), m.nameWidth(), name, folder.Sessions, formatBytes(folder.SizeBytes), ago(folder.Latest), health)
	if active {
		return styleCursor.Render(fmt.Sprintf("%-*s", m.width, row))
	}
	if gold {
		return styleActive.Render(row)
	}
	return row
}

func (m model) sessionRow(session Session, active bool) string {
	state, style := sessionState(session.State)
	// Open sessions render the entire row in gold so live work stands
	// out; the cursor highlight still takes precedence when selected.
	gold := session.LiveStatus != "" && !active
	plain := active || gold
	live := " "
	if session.LiveStatus != "" {
		live = styleUnless(plain, styleActive, "▶")
	}
	size := formatBytes(session.Size)
	if session.State == "LOST" {
		size = fmt.Sprintf("%dp", session.Prompts)
	}
	row := fmt.Sprintf("%s%s  %s %-7s %-12s %8s  %s",
		live, styleUnless(plain, style, state), sessionTitle(session, m.titleWidth(), plain), session.Target,
		truncate(displayModel(session.LastModel), 12), size, ago(session.Modified))
	if active {
		return styleCursor.Render(fmt.Sprintf("%-*s", m.width, row))
	}
	if gold {
		return styleActive.Render(row)
	}
	return row
}

// sessionTitle renders the display title in a fixed-width cell: custom names
// bright, first-prompt fallback dim, plain when the row is selected.
func sessionTitle(session Session, width int, active bool) string {
	switch {
	case session.CustomName != "":
		return fmt.Sprintf("%-*s", width, truncate(session.CustomName, width))
	case session.Title != "":
		return styleUnless(active, styleDim, fmt.Sprintf("%-*s", width, truncate(session.Title, width)))
	default:
		return styleUnless(active, styleDim, fmt.Sprintf("%-*s", width, "(not set)"))
	}
}

func sessionState(state string) (string, lipgloss.Style) {
	switch state {
	case "OK":
		return "● ok       ", styleOK
	case "ACTIVE":
		return "● active   ", styleActive
	case "OPEN":
		return "● open     ", styleActive
	case "STALE_BACKUP":
		return "~ stale    ", styleStale
	case "MISSING_BACKUP":
		return "! unbacked ", styleUnbacked
	case "RESTORED":
		return "✚ restored ", styleRecover
	case "LOST":
		return "✕ lost     ", styleDim
	default:
		return "✝ recover  ", styleRecover
	}
}

func activityGlyph(t time.Time) (string, lipgloss.Style) {
	switch since := time.Since(t); {
	case since < time.Hour:
		return "●", styleOK
	case since < 24*time.Hour:
		return "●", styleStale
	default:
		return "○", styleDim
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// ago renders a compact relative age; the columns' meaning makes the "ago"
// implicit, and it never falls back to absolute dates.
func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	since := time.Since(t)
	switch {
	case since < time.Minute:
		return "now"
	case since < time.Hour:
		return fmt.Sprintf("%dm", int(since.Minutes()))
	case since < 24*time.Hour:
		return fmt.Sprintf("%dh", int(since.Hours()))
	case since < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(since.Hours()/24))
	case since < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(since.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy", int(since.Hours()/24/365))
	}
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}
