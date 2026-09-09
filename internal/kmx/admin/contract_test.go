package admin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// openAt is open() with the plane's reported contract chosen, so both ends of
// the supported range are exercised from the same fake.
func openAt(t *testing.T, contract int, reported string, next http.HandlerFunc) (*Client, *bytes.Buffer) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell to stand in for the port-forward")
	}
	_, port := serve(t, reporting(contract, reported, next))
	log := &bytes.Buffer{}
	c, err := OpenAs(&fakeKube{token: "s3cret-admin-token", forward: forwarding(port)}, port, log, "v2.0.0")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(c.Close)
	return c, log
}

func nothing(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) }

// The old end of the range, and the reported failure. A v0.1.0 plane does not
// serve /admin/version, so kmx concluded nothing and sent the request anyway;
// the operator got Go's "404 page not found" quoted back under a sentence
// about the upstream table. Now the gap is named before anything is sent.
func TestAPlaneTooOldToReportItsVersionIsNamedAsOldNotQuotedAt404(t *testing.T) {
	c, _ := openAt(t, ContractUnreported, "", nothing)

	if got := c.Plane(); got.Reported || got.Contract != ContractUnreported {
		t.Fatalf("handshake got %+v, want an unreported contract 0", got)
	}

	err := c.Require(ContractTableDeclared, "validate an upstream table before applying it")
	if err == nil {
		t.Fatal("an operation the plane cannot serve was allowed through")
	}
	for _, want := range []string{
		"too old to validate an upstream table",
		"v0.1.0 or earlier",
		"admin contract 0",
		"kmx:   v2.0.0",
		"kmx plane",
		"nothing has been applied",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%s", want, err)
		}
	}
	if strings.Contains(err.Error(), "404") {
		t.Errorf("the refusal still leans on the status code:\n%s", err)
	}
}

// An old plane is not a broken plane: everything it CAN serve still works.
// A blanket refusal on any skew would strand a working cluster over one
// feature it does not have.
func TestAnOldPlaneStillServesEverythingItHas(t *testing.T) {
	c, _ := openAt(t, ContractUnreported, "", nothing)
	if err := c.Require(ContractUnreported, "read the ledger"); err != nil {
		t.Errorf("an operation the old plane can serve was refused: %v", err)
	}
}

// The new end of the range. A plane ahead of kmx proceeds — the state lives
// in the plane, and refusing would strand a working cluster because the CLI
// is behind — but it says so, once, where the operator will see it.
func TestAPlaneNewerThanKmxProceedsAndSaysSo(t *testing.T) {
	c, log := openAt(t, Speaks+5, "v9.0.0", nothing)

	if err := c.Require(Speaks, "apply governance"); err != nil {
		t.Errorf("a newer plane was refused: %v", err)
	}
	note := c.SkewNote()
	for _, want := range []string{"newer than kmx", "v9.0.0", "only grows", "go install"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say %q:\n%s", want, note)
		}
	}
	if !strings.Contains(log.String(), "newer than kmx") {
		t.Errorf("the note never reached the session's output:\n%s", log.String())
	}
}

// A matched pair says nothing at all. A note on every run is a note nobody
// reads by the time one matters.
func TestAMatchedPlaneSaysNothing(t *testing.T) {
	c, _ := openAt(t, Speaks, "v2.0.0", nothing)
	if note := c.SkewNote(); note != "" {
		t.Errorf("a matched plane produced a skew note: %s", note)
	}
	if err := c.Require(Speaks, "apply governance"); err != nil {
		t.Errorf("a matched plane was refused: %v", err)
	}
}

// Every session names the plane it is about to act on, the way the context
// guard names the cluster: on the session's own stream, before anything is
// asked of it.
func TestASessionNamesThePlaneItIsAboutToActOn(t *testing.T) {
	_, log := openAt(t, Speaks, "v1.2.3", nothing)
	if !strings.Contains(log.String(), fmt.Sprintf("plane v1.2.3 (admin contract %d)", Speaks)) {
		t.Errorf("the session did not name the plane:\n%s", log.String())
	}
}

// A 404 is the only non-200 that is an answer. A bad admin token must never
// be read as an old plane — that would turn an auth failure into advice to
// reinstall the plane.
func TestANonNotFoundFailureIsNotMistakenForAnOldPlane(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell to stand in for the port-forward")
	}
	_, port := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	_, err := OpenAs(&fakeKube{token: "s3cret-admin-token", forward: forwarding(port)}, port, io.Discard, "v2.0.0")
	if err == nil {
		t.Fatal("a 401 on the handshake was accepted")
	}
	if strings.Contains(err.Error(), "too old") || strings.Contains(err.Error(), "kmx plane") {
		t.Errorf("a 401 was diagnosed as a version gap:\n%v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the failure does not carry what actually happened:\n%v", err)
	}
}

// A plane that answers with half the answer is broken too. A blank version
// would put an empty string where every skew message names the plane, so an
// operator comparing two clusters would be shown nothing and told it was an
// answer.
func TestAPlaneThatReportsNoVersionIsAFaultNotAGap(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell to stand in for the port-forward")
	}
	_, port := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/admin/version":
			w.Write([]byte(`{"version": "  ", "admin_contract": 1}`))
		default:
			nothing(w, r)
		}
	})
	_, err := OpenAs(&fakeKube{token: "s3cret-admin-token", forward: forwarding(port)}, port, io.Discard, "v2.0.0")
	if err == nil || !strings.Contains(err.Error(), "plane bug") {
		t.Fatalf("a blank version was accepted as an answer: %v", err)
	}
	if strings.Contains(err.Error(), "kmx plane") {
		t.Errorf("a plane fault was diagnosed as a version gap:\n%v", err)
	}
}

// A plane that serves the route and reports a contract no release ever
// served is broken, not old, and gets told so — routing it to `kmx plane`
// would send an operator to reinstall over a fault an upgrade cannot fix.
func TestAPlaneReportingAnImpossibleContractIsAFaultNotAGap(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell to stand in for the port-forward")
	}
	// The route is served, and what it reports is impossible.
	_, badPort := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/admin/version":
			w.Write([]byte(`{"version": "v1.0.0", "admin_contract": 0}`))
		default:
			nothing(w, r)
		}
	})
	_, err := OpenAs(&fakeKube{token: "s3cret-admin-token", forward: forwarding(badPort)}, badPort, io.Discard, "v2.0.0")
	if err == nil || !strings.Contains(err.Error(), "plane bug") {
		t.Fatalf("an impossible contract was accepted as an old plane: %v", err)
	}
}

// The skew case against a plane that is genuinely old, rather than a fake
// that returns what the test expects.
//
// scripts/plane-upgrade-probe.sh already installs an older plane straight
// from the module proxy (`go install .../kaimahi-proxy@<rev>`) and runs it as
// a plain process — no cluster, no image — because the proxy is
// env-configured and file-fed. That is the affordable way to hold a version
// gap open on every PR, and it is worth more here than a second mechanism, so
// this test simply attaches to the plane that probe already has running.
//
// It is skipped when nothing set those variables, which is every ordinary
// `go test ./...`.
func TestAgainstARealOldPlane(t *testing.T) {
	port, token := os.Getenv("KAIMAHI_SKEW_PLANE_PORT"), os.Getenv("KAIMAHI_SKEW_PLANE_TOKEN")
	if port == "" || token == "" {
		t.Skip("no plane to attach to; scripts/plane-upgrade-probe.sh sets KAIMAHI_SKEW_PLANE_PORT/TOKEN")
	}
	wantOld := os.Getenv("KAIMAHI_SKEW_PLANE_OLD") == "1"

	c, err := OpenAs(&fakeKube{token: token, forward: forwarding(port)}, port, io.Discard, "v2.0.0-skew-probe")
	if err != nil {
		t.Fatalf("Open against the plane on 127.0.0.1:%s: %v", port, err)
	}
	defer c.Close()

	got := c.Plane()
	t.Logf("handshake: %s", got.Describe())

	if !wantOld {
		if !got.Reported || got.Contract < ContractTableDeclared {
			t.Fatalf("a plane built from this checkout did not report a usable contract: %+v", got)
		}
		if err := c.Require(ContractTableDeclared, "validate an upstream table before applying it"); err != nil {
			t.Fatalf("the current plane was refused: %v", err)
		}
		return
	}

	if got.Reported {
		t.Fatalf("the plane pinned as OLD reports a version (%s) — the gap this proves has closed, "+
			"so raise the probe's OLD_REV or drop this assertion", got.Describe())
	}
	err = c.Require(ContractTableDeclared, "validate an upstream table before applying it")
	if err == nil {
		t.Fatal("an operation the old plane cannot serve was allowed through")
	}
	for _, want := range []string{"too old", "kmx plane", "nothing has been applied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%s", want, err)
		}
	}
	if strings.Contains(err.Error(), "404 page not found") {
		t.Errorf("the operator is still being shown the plane's 404:\n%s", err)
	}
	t.Logf("refusal against a genuinely old plane:\n%s", err)
}
