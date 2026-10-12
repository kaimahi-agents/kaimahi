package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
)

func captureFixtureRequest(ctx context.Context, cmd *exec.Cmd) protocol.Request {
	return protocol.Request{Context: ctx, Args: cmd.Args[1:], Getenv: protocol.Environment(cmd.Environ()), Stdin: cmd.Stdin, Stdout: cmd.Stdout, Stderr: cmd.Stderr}
}
func reconcileCaptureFixture(ctx context.Context, cmd *exec.Cmd) error {
	return protocol.Reconcile(captureFixtureRequest(ctx, cmd))
}
func kagentCaptureFixture(ctx context.Context, cmd *exec.Cmd) error {
	return protocol.Kagent(captureFixtureRequest(ctx, cmd))
}

// A capture fixture must not quietly fall back to a real kubectl launch.
func forbidKubectlLaunch(t *testing.T, dir string) {
	t.Helper()
	marker := filepath.Join(dir, "unexpected-kubectl-launch")
	fakeTool(t, dir, "kubectl", "printf '%s\\n' \"$*\" >> "+shellArg(marker)+"; printf '%s\\n' 'unexpected kubectl executable launch' >&2; exit 97")
	t.Cleanup(func() {
		if raw, err := os.ReadFile(marker); err == nil {
			t.Errorf("in-process fixture launched kubectl executable: %s", raw)
		} else if !os.IsNotExist(err) {
			t.Errorf("check kubectl launch sentinel: %v", err)
		}
	})
}

func TestCaptureInProcessFixturesDoNotBuildExecutable(t *testing.T) {
	beforeDir, beforeBinary := kubectlFixtureBuild.dir, kubectlFixtureBuild.binary
	t.Run("retire", func(t *testing.T) {
		a, opt, _, _ := retireBundleFixture(t)
		opt.Plan = true
		if err := a.LiftAgentBundle(opt); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("kagent", func(t *testing.T) {
		a, opt, _ := kagentLiftFixture(t, "")
		opt.Plan = true
		if err := a.LiftAgentBundle(opt); err != nil {
			t.Fatal(err)
		}
	})
	if kubectlFixtureBuild.dir != beforeDir || kubectlFixtureBuild.binary != beforeBinary {
		t.Fatal("in-process fixture built the executable helper")
	}
}

func TestKagentBundleFixtureCapturesLiftStatusAndReuseWithoutExecutable(t *testing.T) {
	beforeDir, beforeBinary := kubectlFixtureBuild.dir, kubectlFixtureBuild.binary
	a, opt, dir := kagentLiftFixture(t, "")
	forbidKubectlLaunch(t, dir)
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if got := readKagentStatusForTest(t, a, opt); got.State != "ready" || !got.Ready {
		t.Fatalf("status = %+v, want ready", got)
	}
	beforeReuse := len(kagentCalls(t, dir))
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[beforeReuse:])
	if kubectlFixtureBuild.dir != beforeDir || kubectlFixtureBuild.binary != beforeBinary {
		t.Fatal("bundle fixture built the executable helper")
	}
}

func retireBundleFixture(t *testing.T) (*App, LiftAgentBundleOptions, string, *bytes.Buffer) {
	t.Helper()
	return liftBundleFixtureMode(t, true)
}
