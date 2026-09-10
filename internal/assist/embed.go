package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rexovas/session-protect/internal/config"
)

// knownEmbedders are ollama models that produce embeddings rather than
// chat completions. Auto-detection matches an installed model against
// these prefixes when no embed model is configured.
var knownEmbedders = []string{
	"nomic-embed-text", "mxbai-embed-large", "all-minilm",
	"bge-", "snowflake-arctic-embed", "granite-embedding", "embed",
}

// EmbedModel resolves the ollama embedding model for semantic search:
// the configured model if set and installed, otherwise the first
// installed model that looks like an embedder. Returns "" when semantic
// search is unavailable (no ollama, or no embedder installed), in which
// case callers fall back to keyword grounding.
func EmbedModel(cfg config.Assist) string {
	if cfg.Backend == "none" {
		return ""
	}
	url := cfg.URL
	if url == "" {
		url = "http://localhost:11434"
	}
	installed := listOllamaModels(url)
	if len(installed) == 0 {
		return ""
	}
	has := func(name string) bool {
		for _, m := range installed {
			if m == name || m == name+":latest" {
				return true
			}
		}
		return false
	}
	if cfg.EmbedModel != "" {
		if has(cfg.EmbedModel) {
			return cfg.EmbedModel
		}
		return "" // explicitly configured but absent: don't silently swap
	}
	for _, m := range installed {
		for _, known := range knownEmbedders {
			if len(m) >= len(known) && m[:len(known)] == known {
				return m
			}
		}
	}
	return ""
}

// Embed returns one vector per input string via ollama's /api/embed
// (batch). The caller supplies the model (from EmbedModel).
func Embed(cfg config.Assist, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	url := cfg.URL
	if url == "" {
		url = "http://localhost:11434"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": inputs})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed: status %d", resp.StatusCode)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama embed: got %d vectors for %d inputs", len(out.Embeddings), len(inputs))
	}
	return out.Embeddings, nil
}
