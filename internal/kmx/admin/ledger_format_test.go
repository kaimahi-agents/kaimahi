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
// ownerRow renders one owner-application ledger row through the real
// renderer. Only the three cells the shards vary are parameters; every
// other cell is the value the plane writes for a direct, unattributed
// call from the owner's Deployment.
func ownerRow(source, status, actedFor string) string {
	var buf bytes.Buffer
	renderTable(&buf,
		[]string{"created (UTC)", "credential", "upstream", "model", "in", "out", "cents",
			"source", "status", "caller (claimed)", "from (observed)", "acted for"},
		[][]string{{"2026-09-20 10:00:00", "owner-ci", "orka", trunc("local/qwen2.5:3b", 16),
			"39", "5", "0", source, status, "none", "10.244.0.9", actedFor}},
		ledgerFmt)
	return buf.String()
}

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

// The `e2e-spend` shard reads three further verdicts off this same table,
// and two of them are END-ANCHORED on the `acted for` column: a call the
// owner's Deployment makes for nobody is attributed `none`, and that is a
// complete answer rather than a missing one. An anchored pattern is the
// fragile kind — a column appended after `acted for`, or a trailing space
// introduced anywhere, stops it matching, and a `grep` that matches
// nothing fails the shard while the assertion it was protecting quietly
// stopped being made.
//
// Each case below is the exact expression in .github/workflows/ci.yml's
// `e2e-spend` job, run against a row this renderer produced.
//
// `grep` matches a LINE, so the patterns are compiled with `(?m)`: in Go
// a bare `$` means end of text, and a table always ends in a newline, so
// an end-anchored expression copied in verbatim would match nothing here
// while matching perfectly in the shard — a pin that passes for the wrong
// reason is worse than no pin.
func grepLine(pattern string) *regexp.Regexp { return regexp.MustCompile("(?m)" + pattern) }

func TestSpendShardLedgerAssertionsMatchTheTable(t *testing.T) {
	for _, c := range []struct {
		name    string
		row     string
		pattern string
	}{
		{"a governed turn, attributed to nobody",
			ownerRow("unpriced", "200", "none"),
			`owner-ci +orka +local/qwen2\.5:3b +[0-9]+ +[0-9]+ +0 +unpriced +200 .*none *$`},
		{"the refusal an expired credential earns",
			ownerRow("denied", "403", "none"),
			`owner-ci +orka .*denied +403 .*none *$`},
		{"the refusal an exhausted budget earns",
			ownerRow("denied", "429", "none"),
			`denied +429`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !grepLine(c.pattern).MatchString(c.row) {
				t.Fatalf("the e2e-spend assertion %s no longer matches this table:\n%s", c.pattern, c.row)
			}
		})
	}

	// The negative the shard's attribution check depends on. 'none' means
	// there is no person behind the call; 'unknown' means the plane LOST
	// the attribution and 'legacy' means the row predates it. On a cluster
	// the migration created neither can occur, so the shard fails closed on
	// them — which only works while they are distinguishable here.
	lost := grepLine(` (unknown|legacy) *$`)
	for _, word := range []string{"unknown", "legacy"} {
		if !lost.MatchString(ownerRow("unpriced", "200", word)) {
			t.Fatalf("a %q row no longer trips the shard's lost-attribution check:\n%s",
				word, ownerRow("unpriced", "200", word))
		}
	}
	if lost.MatchString(ownerRow("unpriced", "200", "none")) {
		t.Fatal("a complete `none` attribution reads as a lost one")
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
	// `caller` is pinned to the ACTUAL values curl and the port-forwarded
	// loopback produce, not just to some text sitting before `none`: a
	// row whose caller pair silently regressed to one of the closed
	// vocabulary's non-claim words must not satisfy it.
	metered := regexp.MustCompile(`model-ci +ollama +qwen2\.5:3b +[0-9]+ +[0-9]+ +0 +free +200`)
	caller := regexp.MustCompile(`(?m)model-ci +ollama .*ua:curl/[0-9].*127\.0\.0\.1 +none *$`)
	unrecorded := regexp.MustCompile(` (legacy|unrecorded|unknown) +(legacy|unrecorded|unknown) +`)

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
	// fail. A row the plane could not attribute must trip the alarm AND
	// fail the strengthened positive, for every non-claim word the closed
	// vocabulary allows in these columns (caller.go: none/unrecorded/legacy
	// for the claim, unknown/unrecorded/legacy for the observed address) —
	// not only the `unrecorded` pairing the old, weaker pin exercised. An
	// `unpriced` row must not pass for the free upstream's either.
	for _, tc := range []struct{ claim, addr string }{
		{"legacy", "legacy"},
		{"unrecorded", "unrecorded"},
		{"unrecorded", "unknown"},
	} {
		lost := row("free", tc.claim, tc.addr)
		if !unrecorded.MatchString(lost) {
			t.Fatalf("a row that lost its caller fields (%s/%s) no longer trips the shard's alarm:\n%s", tc.claim, tc.addr, lost)
		}
		if caller.MatchString(lost) {
			t.Fatalf("a row without a real caller claim (%s/%s) satisfies the strengthened caller assertion:\n%s", tc.claim, tc.addr, lost)
		}
	}
	if metered.MatchString(row("unpriced", "ua:curl/8.5.0", "127.0.0.1")) {
		t.Fatal("an `unpriced` row satisfies the shard's free-upstream assertion")
	}
}
