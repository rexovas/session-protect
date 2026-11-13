package browse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

// Claude homes in the explorer: sessions from every home share one tree.
// With more than one home, rows gain an ACCOUNT column and the f filter an
// ACCOUNT section, whose selection can be saved as the launch default.

// homeOf is a session's account key: its claude home label, or "" for
// codex, which has a single home and so no account.
func homeOf(session Session) string {
	if session.Target != "claude" {
		return ""
	}
	if session.Home == "" {
		return targets.DefaultHomeLabel
	}
	return session.Home
}

// multiHome reports whether accounts are worth showing at all.
func (m model) multiHome() bool { return len(m.homes) > 1 }

// accountWidth fits the longest home label.
func (m model) accountWidth() int {
	width := len("ACCOUNT")
	for _, home := range m.homes {
		width = max(width, len(home.Label))
	}
	return width
}

// accountHeader and accountCell are the ACCOUNT column, empty with one home.
func (m model) accountHeader() string {
	if !m.multiHome() {
		return ""
	}
	return fmt.Sprintf("%-*s ", m.accountWidth(), "ACCOUNT")
}

func (m model) accountCell(session Session, plain bool) string {
	if !m.multiHome() {
		return ""
	}
	label := homeOf(session)
	cell := fmt.Sprintf("%-*s ", m.accountWidth(), label)
	switch label {
	case "":
		return fmt.Sprintf("%-*s ", m.accountWidth(), "-")
	case targets.DefaultHomeLabel:
		return styleUnless(plain, styleDim, cell)
	}
	return styleUnless(plain, styleAccount, cell)
}

// accountColumns is the width the ACCOUNT column takes from the title.
func (m model) accountColumns() int {
	if !m.multiHome() {
		return 0
	}
	return m.accountWidth() + 1
}

// --- saved default -----------------------------------------------------------

// explorerPrefs is per-machine explorer state that outlives a session.
type explorerPrefs struct {
	// Accounts is the default ACCOUNT filter; empty shows every account.
	Accounts []string `json:"accounts,omitempty"`
}

func prefsPath(cfg config.Config) string {
	dir := filepath.Dir(cfg.ConfigPath)
	if cfg.ConfigPath == "" {
		dir = filepath.Dir(config.Path())
	}
	return filepath.Join(dir, "explorer.json")
}

func loadPrefs(cfg config.Config) explorerPrefs {
	var prefs explorerPrefs
	if data, err := os.ReadFile(prefsPath(cfg)); err == nil {
		_ = json.Unmarshal(data, &prefs)
	}
	return prefs
}

func savePrefs(cfg config.Config, prefs explorerPrefs) error {
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	path := prefsPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// applyDefaultAccounts starts the ACCOUNT filter from the saved default,
// ignoring accounts that no longer exist.
func (m *model) applyDefaultAccounts() {
	m.fHomes = nil
	if !m.multiHome() {
		return
	}
	known := map[string]bool{}
	for _, home := range m.homes {
		known[home.Label] = true
	}
	for _, label := range loadPrefs(m.cfg).Accounts {
		if known[label] {
			if m.fHomes == nil {
				m.fHomes = map[string]bool{}
			}
			m.fHomes[label] = true
		}
	}
}

// saveDefaultAccounts stores the current ACCOUNT selection as the default.
func (m *model) saveDefaultAccounts() {
	var labels []string
	for label := range m.fHomes {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	prefs := loadPrefs(m.cfg)
	prefs.Accounts = labels
	if err := savePrefs(m.cfg, prefs); err != nil {
		m.notice, m.noticeErr = "could not save the default: "+err.Error(), true
		return
	}
	m.noticeErr = false
	if len(labels) == 0 {
		m.notice = "default saved: all accounts"
	} else {
		m.notice = "default saved: " + strings.Join(labels, ", ")
	}
}
