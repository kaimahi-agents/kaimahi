package app

import (
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
