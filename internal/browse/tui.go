package browse

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rexovas/session-protect/internal/config"
)

var (
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7D79F6"})
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#999999", Dark: "#666666"})
	styleCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#FFFFFF"}).Background(lipgloss.AdaptiveColor{Light: "#E8E8FF", Dark: "#333355"})
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0A8754", Dark: "#3DDC97"})
	styleStale    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#F5C518"})
	styleUnbacked = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C0392B", Dark: "#FF6B6B"})
	styleRecover  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8E44AD", Dark: "#C39BD3"})
	styleFooter   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#999999"})
)

type model struct {
	cfg      config.Config
	projects []*Project

	start string   // where the shell launched us
	root  string   // directory currently viewed
	trail []string // roots we descended through, for going back up

	folders []Folder
	here    *Project // project whose sessions live exactly at root

	// One pane is visible at a time; tab switches. Each keeps its own
	// cursor so toggling does not lose the user's place.
	showSessions bool
	fCursor      int
	fOffset      int
	sCursor      int
	sOffset      int

	width  int
	height int
}

type rescanMsg []*Project

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
	if m.here != nil {
		LoadCustomNames(m.here)
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
	if m.here == nil {
		return 0
	}
	return len(m.here.Sessions)
}

func (m model) itemCount() int {
	if m.showSessions {
		return m.sessionCount()
	}
	return len(m.folders)
}

func (m model) currentCursor() int {
	if m.showSessions {
		return m.sCursor
	}
	return m.fCursor
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case rescanMsg:
		m.projects = msg
		m.rebuild()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		cfg := m.cfg
		return m, func() tea.Msg { return rescanMsg(Scan(cfg)) }
	case "tab":
		if len(m.folders) > 0 && m.sessionCount() > 0 {
			m.showSessions = !m.showSessions
			m.setCursor(m.currentCursor())
		}
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
		if !m.showSessions && m.fCursor < len(m.folders) {
			m.trail = append(m.trail, m.root)
			m.root = m.folders[m.fCursor].Path
			m.fCursor, m.fOffset, m.sCursor, m.sOffset = 0, 0, 0, 0
			m.showSessions = false
			m.rebuild()
		}
	case "esc", "backspace", "h", "left":
		m.goUp()
	}
	return m, nil
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
	m.fCursor, m.fOffset, m.sCursor, m.sOffset = 0, 0, 0, 0
	m.showSessions = false
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
	if m.showSessions {
		m.sCursor = position
		m.sOffset = clampOffset(m.sOffset, position, m.pageSize())
	} else {
		m.fCursor = position
		m.fOffset = clampOffset(m.fOffset, position, m.pageSize())
	}
}

func (m model) pageSize() int { return max(4, m.height-6) }

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
	var b strings.Builder

	title := "Session Explorer"
	legend := styleDim.Render("  ") + styleOK.Render("●") + styleDim.Render(" <1h  ") +
		styleStale.Render("●") + styleDim.Render(" today  ") + styleDim.Render("○ older  ") +
		styleOK.Render("▶") + styleDim.Render(" open now")
	b.WriteString(styleHeader.Render(title) + legend + "\n")

	total := 0
	for _, folder := range m.folders {
		total += folder.Sessions
	}
	total += m.sessionCount()
	b.WriteString(styleDim.Render(fmt.Sprintf("%s — %d sessions beneath", truncate(m.root, m.width-24), total)) + "\n")
	b.WriteString(m.tabBar() + "\n")

	cursor := m.currentCursor()
	if m.showSessions {
		b.WriteString(styleDim.Render(fmt.Sprintf("  %-11s  %-36s %-7s %-10s %8s  %-12s %s",
			"STATE", "TITLE", "AGENT", "SESSION", "SIZE", "MODIFIED", "BACKED UP")) + "\n")
		end := min(m.sessionCount(), m.sOffset+m.pageSize())
		for i := m.sOffset; i < end; i++ {
			b.WriteString(m.sessionRow(m.here.Sessions[i], i == cursor) + "\n")
		}
	} else {
		b.WriteString(styleDim.Render(fmt.Sprintf("   %-38s  %8s  %8s  %-16s%s",
			"FOLDER", "SESSIONS", "SIZE", "LAST ACTIVITY", " BACKUP")) + "\n")
		end := min(len(m.folders), m.fOffset+m.pageSize())
		for i := m.fOffset; i < end; i++ {
			b.WriteString(m.folderRow(m.folders[i], i == cursor) + "\n")
		}
	}
	if m.itemCount() == 0 {
		b.WriteString(styleDim.Render("  nothing here") + "\n")
	}

	help := "↑/↓ move · enter open · ← up · tab sessions · r refresh · q quit"
	if m.showSessions {
		help = "↑/↓ move · ← up · tab folders · r refresh · q quit"
	}
	b.WriteString(m.footer(help))
	return b.String()
}

// tabBar renders the pane switcher; the active pane is highlighted.
func (m model) tabBar() string {
	folders := fmt.Sprintf(" Folders %d ", len(m.folders))
	sessions := fmt.Sprintf(" Sessions %d ", m.sessionCount())
	if m.showSessions {
		return styleDim.Render(folders) + styleCursor.Render(sessions)
	}
	return styleCursor.Render(folders) + styleDim.Render(sessions)
}

func (m model) folderRow(folder Folder, active bool) string {
	glyph, glyphStyle := activityGlyph(folder.Latest)
	name := truncate(folder.Name, 37) + "/"
	if folder.Pseudo {
		name = truncate(folder.Name, 38)
	}

	health := ""
	if folder.Open > 0 {
		health += styleOK.Render(fmt.Sprintf(" ▶%d", folder.Open))
	}
	if folder.Stale > 0 {
		health += styleStale.Render(fmt.Sprintf(" ~%d", folder.Stale))
	}
	if folder.Unbacked > 0 {
		health += styleUnbacked.Render(fmt.Sprintf(" !%d", folder.Unbacked))
	}
	if folder.RecoverOnly > 0 {
		health += styleRecover.Render(fmt.Sprintf(" ✝%d", folder.RecoverOnly))
	}
	if health == "" {
		health = styleOK.Render(" ok")
	}

	row := fmt.Sprintf(" %s %-38s  %8d  %8s  %-16s%s",
		glyphStyle.Render(glyph), name, folder.Sessions, formatBytes(folder.SizeBytes), ago(folder.Latest), health)
	if active {
		return styleCursor.Render(row)
	}
	return row
}

func (m model) sessionRow(session Session, active bool) string {
	state, style := sessionState(session.State)
	backedUp := ago(session.BackupModified)
	if session.BackupModified.IsZero() {
		backedUp = "never"
	}
	var title string
	switch {
	case session.CustomName != "":
		title = fmt.Sprintf("%-36s", truncate(session.CustomName, 36))
	case session.Title != "":
		title = styleDim.Render(fmt.Sprintf("%-36s", truncate(session.Title, 36)))
	default:
		title = styleDim.Render(fmt.Sprintf("%-36s", "(not set)"))
	}
	live := " "
	if session.LiveStatus != "" {
		live = styleOK.Render("▶")
	}
	row := fmt.Sprintf("%s%s  %s %-7s %-10s %8s  %-12s %s",
		live, style.Render(state), title, session.Target, shortID(session.ID),
		formatBytes(session.Size), ago(session.Modified), styleDim.Render(backedUp))
	if active {
		return styleCursor.Render(row)
	}
	return row
}

func (m model) footer(help string) string {
	return styleFooter.Render(strings.Repeat("─", min(m.width, 120))) + "\n" + styleFooter.Render(" "+help)
}

func sessionState(state string) (string, lipgloss.Style) {
	switch state {
	case "OK":
		return "● ok       ", styleOK
	case "STALE_BACKUP":
		return "~ stale    ", styleStale
	case "MISSING_BACKUP":
		return "! unbacked ", styleUnbacked
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

func shortID(id string) string { return truncate(id, 10) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	since := time.Since(t)
	switch {
	case since < time.Minute:
		return "just now"
	case since < time.Hour:
		return fmt.Sprintf("%dm ago", int(since.Minutes()))
	case since < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(since.Hours()))
	case since < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(since.Hours()/24))
	default:
		return t.Format("2006-01-02")
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
