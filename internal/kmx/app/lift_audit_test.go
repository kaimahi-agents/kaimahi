package app

import (
	"bytes"
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
  'version --client'|'config '*) exit 0 ;;
  *'delete networkpolicy'*) [ "$LIFT_FAIL" != policy ] ;;
  *'delete podmonitors.azmonitoring.coreos.com'*) [ "$LIFT_FAIL" != monitor ] && [ "$LIFT_FAIL" != mixed ] ;;
  *'get podmonitors.azmonitoring.coreos.com'*) exit 0 ;;
  *'get secret'*)
    if [ "$LIFT_FAIL" = credential ]; then printf 'NotFound\n' >&2; exit 1; fi ;;
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
				opt := lift.Options{BringYourOwn: byo, ResourceGroup: "demo(rg)", Cluster: "demo-cluster"}
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
	opt := lift.Options{ResourceGroup: "demo-rg", Cluster: "demo-cluster"}
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
			opt := lift.Options{BringYourOwn: true, ResourceGroup: "demo-rg", Cluster: "demo-cluster"}
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
	opt := lift.Options{BringYourOwn: true, ResourceGroup: "demo-rg", Cluster: "demo-cluster"}
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
	opt := lift.Options{ResourceGroup: "demo(rg)", Cluster: "demo-cluster", Registry: "reg12345",
		Location: "eastus", NodeSize: "size'$(false)", NodeCount: 3, NetworkPolicy: "calico", NetworkPolicySet: true, Step: "credential"}
	command := a.liftCommand(opt, false)
	// Execute only a shell function named kmx, never a real command.
	out, err := exec.Command("/bin/sh", "-c", "kmx() { printf '%s\\000' \"$@\"; }; "+command).Output()
	if err != nil {
		t.Fatalf("command is not shell-safe: %s: %v", command, err)
	}
	want := []string{"--context", opt.Cluster, "lift", "--step", opt.Step, "--observability=false",
		"--location", opt.Location, "--node-size", opt.NodeSize, "--network-policy", opt.NetworkPolicy,
		"--node-count", "3", "--resource-group", opt.ResourceGroup, "--cluster", opt.Cluster, "--registry", opt.Registry}
	if got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if a.Cfg.KubeContext != "kind-unrelated" {
		t.Fatal("rendering a command changed configuration")
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
			opt := lift.Options{ResourceGroup: "demo(rg)", Cluster: "demo-cluster", Registry: "reg12345",
				Location: "eastus", NodeSize: "Standard_B8ms", NodeCount: 3, NetworkPolicy: "calico", Step: "credential"}
			a.Cfg.Confirm = opt.Cluster
			err := a.Lift(opt)
			if failure != "" {
				if err == nil || !strings.Contains(err.Error(), a.liftCommand(opt, false)) {
					t.Fatalf("credential recovery lost effective options: %v", err)
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
	for _, byo := range []bool{false, true} {
		a, out, _ := liftAuditApp(t)
		opt := lift.Options{BringYourOwn: byo, ResourceGroup: "demo(rg)", Cluster: "demo-cluster", Registry: "reg12345"}
		a.aimAtTheCluster(opt)
		a.liftNextSteps(opt, &lift.Record{RunID: "abcd1234"})
		for _, claim := range []string{"The dashboard is", "two monitoring workspaces", "kmx lift down --byo --byo"} {
			if strings.Contains(out.String(), claim) {
				t.Errorf("disabled observability claimed %q: %s", claim, out)
			}
		}
		for _, want := range []string{"Azure metrics and logs were not checked", a.operationCommand("ledger", a.Cfg.Credential), a.liftCommand(opt, true)} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q: %s", want, out)
			}
		}
	}
}

func TestLiftPlanWithObservabilityDisabledRemainsReadOnly(t *testing.T) {
	a, out, dir := liftAuditApp(t)
	opt := lift.Options{ResourceGroup: "demo-rg", Cluster: "demo-cluster", Registry: "reg12345", Step: "verify", Plan: true}
	if err := a.Lift(opt); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "dashboard has its traffic") || !strings.Contains(out.String(), "Azure telemetry not checked") {
		t.Fatalf("plan promised disabled telemetry: %s", out)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if line != "az version" && line != "kubectl version --client" && line != "az account show -o json" {
			t.Fatalf("plan went beyond preflight and account: %s", calls)
		}
	}
	path, _ := liftRecordPath(opt.ResourceGroup, opt.Cluster)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plan wrote recovery record: %v", err)
	}
}
