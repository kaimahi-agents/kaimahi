//go:build !windows

package app

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

func TestTaskResultUnixPreservesKubercCredentialPolicy(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KUBERC", "/operator/credential-policy")
	t.Setenv("KUBECTL_KUBERC", "true")
	capture := f.app.captureCommand
	f.app.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if !slices.Contains(cmd.Environ(), "KUBERC=/operator/credential-policy") || !slices.Contains(cmd.Environ(), "KUBECTL_KUBERC=true") {
			t.Fatalf("Task Result bypassed operator kuberc policy: %v", cmd.Environ())
		}
		if capture != nil {
			return capture(ctx, cmd)
		}
		return cmd.Run()
	}
	if err := f.app.TaskResult(opt); err != nil {
		t.Fatal(err)
	}
}
