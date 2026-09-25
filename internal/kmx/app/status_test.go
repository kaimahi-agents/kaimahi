package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
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

// Reused rather than reimplemented: `kmx status` and `kmx orka status` are
// the same reading of the same cluster, so they must not be able to drift
// into two answers about it.
func TestStatusAndOrkaStatusAgree(t *testing.T) {
	a, out, _ := statusFixture(t, orkaStatusScript)
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	viaStatus := out.String()
	b, other, _ := statusFixture(t, orkaStatusScript)
	if err := b.OrkaStatus(); err != nil {
		t.Fatal(err)
	}
	if viaStatus != other.String() {
		t.Fatalf("the two status commands disagree:\n%s\n---\n%s", viaStatus, other)
	}
}

// Delegation has to hold for the UNHAPPY cluster too, which is where a
// second reading would have been bolted on. `kmx status` once ran its own
// context check before delegating, so a missing context — and a machine
// without kubectl, whose toolchain fetch lives in the delegated preflight —
// got a different answer depending on which of the two names was typed.
// Whatever the reading says here, both commands must say it.
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
	if out.String() != other.String() {
		t.Fatalf("the two status commands printed different reports:\n%s\n---\n%s", out, other)
	}
	// And they asked the cluster the same questions: a pre-call that reads
	// the kubeconfig on its own would show up here as an extra read.
	asked, _ := os.ReadFile(viaStatusCalls)
	delegated, _ := os.ReadFile(orkaCalls)
	if string(asked) != string(delegated) {
		t.Fatalf("`kmx status` reads the cluster differently from `kmx orka status`:\n%s\n---\n%s", asked, delegated)
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

func TestTableAlignment(t *testing.T) {
	var out bytes.Buffer
	table(&out, []string{"NAME", "READY"}, [][]string{{"short", "yes"}, {"longer", "no"}})
	if got := out.String(); got != "  NAME    READY\n  short   yes  \n  longer  no   \n" {
		t.Fatalf("unexpected table:\n%q", got)
	}
}
