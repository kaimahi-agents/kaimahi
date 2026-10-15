package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func offlineMultiWorkspace(t *testing.T) string {
	t.Helper()
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "Answer briefly.\n")
	roots := []string{t.TempDir(), t.TempDir()}
	for i, name := range []string{"first", "second"} {
		if err := workspace.Create(t.Context(), roots[i], workspace.CreateOptions{Name: name, Model: "exact-model", InstructionsFile: input}); err != nil {
			t.Fatal(err)
		}
	}
	move := func(from, to string) {
		t.Helper()
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
	}
	root := roots[0]
	move(filepath.Join(root, "eval"), filepath.Join(root, "first-cases"))
	if err := os.Mkdir(filepath.Join(root, "eval"), 0700); err != nil {
		t.Fatal(err)
	}
	move(filepath.Join(root, "first-cases"), filepath.Join(root, "eval/first"))
	move(filepath.Join(roots[1], "agents/second"), filepath.Join(root, "agents/second"))
	move(filepath.Join(roots[1], "eval"), filepath.Join(root, "eval/second"))
	writeFile(t, filepath.Join(root, "workspace.yaml"), "schemaVersion: 2\nname: pair\nmembers: [agents/first/member.yaml, agents/second/member.yaml]\nbuildProfile: build-profiles/default.json\n")
	return root
}

func TestMultiMemberWorkspaceOperationsRefuseWithoutEffects(t *testing.T) {
	root := offlineMultiWorkspace(t)
	for _, operation := range []string{"eval", "development eval", "push", "evaluated push", "managed push", "unevaluated push", "review", "lift"} {
		t.Run(operation, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "absent-state")
			t.Setenv("KMX_HOME", state)
			output := filepath.Join(t.TempDir(), "absent-output")
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics}
			var err error
			switch operation {
			case "eval", "development eval":
				opt := SuiteWorkspaceEvaluationOptions{Workspace: root, ToContext: "kind-test", Inference: "provider:inference", Out: output}
				if operation == "development eval" {
					opt.ToContext, opt.Inference, opt.DevelopmentHost = "", "", "missing-host.json"
				}
				_, err = PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			case "push":
				_, err = a.PushSuiteWorkspace(t.Context(), root, "team:v1", output, false, false, SuitePushProvenance{})
			case "evaluated push":
				_, err = a.PushSuiteWorkspaceEvaluated(t.Context(), root, "team:v1", output, false, false, SuitePushProvenance{}, "missing-proof.json")
			case "managed push", "unevaluated push":
				_, err = a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: root, Reference: "team:v1", Layout: output, Evaluation: "missing-proof.json"})
				if operation == "unevaluated push" {
					_, err = a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: root, Reference: "team:v1", Layout: output, Unevaluated: true})
				}
			case "review":
				_, err = a.reviewSuiteWorkspace(t.Context(), root)
			case "lift":
				_, err = PrepareWorkspaceLift(t.Context(), WorkspaceLiftOptions{Workspace: root, Repository: "registry.example/team", EvalName: "dev", DestinationName: "prod", Evaluation: SuiteWorkspaceEvaluationOptions{ToContext: "kind-dev", Inference: "provider:inference"}, Destination: ReleaseLiftOptions{ToContext: "kind-prod", Inference: "provider:inference"}})
			}
			if !errors.Is(err, workspace.ErrMultiMemberUnsupported) || !strings.Contains(err.Error(), "multi-member") || !strings.Contains(err.Error(), "offline") {
				t.Fatalf("expected safe multi-member offline-only refusal, got %v", err)
			}
			for _, absent := range []string{state, output} {
				if _, err := os.Lstat(absent); !os.IsNotExist(err) {
					t.Fatalf("refusal created %s: %v", absent, err)
				}
			}
			if out.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatalf("refusal emitted operation output: %q %q", out.String(), diagnostics.String())
			}
		})
	}
}

func TestMultiMemberArtifactLiftRefusesBeforeTargetObservation(t *testing.T) {
	a, fixture, opt, _ := releaseLiftFixture(t)
	prepared, err := workspace.Prepare(t.Context(), offlineMultiWorkspace(t), filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	opt.Reference, opt.FromLayout = "suite@"+prepared.Descriptor.Digest.String(), prepared.Path
	unrelated := t.TempDir()
	writeFile(t, filepath.Join(unrelated, "workspace.yaml"), "broken: [\n")
	t.Chdir(unrelated)
	before := fixture.calls
	plan, err := a.PrepareReleaseLift(t.Context(), opt)
	if plan != nil || !errors.Is(err, ErrReleaseLift) || !errors.Is(err, workspace.ErrMultiMemberUnsupported) {
		t.Fatalf("multi artifact did not retain safe refusal identities: %v", err)
	}
	if fixture.calls != before {
		t.Fatal("multi-member artifact reached target observation")
	}
}

func TestMultiMemberRefusalPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PrepareSuiteWorkspaceEvaluation(ctx, SuiteWorkspaceEvaluationOptions{Workspace: offlineMultiWorkspace(t)})
	if err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}
