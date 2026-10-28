package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type pairPreviewRunner func(context.Context, agentpreview.Request) (agentpreview.Outcome, error)

func (f pairPreviewRunner) Run(c context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	return f(c, r)
}

func previewPair(t *testing.T) *agentfile.Pair {
	t.Helper()
	file := previewFile(t)
	tests := filepath.Join(filepath.Dir(file), "chosen.md")
	if err := os.WriteFile(tests, []byte("---\nschemaVersion: 1\n---\nPrivate prose\n```yaml agent-test\nid: external\ninput: external input\nassertions:\n  - id: check\n    type: contains\n    value: wanted\n```\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := agentfile.LoadPair(file, tests)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pairedScript(t *testing.T, verdict runtime.EvaluationVerdict, mutate func(*agentpreview.Outcome)) agentpreview.Runner {
	t.Helper()
	return pairPreviewRunner(func(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
		fake := &scriptedPreview{verdict: verdict}
		out, err := fake.Run(ctx, req)
		for i := range out.Examples {
			out.Examples[i].Completion.SnapshotRevision = req.SnapshotRevision
			if req.SkipAssertions {
				out.Examples[i].Assertions = nil
			}
		}
		if out.AdHoc != nil {
			out.AdHoc.SnapshotRevision = req.SnapshotRevision
		}
		if mutate != nil {
			mutate(&out)
		}
		return out, err
	})
}

func TestFilePairExecutionContract(t *testing.T) {
	for _, mode := range []string{"test", "run", "adhoc"} {
		t.Run(mode, func(t *testing.T) {
			p := previewPair(t)
			original := *p.Agent
			original.Examples = agentfile.CloneExamples(p.Agent.Examples)
			opt := FilePreviewOptions{File: p.AgentPath, Operation: "test"}
			if mode != "test" {
				opt.Operation = "run"
				opt.SkipAssertions = true
			}
			if mode == "adhoc" {
				input := "hello"
				opt.Input = &input
				opt.AdHocOnly = true
			}
			inner := pairedScript(t, runtime.EvaluationFail, nil)
			app := &App{FileRunner: pairPreviewRunner(func(ctx context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
				if r.SourceAgent != p.Agent || r.Agent == p.Agent || r.Agent.Revision != p.Agent.Revision || r.Agent.Instructions != p.Agent.Instructions || r.SnapshotRevision != p.SnapshotRevision || len(r.SourceText) != 3 || !strings.Contains(r.SourceText[2], "Private prose") {
					t.Fatal("source contract lost")
				}
				if mode != "adhoc" && (len(r.Examples) != 1 || r.Examples[0].ID != "external") {
					t.Fatal("wrong selected examples")
				}
				return inner.Run(ctx, r)
			})}
			r, err := app.PreviewAgentPairSnapshot(t.Context(), p, opt)
			if mode == "test" {
				if err == nil || r.Verdict != runtime.EvaluationFail {
					t.Fatalf("test = %+v %v", r, err)
				}
			} else if err != nil || r.Verdict != runtime.EvaluationUnknown || r.FullPass {
				t.Fatalf("run = %+v %v", r, err)
			}
			if r.SnapshotRevision != p.SnapshotRevision || r.File.Revision != p.Agent.Revision || r.Tests == nil || r.Tests.Revision != p.Tests.Revision {
				t.Fatal("attribution lost")
			}
			if !reflect.DeepEqual(original, *p.Agent) {
				t.Fatal("original agent mutated")
			}
		})
	}
}

func TestFilePairCompletionAndRedaction(t *testing.T) {
	for _, mode := range []string{"missing", "wrong", "credentials"} {
		t.Run(mode, func(t *testing.T) {
			p := previewPair(t)
			runner := pairedScript(t, runtime.EvaluationPass, func(o *agentpreview.Outcome) {
				o.Examples[0].Completion.SnapshotRevision = ""
				if mode == "wrong" {
					o.Examples[0].Completion.SnapshotRevision = p.Agent.Revision
				}
			})
			if mode == "credentials" {
				runner = pairPreviewRunner(func(context.Context, agentpreview.Request) (agentpreview.Outcome, error) {
					return agentpreview.Outcome{}, agentpreview.ErrCredentialContent
				})
			}
			r, _ := (&App{FileRunner: runner}).PreviewAgentPairSnapshot(t.Context(), p, FilePreviewOptions{File: p.AgentPath, Operation: "test"})
			if mode == "credentials" {
				if r.Tests != nil || r.RecordingTarget != nil || r.SnapshotRevision != "" || r.SavedSnapshotRevision != "" || !redactedFilePreviewSummary(r) {
					t.Fatalf("redaction: %+v", r)
				}
			} else if r.ReasonCode != "invalid_completion" {
				t.Fatalf("accepted wrong attribution: %+v", r)
			}
		})
	}
}

func TestFilePairRetentionCurrentness(t *testing.T) {
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	p := previewPair(t)
	opt := WithPairFilePreviewRetention(FilePreviewOptions{File: p.AgentPath, Operation: "test"}, p)
	result, _ := (&App{FileRunner: pairedScript(t, runtime.EvaluationFail, nil)}).PreviewAgentPairSnapshot(t.Context(), p, opt)
	if result.Retention.Status != "retained" {
		t.Fatalf("retention: %+v", result)
	}
	explain := func(tests, status string) {
		t.Helper()
		var out bytes.Buffer
		err := (&App{Out: &out}).ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: p.AgentPath, TestsFile: tests})
		if err != nil || !strings.Contains(out.String(), status) {
			t.Fatalf("Explain %s: %s %v", status, out.String(), err)
		}
	}
	explain(p.Tests.Selection.Path, "current")
	explain("", "historical")
	raw, err := os.ReadFile(p.Tests.Selection.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Tests.Selection.Path, append(raw, []byte("changed prose\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	explain(p.Tests.Selection.Path, "historical")
}

func TestFilePairSaveAcknowledgement(t *testing.T) {
	p := previewPair(t)
	target := FileRecordingTarget{Role: "tests", Path: p.Tests.Selection.Path, SelectionKey: p.Tests.Selection.Key}
	good := PairRecordingSaveResult{Revision: "sha256:" + strings.Repeat("a", 64), SnapshotRevision: "sha256:" + strings.Repeat("b", 64), AgentRevision: p.Agent.Revision, Target: target}
	if err := validatePairRecordingSave(p, good); err != nil {
		t.Fatal(err)
	}
	// The raw tests target may equal the raw agent hash; roles, not cross-source inequality, govern it.
	sameRaw := good
	sameRaw.Revision = p.Agent.Revision
	if err := validatePairRecordingSave(p, sameRaw); err != nil {
		t.Fatal("compared tests revision to agent revision")
	}
	for _, field := range []string{"role", "path", "key", "revision", "snapshot", "agent"} {
		t.Run(field, func(t *testing.T) {
			bad := good
			switch field {
			case "role":
				bad.Target.Role = "agent"
			case "path":
				bad.Target.Path = p.AgentPath
			case "key":
				bad.Target.SelectionKey = "wrong"
			case "revision":
				bad.Revision = p.Tests.Revision
			case "snapshot":
				bad.SnapshotRevision = p.SnapshotRevision
			case "agent":
				bad.AgentRevision = good.Revision
			}
			if !errors.Is(validatePairRecordingSave(p, bad), agentfile.ErrRecordingSave) {
				t.Fatal("accepted invalid acknowledgement")
			}
		})
	}
}
