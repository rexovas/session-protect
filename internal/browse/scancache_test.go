package browse

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func appendFile(t *testing.T, path string, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// Only newline-terminated lines advance the offset; a truncated file
// signals a restart.
func TestReadAppendedPartialAndTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	writeFile(t, path, "a\nb\npart")
	var seen []string
	next, ok := readAppended(path, 0, false, func(line []byte) { seen = append(seen, string(line)) })
	if !ok || next != 4 || strings.Join(seen, ",") != "a,b" {
		t.Fatalf("next=%d ok=%v seen=%v", next, ok, seen)
	}
	seen = nil
	appendFile(t, path, "ial\nc\n")
	next, _ = readAppended(path, next, false, func(line []byte) { seen = append(seen, string(line)) })
	if next != 14 || strings.Join(seen, ",") != "partial,c" {
		t.Fatalf("next=%d seen=%v", next, seen)
	}
	writeFile(t, path, "x\n")
	if _, ok := readAppended(path, next, false, func([]byte) {}); ok {
		t.Fatal("a shrunken file must report !ok so the caller restarts")
	}
}

// scanFileMeta picks up appended names and models, sees a line still
// being written, and starts over when the file is replaced.
func TestScanFileMetaIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, `{"type":"assistant","message":{"model":"claude-opus-4-8"}}`+"\n")
	if name, model := scanFileMeta(path); name != "" || model != "claude-opus-4-8" {
		t.Fatalf("initial: %q %q", name, model)
	}
	appendFile(t, path, `{"type":"custom-title","customTitle":"FIRST"}`+"\n"+
		`{"type":"assistant","message":{"model":"claude-fable-5"}}`+"\n")
	if name, model := scanFileMeta(path); name != "FIRST" || model != "claude-fable-5" {
		t.Fatalf("after append: %q %q", name, model)
	}
	// A line still being written (no newline yet) already counts...
	appendFile(t, path, `{"type":"custom-title","customTitle":"SECOND"}`)
	if name, _ := scanFileMeta(path); name != "SECOND" {
		t.Fatalf("partial line: %q", name)
	}
	// ...and is read again, correctly, once complete.
	appendFile(t, path, "\n"+`{"type":"assistant","message":{"model":"claude-opus-5"}}`+"\n")
	if name, model := scanFileMeta(path); name != "SECOND" || model != "claude-opus-5" {
		t.Fatalf("completed line: %q %q", name, model)
	}
	// A rewritten, shorter file is parsed from scratch.
	writeFile(t, path, `{"type":"assistant","message":{"model":"claude-haiku-4-5"}}`+"\n")
	if name, model := scanFileMeta(path); name != "" || model != "claude-haiku-4-5" {
		t.Fatalf("after rewrite: %q %q", name, model)
	}
}

// Incremental history indexing must match a from-scratch parse, and must
// never mutate a map already handed to an earlier caller.
func TestHistoryCacheIncrementalMatchesFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writeFile(t, path, `{"display":"/model","sessionId":"a","project":"/p","timestamp":1000}
{"display":"first real prompt","sessionId":"a","project":"/p","timestamp":2000}
`)
	cache := &historyCache{apply: claudeHistory.apply}
	firstSessions, firstTitles := cache.load(path)
	if firstSessions["a"].Count != 2 || firstTitles["a"] != "first real prompt" {
		t.Fatalf("initial: %+v %v", firstSessions["a"], firstTitles)
	}
	appendFile(t, path, `{"display":"more","sessionId":"a","project":"/p","timestamp":3000}
{"display":"other","sessionId":"b","project":"/q","timestamp":4000}
`)
	sessions, titles := cache.load(path)
	fresh := &historyCache{apply: claudeHistory.apply}
	wantSessions, wantTitles := fresh.load(path)
	if !reflect.DeepEqual(sessions, wantSessions) || !reflect.DeepEqual(titles, wantTitles) {
		t.Fatalf("incremental != full:\n%+v\n%+v", sessions, wantSessions)
	}
	if firstSessions["a"].Count != 2 || len(firstSessions) != 1 {
		t.Fatal("an earlier caller's map was mutated")
	}
	// Unchanged file: the same maps come back without a re-read.
	again, _ := cache.load(path)
	if reflect.ValueOf(again).Pointer() != reflect.ValueOf(sessions).Pointer() {
		t.Fatal("unchanged history was re-indexed")
	}
}

// A found cwd survives the transcript growing; an unmatched short one is
// re-read, so a later matching line is still found.
func TestCachedClaudeCwdSurvivesGrowth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeFile(t, path, `{"cwd":"/elsewhere"}`+"\n")
	slug := "-work-app"
	if matched, first := cachedClaudeCwd(path, slug); matched != "" || first != "/elsewhere" {
		t.Fatalf("initial: %q %q", matched, first)
	}
	appendFile(t, path, `{"cwd":"/work/app"}`+"\n")
	if matched, _ := cachedClaudeCwd(path, slug); matched != "/work/app" {
		t.Fatalf("unmatched result was not re-read after growth: %q", matched)
	}
	appendFile(t, path, `{"cwd":"/work/other"}`+"\n")
	if matched, _ := cachedClaudeCwd(path, slug); matched != "/work/app" {
		t.Fatalf("found result changed after growth: %q", matched)
	}
}

// Unfocused, the refresh loop only rescans once a minute; regaining focus
// on a stale view rescans immediately.
func TestTickBacksOffWhenBlurred(t *testing.T) {
	m := model{blurred: true, lastScan: time.Now()}
	next, _ := m.Update(tickMsg(time.Now()))
	if next.(model).scanning {
		t.Fatal("blurred tick rescanned within the idle interval")
	}
	m.lastScan = time.Now().Add(-2 * idleRefreshEvery)
	next, _ = m.Update(tickMsg(time.Now()))
	if !next.(model).scanning {
		t.Fatal("blurred tick never rescanned after the idle interval")
	}
	m = model{lastScan: time.Now()}
	next, _ = m.Update(tickMsg(time.Now()))
	if !next.(model).scanning {
		t.Fatal("focused tick did not rescan")
	}
	m = model{scanning: true}
	next, _ = m.Update(tickMsg(time.Now()))
	if n := next.(model); !n.scanning || !n.lastScan.IsZero() {
		t.Fatal("a tick stacked a second scan on one in flight")
	}
	m = model{blurred: true, lastScan: time.Now().Add(-time.Minute)}
	next, cmd := m.Update(tea.FocusMsg{})
	if n := next.(model); n.blurred || !n.scanning || cmd == nil {
		t.Fatal("regaining focus on a stale view did not rescan")
	}
}
