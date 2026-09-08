package store

// What may enter a free-text audit column.
//
// Every string a governed row carries — a tool name, a method, a refusal
// detail, a model, the caller's own name — arrives from outside the
// plane, and all of it is printed into fixed-width tables that operators
// read and CI greps (`make tool-audit`, `kmx audit tool`, `kmx flow`).
// An unbounded value pushes every column after it out of line; a value
// with a newline in it renders as a SECOND LINE, which reads as a second
// audit row that nobody wrote.
//
// So the bound lives here, at the store, and every write goes through
// it. Bounding at the seam would leave it to be remembered once per
// caller; this way an audit column cannot be written unbounded at all.
// It is not redaction — internal/redact scrubs known secret values out
// of LOGS and is a different job.

import (
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
// multibyte rune in half — these values land in an audit row, an
// approval request and a Slack message, none of which should carry
// invalid UTF-8 — and would also overrun n, since "…" is three bytes.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	r := []rune(s)
	for len(r) > 0 && len(string(r))+len(ellipsis) > n {
		r = r[:len(r)-1]
	}
	return string(r) + ellipsis
}

// auditText is what an ordinary free-text column gets on the way in.
func auditText(s string) string { return Clip(OneLine(s), maxAuditText) }
