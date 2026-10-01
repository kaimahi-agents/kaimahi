package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// TestEvalKubectlHelper is the fake kubectl for evaluate. It serves the
// destination's kube-system UID, one live Agent from agent.json, the result
// account and Service, a token, a port-forward that only announces its bind
// (the test's own HTTP server owns the port), and Tasks: every create is
// logged and stored, and every read reports the phase KMX_EVAL_PHASE names.
func TestEvalKubectlHelper(t *testing.T) {
	dir := os.Getenv("KMX_EVAL_DIR")
	if dir == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if slices.Equal(args, []string{"version", "--client"}) {
		os.Exit(0)
	}
	fail := func() { fmt.Fprint(os.Stderr, "external failure "+orkaTestToken()); os.Exit(1) }
	if len(args) < 2 || args[0] != "--context" || args[1] != "kind-test" {
		fail()
	}
	call := orkaCall{Args: args}
	if slices.Contains(args, "-f") {
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(os.Stdin)
		_ = json.Unmarshal(raw.Bytes(), &call.Document)
	}
	log, _ := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	_ = json.NewEncoder(log).Encode(call)
	_ = log.Close()
	if slices.Contains(args, "config") {
		server := "https://127.0.0.1:6443"
		if os.Getenv("KMX_EVAL_REMOTE") == "1" {
			server = "https://managed.example.invalid"
		}
		fmt.Printf(`{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":%q}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`, server)
		os.Exit(0)
	}
	if slices.Contains(args, "port-forward") {
		if os.Getenv("KMX_EVAL_STOCK_RELEASE") == "1" && !slices.Contains(args, "svc/orka") {
			fail()
		}
		if delay := os.Getenv("KMX_EVAL_FORWARD_DELAY"); delay != "" {
			pause, err := time.ParseDuration(delay)
			if err != nil {
				fail()
			}
			time.Sleep(pause)
		}
		_ = os.WriteFile(filepath.Join(dir, "forward-pid"), []byte(fmt.Sprint(os.Getpid())), 0600)
		port, _, _ := strings.Cut(args[len(args)-1], ":")
		fmt.Println("Forwarding from 127.0.0.1:" + port + " -> 8080")
		time.Sleep(time.Hour)
	}
	if slices.Contains(args, "token") {
		fmt.Printf(`{"status":{"token":%q,"expirationTimestamp":%q}}`, orkaTestToken(), time.Now().Add(10*time.Minute).Format(time.RFC3339))
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 {
		kind, name := args[i+1], args[i+2]
		switch kind {
		case "namespace":
			fmt.Printf(`{"kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, getenvLiftTest("KMX_EVAL_CLUSTER_UID", "cluster-uid"))
		case "crd":
			plural, _, _ := strings.Cut(name, ".")
			raw, err := os.ReadFile(filepath.Join(os.Getenv("KMX_RECONCILE_FIXTURES"), "v0.1.3", plural+".yaml"))
			if err != nil {
				fail()
			}
			_, _ = os.Stdout.Write(raw)
		case "serviceaccount":
			fmt.Print("serviceaccount/" + name)
		case "services":
			if raw := os.Getenv("KMX_EVAL_SERVICES"); raw != "" {
				fmt.Print(raw)
				break
			}
			fmt.Print(`{"items":[{"metadata":{"name":"orka","labels":{"app.kubernetes.io/name":"orka"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"},"ports":[{"name":"api","port":8080}]}}]}`)
		case "service":
			if os.Getenv("KMX_EVAL_STOCK_RELEASE") == "1" && name != "orka" {
				fail()
			}
			fmt.Print(`{"spec":{"ports":[{"port":8080}]}}`)
		case "providers.core.orka.ai":
			raw, err := os.ReadFile(filepath.Join(dir, "provider.json"))
			if err != nil {
				os.Exit(0)
			}
			_, _ = os.Stdout.Write(raw)
		case "agents.core.orka.ai":
			raw, err := os.ReadFile(filepath.Join(dir, "agent.json"))
			if err != nil {
				os.Exit(0)
			}
			if swap := os.Getenv("KMX_EVAL_AGENT_SWAP_ON_READ"); swap != "" {
				countPath := filepath.Join(dir, "agent-read-count")
				countRaw, _ := os.ReadFile(countPath)
				count, _ := strconv.Atoi(string(countRaw))
				count++
				_ = os.WriteFile(countPath, []byte(strconv.Itoa(count)), 0600)
				threshold, _ := strconv.Atoi(swap)
				if count >= threshold {
					raw = bytes.Replace(raw, []byte(`"uid":"agent-uid"`), []byte(`"uid":"replacement-uid"`), 1)
				}
			}
			_, _ = os.Stdout.Write(raw)
		case "tasks.core.orka.ai":
			if delay := os.Getenv("KMX_EVAL_TASK_GET_DELAY"); delay != "" {
				pause, err := time.ParseDuration(delay)
				if err != nil {
					fail()
				}
				time.Sleep(pause)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "task-"+name+".json"))
			if err != nil {
				os.Exit(0)
			}
			var task map[string]any
			_ = json.Unmarshal(raw, &task)
			phase := getenvLiftTest("KMX_EVAL_PHASE", "Succeeded")
			if sequence := os.Getenv("KMX_EVAL_PHASE_SEQUENCE"); sequence != "" {
				path := filepath.Join(dir, "task-phase-read-count")
				countRaw, _ := os.ReadFile(path)
				count, _ := strconv.Atoi(string(countRaw))
				phases := strings.Split(sequence, ",")
				phase = phases[min(count, len(phases)-1)]
				_ = os.WriteFile(path, []byte(strconv.Itoa(count+1)), 0600)
			}
			if swap := os.Getenv("KMX_EVAL_TASK_SWAP_ON_READ"); swap != "" {
				threshold, _ := strconv.Atoi(swap)
				countRaw, _ := os.ReadFile(filepath.Join(dir, "task-phase-read-count"))
				count, _ := strconv.Atoi(string(countRaw))
				if count >= threshold {
					task["metadata"].(map[string]any)["uid"] = "replacement-task-uid"
				}
			}
			available := phase == "Succeeded" && os.Getenv("KMX_EVAL_RESULT_AVAILABLE") != "false"
			task["status"] = map[string]any{"phase": phase, "resultRef": map[string]any{"available": available}}
			_ = json.NewEncoder(os.Stdout).Encode(task)
		default:
			fail()
		}
		os.Exit(0)
	}
	if slices.Contains(args, "replace") && slices.Contains(args, "--dry-run=server") && call.Document != nil {
		_ = json.NewEncoder(os.Stdout).Encode(call.Document)
		os.Exit(0)
	}
	if slices.Contains(args, "create") && call.Document != nil && call.Document["kind"] == "Task" {
		if os.Getenv("KMX_EVAL_CREATE_FAIL") == "1" {
			fail()
		}
		meta := call.Document["metadata"].(map[string]any)
		meta["uid"] = "task-uid-" + meta["name"].(string)
		meta["generation"] = 1
		body, _ := json.Marshal(call.Document)
		_ = os.WriteFile(filepath.Join(dir, "task-"+meta["name"].(string)+".json"), body, 0600)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	fail()
}

type evalFixture struct {
	app                              *App
	opt                              EvaluateAgentBundleOptions
	dir, bundle                      string
	name, digest                     string
	out                              *bytes.Buffer
	answers                          map[string]string
	resultStatus                     int
	resultDisconnect, resultHoldBody bool
}

// newEvalFixture writes a created bundle (with its scaffolded example case
// replaced by the given cases), seeds a live Agent owned by it at its current
// digest, and serves Task results from answers keyed by prompt.
func newEvalFixture(t *testing.T, cases map[string]string) *evalFixture {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kubectl", "exec "+shellArg(exe)+" -test.run=^TestEvalKubectlHelper$ -- \"$@\"")
	t.Setenv("PATH", dir)
	t.Setenv("KMX_EVAL_DIR", dir)
	t.Setenv("KMX_HOME", filepath.Join(dir, "user-state"))
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	create := goldenNoTaskCreate("")
	source, err := portableOrkaSource(create)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "agents", create.Name)
	if err := writeOrkaBundle(bundle, source, mustBindingsSource(t, create)); err != nil {
		t.Fatal(err)
	}
	if cases != nil {
		if err := os.Remove(filepath.Join(bundle, "eval", "example.yaml")); err != nil {
			t.Fatal(err)
		}
		for file, body := range cases {
			if err := os.WriteFile(filepath.Join(bundle, "eval", file), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	out := &bytes.Buffer{}
	f := &evalFixture{
		app: &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Out: out, Err: &bytes.Buffer{}, Run: &run.Runner{Stdout: out, Stderr: &bytes.Buffer{}}},
		opt: EvaluateAgentBundleOptions{BundleDir: bundle, ToContext: "kind-test", CaseTimeout: 30 * time.Second},
		dir: dir, bundle: bundle, name: create.Name, digest: agentruntime.PortableBundleDigest(source), out: out,
		answers: map[string]string{}, resultStatus: http.StatusOK,
	}
	f.seedAgent(t, f.digest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		task := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
		raw, err := os.ReadFile(filepath.Join(dir, "task-"+task+".json"))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		if f.resultDisconnect {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		if f.resultHoldBody {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if f.resultStatus != http.StatusOK {
			w.WriteHeader(f.resultStatus)
			fmt.Fprint(w, `{"error":{"code":500,"message":"broken"}}`)
			return
		}
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		prompt := doc["spec"].(map[string]any)["prompt"].(string)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": f.answers[prompt]})
	}))
	t.Cleanup(server.Close)
	_, f.opt.ResultPort, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	return f
}

func (f *evalFixture) seedAgent(t *testing.T, digest string) {
	t.Helper()
	agent := map[string]any{
		"kind": "Agent",
		"metadata": map[string]any{"name": f.name, "namespace": "orka-system", "uid": "agent-uid", "generation": 1,
			"annotations": map[string]any{orkaBundleMarker: f.name, orkaPortableMarker: digest, orkaRenderedMarker: strings.Repeat("b", 64)}},
		"status": map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}},
	}
	raw, _ := json.Marshal(agent)
	if err := os.WriteFile(filepath.Join(f.dir, "agent.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *evalFixture) receipt(t *testing.T) (bundleEvaluationReceipt, string) {
	t.Helper()
	raw, err := os.ReadFile(bundleEvaluationReceiptPath(f.bundle, "kind-test", "orka-system", "cluster-uid"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt bundleEvaluationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, string(raw)
}

func (f *evalFixture) taskCreates(t *testing.T) int {
	t.Helper()
	count := 0
	for _, call := range orkaCalls(t, f.dir) {
		if call.Document != nil && call.Document["kind"] == "Task" && slices.Contains(call.Args, "create") {
			count++
		}
	}
	return count
}

const (
	evalSignCase  = "id: sign-off\ninput: Say hello and sign off.\nexpectContains:\n  - hello\n  - Signed, Sample\n"
	evalHelloCase = "id: greet\ninput: Say hello.\nexpectContains: [hello]\n"
)

func TestOrkaAPIServiceDiscoveryRefusesAbsentAndAmbiguousMatches(t *testing.T) {
	const chart = `{"metadata":{"name":"orka","labels":{"app.kubernetes.io/name":"orka"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"},"ports":[{"name":"api","port":8080}]}}`
	const metrics = `{"metadata":{"name":"orka-metrics","labels":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"},"ports":[{"name":"https","port":8443}]}}`
	const legacy = `{"metadata":{"name":"orka-api","labels":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","control-plane":"controller-manager"},"ports":[{"name":"http","port":8080}]}}`
	for _, tc := range []struct{ name, services, want string }{
		{"legacy with metrics", `{"items":[` + metrics + `,` + legacy + `]}`, "orka-api"},
		{"chart", `{"items":[` + chart + `]}`, "orka"},
		{"absent", `{"items":[` + metrics + `]}`, "no Orka API Service"},
		{"ambiguous", `{"items":[` + chart + `,` + legacy + `]}`, "multiple Orka API Services"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t, nil)
			t.Setenv("KMX_EVAL_SERVICES", tc.services)
			got, err := f.app.orkaAPIService(t.Context(), "orka-system")
			if tc.name == "absent" || tc.name == "ambiguous" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got service=%q error=%v; want %s", got, err, tc.want)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got service=%q error=%v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestEvaluateFindsStockHelmReleaseAPIService(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"case.yaml": evalHelloCase})
	f.answers["Say hello."] = "hello from Orka"
	t.Setenv("KMX_EVAL_STOCK_RELEASE", "1")
	if err := f.app.EvaluateAgentBundle(f.opt); err != nil {
		t.Fatalf("evaluate stock release: %v", err)
	}
	var list, selected, forwarded bool
	for _, c := range orkaCalls(t, f.dir) {
		line := strings.Join(c.Args, " ")
		list = list || strings.Contains(line, "get services -o json")
		selected = selected || strings.Contains(line, "get service orka -o json")
		forwarded = forwarded || slices.Contains(c.Args, "port-forward") && slices.Contains(c.Args, "svc/orka")
	}
	if !list || !selected || !forwarded {
		t.Fatalf("stock chart Service was not selected (list=%t get=%t forward=%t)", list, selected, forwarded)
	}
}

func TestWizardTaskFindsStockHelmReleaseAPIService(t *testing.T) {
	f := newEvalFixture(t, nil)
	f.answers["Say hello."] = "hello from Orka"
	t.Setenv("KMX_EVAL_STOCK_RELEASE", "1")
	opt := quickstartResultOptions("orka-system", "Say hello.")
	if opt.OrkaAPIService != "" {
		t.Fatal("wizard pinned a release-specific Service")
	}
	opt.ResultPort = f.opt.ResultPort // the fixture's HTTP server uses a free port
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	session, err := f.app.openOrkaResultSession(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	answer, err := f.app.runQuickstartOrkaTaskProfile(ctx, f.name, "orka-system", opt.Task, nil, nil, session)
	if err != nil || answer != "hello from Orka" {
		t.Fatalf("wizard task on stock release: answer=%q err=%v", answer, err)
	}
	var forwarded bool
	for _, c := range orkaCalls(t, f.dir) {
		forwarded = forwarded || slices.Contains(c.Args, "port-forward") && slices.Contains(c.Args, "svc/orka")
	}
	if !forwarded {
		t.Fatal("wizard result did not forward through stock chart API Service")
	}
}

func TestEvaluatePassRecordsDigestsAndNoAnswerText(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalSignCase, "b.yaml": evalHelloCase})
	f.answers["Say hello and sign off."] = "hello there. Signed, Sample"
	f.answers["Say hello."] = "hello from a private-answer-text"
	if err := f.app.EvaluateAgentBundle(f.opt); err != nil {
		t.Fatalf("passing evaluation returned %v\n%s", err, f.out)
	}
	for _, answer := range f.answers {
		if !strings.Contains(f.out.String(), answer) {
			t.Fatalf("answer %q not printed:\n%s", answer, f.out)
		}
	}
	receipt, raw := f.receipt(t)
	if receipt.Result != "pass" || len(receipt.Cases) != 2 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.PortableDigest != f.digest || receipt.Bundle != f.name || receipt.Target.AgentUID != "agent-uid" || receipt.Target.Context != "kind-test" || receipt.Target.Namespace != "orka-system" {
		t.Fatalf("receipt identity = %+v", receipt)
	}
	_, files, err := loadBundleEvaluationCases(f.bundle)
	if err != nil || receipt.CasesDigest != agentruntime.EvaluationCasesDigest(files) {
		t.Fatalf("cases digest %s does not identify the case files (%v)", receipt.CasesDigest, err)
	}
	for _, c := range receipt.Cases {
		if c.Verdict != "pass" || c.TaskName == "" || c.TaskUID != "task-uid-"+c.TaskName || len(c.Missing) != 0 {
			t.Fatalf("case = %+v", c)
		}
	}
	if got := receipt.Cases[0].AnswerSHA256; got != agentruntime.EvaluationAnswerDigest("hello there. Signed, Sample") {
		t.Fatalf("answer digest = %s", got)
	}
	for _, answer := range f.answers {
		if strings.Contains(raw, answer) {
			t.Fatal("receipt contains answer text")
		}
	}
	if strings.Contains(raw, "private-answer-text") || strings.Contains(raw+f.out.String(), orkaTestToken()) {
		t.Fatal("receipt or output leaked answer text or the session token")
	}
	if f.taskCreates(t) != 2 {
		t.Fatalf("Task creates = %d, want one per case", f.taskCreates(t))
	}
	assertOrkaForwardExited(t, f.dir)
}

func TestEvaluateMissingExpectationFailsAndExitsNonZero(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalSignCase})
	f.answers["Say hello and sign off."] = "hello there."
	err := f.app.EvaluateAgentBundle(f.opt)
	if err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("failing evaluation returned %v", err)
	}
	receipt, _ := f.receipt(t)
	c := receipt.Cases[0]
	if receipt.Result != "fail" || c.Verdict != "fail" || !slices.Equal(c.Matched, []string{"hello"}) || !slices.Equal(c.Missing, []string{"Signed, Sample"}) {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !strings.Contains(f.out.String(), "hello there.") || !strings.Contains(f.out.String(), `missing "Signed, Sample"`) {
		t.Fatalf("output did not show the answer and what it lacked:\n%s", f.out)
	}
}

func TestEvaluateFailedTaskIsFail(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
	t.Setenv("KMX_EVAL_PHASE", "Failed")
	if err := f.app.EvaluateAgentBundle(f.opt); err == nil {
		t.Fatal("a failed Task passed the gate")
	}
	receipt, _ := f.receipt(t)
	if receipt.Result != "fail" || receipt.Cases[0].Verdict != "fail" || !strings.Contains(receipt.Cases[0].Detail, "Failed") {
		t.Fatalf("receipt = %+v", receipt)
	}
	if f.taskCreates(t) != 1 {
		t.Fatal("a failed Task was retried")
	}
}

// An unreadable result is neither a pass nor a failure, and the Task that
// produced it is never resubmitted.
func TestEvaluateUnreadableResultIsUnknownAndNotRetried(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
	f.resultStatus = http.StatusInternalServerError
	err := f.app.EvaluateAgentBundle(f.opt)
	if err == nil {
		t.Fatal("an unknown case passed the gate")
	}
	receipt, _ := f.receipt(t)
	c := receipt.Cases[0]
	if receipt.Result != "unknown" || c.Verdict != "unknown" || c.TaskUID == "" || c.AnswerSHA256 != "" || len(c.Matched)+len(c.Missing) != 0 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if f.taskCreates(t) != 1 {
		t.Fatalf("Task creates = %d, want exactly one", f.taskCreates(t))
	}
}

// Any unknown case keeps the overall result unknown even when the others
// passed, and a failing case makes it fail.
func TestEvaluationOverallResult(t *testing.T) {
	for _, tc := range []struct {
		verdicts []string
		want     string
	}{
		{[]string{"pass", "pass"}, "pass"},
		{[]string{"pass", "unknown"}, "unknown"},
		{[]string{"unknown", "fail"}, "fail"},
	} {
		var cases []bundleEvaluationResult
		for _, verdict := range tc.verdicts {
			cases = append(cases, bundleEvaluationResult{Verdict: verdict})
		}
		if got := bundleEvaluationOverall(cases); got != tc.want {
			t.Errorf("%v = %s, want %s", tc.verdicts, got, tc.want)
		}
	}
}

func TestEvaluateRefusesBeforeRunning(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(*testing.T, *evalFixture)
	}{
		{"revision mismatch", "deployed revision differs; lift first", func(t *testing.T, f *evalFixture) {
			f.seedAgent(t, strings.Repeat("a", 64))
		}},
		{"not deployed", "lift first", func(t *testing.T, f *evalFixture) {
			_ = os.Remove(filepath.Join(f.dir, "agent.json"))
		}},
		{"stale remembered target", "stale remembered target", func(t *testing.T, f *evalFixture) {
			f.opt.ToContext = ""
			path, err := bundleLiftSelectionPath(f.bundle)
			if err != nil {
				t.Fatal(err)
			}
			if err := saveBundleLiftSelection(path, bundleLiftSelection{Context: "kind-test", ClusterUID: "replaced-cluster", Namespace: "orka-system", Inference: "provider:inference"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"unconfirmed remote context", "kube-guard:", func(t *testing.T, f *evalFixture) { t.Setenv("KMX_EVAL_REMOTE", "1") }},
		{"no target", "requires --to-context", func(t *testing.T, f *evalFixture) { f.opt.ToContext = "" }},
		{"unknown case", `no evaluation case with id "nope"`, func(t *testing.T, f *evalFixture) { f.opt.Case = "nope" }},
		{"invalid case", "field surprise not found", func(t *testing.T, f *evalFixture) {
			_ = os.WriteFile(filepath.Join(f.bundle, "eval", "bad.yaml"), []byte(evalHelloCase+"surprise: true\n"), 0600)
		}},
		{"duplicate ids", `repeats case id "greet"`, func(t *testing.T, f *evalFixture) {
			_ = os.WriteFile(filepath.Join(f.bundle, "eval", "c.yaml"), []byte(evalHelloCase), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t, map[string]string{"b.yaml": evalHelloCase})
			tc.setup(t, f)
			err := f.app.EvaluateAgentBundle(f.opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if f.taskCreates(t) != 0 {
				t.Fatal("a Task was created before the refusal")
			}
			if _, err := os.Stat(bundleEvaluationReceiptPath(f.bundle, "kind-test", "orka-system", "cluster-uid")); !os.IsNotExist(err) {
				t.Fatal("a refused evaluation wrote a receipt")
			}
		})
	}
}

// The remembered target is used when --to-context is omitted, provided its
// cluster identity still matches.
func TestEvaluateUsesRememberedTarget(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"b.yaml": evalHelloCase})
	f.answers["Say hello."] = "hello"
	f.opt.ToContext = ""
	path, err := bundleLiftSelectionPath(f.bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveBundleLiftSelection(path, bundleLiftSelection{Context: "kind-test", ClusterUID: "cluster-uid", Namespace: "orka-system", Inference: "provider:inference"}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.EvaluateAgentBundle(f.opt); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateSingleCase(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalSignCase, "b.yaml": evalHelloCase})
	f.answers["Say hello."] = "hello"
	f.opt.Case = "greet"
	if err := f.app.EvaluateAgentBundle(f.opt); err != nil {
		t.Fatal(err)
	}
	receipt, _ := f.receipt(t)
	if len(receipt.Cases) != 1 || receipt.Cases[0].ID != "greet" || f.taskCreates(t) != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	// A subset of the cases is not the bundle's case set.
	if receipt.CasesDigest == currentBundleCasesDigest(f.bundle) {
		t.Fatal("a single-case receipt claims the full case set")
	}
}

func TestEvaluateAdapterRefusesUnboundRequests(t *testing.T) {
	adapter := orkaRuntimeAdapter{app: &App{Cfg: &config.Config{KubeContext: "kind-test"}}}
	ref := agentruntime.AgentRef{Namespace: "orka-system", Name: "sample", UID: "agent-uid"}
	request := agentruntime.EvaluationRequest{CaseID: "c", Input: "hi", ExpectContains: []string{"hi"}}
	if _, err := adapter.Evaluate(t.Context(), ref, request); err == nil || !strings.Contains(err.Error(), "portable digest") {
		t.Fatalf("an evaluation bound to no revision was attempted: %v", err)
	}
	request.PortableDigest = strings.Repeat("a", 64)
	if _, err := adapter.Evaluate(t.Context(), agentruntime.AgentRef{Namespace: "orka-system", Name: "sample"}, request); err == nil {
		t.Fatal("an evaluation of an Agent with no UID was attempted")
	}
	if _, err := adapter.Evaluate(t.Context(), ref, request); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("an unbounded evaluation was attempted: %v", err)
	}
}

// Status reports the recorded result only for the deployed revision when it
// is the current digest and case set, and none otherwise.
func TestBundleStatusShowsEvaluation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		edit       func(*bundleEvaluationReceipt)
		write      bool
	}{
		{"pass", "pass", nil, true},
		{"fail", "fail", func(r *bundleEvaluationReceipt) { r.Result = "fail" }, true},
		{"none without receipt", "none", nil, false},
		{"none for another case set", "none", func(r *bundleEvaluationReceipt) { r.CasesDigest = strings.Repeat("c", 64) }, true},
		{"none for another revision", "none", func(r *bundleEvaluationReceipt) { r.PortableDigest = strings.Repeat("d", 64) }, true},
		{"none for a replaced Agent", "none", func(r *bundleEvaluationReceipt) { r.Target.AgentUID = "old-agent-uid" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir, rendered, name := bundleStatusFixture(t)
			seedBundleLiveResources(t, dir, rendered, name, nil)
			if tc.write {
				receipt := bundleEvaluationReceipt{
					Bundle: name, PortableDigest: rendered.PortableDigest(), CasesDigest: currentBundleCasesDigest(opt.BundleDir), Result: "pass",
					Target: bundleEvaluationTarget{Runtime: agentruntime.Orka, Context: "kind-test", Namespace: "orka-system", Agent: name, AgentUID: "agent-uid"},
				}
				if tc.edit != nil {
					tc.edit(&receipt)
				}
				if err := writeBundleEvaluationReceipt(opt.BundleDir, "cluster-uid", receipt); err != nil {
					t.Fatal(err)
				}
			}
			opt.Context, opt.Namespace = "kind-test", "orka-system"
			report, err := a.bundleStatusReport(opt)
			if err != nil {
				t.Fatal(err)
			}
			target := report.Targets[0]
			if target.State != bundleStateInSync || target.Evaluation != tc.want {
				t.Fatalf("state %q evaluation %q, want in sync and %q", target.State, target.Evaluation, tc.want)
			}
			// An evaluation receipt never counts as a deployment target.
			targets, err := loadBundleReceiptTargets(opt.BundleDir)
			if err != nil || len(targets) != 0 {
				t.Fatalf("evaluation receipt read as a lift receipt: %+v %v", targets, err)
			}
		})
	}
}

func TestCreateScaffoldsAValidExampleCase(t *testing.T) {
	create := goldenNoTaskCreate("")
	source, err := portableOrkaSource(create)
	if err != nil {
		t.Fatal(err)
	}
	bindings := mustBindingsSource(t, create)
	bundle := filepath.Join(t.TempDir(), "agents", "sample")
	if err := writeOrkaBundle(bundle, source, bindings); err != nil {
		t.Fatal(err)
	}
	cases, _, err := loadBundleEvaluationCases(bundle)
	if err != nil || len(cases) != 1 || cases[0].Case.ID != "example" {
		t.Fatalf("scaffolded cases = %+v, %v", cases, err)
	}
	// An identical rerun accepts the eval/ directory, whatever cases an
	// operator has since added, and leaves them alone.
	extra := filepath.Join(bundle, "eval", "mine.yaml")
	if err := os.WriteFile(extra, []byte(evalHelloCase), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeOrkaBundle(bundle, source, bindings); err != nil {
		t.Fatalf("rerun refused a bundle with eval/: %v", err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatal("rerun removed an operator's case")
	}
	// eval/ must be a directory, never a link.
	if err := os.RemoveAll(filepath.Join(bundle, "eval")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(bundle, "eval")); err != nil {
		t.Fatal(err)
	}
	if err := preflightOrkaBundle(bundle, source, bindings); err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Fatalf("preflight accepted a linked eval/: %v", err)
	}
}
