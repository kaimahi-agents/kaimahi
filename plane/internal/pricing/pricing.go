// Package pricing turns token usage into ledger cents. Ported from
// tomte-old's llm/pricing.go cost math; the bundled price table was NOT
// ported — an invented price is never acceptable, so every price row is
// operator-configured (see internal/config), and a model without a row is
// "unpriced", which the proxy's priced-pair gate fails closed on when a
// cents budget is in force.
package pricing

// Price is USD cents per 1,000,000 tokens as fixed-point integers:
// 75 means $0.75 per 1M.
type Price struct {
	InCentsPer1M  int `json:"in_cents_per_1m"`
	OutCentsPer1M int `json:"out_cents_per_1m"`
}

// MaxTokens bounds each token count in the cost math. Usage numbers come
// from an upstream's response body, so they are input, not truth: with
// prices capped at config's 1e6 cents/1M, clamping counts to 1e12 keeps
// every numerator term at or below 1e18 — no int64 overflow — while
// staying far beyond anything a real response can carry. Negative counts
// clamp to zero.
const MaxTokens = 1_000_000_000_000

func clampTokens(n int64) int64 {
	switch {
	case n < 0:
		return 0
	case n > MaxTokens:
		return MaxTokens
	}
	return n
}

// CostCents returns the whole-cent (floored) cost of the given usage.
// Math ported from tomte-old: sum the input and output numerators before
// dividing, so we floor once on the combined total instead of flooring
// input and output separately (which can throw away up to 2 cents per
// call).
func CostCents(p Price, inputTokens, outputTokens int64) int64 {
	numerator := clampTokens(inputTokens)*int64(p.InCentsPer1M) +
		clampTokens(outputTokens)*int64(p.OutCentsPer1M)
	return numerator / 1_000_000
}
