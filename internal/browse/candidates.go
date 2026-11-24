package browse

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
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

const (
	bm25K        = 1.2  // term-count saturation
	bm25B        = 0.75 // length normalisation
	passageChars = 1500 // a passage: about a screenful of conversation
	promptWeight = 1.5  // the user's own words outweigh the agent's
	maxAnchors   = 300  // passages tried per session
)

// Blend weights for hybrid retrieval: semantic leads for recall,
// keyword grounds for exact-name precision.
const (
	semanticWeight = 0.6
	keywordWeight  = 0.4
)

// headExcerpt returns a compact opening snippet of a transcript for
// candidates that matched semantically but share no query keyword.
func headExcerpt(text string) string {
	return truncate(strings.Join(strings.Fields(text), " "), 200)
}

// BuildCandidates grounds an AI find: the description's significant words
// are counted against the text cache, and the best-scoring sessions go to
// the model with metadata and a matching excerpt. With no word hits at all
// the most recent sessions go instead, metadata only — the model then works
// from titles and projects alone. The returned counts (raw keyword hits
// per session) are display-only: they never reach the model.
func BuildCandidates(cfg config.Config, sessions []Session, query string) ([]assist.Candidate, map[string]int, string) {
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

	// Semantic scores (cosine similarity of query vs each session's
	// embedding) are the recall engine — they find sessions described in
	// words that never appear in the transcript. Keyword IDF stays the
	// precision engine — an exact name or file path outweighs vague
	// vocabulary. When an embedder is available the two blend; without
	// one, this is pure keyword grounding exactly as before.
	sem, semPassage, retrieval := semanticScores(cfg, query)
	if sem == nil && len(loadVecIndex(cfg)) > 0 {
		// Built, but unusable right now: say so instead of quietly
		// falling back to keywords.
		retrieval = semanticUnavailable
	}

	// Keyword score, three parts so a focused session isn't buried under
	// long ones that use the same words in passing:
	//   - counts with diminishing returns and length normalisation
	//     (BM25-style), not raw counts;
	//   - the best passage: where the most distinct query words appear
	//     together, which is also what the model is shown;
	//   - the user's own best-matching prompt, weighted up: the session
	//     where they asked for the work is where they described it.
	totalLen := 0
	for _, entry := range all {
		totalLen += len(entry.lower)
	}
	avgLen := 1.0
	if len(all) > 0 && totalLen > 0 {
		avgLen = float64(totalLen) / float64(len(all))
	}
	prompts := sessionPrompts()

	type scored struct {
		session Session
		kw      float64
		excerpt string
		prompts []string
		text    string
		lower   string
	}
	var items []scored
	maxKw := 0.0
	for _, entry := range all {
		item := scored{session: entry.session, text: entry.text, lower: entry.lower}
		length := float64(len(entry.lower))
		for i := range words {
			if tf := float64(entry.counts[i]); tf > 0 {
				item.kw += idf[i] * tf * (bm25K + 1) / (tf + bm25K*(1-bm25B+bm25B*length/avgLen))
			}
		}
		if item.kw > 0 {
			passage, excerpt := bestPassage(entry.text, entry.lower, words, idf)
			item.kw += passage
			item.excerpt = excerpt
		}
		promptScore, best := bestPrompts(prompts[entry.session.ID], words, idf)
		item.kw += promptWeight * promptScore
		item.prompts = best
		if item.kw > maxKw {
			maxKw = item.kw
		}
		items = append(items, item)
	}

	type finalScore struct {
		session Session
		score   float64
		excerpt string
		prompts []string
	}
	var ranked []finalScore
	for _, item := range items {
		score := item.kw
		excerpt := item.excerpt
		if sem != nil {
			kwNorm := 0.0
			if maxKw > 0 {
				kwNorm = item.kw / maxKw
			}
			score = semanticWeight*sem[item.session.ID] + keywordWeight*kwNorm
			// Show the model the passage that matched by meaning: the only
			// evidence for a session described in other words, and a
			// second look when the keyword passage is elsewhere.
			if at, ok := semPassage[item.session.ID]; ok && item.text != "" {
				passages := sessionPassages(item.session, item.text)
				if at < len(passages) {
					closest := strings.Join(strings.Fields(passages[at]), " ")
					if excerpt == "" {
						excerpt = truncate(closest, 300)
					} else if !strings.Contains(closest, strings.Trim(excerpt, "…")) {
						excerpt = excerpt + " | closest passage: " + truncate(closest, 240)
					}
				}
			}
			if excerpt == "" && item.text != "" {
				excerpt = headExcerpt(item.text)
			}
		}
		ranked = append(ranked, finalScore{session: item.session, score: score, excerpt: excerpt, prompts: item.prompts})
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
			Excerpt:  truncate(entry.excerpt, 560),
			Prompts:  entry.prompts,
		})
	}
	return out, rawHits, retrieval
}

// bestPassage finds the stretch of a transcript where the most distinct
// query words appear together (weighted by rarity), and returns its score
// and an excerpt from its middle. Passages are tried around occurrences of
// the rarest query word present, which names what the user remembers.
func bestPassage(text, lower string, words []string, idf []float64) (float64, string) {
	anchor, anchorIDF := -1, -1.0
	for i, word := range words {
		if idf[i] > anchorIDF && strings.Contains(lower, word) {
			anchor, anchorIDF = i, idf[i]
		}
	}
	if anchor < 0 {
		return 0, ""
	}
	best, bestStart := -1.0, 0
	from := 0
	for tries := 0; tries < maxAnchors; tries++ {
		idx := strings.Index(lower[from:], words[anchor])
		if idx < 0 {
			break
		}
		idx += from
		start := max(0, idx-passageChars/2)
		end := min(len(lower), start+passageChars)
		window := lower[start:end]
		score := 0.0
		for i, word := range words {
			if strings.Contains(window, word) {
				score += idf[i]
			}
		}
		if score > best {
			best, bestStart = score, start
		}
		from = idx + len(words[anchor])
	}
	end := min(len(text), bestStart+passageChars)
	return best, passageExcerpt(text[bestStart:end], lower[bestStart:end], words)
}

// passageExcerpt picks the line of a passage with the most distinct query
// words, trimmed around them.
func passageExcerpt(text, lower string, words []string) string {
	lines, lowers := strings.Split(text, "\n"), strings.Split(lower, "\n")
	bestLine, bestHits := 0, -1
	for i, line := range lowers {
		hits := 0
		for _, word := range words {
			if strings.Contains(line, word) {
				hits++
			}
		}
		if hits > bestHits {
			bestLine, bestHits = i, hits
		}
	}
	if bestLine >= len(lines) {
		return ""
	}
	line := strings.Join(strings.Fields(lines[bestLine]), " ")
	if len(line) <= 300 {
		return line
	}
	lowerLine := strings.ToLower(line)
	first := len(line)
	for _, word := range words {
		if i := strings.Index(lowerLine, word); i >= 0 && i < first {
			first = i
		}
	}
	start := max(0, min(first-60, len(line)-300))
	return "…" + line[start:min(len(line), start+300)] + "…"
}

// bestPrompts scores a session by the user's own prompt covering the most
// distinct query words, and returns up to two best-matching prompts for
// the model to see.
func bestPrompts(prompts []string, words []string, idf []float64) (float64, []string) {
	type scoredPrompt struct {
		score float64
		text  string
	}
	var matched []scoredPrompt
	seen := map[string]bool{}
	for _, prompt := range prompts {
		lower := strings.ToLower(prompt)
		if seen[lower] {
			continue // the same prompt sent twice
		}
		seen[lower] = true
		score := 0.0
		for i, word := range words {
			if strings.Contains(lower, word) {
				score += idf[i]
			}
		}
		if score > 0 {
			matched = append(matched, scoredPrompt{score, prompt})
		}
	}
	if len(matched) == 0 {
		return 0, nil
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].score > matched[j].score })
	var out []string
	for _, m := range matched[:min(2, len(matched))] {
		out = append(out, truncate(strings.Join(strings.Fields(m.text), " "), 240))
	}
	return matched[0].score, out
}

// sessionPrompts reads every agent home's prompt history into the user's
// prompts per session id.
func sessionPrompts() map[string][]string {
	prompts := map[string][]string{}
	var histories []string
	for _, home := range targets.ClaudeHomes() {
		histories = append(histories, filepath.Join(home.Dir, "history.jsonl"))
	}
	for _, home := range targets.CodexHomes() {
		histories = append(histories, filepath.Join(home.Dir, "history.jsonl"))
	}
	for _, path := range histories {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
		seen := map[string]bool{}
		for scanner.Scan() {
			var entry struct {
				Display   string `json:"display"`
				SessionID string `json:"sessionId"`
				CodexID   string `json:"session_id"`
				Text      string `json:"text"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			id, text := entry.SessionID, entry.Display
			if id == "" {
				id, text = entry.CodexID, entry.Text
			}
			// The same line can sit in two homes after a transfer.
			key := id + "\x00" + text
			if id == "" || text == "" || seen[key] {
				continue
			}
			seen[key] = true
			prompts[id] = append(prompts[id], text)
		}
		file.Close()
	}
	return prompts
}
