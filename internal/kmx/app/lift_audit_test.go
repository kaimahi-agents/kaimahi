package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// Only these executables are on PATH. Even the real carried teardown script
// can reach nothing except the local Azure and Kubernetes mocks.
func liftAuditApp(t *testing.T) (*App, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("LIFT_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("LIFT_GONE", filepath.Join(dir, "gone"))
	t.Setenv("LIFT_FAIL", "")
	t.Setenv("KAIMAHI_CONFIRM", "wrong-inherited-value")
	for name, body := range map[string]string{
		"az": `printf 'az %s\n' "$*" >> "$LIFT_CALLS"
case "$*" in
  version) exit 0 ;;
  'account show'*) printf '%s\n' '{"id":"test-subscription","name":"Test","user":{"name":"test"}}' ;;
  'group exists'*) if [ -f "$LIFT_GONE" ]; then printf 'false\n'; else printf 'true\n'; fi ;;
  'group show'*) if [ "$LIFT_FAIL" = tag ]; then printf 'not-owned\n'; else printf 'p5b\n'; fi ;;
  'group delete'*) printf '%s\n' "$KAIMAHI_CONFIRM" > "$LIFT_GONE" ;;
  'resource list'*) exit 0 ;;
  'resource show'*) printf '%s\n' "$4" ;;
  'resource delete'*) exit 0 ;;
  'aks get-credentials'*) exit 0 ;;
  'aks update'*|'aks disable-addons'*) exit 0 ;;
  *) printf 'unexpected Azure call\n' >&2; exit 99 ;;
esac`,
		"kubectl": `printf 'kubectl %s\n' "$*" >> "$LIFT_CALLS"
case "$*" in
  'version --client') exit 0 ;;
  'config '*) printf '%s\n' '{"current-context":"demo-cluster","contexts":[{"name":"demo-cluster","context":{"cluster":"demo-cluster","namespace":"default"}}],"clusters":[{"name":"demo-cluster","cluster":{"server":"https://example.invalid"}}]}' ;;
  *'delete networkpolicy'*) [ "$LIFT_FAIL" != policy ] ;;
  *'delete podmonitors.azmonitoring.coreos.com'*) [ "$LIFT_FAIL" != monitor ] && [ "$LIFT_FAIL" != mixed ] ;;
  *'get podmonitors.azmonitoring.coreos.com'*) exit 0 ;;
  *'get secret'*)
    if [ "$LIFT_FAIL" = credential ]; then printf 'Error from server (Forbidden): secrets is forbidden\n' >&2; exit 1; fi ;;
  *) printf 'unexpected kubectl call\n' >&2; exit 99 ;;
esac`,
		"helm":    "exit 0",
		"python3": "exit 0",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"bash", "sed"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-unrelated", Credential: "test-credential"},
		Run: &run.Runner{Stdout: io.Discard, Stderr: &out}, Out: io.Discard, Err: &out}
	return a, &out, dir
}

func liftAuditRecord(t *testing.T, a *App, opt lift.Options, before lift.Pre, outside bool) string {
	t.Helper()
	record, save, err := a.openLiftRecord(opt, "test-subscription")
	if err != nil {
		t.Fatal(err)
	}
	record.Before = before
	record.ScrapeMonitorApplied = before.Recorded
	if outside {
		record.Created = []lift.Resource{{Kind: "workspace", Name: "outside", ID: "/resourceGroups/other/providers/test/outside"}}
	}
	if err := save(); err != nil {
		t.Fatal(err)
	}
	path, err := liftRecordPath(opt.ResourceGroup, opt.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLiftDownConfirmationPrecedesEveryDeletion(t *testing.T) {
	for _, byo := range []bool{false, true} {
		for _, confirm := range []string{"", "wrong", "demo-cluster", "demo(rg)"} {
			t.Run(strings.Join([]string{map[bool]string{false: "created", true: "byo"}[byo], confirm}, "/"), func(t *testing.T) {
				a, out, dir := liftAuditApp(t)
				opt := lift.Options{Payload: lift.PayloadOrka, BringYourOwn: byo, ResourceGroup: "demo(rg)", Cluster: "demo-cluster"}
				path := liftAuditRecord(t, a, opt, lift.Pre{Recorded: true}, true)
				a.Cfg.Confirm = confirm
				err := a.LiftDown(opt)
				accepted := confirm == opt.ResourceGroup && !byo || confirm == opt.Cluster && byo
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				if !accepted {
					if err == nil {
						t.Fatal("teardown accepted without target confirmation")
					}
					if !strings.Contains(err.Error(), a.liftCommand(opt, true)) {
						t.Fatalf("confirmation recovery lost target: %v", err)
					}
					for _, mutation := range []string{"delete", "aks update", "disable-addons", "resource show"} {
						if strings.Contains(string(calls), mutation) {
							t.Fatalf("acted before confirmation: %s", calls)
						}
					}
					if _, err := os.Stat(path); err != nil {
						t.Fatal("lost recovery record", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("confirmed teardown: %v\n%s", err, out)
				}
				if !strings.Contains(string(calls), "resource delete") {
					t.Fatal("outside resource was not removed")
				}
				if !byo {
					confirmation, err := os.ReadFile(filepath.Join(dir, "gone"))
					if err != nil || strings.TrimSpace(string(confirmation)) != opt.ResourceGroup {
						t.Fatalf("script did not receive verified confirmation: %q, %v", confirmation, err)
					}
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("completed teardown retained record: %v", err)
				}
				if strings.Contains(out.String(), "Nothing is billing") || strings.Contains(out.String(), "nothing this run created is left") {
					t.Fatalf("unqualified completion: %s", out)
				}
			})
		}
	}
}

func TestLiftDownScriptStillRequiresOwnershipTag(t *testing.T) {
	a, out, dir := liftAuditApp(t)
	t.Setenv("LIFT_FAIL", "tag")
	opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "demo-rg", Cluster: "demo-cluster"}
	a.Cfg.Confirm = opt.ResourceGroup
	path := liftAuditRecord(t, a, opt, lift.Pre{}, false)
	if err := a.LiftDown(opt); err == nil {
		t.Fatalf("untagged group accepted: %s", out)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Contains(string(calls), "group delete") {
		t.Fatalf("Go confirmation bypassed script ownership proof: %s", calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed teardown lost record", err)
	}
}

func TestLiftDownRetainsRecordForInClusterCleanupFailure(t *testing.T) {
	for _, failure := range []string{"policy", "monitor", "mixed", "absent"} {
		t.Run(failure, func(t *testing.T) {
			a, out, dir := liftAuditApp(t)
			t.Setenv("LIFT_FAIL", failure)
			opt := lift.Options{Payload: lift.PayloadOrka, BringYourOwn: true, ResourceGroup: "demo-rg", Cluster: "demo-cluster"}
			a.Cfg.Confirm = opt.Cluster
			path := liftAuditRecord(t, a, opt, lift.Pre{Recorded: true}, true)
			err := a.LiftDown(opt)
			if failure == "absent" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "run record has been kept") {
				t.Fatalf("cleanup failure not propagated: %v\n%s", err, out)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("lost recovery record", err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Contains(string(calls), "resource delete") {
				t.Fatalf("deleted workspace despite incomplete cluster cleanup: %s", calls)
			}
			if !strings.Contains(string(calls), "kubectl --context demo-cluster") {
				t.Fatalf("cleanup did not target managed cluster: %s", calls)
			}
		})
	}
}

func TestLiftDownUnknownAndZeroAreNotCompleteCleanup(t *testing.T) {
	a, out, _ := liftAuditApp(t)
	opt := lift.Options{Payload: lift.PayloadOrka, BringYourOwn: true, ResourceGroup: "demo-rg", Cluster: "demo-cluster"}
	a.Cfg.Confirm = opt.Cluster
	path := liftAuditRecord(t, a, opt, lift.Pre{}, false)
	if err := a.LiftDown(opt); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no Azure resources were checked or removed", "prior state was not established", "cleanup was not checked"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("unknown ownership lost its record", err)
	}
}

func TestLiftRecoveryCommandsPreserveOptionsAndShellArguments(t *testing.T) {
	a := &App{Cfg: &config.Config{KubeContext: "kind-unrelated"}}
	opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "demo(rg)", Cluster: "demo-cluster", Registry: "reg12345",
		Location: "eastus", NodeSize: "size'$(false)", NodeCount: 3, NetworkPolicy: "calico", NetworkPolicySet: true, Step: "credential"}
	command := a.liftCommand(opt, false)
	// Execute only a shell function named kmx, never a real command.
	out, err := exec.Command("/bin/sh", "-c", "kmx() { printf '%s\\000' \"$@\"; }; "+command).Output()
	if err != nil {
		t.Fatalf("command is not shell-safe: %s: %v", command, err)
	}
	want := []string{"--context", opt.Cluster, "lift", "--payload", opt.Payload, "--step", opt.Step, "--observability=false",
		"--location", opt.Location, "--node-size", opt.NodeSize, "--network-policy", opt.NetworkPolicy,
		"--node-count", "3", "--resource-group", opt.ResourceGroup, "--cluster", opt.Cluster, "--registry", opt.Registry}
	if got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if a.Cfg.KubeContext != "kind-unrelated" {
		t.Fatal("rendering a command changed configuration")
	}
	// --payload is mandatory. A recovery command without it is a command the
	// operator cannot paste back, which is the only thing it is for.
	if !strings.Contains(command, "--payload "+opt.Payload) {
		t.Fatalf("the recovery command omits the mandatory payload: %s", command)
	}
	opt.BringYourOwn = true
	down := a.liftCommand(opt, true)
	if strings.Count(down, "--byo") != 1 || strings.Contains(down, "--step") || strings.Contains(down, "--observability") || strings.Contains(down, "--node") {
		t.Fatalf("down has duplicated or unsupported flags: %s", down)
	}
}

func TestLiftCredentialRecoveryAndPhaseCompletion(t *testing.T) {
	for _, failure := range []string{"", "credential"} {
		t.Run(failure, func(t *testing.T) {
			a, out, _ := liftAuditApp(t)
			t.Setenv("LIFT_FAIL", failure)
			opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "demo(rg)", Cluster: "demo-cluster", Registry: "reg12345",
				Location: "eastus", NodeSize: "Standard_B8ms", NodeCount: 3, NetworkPolicy: "calico", Step: "credential"}
			a.Cfg.Confirm = opt.Cluster
			err := a.Lift(opt)
			if failure != "" {
				if err == nil || !strings.Contains(err.Error(), "cannot tell whether") {
					t.Fatalf("credential failure was not preserved: %v", err)
				}
				if !strings.Contains(out.String(), a.liftCommand(opt, false)) {
					t.Fatalf("phase failure lost recovery command: %s", out)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Other phases were not checked") || strings.Contains(out.String(), "The agent is running on a managed cluster") {
				t.Fatalf("one phase claimed entire lift: %s", out)
			}
			opt.Step = ""
			if !strings.Contains(out.String(), a.liftCommand(opt, false)) {
				t.Fatalf("full-run follow-up lost options: %s", out)
			}
		})
	}
}

func TestLiftNextStepsDoNotClaimDisabledObservability(t *testing.T) {
	for _, payload := range lift.Payloads {
		for _, byo := range []bool{false, true} {
			a, out, _ := liftAuditApp(t)
			opt := lift.Options{Payload: payload, BringYourOwn: byo, ResourceGroup: "demo(rg)", Cluster: "demo-cluster", Registry: "reg12345"}
			a.aimAtTheCluster(opt)
			a.liftNextSteps(opt, &lift.Record{RunID: "abcd1234"})
			for _, claim := range []string{"The dashboard is", "two monitoring workspaces", "kmx lift down --byo --byo"} {
				if strings.Contains(out.String(), claim) {
					t.Errorf("%s: disabled observability claimed %q: %s", payload, claim, out)
				}
			}
			want := []string{"Azure metrics and logs were not checked", a.liftCommand(opt, true)}
			// The spend ledger is offered only where something writes to it.
			// The orka payload wires no Provider through the plane, so
			// pointing at a credential's ledger would be pointing at an
			// empty one; `kmx flow` is the honest view there.
			if payload == lift.PayloadOrka {
				want = append(want, a.operationCommand("orka", "status"), a.operationCommand("flow"))
			} else {
				want = append(want, a.operationCommand("ledger", a.Cfg.Credential))
			}
			for _, w := range want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("%s: missing %q: %s", payload, w, out)
				}
			}
		}
	}
}

func TestLiftPlanWithObservabilityDisabledRemainsReadOnly(t *testing.T) {
	a, out, dir := liftAuditApp(t)
	opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "demo-rg", Cluster: "demo-cluster", Registry: "reg12345", Step: "verify", Plan: true}
	if err := a.Lift(opt); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "dashboard has its traffic") || !strings.Contains(out.String(), "Azure telemetry not checked") {
		t.Fatalf("plan promised disabled telemetry: %s", out)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if line != "az version" && line != "az account show -o json" {
			t.Fatalf("plan went beyond preflight and account: %s", calls)
		}
	}
	path, _ := liftRecordPath(opt.ResourceGroup, opt.Cluster)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plan wrote recovery record: %v", err)
	}
}

func TestLiftPlanNeedsOnlyTheInstalledAzureCLI(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	t.Setenv("PLAN_CALLS", calls)
	t.Setenv("KMX_HOME", filepath.Join(dir, "home"))
	t.Setenv("KMX_TOOLCHAIN", "")
	stub := `#!/bin/sh
printf 'az %s\n' "$*" >> "$PLAN_CALLS"
case "$*" in
  version) exit 0 ;;
  'account show -o json') printf '%s\n' '{"id":"test-subscription","name":"Test","user":{"name":"test"}}' ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "az"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{Stdout: io.Discard, Stderr: &out}, Out: io.Discard, Err: &out}
	opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "demo-rg", Cluster: "demo-cluster", Registry: "reg12345", Plan: true}
	if err := a.Lift(opt); err != nil {
		t.Fatalf("installed-binary plan required more than az: %v\n%s", err, out.String())
	}
	if len(a.provisioned) != 0 {
		t.Fatalf("plan downloaded tools: %+v", a.provisioned)
	}
	got, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "az version\naz account show -o json\n" {
		t.Fatalf("plan executed more than Azure preflight and account inspection:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "home")); !os.IsNotExist(err) {
		t.Fatalf("plan created state or a lift record: %v", err)
	}
	if !strings.Contains(out.String(), "will do, in order") || !strings.Contains(out.String(), "nothing was created") {
		t.Fatalf("plan was not printed: %s", out.String())
	}
}

func TestLiftDependenciesAreSelectedByPhase(t *testing.T) {
	names := func(opt lift.Options) map[string]bool {
		got := map[string]bool{}
		for _, dep := range (&App{}).liftDependencies(opt) {
			got[dep.name] = true
		}
		return got
	}
	base := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "rg", Cluster: "cluster", Registry: "reg12345", Observability: true}
	for _, tc := range []struct {
		step string
		want []string
		not  []string
	}{
		{"cluster", []string{"az", "kubectl", "bash"}, []string{"helm", "go", "python3", "curl"}},
		{"boundary", []string{"az", "kubectl", "bash", "python3"}, []string{"helm", "go", "curl"}},
		{"kagent", []string{"az", "kubectl", "helm"}, []string{"bash", "go", "python3", "curl"}},
		{"credential", []string{"az", "kubectl"}, []string{"bash", "helm", "go", "python3", "curl"}},
		{"plane", []string{"az", "kubectl", "bash", "go"}, []string{"helm", "curl"}},
		{"verify", []string{"az", "kubectl", "curl"}, []string{"bash", "helm", "go", "python3"}},
	} {
		t.Run(tc.step, func(t *testing.T) {
			opt := base
			opt.Step = tc.step
			got := names(opt)
			for _, want := range tc.want {
				if !got[want] {
					t.Errorf("missing %s dependency: %v", want, got)
				}
			}
			for _, unwanted := range tc.not {
				if got[unwanted] {
					t.Errorf("unexpected %s dependency: %v", unwanted, got)
				}
			}
		})
	}

	// A full lift preflights everything it will EVENTUALLY need, before it
	// creates anything — and the payload decides what that is. An Orka lift
	// never runs Helm, so demanding it would make an operator install a tool
	// this path has no use for.
	for _, tc := range []struct {
		payload string
		want    []string
		not     []string
	}{
		{lift.PayloadKagent, []string{"az", "kubectl", "bash", "python3", "helm", "go", "curl"}, nil},
		{lift.PayloadOrka, []string{"az", "kubectl", "bash", "python3", "go", "curl"}, []string{"helm"}},
	} {
		payloadBase := base
		payloadBase.Payload = tc.payload
		full := names(payloadBase)
		for _, want := range tc.want {
			if !full[want] {
				t.Errorf("%s: full lift does not preflight eventual dependency %s before cluster creation: %v",
					tc.payload, want, full)
			}
		}
		for _, unwanted := range tc.not {
			if full[unwanted] {
				t.Errorf("%s: full lift demands %s, which this payload never runs: %v", tc.payload, unwanted, full)
			}
		}
	}
	withoutTelemetry := base
	withoutTelemetry.Step = "verify"
	withoutTelemetry.Observability = false
	if names(withoutTelemetry)["curl"] {
		t.Error("verify without Azure telemetry requires curl")
	}
}

func TestLiftPyYAMLAndScriptPlatformChecksAreStepAware(t *testing.T) {
	if liftNeedsManifestRenderer([]string{"credential", "verify"}) {
		t.Fatal("a phase that does not render the plane requires PyYAML")
	}
	if !liftNeedsManifestRenderer([]string{"cluster", "plane", "verify"}) {
		t.Fatal("a run containing plane does not require PyYAML")
	}
	if err := liftPlatformError([]string{"credential"}, "windows"); err != nil {
		t.Fatalf("a phase with no script was rejected on Windows: %v", err)
	}
	if err := liftPlatformError([]string{"boundary"}, "windows"); err == nil || !strings.Contains(err.Error(), "WSL") {
		t.Fatalf("script-backed phase has no clear Windows refusal: %v", err)
	}
}

// A lift is recorded with the payload it landed. Resuming it with the other
// one would install BOTH platforms onto a single cluster — the exact outcome
// the payload split exists to prevent — so the difference is refused rather
// than reconciled, the same way a branch mismatch is.
func TestAResumedLiftCannotSwitchPayload(t *testing.T) {
	a, _, _ := liftAuditApp(t)
	opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "demo-rg", Cluster: "demo-cluster", Registry: "reg12345"}
	liftAuditRecord(t, a, opt, lift.Pre{}, false)

	opt.Payload = lift.PayloadKagent
	_, _, err := a.openLiftRecord(opt, "test-subscription")
	if err == nil {
		t.Fatal("a recorded orka lift was resumed as kagent")
	}
	for _, want := range []string{"orka", "kagent", "both platforms"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never says %q: %v", want, err)
		}
	}

	// The matching payload still opens, or the guard would block every resume.
	opt.Payload = lift.PayloadOrka
	if _, _, err := a.openLiftRecord(opt, "test-subscription"); err != nil {
		t.Fatalf("resuming with the recorded payload was refused: %v", err)
	}
}

// A record written before the split carries no payload, and only kagent could
// have written it. Refusing those would strand every existing lift; reading
// one as orka would be a lie about what is on the cluster.
func TestALiftRecordedBeforeThePayloadSplitResumesAsKagent(t *testing.T) {
	a, _, _ := liftAuditApp(t)
	opt := lift.Options{Payload: lift.PayloadKagent, ResourceGroup: "old-rg", Cluster: "old-cluster", Registry: "reg12345"}
	path := liftAuditRecord(t, a, opt, lift.Pre{}, false)

	// Strip the field, as a record from before it existed has no payload.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "payload")
	rewritten, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := a.openLiftRecord(opt, "test-subscription"); err != nil {
		t.Fatalf("a legacy record was refused a kagent resume: %v", err)
	}
	opt.Payload = lift.PayloadOrka
	if _, _, err := a.openLiftRecord(opt, "test-subscription"); err == nil {
		t.Fatal("a legacy kagent record accepted an orka resume")
	}
}
