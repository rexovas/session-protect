package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rexovas/session-protect/internal/config"
)

// buildEnv fabricates a home with two nested claude projects and one lost
// session, then returns a model rooted at the work dir.
func buildEnv(t *testing.T) model {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	work := filepath.Join(home, "work")
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		work = filepath.Join(resolved, "work")
	}
	appPath := filepath.Join(work, "app")
	subPath := filepath.Join(work, "app", "sub")

	write := func(project string, id string, prompt string) {
		slug := ""
		for _, c := range project {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				slug += string(c)
			} else {
				slug += "-"
			}
		}
		path := filepath.Join(home, ".claude", "projects", slug, id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		content := `{"type":"user","cwd":"` + project + `","sessionId":"` + id + `","message":{"role":"user","content":"` + prompt + `"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"reply"}],"usage":{"input_tokens":5,"output_tokens":2}}}
`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(appPath, "aaaa1111-0000-0000-0000-000000000001", "searchable alpha prompt")
	write(subPath, "bbbb2222-0000-0000-0000-000000000002", "beta prompt")

	// History carries titles for the live sessions plus one permanently
	// lost session that exists nowhere else.
	now := time.Now().UnixMilli()
	history := fmt.Sprintf(`{"display":"searchable alpha prompt","sessionId":"aaaa1111-0000-0000-0000-000000000001","project":%q,"timestamp":%d}
{"display":"beta prompt","sessionId":"bbbb2222-0000-0000-0000-000000000002","project":%q,"timestamp":%d}
{"display":"the lost one","sessionId":"cccc3333-0000-0000-0000-000000000003","project":%q,"timestamp":%d}`,
		appPath, now, subPath, now, appPath, now)
	if err := os.WriteFile(filepath.Join(home, ".claude", "history.jsonl"), []byte(history+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("backup_root = \""+filepath.Join(home, "root")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := model{cfg: cfg, projects: Scan(cfg), start: work, width: 120, height: 40}
	m.root = NearestRoot(m.projects, work)
	m.rebuild()
	return m
}

func press(t *testing.T, m model, keys ...any) model {
	t.Helper()
	for _, key := range keys {
		var msg tea.KeyMsg
		switch k := key.(type) {
		case tea.KeyType:
			msg = tea.KeyMsg{Type: k}
		case string:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		default:
			t.Fatalf("bad key %v", key)
		}
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func TestNavigationDescendAndBack(t *testing.T) {
	m := buildEnv(t)
	if len(m.folders) != 1 || m.folders[0].Name != "app" {
		t.Fatalf("folders = %+v", m.folders)
	}
	root := m.root

	m = press(t, m, tea.KeyEnter) // descend into app
	if m.root == root || filepath.Base(m.root) != "app" {
		t.Fatalf("enter did not descend: %s", m.root)
	}
	m = press(t, m, tea.KeyEsc)
	if m.root != root {
		t.Fatalf("esc did not return: %s vs %s", m.root, root)
	}
}

func TestExpandAllToggle(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyCtrlE)
	depths := map[int]bool{}
	for _, folder := range m.folders {
		depths[folder.Depth] = true
	}
	if !depths[1] {
		t.Fatalf("ctrl+e did not expand: %+v", m.folders)
	}
	m = press(t, m, tea.KeyCtrlE)
	for _, folder := range m.folders {
		if folder.Depth > 0 {
			t.Fatal("second ctrl+e did not collapse")
		}
	}
}

func TestFilterStaysInPane(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter) // into app: folder+session panes
	m = press(t, m, tea.KeyTab)   // sessions pane
	if !m.showSessions {
		t.Fatal("tab did not switch panes")
	}
	m = press(t, m, "/", "z", "z", "z")
	if !m.searching || m.query != "zzz" {
		t.Fatalf("search state: %v %q", m.searching, m.query)
	}
	if !m.showSessions {
		t.Fatal("empty result must not flip panes")
	}
	if view := m.View(); !strings.Contains(view, "no matches for /zzz") {
		t.Fatal("no-matches line missing")
	}
	m = press(t, m, tea.KeyEsc)
	if m.query != "" || m.searching {
		t.Fatal("esc did not clear the query")
	}
}

func TestFilterFindsSessionByTitle(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab) // app sessions pane
	m = press(t, m, "/", "a", "l", "p", "h", "a")
	if len(m.visible) != 1 || !strings.Contains(m.visible[0].Title, "alpha") {
		t.Fatalf("filter missed the session: %+v", m.visible)
	}
	m = press(t, m, tea.KeyEnter) // keep filter
	if m.searching || m.query != "alpha" {
		t.Fatal("enter should keep the filter")
	}
}

func TestLostToggleAndSearchOverride(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab)
	base := len(m.visible)
	m = press(t, m, "x")
	if len(m.visible) != base+1 {
		t.Fatalf("x did not reveal the lost session: %d → %d", base, len(m.visible))
	}
	if state, _ := sessionState(m.visible[len(m.visible)-1].State); !strings.Contains(state, "lost") {
		t.Fatal("revealed session should be lost")
	}
	m = press(t, m, "x")
	if len(m.visible) != base {
		t.Fatal("x did not hide again")
	}
	// An active query overrides the toggle.
	m = press(t, m, "/", "l", "o", "s", "t")
	found := false
	for _, session := range m.visible {
		if session.State == "LOST" {
			found = true
		}
	}
	if !found {
		t.Fatal("search must find lost sessions with the toggle off")
	}
}

func TestMenuTabsAndKeysReference(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, "m")
	if !m.showMenu || m.menuTab != 0 {
		t.Fatalf("menu state: %v %d", m.showMenu, m.menuTab)
	}
	if view := m.View(); !strings.Contains(view, "PROTECTION") {
		t.Fatal("stats tab missing protection summary")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "no recorded actions yet") {
		t.Fatal("activity tab empty-state missing")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "NAVIGATE") || !strings.Contains(view, "transplant") {
		t.Fatal("keys tab reference missing")
	}
	m = press(t, m, tea.KeyEsc)
	if m.showMenu {
		t.Fatal("esc did not close the menu")
	}
	// s deep-links into stats.
	m = press(t, m, "s")
	if !m.showMenu || m.menuTab != 0 {
		t.Fatal("s should open the stats section")
	}
}

func TestDetailTabsLifecycle(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab) // app sessions
	m = press(t, m, "i")
	if m.detail == nil {
		t.Fatal("i did not open the inspector")
	}
	if view := m.View(); !strings.Contains(view, "initial prompt") || !strings.Contains(view, "searchable alpha prompt") {
		t.Fatalf("overview missing prompt box")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "CACHE READ") {
		t.Fatal("usage tab missing")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "session start") {
		t.Fatal("transcript should show the start marker for a short session")
	}
	m = press(t, m, tea.KeyEsc)
	if m.detail != nil {
		t.Fatal("esc did not close the inspector")
	}
}

func TestHitsPaneLifecycle(t *testing.T) {
	m := buildEnv(t)
	m.hitsQuery = "alpha"
	next, _ := m.Update(hitsMsg([]Hit{
		{Session: m.projects[0].Sessions[0], Count: 7, Snippet: "searchable alpha prompt"},
	}))
	m = next.(model)
	if !m.showHits || len(m.hits) != 1 {
		t.Fatal("hitsMsg did not open the pane")
	}
	if view := m.View(); !strings.Contains(view, "HITS") || !strings.Contains(view, "7") {
		t.Fatal("hits view missing count")
	}
	m = press(t, m, tea.KeyEnter) // open the session from the hit
	if m.detail == nil {
		t.Fatal("enter on a hit should open detail")
	}
	m = press(t, m, tea.KeyEsc, tea.KeyEsc)
	if m.showHits {
		t.Fatal("esc did not leave the hits pane")
	}
}

func TestAskPageLifecycle(t *testing.T) {
	m := buildEnv(t)
	// No backend reachable: notice, no page.
	t.Setenv("PATH", t.TempDir())
	m.cfg.Assist = config.Assist{Backend: "auto", URL: "http://127.0.0.1:1"}
	m = press(t, m, tea.KeyCtrlG)
	if m.showAsk || m.notice == "" {
		t.Fatal("missing backend should notice, not open")
	}
	// Explicit ollama config opens without probing.
	m.cfg.Assist = config.Assist{Backend: "ollama", URL: "http://127.0.0.1:1"}
	m.notice = ""
	m = press(t, m, tea.KeyCtrlG)
	if !m.showAsk || m.askBackend != "ollama" {
		t.Fatalf("ask page state: %v %q", m.showAsk, m.askBackend)
	}
	m = press(t, m, "h", "i", tea.KeySpace, "x")
	if m.askInput != "hi x" {
		t.Fatalf("askInput = %q", m.askInput)
	}
	if view := m.View(); !strings.Contains(view, "ai find") || !strings.Contains(view, "hi x") {
		t.Fatal("ask view missing input")
	}
	m = press(t, m, tea.KeyEsc)
	if m.showAsk {
		t.Fatal("esc did not close the ask page")
	}
}

func TestTransplantPageOpenToggleClose(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, "t") // folder scope on app
	if !m.showTransplant || !strings.HasPrefix(m.tpSource, "project ") {
		t.Fatalf("transplant state: %v %q", m.showTransplant, m.tpSource)
	}
	if m.tpCopy {
		t.Fatal("default mode must be move")
	}
	m = press(t, m, tea.KeyTab)
	if !m.tpCopy {
		t.Fatal("tab did not toggle to copy")
	}
	if view := m.View(); !strings.Contains(view, "copy") || !strings.Contains(view, "target directory") {
		t.Fatal("transplant view missing elements")
	}
	m = press(t, m, tea.KeyEsc)
	if m.showTransplant {
		t.Fatal("esc did not close the transplant page")
	}
}

func TestResizeRebuildsLayout(t *testing.T) {
	m := buildEnv(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(model)
	if m.width != 80 || m.height != 24 {
		t.Fatal("resize not applied")
	}
	if view := m.View(); !strings.Contains(view, "Session Explorer") {
		t.Fatal("view broken after resize")
	}
}
