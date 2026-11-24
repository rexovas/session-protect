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
		switch a {
		case "--clear":
			if err := ClearVecIndex(cfg); err != nil {
				fmt.Fprintln(stderr, "nothing to clear")
				return 0
			}
			fmt.Fprintln(stdout, "semantic index removed — AI find is back to keyword search")
			return 0
		case "help", "-h", "--help":
			indexUsage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "unexpected argument: %s\n\n", a)
			indexUsage(stderr)
			return 2
		}
	}

	if !assist.EmbedderReachable(cfg.Assist) {
		fmt.Fprintln(stderr, "can't reach Ollama, which computes the embeddings. Start it with:")
		fmt.Fprintln(stderr, "  brew services start ollama   (stays running across logins)")
		fmt.Fprintln(stderr, "or run `ollama serve` in another terminal.")
		return 1
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

func indexUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect index           build or refresh the semantic index (only")
	fmt.Fprintln(out, "                                  new or changed sessions are embedded)")
	fmt.Fprintln(out, "  session-protect index --clear   remove it; AI find goes back to keywords")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The index lets AI find match sessions by meaning, not just shared words.")
	fmt.Fprintln(out, "It needs Ollama running with an embedding model (ollama pull")
	fmt.Fprintln(out, "nomic-embed-text). Once built, the daily backup keeps it current.")
}

// RefreshVecIndex brings an existing semantic index up to date, quietly:
// the daily backup runs it so the index never goes stale. It does nothing
// when no index was built (semantic search is opt-in) or when the
// embedder can't be reached, and reports how many sessions it embedded.
func RefreshVecIndex(cfg config.Config) (int, error) {
	if !vecIndexWanted(cfg) || !assist.EmbedderReachable(cfg.Assist) {
		return 0, nil
	}
	index := loadVecIndex(cfg)
	model := indexModel(index)
	if model == "" {
		model = assist.EmbedModel(cfg.Assist) // replacing an earlier-format index
	}
	before := len(index)
	total, err := BuildVecIndex(cfg, AllSessions(cfg), model, func(int, int) {})
	return max(0, total-before), err
}
