package browse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/config"
)

// embedFn is the embedding call, seamed for tests.
var embedFn = assist.Embed

// The vector cache holds one embedding per session under
// <backup root>/.session-vec/index.json, keyed by session id with the
// content hash and model that produced it — so a session re-embeds only
// when its distilled text or the embedder changes. Semantic AI find
// scores the query embedding against these by cosine similarity.

const vecCacheDir = ".session-vec"

// embedCharLimit caps the text sent to the embedder. Embedders truncate
// at a token budget anyway; the head of the conversation (early prompts,
// the framing of the work) is the most identifying part.
const embedCharLimit = 6000

type vecEntry struct {
	Hash  string    `json:"hash"`
	Model string    `json:"model"`
	Vec   []float32 `json:"vec"`
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

// refreshVecCache embeds any session whose distilled text or embedder
// changed, in bounded batches, and returns the up-to-date index. It is a
// no-op (nil) when no embedder is available. Building over many sessions
// the first time is the slow path; afterwards only new/changed sessions
// re-embed.
func refreshVecCache(cfg config.Config, sessions []Session, model string) map[string]vecEntry {
	if model == "" {
		return nil
	}
	refreshTextCache(cfg, sessions)
	textDir := filepath.Join(cfg.BackupRoot, textCacheDir)
	dir := filepath.Join(cfg.BackupRoot, vecCacheDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	indexPath := filepath.Join(dir, "index.json")
	index := map[string]vecEntry{}
	if data, err := os.ReadFile(indexPath); err == nil {
		_ = json.Unmarshal(data, &index)
	}

	lost := lostTexts(sessions)
	type pending struct {
		id   string
		hash string
		text string
	}
	var todo []pending
	distilled := map[string]string{}
	for _, session := range sessions {
		body := ""
		if session.State == "LOST" {
			body = lost[session.ID]
		} else if data, err := os.ReadFile(filepath.Join(textDir, session.ID+".txt")); err == nil {
			body = string(data)
		}
		text := distillForEmbedding(session, body)
		distilled[session.ID] = text
		hash := vecHash(text)
		if entry, ok := index[session.ID]; ok && entry.Hash == hash && entry.Model == model {
			continue
		}
		todo = append(todo, pending{session.ID, hash, text})
	}

	// Drop cache entries for sessions that no longer exist.
	live := map[string]bool{}
	for _, s := range sessions {
		live[s.ID] = true
	}
	for id := range index {
		if !live[id] {
			delete(index, id)
		}
	}

	const batch = 32
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
			break // partial index is fine; next refresh finishes the rest
		}
		for i, p := range chunk {
			index[p.id] = vecEntry{Hash: p.hash, Model: model, Vec: vecs[i]}
		}
	}

	if data, err := json.Marshal(index); err == nil {
		_ = os.WriteFile(indexPath, data, 0o600)
	}
	return index
}

// semanticScores embeds the query and returns cosine similarity in
// [0,1]-ish (normalized to [0,1] from [-1,1]) for every cached session.
// Returns nil when semantic search is unavailable.
func semanticScores(cfg config.Config, sessions []Session, query string, model string) map[string]float64 {
	if model == "" {
		return nil
	}
	index := refreshVecCache(cfg, sessions, model)
	if len(index) == 0 {
		return nil
	}
	qv, err := embedFn(cfg.Assist, model, []string{query})
	if err != nil || len(qv) == 0 {
		return nil
	}
	q := qv[0]
	scores := map[string]float64{}
	for id, entry := range index {
		if len(entry.Vec) != len(q) {
			continue
		}
		scores[id] = (cosine(q, entry.Vec) + 1) / 2
	}
	return scores
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
