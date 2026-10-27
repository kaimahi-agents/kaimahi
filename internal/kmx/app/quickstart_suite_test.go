package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// A changed instruction or case must never be silently replaced by quickstart's
// fixed source, and therefore must not become fresh evidence for that source.
func TestQuickstartSuiteSourceRefusesAuthoredDrift(t *testing.T) {
	for _, file := range []string{"agents/hello-world-agent/instructions.md", "eval/example.yaml"} {
		t.Run(file, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			a := &App{Out: io.Discard, Err: io.Discard, Cfg: newQuickstartConfig()}
			root, _, err := a.quickstartSuiteSource(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, file)
			edited := []byte("operator edit\n")
			if err := os.WriteFile(path, edited, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := a.quickstartSuiteSource(t.Context()); err == nil {
				t.Fatal("edited source accepted")
			}
			body, _ := os.ReadFile(path)
			if !bytes.Equal(body, edited) {
				t.Fatal("operator source was overwritten")
			}
		})
	}
}

func TestQuickstartSuiteSourceReusesPreGroupingWorkspace(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	a := &App{Out: io.Discard, Err: io.Discard, Cfg: newQuickstartConfig()}
	base, err := quickstartSuiteState()
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte(scaffold.DefaultOrkaInstructions(QuickstartAgent)), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, QuickstartAgent)
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{
		Name: QuickstartAgent, Model: a.Cfg.Model,
		Description:      "The agent kmx quickstart deploys to answer a first question",
		InstructionsFile: input,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	gotRoot, gotDigest, err := a.quickstartSuiteSource(t.Context())
	if err != nil || gotRoot != root || gotDigest != before.Descriptor.Digest.String() {
		t.Fatalf("pre-grouping fixed source not reused unchanged: root=%q digest=%q err=%v", gotRoot, gotDigest, err)
	}
}

func TestQuickstartCreateOptionsRetainsSourceDescription(t *testing.T) {
	a := &App{Cfg: newQuickstartConfig()}
	if got := a.quickstartCreateOptions().Description; got != "The agent kmx quickstart deploys to answer a first question" {
		t.Fatalf("persisted Quickstart description changed: %q", got)
	}
}

func TestQuickstartSuiteSourceExactReuse(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	a := &App{Out: io.Discard, Err: io.Discard, Cfg: newQuickstartConfig()}
	root, _, err := a.quickstartSuiteSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := a.quickstartSuiteSource(t.Context())
	if err != nil || root != second {
		t.Fatalf("unchanged source not reusable: %v", err)
	}
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := workspace.Load(t.Context(), p)
	if err != nil || s.MemberID != "hello-world-agent" || len(s.Cases) != 1 || s.Model != "qwen2.5:3b" {
		t.Fatalf("unexpected fixed native source: %v", err)
	}
	if strings.Contains(string(s.Instructions), "k8s-get-resources") {
		t.Fatal("tool shortcut entered immutable suite")
	}
	a.Cfg.Model = "different-model"
	if _, _, err := a.quickstartSuiteSource(t.Context()); err == nil {
		t.Fatal("configured model change overwrote source")
	}
}

// Exercise real authored-source preparation, evaluation, receipt-file publication
// and merged read-only lift planning. Only cluster/result transport is simulated.
func TestQuickstartPreparesExactSuiteLift(t *testing.T) {
	a, _, _, f := nativeEvaluationFixture(t)
	reads := f.nativeReads
	source := reads.objects["providers.core.orka.ai/inference"]
	source["apiVersion"] = "core.orka.ai/v1alpha1"
	source["spec"].(map[string]any)["secretRef"].(map[string]any)["name"] = "inference-provider-key"
	secret := reads.objects["secret/model-secret"]
	secret["metadata"].(map[string]any)["name"] = "inference-provider-key"
	reads.objects["secret/inference-provider-key"] = secret
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "replace") && slices.Contains(cmd.Args, "--dry-run=server") {
			raw, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				return err
			}
			cmd.Stdin = bytes.NewReader(raw)
			var doc map[string]any
			if json.Unmarshal(raw, &doc) == nil && doc["metadata"].(map[string]any)["name"] == "inference" {
				return json.NewEncoder(cmd.Stdout).Encode(doc)
			}
		}
		return capture(ctx, cmd)
	}
	a.Cfg.Model = "packaged-model"
	a.Cfg.KubeContext = "kind-test"
	// The shared reconcile fixture stores one pair by kind; its seeded greeter
	// is a different agent, not a quickstart collision.
	for _, kind := range []string{"providers.core.orka.ai", "agents.core.orka.ai"} {
		if err := os.Remove(filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), kind+".json")); err != nil {
			t.Fatal(err)
		}
	}
	root, _, err := a.quickstartSuiteSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	f.answers["What is 2+2?"] = "4"
	var stdout bytes.Buffer
	a.Out = &stdout
	first, err := a.prepareQuickstartSuite(t.Context(), "provider:inference", f.opt.ResultPort)
	if err != nil {
		t.Fatalf("%v; diagnostics: %s", err, a.Err.(*bytes.Buffer).String())
	}
	if first.Plan.Summary().SuiteDigest != prepared.Descriptor.Digest.String() || first.Plan.Summary().Member != "hello-world-agent" || first.Plan.Summary().Gate != "same-subject" || first.Plan.Summary().Target.Context != "kind-test" {
		t.Fatalf("planning did not retain evaluated S: %+v", first.Plan.Summary())
	}
	if stdout.Len() != 0 {
		t.Fatal("preparation polluted answer/JSON stdout")
	}
	// This is local up's Provider writer, between fresh evaluations. It must
	// normalize and reuse the source instead of applying bookkeeping over it.
	if err := a.orkaProvider(OrkaOptions{Provider: "inference", Model: "provider-default", ModelURL: "https://model.example.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	second, err := a.prepareQuickstartSuite(t.Context(), "provider:inference", f.opt.ResultPort)
	if err != nil {
		t.Fatal(err)
	}
	if first.Evidence == second.Evidence || first.Plan.Summary().SuiteDigest != second.Plan.Summary().SuiteDigest {
		t.Fatal("rerun reused evidence export or changed S")
	}
	third, err := a.prepareQuickstartSuite(t.Context(), "provider:inference", f.opt.ResultPort)
	if err != nil {
		t.Fatal(err)
	}
	runs, tasks, taskUIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, path := range []string{first.Evidence, second.Evidence, third.Evidence} {
		receipt, err := readSuitePassingReceipt(t.Context(), path)
		if err != nil {
			t.Fatalf("incomplete evidence: %v", err)
		}
		if runs[receipt.RunID] || tasks[receipt.Cases[0].TaskName] || taskUIDs[receipt.Cases[0].TaskUID] {
			t.Fatal("reused evaluation attribution or receipt identity")
		}
		runs[receipt.RunID], tasks[receipt.Cases[0].TaskName], taskUIDs[receipt.Cases[0].TaskUID] = true, true, true
		if receipt.Subject.Binding.Provider.ResourceVersion != "1" {
			t.Fatal("runtime writer changed source binding identity")
		}
	}
	for _, row := range third.Plan.Summary().Resources {
		if row.Action != "reuse" {
			t.Fatalf("third evaluation did not reuse placement: %+v", row)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "agent.yaml")); !os.IsNotExist(err) {
		t.Fatal("caller manufactured a legacy bundle")
	}
}

func TestNativePlacementRefusalPreservesIdentityWithoutPayload(t *testing.T) {
	for _, cancellation := range []error{nil, context.Canceled, context.DeadlineExceeded} {
		cause := errors.Join(&suiteNativeReuseRefusal{"Provider", "sample"}, fmt.Errorf("%s", nativeCanary), cancellation)
		got := suiteEvaluationError(t.Context(), cause, true)
		var refusal *suiteNativeReuseRefusal
		if !errors.As(got, &refusal) || !errors.Is(got, errSuiteNativeEvaluation) || cancellation != nil && !errors.Is(got, cancellation) {
			t.Fatalf("lost refusal or cancellation identity: %v", got)
		}
		if strings.Contains(got.Error(), nativeCanary) || strings.Contains(got.Error(), "outcome unknown") {
			t.Fatal("payload escaped or placement phase lost")
		}
	}
	got := suiteEvaluationError(t.Context(), fmt.Errorf("%s", nativeCanary), true)
	if strings.Contains(got.Error(), nativeCanary) {
		t.Fatal("untrusted diagnostic escaped")
	}
}

func TestQuickstartSuiteEvaluationStopsBeforeTasksOnDrift(t *testing.T) {
	a, _, _, f := nativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ResultPort = f.opt.ResultPort
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, frame := range p.inventory {
		f.answers[frame.Case.Input] = nativeCanary + " answer"
	}
	writes := 0
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "-f") && !slices.Contains(cmd.Args, "--dry-run=server") {
			writes++
		}
		return capture(ctx, cmd)
	}
	// Existing fixture owns greeter with a different packaged revision; strict
	// eval must refuse rather than replace it and then run the new cases.
	err = a.evaluateSuiteWorkspace(t.Context(), p, true)
	if err == nil || writes != 0 {
		t.Fatalf("strict eval changed drifted pair or sent a Task: writes=%d err=%v", writes, err)
	}
	if !strings.Contains(err.Error(), "Provider/greeter differs from the reviewed suite") {
		t.Fatalf("placement refusal was replaced by an unattributed evaluation error: %v", err)
	}
	if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
		t.Fatal("refused evaluation exported evidence")
	}
}
