// Package assist is the optional AI find backend: given a natural-language
// description of a half-remembered session and a grounded candidate list,
// it asks a model to pick the likely matches. No vendor is required or
// bundled — everything speaks plain HTTP or shells out to a CLI the user
// already has, and the feature disappears when no backend exists.
package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

// Candidate is one session offered to the model, with just enough context
// to reason about: metadata plus a content excerpt when available.
type Candidate struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Project  string `json:"project,omitempty"`
	LastUsed string `json:"last_used,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
}

// Match is one session the model considers likely, with its reasoning.
type Match struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Backend ranks candidates against a description. Implementations must be
// safe to call from a background goroutine.
type Backend interface {
	Name() string
	Rank(query string, candidates []Candidate) ([]Match, error)
}

// Detect resolves the configured backend, probing availability for auto.
// nil means the feature is unavailable (or configured off).
func Detect(cfg config.Assist) Backend {
	url := cfg.URL
	if url == "" {
		url = "http://localhost:11434"
	}
	switch cfg.Backend {
	case "none":
		return nil
	case "ollama":
		return &ollamaBackend{url: url, model: cfg.Model}
	case "claude":
		if _, err := exec.LookPath("claude"); err != nil {
			return nil
		}
		return &claudeBackend{model: cfg.ClaudeModel}
	default: // auto: local server first, then the CLI
		if ollamaAlive(url) {
			return &ollamaBackend{url: url, model: cfg.Model}
		}
		if _, err := exec.LookPath("claude"); err == nil {
			return &claudeBackend{model: cfg.ClaudeModel}
		}
		return nil
	}
}

// buildPrompt frames the task as re-ranking with evidence — bounded input,
// strict JSON output — which small local models handle well.
func buildPrompt(query string, candidates []Candidate) string {
	list, _ := json.Marshal(candidates)
	return fmt.Sprintf(`You are helping a developer find a coding-agent session on their machine.
They describe a session they half-remember; below are candidate sessions with
metadata and content excerpts. Pick up to 5 likely matches, best first.
Weigh last_used when the description implies recency ("yesterday", "recent").
Respond with ONLY this JSON, no other text:
{"matches":[{"id":"<session id>","reason":"<one short line why>"}]}

Description of the session they are looking for:
%s

Candidates:
%s`, query, list)
}

// parseMatches pulls the matches object out of a model response, tolerating
// prose or code fences around the JSON.
func parseMatches(text string, candidates []Candidate) ([]Match, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON in model response")
	}
	var parsed struct {
		Matches []Match `json:"matches"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil {
		return nil, fmt.Errorf("unparseable model response: %w", err)
	}
	known := map[string]bool{}
	for _, candidate := range candidates {
		known[candidate.ID] = true
	}
	var out []Match
	for _, match := range parsed.Matches {
		if known[match.ID] { // drop hallucinated ids
			out = append(out, match)
		}
	}
	return out, nil
}

// --- ollama: plain HTTP against a local server, stdlib only ---

type ollamaBackend struct {
	url   string
	model string
}

func (b *ollamaBackend) Name() string { return "ollama" }

func ollamaAlive(url string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(url + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// resolveModel picks the configured model, or the first installed one.
func (b *ollamaBackend) resolveModel() (string, error) {
	if b.model != "" {
		return b.model, nil
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(b.url + "/api/tags")
	if err != nil {
		return "", fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil || len(tags.Models) == 0 {
		return "", fmt.Errorf("no ollama models installed (ollama pull <model>)")
	}
	return tags.Models[0].Name, nil
}

func (b *ollamaBackend) Rank(query string, candidates []Candidate) ([]Match, error) {
	model, err := b.resolveModel()
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "user", "content": buildPrompt(query, candidates)},
		},
		"options": map[string]any{"temperature": 0},
	})
	// First use of a model includes load time; be generous.
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Post(b.url+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	var reply struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, fmt.Errorf("ollama response: %w", err)
	}
	if reply.Error != "" {
		return nil, fmt.Errorf("ollama: %s", reply.Error)
	}
	return parseMatches(reply.Message.Content, candidates)
}

// --- claude: headless CLI on the user's own subscription ---

type claudeBackend struct {
	model string
}

func (b *claudeBackend) Name() string { return "claude" }

func (b *claudeBackend) Rank(query string, candidates []Candidate) ([]Match, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	model := b.model
	if model == "" {
		// Ranking a candidate list needs a capable-but-cheap model —
		// never the user's (possibly premium) default.
		model = "sonnet"
	}

	// Run in a throwaway working directory so the helper session never
	// appears in any real project, then remove the transcript claude
	// persists for it. Headless runs write no prompt history, so this
	// leaves zero traces.
	scratch, err := os.MkdirTemp("", "sp-assist-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	if resolved, err := filepath.EvalSymlinks(scratch); err == nil {
		scratch = resolved
	}
	defer os.RemoveAll(filepath.Join(targets.DetectClaude().Source, "projects", targets.ClaudeSlug(scratch)))

	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", model, "--max-turns", "1", buildPrompt(query, candidates))
	cmd.Dir = scratch
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude CLI: %w", err)
	}
	return parseMatches(string(out), candidates)
}
