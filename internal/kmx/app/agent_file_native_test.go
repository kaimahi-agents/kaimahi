package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func fileNativeMetadata() *agentpreview.NativeExecution {
	return &agentpreview.NativeExecution{Selection: "name", Host: "ws://127.0.0.1:8080", ProtocolVersion: "0.1", Session: "ahp-session:/abcdef", Chat: "ahp-chat:/abcdef", Turn: "abcdef", ContentRevision: "sha256:" + strings.Repeat("a", 64), ActualModels: []string{"gpt-4.1"}}
}
func nativeScript(cause error) *scriptedPreview {
	return &scriptedPreview{verdict: runtime.EvaluationPass, err: cause, mutate: func(o *agentpreview.Outcome) {
		o.Journal = ""
		o.NoJournal = true
		for i := range o.Examples {
			c := &o.Examples[i].Completion
			c.DurationMs = 0
			c.Native = fileNativeMetadata()
			c.Native.Session += o.Examples[i].ID
			c.ID = c.Native.Session
			c.ToolCalls = 7
			c.Tools = []agentpreview.ToolSummary{{Name: "read", Mode: "live", Source: "live", CallCount: 7, RequestCount: 7}}
			if cause != nil {
				c.State = agentpreview.Failed
				c.Native.PendingTool = "shell"
				c.Native.PendingInput = "echo diagnostic\x1b[31m"
				o.Examples[i].Assertions = nil
			}
		}
	}}
}
func TestNativeOmittedToolsNotice(t *testing.T) {
	parsed, err := agentfile.Load(previewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	parsed.CopilotToolsOmitted = true
	for _, native := range []bool{false, true} {
		result, _ := (&App{FileRunner: nativeScript(nil)}).PreviewAgentSnapshot(t.Context(), parsed, FilePreviewOptions{File: "reader.agent.md", Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: native})
		has := false
		for _, notice := range result.Notices {
			has = has || notice == agentfile.CopilotToolsNotice
		}
		if has == native {
			t.Fatalf("native=%t embedded notice=%t", native, has)
		}
	}
}

func TestNativeObservedCallsAndTrustedPolicy(t *testing.T) {
	for _, native := range []bool{false, true} {
		runner := nativeScript(nil)
		var progress []FilePreviewCaseProgress
		// Exercise the production observer too, not just the final projection.
		observerRunner := nativeObserverRunner{runner}
		r, err := (&App{FileRunner: observerRunner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: native, CaseFinished: func(p FilePreviewCaseProgress) { progress = append(progress, p) }})
		if !native {
			if err == nil || r.ReasonCode != "invalid_completion" {
				t.Fatalf("forged embedded metadata accepted: %+v", r)
			}
			continue
		}
		if err != nil || !r.FullPass || len(progress) != 2 || r.Examples[0].ToolCalls != 7 || r.Examples[0].Native == nil {
			t.Fatalf("native result: %+v / %v / progress=%v", r, err, progress)
		}
		var summary bytes.Buffer
		if err := WriteFilePreviewGitHubSummary(&summary, r); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(summary.String(), "| 7 |") {
			t.Fatal(summary.String())
		}
	}
}

type nativeObserverRunner struct{ *scriptedPreview }

func (n nativeObserverRunner) Run(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
	o, e := n.scriptedPreview.Run(ctx, req)
	for _, r := range o.Examples {
		if req.ExampleFinished != nil {
			req.ExampleFinished(r)
		}
	}
	return o, e
}
func TestNativeFailedPendingProjection(t *testing.T) {
	for _, hide := range []bool{false, true} {
		runner := nativeScript(context.DeadlineExceeded)
		r, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: true, ShowFailedContent: !hide})
		if err == nil || r.ReasonCode != "deadline_exceeded" || r.Examples[0].Native == nil || r.Examples[0].State != "error" {
			t.Fatalf("lost partial identity: %+v", r)
		}
		n := r.Examples[0].Native
		if n.PendingTool != "shell" || hide && n.PendingInput != "" || !hide && n.PendingInput != "echo diagnostic" || len(n.ActualModels) != 1 {
			t.Fatalf("pending view: %+v", n)
		}
		runner.request.Examples = nil
		n.ActualModels[0] = "changed"
		var out bytes.Buffer
		_ = WriteFilePreview(&out, r, "text")
		if !strings.Contains(out.String(), "pending tool shell") {
			t.Fatal(out.String())
		}
		var summary bytes.Buffer
		if err := WriteFilePreviewGitHubSummary(&summary, r); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNativeChatDigestIsIdentity(t *testing.T) {
	n := fileNativeMetadata()
	n.Chat = "ahp-chat:/" + strings.Join([]string{"34449f0ecf1d574a", "b573fec561c5bec1"}, "")
	if previewNative(n, true) == nil {
		t.Fatal("provider chat identity withheld")
	}
}

func TestNativeGitRevisionIsIdentity(t *testing.T) {
	n := fileNativeMetadata()
	n.GitRevision = "1b117bf31b298f80b2dbe5d285cd855f658c1d9d"
	if previewNative(n, true) == nil {
		t.Fatal("valid Git identity withheld")
	}
}

func TestNativeFailedWithoutCompletedCalls(t *testing.T) {
	runner := nativeScript(context.DeadlineExceeded)
	original := runner.mutate
	runner.mutate = func(o *agentpreview.Outcome) {
		original(o)
		for i := range o.Examples {
			o.Examples[i].Completion.Tools = nil
			o.Examples[i].Completion.ToolCalls = 0
		}
	}
	r, _ := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: true, ShowFailedContent: true})
	if r.Examples[0].Native == nil || r.Examples[0].Native.PendingTool != "shell" {
		t.Fatalf("lost partial native identity: %+v", r)
	}
}

func TestNativeModelMismatchReason(t *testing.T) {
	runner := nativeScript(agentpreview.ErrModelUnavailable)
	previous := runner.mutate
	runner.mutate = func(o *agentpreview.Outcome) {
		previous(o)
		for i := range o.Examples {
			o.Examples[i].Completion.Native.ActualModels = []string{"actual-wrong"}
		}
	}
	r, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: true})
	if err == nil || r.ReasonCode != "native_model_mismatch" || r.Verdict != runtime.EvaluationUnknown || r.FullPass || r.ModelRoute.Model != "gpt-4.1" || r.Examples[0].Native.ActualModels[0] != "actual-wrong" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if e, ok := err.(*FilePreviewError); !ok || e.ExitCode() != 2 {
		t.Fatalf("exit: %v", err)
	}
}

func TestNativeCompleteModelAndTraceFences(t *testing.T) {
	for _, mutate := range []func(*agentpreview.NativeExecution){func(n *agentpreview.NativeExecution) { n.Selection = "" }, func(n *agentpreview.NativeExecution) { n.Selection = "path" }, func(n *agentpreview.NativeExecution) { n.ActualModels = []string{"wrong-model"} }, func(n *agentpreview.NativeExecution) { n.ActualModels = nil }, func(n *agentpreview.NativeExecution) { n.Chat = "" }, func(n *agentpreview.NativeExecution) { n.Turn = "" }} {
		runner := nativeScript(nil)
		original := runner.mutate
		runner.mutate = func(o *agentpreview.Outcome) { original(o); mutate(o.Examples[0].Completion.Native) }
		r, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: true})
		if err == nil || r.ReasonCode != "invalid_completion" {
			t.Fatalf("invalid native completion passed: %+v", r)
		}
	}
}

func TestNativeFailureExplainLabels(t *testing.T) {
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	path := previewFile(t)
	runner := nativeScript(nil)
	runner.verdict = runtime.EvaluationFail
	r, _ := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: path, RetentionFile: path, Operation: "test", Model: "gpt-4.1", DefaultCopilot: true, Copilotd: true})
	if r.Retention.Status != "retained" {
		t.Fatalf("retention: %+v", r)
	}
	var out bytes.Buffer
	if err := (&App{Out: &out}).ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: path, ID: r.Retention.Diagnostics[0].ID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "native session") || !strings.Contains(out.String(), "gpt-4.1") || strings.Contains(out.String(), "journal") {
		t.Fatal(out.String())
	}
}

func TestNativeFailureCodecCompatibility(t *testing.T) {
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	path := previewFile(t)
	_, _ = failingFilePreview(t, path)
	store, err := openFileFailureStore(path, false, retainedFileFailureCodec{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.scan()
	_ = store.Close()
	if err != nil || len(entries) == 0 {
		t.Fatalf("fixture: %v", err)
	}
	r := *entries[0].file
	old, err := marshalFileFailureRecord(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(old, []byte(`"native"`)) {
		t.Fatal("old bytes changed")
	}
	r.Native = fileNativeMetadata()
	r.Model = FilePreviewRoute{Provider: "copilot", Model: "gpt-4.1"}
	r.Native.PendingInput = "private partial input"
	if _, err = marshalFileFailureRecord(r, nil); err == nil {
		t.Fatal("retained partial input accepted")
	}
	r.Native.PendingInput = ""
	r.Native.PendingTool = ""
	raw, err := marshalFileFailureRecord(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, decoded, err := (retainedFileFailureCodec{}).decode(raw)
	if err != nil || decoded.Native == nil {
		t.Fatalf("roundtrip: %v", err)
	}
	var shape map[string]any
	_ = json.Unmarshal(raw, &shape)
	if shape["schemaVersion"] != float64(2) {
		t.Fatal(shape)
	}
}
