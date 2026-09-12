package proxy_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
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

// The removed routes must be distinguishable from the tool-governance
// surface. Older clients still need a matching CLI upgrade.
func TestContract4ReportsToolRetirement(t *testing.T) {
	mux, token := adminMux(t, newFakeStore())
	for _, path := range []string{"/admin/inbound-audit", "/admin/tool-audit", "/admin/tool-allowlist"} {
		require.Equal(t, 404, adminDo(mux, "GET", path, token, "").Code)
	}

	res := adminDo(mux, "GET", "/admin/version", token, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	var doc struct {
		AdminContract int `json:"admin_contract"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &doc))
	require.Equal(t, 5, doc.AdminContract, "approval retirement must not report an approval-capable contract")
}

// The build is not the one thing on this surface that answers without a
// token. Nothing here is sensitive, but a surface with one unauthenticated
// exception is a surface whose rule has to be remembered.
func TestTheVersionEndpointIsAuthenticatedLikeEveryOtherAdminRoute(t *testing.T) {
	mux, _ := adminMux(t, newFakeStore())
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/version", "", "").Code)
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/version", "wrong", "").Code)
}

// The contract only means anything if it moves when the surface does, and a
// test cannot check that against the constant it is checking: AdminContract is
// DEFINED as the newest step constant, so comparing the two holds for every
// possible value and proves nothing.
//
// What can regress is the relationship between the served contract and the
// steps the source records. Each raise is required to leave a named constant
// behind saying what changed, including deliberate retirements. The set of
// step constants records revision progression, not route survival. Reading
// them back out of the source gives the test a subject it does not control: lowering
// AdminContract while a higher step is still named fails here, and so does
// adding a step constant without serving it.
func TestTheContractNeverGoesBackwards(t *testing.T) {
	steps := declaredContractSteps(t)
	require.GreaterOrEqual(t, len(steps), 2,
		"the source must name at least the unreported floor and one served contract; "+
			"a set this test could not shrink would be a set it could not check")

	highest := 0
	for name, value := range steps {
		require.GreaterOrEqual(t, proxy.AdminContract, value,
			"%s is a revision this source records, so reporting less would conceal a recorded change", name)
		if value > highest {
			highest = value
		}
	}
	require.Equal(t, highest, proxy.AdminContract,
		"the served contract is the newest step named; raising it without a constant saying what changed "+
			"leaves the next reader unable to see what changed")

	require.Equal(t, 0, proxy.AdminContractUnreported,
		"0 is reserved for a plane that served no version at all")
	require.Equal(t, 1, proxy.AdminContractInitial,
		"the first reporting contract is 1; renumbering it would make every deployed plane's report mean something else")
}

// declaredContractSteps reads the AdminContract* step constants out of the
// committed source, excluding AdminContract itself — the value under test.
func declaredContractSteps(t *testing.T) map[string]int {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	steps := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range vs.Names {
					if !strings.HasPrefix(ident.Name, "AdminContract") || ident.Name == "AdminContract" {
						continue
					}
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.INT {
						continue
					}
					n, err := strconv.Atoi(lit.Value)
					require.NoError(t, err)
					steps[ident.Name] = n
				}
			}
		}
	}
	return steps
}
