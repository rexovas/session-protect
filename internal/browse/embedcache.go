package browse

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/config"
)

// embedFn is the embedding call, seamed for tests.
var embedFn = assist.Embed

// The vector cache holds one embedding per session under
// <backup root>/.session-vec/index.bin, keyed by session id with the
// content hash and model that produced it — so a session re-embeds only
// when its distilled text or the embedder changes. Semantic AI find
// scores the query embedding against these by cosine similarity.

const vecCacheDir = ".session-vec"

// embedCharLimit caps the text sent to the embedder. Embedders truncate
// at a token budget anyway; the head of the conversation (early prompts,
// the framing of the work) is the most identifying part.
const embedCharLimit = 6000

type vecEntry struct {
	Hash  string
	Model string
	Vec   []float32
}

// distillForEmbedding builds the text embedded for a session: its title
// and custom name lead (they are dense, query-like signal), followed by
// the head of the conversation text.
func distillForEmbedding(session Session, body string) string {
	head := session.CustomName + "\n" + session.Title + "\n" + body
	if len(head) > embedCharLimit {
		head = head[:embedCharLimit]
	}
	return head
}

func vecHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func vecIndexPath(cfg config.Config) string {
	return filepath.Join(cfg.BackupRoot, vecCacheDir, "index.bin")
}

// loadVecIndex reads the prebuilt embedding index (opt-in: it exists only
// after `sp index`). Empty when semantic search was never enabled.
func loadVecIndex(cfg config.Config) map[string]vecEntry {
	_ = os.Remove(filepath.Join(cfg.BackupRoot, vecCacheDir, "index.json")) // supersede early JSON builds
	return readVecIndex(vecIndexPath(cfg))
}

// indexModel returns the embedder an existing index was built with (all
// entries share it), or "" when there is no index.
func indexModel(index map[string]vecEntry) string {
	for _, entry := range index {
		return entry.Model
	}
	return ""
}

// BuildVecIndex embeds every session whose distilled text or embedder
// changed and writes the index, reporting progress as (done, total).
// This is the explicit, opt-in build behind `sp index` — the search
// path never triggers it, so no work happens without the user asking.
func BuildVecIndex(cfg config.Config, sessions []Session, model string, progress func(done, total int)) (int, error) {
	if model == "" {
		return 0, nil
	}
	refreshTextCache(cfg, sessions)
	textDir := filepath.Join(cfg.BackupRoot, textCacheDir)
	dir := filepath.Join(cfg.BackupRoot, vecCacheDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	index := loadVecIndex(cfg)

	lost := lostTexts(sessions)
	type pending struct {
		id   string
		hash string
		text string
	}
	var todo []pending
	for _, session := range sessions {
		body := ""
		if session.State == "LOST" {
			body = lost[session.ID]
		} else if data, err := os.ReadFile(filepath.Join(textDir, session.ID+".txt")); err == nil {
			body = string(data)
		}
		text := distillForEmbedding(session, body)
		hash := vecHash(text)
		if entry, ok := index[session.ID]; ok && entry.Hash == hash && entry.Model == model {
			continue
		}
		todo = append(todo, pending{session.ID, hash, text})
	}

	live := map[string]bool{}
	for _, s := range sessions {
		live[s.ID] = true
	}
	for id := range index {
		if !live[id] {
			delete(index, id)
		}
	}

	if progress != nil {
		progress(0, len(todo))
	}
	const batch = 32
	done := 0
	for start := 0; start < len(todo); start += batch {
		end := start + batch
		if end > len(todo) {
			end = len(todo)
		}
		chunk := todo[start:end]
		inputs := make([]string, len(chunk))
		for i, p := range chunk {
			inputs[i] = p.text
		}
		vecs, err := embedFn(cfg.Assist, model, inputs)
		if err != nil {
			writeVecIndex(vecIndexPath(cfg), index) // keep what succeeded
			return done, err
		}
		for i, p := range chunk {
			index[p.id] = vecEntry{Hash: p.hash, Model: model, Vec: vecs[i]}
		}
		done += len(chunk)
		if progress != nil {
			progress(done, len(todo))
		}
	}
	writeVecIndex(vecIndexPath(cfg), index)
	return len(index), nil
}

// ClearVecIndex removes the semantic index (opt-out).
func ClearVecIndex(cfg config.Config) error {
	return os.Remove(vecIndexPath(cfg))
}

// VecIndexStats reports the built index: session count and embedder.
func VecIndexStats(cfg config.Config) (count int, model string) {
	index := loadVecIndex(cfg)
	return len(index), indexModel(index)
}

// semanticScores loads the prebuilt index (nil when semantic search was
// never enabled), embeds only the query with the index's own embedder,
// and returns cosine similarity in [0,1] per session plus the embedder
// name. The search path never embeds sessions.
func semanticScores(cfg config.Config, query string) (map[string]float64, string) {
	index := loadVecIndex(cfg)
	if len(index) == 0 {
		return nil, ""
	}
	model := indexModel(index)
	qv, err := embedFn(cfg.Assist, model, []string{query})
	if err != nil || len(qv) == 0 {
		return nil, ""
	}
	q := qv[0]
	scores := map[string]float64{}
	for id, entry := range index {
		if len(entry.Vec) != len(q) {
			continue
		}
		scores[id] = (cosine(q, entry.Vec) + 1) / 2
	}
	return scores, model
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// The vector index is a compact self-describing binary file: floats
// stored raw (~3x smaller than JSON text, and no parse cost on load).
// A version mismatch or short read is treated as an empty cache — it is
// derived from the transcripts and rebuilds cheaply.
var vecMagic = []byte("SPVEC1\n")

func writeVecIndex(path string, index map[string]vecEntry) {
	var buf bytes.Buffer
	buf.Write(vecMagic)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(index)))
	writeStr := func(s string) {
		_ = binary.Write(&buf, binary.LittleEndian, uint16(len(s)))
		buf.WriteString(s)
	}
	for id, entry := range index {
		writeStr(id)
		writeStr(entry.Hash)
		writeStr(entry.Model)
		_ = binary.Write(&buf, binary.LittleEndian, uint16(len(entry.Vec)))
		_ = binary.Write(&buf, binary.LittleEndian, entry.Vec)
	}
	_ = os.WriteFile(path, buf.Bytes(), 0o600)
}

func readVecIndex(path string) map[string]vecEntry {
	index := map[string]vecEntry{}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < len(vecMagic) || !bytes.Equal(data[:len(vecMagic)], vecMagic) {
		return index
	}
	r := bufio.NewReader(bytes.NewReader(data[len(vecMagic):]))
	var count uint32
	if binary.Read(r, binary.LittleEndian, &count) != nil {
		return index
	}
	readStr := func() (string, error) {
		var n uint16
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return "", err
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return string(b), nil
	}
	for i := uint32(0); i < count; i++ {
		id, err := readStr()
		if err != nil {
			break
		}
		hash, err := readStr()
		if err != nil {
			break
		}
		model, err := readStr()
		if err != nil {
			break
		}
		var dims uint16
		if binary.Read(r, binary.LittleEndian, &dims) != nil {
			break
		}
		vec := make([]float32, dims)
		if binary.Read(r, binary.LittleEndian, &vec) != nil {
			break
		}
		index[id] = vecEntry{Hash: hash, Model: model, Vec: vec}
	}
	return index
}
