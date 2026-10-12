package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKagentEvaluateRejectsSelectors(t *testing.T) {
	for _, opt := range []EvaluateAgentBundleOptions{
		{Runtime: "kagent", RuntimeVersion: "v0.10.2"},
		{Runtime: "orka", RuntimeVersion: "v0.10.3"},
		{Runtime: "orka", Bindings: "bindings.yaml"},
		{Runtime: "orka", ToNamespace: "agents"},
		{Runtime: "foreign"},
	} {
		err := (&App{}).EvaluateAgentBundle(opt)
		if err == nil || strings.Contains(err.Error(), "configured kubectl") {
			t.Fatalf("foreign selector reached execution: %v", err)
		}
	}
}

func evaluationTaskJSON(rpc, message, state, artifact string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"kind":"task","id":"task-1","contextId":"context-1","status":{"state":%q},"artifacts":%s,"history":[{"kind":"message","role":"user","messageId":%q,"parts":[{"kind":"text","text":"input"}]}]}}`, rpc, state, artifact, message))
}

func TestKagentEvaluationParserExactText(t *testing.T) {
	raw := evaluationTaskJSON("rpc-1", "request-1", "completed", `[{"artifactId":"answer-1","parts":[{"kind":"text","text":"  first \n"},{"kind":"text","text":"second\t "}]}]`)
	execution, answer, err := parseKagentEvaluationResponse(raw, kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
	if err != nil || answer != "  first \n\nsecond\t " || execution.TaskID != "task-1" || execution.ContextID != "context-1" || execution.State != "completed" {
		t.Fatalf("exact answer/native identity lost: %+v %q %v", execution, answer, err)
	}
}

func TestKagentEvaluationParserRepeatedRequestHistoryIsOneTurn(t *testing.T) {
	base := string(evaluationTaskJSON("rpc-1", "request-1", "completed", `[{"artifactId":"answer-1","parts":[{"kind":"text","text":"answer"}]}]`))
	user := `{"kind":"message","role":"user","messageId":"request-1","parts":[{"kind":"text","text":"input"}]}`
	raw := []byte(strings.Replace(base, `"history":[`, `"history":[`+user+`,`, 1))
	_, answer, err := parseKagentEvaluationResponse(raw, kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
	if err != nil || answer != "answer" {
		t.Fatalf("same submitted message was counted as prior history: %v", err)
	}
}

func TestKagentEvaluationParserRefusesAmbiguity(t *testing.T) {
	base := string(evaluationTaskJSON("rpc-1", "request-1", "completed", `[{"artifactId":"answer-1","parts":[{"kind":"text","text":"answer"}]}]`))
	for _, tc := range []struct{ name, old, replacement string }{
		{"rpc", `"id":"rpc-1"`, `"id":"other"`},
		{"exact-EOF", base, base + " {}"},
		{"artifact-parts-type", `"parts":[{"kind":"text","text":"answer"}]`, `"parts":{}`},
		{"artifact-empty-parts", `"parts":[{"kind":"text","text":"answer"}]`, `"parts":[]`},
		{"rpc-duplicate", `"id":"rpc-1"`, `"id":"rpc-1","ID":"rpc-1"`},
		{"rpc-error", `"result":`, `"error":{"message":"private diagnostic"},"result":`},
		{"message", `"kind":"task"`, `"kind":"message"`},
		{"task-id", `"id":"task-1"`, `"id":"bad/id"`},
		{"context", `"contextId":"context-1"`, `"contextId":null`},
		{"status", `"state":"completed"`, `"state":42`},
		{"blank", `"text":"answer"`, `"text":"  "`},
		{"control", `"text":"answer"`, `"text":"answer\u001b"`},
		{"text-type", `"text":"answer"`, `"text":null`},
		{"part-kind", `"kind":"text","text":"answer"`, `"kind":"data","text":"answer"`},
		{"artifact-id", `"artifactId":"answer-1"`, `"artifactId":null`},
		{"multiple-answers", `"artifacts":[`, `"artifacts":[{"artifactId":"other","parts":[{"kind":"text","text":"other"}]},`},
		{"history-user", `"messageId":"request-1"`, `"messageId":"foreign"`},
		{"history-input", `"text":"input"`, `"text":"foreign input"`},
		{"history-task", `"messageId":"request-1"`, `"messageId":"request-1","taskId":"other"`},
		{"extra-user", `"history":[`, `"history":[{"kind":"message","role":"user","messageId":"previous","parts":[{"kind":"text","text":"input"}]},`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(strings.Replace(base, tc.old, tc.replacement, 1))
			_, _, err := parseKagentEvaluationResponse(raw, kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
			if err == nil || strings.Contains(err.Error(), "private diagnostic") || strings.Contains(err.Error(), "answer") {
				t.Fatalf("ambiguous/payload diagnostic: %v", err)
			}
		})
	}
}

func TestKagentEvaluationParserNativeStates(t *testing.T) {
	for _, state := range []string{"failed", "canceled", "rejected", "working", "input-required", "auth-required", "submitted", "unknown"} {
		execution, answer, err := parseKagentEvaluationResponse(evaluationTaskJSON("rpc-1", "request-1", state, `[]`), kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
		if err != nil || execution.State != state || answer != "" {
			t.Fatalf("native state %s lost: %+v %q %v", state, execution, answer, err)
		}
	}
}

func kagentEvaluateLiftFixture(t *testing.T) (*App, EvaluateAgentBundleOptions, string) {
	t.Helper()
	a, lift, dir := kagentLiftFixture(t, "")
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	eval := filepath.Join(lift.BundleDir, "eval")
	if err := os.Mkdir(eval, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(eval, id+".yaml"), []byte("id: "+id+"\ninput: input\nexpectContains: [exact]\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return a, EvaluateAgentBundleOptions{BundleDir: lift.BundleDir, Runtime: "kagent", RuntimeVersion: "v0.10.3", ToContext: lift.ToContext}, dir
}

func readKagentEvaluationForTest(t *testing.T, opt EvaluateAgentBundleOptions) (kagentEvaluationReceipt, []byte) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(opt.BundleDir, "receipts", "eval-kagent-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("latest receipts: %v %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parseKagentEvaluation(raw); !ok {
		t.Fatal("produced receipt is not strictly readable")
	}
	var r kagentEvaluationReceipt
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r, raw
}

func evaluationSends(t *testing.T, dir string) []kagentCall {
	t.Helper()
	var sends []kagentCall
	for _, call := range kagentCalls(t, dir) {
		if call.Body["method"] == "message/send" {
			sends = append(sends, call)
		}
	}
	return sends
}

func TestKagentEvaluateExactAnswerAndFreshIDs(t *testing.T) {
	a, opt, dir := kagentEvaluateLiftFixture(t)
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
	if err := a.EvaluateAgentBundle(opt); err != nil {
		t.Fatal(err, a.Err)
	}
	r, raw := readKagentEvaluationForTest(t, opt)
	if r.Result != "pass" || len(r.Cases) != 2 {
		t.Fatalf("result=%+v", r)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("  exact\nanswer\t ")))
	for _, c := range r.Cases {
		if c.AnswerSHA256 != hash || c.Execution.State != "completed" {
			t.Fatalf("exact answer lost: %+v", c)
		}
	}
	if r.Cases[0].Execution.TaskID == r.Cases[1].Execution.TaskID || r.Cases[0].Execution.ContextID == r.Cases[1].Execution.ContextID {
		t.Fatal("cases reused a conversation")
	}
	if bytes.Contains(raw, []byte("exact")) || bytes.Contains(raw, []byte("input\"")) {
		t.Fatal("receipt contains payload")
	}
	sends := evaluationSends(t, dir)
	if len(sends) != 2 {
		t.Fatalf("sends=%d", len(sends))
	}
	for i, send := range sends {
		params := send.Body["params"].(map[string]any)
		message := params["message"].(map[string]any)
		if params["configuration"].(map[string]any)["blocking"] != true || message["taskId"] != nil || message["contextId"] != nil || send.Body["id"] != r.Cases[i].Execution.RPCID || message["messageId"] != r.Cases[i].Execution.RequestMessageID {
			t.Fatal("not one fresh correlated blocking Task")
		}
	}
	depPaths, _ := filepath.Glob(filepath.Join(opt.BundleDir, kagentLiftReceiptDir, "*.json"))
	dep, _ := os.ReadFile(depPaths[0])
	if r.DeploymentReceiptDigest != fmt.Sprintf("%x", sha256.Sum256(dep)) {
		t.Fatal("deployment digest not exact bytes")
	}
	if r.Target.Platform.ControllerImage.Digest != "8ca13042c21181ecf280d6f3ca005d58e216a0a56978bb8824fd73cc02b685d8" {
		t.Fatal("platform evidence inferred instead of observed")
	}
}

func TestKagentEvaluateRefusesInvalidHistoryDirectoryBeforeSend(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprint(link), func(t *testing.T) {
			a, opt, dir := kagentEvaluateLiftFixture(t)
			path := filepath.Join(opt.BundleDir, "receipts", "runs")
			var err error
			if link {
				err = os.Symlink(t.TempDir(), path)
			} else {
				err = os.WriteFile(path, []byte("not a directory"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = a.EvaluateAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "history directory unavailable") || len(evaluationSends(t, dir)) != 0 {
				t.Fatalf("invalid history allowed execution: %v", err)
			}
		})
	}
}

func TestKagentEvaluateOutputFailureStopsWithoutResend(t *testing.T) {
	a, opt, dir := kagentEvaluateLiftFixture(t)
	a.Out = evaluationDiffFailWriter{}
	err := a.EvaluateAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "output unavailable") || strings.Contains(err.Error(), "PRIVATE_WRITER_CANARY") {
		t.Fatalf("output failure lost or exposed: %v", err)
	}
	r, _ := readKagentEvaluationForTest(t, opt)
	if len(evaluationSends(t, dir)) != 1 || r.Cases[0].Execution.State != "completed" || r.Cases[1].Verdict != "unknown" {
		t.Fatalf("output failure sent more work or lost observed identity: %+v", r.Cases)
	}
}

func TestKagentEvaluateFullVersusSelected(t *testing.T) {
	a, opt, _ := kagentEvaluateLiftFixture(t)
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
	if err := a.EvaluateAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	full, _ := readKagentEvaluationForTest(t, opt)
	opt.Case = "one"
	if err := a.EvaluateAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	selected, _ := readKagentEvaluationForTest(t, opt)
	if !full.FullCaseSet || selected.FullCaseSet || len(selected.Cases) != 1 || full.CasesDigest == selected.CasesDigest || full.RunID == selected.RunID {
		t.Fatal("full/selected evidence conflated")
	}
	archives, _ := filepath.Glob(filepath.Join(opt.BundleDir, "receipts/runs/eval-*.json"))
	if len(archives) != 2 {
		t.Fatalf("archives=%d", len(archives))
	}
}

func TestKagentEvaluateInitialRefusalNeverSends(t *testing.T) {
	for _, scenario := range []string{"missing-receipt", "trusted-proxy", "python", "dependency-drift", "wrong-version"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := kagentEvaluateLiftFixture(t)
			switch scenario {
			case "missing-receipt":
				if err := os.RemoveAll(filepath.Join(opt.BundleDir, kagentLiftReceiptDir)); err != nil {
					t.Fatal(err)
				}
			case "python":
				path := filepath.Join(opt.BundleDir, "agent.yaml")
				raw, _ := os.ReadFile(path)
				if err := os.WriteFile(path, bytes.ReplaceAll(raw, []byte("runtime: go"), []byte("runtime: python")), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				t.Setenv("KMX_KAGENT_TEST_SCENARIO", scenario)
			}
			want := map[string]string{"missing-receipt": "complete selected-target deployment receipt", "trusted-proxy": "trusted-proxy refused", "python": "Python is render-only", "dependency-drift": "dependency identity", "wrong-version": "qualification"}[scenario]
			if err := a.EvaluateAgentBundle(opt); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("wrong initial refusal: %v; want %s", err, want)
			}
			if len(evaluationSends(t, dir)) != 0 {
				t.Fatal("initial refusal sent a Task")
			}
			paths, _ := filepath.Glob(filepath.Join(opt.BundleDir, "receipts", "eval-kagent-*.json"))
			if len(paths) != 0 {
				t.Fatal("initial refusal fabricated run")
			}
		})
	}
}

func TestKagentEvaluateAmbiguousSendStopsRemainingCases(t *testing.T) {
	for _, scenario := range []string{"wrong-rpc-id", "eval-repeated-context", "eval-malformed", "task-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := kagentEvaluateLiftFixture(t)
			t.Setenv("KMX_KAGENT_TEST_SCENARIO", scenario)
			if scenario == "task-timeout" {
				kagentTestPhaseDeadline(t, a, "kagent-evaluation-case", 200*time.Millisecond)
			}
			if err := a.EvaluateAgentBundle(opt); err == nil {
				t.Fatal("ambiguous execution passed")
			}
			r, _ := readKagentEvaluationForTest(t, opt)
			want := 1
			if scenario == "eval-repeated-context" {
				want = 2
			}
			if len(evaluationSends(t, dir)) != want {
				t.Fatalf("resend after ambiguity: %d", len(evaluationSends(t, dir)))
			}
			for i, c := range r.Cases {
				if i >= want-1 && c.Verdict != "unknown" {
					t.Fatalf("unknown execution passed: %+v", c)
				}
			}
			if r.Cases[0].Execution.RPCID == r.Cases[1].Execution.RPCID || r.Cases[0].Execution.RequestMessageID == r.Cases[1].Execution.RequestMessageID {
				t.Fatal("stopped cases lack reserved fresh request IDs")
			}
		})
	}
}

func TestKagentEvaluatePostDriftNeverJudges(t *testing.T) {
	for _, scenario := range []string{"eval-post-drift", "eval-failed-drift"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := kagentEvaluateLiftFixture(t)
			var calls atomic.Int32
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected judge", 500) }))
			defer judge.Close()
			opt.JudgeBaseURL = judge.URL
			for _, id := range []string{"one", "two"} {
				data := "id: " + id + "\ninput: input\nassertions:\n  - id: quality\n    type: llmJudge\n    rubric: Check answer.\n    judgeModel: judge-fixture\n    judgeVersion: v1\n    minScore: 50\n"
				if err := os.WriteFile(filepath.Join(opt.BundleDir, "eval", id+".yaml"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("KMX_KAGENT_TEST_SCENARIO", scenario)
			if err := a.EvaluateAgentBundle(opt); err == nil {
				t.Fatal("lost attribution passed")
			}
			r, _ := readKagentEvaluationForTest(t, opt)
			if len(evaluationSends(t, dir)) != 1 || calls.Load() != 0 {
				t.Fatal("lost attribution caused another agent/judge send")
			}
			for _, c := range r.Cases {
				if c.Verdict != "unknown" || c.AnswerSHA256 != "" || c.Assertions[0].Judge == nil || len(c.Assertions[0].Judge.Scores) != 0 {
					t.Fatalf("lost attribution retained judged answer: %+v", c)
				}
			}
			want := "completed"
			if scenario == "eval-failed-drift" {
				want = "failed"
			}
			if r.Cases[0].Execution.State != want || r.Cases[0].Execution.TaskID == "" {
				t.Fatal("observed native identities lost on drift")
			}
		})
	}
}

func TestKagentEvaluateLocalSnapshotStopsAfterSend(t *testing.T) {
	for _, input := range []string{"agent.yaml", "bindings.yaml", "eval/two.yaml", "receipt"} {
		t.Run(input, func(t *testing.T) {
			a, opt, dir := kagentEvaluateLiftFixture(t)
			path := filepath.Join(opt.BundleDir, input)
			if input == "receipt" {
				paths, _ := filepath.Glob(filepath.Join(opt.BundleDir, kagentLiftReceiptDir, "*.json"))
				path = paths[0]
			}
			t.Setenv("KMX_KAGENT_TEST_EVAL_MUTATE", path)
			t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-local-drift")
			if err := a.EvaluateAgentBundle(opt); err == nil {
				t.Fatal("changed local snapshot passed")
			}
			r, _ := readKagentEvaluationForTest(t, opt)
			if len(evaluationSends(t, dir)) != 1 || r.Cases[0].Verdict != "unknown" || r.Cases[0].Execution.TaskID == "" || r.Cases[1].Execution.TaskID != "" {
				t.Fatal("changed snapshot allowed another send or lost IDs")
			}
		})
	}
}

func TestKagentEvaluateSelectedCaseSnapshotsUnselectedFiles(t *testing.T) {
	a, opt, dir := kagentEvaluateLiftFixture(t)
	opt.Case = "one"
	t.Setenv("KMX_KAGENT_TEST_EVAL_MUTATE", filepath.Join(opt.BundleDir, "eval", "two.yaml"))
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-pre-local-drift")
	if err := a.EvaluateAgentBundle(opt); err == nil {
		t.Fatal("changed unselected file authorized execution")
	}
	if len(evaluationSends(t, dir)) != 0 {
		t.Fatal("initial snapshot drift sent a Task")
	}
}

func TestKagentEvaluationPreflightRejectsUnrepresentableModel(t *testing.T) {
	r := kagentEvaluationFixture(t)
	r.Target.Model = strings.Repeat("m", 257)
	cases := []bundleEvaluationCase{{Case: agentruntime.EvaluationCase{ID: "case", Input: "input", ExpectContains: []string{"answer"}}}}
	if err := preflightKagentEvaluationReceiptSize(cases, r); err == nil {
		t.Fatal("unrepresentable target allowed execution before receipt refusal")
	}
}

func TestKagentEvaluationBudgetIncludesActualRichTarget(t *testing.T) {
	r := kagentEvaluationFixture(t)
	cases := make([]bundleEvaluationCase, 200)
	for i := range cases {
		cases[i].Case = agentruntime.EvaluationCase{ID: "case", Input: "input", ExpectContains: []string{"answer"}}
	}
	if err := preflightKagentEvaluationReceiptSize(cases, r); err != nil {
		t.Fatalf("small target should fit: %v", err)
	}
	for i := 0; i < 600; i++ {
		r.Target.Snapshot.Dependencies = append(r.Target.Snapshot.Dependencies, agentruntime.ResourceEvidence{Kind: "ConfigMap", Name: strings.Repeat("n", 240) + fmt.Sprintf("-%03d", i), Namespace: r.Target.Namespace, UID: strings.Repeat("u", 240) + fmt.Sprintf("-%03d", i), ResourceVersion: "1"})
	}
	if err := preflightKagentEvaluationReceiptSize(cases, r); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("rich target not budgeted: %v", err)
	}
}

func TestKagentEvaluateReceiptBudgetRefusesBeforeSend(t *testing.T) {
	a, opt, dir := kagentEvaluateLiftFixture(t)
	for i := 0; i < 240; i++ {
		id := fmt.Sprintf("case-%03d", i)
		if err := os.WriteFile(filepath.Join(opt.BundleDir, "eval", id+".yaml"), []byte("id: "+id+"\ninput: input\nexpectContains: [exact]\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
	if err := a.EvaluateAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("wrong budget refusal: %v", err)
	}
	if len(evaluationSends(t, dir)) != 0 {
		t.Fatal("oversized receipt budget sent a Task")
	}
}

func TestKagentEvaluationRejectsMixedObservedImages(t *testing.T) {
	image := kagentEvaluationFixture(t).Target.Platform.ControllerImage
	service := kagentControllerService{ControllerImages: []kagentEvaluationImage{image, {OS: "linux", Architecture: "arm64", Digest: kagentLiftProfile().ControllerDigests[2]}}}
	if _, err := kagentObservedEvaluationPlatform(kagentLiftProfile(), service, nil); err == nil || !strings.Contains(err.Error(), "mixed") {
		t.Fatalf("mixed qualified replicas accepted: %v", err)
	}
}

func TestKagentEvaluateNativeFailureOnlyWhenAttributed(t *testing.T) {
	a, opt, dir := kagentEvaluateLiftFixture(t)
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-failed")
	if err := a.EvaluateAgentBundle(opt); err == nil {
		t.Fatal("native failure passed")
	}
	r, _ := readKagentEvaluationForTest(t, opt)
	if r.Result != "fail" || r.Cases[0].Execution.State != "failed" || r.Cases[0].Detail != evaluationExecutionFailed || len(evaluationSends(t, dir)) != 2 {
		t.Fatal("attributed native failure confused with ambiguous send")
	}
}
