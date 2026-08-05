package assist

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/config"
)

var testCandidates = []Candidate{
	{ID: "aaa", Title: "conduit mesh"},
	{ID: "bbb", Title: "tax notes"},
}

func TestParseMatchesTolerantAndStrictOnIDs(t *testing.T) {
	text := "Sure! Here you go:\n```json\n{\"matches\":[{\"id\":\"aaa\",\"reason\":\"mesh work\"},{\"id\":\"zzz\",\"reason\":\"hallucinated\"}]}\n```"
	matches, err := parseMatches(text, testCandidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != "aaa" || matches[0].Reason != "mesh work" {
		t.Fatalf("expected the one real id to survive, got %+v", matches)
	}
	if _, err := parseMatches("I could not find anything.", testCandidates); err == nil {
		t.Fatal("prose without JSON must error")
	}
}

func TestOllamaBackendRank(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"testmodel:latest"}]}`))
		case "/api/chat":
			var req struct {
				Model    string `json:"model"`
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Model != "testmodel:latest" {
				t.Errorf("model not resolved from tags: %q", req.Model)
			}
			if !strings.Contains(req.Messages[0].Content, "conduit mesh") {
				t.Error("prompt missing candidate data")
			}
			_, _ = w.Write([]byte(`{"message":{"content":"{\"matches\":[{\"id\":\"aaa\",\"reason\":\"mesh\"}]}"}}`))
		}
	}))
	defer server.Close()

	backend := Detect(config.Assist{Backend: "ollama", URL: server.URL})
	if backend == nil || backend.Name() != "ollama" {
		t.Fatal("expected the ollama backend")
	}
	matches, err := backend.Rank("the mesh session", testCandidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != "aaa" {
		t.Fatalf("unexpected matches: %+v", matches)
	}
}

func TestDetectNone(t *testing.T) {
	if Detect(config.Assist{Backend: "none"}) != nil {
		t.Fatal("backend none must disable the feature")
	}
}

func TestClaudeBackendViaStub(t *testing.T) {
	dir := t.TempDir()
	stub := dir + "/claude"
	script := "#!/bin/sh\necho 'Sure!'\necho '{\"matches\":[{\"id\":\"bbb\",\"reason\":\"stubbed\"}]}'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	backend := Detect(config.Assist{Backend: "claude"})
	if backend == nil || backend.Name() != "claude" {
		t.Fatal("claude backend not detected from PATH")
	}
	matches, err := backend.Rank("find it", testCandidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != "bbb" || matches[0].Reason != "stubbed" {
		t.Fatalf("matches = %+v", matches)
	}

	// auto with no ollama reachable falls through to the CLI.
	auto := Detect(config.Assist{Backend: "auto", URL: "http://127.0.0.1:1"})
	if auto == nil || auto.Name() != "claude" {
		t.Fatal("auto did not fall back to claude")
	}
}

func TestDetectAbsentEverything(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if Detect(config.Assist{Backend: "claude"}) != nil {
		t.Fatal("claude backend without a binary must be nil")
	}
	if Detect(config.Assist{Backend: "auto", URL: "http://127.0.0.1:1"}) != nil {
		t.Fatal("auto with nothing available must be nil")
	}
}
