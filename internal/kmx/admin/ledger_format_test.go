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

// The same tie, for the other shard that reads governance evidence out of
// this table. `e2e-models` no longer produces its rows through an agent
// client; it calls the TLS seam directly under an explicitly namespaced
// credential, and the row below is what it greps for. Two columns it
// depends on are not in the resilience row at all: `free`, which is the
// committed `ollama` upstream's own classification, and the caller pair
// the shard asserts was recorded rather than left blank.
func TestModelShardLedgerRowMatchesTheShardPatterns(t *testing.T) {
	headers := []string{"created (UTC)", "credential", "upstream", "model", "in", "out", "cents",
		"source", "status", "caller (claimed)", "from (observed)", "acted for"}
	row := func(source, claim, addr string) string {
		var buf bytes.Buffer
		renderTable(&buf, headers,
			[][]string{{"2026-09-20 10:00:00", "model-ci", "ollama", trunc("qwen2.5:3b", 16),
				"11", "3", "0", source, "200", claim, addr, "none"}},
			ledgerFmt)
		return buf.String()
	}

	// Exactly the patterns in .github/workflows/ci.yml's e2e-models job.
	metered := regexp.MustCompile(`model-ci +ollama +qwen2\.5:3b +[0-9]+ +[0-9]+ +0 +free +200`)
	caller := regexp.MustCompile(`(?m)model-ci +ollama .*none *$`)
	unrecorded := regexp.MustCompile(` (legacy|unrecorded) +(legacy|unrecorded) +`)

	recorded := row("free", "ua:curl/8.5.0", "127.0.0.1")
	if !metered.MatchString(recorded) {
		t.Fatalf("the e2e-models ledger assertion no longer matches this table:\n%s", recorded)
	}
	if !caller.MatchString(recorded) {
		t.Fatalf("the e2e-models caller assertion no longer matches this table:\n%s", recorded)
	}
	if unrecorded.MatchString(recorded) {
		t.Fatalf("a row WITH caller fields satisfies the shard's lost-caller alarm:\n%s", recorded)
	}

	// The negatives, both of which the shard depends on being able to
	// fail. A row the plane could not attribute must trip the alarm, and
	// an `unpriced` row must not pass for the free upstream's.
	if !unrecorded.MatchString(row("free", "unrecorded", "unrecorded")) {
		t.Fatal("a row that lost its caller fields no longer trips the shard's alarm")
	}
	if metered.MatchString(row("unpriced", "ua:curl/8.5.0", "127.0.0.1")) {
		t.Fatal("an `unpriced` row satisfies the shard's free-upstream assertion")
	}
}
