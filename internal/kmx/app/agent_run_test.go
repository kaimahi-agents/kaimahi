package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func runFixture(t *testing.T) *evalFixture {
	t.Helper()
	f := newEvalFixture(t, nil)
	f.app.Err = &bytes.Buffer{}
	return f
}

func runOption(f *evalFixture) RunAgentOptions {
	return RunAgentOptions{BundleDir: f.bundle, ToContext: "kind-test", Prompt: "private prompt", ResultPort: f.opt.ResultPort, Wait: 30 * time.Second}
}

func TestRunAgentBundlePrintsOnlyAnswerAndReportsIdentityBeforeCreate(t *testing.T) {
	f := runFixture(t)
	f.answers["private prompt"] = "a private answer"
	err := f.app.RunAgent(runOption(f))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.out.String(); got != "a private answer\n" {
		t.Fatalf("stdout = %q", got)
	}
	if f.taskCreates(t) != 1 {
		t.Fatalf("creates = %d", f.taskCreates(t))
	}
	calls := orkaCalls(t, f.dir)
	var taskName string
	for _, c := range calls {
		if c.Document != nil && c.Document["kind"] == "Task" {
			taskName = c.Document["metadata"].(map[string]any)["name"].(string)
			if c.Document["spec"].(map[string]any)["prompt"] != "private prompt" {
				t.Fatal("wrong prompt submitted")
			}
		}
	}
	stderr := f.app.Err.(*bytes.Buffer).String()
	if taskName == "" || !strings.Contains(stderr, taskName) || !strings.Contains(stderr, "authority") || !strings.Contains(stderr, "state") || !strings.Contains(stderr, "commit") || strings.Contains(stderr, "private prompt") || strings.Contains(stderr, "a private answer") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunAgentUsesRememberedTargetAndRefusesChangedCluster(t *testing.T) {
	for _, uid := range []string{"cluster-uid", "old-cluster"} {
		t.Run(uid, func(t *testing.T) {
			f := runFixture(t)
			f.answers["private prompt"] = "ok"
			selection, err := bundleLiftSelectionPath(f.bundle)
			if err != nil {
				t.Fatal(err)
			}
			if err := saveBundleLiftSelection(selection, bundleLiftSelection{Context: "kind-test", Namespace: "orka-system", ClusterUID: uid, Inference: "provider:inference"}); err != nil {
				t.Fatal(err)
			}
			opt := runOption(f)
			opt.ToContext = ""
			err = f.app.RunAgent(opt)
			if uid == "cluster-uid" && err != nil {
				t.Fatal(err)
			}
			if uid != "cluster-uid" && (err == nil || !strings.Contains(err.Error(), "stale remembered target")) {
				t.Fatalf("err = %v", err)
			}
			want := 1
			if uid != "cluster-uid" {
				want = 0
			}
			if f.taskCreates(t) != want {
				t.Fatalf("creates = %d", f.taskCreates(t))
			}
		})
	}
}

func TestRunAgentRefusesUnownedOrNotReadyAgent(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(*testing.T, *evalFixture)
	}{
		{"missing", "not deployed", func(t *testing.T, f *evalFixture) {
			if err := os.Remove(filepath.Join(f.dir, "agent.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign", "another bundle", func(t *testing.T, f *evalFixture) {
			f.seedAgent(t, f.digest)
			raw, _ := os.ReadFile(filepath.Join(f.dir, "agent.json"))
			_ = os.WriteFile(filepath.Join(f.dir, "agent.json"), bytes.Replace(raw, []byte(`"`+orkaBundleMarker+`":"`+f.name+`"`), []byte(`"`+orkaBundleMarker+`":"foreign"`), 1), 0600)
		}},
		{"not ready", "not Ready", func(t *testing.T, f *evalFixture) {
			raw, _ := os.ReadFile(filepath.Join(f.dir, "agent.json"))
			_ = os.WriteFile(filepath.Join(f.dir, "agent.json"), bytes.Replace(raw, []byte(`"ready":true`), []byte(`"ready":false`), 1), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := runFixture(t)
			tc.setup(t, f)
			err := f.app.RunAgent(runOption(f))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
			if f.taskCreates(t) != 0 {
				t.Fatal("created after refusal")
			}
		})
	}
}

func TestRunAgentReportsBehindAndStillCreatesOneTask(t *testing.T) {
	f := runFixture(t)
	deployed := strings.Repeat("c", 64)
	f.seedAgent(t, deployed)
	provider := map[string]any{
		"kind": "Provider", "metadata": map[string]any{"name": f.name, "namespace": "orka-system", "uid": "provider-uid", "generation": 1,
			"annotations": map[string]any{orkaBundleMarker: f.name, orkaPortableMarker: deployed, orkaRenderedMarker: strings.Repeat("b", 64)}},
		"status": map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}},
	}
	raw, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "provider.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.answers["private prompt"] = "answer from deployed revision"
	if err := f.app.RunAgent(runOption(f)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.app.Err.(*bytes.Buffer).String(), "Bundle state: behind; deployed commit: unknown") {
		t.Fatalf("stderr=%s", f.app.Err)
	}
	if f.out.String() != "answer from deployed revision\n" || f.taskCreates(t) != 1 {
		t.Fatalf("stdout=%q creates=%d", f.out, f.taskCreates(t))
	}
}

func TestRunAgentReportsDriftAndStillCreatesOneTask(t *testing.T) {
	f := runFixture(t)
	fixtures, err := filepath.Abs("../orkaschema/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_RECONCILE_FIXTURES", fixtures)
	rendered, err := RenderOrkaBundleFile(f.bundle, orkaBindingsFromCreate(goldenNoTaskCreate("")))
	if err != nil {
		t.Fatal(err)
	}
	provider := reconcileLive(t, f.dir, "Provider", rendered)
	agent := reconcileLive(t, f.dir, "Agent", rendered)
	for _, doc := range []map[string]any{provider, agent} {
		markBundleOwned(doc, f.name, rendered)
		doc["status"] = map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
	}
	agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "live change"}
	for _, item := range []struct {
		file string
		doc  map[string]any
	}{{"provider.json", provider}, {"agent.json", agent}} {
		raw, err := json.Marshal(item.doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.dir, item.file), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.answers["private prompt"] = "answer from drifted agent"
	if err := f.app.RunAgent(runOption(f)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.app.Err.(*bytes.Buffer).String(), "Bundle state: drifted;") {
		observed := f.app.observeBundleTarget(t.Context(), f.bundle, f.name, f.digest, bundleStatusTarget{Context: "kind-test", Namespace: "orka-system"})
		t.Fatalf("state=%s detail=%s stderr=%s", observed.State, observed.Detail, f.app.Err)
	}
	if f.out.String() != "answer from drifted agent\n" || f.taskCreates(t) != 1 {
		t.Fatalf("stdout=%q creates=%d", f.out, f.taskCreates(t))
	}
}

func TestRunAgentDirectAndPromptFile(t *testing.T) {
	f := runFixture(t)
	f.answers["from file"] = "direct answer"
	file := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(file, []byte("from file"), 0600); err != nil {
		t.Fatal(err)
	}
	opt := RunAgentOptions{Agent: f.name, Namespace: "orka-system", PromptFile: file, ResultPort: f.opt.ResultPort, Wait: 30 * time.Second}
	if err := f.app.RunAgent(opt); err != nil {
		t.Fatal(err)
	}
	if f.out.String() != "direct answer\n" || f.taskCreates(t) != 1 {
		t.Fatalf("out=%q creates=%d", f.out, f.taskCreates(t))
	}
}

func TestRunAgentDirectDoesNotRequireBundleOwnership(t *testing.T) {
	f := runFixture(t)
	f.answers["private prompt"] = "direct"
	raw, err := os.ReadFile(filepath.Join(f.dir, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"`+orkaBundleMarker+`":"`+f.name+`"`), []byte(`"`+orkaBundleMarker+`":"foreign"`), 1)
	if err := os.WriteFile(filepath.Join(f.dir, "agent.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	opt := RunAgentOptions{Agent: f.name, Prompt: "private prompt", ResultPort: f.opt.ResultPort, Wait: 30 * time.Second}
	if err := f.app.RunAgent(opt); err != nil {
		t.Fatal(err)
	}
	if got := f.out.String(); got != "direct\n" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestRunAgentRefusesInvalidInputsWithoutCreating(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RunAgentOptions)
	}{
		{"empty", func(o *RunAgentOptions) { o.Prompt = "   " }},
		{"both", func(o *RunAgentOptions) { o.PromptFile = "-" }},
		{"mode", func(o *RunAgentOptions) { o.Agent = "different" }},
		{"wait", func(o *RunAgentOptions) { o.Wait = 10 * time.Minute }},
		{"port", func(o *RunAgentOptions) { o.ResultPort = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := runFixture(t)
			opt := runOption(f)
			tc.change(&opt)
			if err := f.app.RunAgent(opt); err == nil {
				t.Fatal("accepted invalid input")
			}
			if f.taskCreates(t) != 0 {
				t.Fatal("created after invalid input")
			}
		})
	}
}

// The stderr writer is a terminal boundary: a broken output stream must stop
// the mutation before the Task can be submitted.
type failingRunWriter struct{ content bytes.Buffer }

func (w *failingRunWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("Task name:")) {
		return 0, fmt.Errorf("terminal disconnected")
	}
	return w.content.Write(p)
}
func TestRunAgentRefusesCreateWhenTaskNameCannotBeWritten(t *testing.T) {
	f := runFixture(t)
	out := &failingRunWriter{}
	f.app.Err = out
	if err := f.app.RunAgent(runOption(f)); err == nil || !strings.Contains(err.Error(), "Task name") {
		t.Fatalf("err=%v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("created despite name write failure")
	}
}

type shortRunWriter struct{}

func (shortRunWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestRunAgentRefusesShortTaskNameWrite(t *testing.T) {
	f := runFixture(t)
	f.app.Err = shortRunWriter{}
	opt := runOption(f)
	opt.Wait = 10 * time.Second
	if err := f.app.RunAgent(opt); err == nil || !strings.Contains(err.Error(), "Task name") {
		t.Fatalf("err=%v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("created after short Task name write")
	}
}

type recoveryFailWriter struct{ content bytes.Buffer }

func (w *recoveryFailWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("Recovery:")) {
		return 0, fmt.Errorf("terminal disconnected")
	}
	return w.content.Write(p)
}

func TestRunAgentRefusesCreateWhenRecoveryCannotBeWritten(t *testing.T) {
	f := runFixture(t)
	f.answers["private prompt"] = "answer"
	writer := &recoveryFailWriter{}
	f.app.Err = writer
	err := f.app.RunAgent(runOption(f))
	if err == nil || !strings.Contains(err.Error(), "Recovery") {
		t.Fatalf("err=%v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("Task created without written recovery command")
	}
	if !strings.Contains(writer.content.String(), "Task name:") {
		t.Fatal("Task name missing before recovery write")
	}
}

func TestRunAgentTimeoutReturnsPendingRecoveryWithoutRetry(t *testing.T) {
	f := runFixture(t)
	t.Setenv("KMX_EVAL_PHASE", "Running")
	opt := runOption(f)
	opt.Wait = 10 * time.Second
	// A caller-owned deadline simulates expiry without sleeping ten seconds.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	err := f.app.RunAgent(opt)
	if !errors.Is(err, ErrTaskPending) {
		t.Fatalf("err=%v", err)
	}
	if f.taskCreates(t) != 1 || f.out.Len() != 0 {
		t.Fatalf("creates=%d stdout=%q", f.taskCreates(t), f.out)
	}
	if !strings.Contains(err.Error(), "kmx task result ") || !strings.Contains(err.Error(), "--context kind-test --namespace orka-system --wait") {
		t.Fatalf("recovery=%v", err)
	}
}

func TestRunWaitErrorKeepsTerminalFailureAndRefusalAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"failed Task", fmt.Errorf("Task ended: %w", &orkaTaskEndedError{Phase: "Failed"})},
		{"result refusal", errors.New("Orka result read refused (HTTP 403)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runTaskWaitError(ctx, tc.err, "task", "kind-test", "orka-system")
			if errors.Is(got, ErrTaskPending) || !errors.Is(got, tc.err) {
				t.Fatalf("deadline masked %s: %v", tc.name, got)
			}
		})
	}
	if got := runTaskWaitError(ctx, fmt.Errorf("waiting: %w", context.DeadlineExceeded), "task", "kind-test", "orka-system"); !errors.Is(got, ErrTaskPending) {
		t.Fatalf("expired wait not pending: %v", got)
	}
}

func TestRunAgentFailedTaskDoesNotReturnPending(t *testing.T) {
	f := runFixture(t)
	t.Setenv("KMX_EVAL_PHASE", "Failed")
	err := f.app.RunAgent(runOption(f))
	if err == nil || errors.Is(err, ErrTaskPending) || !strings.Contains(err.Error(), "Failed") {
		t.Fatalf("err=%v", err)
	}
	if f.taskCreates(t) != 1 {
		t.Fatal("unexpected retry")
	}
}

func TestRunAgentDoesNotWriteBundleReceipt(t *testing.T) {
	f := runFixture(t)
	f.answers["private prompt"] = "ok"
	if err := f.app.RunAgent(runOption(f)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(f.bundle, "receipts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote receipts: %v", entries)
	}
}

func TestRunAgentPromptIsRedactedFromRemoteGuard(t *testing.T) {
	f := runFixture(t)
	t.Setenv("KMX_EVAL_REMOTE", "1")
	f.app.InvocationCommand = "kmx agent run --prompt private prompt --context kind-test"
	err := f.app.RunAgent(runOption(f))
	if err == nil {
		t.Fatal("remote guard accepted")
	}
	if strings.Contains(err.Error()+f.app.Err.(*bytes.Buffer).String(), "private prompt") {
		t.Fatalf("prompt leaked: %v %s", err, f.app.Err)
	}
	want := "kmx agent run " + shellArg(f.bundle) + " --to-context kind-test --wait 30s --result-port " + f.opt.ResultPort + " --prompt-file -"
	if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), " --agent ") || strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("guard retry lost bundle selection or exposed prompt: %v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("created")
	}
}

func TestRunAgentRemoteGuardPreservesRememberedBundleDestination(t *testing.T) {
	f := runFixture(t)
	selection, err := bundleLiftSelectionPath(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveBundleLiftSelection(selection, bundleLiftSelection{Context: "kind-test", Namespace: "orka-system", ClusterUID: "cluster-uid", Inference: "provider:inference"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_EVAL_REMOTE", "1")
	opt := runOption(f)
	opt.ToContext = ""
	err = f.app.RunAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "kmx agent run "+shellArg(f.bundle)+" --to-context kind-test --wait 30s --result-port "+f.opt.ResultPort+" --prompt-file -") {
		t.Fatalf("remembered destination not restored in retry: %v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("created after guard refusal")
	}
}

func TestRunAgentTaskNameIsNotOnStdout(t *testing.T) {
	f := runFixture(t)
	f.answers["private prompt"] = "answer"
	if err := f.app.RunAgent(runOption(f)); err != nil {
		t.Fatal(err)
	}
	for _, c := range orkaCalls(t, f.dir) {
		if c.Document != nil && c.Document["kind"] == "Task" {
			name := c.Document["metadata"].(map[string]any)["name"].(string)
			if strings.Contains(f.out.String(), name) {
				t.Fatal("Task name leaked to stdout")
			}
		}
	}
}

func TestRunAgentRejectsAgentReplacedAfterInitialRead(t *testing.T) {
	f := runFixture(t)
	t.Setenv("KMX_EVAL_AGENT_SWAP_ON_READ", "2")
	opt := RunAgentOptions{Agent: f.name, Prompt: "private prompt", ResultPort: f.opt.ResultPort}
	err := f.app.RunAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("err=%v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("created against replaced Agent")
	}
}

func TestRunAgentRejectsAgentReplacedDuringResultSession(t *testing.T) {
	f := runFixture(t)
	t.Setenv("KMX_EVAL_AGENT_SWAP_ON_READ", "3")
	opt := RunAgentOptions{Agent: f.name, Prompt: "private prompt", ResultPort: f.opt.ResultPort, Wait: 10 * time.Second}
	err := f.app.RunAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("err=%v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("created against replaced Agent")
	}
}

func TestRunAgentPromptFileStdin(t *testing.T) {
	f := runFixture(t)
	f.answers["stdin secret"] = "stdin answer"
	file, err := os.CreateTemp(t.TempDir(), "prompt-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("stdin secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	f.app.Stdin = file
	opt := runOption(f)
	opt.Prompt = ""
	opt.PromptFile = "-"
	if err := f.app.RunAgent(opt); err != nil {
		t.Fatal(err)
	}
	if got := f.out.String(); got != "stdin answer\n" {
		t.Fatalf("stdout=%q", got)
	}
	if strings.Contains(f.app.Err.(*bytes.Buffer).String(), "stdin secret") {
		t.Fatal("prompt leaked")
	}
}

func TestTaskRecoveryQuotesContext(t *testing.T) {
	got := taskRecovery("safe-task", "kind-prod;echo-danger", "orka-system")
	want := "kmx task result safe-task --context 'kind-prod;echo-danger' --namespace orka-system --wait 5m"
	if got != want {
		t.Fatalf("recovery command = %q, want %q", got, want)
	}
}

func TestRunAgentRefusesSymlinkPromptFile(t *testing.T) {
	f := runFixture(t)
	target := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(target, []byte("private prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	opt := runOption(f)
	opt.Prompt, opt.PromptFile = "", link
	if err := f.app.RunAgent(opt); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("symlink accepted: %v", err)
	}
	if f.taskCreates(t) != 0 {
		t.Fatal("created Task from linked prompt")
	}
}

func TestTaskRecoveryWaitHasExplicitDuration(t *testing.T) {
	got := taskRecovery("safe-task", "kind-test", "orka-system")
	if !strings.HasSuffix(got, "--wait 5m") {
		t.Fatalf("recovery command = %q", got)
	}
}

func TestRunAgentBodyReadDeadlineIsPending(t *testing.T) {
	f := runFixture(t)
	f.resultHoldBody = true
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	opt := runOption(f)
	opt.Wait = 10 * time.Second
	err := f.app.RunAgent(opt)
	if !errors.Is(err, ErrTaskPending) || f.taskCreates(t) != 1 || f.out.Len() != 0 {
		t.Fatalf("body-read deadline: err=%v creates=%d stdout=%q", err, f.taskCreates(t), f.out)
	}
}

func TestRunAgentSessionLossAfterCreateIncludesRecovery(t *testing.T) {
	f := runFixture(t)
	f.resultDisconnect = true
	err := f.app.RunAgent(runOption(f))
	if err == nil || errors.Is(err, ErrTaskPending) {
		t.Fatalf("lost session = %v", err)
	}
	if f.taskCreates(t) != 1 {
		t.Fatalf("create attempts = %d", f.taskCreates(t))
	}
	var taskName string
	for _, call := range orkaCalls(t, f.dir) {
		if call.Document != nil && call.Document["kind"] == "Task" {
			taskName = call.Document["metadata"].(map[string]any)["name"].(string)
		}
	}
	if taskName == "" || !strings.Contains(err.Error(), taskRecovery(taskName, "kind-test", "orka-system")) {
		t.Fatalf("missing context-pinned recovery after lost session: %v", err)
	}
}

func TestRunAgentTaskNeverRetriedOnCreateError(t *testing.T) {
	f := runFixture(t)
	t.Setenv("KMX_EVAL_CREATE_FAIL", "1")
	err := f.app.RunAgent(runOption(f))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err=%v", err)
	}
	count := 0
	for _, c := range orkaCalls(t, f.dir) {
		if slices.Contains(c.Args, "create") && c.Document != nil && c.Document["kind"] == "Task" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("create attempts=%d", count)
	}
}
