package browse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/rexovas/session-protect/internal/targets"
)

// Stats mirrors the agent's own usage-statistics cache. The file is internal
// to the agent and undocumented, so parsing is defensive and absence is fine.
type Stats struct {
	LastComputed  string
	TotalSessions int
	Models        []ModelStats
	Daily         []DayStats
}

type ModelStats struct {
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	CostUSD    float64
}

type DayStats struct {
	Date      string
	Messages  int
	Sessions  int
	ToolCalls int
}

// LoadStats reads the claude stats cache; a missing or unparsable file
// returns nil.
func LoadStats() *Stats {
	path := filepath.Join(targets.DetectClaude().Source, "stats-cache.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		LastComputedDate string `json:"lastComputedDate"`
		TotalSessions    int    `json:"totalSessions"`
		ModelUsage       map[string]struct {
			InputTokens              int64   `json:"inputTokens"`
			OutputTokens             int64   `json:"outputTokens"`
			CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
			CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
			CostUSD                  float64 `json:"costUSD"`
		} `json:"modelUsage"`
		DailyActivity []struct {
			Date         string `json:"date"`
			MessageCount int    `json:"messageCount"`
			SessionCount int    `json:"sessionCount"`
			ToolCall     int    `json:"toolCallCount"`
		} `json:"dailyActivity"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}

	stats := &Stats{LastComputed: raw.LastComputedDate, TotalSessions: raw.TotalSessions}
	for model, usage := range raw.ModelUsage {
		stats.Models = append(stats.Models, ModelStats{
			Model:      model,
			Input:      usage.InputTokens,
			Output:     usage.OutputTokens,
			CacheRead:  usage.CacheReadInputTokens,
			CacheWrite: usage.CacheCreationInputTokens,
			CostUSD:    usage.CostUSD,
		})
	}
	sort.Slice(stats.Models, func(i, j int) bool {
		return stats.Models[i].Output > stats.Models[j].Output
	})
	for _, day := range raw.DailyActivity {
		stats.Daily = append(stats.Daily, DayStats{
			Date: day.Date, Messages: day.MessageCount,
			Sessions: day.SessionCount, ToolCalls: day.ToolCall,
		})
	}
	sort.Slice(stats.Daily, func(i, j int) bool { return stats.Daily[i].Date > stats.Daily[j].Date })
	return stats
}
