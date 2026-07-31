package browse

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rexovas/session-protect/internal/config"
)

const (
	viewProjects = iota
	viewSessions
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
	view     int
	selected *Project

	cursor  int
	offset  int
	sCursor int
	sOffset int

	width  int
	height int
}

type rescanMsg []*Project

func newModel(cfg config.Config) model {
	return model{cfg: cfg, projects: Scan(cfg), width: 100, height: 32}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case rescanMsg:
		m.projects = msg
		if m.cursor >= len(m.projects) {
			m.cursor = max(0, len(m.projects)-1)
		}
		if m.selected != nil {
			m.selected = findProject(m.projects, m.selected.Path)
			if m.selected == nil {
				m.view = viewProjects
			}
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if m.view == viewSessions && msg.String() == "q" {
			m.view = viewProjects
			return m, nil
		}
		return m, tea.Quit
	case "r":
		cfg := m.cfg
		return m, func() tea.Msg { return rescanMsg(Scan(cfg)) }
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup":
		m.moveCursor(-m.pageSize())
	case "pgdown":
		m.moveCursor(m.pageSize())
	case "g":
		m.setCursor(0)
	case "G":
		m.setCursor(1 << 30)
	case "enter", "l", "right":
		if m.view == viewProjects && m.cursor < len(m.projects) {
			m.selected = m.projects[m.cursor]
			LoadCustomNames(m.selected)
			m.view = viewSessions
			m.sCursor, m.sOffset = 0, 0
		}
	case "esc", "backspace", "h", "left":
		if m.view == viewSessions {
			m.view = viewProjects
		}
	}
	return m, nil
}

func (m *model) moveCursor(delta int) { m.setCursor(m.current() + delta) }

func (m *model) setCursor(position int) {
	limit := len(m.projects)
	if m.view == viewSessions && m.selected != nil {
		limit = len(m.selected.Sessions)
	}
	if position < 0 {
		position = 0
	}
	if position > limit-1 {
		position = max(0, limit-1)
	}
	page := m.pageSize()
	if m.view == viewProjects {
		m.cursor = position
		m.offset = clampOffset(m.offset, position, page)
	} else {
		m.sCursor = position
		m.sOffset = clampOffset(m.sOffset, position, page)
	}
}

func (m model) current() int {
	if m.view == viewSessions {
		return m.sCursor
	}
	return m.cursor
}

func (m model) pageSize() int { return max(4, m.height-5) }

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
	if m.view == viewSessions && m.selected != nil {
		return m.sessionsView()
	}
	return m.projectsView()
}

func (m model) projectsView() string {
	var b strings.Builder
	title := fmt.Sprintf("Session Explorer — Projects (%d)", len(m.projects))
	legend := styleDim.Render("  ") + styleOK.Render("●") + styleDim.Render(" <1h  ") +
		styleStale.Render("●") + styleDim.Render(" today  ") + styleDim.Render("○ older  ") +
		styleOK.Render("▶") + styleDim.Render(" open now")
	b.WriteString(styleHeader.Render(title) + legend + "\n")
	b.WriteString(styleDim.Render(strings.Repeat("─", min(m.width, 120))) + "\n")

	b.WriteString(styleDim.Render(fmt.Sprintf("   %-38s  %6s %6s  %8s  %-16s%s",
		"PROJECT", "CLAUDE", "CODEX", "SIZE", "LAST ACTIVITY", " BACKUP")) + "\n")

	page := m.pageSize() - 1
	end := min(len(m.projects), m.offset+page)
	for i := m.offset; i < end; i++ {
		b.WriteString(m.projectRow(m.projects[i], i == m.cursor) + "\n")
	}
	if len(m.projects) == 0 {
		b.WriteString(styleDim.Render("  no sessions found") + "\n")
	}

	b.WriteString(m.footer("↑/↓ move · enter open · r refresh · q quit"))
	return b.String()
}

func (m model) projectRow(project *Project, active bool) string {
	glyph, glyphStyle := activityGlyph(project.Latest)
	name := truncate(displayName(project.Path), 38)
	counts := countCell(project.ClaudeCount) + " " + countCell(project.CodexCount)

	health := ""
	if project.Open > 0 {
		health += styleOK.Render(fmt.Sprintf(" ▶%d", project.Open))
	}
	if project.Stale > 0 {
		health += styleStale.Render(fmt.Sprintf(" ~%d", project.Stale))
	}
	if project.Unbacked > 0 {
		health += styleUnbacked.Render(fmt.Sprintf(" !%d", project.Unbacked))
	}
	if project.RecoverOnly > 0 {
		health += styleRecover.Render(fmt.Sprintf(" ✝%d", project.RecoverOnly))
	}
	if health == "" {
		health = styleOK.Render(" ok")
	}

	row := fmt.Sprintf(" %s %-38s  %s  %8s  %-16s%s",
		glyphStyle.Render(glyph), name, counts, formatBytes(project.SizeBytes), ago(project.Latest), health)
	if active {
		return styleCursor.Render(row)
	}
	return row
}

func (m model) sessionsView() string {
	var b strings.Builder
	project := m.selected
	title := fmt.Sprintf("Session Explorer — %s (%d sessions)", displayName(project.Path), len(project.Sessions))
	b.WriteString(styleHeader.Render(title) + "\n")
	b.WriteString(styleDim.Render(truncate(project.Path, m.width)) + "\n")

	b.WriteString(styleDim.Render(fmt.Sprintf(" %-11s  %-36s %-7s %-10s %8s  %-12s %s",
		"STATE", "TITLE", "AGENT", "SESSION", "SIZE", "MODIFIED", "BACKED UP")) + "\n")

	page := m.pageSize() - 2
	end := min(len(project.Sessions), m.sOffset+page)
	for i := m.sOffset; i < end; i++ {
		b.WriteString(m.sessionRow(project.Sessions[i], i == m.sCursor) + "\n")
	}

	b.WriteString(m.footer("↑/↓ move · esc back · r refresh · q back · ctrl+c quit"))
	return b.String()
}

func (m model) sessionRow(session Session, active bool) string {
	state, style := sessionState(session.State)
	backedUp := ago(session.BackupModified)
	if session.BackupModified.IsZero() {
		backedUp = "never"
	}
	// Custom names render bright; the first-prompt fallback renders dim so
	// the two are distinguishable at a glance.
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

func displayName(path string) string {
	if strings.HasPrefix(path, "/") {
		return filepath.Base(path)
	}
	return path
}

func shortID(id string) string { return truncate(id, 10) }

// countCell renders a session count in a fixed-width cell, dimming zeros so
// the eye lands on projects that actually have sessions for that agent.
func countCell(count int) string {
	cell := fmt.Sprintf("%6d", count)
	if count == 0 {
		return styleDim.Render(cell)
	}
	return cell
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

func findProject(projects []*Project, path string) *Project {
	for _, project := range projects {
		if project.Path == path {
			return project
		}
	}
	return nil
}
