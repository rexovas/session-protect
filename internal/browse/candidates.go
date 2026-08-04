package browse

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/config"
)

// stopwords are description filler that would ground nothing.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "with": true,
	"was": true, "were": true, "where": true, "when": true, "which": true,
	"session": true, "about": true, "this": true, "how": true, "had": true,
	"one": true, "did": true, "its": true, "our": true, "you": true,
	"then": true, "there": true, "into": true, "from": true, "what": true,
	"remember": true, "think": true, "some": true, "worked": true,
	"working": true, "trying": true, "tried": true, "want": true,
	"wanted": true, "looking": true, "find": true, "using": true,
}

const candidateLimit = 30

// BuildCandidates grounds an AI find: the description's significant words
// are counted against the text cache, and the best-scoring sessions go to
// the model with metadata and a matching excerpt. With no word hits at all
// the most recent sessions go instead, metadata only — the model then works
// from titles and projects alone.
func BuildCandidates(cfg config.Config, sessions []Session, query string) []assist.Candidate {
	refreshTextCache(cfg, sessions)
	dir := filepath.Join(cfg.BackupRoot, textCacheDir)

	var words []string
	for _, word := range strings.Fields(strings.ToLower(query)) {
		word = strings.Trim(word, ".,!?\"'()[]")
		if len(word) >= 3 && !stopwords[word] {
			words = append(words, word)
		}
	}

	type scored struct {
		session Session
		score   int
		excerpt string
	}
	var ranked []scored
	for _, session := range sessions {
		if session.State == "LOST" {
			continue
		}
		entry := scored{session: session}
		if data, err := os.ReadFile(filepath.Join(dir, session.ID+".txt")); err == nil && len(data) > 0 {
			text := string(data)
			lower := strings.ToLower(text)
			for _, word := range words {
				count := strings.Count(lower, word)
				entry.score += count
				if count > 0 && entry.excerpt == "" {
					entry.excerpt = snippetAround(text, lower, word)
				}
			}
		}
		ranked = append(ranked, entry)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].session.Modified.After(ranked[j].session.Modified)
	})
	if len(ranked) > candidateLimit {
		ranked = ranked[:candidateLimit]
	}

	var out []assist.Candidate
	for _, entry := range ranked {
		title := entry.session.CustomName
		if title == "" {
			title = entry.session.Title
		}
		out = append(out, assist.Candidate{
			ID:       entry.session.ID,
			Title:    truncate(title, 80),
			Project:  tildePath(entry.session.ProjectPath),
			LastUsed: entry.session.Modified.Format("2006-01-02"),
			Excerpt:  truncate(entry.excerpt, 200),
		})
	}
	return out
}
