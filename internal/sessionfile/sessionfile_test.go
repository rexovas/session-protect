package sessionfile

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// WriteCompressed writes content as a zstd-compressed transcript, the way
// newer codex rewrites older rollouts.
func writeCompressed(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamesAndStems(t *testing.T) {
	for name, want := range map[string]struct {
		transcript, compressed bool
		stem                   string
	}{
		"rollout-x-019a.jsonl":     {true, false, "rollout-x-019a"},
		"rollout-x-019a.jsonl.zst": {true, true, "rollout-x-019a"},
		"notes.txt":                {false, false, "notes.txt"},
		"rollout-x.jsonl.lock":     {false, false, "rollout-x.jsonl.lock"},
	} {
		if IsTranscript(name) != want.transcript || Compressed(name) != want.compressed || Stem(name) != want.stem {
			t.Errorf("%s: transcript=%v compressed=%v stem=%q", name, IsTranscript(name), Compressed(name), Stem(name))
		}
	}
}

// Open yields plain JSONL for both forms.
func TestOpenReadsBothForms(t *testing.T) {
	dir := t.TempDir()
	content := "{\"type\":\"session_meta\"}\n{\"type\":\"turn\"}\n"
	plain := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(plain, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	compressed := filepath.Join(dir, "b.jsonl.zst")
	writeCompressed(t, compressed, content)
	for _, path := range []string{plain, compressed} {
		r, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil || string(got) != content {
			t.Fatalf("%s: got %q err %v", path, got, err)
		}
	}
}
