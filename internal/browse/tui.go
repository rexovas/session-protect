package browse

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/rexovas/session-protect/internal/config"
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
	stats        *Stats
	showStats    bool
	fCursor      int
	fOffset      int
	sCursor      int
	sOffset      int
	aCursor      int
	aOffset      int

	scanning bool // a background rescan is in flight

	width  int
	height int
}

type rescanMsg []*Project

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
	m.folders = ChildrenOf(m.projects, m.root, m.start)
	m.here = ProjectAt(m.projects, m.root)
	m.visible = nil
	if m.here != nil {
		LoadCustomNames(m.here)
		m.visible = m.filterLost(m.here.Sessions)
	}
	if m.showAll {
		m.allSessions = m.filterLost(AllUnder(m.projects, m.root))
	}
	// Show the pane that has content when the other is empty.
	if len(m.folders) == 0 && m.sessionCount() > 0 {
		m.showSessions = true
	}
	if m.sessionCount() == 0 && len(m.folders) > 0 {
		m.showSessions = false
	}
	m.setCursor(m.currentCursor())
}

func (m model) sessionCount() int {
	return len(m.visible)
}

// filterLost hides history-only sessions unless the toggle is on.
func (m model) filterLost(sessions []Session) []Session {
	if m.showLost {
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
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.detail != nil {
				m.scrollTail(3)
			} else if !m.showStats {
				m.setCursor(m.currentCursor() - 3)
			}
		case tea.MouseButtonWheelDown:
			if m.detail != nil {
				m.scrollTail(-3)
			} else if !m.showStats {
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
	if m.showStats {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "s", "esc", "q", "backspace", "h", "left", "enter":
			m.showStats = false
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "s":
		m.stats = LoadStats()
		m.showStats = true
	case "x":
		m.showLost = !m.showLost
		m.rebuild()
	case "i":
		if session := m.selectedSession(); session != nil {
			m.detail = session
			if session.State == "LOST" {
				m.detailData = LoadLostDetail(session.ID)
			} else {
				m.detailData = LoadDetail(*session)
			}
			m.buildTailLines()
		}
	case "r":
		m.scanning = true
		cfg := m.cfg
		return m, func() tea.Msg { return rescanMsg(ScanNamed(cfg)) }
	case "tab":
		if m.showAll {
			m.showAll = false
			m.showSessions = len(m.folders) == 0 && m.sessionCount() > 0
		} else if len(m.folders) > 0 && m.sessionCount() > 0 {
			m.showSessions = !m.showSessions
		}
		m.setCursor(m.currentCursor())
	case "ctrl+a":
		if len(m.folders) == 0 {
			break // sessions here ARE all nested sessions; nothing to add
		}
		m.showAll = !m.showAll
		if m.showAll {
			m.allSessions = m.filterLost(AllUnder(m.projects, m.root))
			m.aCursor, m.aOffset = 0, 0
		}
		m.setCursor(m.currentCursor())
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
		if !m.showSessions && !m.showAll && m.fCursor < len(m.folders) {
			m.trail = append(m.trail, m.root)
			m.root = m.folders[m.fCursor].Path
			m.resetPanes()
			m.rebuild()
		}
	case "esc", "backspace", "h", "left":
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
	if m.showStats {
		return m.statsView()
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
		b.WriteString(styleDim.Render("  nothing here") + "\n")
	}

	help := "↑/↓ move · enter open · ← up · tab sessions · ctrl+a all · s stats · x lost · q quit"
	if m.showSessions || m.showAll {
		help = "↑/↓ move · i info · ← folders · tab folders · ctrl+a all · x lost · q quit"
	}
	return m.pinBottom(b.String(), help)
}

// statsView shows global usage statistics from the agent's own stats cache.
func (m model) statsView() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("Session Explorer ▸ usage stats") + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n")

	if m.stats == nil {
		b.WriteString(styleDim.Render("  no stats available (claude stats cache not found)") + "\n")
		return m.pinBottom(b.String(), "s/esc close · ctrl+c quit")
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

	return m.pinBottom(b.String(), "s/esc close · ctrl+c quit")
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
		revision := styleDim.Render("revision: " + commit + " ")
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
		return m.pinBottom(b.String(), "tab switch · i/esc close · ctrl+c quit")
	case 2:
		m.renderTail(&b, max(4, m.height-6))
		return m.pinBottom(b.String(), "↑/↓/wheel scroll · tab switch · i/esc close")
	}
	m.overviewTab(&b, session)
	return m.pinBottom(b.String(), "tab switch · i/esc close · ctrl+c quit")
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
		b.WriteString("\n" + styleDim.Render(" initial prompt") + "\n")
		b.WriteString(detailBox(width).Render(strings.Join(wrapPreserve(first, inner, 6), "\n")) + "\n")
	}

	if data.LastPrompt != "" || data.LastResponse != "" {
		var exchange []string
		if data.LastPrompt != "" {
			prompt := wrapPreserve(data.LastPrompt, inner-2, 4)
			exchange = append(exchange, styleActive.Render("❯ ")+prompt[0])
			for _, line := range prompt[1:] {
				exchange = append(exchange, "  "+line)
			}
		}
		if data.LastResponse != "" {
			if len(exchange) > 0 {
				exchange = append(exchange, "")
			}
			responseCap := max(4, m.height-len(exchange)-24)
			exchange = append(exchange, wrapPreserve(data.LastResponse, inner, responseCap)...)
		}
		b.WriteString("\n" + styleDim.Render(" last exchange") + "\n")
		b.WriteString(detailBox(width).Render(strings.Join(exchange, "\n")) + "\n")
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
	name := truncate(folder.Name, m.nameWidth()-1) + "/"
	if folder.Pseudo {
		name = truncate(folder.Name, m.nameWidth())
	}

	health := ""
	if folder.Open > 0 {
		health += styleUnless(plain, styleActive, fmt.Sprintf(" ▶%d", folder.Open))
	}
	if folder.Stale > 0 {
		health += styleUnless(plain, styleStale, fmt.Sprintf(" ~%d", folder.Stale))
	}
	if folder.Unbacked > 0 {
		health += styleUnless(plain, styleUnbacked, fmt.Sprintf(" !%d", folder.Unbacked))
	}
	if folder.RecoverOnly > 0 {
		health += styleUnless(plain, styleRecover, fmt.Sprintf(" ✝%d", folder.RecoverOnly))
	}
	if folder.Lost > 0 {
		health += styleUnless(plain, styleDim, fmt.Sprintf(" ✕%d", folder.Lost))
	}
	if health == "" {
		health = styleUnless(plain, styleOK, " ok")
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
