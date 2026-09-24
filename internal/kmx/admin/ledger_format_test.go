package admin

import (
	"bytes"
	"regexp"
	"testing"
)

// The `e2e-resilience` shard reads governance evidence out of `kmx ledger`
// with a regular expression, because the only row that proves an
// owner-managed application's call was metered is the one this table
// prints. That regular expression lives in a workflow file and this
// format string lives here, and nothing connected them: widening a column
// would have left the shard silently matching nothing, which on a `grep`
// is a failure and on a negative assertion is a pass for the wrong reason.
//
// The row below is the one the shard asserts, rendered by the real
// renderer. `local/qwen2.5:3b` is exactly the model column's width, so it
// is also the case where a one-character narrowing starts truncating the
// model name the shard names in full.
func TestOwnerApplicationLedgerRowMatchesTheShardPattern(t *testing.T) {
	var buf bytes.Buffer
	renderTable(&buf,
		[]string{"created (UTC)", "credential", "upstream", "model", "in", "out", "cents",
			"source", "status", "caller (claimed)", "from (observed)", "acted for"},
		[][]string{{"2026-09-20 10:00:00", "owner-ci", "orka", trunc("local/qwen2.5:3b", 16),
			"39", "5", "0", "unpriced", "200", "none", "10.244.0.9", "none"}},
		ledgerFmt)
	row := buf.String()
	// Exactly the pattern in .github/workflows/ci.yml's e2e-resilience job.
	shard := regexp.MustCompile(`owner-ci +orka +local/qwen2\.5:3b +[0-9]+ +[0-9]+ +0 +unpriced +200`)
	if !shard.MatchString(row) {
		t.Fatalf("the e2e-resilience ledger assertion no longer matches this table:\n%s", row)
	}
	// And the negative the shard depends on: `unpriced` is a metered
	// upstream with no configured price, and a `free` row would mean the
	// upstream had been reclassified. The assertion must not match one.
	buf.Reset()
	renderTable(&buf,
		[]string{"created (UTC)", "credential", "upstream", "model", "in", "out", "cents",
			"source", "status", "caller (claimed)", "from (observed)", "acted for"},
		[][]string{{"2026-09-20 10:00:00", "owner-ci", "orka", trunc("local/qwen2.5:3b", 16),
			"39", "5", "0", "free", "200", "none", "10.244.0.9", "none"}},
		ledgerFmt)
	if shard.MatchString(buf.String()) {
		t.Fatalf("a reclassified `free` row satisfies the shard's metered assertion:\n%s", buf.String())
	}
}
