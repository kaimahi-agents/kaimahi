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
*"get deploy -o json") printf '%s' '{"items":[{"metadata":{"name":"w112-controller","labels":{"app.kubernetes.io/component":"controller","app.kubernetes.io/name":"orka"}},"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/orka-agents/orka:0.2.0"}]}}}}]}';;
*"get deploy"*) printf '%s' 'orka-controller=1/1 ';;
*"get crd"*) printf '%s' 'agents.core.orka.ai providers.core.orka.ai ';;
*"get providers"*) printf '%s' 'local=true ';;
esac`

// `kmx status` reports the runtime this repository installs, not unrelated
// cluster objects that would give a false impression of coverage.
func TestStatusReportsTheOrkaRuntimeAndNotTheLegacyOne(t *testing.T) {
	a, out, calls := statusFixture(t, orkaStatusScript)
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	asked, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"kagent", "modelconfigs", "-n kaimahi", "get secret"} {
		if strings.Contains(string(asked), forbidden) {
			t.Errorf("status made a non-native read %q:\n%s", forbidden, asked)
		}
	}
	for _, forbidden := range []string{"plane", "kmx migrate", "seam"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("native status advertises %q: %s", forbidden, out.String())
		}
	}
	for _, want := range []string{"version running", "deployments", "providers"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status does not report %q:\n%s", want, out)
		}
	}
}

// Status must produce exactly the native Orka report, with no plane suffix
// or extra CA, Deployment, pod or Secret reads.
func TestStatusIncludesOrkaStatusWithoutReinterpretingIt(t *testing.T) {
	a, out, statusCalls := statusFixture(t, orkaStatusScript)
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	viaStatus := out.String()
	b, other, orkaCalls := statusFixture(t, orkaStatusScript)
	if err := b.OrkaStatus(); err != nil {
		t.Fatal(err)
	}
	if viaStatus != other.String() {
		t.Errorf("status changed the Orka reading:\n%s\n---\n%s", viaStatus, other)
	}
	asked, err := os.ReadFile(statusCalls)
	if err != nil {
		t.Fatal(err)
	}
	delegated, err := os.ReadFile(orkaCalls)
	if err != nil {
		t.Fatal(err)
	}
	if string(asked) != string(delegated) {
		t.Errorf("status added cluster reads:\n%s\n---\n%s", asked, delegated)
	}
}

// Status preserves native preflight, report and error behavior even when the
// cluster cannot be read.
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
		t.Fatalf("status changed the Orka report for an absent runtime:\n%s\n---\n%s", out, other)
	}
	// Status must make only the native Orka reads.
	asked, err := os.ReadFile(viaStatusCalls)
	if err != nil {
		t.Fatal(err)
	}
	delegated, err := os.ReadFile(orkaCalls)
	if err != nil {
		t.Fatal(err)
	}
	if string(asked) != string(delegated) {
		t.Fatalf("status changed Orka's cluster reads:\n%s\n---\n%s", asked, delegated)
	}
}

// Structured status output is refused rather than emptied. Validation must
// not touch a cluster or advertise commands that no longer exist.
func TestStatusRefusesStructuredOutputRatherThanInventingIt(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			a, out, calls := statusFixture(t, orkaStatusScript)
			err := a.StatusWithOptions(StatusOptions{Output: format})
			if err == nil {
				t.Fatalf("%s output was accepted", format)
			}
			if out.Len() != 0 {
				t.Errorf("format refusal wrote output: %s", out.String())
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Errorf("format refusal touched kubectl: %v", err)
			}
			for _, forbidden := range []string{"kmx migrate", "kmx plane", "seam"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("format refusal advertises %q: %v", forbidden, err)
				}
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
	a, out, calls := statusFixture(t, orkaStatusScript)
	err := a.StatusWithOptions(StatusOptions{Output: "toml"})
	if err == nil || !strings.Contains(err.Error(), `status output "toml" is not supported`) {
		t.Fatalf("unsupported status output refusal = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("format refusal wrote output: %s", out.String())
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Errorf("format refusal touched kubectl: %v", err)
	}
}

func TestStatusPreservesNativePreflightAndReadErrors(t *testing.T) {
	for _, tc := range []struct{ name, script, want string }{
		{"preflight", "exit 7", "kubectl was found"},
		{"unreachable", `case "$*" in *"get deploy"*) echo 'Unable to connect to the server: connection refused' >&2; exit 1;; esac`, "cannot read Orka: the cluster did not answer"},
		{"forbidden", `case "$*" in *"get deploy"*) echo 'Error from server (Forbidden)' >&2; exit 1;; esac`, "access forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, out, statusCalls := statusFixture(t, tc.script)
			statusErr := a.Status()
			b, other, orkaCalls := statusFixture(t, tc.script)
			orkaErr := b.OrkaStatus()
			if statusErr == nil || !strings.Contains(statusErr.Error(), tc.want) {
				t.Fatalf("native status error = %v, want %q", statusErr, tc.want)
			}
			// Preflight errors include the fixture's executable path.
			statusText := strings.ReplaceAll(statusErr.Error(), filepath.Dir(statusCalls), "<fixture>")
			orkaText := strings.ReplaceAll(fmt.Sprint(orkaErr), filepath.Dir(orkaCalls), "<fixture>")
			if statusText != orkaText || out.String() != other.String() {
				t.Errorf("status changed native failure: %v / %v; output %q / %q", statusErr, orkaErr, out.String(), other.String())
			}
			asked, err := os.ReadFile(statusCalls)
			if err != nil {
				t.Fatal(err)
			}
			delegated, err := os.ReadFile(orkaCalls)
			if err != nil {
				t.Fatal(err)
			}
			if string(asked) != string(delegated) {
				t.Errorf("status changed failed native reads:\n%s\n---\n%s", asked, delegated)
			}
		})
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
