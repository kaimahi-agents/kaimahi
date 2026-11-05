package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestSourceProfileDeployReadKeepsEvidenceGate(t *testing.T) {
	a, _, opt, _ := releaseLiftFixture(t)
	input := workspace.MappedInput{SuiteName: "greeter", MemberID: "greeter", Model: "qwen-3.5-2b", Protocol: "openai-compatible", Runtime: workspace.RuntimeProfile{ID: "orka"}, Instructions: []byte("Answer briefly.\n"), Cases: []runtime.EvaluationCase{{ID: "greeting", Input: "Ada", ExpectContains: []string{"Ada"}}}, Sources: workspace.SourceBundle{Format: "kmx.orka-agent-markdown", SchemaVersion: 1, Agent: []byte("# original agent\n"), Tests: []byte("# original tests\n")}}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.CompileMapped(t.Context(), input, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	opt.FromLayout = filepath.Join(parent, "copy")
	if _, err := p.CopyToLayout(t.Context(), opt.FromLayout); err != nil {
		t.Fatal(err)
	}
	opt.Reference = "suite@" + p.Descriptor().Digest.String()
	in, err := readReleaseLiftInputs(t.Context(), opt)
	if in.suite == nil || in.suite.Digest != p.Descriptor().Digest.String() || err == nil {
		t.Fatal("source read did not preserve canonical evidence gate", err)
	}
	calls := 0
	a.captureCommand = func(context.Context, *exec.Cmd) error { calls++; return errors.New("unexpected runtime command") }
	if _, err := a.PrepareReleaseLift(t.Context(), opt); err == nil || calls != 0 {
		t.Fatal("mismatched evidence reached runtime", calls, err)
	}
	opt.Evidence = filepath.Join(parent, "missing.json")
	if _, err := a.PrepareReleaseLift(t.Context(), opt); err == nil || calls != 0 {
		t.Fatal("missing evidence reached runtime", calls, err)
	}
}
