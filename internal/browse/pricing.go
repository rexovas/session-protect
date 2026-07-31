package browse

import "strings"

// modelPrice is USD per million tokens. This is a static local table — cost
// estimation never calls any external service. Cache reads bill at 0.1x the
// input rate and cache writes at 1.25x (5-minute TTL), per Anthropic's
// published pricing. Prices as of 2026-07; unknown models show tokens
// without a cost estimate.
type modelPrice struct {
	Input  float64
	Output float64
}

var prices = map[string]modelPrice{
	"claude-fable-5":    {10, 50},
	"claude-mythos-5":   {10, 50},
	"claude-opus-4-8":   {5, 25},
	"claude-opus-4-7":   {5, 25},
	"claude-opus-4-6":   {5, 25},
	"claude-opus-4-5":   {5, 25},
	"claude-sonnet-4-6": {3, 15},
	"claude-sonnet-4-5": {3, 15},
	"claude-haiku-4-5":  {1, 5},
}

// priceFor matches a model id against the table, tolerating date-suffixed ids
// like claude-haiku-4-5-20251001 and bracketed variants.
func priceFor(model string) (modelPrice, bool) {
	if price, ok := prices[model]; ok {
		return price, true
	}
	for id, price := range prices {
		if strings.HasPrefix(model, id) {
			return price, true
		}
	}
	return modelPrice{}, false
}

// costUSD estimates the cost of the given usage under a model's pricing.
func costUSD(model string, t TokenTotals) (float64, bool) {
	price, ok := priceFor(model)
	if !ok {
		return 0, false
	}
	const mtok = 1_000_000
	return float64(t.Input)/mtok*price.Input +
		float64(t.Output)/mtok*price.Output +
		float64(t.CacheRead)/mtok*price.Input*0.1 +
		float64(t.CacheWrite)/mtok*price.Input*1.25, true
}
