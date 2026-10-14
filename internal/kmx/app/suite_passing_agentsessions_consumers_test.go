package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
)

func TestLegacySuitePassingReceiptByteGoldens(t *testing.T) {
	// Freeze random run/request identities; packaged subjects and inventory stay exact.
	golden := map[string]string{"native-orka": "5c4be87f463200c92becd7e89c029192b82e32f1a5c97a39165742bda6298962", "native-kagent": "4f2052fa4a4647714a5df5a280ac2a14f633469ede53bbf23254f1080a7ef03b"}
	for mode, r := range suiteDiffFixtures(t) {
		r.RunID = strings.Repeat("a", 32)
		for i := range r.Cases {
			if e := r.Cases[i].KagentExecution; e != nil {
				e.RPCID = fmt.Sprintf("rpc-%032d", i)
				e.RequestMessageID = fmt.Sprintf("message-%032d", i)
				e.TaskID, e.ContextID = fmt.Sprintf("task-%d", i), fmt.Sprintf("context-%d", i)
			}
		}
		raw, err := spec.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(raw))
		if got != golden[mode] {
			t.Errorf("%s byte golden: %s", mode, got)
		}
	}
}

func TestSuiteSessionsProofRefusesMutationsAndForeignConsumers(t *testing.T) {
	originalPath := os.Getenv("PATH")
	a, opt, _ := suiteSessionsEvaluationFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	r, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, p.suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"duplicate session", "duplicate execution", "mixed proof", "host version", "host digest", "profile", "S", "inventory", "assertion"} {
		t.Run(name, func(t *testing.T) {
			bad, err := parseSuitePassingReceipt(raw)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "duplicate session":
				bad.Cases[1].SessionsExecution.SessionUID = bad.Cases[0].SessionsExecution.SessionUID
			case "duplicate execution":
				bad.Cases[1].SessionsExecution.ExecutionID = bad.Cases[0].SessionsExecution.ExecutionID
			case "mixed proof":
				bad.Kagent = &suiteKagentPassingProof{}
			case "host version":
				bad.Sessions.Subject.Host.RuntimeVersion = "other"
			case "host digest":
				bad.Sessions.Subject.Host.RuntimeBinaryDigest = "unknown"
			case "profile":
				bad.Sessions.Subject.Profile.ID = "orka"
			case "S":
				bad.Sessions.Subject.SuiteDigest = "sha256:" + strings.Repeat("f", 64)
			case "inventory":
				bad.Cases[0], bad.Cases[1] = bad.Cases[1], bad.Cases[0]
			case "assertion":
				bad.Cases[0].Assertions[0].DefinitionDigest = strings.Repeat("e", 64)
			}
			if _, err := canonicalSuitePassingReceipt(t.Context(), bad, p.suite); err == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
	// Diff must refuse valid Sessions in either order, including against itself.
	legacy := suiteDiffFixtures(t)
	for mode, receipt := range legacy {
		foreign := diffWrite(t, receipt)
		for _, pair := range [][2]string{{opt.Out, foreign}, {foreign, opt.Out}, {opt.Out, opt.Out}} {
			if err := (&App{Out: io.Discard, Err: io.Discard}).DiffAgentEvaluations(EvaluationDiffOptions{Before: pair[0], After: pair[1]}); err == nil {
				t.Fatalf("%s diff accepted Sessions", mode)
			}
		}
	}
	// Exact packaged Sessions S/E is valid for push, but never target effects.
	t.Setenv("PATH", originalPath)
	orka, _, lift, _ := releaseLiftFixture(t)
	layout := filepath.Join(t.TempDir(), "layout")
	result, err := a.PushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v1", layout, false, false, SuitePushProvenance{}, opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	lift.FromLayout, lift.Reference, lift.Evidence = layout, "example.invalid/suite@"+result.Descriptor.Digest.String(), opt.Out
	calls := 0
	orka.captureCommand = func(context.Context, *exec.Cmd) error { calls++; return errors.New("unexpected target effect") }
	if plan, err := orka.PrepareReleaseLift(t.Context(), lift); plan != nil || !errors.Is(err, ErrReleaseLift) || calls != 0 {
		t.Fatalf("Sessions lift fence: %v %v calls=%d", plan, err, calls)
	}
	// Existing non-Sessions E likewise cannot authorize Sessions S.
	lift.Evidence = diffWrite(t, legacy["native-orka"])
	if plan, err := orka.PrepareReleaseLift(t.Context(), lift); plan != nil || !errors.Is(err, ErrReleaseLift) || calls != 0 {
		t.Fatalf("reverse lift fence: %v %v calls=%d", plan, err, calls)
	}
	writeFile(t, filepath.Join(opt.Workspace, "eval/second.yml"), "id: second\ninput: changed\nexpectContains: ['4']\n")
	denied := filepath.Join(t.TempDir(), "denied")
	if _, err := a.PushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v1", denied, false, false, SuitePushProvenance{}, opt.Out); err == nil {
		t.Fatal("changed S published")
	}
	if _, err := os.Stat(denied); !os.IsNotExist(err) {
		t.Fatal("refused push created layout")
	}
}
