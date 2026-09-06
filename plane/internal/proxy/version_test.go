package proxy_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
)

// The plane states what it is, so no client has to infer it from a route
// that happens to be missing.
func TestThePlaneReportsItsVersionAndContract(t *testing.T) {
	mux, token := adminMux(t, newFakeStore())

	res := adminDo(mux, "GET", "/admin/version", token, "")
	require.Equal(t, 200, res.Code, res.Body.String())

	var doc struct {
		Version       string `json:"version"`
		AdminContract int    `json:"admin_contract"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &doc))
	require.NotEmpty(t, doc.Version, "a plane that cannot name itself is the problem this endpoint exists to fix")
	require.Equal(t, proxy.AdminContract, doc.AdminContract)
	require.GreaterOrEqual(t, doc.AdminContract, proxy.AdminContractInitial,
		"the contract a plane reports must be one a client can act on; 0 means 'did not report'")
}

// The build is not the one thing on this surface that answers without a
// token. Nothing here is sensitive, but a surface with one unauthenticated
// exception is a surface whose rule has to be remembered.
func TestTheVersionEndpointIsAuthenticatedLikeEveryOtherAdminRoute(t *testing.T) {
	mux, _ := adminMux(t, newFakeStore())
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/version", "", "").Code)
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/version", "wrong", "").Code)
}

// The contract only means anything if it moves when the surface does. This
// pins the direction: a release may raise it, and a change that lowers it is
// a client-visible removal, which the growth promise forbids.
func TestTheContractNeverGoesBackwards(t *testing.T) {
	require.GreaterOrEqual(t, proxy.AdminContract, proxy.AdminContractInitial)
	require.Equal(t, 0, proxy.AdminContractUnreported,
		"0 is reserved for a plane that served no version at all")
}
