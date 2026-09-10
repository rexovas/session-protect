package browse

import (
	"fmt"
	"io"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/config"
)

// AllSessions returns every session across every project, flattened.
func AllSessions(cfg config.Config) []Session {
	var all []Session
	for _, project := range Scan(cfg) {
		for _, session := range project.Sessions {
			session.ProjectPath = project.Path
			all = append(all, session)
		}
	}
	return all
}

// RunIndex builds (or clears) the semantic-search embedding index — the
// opt-in for semantic AI find. Nothing is embedded until this runs, and
// the search path only ever reads the index it produces.
func RunIndex(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return 1
	}
	for _, a := range args {
		if a == "--clear" {
			if err := ClearVecIndex(cfg); err != nil {
				fmt.Fprintln(stderr, "nothing to clear")
				return 0
			}
			fmt.Fprintln(stdout, "semantic index removed — AI find is back to keyword search")
			return 0
		}
	}

	model := assist.EmbedModel(cfg.Assist)
	if model == "" {
		fmt.Fprintln(stderr, "no embedding model found. Install one, e.g.:")
		fmt.Fprintln(stderr, "  ollama pull nomic-embed-text")
		fmt.Fprintln(stderr, "or set assist.embed_model in your config.")
		return 1
	}

	sessions := AllSessions(cfg)
	fmt.Fprintf(stdout, "Building semantic index over %d sessions with %s …\n", len(sessions), shortModel(model))
	total, err := BuildVecIndex(cfg, sessions, model, func(done, count int) {
		if count > 0 {
			fmt.Fprintf(stderr, "\r  embedding %d/%d (%d%%)   ", done, count, done*100/count)
		}
	})
	fmt.Fprintln(stderr)
	if err != nil {
		fmt.Fprintln(stderr, "index build stopped:", err, "(partial index kept — re-run to finish)")
		return 1
	}
	fmt.Fprintf(stdout, "Semantic index ready: %d sessions embedded. ctrl+g now searches by meaning.\n", total)
	return 0
}
