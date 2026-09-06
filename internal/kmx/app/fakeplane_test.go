package app

import (
	"fmt"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// planePreamble is what every real plane answers before it is asked for
// anything: it is up, and it is this version. An admin session probes both,
// in that order, so a fake that skipped either would be standing in for a
// plane that does not exist.
//
// contract is the admin contract the fake reports.
// admin.ContractUnreported makes it behave like a v0.1.0 plane — the route is
// simply not there — which is the skew case worth testing.
func planePreamble(contract int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/admin/version":
			if contract == admin.ContractUnreported {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"version": "v9.9.9-test", "admin_contract": %d}`, contract)
		default:
			next(w, r)
		}
	}
}
