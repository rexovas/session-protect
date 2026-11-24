package browse

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/config"
)

// embedFn is the embedding call, seamed for tests.
var embedFn = assist.Embed

// semanticUnavailable is the retrieval label when a semantic index exists
// but the query couldn't be embedded (Ollama not running).
const semanticUnavailable = "unavailable"

// The semantic index embeds every session in passages (about a screenful
// of conversation each), so work deep inside a long session is as
// findable as its opening; a session scores by its best passage. It lives
// under <backup root>/.session-vec/passages.bin, keyed by session id. Each
// passage carries a hash of its text, so a growing session embeds only
// its new passages, and the embedder's name, so a model change rebuilds.
//
// Vectors are stored as int8 with no per-vector scale: cosine similarity
// is scale-invariant, so the rounding is the only loss, and on real
// histories it left rankings unchanged at a quarter of the size.

const vecCacheDir = ".session-vec"

const (
	passageSize = 2000 // characters per passage, cut at a line break
	embedBatch  = 32
	saveEvery   = 20 // batches between saves during a long build
)

type passageVec struct {
	Hash [8]byte
	Q    []int8
	norm float64 // computed on load
}

type vecEntry struct {
	Model    string
	Passages []passageVec
}

func vecIndexPath(cfg config.Config) string {
	return filepath.Join(cfg.BackupRoot, vecCacheDir, "passages.bin")
}

// legacyVecIndexPath is the earlier one-vector-per-session index. Its
// presence means semantic search was enabled, so a refresh replaces it.
func legacyVecIndexPath(cfg config.Config) string {
	return filepath.Join(cfg.BackupRoot, vecCacheDir, "index.bin")
}

// vecIndexWanted reports whether semantic search was enabled: an index
// exists, in either format.
func vecIndexWanted(cfg config.Config) bool {
	for _, path := range []string{vecIndexPath(cfg), legacyVecIndexPath(cfg)} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// loadVecIndex reads the passage index (opt-in: it exists only after
// `sp index`). Empty when semantic search was never enabled or the index
// is still in the earlier format.
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

// sessionPassages splits a session's text into passages of about
// passageSize characters, cut at line breaks. The custom name and title
// lead the first passage: they are dense, query-like signal.
func sessionPassages(session Session, body string) []string {
	head := strings.TrimSpace(session.CustomName + "\n" + session.Title)
	var out []string
	for start := 0; start < len(body); {
		end := min(len(body), start+passageSize)
		if end < len(body) {
			if cut := strings.LastIndexByte(body[start:end], '\n'); cut > passageSize/2 {
				end = start + cut + 1
			}
		}
		out = append(out, body[start:end])
		start = end
	}
	if len(out) == 0 {
		out = []string{""}
	}
	if head != "" {
		out[0] = head + "\n" + out[0]
	}
	return out
}

// embedInput labels text for embedders trained on task prefixes: nomic
// models expect "search_document: " / "search_query: ".
func embedInput(model, kind, text string) string {
	if strings.Contains(model, "nomic") {
		return kind + ": " + text
	}
	return text
}

func passageHash(text string) [8]byte {
	sum := sha256.Sum256([]byte(text))
	var out [8]byte
	copy(out[:], sum[:8])
	return out
}

func quantize(vec []float32) []int8 {
	peak := 0.0
	for _, x := range vec {
		peak = math.Max(peak, math.Abs(float64(x)))
	}
	out := make([]int8, len(vec))
	if peak == 0 {
		return out
	}
	for i, x := range vec {
		out[i] = int8(math.Round(float64(x) / peak * 127))
	}
	return out
}

// BuildVecIndex embeds every passage whose text or embedder changed and
// writes the index, reporting progress as (done, total) passages. This is
// the explicit, opt-in build behind `sp index` (and the daily refresh of
// an index that exists): the search path never embeds sessions.
func BuildVecIndex(cfg config.Config, sessions []Session, model string, progress func(done, total int)) (int, error) {
	if model == "" {
		return 0, nil
	}
	refreshTextCache(cfg, sessions)
	textDir := filepath.Join(cfg.BackupRoot, textCacheDir)
	if err := os.MkdirAll(filepath.Join(cfg.BackupRoot, vecCacheDir), 0o700); err != nil {
		return 0, err
	}
	old := loadVecIndex(cfg)
	index := map[string]vecEntry{}

	lost := lostTexts(sessions)
	type pending struct {
		id   string
		slot int
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
		texts := sessionPassages(session, body)
		reuse := map[[8]byte][]int8{}
		if prior, ok := old[session.ID]; ok && prior.Model == model {
			for _, p := range prior.Passages {
				reuse[p.Hash] = p.Q
			}
		}
		entry := vecEntry{Model: model, Passages: make([]passageVec, len(texts))}
		for i, text := range texts {
			hash := passageHash(text)
			entry.Passages[i].Hash = hash
			if q, ok := reuse[hash]; ok {
				entry.Passages[i].Q = q
				continue
			}
			todo = append(todo, pending{session.ID, i, text})
		}
		index[session.ID] = entry
	}

	if progress != nil {
		progress(0, len(todo))
	}
	save := func() {
		complete := map[string]vecEntry{}
		for id, entry := range index {
			if allEmbedded(entry) {
				complete[id] = entry
			} else if prior, ok := old[id]; ok {
				complete[id] = prior // keep the last full version until this one is
			}
		}
		writeVecIndex(vecIndexPath(cfg), complete)
	}
	done := 0
	for start, batches := 0, 0; start < len(todo); start, batches = start+embedBatch, batches+1 {
		chunk := todo[start:min(len(todo), start+embedBatch)]
		inputs := make([]string, len(chunk))
		for i, p := range chunk {
			inputs[i] = embedInput(model, "search_document", p.text)
		}
		vecs, err := embedFn(cfg.Assist, model, inputs)
		if err != nil || len(vecs) != len(chunk) {
			save() // keep what succeeded
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return done, err
		}
		for i, p := range chunk {
			index[p.id].Passages[p.slot].Q = quantize(vecs[i])
		}
		done += len(chunk)
		if progress != nil {
			progress(done, len(todo))
		}
		if batches%saveEvery == saveEvery-1 {
			save()
		}
	}
	save()
	_ = os.Remove(legacyVecIndexPath(cfg))
	return len(index), nil
}

func allEmbedded(entry vecEntry) bool {
	for _, p := range entry.Passages {
		if p.Q == nil {
			return false
		}
	}
	return true
}

// ClearVecIndex removes the semantic index (opt-out).
func ClearVecIndex(cfg config.Config) error {
	_ = os.Remove(legacyVecIndexPath(cfg))
	return os.Remove(vecIndexPath(cfg))
}

// VecIndexStats reports the built index: session count and embedder.
func VecIndexStats(cfg config.Config) (count int, model string) {
	index := loadVecIndex(cfg)
	return len(index), indexModel(index)
}

// semanticScores loads the index (nil when semantic search was never
// enabled), embeds only the query with the index's own embedder, and
// returns per session the best passage's cosine similarity, mapped to
// [0,1], and that passage's position, plus the embedder name.
func semanticScores(cfg config.Config, query string) (map[string]float64, map[string]int, string) {
	index := loadVecIndex(cfg)
	if len(index) == 0 {
		return nil, nil, ""
	}
	model := indexModel(index)
	qv, err := embedFn(cfg.Assist, model, []string{embedInput(model, "search_query", query)})
	if err != nil || len(qv) == 0 {
		return nil, nil, ""
	}
	q := qv[0]
	qNorm := 0.0
	for _, x := range q {
		qNorm += float64(x) * float64(x)
	}
	qNorm = math.Sqrt(qNorm)
	scores, best := map[string]float64{}, map[string]int{}
	for id, entry := range index {
		top, at := -1.0, -1
		for i, p := range entry.Passages {
			if len(p.Q) != len(q) || p.norm == 0 || qNorm == 0 {
				continue
			}
			dot := 0.0
			for j, x := range p.Q {
				dot += float64(q[j]) * float64(x)
			}
			if sim := dot / (qNorm * p.norm); sim > top {
				top, at = sim, i
			}
		}
		if at >= 0 {
			scores[id], best[id] = (top+1)/2, at
		}
	}
	return scores, best, model
}

// The index file: a magic line, then per session its id, embedder, and
// passages (8-byte hash + int8 vector). A version mismatch or short read
// is an empty index: it is derived from the transcripts and rebuilds.
var vecMagic = []byte("SPVEC2\n")

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
		writeStr(entry.Model)
		dims := 0
		if len(entry.Passages) > 0 {
			dims = len(entry.Passages[0].Q)
		}
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(entry.Passages)))
		_ = binary.Write(&buf, binary.LittleEndian, uint16(dims))
		for _, p := range entry.Passages {
			buf.Write(p.Hash[:])
			_ = binary.Write(&buf, binary.LittleEndian, p.Q)
		}
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
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
		model, err := readStr()
		if err != nil {
			break
		}
		var passages uint32
		var dims uint16
		if binary.Read(r, binary.LittleEndian, &passages) != nil || binary.Read(r, binary.LittleEndian, &dims) != nil {
			break
		}
		entry := vecEntry{Model: model, Passages: make([]passageVec, passages)}
		ok := true
		for j := range entry.Passages {
			p := &entry.Passages[j]
			if _, err := io.ReadFull(r, p.Hash[:]); err != nil {
				ok = false
				break
			}
			p.Q = make([]int8, dims)
			if binary.Read(r, binary.LittleEndian, &p.Q) != nil {
				ok = false
				break
			}
			for _, x := range p.Q {
				p.norm += float64(x) * float64(x)
			}
			p.norm = math.Sqrt(p.norm)
		}
		if !ok {
			break
		}
		index[id] = entry
	}
	return index
}
