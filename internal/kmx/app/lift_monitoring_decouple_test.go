package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// New runs must not infer plane-object ownership from an absent prior-state
// field. Old records still need their recorded cleanup, including billed ARM
// resources, even when opened again by the decoupled lifecycle.
func TestLiftMonitoringOwnershipSurvivesRecordResume(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new run", true: "old record"}[legacy], func(t *testing.T) {
			a, out, dir := liftAuditApp(t)
			opt := lift.Options{Payload: lift.PayloadOrka, BringYourOwn: true, ResourceGroup: "demo-rg", Cluster: "demo-cluster", Registry: "reg12345"}
			record, save, err := a.openLiftRecord(opt, "test-subscription")
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				// Literal old-format record: the new marker must stay absent
				// across resume, not be defaulted to new-run semantics.
				path, _ := liftRecordPath(opt.ResourceGroup, opt.Cluster)
				body := `{"run_id":"abcd1234","branch":"byo","payload":"orka","subscription":"test-subscription","resource_group":"demo-rg","cluster":"demo-cluster","before":{"recorded":true},"scrape_monitor_applied":true,"created":[{"kind":"workspace","name":"outside","id":"/resourceGroups/other/providers/test/outside"}]}`
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				record.Before = lift.Pre{Recorded: true}
				record.Created = []lift.Resource{{Kind: "workspace", Name: "outside", ID: "/resourceGroups/other/providers/test/outside"}}
				if err := save(); err != nil {
					t.Fatal(err)
				}
			}
			resumed, _, err := a.openLiftRecord(opt, "test-subscription")
			if err != nil {
				t.Fatal(err)
			}
			before, unmanaged := resumed.Before, resumed.PlaneMonitoringUnmanaged
			if err := a.recordPreExistingState(opt, resumed, func() error { t.Fatal("resume rewrote established ownership"); return nil }); err != nil {
				t.Fatal(err)
			}
			if resumed.Before != before || resumed.PlaneMonitoringUnmanaged != unmanaged {
				t.Fatalf("resume changed established ownership: %+v", resumed)
			}
			a.Cfg.Confirm = opt.Cluster
			if err := a.LiftDown(opt); err != nil {
				t.Fatalf("teardown: %v\n%s", err, out)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			for _, command := range []string{"delete networkpolicy", "delete podmonitors.azmonitoring.coreos.com"} {
				if got := strings.Contains(string(calls), command); got != legacy {
					t.Errorf("%s: deletion = %t, legacy = %t:\n%s", command, got, legacy, calls)
				}
			}
			for _, command := range []string{"--disable-azure-monitor-metrics", "disable-addons", "resource show --ids", "resource delete --ids"} {
				if !strings.Contains(string(calls), command) {
					t.Errorf("lost owned monitoring cleanup %q:\n%s", command, calls)
				}
			}
		})
	}
}

func TestLiftMonitoringCreatesOnlyAzureMonitoringResources(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	t.Setenv("LIFT_MONITOR_CALLS", calls)
	t.Setenv("PATH", dir)
	az := `#!/bin/sh
printf 'az %s\n' "$*" >> "$LIFT_MONITOR_CALLS"
case "$*" in
  'aks show'*'--query id '*) echo /resourceGroups/rg/providers/test/cluster ;;
  'group show'*) echo westus3 ;;
  'aks show'*'--query nodeResourceGroup '*) echo nodes ;;
  'aks show'*) echo '{}' ;;
  'monitor account create'*|'monitor log-analytics workspace create'*|'aks update'*|'aks enable-addons'*) exit 0 ;;
  'monitor account show'*) echo /resourceGroups/rg/providers/test/metrics ;;
  'monitor log-analytics workspace show'*) echo /resourceGroups/rg/providers/test/logs ;;
  'resource list'*) echo '[]' ;;
  *) echo 'unexpected Azure operation' >&2; exit 99 ;;
esac
`
	kubectl := `#!/bin/sh
printf 'kubectl %s\n' "$*" >> "$LIFT_MONITOR_CALLS"
case "$*" in
  'config '*) echo '{"current-context":"cluster","contexts":[{"name":"cluster","context":{"cluster":"cluster"}}],"clusters":[{"name":"cluster","cluster":{"server":"https://example.invalid"}}]}' ;;
  *) echo 'unexpected plane-object operation' >&2; exit 99 ;;
esac
`
	for name, body := range map[string]string{"az": az, "kubectl": kubectl} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "cluster", Confirm: "cluster"}, Run: &run.Runner{Stdout: io.Discard, Stderr: &out}, Out: io.Discard, Err: &out}
	opt := lift.Options{Payload: lift.PayloadOrka, BringYourOwn: true, ResourceGroup: "rg", Cluster: "cluster", Registry: "reg12345", Observability: true}
	record, err := lift.NewRecord("abcd1234", lift.BringYourOwn, lift.PayloadOrka, "sub", "rg", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	a.aimAtTheCluster(opt)
	saved := 0
	if err := a.liftObservability(opt, record, func() error { saved++; return nil }); err != nil {
		t.Fatalf("monitoring failed: %v\n%s", err, out.String())
	}
	if !record.Before.Recorded || len(record.Created) != 2 || saved < 3 {
		t.Fatalf("monitoring ownership was not saved: %+v, saves=%d", record, saved)
	}
	asked, _ := os.ReadFile(calls)
	for _, forbidden := range []string{"kubectl --context", "deployment group create", "workbook", "kaimahi-plane", "kaimahi-proxy"} {
		if strings.Contains(string(asked), forbidden) {
			t.Errorf("new monitoring still touches plane %q:\n%s", forbidden, asked)
		}
	}
	if record.ScrapeMonitorApplied {
		t.Fatal("new run claims to have applied a PodMonitor")
	}
}

func TestLiftVerifyChecksOrkaWithoutPollingPlaneTelemetry(t *testing.T) {
	for _, monitoring := range []bool{false, true} {
		for _, ready := range []bool{false, true} {
			t.Run(map[bool]string{false: "monitoring off", true: "monitoring on"}[monitoring]+"/"+map[bool]string{false: "absent", true: "ready"}[ready], func(t *testing.T) {
				f := newOrkaFixture(t, nil)
				// Any Azure query or curl request is a regression: verify has
				// no plane metric/log source to wait for, even with add-ons on.
				for _, name := range []string{"az", "curl"} {
					body := "#!/bin/sh\nprintf 'telemetry %s\\n' \"$*\" >> \"$KMX_TEST_ARGS\"\nexit 99\n"
					if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("KMX_TEST_DEPLOY_JSON", `{"items":[]}`)
				if ready {
					t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeploy(orkaChartController, nil)))
				}
				err := f.app.liftVerify(lift.Options{Observability: monitoring})
				if ready {
					if err != nil || !strings.Contains(f.errOut.String(), "arrival were not checked") || !strings.Contains(f.errOut.String(), "No model call was made") {
						t.Fatalf("verification did not distinguish readiness from inference/arrival: %v\n%s", err, f.errOut)
					}
				} else if err == nil || !strings.Contains(err.Error(), "no Orka controller Deployment") {
					t.Fatalf("missing Orka was reported ready: %v", err)
				}
				if strings.Contains(f.calls(t), "telemetry ") {
					t.Fatalf("verify polled retired telemetry:\n%s", f.calls(t))
				}
			})
		}
	}
}

func TestLiftClusterResumeStartsAtOrka(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nprintf '%s\\n' \"$KMX_LIFT_CONTINUE\" \"$KMX_LIFT_DOWN\"\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "aks-up.sh"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{}, Run: &run.Runner{Stdout: &out, Stderr: io.Discard}}
	opt := withLiftDefaults(lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "rg", Cluster: "cluster", Registry: "reg12345"})
	if err := a.liftCluster(opt, dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--step orka") || !strings.Contains(out.String(), "KAIMAHI_CONFIRM=rg") {
		t.Fatalf("provisioning lost native resume or confirmed teardown: %s", out.String())
	}
	for _, retired := range []string{"boundary", "credential", "plane"} {
		if strings.Contains(out.String(), "--step "+retired) {
			t.Errorf("provisioning advises a retired phase: %s", out.String())
		}
	}
}

func TestLiftNextStepsOfferNativeOrkaWithoutPlaneAdvice(t *testing.T) {
	for _, monitoring := range []bool{false, true} {
		a, out, _ := liftAuditApp(t)
		opt := lift.Options{Payload: lift.PayloadOrka, Cluster: "cluster", ResourceGroup: "rg", Registry: "reg12345", Observability: monitoring}
		a.aimAtTheCluster(opt)
		a.liftNextSteps(opt)
		for _, forbidden := range []string{" flow", "plane", "workbook", "dashboard", "PodMonitor"} {
			if strings.Contains(out.String(), forbidden) {
				t.Errorf("retired advice %q:\n%s", forbidden, out.String())
			}
		}
		for _, want := range []string{"orka status", "agent create", "no Provider", "COSTS MONEY", "aks down"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing native next step %q:\n%s", want, out.String())
			}
		}
	}
}
