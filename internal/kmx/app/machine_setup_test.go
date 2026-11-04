package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestMachineSetupPhaseMapping(t *testing.T) {
	for _, row := range [][3]string{
		{"cluster.up", "Prepare kind cluster", "cluster"}, {"cluster.up", "Deploy Ollama", "ollama"},
		{"cluster.up", "Pull model", "model"}, {"cluster.up", "Install Orka", "orka"},
		{"cluster.orka.install", "Fetch the pinned installer", "installer"},
		{"cluster.orka.install", "Validate chart CRDs", "runtime"},
		{"cluster.orka.install", "Apply chart CRDs and install Orka", "runtime"},
		{"cluster.orka.install", "Provision Task result reader", "result_reader"},
		{"cluster.orka.install", "Wire a keyless Provider", "provider"},
		{"cluster.up", "private unexpected label", "other"},
	} {
		if got := MachineProvisioningStage(row[0], row[1]); got != row[2] {
			t.Fatalf("stage=%q want %q", got, row[2])
		}
	}
}

func TestMachinePhaseCancellation(t *testing.T) {
	for _, at := range []string{"before", "start", "body"} {
		t.Run(at, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := &App{Run: &run.Runner{Context: ctx}, Err: io.Discard}
			notifications, calls := 0, 0
			a := original.WithProvisioningProgress(func(_, _ string, _ error) {
				notifications++
				if at == "start" {
					cancel()
				}
			})
			if at == "before" {
				cancel()
			}
			err := a.runPhase(phase{name: "phase"}, func() error {
				calls++
				if at == "body" {
					cancel()
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel lost: %v", err)
			}
			if at != "body" && calls != 0 {
				t.Fatal("entered cancelled body")
			}
			if at == "before" && notifications != 0 {
				t.Fatal("started cancelled phase")
			}
			if original.operationProgress != nil {
				t.Fatal("mutated original App")
			}
		})
	}
}

func TestMachineProvisioningFixtures(t *testing.T) {
	for _, mode := range []string{"apply", "no_apply", "dry_run", "no_provider", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			chart := orkaChart(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.app.Run.Context = ctx
			var stages []string
			a := f.app.WithProvisioningProgress(func(label, status string, _ error) {
				if status == "active" {
					stages = append(stages, MachineProvisioningStage("cluster.orka.install", label))
					if mode == "cancel" && stages[len(stages)-1] == "runtime" {
						cancel()
					}
				}
			})
			runner := *a.Run
			runner.Stdout, runner.Stderr, runner.Echo = io.Discard, io.Discard, false
			a.Run = &runner
			a.Out, a.Err, a.Stdin = io.Discard, io.Discard, nil
			opt := OrkaOptions{NoApply: mode == "no_apply", DryRun: mode == "dry_run"}
			if mode == "no_provider" {
				opt.Provider = "-"
			}
			err := a.OrkaInstall(opt)
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || f.writes(t) != "" {
					t.Fatal("entered cancelled mutation", err, f.writes(t))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "installer,runtime,result_reader,provider"
			switch mode {
			case "no_apply":
				want = ""
			case "dry_run":
				want = "installer,runtime"
			case "no_provider":
				want = "installer,runtime,result_reader"
			}
			if strings.Join(stages, ",") != want {
				t.Fatal(stages, want)
			}
			if f.out.Len() != 0 || f.errOut.Len() != 0 {
				t.Fatal("raw output escaped copied sinks")
			}
		})
	}
	for _, mode := range []string{"apply", "no_apply", "dry_run"} {
		t.Run("offline-"+mode, func(t *testing.T) {
			original, dir, _, _ := offlineInstallApp(t)
			var raw bytes.Buffer
			original.Out, original.Err = &raw, &raw
			original.Run.Stdout, original.Run.Stderr = &raw, &raw
			notifications := 0
			a := original.WithProvisioningProgress(func(string, string, error) { notifications++ })
			runner := *a.Run
			runner.Stdout, runner.Stderr, runner.Echo = io.Discard, io.Discard, false
			a.Run = &runner
			a.Out, a.Err, a.Stdin = io.Discard, io.Discard, nil
			if err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-", NoApply: mode == "no_apply", DryRun: mode == "dry_run"}); err != nil {
				t.Fatal(err)
			}
			if notifications != 0 || raw.Len() != 0 {
				t.Fatal("invented offline phases or leaked child output", notifications, raw.String())
			}
		})
	}
}

func TestMachineUpObserver(t *testing.T) {
	a, _, _ := upFixture(t)
	var stages []string
	a = a.WithProvisioningProgress(func(label, status string, _ error) {
		if status == "active" {
			stages = append(stages, MachineProvisioningStage("cluster.up", label))
		}
	})
	if err := a.Up(""); err != nil {
		t.Fatal(err)
	}
	if strings.Join(stages, ",") != "cluster,ollama,model,orka" {
		t.Fatal(stages)
	}
}

func TestMachinePreflightKubeconfigCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", "exec /bin/sleep 2")
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	a := (&App{Run: &run.Runner{Context: ctx}}).WithProvisioningProgress(func(string, string, error) {})
	if _, err := a.kubeconfig(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("machine preflight ignored cancellation: %v", err)
	}
	fakeTool(t, dir, "kubectl", `printf '{}'`)
	legacy := &App{Run: &run.Runner{Context: ctx}}
	if _, err := legacy.kubeconfig(); err != nil {
		t.Fatal("changed legacy context behavior", err)
	}
}

func TestMachineManagedDownloadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload := []byte("fixture tool")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			fmt.Fprintf(w, "%x", sha256.Sum256(payload))
			return
		}
		w.Write(payload)
		w.(http.Flusher).Flush()
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer server.Close()
	previous := toolchainBase
	toolchainBase = server.URL
	t.Cleanup(func() { toolchainBase = previous })
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "")
	a := (&App{Run: &run.Runner{Context: ctx}, Err: io.Discard}).WithProvisioningProgress(func(string, string, error) {})
	if err := a.provision([]dependency{depKubectl}); !errors.Is(err, context.Canceled) {
		t.Fatalf("machine managed preflight ignored cancellation: %v", err)
	}
}

func TestMachineTextPhaseUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &App{Run: &run.Runner{Context: ctx}, Err: io.Discard}
	calls := 0
	if err := a.runPhase(phase{name: "text"}, func() error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatal("changed legacy context behavior", err)
	}
}
