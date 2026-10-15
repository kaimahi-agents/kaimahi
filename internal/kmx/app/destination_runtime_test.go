package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestResolvedDestinationConfigEditDuringEvaluationGuardCannotRedirect(t *testing.T) {
	a, _, fixture, _ := publicNativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ResultPort = fixture.opt.ResultPort
	path := filepath.Join(opt.Workspace, "kmx.yaml")
	writeFile(t, path, "version: 1\npublishRepository: '127.0.0.1:5001/agents'\ntargets: {dev: {context: kind-test, namespace: orka-system, runtime: orka, inference: 'provider:inference'}}\nregistries: {'127.0.0.1:5001': {plainHTTP: true}}\n")
	doc, err := config.LoadDestinations(opt.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	target, err := doc.Target("dev")
	if err != nil {
		t.Fatal(err)
	}
	repository, plainHTTP := doc.PublishRepository, doc.RegistryHTTP(doc.PublishRepository)
	opt.ToContext, opt.ToNamespace, opt.ExpectedRuntime, opt.Inference, opt.Bindings = target.Context, target.Namespace, target.Runtime, target.Inference, target.Bindings
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, frame := range p.inventory {
		fixture.answers[frame.Case.Input] = nativeCanary + " answer"
	}
	capture := a.captureCommand
	guards := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if i := slices.Index(cmd.Args, "--context"); i >= 0 && cmd.Args[i+1] != "kind-test" {
			t.Fatalf("redirected context: %v", cmd.Args)
		}
		if slices.Contains(cmd.Args, "config") {
			guards++
			writeFile(t, path, "version: 1\npublishRepository: registry.example/redirected\ntargets: {dev: {context: redirected, namespace: other, runtime: kagent, bindings: redirected.yaml}}\nregistries: {'127.0.0.1:5001': {plainHTTP: false}}\n")
		}
		return capture(ctx, cmd)
	}
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatalf("config edit changed reviewed operation: %v", err)
	}
	receipt, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if guards != 1 || receipt.Subject.Target.Context != "kind-test" || receipt.Subject.Target.Namespace != "orka-system" || p.options.Inference != "provider:inference" || p.options.ExpectedRuntime != "orka" || p.options.Bindings != "" || repository != "127.0.0.1:5001/agents" || !plainHTTP {
		t.Fatalf("frozen concrete values changed: %+v", p.options)
	}
	changed, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(changed), "redirected") {
		t.Fatal("guard did not edit config", err)
	}
}

func TestDestinationRuntimeExpectationEvaluationRefusesOffline(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	for _, runtime := range []string{"kagent", "agentsessions", "invalid"} {
		opt := nativeEvaluationOptions(t)
		opt.ExpectedRuntime = runtime
		p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
		if p != nil {
			p.Close()
		}
		if p != nil || err == nil || !strings.Contains(err.Error(), "destination runtime") {
			t.Fatalf("runtime expectation reached export/cluster preparation: %v", err)
		}
	}
}
func TestDestinationRuntimeExpectationLiftRefusesBeforeClusterReads(t *testing.T) {
	a, f, opt, _ := releaseLiftFixture(t)
	before := f.calls
	opt.ExpectedRuntime = "kagent"
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if p != nil || !errors.Is(err, ErrReleaseLift) || f.calls != before {
		t.Fatalf("runtime expectation reached cluster: %v reads %d -> %d", err, before, f.calls)
	}
	opt.ExpectedRuntime = "orka"
	if _, err := a.PrepareReleaseLift(t.Context(), opt); err != nil {
		t.Fatalf("matching expectation refused: %v", err)
	}
}
