package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

func TestDiscoveryReviewInitialRuntimeReadFailure(t *testing.T) {
	a, p, _ := toolFixture(t)
	p["/apis/apps/v1/namespaces/orka-system/deployments?limit=64"] = func(c *exec.Cmd) error {
		fmt.Fprint(c.Stderr, "Error from server (Forbidden): private")
		return errors.New("fail")
	}
	got, e := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
	if e == nil || got.Complete || len(got.Tools) != 1 || got.Tools[0].Reason != "read_denied" {
		t.Fatal(got, e)
	}
}
func TestDiscoveryReviewLateTerminalFailure(t *testing.T) {
	for _, terminal := range []error{context.DeadlineExceeded, context.Canceled} {
		a, p := targetFixture(t)
		p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/providers?limit=64"] = func(c *exec.Cmd) error {
			fmt.Fprint(c.Stderr, "Error from server (Forbidden): private")
			return errors.New("fail")
		}
		p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"] = func(*exec.Cmd) error { return terminal }
		r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
		if r.Complete || !errors.Is(e, terminal) {
			t.Fatalf("terminal=%v got=%#v", terminal, e)
		}
	}
}
