package store

// What may enter a free-text audit column.
//
// Every string a governed row carries — a tool name, a method, an
// upstream name, a refusal detail, a model, the caller's own name —
// arrives from outside the plane, and all of it is printed into
// fixed-width tables that operators read and CI greps (`make tool-audit`,
// `kmx audit tool`, `kmx flow`).
//
// The upstream name is worth spelling out because it looks safe and is
// not: it is a URL path segment, and Go's mux UNESCAPES path values, so
// `/upstream/foo%0A…/mcp` arrives as a name with a real newline in it.
// The unknown-upstream refusal is audited before any table lookup, so
// that name reaches a row.
// An unbounded value pushes every column after it out of line; a value
// with a newline in it renders as a SECOND LINE, which reads as a second
// audit row that nobody wrote.
//
// So the bound lives here, at the store, and every write to the three
// trails goes through it — the spend ledger, the tool audit, and the
// approvals trail. Bounding at the seam would
// leave it to be remembered once per caller; this way an audit column
// cannot be written unbounded at all. It is not redaction —
// internal/redact scrubs known secret values out of LOGS and is a
// different job.
//
// The renderers apply their own one-line rule on the way out, because
// they print rows this writer did not write: an older plane's, a
// restored dump's. Go's unicode.IsPrint and Python's str.isprintable
// are the same definition against different Unicode releases, so they
// disagree only on characters one of them has not heard of yet — never
// on a control character, which is the part that matters here.

import (
	"strconv"
	"strings"
	"unicode"
)

const (
	// maxAuditText bounds one ordinary audit cell.
	maxAuditText = 240
	// MaxCallerClaim bounds the caller's self-reported name, matching the
	// CHECK constraint migration 00011 puts on the column. Generous for a
	// real user agent and far short of what a hostile one would send.
	MaxCallerClaim = 160
	// MaxCallerAddr bounds an observed peer address: an IPv6 literal with
	// a zone is the longest honest value.
	MaxCallerAddr = 64
)

// OneLine reduces s to a single printable line. Tabs, newlines and
// carriage returns become spaces rather than disappearing, so text that
// was deliberately spread over lines still reads as separate words;
// everything else unprintable is dropped.
func OneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
}

// Clip bounds s to n BYTES, cutting on rune boundaries and counting the
// ellipsis against the bound. Slicing by byte index would cut a
// multibyte rune in half — these values land in audit rows and
// approval requests, neither of which should carry
// invalid UTF-8 — and would also overrun n, since "…" is three bytes.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	// The ellipsis is three bytes, so below that there is nothing to say
	// but nothing. Guarded rather than assumed: no bound here is that
	// small today, and a column that gained one would otherwise get a
	// value LONGER than its own limit.
	if n < len(ellipsis) {
		return ""
	}
	// One forward pass over the rune boundaries, keeping the last one that
	// still leaves room for the ellipsis. Shrinking a rune slice from the
	// end and re-encoding it to measure was quadratic, and the input here
	// is chosen by the caller — a header of a few kilobytes clipped to a
	// couple of hundred bytes did millions of byte copies on the audit
	// write path.
	cut := 0
	for i := range s {
		if i+len(ellipsis) > n {
			break
		}
		cut = i
	}
	return s[:cut] + ellipsis
}

// auditText is what an ordinary free-text column gets on the way in.
//
// DESCRIPTIVE COLUMNS ONLY. Never a column anything filters, matches or
// deduplicates on — a tool audit's `tool` and `detail`, yes; an approval
// request's `subject`, no. A grant is read back out of
// `approval_request.subject` and matched against the raw tool name the
// gateway hands `ConsumeToolGrant`, so altering it on the way in would
// mint grants that can never be consumed. Those columns stay exactly
// as they arrived, and the renderers keep them from breaking a table.
//
// A clean value — already one printable line, no leading or trailing
// space — is stored as it arrived. Anything else is stored in Go's
// quoted form, which is one printable line by construction.
//
// Quoting rather than stripping, because stripping CREATES A COLLISION
// and that is worse than the mess it tidies. The upstream name arrives
// as an unescaped URL path segment, and the unknown-upstream refusal is
// audited before any table lookup, so a caller can choose it: mapping
// `openai\t` to a space and padding it into a fixed-width column renders
// exactly like the real `openai`, and the trail then shows what reads as
// a denial against a real upstream that was never asked for. `"openai\t"`
// cannot be misread as anything, and it says what actually arrived.
func auditText(s string) string {
	if isCleanLine(s) {
		return Clip(s, maxAuditText)
	}
	return Clip(strconv.Quote(s), maxAuditText)
}

// isCleanLine reports whether s can be written to an audit column
// unaltered: printable throughout, and not padded at either end.
func isCleanLine(s string) bool {
	if s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
