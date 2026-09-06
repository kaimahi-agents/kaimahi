package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// How kmx learns what the plane on the other end of the forward can do.
//
// The failure this replaces: a newer kmx sent a request an older plane had
// never heard of, and reported the plane's own reply — "404 page not found".
// It failed closed, which was right, and it named neither the problem nor
// the fix. One endpoint had been patched to notice a missing response field
// and say "the plane is older than this kmx", which works for that field and
// says nothing about the other fifteen routes.
//
// Three ways to learn a version were available. A field on every existing
// response was rejected: every handler would have to carry it, and the field
// itself becomes the thing that can be missing — the same failure, moved.
// Inferring from a 404 was rejected as the ONLY mechanism: a 404 says a
// route is absent, never which version you have, so it can produce a
// warning but never a message an operator can act on. What is used is an
// endpoint the plane serves, with the 404 kept as its floor: a plane too old
// to answer is contract 0, and that is a fact rather than a guess, because
// the handshake runs only after /healthz answered on a forward kmx has
// already proved is its own.
//
// The compatibility policy, which is the part meant to outlive the fix:
//
//   - **Per operation, never global.** kmx refuses only what this plane
//     cannot serve, and refuses it BEFORE sending it. Everything else works
//     normally. A blanket refusal on any skew would strand a working cluster
//     over one unreachable feature; a blanket warning is the 404 again with
//     extra words in front of it.
//   - **A plane NEWER than kmx proceeds**, with one line saying so. The
//     state lives in the plane — the ledger, the grants, the approvals — and
//     stranding it because the CLI is behind would be the more expensive
//     mistake. This is safe because the plane promises it: within a major
//     version the admin surface only grows, so every route this kmx knows is
//     still there and still shaped the same way.
//   - **A plane OLDER than kmx proceeds too**, for everything it can serve.
//     Only the operations above its contract are refused, and the refusal
//     names both versions and the one command that fixes it.
const (
	// ContractUnreported is what a plane that does not serve /admin/version
	// is: everything up to and including v0.1.0.
	ContractUnreported = 0

	// ContractTableDeclared is the first contract a plane reports, and the
	// first that guarantees /admin/config/validate returns the merged
	// upstream table's policy-relevant fields.
	ContractTableDeclared = 1

	// Speaks is the highest contract this kmx knows about. A plane reporting
	// more than this is newer than kmx, which is allowed.
	Speaks = ContractTableDeclared
)

// PlaneVersion is what the handshake learned.
type PlaneVersion struct {
	// Version is the plane's own version string, empty when it served none.
	Version string
	// Contract is the admin surface it reports, or ContractUnreported.
	Contract int
	// Reported distinguishes a plane that answered from one that 404'd.
	// Without it, "contract 0" would read as a plane claiming 0 rather than
	// a plane that could not be asked.
	Reported bool
}

// Describe names the plane the way a refusal has to: an operator comparing
// two clusters needs the version string, and an operator reading the reason
// needs the number the decision was actually made on.
func (p PlaneVersion) Describe() string {
	if !p.Reported {
		return "a plane too old to report its version (v0.1.0 or earlier; admin contract 0)"
	}
	return fmt.Sprintf("plane %s (admin contract %d)", p.Version, p.Contract)
}

// handshake asks the plane what it is, once per session.
//
// A 404 is the one non-200 that is an ANSWER rather than a failure, and it is
// only trustworthy here: Open has already seen /healthz return 200 on a
// forward it proved is its own, so an absent route means the route is absent
// and not that we are talking to something else. Every other non-200 is a
// real failure and is returned as one — a 401 is a bad admin token and must
// never be silently read as "old plane".
func (c *Client) handshake() error {
	status, body, err := c.Do(http.MethodGet, "/admin/version", nil)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusNotFound:
		c.plane = PlaneVersion{Contract: ContractUnreported}
		return nil
	case status != http.StatusOK:
		return fmt.Errorf("the plane would not say what version it is (HTTP %d): %s",
			status, strings.TrimSpace(string(body)))
	}
	var doc struct {
		Version       string `json:"version"`
		AdminContract int    `json:"admin_contract"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("the plane's version reply could not be read: %w", err)
	}
	if doc.AdminContract < ContractTableDeclared {
		// A plane that serves the route must report a usable contract.
		// Reading a 0 here as "unreported" would let a broken plane look
		// like an old one and get the wrong advice.
		return fmt.Errorf("the plane reported admin contract %d, which no released plane serves — "+
			"this is a plane bug, not a version gap", doc.AdminContract)
	}
	c.plane = PlaneVersion{Version: doc.Version, Contract: doc.AdminContract, Reported: true}
	return nil
}

// Plane returns what the handshake learned.
func (c *Client) Plane() PlaneVersion { return c.plane }

// Require refuses an operation this plane cannot serve, before it is sent.
//
// what is the thing the operator asked for, in their words, because the
// message has to say what did not happen and not which route was skipped.
func (c *Client) Require(contract int, what string) error {
	if c.plane.Contract >= contract {
		return nil
	}
	return fmt.Errorf("this plane is too old to %s — nothing has been applied.\n"+
		"  plane: %s\n"+
		"  kmx:   %s, and this needs admin contract %d\n"+
		"  Upgrade the plane with the kmx you are already running:\n"+
		"    kmx plane",
		what, c.plane.Describe(), c.kmxVersion, contract)
}

// SkewNote is the one line a session prints about a plane newer than this
// kmx, or "" when there is nothing to say.
//
// It is a note and not a refusal on purpose: the plane holds the state, its
// surface only grows, and refusing here would strand a working cluster
// because the CLI is behind.
func (c *Client) SkewNote() string {
	if c.plane.Contract <= Speaks {
		return ""
	}
	return fmt.Sprintf("note: this plane is newer than kmx (%s; kmx %s speaks admin contract %d).\n"+
		"  Everything kmx knows about still works — the plane's admin surface only grows.\n"+
		"  Upgrade kmx to reach what was added: go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest",
		c.plane.Describe(), c.kmxVersion, Speaks)
}
