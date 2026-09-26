package app

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seamcert"
)

// statusFixture is a cluster that answers the Orka reads and records every
// call, so a test can assert what status ASKED as well as what it printed.
func statusFixture(t *testing.T, script string) (*App, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + calls + "\n" + script + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TOOLCHAIN", "off")
	out := &bytes.Buffer{}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceKubeCtx},
		Out: out, Err: &bytes.Buffer{}, Run: &run.Runner{Stdout: out, Stderr: &bytes.Buffer{}}}
	return a, out, calls
}

const orkaStatusScript = `case "$*" in
*"config view"*) printf '%s' '{"current-context":"kind-test","contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}';;
*"get deploy"*) printf '%s' 'orka-controller=1/1 ';;
*"get crd"*) printf '%s' 'agents.core.orka.ai providers.core.orka.ai ';;
*"get providers"*) printf '%s' 'local=true ';;
esac`

// `kmx status` reports the runtime this repository installs. The kagent
// listing it used to print described objects nothing here deploys, reads or
// drives — a report that is confidently about somebody else's cluster state
// is worse than no report, because it reads as coverage.
func TestStatusReportsTheOrkaRuntimeAndNotTheLegacyOne(t *testing.T) {
	a, out, calls := statusFixture(t, orkaStatusScript)
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	asked, _ := os.ReadFile(calls)
	if strings.Contains(string(asked), "kagent") || strings.Contains(string(asked), "modelconfigs") {
		t.Errorf("status still reads the legacy runtime:\n%s", asked)
	}
	for _, want := range []string{"version running", "deployments", "providers"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status does not report %q:\n%s", want, out)
		}
	}
}

// The Orka portion must remain the same reading; status also reports the
// independently deployed plane and certificate, which Orka status does not.
func TestStatusIncludesOrkaStatusWithoutReinterpretingIt(t *testing.T) {
	a, out, _ := statusFixture(t, orkaStatusScript)
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	viaStatus := out.String()
	b, other, _ := statusFixture(t, orkaStatusScript)
	if err := b.OrkaStatus(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(viaStatus, other.String()) {
		t.Fatalf("status changed the Orka reading:\n%s\n---\n%s", viaStatus, other)
	}
}

// The Orka portion of status uses the same preflight even when Orka is absent.
// The additional plane reads are independent and must not change that answer.
func TestStatusDelegatesEvenWhenTheClusterCannotBeRead(t *testing.T) {
	const noCluster = `case "$*" in
*"config view"*) printf '%s' '{"current-context":"other","contexts":[{"name":"other","context":{"cluster":"other"}}],"clusters":[{"name":"other","cluster":{"server":"https://example.test"}}]}';;
esac`
	a, out, viaStatusCalls := statusFixture(t, noCluster)
	a.Cfg.ContextSource = config.SourceDefault
	statusErr := a.Status()

	b, other, orkaCalls := statusFixture(t, noCluster)
	b.Cfg.ContextSource = config.SourceDefault
	orkaErr := b.OrkaStatus()

	if fmt.Sprint(statusErr) != fmt.Sprint(orkaErr) {
		t.Fatalf("the two status commands disagree about an unreadable cluster:\n%v\n---\n%v", statusErr, orkaErr)
	}
	if !strings.HasPrefix(out.String(), other.String()) {
		t.Fatalf("status changed the Orka report for an absent runtime:\n%s\n---\n%s", out, other)
	}
	// The Orka reads remain the prefix of the calls; plane reads follow.
	asked, _ := os.ReadFile(viaStatusCalls)
	delegated, _ := os.ReadFile(orkaCalls)
	if !strings.HasPrefix(string(asked), string(delegated)) {
		t.Fatalf("status changed Orka's cluster reads:\n%s\n---\n%s", asked, delegated)
	}
}

// The structured output is REFUSED rather than emptied. The old JSON document
// published a governance shape assembled from kagent objects, and no
// owner-managed equivalent exists: migrate targets are the owner's own
// workloads with no discovery index, so any replacement document would be a
// count of what kmx happened to be told about rather than of what is there.
// A consumer that pipes this into `jq` has to be told, not handed `{}`.
func TestStatusRefusesStructuredOutputRatherThanInventingIt(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			a, _, _ := statusFixture(t, orkaStatusScript)
			err := a.StatusWithOptions(StatusOptions{Output: format})
			if err == nil {
				t.Fatalf("%s output was accepted", format)
			}
			for _, want := range []string{format, "table"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

func TestStatusOutputValidation(t *testing.T) {
	a, _, _ := statusFixture(t, orkaStatusScript)
	if err := a.StatusWithOptions(StatusOptions{Output: "toml"}); err == nil {
		t.Fatal("unsupported status output was accepted")
	}
}

// The default and an explicit `table` are the same request.
func TestStatusDefaultsToTheTable(t *testing.T) {
	a, out, _ := statusFixture(t, orkaStatusScript)
	if err := a.StatusWithOptions(StatusOptions{Output: "table"}); err != nil {
		t.Fatal(err)
	}
	b, bare, _ := statusFixture(t, orkaStatusScript)
	if err := b.StatusWithOptions(StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != bare.String() {
		t.Fatalf("table and the default differ:\n%s\n---\n%s", out, bare)
	}
}

// Regression: the plane is a separate, surviving workload. Count only its
// pods, not Postgres or any Orka pods; surface renewal before expiry.
func TestStatusReportsProxyPodsAndExpiringSeamCertificate(t *testing.T) {
	now := time.Now().UTC()
	ca, err := seamcert.MintAuthority(now.Add(-380 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	serving, err := ca.Sign(seamcert.SeamNames(), now.Add(-380*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_TEST_CERT", base64.StdEncoding.EncodeToString(serving.CertPEM))
	script := `case "$*" in
*"get deploy kaimahi-proxy"*) printf '%s' '{"metadata":{"name":"kaimahi-proxy"},"spec":{"replicas":2},"status":{"readyReplicas":1}}';;
*"get pods -l app=kaimahi-proxy"*) printf '%s' '{"items":[{"metadata":{"name":"proxy-a"},"status":{"conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":2}]}},{"metadata":{"name":"proxy-b"},"status":{"conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"restartCount":1}]}}]}';;
*"get secret kaimahi-plane-seam-tls"*) printf '%s' "$KMX_TEST_CERT";;
*"get deploy"*) printf '%s' 'orka-controller=1/1 ';;
*"config view"*) printf '%s' '{"current-context":"kind-test","contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}';;
esac`
	a, out, calls := statusFixture(t, script)
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"deployments", "kaimahi-proxy", "1/2 pods ready", "3 restarts", "certificate", "expires in", "kmx plane --step certificate"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q from status:\n%s", want, out)
		}
	}
	asked, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--context kind-test -n kaimahi get deploy kaimahi-proxy -o json --request-timeout=15s",
		"--context kind-test -n kaimahi get pods -l app=kaimahi-proxy -o json --request-timeout=15s",
		"--context kind-test -n kaimahi get secret kaimahi-plane-seam-tls -o jsonpath={.data.tls\\.crt} --request-timeout=15s",
	} {
		if !strings.Contains(string(asked), want) {
			t.Errorf("missing pinned and bounded read %q:\n%s", want, asked)
		}
	}
}

func TestStatusDistinguishesAbsentPlaneFromUnreadablePlane(t *testing.T) {
	for _, tc := range []struct{ name, response, want string }{
		{"absent", `echo 'Error from server (NotFound): deployments.apps "kaimahi-proxy" not found' >&2; exit 1`, "not installed"},
		{"unreadable", `echo 'Error from server (Forbidden): deployments.apps "kaimahi-proxy" is forbidden' >&2; exit 1`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := `case "$*" in
*"get deploy kaimahi-proxy"*) ` + tc.response + `;;
*"get secret kaimahi-plane-seam-tls"*) echo 'Error from server (NotFound): secrets "kaimahi-plane-seam-tls" not found' >&2; exit 1;;
*"get deploy"*) printf '%s' 'orka-controller=1/1 ';;
*"config view"*) printf '%s' '{"current-context":"kind-test","contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}';;
esac`
			a, out, _ := statusFixture(t, script)
			if err := a.Status(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "plane") || !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "certificate") {
				t.Fatalf("missing plane %q or certificate:\n%s", tc.want, out)
			}
		})
	}
}

func TestTableAlignment(t *testing.T) {
	var out bytes.Buffer
	table(&out, []string{"NAME", "READY"}, [][]string{{"short", "yes"}, {"longer", "no"}})
	if got := out.String(); got != "  NAME    READY\n  short   yes  \n  longer  no   \n" {
		t.Fatalf("unexpected table:\n%q", got)
	}
}
