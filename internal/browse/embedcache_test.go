package browse

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/config"
)

// topicEmbed is a fake embedder: one axis per topic word, so cosine
// similarity follows which topics a passage mentions. It counts calls.
func topicEmbed(calls *int) func(config.Assist, string, []string) ([][]float32, error) {
	topics := []string{"newsletter", "database", "photos"}
	return func(_ config.Assist, _ string, inputs []string) ([][]float32, error) {
		out := make([][]float32, len(inputs))
		for i, in := range inputs {
			*calls++
			v := make([]float32, len(topics)+1)
			v[len(topics)] = 0.05 // never all-zero
			for t, topic := range topics {
				v[t] = float32(strings.Count(strings.ToLower(in), topic))
			}
			out[i] = v
		}
		return out, nil
	}
}

// transcript writes a claude transcript whose user turns are the lines of
// text, so the search-text cache built from it holds that text.
func transcript(t *testing.T, path, text string) {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		b.WriteString(`{"type":"user","message":{"role":"user","content":` + strconv.Quote(line) + `}}` + "\n")
	}
	writeFile(t, path, b.String())
}

func passageEnv(t *testing.T) (config.Config, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.Config{
		BackupRoot: filepath.Join(home, "backup"),
		Assist:     config.Assist{Backend: "ollama", EmbedModel: "fake-embed"},
	}
	return cfg, home
}

// Work deep inside a long session is found by meaning: a session scores by
// its best passage, not by its opening.
func TestPassageIndexFindsWorkDeepInASession(t *testing.T) {
	cfg, home := passageEnv(t)
	calls := 0
	embedFn = topicEmbed(&calls)
	defer func() { embedFn = assistEmbed }()

	long := strings.Repeat("sorting photos for the catalogue\n", 600) + // ~20k chars of photos
		"drafting the newsletter for the record sale\n" +
		strings.Repeat("more photos\n", 300)
	sessions := []Session{
		{ID: "long", Title: "photo session", SourcePath: filepath.Join(home, "l.jsonl"), Modified: mustTime("2026-08-01")},
		{ID: "db", Title: "database work", SourcePath: filepath.Join(home, "d.jsonl"), Modified: mustTime("2026-08-02")},
	}
	transcript(t, sessions[0].SourcePath, long)
	transcript(t, sessions[1].SourcePath, strings.Repeat("database migration\n", 50))
	if _, err := BuildVecIndex(cfg, sessions, "fake-embed", nil); err != nil {
		t.Fatal(err)
	}
	scores, best, model := semanticScores(cfg, "newsletter")
	if model != "fake-embed" || scores["long"] <= scores["db"] {
		t.Fatalf("scores = %v", scores)
	}
	cached, _ := os.ReadFile(filepath.Join(cfg.BackupRoot, textCacheDir, "long.txt"))
	passages := sessionPassages(sessions[0], string(cached))
	if best["long"] == 0 || !strings.Contains(passages[best["long"]], "newsletter") {
		t.Fatalf("best passage %d doesn't hold the newsletter work", best["long"])
	}
}

// A growing session embeds only its new passages; an interrupted build
// keeps the last complete version of each session.
func TestPassageIndexIsIncrementalAndSurvivesInterruption(t *testing.T) {
	cfg, home := passageEnv(t)
	calls := 0
	embedFn = topicEmbed(&calls)
	defer func() { embedFn = assistEmbed }()

	session := Session{ID: "s", Title: "t", SourcePath: filepath.Join(home, "s.jsonl"), Modified: mustTime("2026-08-01")}
	text := strings.Repeat("photos and more photos\n", 400) // several passages
	transcript(t, session.SourcePath, text)
	if _, err := BuildVecIndex(cfg, []Session{session}, "fake-embed", nil); err != nil {
		t.Fatal(err)
	}
	first := calls
	if first < 3 {
		t.Fatalf("expected several passages, embedded %d", first)
	}

	transcript(t, session.SourcePath, text+strings.Repeat("a newsletter draft\n", 150))
	bump(t, session.SourcePath)
	if _, err := BuildVecIndex(cfg, []Session{session}, "fake-embed", nil); err != nil {
		t.Fatal(err)
	}
	if added := calls - first; added == 0 || added > 3 {
		t.Fatalf("re-embedded %d passages for a small append", added)
	}

	before := loadVecIndex(cfg)["s"]
	transcript(t, session.SourcePath, text+strings.Repeat("database rewrite\n", 400))
	bump(t, session.SourcePath)
	embedFn = func(config.Assist, string, []string) ([][]float32, error) { return nil, errors.New("ollama stopped") }
	if _, err := BuildVecIndex(cfg, []Session{session}, "fake-embed", nil); err == nil {
		t.Fatal("expected the build to stop")
	}
	if after := loadVecIndex(cfg)["s"]; len(after.Passages) != len(before.Passages) || !allEmbedded(after) {
		t.Fatalf("interrupted build lost the last complete version: %d -> %d passages", len(before.Passages), len(after.Passages))
	}
}

// bump moves a file's mtime forward so the text cache sees the change.
func bump(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

// An index in the earlier one-vector format counts as enabled, so the
// next refresh replaces it; a passage build removes it.
func TestLegacyIndexIsReplaced(t *testing.T) {
	cfg, home := passageEnv(t)
	calls := 0
	embedFn = topicEmbed(&calls)
	defer func() { embedFn = assistEmbed }()
	writeFile(t, legacyVecIndexPath(cfg), "SPVEC1\n")
	if !vecIndexWanted(cfg) || len(loadVecIndex(cfg)) != 0 {
		t.Fatal("legacy index not recognised as enabled-but-unbuilt")
	}
	session := Session{ID: "s", Title: "t", SourcePath: filepath.Join(home, "s.jsonl")}
	transcript(t, session.SourcePath, "photos")
	if _, err := BuildVecIndex(cfg, []Session{session}, "fake-embed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyVecIndexPath(cfg)); err == nil {
		t.Fatal("legacy index kept after a passage build")
	}
	if len(loadVecIndex(cfg)) != 1 {
		t.Fatal("passage index not written")
	}
}
