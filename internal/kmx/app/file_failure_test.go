package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func failingFilePreview(t *testing.T, filename string) (FilePreviewResult, error) {
	t.Helper()
	parsed, err := agentfile.Load(filename)
	if err != nil {
		t.Fatal(err)
	}
	opt := WithFilePreviewRetention(FilePreviewOptions{File: filename, Operation: "test"}, filename, parsed.Revision)
	return (&App{FileRunner: &scriptedPreview{verdict: runtime.EvaluationFail}}).PreviewAgentFile(t.Context(), opt)
}

func TestFileFailureRetentionExplainRevisionAndIsolation(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	first := previewFile(t)
	second := filepath.Join(filepath.Dir(first), "second.agent.md")
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	result, previewErr := failingFilePreview(t, first)
	var status *FilePreviewError
	if !errors.As(previewErr, &status) || status.ExitCode() != 2 || result.Retention.Status != "retained" || len(result.Retention.Diagnostics) != 2 {
		t.Fatalf("retained result = %+v, %v", result, previewErr)
	}
	id := result.Retention.Diagnostics[0].ID
	var out bytes.Buffer
	application := &App{Out: &out}
	if err := application.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: first, ID: id}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{id, result.File.Revision, `agent="greeter"`, "fake/fake", "current", "definition a447"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("explain missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "PRIVATE ANSWER") || strings.Contains(out.String(), "PRIVATE OPERAND") {
		t.Fatalf("default explain leaked content: %s", out.String())
	}
	out.Reset()
	if err := application.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: second}); err != nil || out.String() != "No retained file failures.\n" {
		t.Fatalf("second file saw first history: %q, %v", out.String(), err)
	}
	if err := os.WriteFile(first, append(raw, []byte("\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := application.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: first, ID: id, ShowContent: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "historical") || !strings.Contains(out.String(), "PRIVATE ANSWER") || !strings.Contains(out.String(), "PRIVATE OPERAND") {
		t.Fatalf("edited-file explanation = %s", out.String())
	}
	if err := os.WriteFile(first, []byte("not a valid charter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := application.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: first, ID: id}); err != nil || !strings.Contains(out.String(), "historical") {
		t.Fatalf("invalid edited source hid history: %q, %v", out.String(), err)
	}
	if err := os.Rename(first, first+".moved"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := application.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: first, ID: id}); err == nil || !strings.Contains(err.Error(), "existing readable agent file") {
		t.Fatalf("missing source accepted: %q, %v", out.String(), err)
	}
	if err := os.Rename(first+".moved", first); err != nil {
		t.Fatal(err)
	}
	if err := application.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: second, ID: id, Clear: true}); !errors.Is(err, errSuiteFailureMissing) {
		t.Fatalf("cross-file clear accepted: %v", err)
	}
	out.Reset()
	if err := application.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: first, ID: id, Clear: true}); err != nil || !strings.Contains(out.String(), "Cleared 1") {
		t.Fatalf("targeted clear = %q, %v", out.String(), err)
	}
}

func TestFileFailureRetentionDoesNotChangeOutcome(t *testing.T) {
	path := previewFile(t)
	t.Setenv("KMX_HOME", filepath.Join(path, "unsafe-state"))
	result, err := failingFilePreview(t, path)
	var status *FilePreviewError
	if !errors.As(err, &status) || status.ExitCode() != 2 || result.State != "complete" || result.Verdict != runtime.EvaluationFail || result.Retention.Status != "unavailable" {
		t.Fatalf("retention changed outcome: %+v, %v", result, err)
	}
	if len(result.Retention.Diagnostics) != 0 {
		t.Fatalf("failed pre-publication invented IDs: %+v", result.Retention)
	}
}

func TestFileFailureRetentionKeepsFailedMixedOutcome(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	parsed, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Examples[0].Assertions = append(parsed.Examples[0].Assertions, runtime.EvaluationAssertion{ID: "judge", Type: "llmJudge", Rubric: "grounded", MinScore: func() *int { n := 80; return &n }(), JudgeModel: "fake", JudgeVersion: "v1"})
	runner := &scriptedPreview{verdict: runtime.EvaluationFail, mutate: func(out *agentpreview.Outcome) {
		out.Examples[0].Assertions[1].Verdict = runtime.EvaluationUnknown
	}}
	opt := WithFilePreviewRetention(FilePreviewOptions{File: path, Operation: "test", CaseID: "first"}, path, parsed.Revision)
	result, previewErr := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), parsed, opt)
	var status *FilePreviewError
	if !errors.As(previewErr, &status) || status.ExitCode() != 2 || result.Verdict != runtime.EvaluationFail || result.Retention.Status != "retained" || len(result.Retention.Diagnostics) != 1 {
		t.Fatalf("mixed outcome = %+v, %v", result, previewErr)
	}
	store, err := openFileFailureStore(path, false, retainedFileFailureCodec{})
	if err != nil {
		t.Fatal(err)
	}
	entries, scanErr := store.scan()
	closeErr := store.Close()
	if scanErr != nil || closeErr != nil || len(entries) != 1 || entries[0].file == nil || entries[0].file.Detail.Verdict != "fail" {
		t.Fatalf("mixed retained record = %+v, %v, %v", entries, scanErr, closeErr)
	}
}

func TestFileFailureRetentionAllowsStateBelowStandaloneParent(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "standalone.agent.md")
	source, err := os.ReadFile(previewFile(t))
	if err != nil || os.WriteFile(path, source, 0o600) != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	result, previewErr := failingFilePreview(t, path)
	var status *FilePreviewError
	if !errors.As(previewErr, &status) || status.ExitCode() != 2 || result.Retention.Status != "retained" || len(result.Retention.Diagnostics) != 2 {
		t.Fatalf("nested state retention = %+v, %v", result.Retention, previewErr)
	}
}

func TestFileFailureRetentionRefusesUnsafeRouteIdentity(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	route := modelaccess.Route{Provider: "compatible", Model: "github_pat_" + strings.Repeat("a", 82), Endpoint: "http://127.0.0.1:1234", Auth: "none"}
	parsed, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	opt := WithFilePreviewRetention(FilePreviewOptions{File: path, Operation: "test", Route: &route, Model: route.Model}, path, parsed.Revision)
	result, previewErr := (&App{FileRunner: &scriptedPreview{verdict: runtime.EvaluationFail}}).PreviewAgentFile(t.Context(), opt)
	var status *FilePreviewError
	if !errors.As(previewErr, &status) || status.ExitCode() != 1 || result.Retention.Status != "not_requested" || result.ReasonCode != "invalid_options" {
		t.Fatalf("unsafe route reached retention: %+v, %v", result, previewErr)
	}
}

func TestFileFailureRetentionRefusesInvocationBeyondStoreCapacity(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	parsed, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for len(parsed.Examples) < maxSuiteFailureRecords+1 {
		copy := parsed.Examples[0]
		copy.ID = fmt.Sprintf("extra-%d", len(parsed.Examples))
		parsed.Examples = append(parsed.Examples, copy)
	}
	opt := WithFilePreviewRetention(FilePreviewOptions{File: path, Operation: "test"}, path, parsed.Revision)
	result, previewErr := (&App{FileRunner: &scriptedPreview{verdict: runtime.EvaluationFail}}).PreviewAgentSnapshot(t.Context(), parsed, opt)
	var status *FilePreviewError
	if !errors.As(previewErr, &status) || status.ExitCode() != 2 || result.Retention.Status != "unavailable" || len(result.Retention.Diagnostics) != 0 {
		t.Fatalf("oversized invocation retention = %+v, %v", result.Retention, previewErr)
	}
	if _, err := os.Lstat(filepath.Join(os.Getenv("KMX_HOME"), "eval-failures")); !os.IsNotExist(err) {
		t.Fatalf("oversized invocation created partial state: %v", err)
	}
}

func TestFileFailureRetentionKeepsFrozenSubjectAfterParentReplacement(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	parsed, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	oldParent := filepath.Dir(path)
	moved := oldParent + "-moved"
	replacement := &scriptedPreview{verdict: runtime.EvaluationFail, mutate: func(*agentpreview.Outcome) {
		if err := os.Rename(oldParent, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(oldParent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	opt := WithFilePreviewRetention(FilePreviewOptions{File: path, Operation: "test", CaseID: "first"}, path, parsed.Revision)
	result, previewErr := (&App{FileRunner: replacement}).PreviewAgentSnapshot(t.Context(), parsed, opt)
	var status *FilePreviewError
	if !errors.As(previewErr, &status) || status.ExitCode() != 2 || result.Retention.Status != "retained" || len(result.Retention.Diagnostics) != 1 {
		t.Fatalf("retention = %+v, %v", result.Retention, previewErr)
	}
	if _, err := openFileFailureStore(path, false, retainedFileFailureCodec{}); !errors.Is(err, errSuiteFailureMissing) {
		t.Fatalf("replacement subject received old result: %v", err)
	}
	original := filepath.Join(moved, filepath.Base(path))
	store, err := openFileFailureStore(original, false, retainedFileFailureCodec{})
	if err != nil {
		t.Fatal(err)
	}
	entries, scanErr := store.scan()
	closeErr := store.Close()
	if scanErr != nil || closeErr != nil || len(entries) != 1 || entries[0].record.Detail.ID != result.Retention.Diagnostics[0].ID {
		t.Fatalf("frozen subject lost: %+v, %v, %v", entries, scanErr, closeErr)
	}
}

func TestFileFailureRetentionReportsMissingScopeOnlyForCompletedNonPass(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	parsed, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte("\n"), []byte("not the parsed snapshot")...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		verdict   runtime.EvaluationVerdict
		cause     error
		retention string
		exit      int
	}{
		{"pass", runtime.EvaluationPass, nil, "not_requested", 0},
		{"fail", runtime.EvaluationFail, nil, "unavailable", 2},
		{"cancel", runtime.EvaluationPass, context.Canceled, "not_requested", 130},
		{"execution", runtime.EvaluationPass, errors.New("private execution error"), "not_requested", 1},
		{"journal", runtime.EvaluationPass, agentpreview.ErrJournalFailed, "not_requested", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := WithFilePreviewRetention(FilePreviewOptions{File: path, Operation: "test"}, path, parsed.Revision)
			result, previewErr := (&App{FileRunner: &scriptedPreview{verdict: tc.verdict, err: tc.cause}}).PreviewAgentSnapshot(t.Context(), parsed, opt)
			exit := 0
			if previewErr != nil {
				var status *FilePreviewError
				if !errors.As(previewErr, &status) {
					t.Fatalf("unexpected error type: %v", previewErr)
				}
				exit = status.ExitCode()
			}
			if exit != tc.exit || result.Retention.Status != tc.retention || len(result.Retention.Diagnostics) != 0 {
				t.Fatalf("mismatched snapshot retention = %+v, %v", result.Retention, previewErr)
			}
		})
	}
}

func TestFileFailureStoreUnresolvedVisibilityKeepsDiagnosticID(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	result, _ := failingFilePreview(t, path)
	store, err := openFileFailureStore(path, false, retainedFileFailureCodec{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.scan()
	if err != nil || len(entries) == 0 || entries[0].file == nil {
		_ = store.Close()
		t.Fatal(entries, err)
	}
	record := *entries[0].file
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := clearFileFailures(t.Context(), path, ""); err != nil {
		t.Fatal(err)
	}
	store, err = openFileFailureStore(path, true, retainedFileFailureCodec{
		revision: record.Revision, agentID: record.AgentID, operation: record.Operation,
		selection: record.Selection, model: record.Model, judge: record.Judge, assertions: record.Assertions,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.publish = func(context.Context, *fileaccess.Directory, string, []byte, string) (fileaccess.PublicationResult, error) {
		return fileaccess.PublicationResult{VisibilityKnown: false}, errors.New("injected uncertainty")
	}
	record.Detail.ID = ""
	id, err := store.save(t.Context(), record.Detail)
	if !suiteFailureID.MatchString(id) || !errors.Is(err, errSuiteFailureStore) || len(result.Retention.Diagnostics) == 0 {
		t.Fatalf("unresolved file publication = %q, %v", id, err)
	}
}

func TestFileFailureStoreInterruptedWriteAndScopedClear(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	result, _ := failingFilePreview(t, path)
	store, err := openFileFailureStore(path, false, retainedFileFailureCodec{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.scan()
	storePath := store.path
	if closeErr := store.Close(); err != nil || closeErr != nil || len(entries) != 2 {
		t.Fatalf("scan = %+v, %v, %v", entries, err, closeErr)
	}
	pending := filepath.Join(storePath, suiteFailurePendingName)
	directory, err := fileaccess.OpenPrivateDirectory(t.Context(), storePath)
	if err != nil {
		t.Fatal(err)
	}
	publication, publishErr := fileaccess.PublishPrivate(t.Context(), directory, suiteFailurePendingName, []byte(`{"partial`))
	closeErr := directory.Close()
	if publishErr != nil || closeErr != nil || !publication.Visible || !publication.VisibilityKnown {
		t.Fatalf("publish pending fixture = %+v, %v, close=%v", publication, publishErr, closeErr)
	}
	if n, err := clearFileFailures(t.Context(), path, result.Retention.Diagnostics[0].ID); err != nil || n != 1 {
		t.Fatalf("targeted clear = %d, %v", n, err)
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("targeted clear touched pending: %v", err)
	}
	if n, err := clearFileFailures(t.Context(), path, ""); err != nil || n != 2 {
		t.Fatalf("whole clear = %d, %v", n, err)
	}
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending remains: %v", err)
	}
}

func TestFileFailureStoreLeavesWorkspaceV1BytesCompatible(t *testing.T) {
	root, detail := failureStoreFixture(t)
	id, err := saveSuiteFailure(t.Context(), root, detail)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.scan()
	path := store.path
	if closeErr := store.Close(); err != nil || closeErr != nil || len(entries) != 1 {
		t.Fatalf("workspace scan = %+v, %v, %v", entries, err, closeErr)
	}
	raw, err := os.ReadFile(filepath.Join(path, entries[0].name))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := marshalSuiteFailureRecord(suiteFailureRecord{SchemaVersion: 1, Sequence: 1, Detail: func() suiteFailureDetail { detail.ID = id; return detail }()}, nil)
	if err != nil || !bytes.Equal(raw, expected) || bytes.Contains(raw, []byte(`"subject"`)) {
		t.Fatalf("workspace v1 bytes changed: %v\n%s\n%s", err, raw, expected)
	}
}

func TestFileFailureStoreRejectsContradictoryRecords(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	if _, err := failingFilePreview(t, path); err == nil {
		t.Fatal("fixture unexpectedly passed")
	}
	for _, mode := range []string{"aggregate", "reason", "detail-verdict", "scores"} {
		t.Run(mode, func(t *testing.T) {
			store, err := openFileFailureStore(path, false, retainedFileFailureCodec{})
			if err != nil {
				t.Fatal(err)
			}
			entries, err := store.scan()
			target := filepath.Join(store.path, entries[0].name)
			if closeErr := store.Close(); err != nil || closeErr != nil || len(entries) == 0 {
				t.Fatalf("scan = %+v, %v, %v", entries, err, closeErr)
			}
			raw, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			var record fileFailureRecord
			if json.Unmarshal(raw, &record) != nil {
				t.Fatal("bad fixture")
			}
			switch mode {
			case "aggregate":
				record.Assertions[0].Verdict = runtime.EvaluationPass
				record.Assertions[0].ReasonCode = "assertion_satisfied"
			case "reason":
				record.Assertions[0].ReasonCode = "judge_unknown"
			case "detail-verdict":
				record.Detail.Assertions[0].Verdict = "unknown"
			case "scores":
				record.Detail.Assertions[0].Scores = []int{10, 20}
			}
			mutated, err := json.Marshal(record)
			if err != nil || os.WriteFile(target, mutated, 0o600) != nil {
				t.Fatal(err)
			}
			store, err = openFileFailureStore(path, false, retainedFileFailureCodec{})
			if err != nil {
				t.Fatal(err)
			}
			_, scanErr := store.scan()
			_ = store.Close()
			if !errors.Is(scanErr, errSuiteFailureStore) {
				t.Fatalf("contradictory %s record accepted: %v", mode, scanErr)
			}
			if err := os.WriteFile(target, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFileFailureStoreRetentionAcrossRevisions(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for i := 0; i < maxSuiteFailureRecords+1; i++ {
		candidate := append(append([]byte(nil), raw...), []byte(strings.Repeat("\n", i+1))...)
		if err := os.WriteFile(path, candidate, 0o600); err != nil {
			t.Fatal(err)
		}
		parsed, err := agentfile.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		opt := WithFilePreviewRetention(FilePreviewOptions{File: path, Operation: "test", CaseID: "first"}, path, parsed.Revision)
		result, _ := (&App{FileRunner: &scriptedPreview{verdict: runtime.EvaluationFail}}).PreviewAgentSnapshot(t.Context(), parsed, opt)
		if len(result.Retention.Diagnostics) != 1 {
			t.Fatalf("revision %d retention = %+v", i, result.Retention)
		}
		if i == 0 {
			first = result.Retention.Diagnostics[0].ID
		}
	}
	store, err := openFileFailureStore(path, false, retainedFileFailureCodec{})
	if err != nil {
		t.Fatal(err)
	}
	entries, scanErr := store.scan()
	closeErr := store.Close()
	if scanErr != nil || closeErr != nil || len(entries) != maxSuiteFailureRecords {
		t.Fatalf("retention = %d, %v, %v", len(entries), scanErr, closeErr)
	}
	for _, entry := range entries {
		if entry.record.Detail.ID == first {
			t.Fatal("oldest file failure retained")
		}
	}
}

func TestDarwinFileFailureCaseAliasesSharePartition(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("Darwin case-alias contract")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	alias := filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
	if _, err := os.Stat(alias); err != nil {
		t.Skip("test filesystem is case-sensitive")
	}
	result, _ := failingFilePreview(t, path)
	if len(result.Retention.Diagnostics) == 0 {
		t.Fatalf("retention = %+v", result.Retention)
	}
	var out bytes.Buffer
	if err := (&App{Out: &out}).ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: alias, ID: result.Retention.Diagnostics[0].ID}); err != nil || !strings.Contains(out.String(), result.Retention.Diagnostics[0].ID) {
		t.Fatalf("case alias split partition: %q, %v", out.String(), err)
	}
}

func TestFileFailureRetentionSkipsRunAndJournalFailure(t *testing.T) {
	base := t.TempDir()
	t.Setenv("KMX_HOME", filepath.Join(base, "state"))
	path := previewFile(t)
	for _, tc := range []struct {
		operation string
		cause     error
	}{{operation: "run"}, {operation: "test", cause: agentpreview.ErrJournalFailed}} {
		parsed, err := agentfile.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		opt := WithFilePreviewRetention(FilePreviewOptions{File: path, Operation: tc.operation}, path, parsed.Revision)
		result, _ := (&App{FileRunner: &scriptedPreview{verdict: runtime.EvaluationFail, err: tc.cause}}).PreviewAgentFile(t.Context(), opt)
		if result.Retention.Status != "not_requested" {
			t.Fatalf("%s/%v retained: %+v", tc.operation, tc.cause, result.Retention)
		}
	}
}
