package proxy

import (
	"encoding/json"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/plane/internal/metrics"
)

// What the plane tells a client about itself, and why a number sits beside
// the version string.
//
// A client that has to guess the plane's age gets it wrong in the one
// direction that matters. Before this endpoint existed, a newer kmx sent a
// request an older plane had never heard of and reported the plane's reply
// verbatim — "404 page not found" — which names neither the problem nor the
// fix. The one place that did better inferred age from a field missing out
// of a response body, which works for exactly that field and says nothing
// about the rest of the surface.
//
// So: the plane states what it is, before anything is sent.
//
// AdminContract is the load-bearing half. A version string is for humans and
// cannot be compared — "v0.1.0" against a development build's
// "v0.0.0-20260906..." orders wrongly, and a client cannot know which
// releases carried which routes. The contract records admin surface
// revisions and the introduction floors of surviving capabilities.
//
// Contract 3 deliberately breaks the former grow-only promise by retiring
// inbound. A higher number no longer guarantees every older route survives.
// The retirement marker is not a compatibility guard: older clients accept
// higher numbers and will still call removed routes. Those clients need a
// matching CLI upgrade; surviving capability floors remain unchanged.
const (
	// AdminContractUnreported is not served by anything; it is what a client
	// concludes when this endpoint 404s. Every plane up to and including
	// v0.1.0 is this, and it is a definite fact rather than a guess: the
	// client has already proved the plane is answering on its own forward,
	// so an absent route is an absent route.
	AdminContractUnreported = 0

	// AdminContractInitial was the first reporting contract. It introduced
	// the original sixteen admin routes, and
	// /admin/config/validate returning table_declared — the merged upstream
	// table's policy-relevant fields, without which a blueprint's `requires`
	// cannot be checked against the plane.
	AdminContractInitial = 1

	// AdminContractModelOverlay adds the model seam to what an overlay may
	// carry, and to what /admin/config/validate answers: an `upstreams`
	// block is merged rather than refused, and the response echoes every
	// model upstream with the protocol the plane resolved for it. A client
	// that sends one to an older plane gets the older plane's flat refusal
	// ("carries \"upstreams\", which an overlay may not set"), which is
	// true of that plane and reads like a mistake by the operator — so
	// this is the number `kmx models add` requires before it sends.
	AdminContractModelOverlay = 2

	// AdminContractInboundRetired removes /admin/inbound-audit and inbound
	// approval requests. Model overlays and tool/budget approvals remain.
	AdminContractInboundRetired = 3

	// AdminContractToolRetired removes tool allowlist/audit routes, tool
	// policy validation fields and tool approvals. Budget approvals and
	// model overlays remain; upgrade the CLI and plane together.
	AdminContractToolRetired = 4

	// AdminContractApprovalsRetired removes approval/request/grant APIs
	// and budget overrides. Model caps, accounting and overlays remain.
	AdminContractApprovalsRetired = 5

	// AdminContract is what THIS plane serves. Record additions and deliberate
	// retirements with a named revision so readers can see what changed.
	AdminContract = AdminContractApprovalsRetired
)

// version reports this plane's identity. It is authenticated like every other
// /admin route: the admin port is on no Service, so cluster credentials
// already gate it, and there is no reason for the build to be the one thing
// on this surface that answers without a token.
func (h *handler) version(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		// The same string kaimahi_build_info carries and the boot log
		// prints, so an operator comparing three places sees one answer.
		"version":        metrics.Version(),
		"admin_contract": AdminContract,
	})
}
