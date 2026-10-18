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
	"time"
)

func runFixture(t *testing.T) *evalFixture {
	t.Helper()
	f := newEvalFixture(t, nil)
	f.app.Err = &bytes.Buffer{}
	return f
}

func runOption(f *evalFixture) RunAgentOptions {
	return RunAgentOptions{Agent: f.name, Namespace: "orka-system", Prompt: "private prompt", ResultPort: f.opt.ResultPort, Wait: 30 * time.Second}
}

func TestRunAgentPrintsOnlyAnswerAndReportsIdentityBeforeCreate(t *testing.T) {
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
	if taskName == "" || !strings.Contains(stderr, taskName) || !strings.Contains(stderr, "authority") || !strings.Contains(stderr, "Recovery:") || strings.Contains(stderr, "private prompt") || strings.Contains(stderr, "a private answer") {
		t.Fatalf("stderr = %q", stderr)
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

// Suite ownership must not turn live invocation into a legacy ownership check.
// Run and chat still target the selected Agent, without changing its resources.
func TestSuiteOwnedLiveAgentInvocation(t *testing.T) {
	for _, mode := range []string{"run", "chat"} {
		t.Run(mode, func(t *testing.T) {
			f := runFixture(t)
			f.answers["private prompt"] = "suite-owned answer"
			path := filepath.Join(f.dir, "agent.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var agent map[string]any
			if err := json.Unmarshal(raw, &agent); err != nil {
				t.Fatal(err)
			}
			owner := orkaSuiteOwnership{
				Suite: "sample-suite", Digest: "sha256:" + strings.Repeat("a", 64),
				Member: f.name, Renderer: "kmx.native-orka/v2",
				BindingDigest: strings.Repeat("b", 64), RenderedDigest: strings.Repeat("c", 64),
			}
			agent["metadata"].(map[string]any)["annotations"] = owner.markers()
			agent["spec"] = map[string]any{"tools": []any{}}
			raw, err = json.Marshal(agent)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}

			if mode == "run" {
				err = f.app.RunAgent(runOption(f))
			} else {
				// The existing Task/result harness handles the session; only API
				// discovery needs an additional response for automatic selection.
				f.app.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "api-resources") {
						_, err := io.WriteString(cmd.Stdout, "agents.core.orka.ai\n")
						return err
					}
					return cmd.Run()
				}
				t.Setenv("KMX_EVAL_FORWARD_TARGET", f.resultAddress)
				in, openErr := os.CreateTemp(t.TempDir(), "input")
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer in.Close()
				if _, err := in.WriteString("/exit\n"); err != nil {
					t.Fatal(err)
				}
				if _, err := in.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				f.app.Stdin = in
				err = f.app.ChatWithOptions(ChatOptions{Agent: f.name, Namespace: "orka-system", Task: "private prompt"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(f.out.String(), "suite-owned answer") || f.taskCreates(t) != 1 {
				t.Fatalf("answer=%q Task creates=%d", f.out, f.taskCreates(t))
			}
			for _, call := range orkaCalls(t, f.dir) {
				if call.Document == nil {
					continue
				}
				if call.Document["kind"] != "Task" || !slices.Contains(call.Args, "create") {
					t.Fatalf("invocation wrote a non-Task resource: %+v", call)
				}
				spec := call.Document["spec"].(map[string]any)
				ref := spec["agentRef"].(map[string]any)
				if spec["prompt"] != "private prompt" || ref["name"] != f.name || ref["namespace"] != "orka-system" {
					t.Fatalf("Task did not target the selected live Agent: %+v", spec)
				}
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, after) {
				t.Fatalf("invocation changed the suite-owned Agent: %v", err)
			}
		})
	}
}

func TestRunAgentRefusesInvalidInputsWithoutCreating(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RunAgentOptions)
	}{
		{"empty", func(o *RunAgentOptions) { o.Prompt = "   " }},
		{"both", func(o *RunAgentOptions) { o.PromptFile = "-" }},
		{"mode", func(o *RunAgentOptions) { o.Agent = "" }},
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
	// Expire only after the one Task was created, not during session setup.
	deadline := orkaTestDeadline(t, f.app, "orka-task-status")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	err := f.app.RunAgent(opt)
	if !errors.Is(err, ErrTaskPending) || deadline().Err() != context.DeadlineExceeded {
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
	a := &App{}
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
			got := a.runTaskWaitError(ctx, tc.err, "task", "kind-test", "orka-system")
			if errors.Is(got, ErrTaskPending) || !errors.Is(got, tc.err) {
				t.Fatalf("deadline masked %s: %v", tc.name, got)
			}
		})
	}
	if got := a.runTaskWaitError(ctx, fmt.Errorf("waiting: %w", context.DeadlineExceeded), "task", "kind-test", "orka-system"); !errors.Is(got, ErrTaskPending) {
		t.Fatalf("expired wait not pending: %v", got)
	}
}

func TestRunWaitErrorRecognizesOnlyExpiredOrkaPhases(t *testing.T) {
	a := &App{}
	phase, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct {
		name    string
		err     error
		pending bool
	}{
		{"expired phase", orkaPhaseError(t.Context(), phase, fmt.Errorf("waiting: %w", context.DeadlineExceeded)), true},
		{"unrelated deadline", fmt.Errorf("request: %w", context.DeadlineExceeded), false},
		{"unexpired phase", orkaPhaseError(t.Context(), t.Context(), fmt.Errorf("request: %w", context.DeadlineExceeded)), false},
		{"failed Task", orkaPhaseError(t.Context(), phase, &orkaTaskEndedError{Phase: "Failed"}), false},
		{"result refusal", orkaPhaseError(t.Context(), phase, errors.New("Orka result read refused (HTTP 403)")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := a.runTaskWaitError(t.Context(), tc.err, "task", "kind-test", "orka-system")
			if errors.Is(got, ErrTaskPending) != tc.pending {
				t.Fatalf("pending=%t: %v", tc.pending, got)
			}
			if !tc.pending && !errors.Is(got, tc.err) {
				t.Fatalf("phase classification discarded the original error: %v", got)
			}
		})
	}
}

func TestOrkaDefaultPhaseDeadlinePreservesOriginalError(t *testing.T) {
	parent, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	original := fmt.Errorf("request: %w", context.DeadlineExceeded)
	for _, timing := range []*waitTiming{nil, {}, fastWaitTiming()} {
		a := &App{waitTiming: timing}
		phase, stop := a.waitContext(parent, "orka-task-result", 0)
		got := orkaPhaseError(parent, phase, original)
		stop()
		if got != original {
			t.Fatalf("default phase changed the error: %v", got)
		}
		// A live caller cannot turn a default phase's independent request
		// deadline into pending; an expired caller still can, as before.
		if live := a.runTaskWaitError(t.Context(), got, "task", "kind-test", "orka-system"); live != original {
			t.Fatalf("default phase changed live-parent classification: %v", live)
		}
		if expired := a.runTaskWaitError(parent, got, "task", "kind-test", "orka-system"); !errors.Is(expired, ErrTaskPending) {
			t.Fatalf("default phase lost expired-parent classification: %v", expired)
		}
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
	entries, err := os.ReadDir(filepath.Join(f.dir, "receipts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote receipts: %v", entries)
	}
}

func TestRunAgentPromptIsRedactedFromRemoteGuard(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprint(automatic), func(t *testing.T) {
			f := runFixture(t)
			t.Setenv("KMX_EVAL_REMOTE", "1")
			f.app.InvocationCommand = "kmx run --prompt private prompt --context kind-test"
			opt := runOption(f)
			args := []string{"run", "--agent", f.name, "--namespace", "orka-system", "--wait", "30s"}
			if automatic {
				opt.ResultPort = ""
			} else {
				args = append(args, "--result-port", f.opt.ResultPort)
			}
			err := f.app.RunAgent(opt)
			if err == nil {
				t.Fatal("remote guard accepted")
			}
			if strings.Contains(err.Error()+f.app.Err.(*bytes.Buffer).String(), "private prompt") {
				t.Fatalf("prompt leaked: %v %s", err, f.app.Err)
			}
			want := f.app.operationCommand(append(args, "--prompt-file", "-")...)
			if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "<redacted>") || automatic && strings.Contains(err.Error(), "--result-port") {
				t.Fatalf("guard retry lost live Agent selection, automatic port or prompt privacy: %v", err)
			}
			if f.taskCreates(t) != 0 {
				t.Fatal("created")
			}
		})
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
	a := &App{Executable: "./bin with space/kmx"}
	got := a.taskRecovery("safe-task", "kind-prod;echo-danger", "orka-system")
	want := "'./bin with space/kmx' task result safe-task --context 'kind-prod;echo-danger' --namespace orka-system --wait 5m"
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
	got := (&App{}).taskRecovery("safe-task", "kind-test", "orka-system")
	if !strings.HasSuffix(got, "--wait 5m") {
		t.Fatalf("recovery command = %q", got)
	}
}

func TestRunAgentBodyReadDeadlineIsPending(t *testing.T) {
	f := runFixture(t)
	f.resultHoldBody = true
	// Retain the caller's setup cap and expire during result retrieval. The
	// held-body count proves the handler flushed headers before cancellation.
	deadline := orkaTestDeadline(t, f.app, "orka-task-result")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	opt := runOption(f)
	opt.Wait = 10 * time.Second
	err := f.app.RunAgent(opt)
	if !errors.Is(err, ErrTaskPending) || deadline().Err() != context.DeadlineExceeded || f.taskCreates(t) != 1 || f.resultHeldBodies.Load() != 1 || f.out.Len() != 0 {
		t.Fatalf("body-read deadline: err=%v creates=%d heldBodies=%d stdout=%q", err, f.taskCreates(t), f.resultHeldBodies.Load(), f.out)
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
	if taskName == "" || !strings.Contains(err.Error(), f.app.taskRecovery(taskName, "kind-test", "orka-system")) {
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
