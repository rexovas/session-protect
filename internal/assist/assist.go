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

// ModelOption is one selectable model across the locally available
// backends.
type ModelOption struct {
	Backend string // ollama | claude
	Model   string
}

// Label renders the option for display.
func (o ModelOption) Label() string {
	return o.Model + " · " + o.Backend
}

// AvailableModels probes what can answer locally: claude CLI aliases
// (default head first) and every installed ollama model. Empty means
// the feature is unavailable.
func AvailableModels(cfg config.Assist) []ModelOption {
	url := cfg.URL
	if url == "" {
		url = "http://localhost:11434"
	}
	var options []ModelOption
	claudeOK := false
	if cfg.Backend != "none" && cfg.Backend != "ollama" {
		if _, err := exec.LookPath("claude"); err == nil {
			claudeOK = true
			head := cfg.ClaudeModel
			if head == "" {
				head = "sonnet"
			}
			options = append(options, ModelOption{Backend: "claude", Model: head})
			for _, alias := range []string{"sonnet", "opus", "haiku"} {
				if alias != head {
					options = append(options, ModelOption{Backend: "claude", Model: alias})
				}
			}
		}
	}
	if cfg.Backend == "auto" || cfg.Backend == "" || cfg.Backend == "codex" {
		if _, err := exec.LookPath("codex"); err == nil {
			// One passthrough entry, no model enumeration: execution never
			// passes a model flag, so codex's own config decides — the
			// same indirection that keeps claude's aliases from going
			// stale. The label is a live read of that config.
			options = append(options, ModelOption{Backend: "codex", Model: codexConfiguredModel()})
		}
	}
	if cfg.Backend != "none" && cfg.Backend != "claude" && cfg.Backend != "codex" {
		for _, model := range listOllamaModels(url) {
			options = append(options, ModelOption{Backend: "ollama", Model: model})
		}
	}
	// Ollama-only setups still get a configured default at the head.
	if !claudeOK && len(options) > 0 && cfg.Model != "" {
		for i, option := range options {
			if option.Model == cfg.Model {
				options[0], options[i] = options[i], options[0]
			}
		}
	}
	return options
}

// RankWith runs the query against an explicit model choice.
func RankWith(cfg config.Assist, option ModelOption, query string, candidates []Candidate) ([]Match, error) {
	url := cfg.URL
	if url == "" {
		url = "http://localhost:11434"
	}
	switch option.Backend {
	case "ollama":
		backend := &ollamaBackend{url: url, model: option.Model}
		return backend.Rank(query, candidates)
	case "claude":
		backend := &claudeBackend{model: option.Model}
		return backend.Rank(query, candidates)
	case "codex":
		out, err := codexComplete(buildPrompt(query, candidates), 120*time.Second)
		if err != nil {
			return nil, err
		}
		return parseMatches(out, candidates)
	}
	return nil, fmt.Errorf("unknown backend %q", option.Backend)
}

// codexConfiguredModel reads the model codex itself would use — display
// only; execution deliberately omits the model flag so the value can
// never drift from what actually runs.
func codexConfiguredModel() string {
	data, err := os.ReadFile(filepath.Join(targets.DetectCodex().Source, "config.toml"))
	if err != nil {
		return "default"
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(trimmed, "model"); ok {
			value = strings.TrimSpace(value)
			if value, ok := strings.CutPrefix(value, "="); ok {
				return strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
	}
	return "default"
}

// codexComplete runs a single-turn prompt through the codex CLI.
// --ephemeral keeps it traceless (no session files written — verified
// against codex-cli 0.147.0); the prompt goes via stdin to dodge arg
// limits; the answer comes back through --output-last-message.
func codexComplete(prompt string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	scratch, err := os.MkdirTemp("", "sp-assist-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	outFile := filepath.Join(scratch, "answer.txt")

	cmd := exec.CommandContext(ctx, "codex", "exec",
		"--ephemeral", "--skip-git-repo-check", "--output-last-message", outFile, "-")
	cmd.Dir = scratch
	cmd.Stdin = strings.NewReader(prompt)
	if _, err := cmd.Output(); err != nil {
		return "", fmt.Errorf("codex CLI: %w", err)
	}
	out, err := os.ReadFile(outFile)
	if err != nil {
		return "", fmt.Errorf("codex CLI wrote no answer: %w", err)
	}
	return string(out), nil
}

// listOllamaModels returns the installed model names, or nothing when
// the server is unreachable.
func listOllamaModels(url string) []string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil
	}
	var names []string
	for _, model := range tags.Models {
		names = append(names, model.Name)
	}
	return names
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
	out, err := b.complete(buildPrompt(query, candidates))
	if err != nil {
		return nil, err
	}
	return parseMatches(out, candidates)
}

func (b *ollamaBackend) complete(prompt string) (string, error) {
	model, err := b.resolveModel()
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"options": map[string]any{"temperature": 0},
	})
	// First use of a model includes load time; be generous.
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Post(b.url+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	var reply struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return "", fmt.Errorf("ollama response: %w", err)
	}
	if reply.Error != "" {
		return "", fmt.Errorf("ollama: %s", reply.Error)
	}
	return reply.Message.Content, nil
}

// --- claude: headless CLI on the user's own subscription ---

type claudeBackend struct {
	model string
}

func (b *claudeBackend) Name() string { return "claude" }

func (b *claudeBackend) Rank(query string, candidates []Candidate) ([]Match, error) {
	model := b.model
	if model == "" {
		// Ranking a candidate list needs a capable-but-cheap model —
		// never the user's (possibly premium) default.
		model = "sonnet"
	}
	out, err := claudeComplete(model, buildPrompt(query, candidates), 120*time.Second)
	if err != nil {
		return nil, err
	}
	return parseMatches(out, candidates)
}

// claudeComplete runs one headless turn with full hygiene: pinned model,
// throwaway working directory, persisted transcript removed after.
// Headless runs write no prompt history, so this leaves zero traces.
func claudeComplete(model string, prompt string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	scratch, err := os.MkdirTemp("", "sp-assist-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	if resolved, err := filepath.EvalSymlinks(scratch); err == nil {
		scratch = resolved
	}
	defer os.RemoveAll(filepath.Join(targets.DetectClaude().Source, "projects", targets.ClaudeSlug(scratch)))

	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", model, "--max-turns", "1", prompt)
	cmd.Dir = scratch
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude CLI: %w", err)
	}
	return string(out), nil
}

// Complete runs a free-form single-turn prompt against an explicit model
// choice and returns the raw text.
func Complete(cfg config.Assist, option ModelOption, prompt string) (string, error) {
	switch option.Backend {
	case "claude":
		return claudeComplete(option.Model, prompt, 300*time.Second)
	case "codex":
		return codexComplete(prompt, 300*time.Second)
	case "ollama":
		url := cfg.URL
		if url == "" {
			url = "http://localhost:11434"
		}
		backend := &ollamaBackend{url: url, model: option.Model}
		return backend.complete(prompt)
	}
	return "", fmt.Errorf("unknown backend %q", option.Backend)
}
