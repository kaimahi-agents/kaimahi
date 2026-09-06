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
// releases carried which routes. The contract is a single integer a client
// CAN compare, and it answers the only question a client actually has: can
// this plane serve the thing I am about to ask for?
//
// The rule that makes the number safe to compare, and it is a promise this
// plane keeps rather than an observation about it: within a major version
// the admin surface only ever GROWS. A route that has been served is not
// removed, and its request and response shapes are not changed in place —
// new information arrives as new fields or new routes, and the contract
// number goes up. That is what lets a client older than the plane proceed
// instead of being stranded by a CLI that is merely behind.
const (
	// AdminContractUnreported is not served by anything; it is what a client
	// concludes when this endpoint 404s. Every plane up to and including
	// v0.1.0 is this, and it is a definite fact rather than a guess: the
	// client has already proved the plane is answering on its own forward,
	// so an absent route is an absent route.
	AdminContractUnreported = 0

	// AdminContractInitial is the first contract that reports itself, and it
	// carries the surface as it stands: the sixteen admin routes, and
	// /admin/config/validate returning table_declared — the merged upstream
	// table's policy-relevant fields, without which a blueprint's `requires`
	// cannot be checked against the plane.
	AdminContractInitial = 1

	// AdminContract is what THIS plane serves. Raise it in the same change
	// that adds something a client may depend on, and add a constant above
	// naming what that was — the number is only useful if a reader can see
	// what each step bought.
	AdminContract = AdminContractInitial
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
