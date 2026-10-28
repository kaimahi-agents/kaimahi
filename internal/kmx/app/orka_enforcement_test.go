package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func testTaskEnforcement() *orkaTaskEnforcement {
	return &orkaTaskEnforcement{
		Format: "example.policy/v1", Policy: []byte(" full policy \x00\xff\n"),
		RequiredCapabilities: []string{"filesystem/deny-write/v1"}, Mode: "enforce",
	}
}

func writeTaskEnforcement(t *testing.T, requirement *orkaTaskEnforcement) string {
	t.Helper()
	raw, err := json.Marshal(requirement)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "requirement.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOrkaTaskEnforcementFile(t *testing.T) {
	expected := testTaskEnforcement()
	expected.RequiredCapabilities = []string{"network/egress/v1", "filesystem/deny-write/v1"}
	path := writeTaskEnforcement(t, expected)
	got, err := readOrkaTaskEnforcement(t.Context(), path)
	if err != nil || !bytes.Equal(got.Policy, expected.Policy) || got.Format != expected.Format ||
		!slices.Equal(got.RequiredCapabilities, expected.RequiredCapabilities) {
		t.Fatalf("opaque requirement changed: %v", err)
	}
	for _, raw := range []string{
		`null`, `{}`, `{"format":"x","policy":"not base64"}`,
		`{"format":"x","policy":"eA==","requiredCapabilities":["c"],"mode":"audit"}`,
		`{"format":"x","policy":"eA==","requiredCapabilities":["c","c"],"mode":"enforce"}`,
		`{"format":"x","policy":"eA==","requiredCapabilities":["c"],"mode":"enforce","extra":true}`,
		`{"format":"x","policy":"eA==","requiredCapabilities":["c"],"mode":"enforce"} {}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readOrkaTaskEnforcement(t.Context(), path); err == nil {
			t.Fatal("accepted invalid requirement")
		}
	}
	oversized := testTaskEnforcement()
	oversized.Policy = make([]byte, (512<<10)+1)
	if _, err := readOrkaTaskEnforcement(t.Context(), writeTaskEnforcement(t, oversized)); err == nil {
		t.Fatal("accepted oversized decoded policy")
	}
}

func TestOrkaTaskEnforcementReadbackPreservesCapabilityOrder(t *testing.T) {
	expected := testTaskEnforcement()
	expected.RequiredCapabilities = []string{"network/egress/v1", "filesystem/deny-write/v1"}
	task := map[string]any{"spec": map[string]any{"enforcement": expected}}
	original, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if !orkaTaskEnforcementMatches(original, expected) {
		t.Fatal("unchanged complete requirement did not match")
	}
	reordered := *expected
	reordered.RequiredCapabilities = slices.Clone(expected.RequiredCapabilities)
	slices.Reverse(reordered.RequiredCapabilities)
	task["spec"].(map[string]any)["enforcement"] = &reordered
	changed, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if orkaTaskEnforcementMatches(changed, expected) {
		t.Fatal("accepted changed capability order as the same requirement")
	}
}

func TestOrkaTaskEnforcementRunAndChat(t *testing.T) {
	for _, mode := range []string{"run", "chat"} {
		for _, protocol := range []string{"", "container-v1", "unknown-v2"} {
			t.Run(mode+"/"+protocol, func(t *testing.T) {
				f := runFixture(t)
				f.resultEnforcementProtocol = protocol
				f.answers["private prompt"] = "protected answer"
				requirement := testTaskEnforcement()
				requirement.RequiredCapabilities = []string{"network/egress/v1", "filesystem/deny-write/v1"}
				path := writeTaskEnforcement(t, requirement)
				var err error
				if mode == "run" {
					opt := runOption(f)
					opt.EnforcementFile = path
					err = f.app.RunAgent(opt)
				} else {
					t.Setenv("KMX_EVAL_FORWARD_TARGET", f.resultAddress)
					f.app.taskEnforcement, err = readOrkaTaskEnforcement(t.Context(), path)
					if err == nil {
						_, err = f.app.runQuickstartOrkaTaskProfile(t.Context(), f.name, "orka-system", "private prompt", nil, nil)
					}
				}
				if protocol != "container-v1" {
					if err == nil || f.taskCreates(t) != 0 {
						t.Fatalf("unsupported runtime created work: err=%v creates=%d", err, f.taskCreates(t))
					}
					return
				}
				if err != nil || f.taskCreates(t) != 1 {
					t.Fatalf("protected execution: err=%v creates=%d", err, f.taskCreates(t))
				}
				for _, call := range orkaCalls(t, f.dir) {
					if call.Document == nil || call.Document["kind"] != "Task" {
						continue
					}
					raw, err := json.Marshal(call.Document)
					if err != nil || !orkaTaskEnforcementMatches(raw, requirement) {
						t.Fatal("complete requirement was not carried on Task")
					}
				}
			})
		}
	}
}

func TestOrkaTaskEnforcementRefusesAdmissionLoss(t *testing.T) {
	for _, stage := range []string{"preflight", "create"} {
		t.Run(stage, func(t *testing.T) {
			f := runFixture(t)
			f.resultEnforcementProtocol = "container-v1"
			f.app.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
				if !slices.Contains(cmd.Args, "create") || !slices.Contains(cmd.Args, "-f") ||
					(slices.Contains(cmd.Args, "--dry-run=server") != (stage == "preflight")) {
					return cmd.Run()
				}
				out := cmd.Stdout
				var response bytes.Buffer
				cmd.Stdout = &response
				if err := cmd.Run(); err != nil {
					return err
				}
				var doc map[string]any
				if err := json.Unmarshal(response.Bytes(), &doc); err != nil {
					return err
				}
				delete(doc["spec"].(map[string]any), "enforcement")
				return json.NewEncoder(out).Encode(doc)
			}
			opt := runOption(f)
			opt.EnforcementFile = writeTaskEnforcement(t, testTaskEnforcement())
			if err := f.app.RunAgent(opt); err == nil {
				t.Fatal("accepted dropped enforcement")
			}
			expected := 0
			if stage == "create" {
				expected = 1
			}
			if f.taskCreates(t) != expected || f.out.Len() != 0 {
				t.Fatalf("unexpected retry or answer after admission loss: creates=%d", f.taskCreates(t))
			}
		})
	}
}

func TestOrkaTaskEnforcementRefusesAdmissionReordering(t *testing.T) {
	for _, stage := range []string{"preflight", "create"} {
		t.Run(stage, func(t *testing.T) {
			f := runFixture(t)
			f.resultEnforcementProtocol = "container-v1"
			f.answers["private prompt"] = "protected answer"
			f.app.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
				if !slices.Contains(cmd.Args, "create") || !slices.Contains(cmd.Args, "-f") ||
					(slices.Contains(cmd.Args, "--dry-run=server") != (stage == "preflight")) {
					return cmd.Run()
				}
				output := cmd.Stdout
				var response bytes.Buffer
				cmd.Stdout = &response
				if err := cmd.Run(); err != nil {
					return err
				}
				var document map[string]any
				if err := json.Unmarshal(response.Bytes(), &document); err != nil {
					return err
				}
				requirement := document["spec"].(map[string]any)["enforcement"].(map[string]any)
				capabilities := requirement["requiredCapabilities"].([]any)
				slices.Reverse(capabilities)
				return json.NewEncoder(output).Encode(document)
			}
			requirement := testTaskEnforcement()
			requirement.RequiredCapabilities = []string{"network/egress/v1", "filesystem/deny-write/v1"}
			opt := runOption(f)
			opt.EnforcementFile = writeTaskEnforcement(t, requirement)
			if err := f.app.RunAgent(opt); err == nil {
				t.Fatal("accepted an admission response that changed requirement identity")
			}
			expected := 0
			if stage == "create" {
				expected = 1
			}
			if f.taskCreates(t) != expected || f.out.Len() != 0 {
				t.Fatalf("unexpected retry or answer after requirement changed: creates=%d", f.taskCreates(t))
			}
		})
	}
}

func TestOrkaChatFreezesTaskEnforcementInput(t *testing.T) {
	f := runFixture(t)
	f.resultEnforcementProtocol = "container-v1"
	f.answers["private prompt"] = "protected answer"
	t.Setenv("KMX_EVAL_FORWARD_TARGET", f.resultAddress)
	f.app.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "api-resources") {
			_, err := io.WriteString(cmd.Stdout, "agents.core.orka.ai\n")
			return err
		}

		return cmd.Run()
	}
	in, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := in.WriteString("/exit\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	f.app.Stdin = in
	path := writeTaskEnforcement(t, testTaskEnforcement())
	if err := f.app.ChatWithOptions(ChatOptions{Agent: f.name, Namespace: "orka-system", Task: "private prompt", EnforcementFile: path}); err != nil {
		t.Fatal(err)
	}
	if f.app.taskEnforcement != nil || f.taskCreates(t) != 1 {
		t.Fatal("chat leaked enforcement selection to a later invocation")
	}
}

func TestConsoleChatCarriesFrozenTaskEnforcement(t *testing.T) {
	f := runFixture(t)
	f.resultEnforcementProtocol = "container-v1"
	f.answers["private prompt"] = "protected answer"
	t.Setenv("KMX_EVAL_FORWARD_TARGET", f.resultAddress)
	t.Setenv("KMX_HOME", t.TempDir())
	in, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := in.WriteString("private prompt\n/exit\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	f.app.Stdin = in
	expected := testTaskEnforcement()
	expected.RequiredCapabilities = []string{"network/egress/v1", "filesystem/deny-write/v1"}
	path := writeTaskEnforcement(t, expected)
	requirement, err := readOrkaTaskEnforcement(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.runAgentTUIAction(agentTUIAction{
		kind: "chat", source: agentTUIEnvironment{Name: f.app.Cfg.KubeContext, Local: true},
		agent: agentTUIAgent{Name: f.name, Namespace: "orka-system"},
	}, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if f.app.taskEnforcement != nil || f.taskCreates(t) != 1 {
		t.Fatal("console lost the requirement or leaked its selection to the caller")
	}
	for _, call := range orkaCalls(t, f.dir) {
		if call.Document == nil || call.Document["kind"] != "Task" {
			continue
		}
		raw, err := json.Marshal(call.Document)
		if err != nil || !orkaTaskEnforcementMatches(raw, expected) {
			t.Fatal("console changed complete requirement bytes or capability order")
		}
	}
}

func TestConsoleRejectsEnforcementBeforeDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(path, []byte(`{"format":"incomplete"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, opt := range []AgentTUIOptions{
		{EnforcementFile: path},
		{EnforcementFile: path, Demo: true},
	} {
		f := newOrkaFixture(t, nil)
		if err := f.app.AgentTUI(opt); err == nil {
			t.Fatal("accepted an invalid console enforcement selection")
		}
		if calls := f.calls(t); calls != "" {
			t.Fatalf("invalid selection reached cluster discovery: %s", calls)
		}
	}
}

func TestProtectedChatOwnsEnforcementRendering(t *testing.T) {
	f := runFixture(t)
	f.resultEnforcementProtocol = "container-v1"
	f.answers["private prompt"] = "protected answer"
	t.Setenv("KMX_EVAL_FORWARD_TARGET", f.resultAddress)
	requirement := testTaskEnforcement()
	f.app.taskEnforcement = requirement
	var diagnostics bytes.Buffer
	f.app.Err = &diagnostics
	backend := &orkaChatBackend{app: f.app, agent: f.name, namespace: "orka-system"}
	defer func() {
		if backend.resultSession != nil {
			backend.resultSession.close()
		}
	}()
	var events []chatTimelineEvent
	renderer := newChatRenderer(io.Discard)
	renderer.timeline = func(event chatTimelineEvent) { events = append(events, event) }
	if err := backend.Send(t.Context(), "private prompt", renderer); err != nil {
		t.Fatal(err)
	}
	notices := make(map[string]bool)
	answerSeen := false
	for _, event := range events {
		if event.kind == "agent-operation" {
			if event.agent != f.name || event.text == "" || notices[event.text] {
				t.Fatal("missing, duplicated, or misattributed enforcement observation")
			}
			notices[event.text] = true
		}
		if event.kind == "assistant" && event.text == "protected answer" {
			answerSeen = true
		}
	}
	if len(notices) == 0 || !answerSeen || diagnostics.Len() != 0 {
		t.Fatalf("chat bypassed its renderer: notices=%d answer=%t stderr=%q", len(notices), answerSeen, diagnostics.String())
	}
	if f.app.taskEnforcementNote != nil || f.app.taskEnforcement != requirement {
		t.Fatal("per-turn rendering state leaked to the shared App")
	}
}
