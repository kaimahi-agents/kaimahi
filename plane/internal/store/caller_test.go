package store

// What a caller may put in an audit column, and what the plane says when
// the caller says nothing. The values here are hostile on purpose: the
// claim is the one field in a governed row that an attacker writes.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requestWith(userAgent, remote string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/upstream/erp/mcp", nil)
	r.Header.Del("User-Agent")
	if userAgent != "" {
		r.Header.Set("User-Agent", userAgent)
	}
	r.RemoteAddr = remote
	return r
}

func TestTheClaimIsMarkedAsTheCallersOwnWord(t *testing.T) {
	c := CallerOf(requestWith("curl/8.5.0", "10.244.1.7:41288"))
	assert.Equal(t, "ua:curl/8.5.0", c.Claim,
		"the prefix is stored, not stripped: a bare name would read as a fact the plane established")
	assert.Equal(t, "10.244.1.7", c.Addr, "the ephemeral source port says nothing and is dropped")
}

func TestACallerThatNamesItselfNothingSaysSo(t *testing.T) {
	c := CallerOf(requestWith("", "10.244.1.7:41288"))
	assert.Equal(t, CallerNone, c.Claim, "no identification offered is not the same as none recorded")
	assert.NotEqual(t, CallerUnrecorded, c.Claim)

	// Whitespace is not a name either.
	assert.Equal(t, CallerNone, CallerOf(requestWith("   \t  ", "10.0.0.1:1")).Claim)
}

func TestAHostileNameCannotForgeARowOrRunOffTheTable(t *testing.T) {
	hostile := "evil\n2026-09-08T00:00:00 ap-agent   erp   tools/call payment_schedule allowed 200" +
		strings.Repeat(` "quoted" `, 900)
	c := CallerOf(requestWith(hostile, "10.244.1.7:41288"))

	assert.NotContains(t, c.Claim, "\n", "a newline would render as a second line, which reads as a row nobody wrote")
	assert.NotContains(t, c.Claim, "\r")
	assert.LessOrEqual(t, len(c.Claim), MaxCallerClaim,
		"the bound is the column's, and it is applied at the write")
	assert.True(t, strings.HasPrefix(c.Claim, CallerClaimPrefix))
	assert.True(t, strings.HasSuffix(c.Claim, "…"), "a clipped value says it was clipped")
	assert.Equal(t, c.Claim, string([]rune(c.Claim)), "and it is still valid UTF-8")
}

func TestAnUnreadableAddressIsUnknownNotEmpty(t *testing.T) {
	assert.Equal(t, CallerAddrUnknown, CallerOf(requestWith("curl/8", "")).Addr)
	// A listener that reports no port at all still yields what it saw.
	assert.Equal(t, "/run/plane.sock", CallerOf(requestWith("curl/8", "/run/plane.sock")).Addr)
}

func TestTheStoreWritesNothingTheColumnConstraintWouldReject(t *testing.T) {
	// The CHECK constraints in migration 00011 must be unreachable by any
	// writer: a rejected INSERT trips the seam's fail-closed degradation,
	// so a legibility field would become a way to stop governed traffic.
	for _, in := range []string{
		"", "none", "unrecorded", "legacy",
		"ua:curl/8.5.0",
		"curl/8.5.0",                      // a seam that forgot the prefix
		strings.Repeat("A", 4000),         // unbounded
		"ua:" + strings.Repeat("B", 4000), // unbounded, prefixed
		"ua:line\nbreak",                  // one line, whatever arrives
	} {
		got := callerClaimFor(in)
		require.LessOrEqual(t, len(got), MaxCallerClaim, "claim %q", in)
		require.NotContains(t, got, "\n")
		ok := got == CallerNone || got == CallerUnrecorded || got == CallerLegacy ||
			strings.HasPrefix(got, CallerClaimPrefix)
		require.True(t, ok, "claim %q became %q, which the column would reject", in, got)
	}

	for _, in := range []string{"", "unknown", "unrecorded", "legacy", "10.244.1.7",
		"fe80::1ff:fe23:4567:890a%eth0", strings.Repeat("9", 500)} {
		got := callerAddrFor(in)
		require.LessOrEqual(t, len(got), MaxCallerAddr, "addr %q", in)
		require.NotContains(t, got, "\n")
	}
}

func TestAWriterThatResolvedNothingSaysNoRecordNotNothing(t *testing.T) {
	assert.Equal(t, CallerUnrecorded, callerClaimFor(""))
	assert.Equal(t, CallerUnrecorded, callerAddrFor(""))
	assert.NotEqual(t, callerClaimFor(""), CallerNone,
		"'no record' and 'the caller offered nothing' are different answers and stay different")
}

func TestOrdinaryAuditTextIsBoundedAndOneLined(t *testing.T) {
	// A tool name and a method come out of caller-controlled JSON and are
	// printed unescaped into the same fixed-width tables.
	forged := "k8s_get_resources\n2026-09-08T00:00:00 ap-agent erp tools/call anything allowed 200"
	got := auditText(forged)
	assert.NotContains(t, got, "\n")
	assert.LessOrEqual(t, len(auditText(strings.Repeat("x", 10_000))), maxAuditText)
	// A real tool name is untouched.
	assert.Equal(t, "k8s_get_resources", auditText("k8s_get_resources"))
}
