package store

// Who called: what a governed row records about the client on the other
// end of the socket, and how much of it is worth anything.
//
// The gap this closes. A row named the credential and, since identity.go,
// who the call was acted for. Nothing in it distinguished an agent the
// plane deployed from a shell script holding the same token, so a reader
// meeting 'none' — "there is no person" — had no way to see that the call
// came through a door the plane never opened, and no way to read the word
// in context. That invisibility is why the overclaim went unnoticed.
//
// This is legibility, not a control. Neither value below is an input to
// any decision: nothing here admits, denies, grants or attributes
// anything, and a seam's fail-closed rules are untouched by it.
//
// The two facts are kept in separate columns because they are worth
// different amounts, and merging them would repeat the mistake that made
// this lane necessary — a field that looks authoritative and is not is
// worse than no field.

import (
	"net"
	"net/http"
	"strings"
)

// The closed vocabulary of a caller. Anything that is not one of these
// words is a recorded value, and a recorded CLAIM always carries the
// 'ua:' prefix, so no caller-supplied string can impersonate one of them.
const (
	// CallerClaimPrefix marks the part of the value the caller wrote. It
	// is stored, not stripped: a bare user agent in the column would read
	// as a fact the plane established.
	CallerClaimPrefix = "ua:"
	// CallerNone: the caller offered no identification at all — no User-Agent
	// header on the request. A complete answer about what was offered, and
	// still not a claim about who the caller is.
	CallerNone = "none"
	// CallerAddrUnknown: the plane could not read the peer address of the
	// connection. It cannot say where the call came from.
	CallerAddrUnknown = "unknown"
	// CallerUnrecorded: the writer resolved nothing. The column default
	// from migration 00011 on, so a writer that forgets says "no record"
	// rather than making a claim.
	CallerUnrecorded = "unrecorded"
	// CallerLegacy: the row was written before the caller was recorded.
	// Backfill only; migration 00011 closed the class.
	CallerLegacy = "legacy"
)

// Caller is what a seam stamps on the rows it writes about one request.
//
// Claim is SELF-REPORTED and unverified. It is the User-Agent header,
// which any client can set to any string — including one that imitates
// another client. Treat it as evidence of what the caller wanted to be
// called, never as identification.
//
// Addr is what the plane OBSERVED at its own socket: the peer address the
// request arrived from. It is not a claim by the thing being governed,
// which is the same standard identity.go holds attribution to. It is
// still only an address — in a cluster it names a pod, not a person, and
// pod addresses are reused.
type Caller struct {
	Claim string
	Addr  string
}

// CallerOf reads the caller off one request.
//
// The MCP handshake's own `clientInfo.name` is deliberately NOT used, and
// the reason is worth keeping: the gateway holds no session state, and a
// client can skip `initialize` entirely and still be governed
// (docs/foreign-runtime.md). Carrying a handshake name onto the rows that
// matter would mean keeping per-session state the gateway refuses to keep,
// to gain a value of exactly the same worth as the header — both are the
// caller's own word for itself. The header is on every message; the
// handshake is on one.
//
// X-Forwarded-For and friends are ignored on purpose. They are headers,
// which puts them in the same class as the claim, and reading one here
// would let a caller choose the value in the column that is supposed to
// be the one it cannot choose.
func CallerOf(r *http.Request) Caller {
	return Caller{Claim: callerClaim(r.Header.Get("User-Agent")), Addr: callerAddr(r.RemoteAddr)}
}

// callerClaim bounds and one-lines what the caller called itself. The
// prefix is applied after the bound so a hostile value can never push it
// off the end.
func callerClaim(ua string) string {
	cleaned := strings.TrimSpace(OneLine(ua))
	if cleaned == "" {
		return CallerNone
	}
	return CallerClaimPrefix + Clip(cleaned, MaxCallerClaim-len(CallerClaimPrefix))
}

// callerAddr keeps the host and drops the ephemeral source port, which
// says nothing and changes on every connection.
func callerAddr(remote string) string {
	if remote == "" {
		return CallerAddrUnknown
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote // a listener that reports no port (a unix socket)
	}
	host = strings.TrimSpace(OneLine(host))
	if host == "" {
		return CallerAddrUnknown
	}
	return Clip(host, MaxCallerAddr)
}

// callerClaimFor and callerAddrFor are what the store WRITES, whatever a
// seam handed it. They re-apply the prefix and the bound rather than
// trusting them, so the CHECK constraints migration 00011 adds cannot be
// reached by any writer: a rejected INSERT here would trip the seam's
// fail-closed degradation and turn a legibility field into a way to stop
// governed traffic. Empty is never written as a claim — a writer that
// resolved no caller gets "no record", which is what the column default
// says too.
func callerClaimFor(v string) string {
	switch v {
	case "":
		return CallerUnrecorded
	case CallerNone, CallerUnrecorded, CallerLegacy:
		return v
	}
	claim := OneLine(strings.TrimPrefix(v, CallerClaimPrefix))
	return CallerClaimPrefix + Clip(claim, MaxCallerClaim-len(CallerClaimPrefix))
}

func callerAddrFor(v string) string {
	switch v {
	case "":
		return CallerUnrecorded
	case CallerAddrUnknown, CallerUnrecorded, CallerLegacy:
		return v
	}
	return Clip(OneLine(v), MaxCallerAddr)
}
