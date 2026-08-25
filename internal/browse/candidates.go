package browse

import (
	"math"
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
// from titles and projects alone. The returned counts (raw keyword hits
// per session) are display-only: they never reach the model.
func BuildCandidates(cfg config.Config, sessions []Session, query string) ([]assist.Candidate, map[string]int) {
	refreshTextCache(cfg, sessions)
	dir := filepath.Join(cfg.BackupRoot, textCacheDir)

	var words []string
	for _, word := range strings.Fields(strings.ToLower(query)) {
		word = strings.Trim(word, ".,!?\"'()[]")
		// Possessives search for the base name: lunny's → lunny.
		word = strings.TrimSuffix(word, "'s")
		word = strings.TrimSuffix(word, "’s")
		if len(word) >= 3 && !stopwords[word] {
			words = append(words, word)
		}
	}

	// Two passes: count per word per session, then score with inverse
	// document frequency so a rare discriminating term (a name, a
	// project word) outweighs floods of generic vocabulary.
	type counted struct {
		session Session
		counts  []int
		text    string
		lower   string
	}
	lost := lostTexts(sessions)
	var all []counted
	df := make([]int, len(words))
	for _, session := range sessions {
		entry := counted{session: session, counts: make([]int, len(words))}
		if session.State == "LOST" {
			entry.text = lost[session.ID]
		} else if data, err := os.ReadFile(filepath.Join(dir, session.ID+".txt")); err == nil {
			entry.text = string(data)
		}
		if entry.text != "" {
			entry.lower = strings.ToLower(entry.text)
			for i, word := range words {
				entry.counts[i] = strings.Count(entry.lower, word)
				if entry.counts[i] > 0 {
					df[i]++
				}
			}
		}
		all = append(all, entry)
	}
	idf := make([]float64, len(words))
	for i := range words {
		idf[i] = math.Log(1 + float64(len(all))/float64(1+df[i]))
	}

	type scored struct {
		session Session
		score   float64
		excerpt string
	}
	var ranked []scored
	for _, entry := range all {
		item := scored{session: entry.session}
		bestIDF := 0.0
		for i := range words {
			if entry.counts[i] == 0 {
				continue
			}
			item.score += float64(entry.counts[i]) * idf[i]
			// The excerpt comes from the rarest matched word — the one
			// that most likely names what the user remembers.
			if idf[i] > bestIDF {
				bestIDF = idf[i]
				item.excerpt = snippetAround(entry.text, entry.lower, words[i])
			}
		}
		ranked = append(ranked, item)
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

	rawHits := map[string]int{}
	for _, entry := range all {
		total := 0
		for _, count := range entry.counts {
			total += count
		}
		if total > 0 {
			rawHits[entry.session.ID] = total
		}
	}

	var out []assist.Candidate
	for _, entry := range ranked {
		title := entry.session.CustomName
		if title == "" {
			title = entry.session.Title
		}
		if entry.session.State == "LOST" {
			title = "(LOST — transcript gone, prompts survive) " + title
		}
		out = append(out, assist.Candidate{
			ID:       entry.session.ID,
			Title:    truncate(title, 80),
			Project:  tildePath(entry.session.ProjectPath),
			LastUsed: entry.session.Modified.Format("2006-01-02"),
			Excerpt:  truncate(entry.excerpt, 200),
		})
	}
	return out, rawHits
}
