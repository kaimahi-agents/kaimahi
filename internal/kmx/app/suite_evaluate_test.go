package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const suiteTaskCase = "id: packaged\ninput: Packaged question\nexpectContains: [answer]\nassertions:\n- {id: exact, type: contains, value: answer}\n"
const suiteTaskJudgeCase = "id: packaged\ninput: Packaged question\nexpectContains: [answer]\nassertions:\n- {id: quality, type: llmJudge, rubric: PRIVATE_RUBRIC_CANARY, minScore: 50, judgeModel: fixture/model, judgeVersion: pinned-v1}\n"

// Keep the suite placement and Task/result handlers real. Only kubectl capture
// and the loopback result server are substituted; the owned-pair protocol is
// the same fixture used by suite-native placement tests.
type suiteTaskFixture struct {
	*suiteNativeFixture
	placement                             *suiteNativePlacement
	packaged                              workspace.PackagedCase
	opt                                   suiteNativeEvaluationOptions
	out, stderr                           *bytes.Buffer
	answer, phase                         string
	resultStatus                          int
	disconnect, holdBody, ambiguousCreate bool
	afterResult                           func()
	beforeCapture                         func(orkaCall)
	resultReads, connections              atomic.Int32
	mu                                    sync.Mutex
	tasks                                 map[string]map[string]any
}

func newSuiteTaskFixture(t *testing.T, body string) *suiteTaskFixture {
	t.Helper()
	native := newSuiteNativeFixture(t)
	root := t.TempDir()
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte("  Answer exactly.\n\nPackaged whitespace.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "sample", Description: "Packaged description", Model: "packaged-model", InstructionsFile: input}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	native.suite, err = workspace.Load(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	// New source is deliberately different in all behavior-bearing inputs.
	for path, content := range map[string]string{
		"agents/sample/instructions.md": "New source instructions",
		"eval/example.yaml":             "id: source\ninput: New source question\nexpectContains: [wrong]\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	memberPath := filepath.Join(root, "agents/sample/member.yaml")
	member, err := os.ReadFile(memberPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memberPath, bytes.ReplaceAll(member, []byte("packaged-model"), []byte("new-source-model")), 0600); err != nil {
		t.Fatal(err)
	}
	p := native.prepare(t)
	placement, err := native.app.placeSuiteNative(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	f := &suiteTaskFixture{suiteNativeFixture: native, placement: placement, packaged: native.suite.Cases[0], out: &bytes.Buffer{}, stderr: &bytes.Buffer{}, answer: "answer PRIVATE_ANSWER_CANARY", phase: "Succeeded", resultStatus: http.StatusOK, tasks: map[string]map[string]any{}}
	f.app.Out, f.app.Err = f.out, f.stderr
	f.app.Run.Stdout, f.app.Run.Stderr, f.app.Run.Echo = f.out, f.stderr, true
	// Only version probing and the actual port-forward are executable. All API
	// captures remain in-process, including Task identity checks around HTTP.
	t.Setenv("KMX_EVAL_DIR", f.dir)
	fakeTool(t, f.dir, "kubectl", kubectlFixture(t, "TestEvalKubectlHelper"))
	originalCapture := f.app.captureCommand
	f.app.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		call := orkaCall{Args: append([]string(nil), cmd.Args[1:]...)}
		if slices.Contains(call.Args, "-f") {
			raw, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &call.Document); err != nil {
				return err
			}
			cmd.Stdin = bytes.NewReader(raw)
		}
		if f.beforeCapture != nil {
			f.beforeCapture(call)
		}
		emit := func(v any) error { return json.NewEncoder(cmd.Stdout).Encode(v) }
		if i := slices.Index(call.Args, "get"); i >= 0 && len(call.Args) > i+2 {
			kind, name := call.Args[i+1], call.Args[i+2]
			switch kind {
			case "serviceaccount":
				if name != orkaResultAccount {
					break
				}
				f.calls = append(f.calls, call)
				_, err := io.WriteString(cmd.Stdout, "serviceaccount/"+name)
				return err
			case "services":
				f.calls = append(f.calls, call)
				_, err := io.WriteString(cmd.Stdout, `{"apiVersion":"v1","kind":"ServiceList","items":[{"apiVersion":"v1","kind":"Service","metadata":{"name":"orka","namespace":"orka-system","uid":"service-uid","labels":{"app.kubernetes.io/name":"orka"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"},"ports":[{"name":"api","port":8080}]}}]}`)
				return err
			case "service":
				f.calls = append(f.calls, call)
				_, err := io.WriteString(cmd.Stdout, `{"apiVersion":"v1","kind":"Service","metadata":{"name":"orka","namespace":"orka-system","uid":"service-uid"},"spec":{"ports":[{"name":"api","port":8080}]}}`)
				return err
			case "tasks.core.orka.ai":
				f.calls = append(f.calls, call)
				f.mu.Lock()
				doc := f.tasks[name]
				if doc != nil {
					doc["status"] = map[string]any{"phase": f.phase, "resultRef": map[string]any{"available": f.phase == "Succeeded"}}
				}
				err := emit(doc)
				f.mu.Unlock()
				if f.resultReads.Load() > 0 && f.afterResult != nil {
					after := f.afterResult
					f.afterResult = nil
					after()
				}
				return err
			}
		}
		if slices.Contains(call.Args, "token") {
			f.calls = append(f.calls, call)
			return emit(map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenRequest", "spec": map[string]any{"audiences": []string{"orka"}, "expirationSeconds": 600}, "status": map[string]any{"token": orkaTestToken(), "expirationTimestamp": time.Now().Add(10 * time.Minute).Format(time.RFC3339)}})
		}
		if slices.Contains(call.Args, "create") && call.Document["kind"] == "Task" {
			f.calls = append(f.calls, call)
			// Admission response/status changes must not mutate the captured
			// create request whose exact payload the tests inspect.
			raw, err := json.Marshal(call.Document)
			if err != nil {
				return err
			}
			var stored map[string]any
			if err := json.Unmarshal(raw, &stored); err != nil {
				return err
			}
			meta := nativeMeta(stored)
			name := meta["name"].(string)
			meta["uid"], meta["generation"], meta["resourceVersion"] = "task-uid-"+name, 1, "1"
			f.mu.Lock()
			f.tasks[name] = stored
			f.mu.Unlock()
			if f.ambiguousCreate {
				_, _ = io.WriteString(cmd.Stderr, "PRIVATE_RUNTIME_STDERR_CANARY "+orkaTestToken())
				return errors.New("dropped create response")
			}
			return emit(stored)
		}
		return originalCapture(ctx, cmd)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+orkaTestToken() || r.URL.Query().Get("namespace") != "orka-system" {
			t.Error("invalid result request")
			w.WriteHeader(403)
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
		f.mu.Lock()
		doc := f.tasks[name]
		f.mu.Unlock()
		if doc == nil {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		f.resultReads.Add(1)
		if f.disconnect {
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = c.Close()
			}
			return
		}
		if f.holdBody {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if f.resultStatus != 200 {
			w.WriteHeader(f.resultStatus)
			_, _ = io.WriteString(w, `{"error":{"code":500,"message":"PRIVATE_MODEL_ERROR_CANARY"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"result": f.answer})
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			f.connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	_, f.opt.ResultPort, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	return f
}

// TokenRequest is the existing in-memory result credential, not a persistent
// resource write. Keep every other write in this inventory, not just Tasks.
func (f *suiteTaskFixture) writes(from int) []orkaCall {
	return slices.DeleteFunc(f.suiteNativeFixture.writes(from), func(c orkaCall) bool {
		return slices.Contains(c.Args, "token")
	})
}

func (f *suiteTaskFixture) evaluate(t *testing.T) (suiteNativeCaseResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	return f.app.evaluateSuiteNative(ctx, f.placement, f.packaged, f.opt)
}

// Wrong prompt/model/instructions, portable revision routing, answer disclosure,
// or writing anything but one native Task must fail this end-to-end test.
func TestSuiteTaskPackagedCaseAndPrivateSummary(t *testing.T) {
	f := newSuiteTaskFixture(t, suiteTaskCase)
	before := len(f.calls)
	r, err := f.evaluate(t)
	if err != nil || r.Verdict != agentruntime.EvaluationPass || r.CaseID != "packaged" || r.TaskUID != "task-uid-"+r.TaskName {
		t.Fatalf("result: %+v %v", r, err)
	}
	answerDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("answer PRIVATE_ANSWER_CANARY")))
	caseDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(suiteTaskCase)))
	inputDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("Packaged question")))
	if r.AnswerSHA256 != answerDigest || r.CaseDigest != caseDigest || r.InputDigest != inputDigest || len(r.Assertions) != 2 || r.Assertions[1].Verdict != agentruntime.EvaluationPass {
		t.Fatalf("evidence: %+v", r)
	}
	if len(r.Matched)+len(r.Missing) != 0 {
		t.Fatal("suite evidence retains authored operands")
	}
	writes := f.writes(before)
	if len(writes) != 1 || writes[0].Document["kind"] != "Task" {
		t.Fatalf("writes: %+v", writes)
	}
	task := nativeSpec(writes[0].Document)
	if task["type"] != "ai" || task["prompt"] != "Packaged question" || task["runtimeRef"] != nil || len(task["resources"].(map[string]any)) != 0 {
		t.Fatalf("Task: %+v", task)
	}
	ref := task["agentRef"].(map[string]any)
	if ref["name"] != "sample" || ref["namespace"] != "orka-system" {
		t.Fatalf("Agent binding: %+v", ref)
	}
	if nativeSpec(f.stored(t, "Provider"))["defaultModel"] != "packaged-model" || nativeSpec(f.stored(t, "Agent"))["systemPrompt"].(map[string]any)["inline"] != "  Answer exactly.\n\nPackaged whitespace.\n" {
		t.Fatal("source edit changed placed candidate")
	}
	if f.out.String() != "packaged: pass\n" {
		t.Fatalf("not summary-only: %q", f.out)
	}
	raw, _ := json.Marshal(r)
	for _, forbidden := range []string{"PRIVATE_ANSWER_CANARY", "PRIVATE_RUBRIC_CANARY", "PRIVATE_SECRET_VALUE_CANARY", "PRIVATE_ENV_CANARY", orkaTestToken(), "Packaged question", "Packaged whitespace"} {
		if strings.Contains(string(raw)+f.out.String()+f.stderr.String(), forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	if f.connections.Load() != 1 {
		t.Fatal("more than one result connection")
	}
	assertOrkaForwardExited(t, f.dir)
}

// A failing content sink cannot erase an answer already retrieved from the
// original native Task or turn its unjudged assertions into passing evidence.
func TestSuiteTaskContentWriterFailureRetainsObservedAnswerDigest(t *testing.T) {
	f := newSuiteTaskFixture(t, suiteTaskJudgeCase)
	f.answer = "public answer"
	f.opt.ShowContent = true
	f.app.Out = integrationAnswerWriter{fail: true}
	var judgeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		judgeCalls.Add(1)
		_, _ = io.WriteString(w, judgeEnvelope(`{"score":90,"reasoning":"PRIVATE_REASON_CANARY"}`))
	}))
	defer server.Close()
	f.opt.Judge.BaseURL = server.URL
	before := len(f.calls)
	r, err := f.evaluate(t)
	if err != nil || r.Verdict != agentruntime.EvaluationUnknown || r.Detail != evaluationResultUnavailable || len(f.writes(before)) != 1 {
		t.Fatalf("content failure outcome: %+v %v writes=%d", r, err, len(f.writes(before)))
	}
	if r.TaskName == "" || r.TaskUID != "task-uid-"+r.TaskName {
		t.Fatalf("observed Task identity lost: %+v", r)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("public answer")))
	if r.AnswerSHA256 != wantDigest {
		t.Fatalf("observed answer digest lost: got %q, want %q", r.AnswerSHA256, wantDigest)
	}
	if len(r.Assertions) != 2 || judgeCalls.Load() != 0 || r.Assertions[1].Judge == nil || len(r.Assertions[1].Judge.Scores) != 0 {
		t.Fatalf("content failure judged an answer: %+v judgeCalls=%d", r, judgeCalls.Load())
	}
	for _, assertion := range r.Assertions {
		if assertion.Verdict != agentruntime.EvaluationUnknown {
			t.Fatalf("content failure accepted assertion: %+v", assertion)
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"public answer", "private-writer-canary", "PRIVATE_REASON_CANARY", orkaTestToken()} {
		if strings.Contains(string(raw)+f.out.String()+f.stderr.String(), forbidden) {
			t.Fatalf("content failure leaked %q", forbidden)
		}
	}
	assertOrkaForwardExited(t, f.dir)
}

func TestSuiteTaskJudgesOnlyAttributedAnswerAndExplicitContent(t *testing.T) {
	for _, show := range []bool{false, true} {
		t.Run(fmt.Sprintf("show=%t", show), func(t *testing.T) {
			f := newSuiteTaskFixture(t, suiteTaskJudgeCase)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				var payload struct {
					Model    string                           `json:"model"`
					Messages []struct{ Role, Content string } `json:"messages"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if req.URL.Path != "/v1/chat/completions" || payload.Model != "fixture/model" || len(payload.Messages) != 2 {
					t.Error("wrong judge request")
				} else {
					var data map[string]string
					if err := json.Unmarshal([]byte(payload.Messages[1].Content), &data); err != nil {
						t.Error(err)
					}
					if data["input"] != "Packaged question" || data["answer"] != "answer PRIVATE_ANSWER_CANARY" || data["rubric"] != "PRIVATE_RUBRIC_CANARY" {
						t.Error("judge did not receive packaged input/attributed answer")
					}
				}
				_, _ = io.WriteString(w, judgeEnvelope(`{"score":90,"reasoning":"PRIVATE_REASON_CANARY"}`))
			}))
			defer server.Close()
			f.opt.Judge = evaluationJudgeConfig{BaseURL: server.URL}
			f.opt.ShowContent = show
			r, err := f.evaluate(t)
			if err != nil || r.Verdict != agentruntime.EvaluationPass || calls.Load() != 2 || len(r.Assertions) != 2 || r.Assertions[1].Judge == nil || !agentruntime.EvaluationJudgeEvidenceValid(r.Assertions[1]) {
				t.Fatalf("result: %+v %v judgeCalls=%d", r, err, calls.Load())
			}
			for _, content := range []string{"PRIVATE_ANSWER_CANARY", "PRIVATE_REASON_CANARY"} {
				if strings.Contains(f.out.String(), content) != show {
					t.Fatalf("content option not honored: %q", f.out)
				}
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), "PRIVATE_") {
				t.Fatalf("content retained: %s", raw)
			}
		})
	}
}

// A successful native Task is not an assertion pass, and tool evidence is
// unavailable rather than inferred from answer text or installation readiness.
func TestSuiteTaskAssertionsDoNotInferExecutionSuccessOrTools(t *testing.T) {
	for _, tc := range []struct {
		name, assertion string
		want            agentruntime.EvaluationVerdict
	}{
		{"missing", "{id: missing, type: contains, value: absent}", agentruntime.EvaluationFail},
		{"notContains", "{id: negative, type: notContains, value: answer}", agentruntime.EvaluationFail},
		{"regex", "{id: regex, type: regex, pattern: '^answer '}", agentruntime.EvaluationPass},
		{"toolCalled", "{id: tool, type: toolCalled, tool: nonexistent}", agentruntime.EvaluationUnknown},
		{"toolNotCalled", "{id: no-tool, type: toolNotCalled, tool: nonexistent}", agentruntime.EvaluationUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSuiteTaskFixture(t, "id: packaged\ninput: Packaged question\nassertions:\n- "+tc.assertion+"\n")
			before := len(f.calls)
			r, err := f.evaluate(t)
			if err != nil || r.Verdict != tc.want || len(r.Assertions) != 1 || r.Assertions[0].Verdict != tc.want || r.AnswerSHA256 == "" || len(f.writes(before)) != 1 {
				t.Fatalf("native success inferred: %+v %v", r, err)
			}
		})
	}
}

// Drift at any observation boundary invalidates a passing answer. The fixtures
// change live identities, never replace the verifier with a mocked callback.
func TestSuiteTaskLiveDriftNeverPasses(t *testing.T) {
	for _, stage := range []string{"before", "session", "result", "judge"} {
		for _, drift := range []string{"owned", "binding", "secret", "runtime", "target"} {
			t.Run(stage+"/"+drift, func(t *testing.T) {
				f := newSuiteTaskFixture(t, suiteTaskJudgeCase)
				mutate := func() {
					switch drift {
					case "owned":
						m := f.stored(t, "Agent")
						nativeSpec(m)["systemPrompt"] = map[string]any{"inline": "drift"}
						seedReconcile(t, f.dir, m)
					case "binding":
						nativeMeta(f.source)["resourceVersion"] = "new"
					case "secret":
						f.secretRV = "new"
					case "runtime":
						nativeMeta(f.controller)["uid"] = "new"
					case "target":
						f.clusterUID = "new"
					}
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					_, _ = io.WriteString(w, judgeEnvelope(`{"score":90,"reasoning":"PRIVATE_REASON_CANARY"}`))
				}))
				defer server.Close()
				f.opt.Judge = evaluationJudgeConfig{BaseURL: server.URL}
				switch stage {
				case "before":
					mutate()
				case "session":
					f.beforeCapture = func(c orkaCall) {
						if slices.Contains(c.Args, "token") {
							mutate()
							f.beforeCapture = nil
						}
					}
				case "result":
					f.afterResult = mutate
				case "judge":
					f.beforeCapture = func(c orkaCall) {
						if calls.Load() == 2 {
							mutate()
							f.beforeCapture = nil
						}
					}
				}
				before := len(f.calls)
				r, err := f.evaluate(t)
				if r.Verdict != agentruntime.EvaluationUnknown {
					t.Fatalf("drift passed: %+v %v", r, err)
				}
				wantTasks, wantJudges := 1, int32(0)
				if stage == "before" || stage == "session" {
					wantTasks = 0
					if err == nil {
						t.Fatal("pre-create refusal lost")
					}
				}
				if stage == "judge" {
					wantJudges = 2
				}
				if len(f.writes(before)) != wantTasks || calls.Load() != wantJudges {
					t.Fatalf("tasks=%d judges=%d", len(f.writes(before)), calls.Load())
				}
				if wantTasks == 1 && (r.TaskName == "" || r.TaskUID == "" || r.AnswerSHA256 == "") {
					t.Fatal("original execution identity/digest lost")
				}
				if len(r.Assertions) != 2 || r.Assertions[1].Verdict != agentruntime.EvaluationUnknown || r.Assertions[1].Judge == nil || len(r.Assertions[1].Judge.Scores) != 0 {
					t.Fatalf("unattributed judge scores accepted: %+v", r)
				}
				if strings.Contains(f.out.String()+f.stderr.String(), "PRIVATE_") {
					t.Fatal("private output on drift")
				}
			})
		}
	}
}

func TestSuiteTaskAmbiguousCreateAndResultLossNeverResend(t *testing.T) {
	for _, mode := range []string{"ambiguous", "disconnect", "forward-loss", "cancel", "timeout", "failed", "result-error", "credential-echo", "task-UID", "task-generation"} {
		t.Run(mode, func(t *testing.T) {
			f := newSuiteTaskFixture(t, suiteTaskCase)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			switch mode {
			case "ambiguous":
				f.ambiguousCreate = true
			case "disconnect":
				f.disconnect = true
			case "forward-loss":
				f.holdBody = true
				go func() {
					for f.resultReads.Load() == 0 && ctx.Err() == nil {
						time.Sleep(time.Millisecond)
					}
					raw, err := os.ReadFile(filepath.Join(f.dir, "forward-pid"))
					if err != nil {
						t.Error(err)
						cancel()
						return
					}
					pid, err := strconv.Atoi(string(raw))
					if err != nil {
						t.Error(err)
						cancel()
						return
					}
					process, err := os.FindProcess(pid)
					if err != nil {
						t.Error(err)
						cancel()
						return
					}
					if err := process.Kill(); err != nil {
						t.Error(err)
						cancel()
					}
				}()
			case "cancel":
				f.holdBody = true
				go func() {
					for f.resultReads.Load() == 0 && ctx.Err() == nil {
						time.Sleep(time.Millisecond)
					}
					cancel()
				}()
			case "timeout":
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				f.holdBody = true
			case "failed":
				f.phase = "Failed"
			case "result-error":
				f.resultStatus = 500
			case "credential-echo":
				f.answer = "answer " + orkaTestToken()
			case "task-UID", "task-generation":
				f.beforeCapture = func(c orkaCall) {
					if f.resultReads.Load() == 0 || !slices.Contains(c.Args, "tasks.core.orka.ai") {
						return
					}
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, task := range f.tasks {
						if mode == "task-UID" {
							nativeMeta(task)["uid"] = "replacement-task"
						} else {
							nativeMeta(task)["generation"] = 2
						}
					}
				}
			}
			before := len(f.calls)
			r, err := f.app.evaluateSuiteNative(ctx, f.placement, f.packaged, f.opt)
			want := agentruntime.EvaluationUnknown
			if mode == "failed" {
				want = agentruntime.EvaluationFail
			}
			if err != nil || r.Verdict != want || len(f.writes(before)) != 1 || r.AnswerSHA256 != "" {
				t.Fatalf("outcome: %+v %v writes=%d", r, err, len(f.writes(before)))
			}
			if mode == "ambiguous" && r.TaskUID != "" || mode != "ambiguous" && r.TaskUID == "" {
				t.Fatal("ambiguous/observed UID semantics changed")
			}
			if f.connections.Load() != 1 {
				t.Fatal("result session reconnected")
			}
			if strings.Contains(f.out.String()+f.stderr.String(), "PRIVATE_") || strings.Contains(f.out.String()+f.stderr.String(), orkaTestToken()) {
				t.Fatal("runtime/provider diagnostic leaked")
			}
			assertOrkaForwardExited(t, f.dir)
		})
	}
}

// If the session is lost just as the final live read completes, even a valid
// judge score cannot make an ended case pass.
func TestSuiteTaskCancellationAtFinalObservationIsUnknown(t *testing.T) {
	f := newSuiteTaskFixture(t, suiteTaskJudgeCase)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, judgeEnvelope(`{"score":90,"reasoning":"private reason"}`))
	}))
	defer server.Close()
	f.opt.Judge.BaseURL = server.URL
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ownedReads := 0
	f.after = func(c orkaCall) error {
		if calls.Load() == 2 && slices.Contains(c.Args, "get") && slices.Contains(c.Args, "agents.core.orka.ai") {
			ownedReads++
			if ownedReads == 3 {
				cancel()
			}
		}
		return nil
	}
	r, err := f.app.evaluateSuiteNative(ctx, f.placement, f.packaged, f.opt)
	if err != nil || ownedReads != 3 || r.Verdict != agentruntime.EvaluationUnknown || r.Assertions[1].Verdict != agentruntime.EvaluationUnknown || len(r.Assertions[1].Judge.Scores) != 0 {
		t.Fatalf("ended case accepted: %+v %v finalReads=%d", r, err, ownedReads)
	}
}

func TestSuiteTaskTerminalFailureWithEndedSessionIsUnknown(t *testing.T) {
	f := newSuiteTaskFixture(t, suiteTaskCase)
	f.phase = "Failed"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	terminalRead, ownedReads := false, 0
	f.after = func(c orkaCall) error {
		if terminalRead && slices.Contains(c.Args, "get") && slices.Contains(c.Args, "agents.core.orka.ai") {
			ownedReads++
			if ownedReads == 3 {
				cancel()
			}
		}
		return nil
	}
	// Task reads use the Task fixture rather than the pair's after-read hook.
	f.beforeCapture = func(c orkaCall) {
		if slices.Contains(c.Args, "tasks.core.orka.ai") {
			terminalRead = true
		}
	}
	r, err := f.app.evaluateSuiteNative(ctx, f.placement, f.packaged, f.opt)
	if err != nil || ownedReads != 3 || r.Verdict != agentruntime.EvaluationUnknown || r.Detail != evaluationResultUnavailable {
		t.Fatalf("ended case accepted terminal failure: %+v %v finalReads=%d", r, err, ownedReads)
	}
}

func TestSuiteTaskRefusesInvalidCaseDeadlineAndPlacementBeforeCreate(t *testing.T) {
	for _, mode := range []string{"case", "digest", "judge", "deadline", "canceled", "placement", "guard", "port", "preflight"} {
		t.Run(mode, func(t *testing.T) {
			f := newSuiteTaskFixture(t, suiteTaskCase)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			switch mode {
			case "case":
				f.packaged.Bytes = []byte("id: invalid\ninput: PRIVATE_INVALID_CASE_CANARY\nassertions: [{id: x, type: unsupported}]\n")
				f.packaged.Digest = "sha256:" + fmt.Sprintf("%x", sha256.Sum256(f.packaged.Bytes))
			case "digest":
				f.packaged.Bytes = []byte(suiteTaskCase + "# edited\n")
			case "judge":
				f.opt.Judge.BaseURL = "https://user:PRIVATE_KEY_CANARY@model.invalid"
			case "deadline":
				ctx = t.Context()
			case "canceled":
				cancel()
			case "placement":
				f.placement.Complete = false
			case "guard":
				f.app.guarded = false
			case "port":
				f.opt.ResultPort = "not-a-port"
			case "preflight":
				fakeTool(t, f.dir, "kubectl", "printf '%s\\n' 'PRIVATE_RAW_MODEL_STDERR_CANARY "+orkaTestToken()+"' >&2; exit 1")
			}
			before := len(f.calls)
			r, err := f.app.evaluateSuiteNative(ctx, f.placement, f.packaged, f.opt)
			if err == nil || r.Verdict == agentruntime.EvaluationPass || len(f.writes(before)) != 0 {
				t.Fatalf("invalid accepted: %+v %v", r, err)
			}
			if strings.Contains(err.Error()+f.out.String()+f.stderr.String(), "PRIVATE_") {
				t.Fatal("unsafe failure output")
			}
		})
	}
}
