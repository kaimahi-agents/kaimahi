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
//   - **Per operation, never global.** Require checks the introduction
//     revision of a surviving capability before sending the operation. It
//     does not prove that an arbitrary route survives a later retirement.
//   - **A plane NEWER than kmx proceeds**, with a compatibility warning.
//     Refusing every operation would strand usable state because the CLI is
//     behind. Proceeding is not a guarantee: contract 3 deliberately retires
//     inbound, breaking the former grow-only promise. Older clients still
//     accept higher numbers, so the marker cannot protect their calls to
//     retired routes; those clients need a matching CLI upgrade.
//   - **A plane OLDER than kmx proceeds too**, for surviving capabilities
//     introduced at or below its contract. Higher capability requirements
//     are refused with both versions and the command that upgrades the plane.
const (
	// ContractUnreported is what a plane that does not serve /admin/version
	// is: everything up to and including v0.1.0.
	ContractUnreported = 0

	// ContractTableDeclared is the first contract a plane reports, and the
	// first that guarantees /admin/config/validate returns the merged
	// upstream table's policy-relevant fields.
	ContractTableDeclared = 1

	// ContractModelOverlay is the first contract whose overlay accepts an
	// `upstreams` block — the MODEL seam — and whose
	// /admin/config/validate echoes each model upstream with the protocol
	// the plane resolved for it. An older plane refuses the fragment
	// outright, which is true of that plane and reads like an operator
	// error, so `kmx models add` requires this before it sends.
	ContractModelOverlay = 2

	// ContractInboundRetired removes /admin/inbound-audit and inbound
	// approval requests. It records a retirement, not a higher requirement
	// for the surviving model, tool or approval operations.
	ContractInboundRetired = 3

	// ContractGatewayRetired removes tool allowlist/audit APIs and tool
	// requests. Budget approvals and model overlay capabilities survive.
	ContractGatewayRetired = 4

	// ContractApprovalsRetired removes approval requests, grants and their
	// audit APIs. Ordinary model budgets and credential APIs remain.
	ContractApprovalsRetired = 5

	// Speaks is the highest contract revision this kmx knows about. A plane
	// reporting more is allowed, but compatibility is not guaranteed.
	Speaks = ContractApprovalsRetired
)

// PlaneVersion is what the handshake learned.
type PlaneVersion struct {
	// Version is the plane's own version string, empty when it served none.
	Version string
	// Contract is the admin revision it reports, or ContractUnreported.
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
	// A plane that serves this route must give a usable answer to BOTH
	// halves, and a half-answer is a fault rather than an old plane. Reading
	// a 0 contract as "unreported" would let a broken plane look old and get
	// advice that cannot fix it; accepting a blank version would put an empty
	// string where every skew message names the plane, so an operator
	// comparing two clusters would be shown nothing and told it was an
	// answer. Reported stays false until both pass.
	if doc.AdminContract < ContractTableDeclared {
		return fmt.Errorf("the plane reported admin contract %d, which no released plane serves — "+
			"this is a plane bug, not a version gap", doc.AdminContract)
	}
	if strings.TrimSpace(doc.Version) == "" {
		return fmt.Errorf("the plane reported admin contract %d but no version string — "+
			"this is a plane bug, not a version gap", doc.AdminContract)
	}
	c.plane = PlaneVersion{Version: strings.TrimSpace(doc.Version), Contract: doc.AdminContract, Reported: true}
	return nil
}

// Plane returns what the handshake learned.
func (c *Client) Plane() PlaneVersion { return c.plane }

// Require checks the introduction revision of a surviving capability before
// sending the operation. It is not a guarantee against later retirements.
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
// It is a note and not a refusal so a CLI behind the plane can still reach
// usable state. The warning must not promise that retired routes survive.
func (c *Client) SkewNote() string {
	if c.plane.Contract <= Speaks {
		return ""
	}
	return fmt.Sprintf("note: this plane is newer than kmx (%s; kmx %s speaks admin contract %d).\n"+
		"  Proceeding, but compatibility is not guaranteed; admin routes may have been retired.\n"+
		"  Upgrade kmx: go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest",
		c.plane.Describe(), c.kmxVersion, Speaks)
}
