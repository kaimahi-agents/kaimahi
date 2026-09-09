package app

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// planeIssuing answers the two admin calls `kmx tools govern` makes: mint a
// credential, then set its allowlist.
func planeIssuing(t *testing.T, minted *int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/credentials":
			*minted++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			// Not a key shape: the scanner reads this file too.
			_, _ = w.Write([]byte(`{"token":"kmh_` + strings.Repeat("a", 64) + `"}`))
		case "/admin/tool-allowlist":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected admin call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// The credential and its allowlist are what make a call governed. On a
// cluster with no kagent the six kagent steps have nothing to act on, and
// the command has to finish rather than fail after doing the work.
func TestGovernToolsCompletesOnAClusterWithNoKagent(t *testing.T) {
	minted := 0
	f := newGovernFixture(t, "", planeIssuing(t, &minted))
	t.Setenv("KMX_TEST_NO_KAGENT", "1")

	err := f.app.GovernTools(ToolsOptions{
		Credential: "sundae-concierge", Secret: "kaimahi-sundae-token",
		SecretNamespace: "demo", Server: "kaimahi-sundae",
		Tools: "list_menu,quote_order",
	})
	if err != nil {
		t.Fatalf("governing a foreign runtime must complete: %v", err)
	}
	if minted != 1 {
		t.Fatalf("credentials minted: %d, want 1", minted)
	}

	args := f.args()
	// The token's Secret, and the authority the runtime verifies the seam
	// against, both land where the runtime runs.
	if !strings.Contains(args, "-n demo apply -f -") {
		t.Fatalf("nothing was written into the runtime's namespace:\n%s", args)
	}
	written := readFile(t, f.stdin)
	for _, want := range []string{"kaimahi-sundae-token", "kaimahi-plane-ca"} {
		if !strings.Contains(written, want) {
			t.Fatalf("Secret %q was not written:\n%s", want, written)
		}
	}
	// And nothing was asked of a controller that is not there.
	for _, kagentStep := range []string{"get remotemcpserver", "patch agent", "wait --for"} {
		if strings.Contains(args, kagentStep) {
			t.Fatalf("%q ran on a cluster with no kagent:\n%s", kagentStep, args)
		}
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "/upstream/") || !strings.Contains(notes, "api-key") {
		t.Fatalf("the operator is not told what to point a runtime at:\n%s", notes)
	}
}

// The token is shown once. A Secret namespace that does not exist has to
// refuse BEFORE the credential is minted, or the recovery is a hand-written
// DELETE against the plane's database.
func TestGovernToolsRefusesAMissingNamespaceBeforeMintingAnything(t *testing.T) {
	minted := 0
	f := newGovernFixture(t, "", planeIssuing(t, &minted))
	t.Setenv("KMX_TEST_NO_NAMESPACES", "kagent")

	err := f.app.GovernTools(ToolsOptions{
		Credential: "sundae-concierge", Tools: "list_menu",
	})
	if err == nil {
		t.Fatal("a missing Secret namespace was accepted")
	}
	if !strings.Contains(err.Error(), "--secret-namespace") {
		t.Fatalf("the refusal must name the fix: %v", err)
	}
	if minted != 0 {
		t.Fatalf("a credential was minted before the refusal (%d)", minted)
	}
	if _, statErr := os.Stat(f.stdin); statErr == nil {
		t.Fatal("something was written to the cluster before the refusal")
	}
}

// On a cluster that HAS kagent, every step the skip removes is still run:
// the seam's prior verdict is read before the credential is written, the
// seam is applied, kagent is asked to look again, and the agent is
// repointed. The fake cluster cannot finish a rollout, so this asserts the
// steps that ran rather than a completed command.
func TestGovernToolsStillDrivesKagentWhereItIsInstalled(t *testing.T) {
	minted := 0
	f := newGovernFixture(t, "", planeIssuing(t, &minted))
	t.Setenv("KMX_TEST_SEAM", `{"metadata":{"generation":2},"status":{"observedGeneration":2,`+
		`"conditions":[{"type":"Accepted","status":"True","lastTransitionTime":"2099-01-01T00:00:00Z"}]}}`)
	// The wait's own timeout is not what this test is about.
	old := seamRecheckWait
	seamRecheckWait = 0
	t.Cleanup(func() { seamRecheckWait = old })

	_ = f.app.GovernTools(ToolsOptions{Credential: "hello-tools", Tools: "k8s_get_resources"})

	args := f.args()
	for _, want := range []string{"get remotemcpserver", "patch agent"} {
		if !strings.Contains(args, want) {
			t.Fatalf("the kagent path did not run %q:\n%s", want, args)
		}
	}
	if minted != 1 {
		t.Fatalf("credentials minted: %d, want 1", minted)
	}
}
