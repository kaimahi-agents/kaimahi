package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/canon"
	"github.com/aramase/agentsessions/wire"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Exercise real app orchestration and transport. The external host is fake; its
// journal has the same execution-start/input/model/output/end shape as chat.
type sessionsEvalServer struct {
	v1.UnimplementedSessionsServer
	mu                sync.Mutex
	creates           []*v1.CreateSessionRequest
	execs             []*v1.ExecRequest
	answers           map[string]string
	model             string
	fail              bool
	mixed, errorAfter bool
}

func (s *sessionsEvalServer) CreateSession(_ context.Context, req *v1.CreateSessionRequest) (*v1.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates = append(s.creates, req)
	return &v1.Session{Metadata: &v1.ResourceMetadata{Uid: fmt.Sprintf("case-session-%d", len(s.creates))}, Harness: "chat", Labels: req.GetSession().GetLabels()}, nil
}

func (s *sessionsEvalServer) Exec(req *v1.ExecRequest, stream grpc.ServerStreamingServer[v1.ExecUpdate]) error {
	s.mu.Lock()
	s.execs = append(s.execs, req)
	s.mu.Unlock()
	if err := stream.Send(&v1.ExecUpdate{Update: &v1.ExecUpdate_Session{Session: &v1.Session{Metadata: &v1.ResourceMetadata{Uid: req.GetSession()}, Harness: "chat"}}}); err != nil {
		return err
	}
	if s.fail {
		return status.Error(codes.Internal, "private-remote-error-canary")
	}
	count := int64(1)
	events := []*v1.Event{
		{Kind: v1.EventKind_EVENT_EXECUTION_START, Body: &v1.Event_ExecutionStart{ExecutionStart: &v1.ExecutionStart{Config: req.GetConfig(), InputCount: &count}}},
		{Kind: v1.EventKind_EVENT_INPUT, Body: &v1.Event_Message{Message: req.GetInputs()[0]}},
		{Kind: v1.EventKind_EVENT_MODEL_CALL, Body: &v1.Event_Model{Model: &v1.ModelCall{Model: s.model, Id: "call-1", InputHash: strings.Repeat("a", 64)}}},
		{Kind: v1.EventKind_EVENT_OUTPUT, Body: &v1.Event_Message{Message: &v1.Message{Role: "assistant", Parts: []*v1.Part{{Part: &v1.Part_Text{Text: &v1.TextPart{Text: s.answers[req.GetInputs()[0].GetParts()[0].GetText().GetText()]}}}}}}},
		{Kind: v1.EventKind_EVENT_END, Body: &v1.Event_End{End: &v1.HarnessEnd{State: "COMPLETED"}}},
	}
	if s.mixed {
		other := &v1.Event{Kind: v1.EventKind_EVENT_MODEL_CALL, Body: &v1.Event_Model{Model: &v1.ModelCall{Model: "other-model", Id: "call-2", InputHash: strings.Repeat("b", 64)}}}
		events = append(events[:3], append([]*v1.Event{other}, events[3:]...)...)
	}
	prev := ""
	for i, ev := range events {
		ev.ExecutionId, ev.SchemaVersion, ev.Ts = req.GetSession()+"-exec", 1, timestamppb.New(time.Unix(100, 0))
		seq := int64(i + 1)
		hash, err := canon.HashRecord(prev, seq, wire.EventFromProto(ev))
		if err != nil {
			return err
		}
		r := &v1.LogRecord{Seq: seq, PrevHash: prev, ContentHash: hash, Fence: 1, Event: ev}
		if err := stream.Send(&v1.ExecUpdate{Update: &v1.ExecUpdate_Record{Record: r}}); err != nil {
			return err
		}
		prev = hash
	}
	if s.errorAfter {
		return status.Error(codes.Unavailable, "private-remote-error-canary")
	}
	return nil
}

func newSessionsAppFixture(t *testing.T, s *sessionsEvalServer) (*App, EvaluateAgentBundleOptions, string) {
	t.Helper()
	t.Setenv("KMX_HOME", t.TempDir())
	bundle := t.TempDir()
	source := "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: sample\nspec:\n  instructions: |\n    private-instructions-canary\n    Preserve this newline.\n  model:\n    name: test-model\n"
	if err := os.WriteFile(filepath.Join(bundle, "agent.yaml"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(bundle, "eval"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"a.yaml": "id: one\ninput: private-input-one\nexpectContains: [private-assertion-canary]\n",
		"b.yaml": "id: two\ninput: private-input-two\nexpectContains: [private-assertion-canary]\n",
	} {
		if err := os.WriteFile(filepath.Join(bundle, "eval", name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, s)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); <-done })
	return &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, EvaluateAgentBundleOptions{BundleDir: bundle, Sessions: lis.Addr().String(), CaseTimeout: 10 * time.Second}, source
}

func readSessionsAppReceipt(t *testing.T, bundle string) (sessionsEvaluationReceipt, []byte) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(bundle, "receipts", "eval-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("receipts %v: %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var receipt sessionsEvaluationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, raw
}

func TestSessionsEvaluationRunsCasesAndRecordsEvidence(t *testing.T) {
	for _, tc := range []struct{ name, second, want string }{
		{"pass", "private-answer-canary private-assertion-canary", "pass"},
		{"fail", "private-answer-canary no match", "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sessionsEvalServer{model: "test-model", answers: map[string]string{"private-input-one": "private-answer-canary private-assertion-canary", "private-input-two": tc.second}}
			a, opt, source := newSessionsAppFixture(t, s)
			// A broken remembered Orka target must not obstruct a sessions run.
			selection, err := bundleLiftSelectionPath(opt.BundleDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(selection), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(selection, []byte("invalid cluster selection"), 0600); err != nil {
				t.Fatal(err)
			}
			err = a.EvaluateAgentBundle(opt)
			if (err == nil) != (tc.want == "pass") {
				t.Fatalf("evaluation error: %v", err)
			}
			r, raw := readSessionsAppReceipt(t, opt.BundleDir)
			if r.Result != tc.want || !r.FullCaseSet || r.PortableDigest != agentruntime.PortableBundleDigest([]byte(source)) || len(r.Cases) != 2 {
				t.Fatalf("receipt: %+v", r)
			}
			_, files, err := loadBundleEvaluationCases(opt.BundleDir)
			if err != nil {
				t.Fatal(err)
			}
			if r.CasesDigest != agentruntime.EvaluationCasesDigest(files) {
				t.Fatal("wrong case set digest")
			}
			if r.Target.Identity.Version != 1 || r.Target.Identity.Provenance != "host-reported" || r.Target.Identity.Harness != "chat" || r.Target.Identity.Model != "test-model" || r.Target.Identity.Address != opt.Sessions {
				t.Fatalf("identity: %+v", r.Target.Identity)
			}
			for _, c := range r.Cases {
				if c.SessionUID == "" || c.JournalHead.Seq != 5 || len(c.JournalHead.Hash) != 64 || len(c.AnswerSHA256) != 64 || c.Model != "test-model" {
					t.Fatalf("missing evidence: %+v", c)
				}
			}
			for _, canary := range []string{"private-instructions-canary", "private-input", "private-answer-canary", "private-assertion-canary", "private-remote-error-canary"} {
				if bytes.Contains(raw, []byte(canary)) {
					t.Errorf("receipt leaked %s", canary)
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.creates) != 2 || len(s.execs) != 2 {
				t.Fatalf("sessions created/executed %d/%d", len(s.creates), len(s.execs))
			}
			if s.execs[0].GetSession() == s.execs[1].GetSession() {
				t.Fatal("cases share session")
			}
			for i, create := range s.creates {
				if create.GetSession().GetHarness() != "chat" || create.GetSession().GetModel() != "" {
					t.Fatal("not using host chat/model")
				}
				if create.GetSession().GetLabels()["kmx.portable-digest"] != r.PortableDigest || create.GetSession().GetLabels()["kmx.cases-digest"] != r.CasesDigest {
					t.Fatal("digest labels absent")
				}
				var config map[string]string
				if err := json.Unmarshal(s.execs[i].GetConfig(), &config); err != nil {
					t.Fatal(err)
				}
				if config["system_prompt"] != "private-instructions-canary\nPreserve this newline.\n" {
					t.Fatalf("normalized prompt: %q", config["system_prompt"])
				}
			}
		})
	}
}

func TestSessionsSingleCaseReceiptCannotSatisfyOrkaGate(t *testing.T) {
	s := &sessionsEvalServer{model: "test-model", answers: map[string]string{"private-input-two": "private-assertion-canary"}}
	a, opt, _ := newSessionsAppFixture(t, s)
	opt.Case = "two"
	if err := a.EvaluateAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	r, _ := readSessionsAppReceipt(t, opt.BundleDir)
	if r.FullCaseSet || len(r.Cases) != 1 || r.Cases[0].ID != "two" {
		t.Fatalf("selection: %+v", r)
	}
	_, files, err := loadBundleEvaluationCases(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if r.CasesDigest != agentruntime.EvaluationCasesDigest(files[1:]) {
		t.Fatal("selection has wrong digest")
	}
	required, condition := evaluateBundleLiftGate(opt.BundleDir, "sample", r.PortableDigest, bundleGateTarget{ClusterUID: "production", Namespace: "orka-system"}, opt.Sessions)
	if !required || !strings.Contains(condition, "no evaluation receipt for context label") {
		t.Fatalf("sessions evidence satisfied Orka gate: %v %s", required, condition)
	}
}

func TestSessionsCredentialShapedAddressNeverEntersReceipt(t *testing.T) {
	s := &sessionsEvalServer{model: "test-model"}
	a, opt, _ := newSessionsAppFixture(t, s)
	var host string
	for _, shape := range secretshapes.All() {
		if shape.Name == "azure-openai-key-hex" {
			host = shape.Example
			break
		}
	}
	if host == "" {
		t.Fatal("credential-shaped hostname fixture missing")
	}
	opt.Sessions = host + ":8080"
	err := a.EvaluateAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "credential-shaped") || strings.Contains(err.Error(), host) {
		t.Fatalf("unsafe address diagnostic: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opt.BundleDir, "receipts")); !os.IsNotExist(err) {
		t.Fatalf("unsafe address recorded: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.creates) != 0 || len(s.execs) != 0 {
		t.Fatal("unsafe address caused host mutation")
	}
}

func TestSessionsMixedModelsAreNotRecordedAsOneModel(t *testing.T) {
	for _, afterError := range []bool{false, true} {
		t.Run(fmt.Sprint(afterError), func(t *testing.T) {
			s := &sessionsEvalServer{model: "test-model", mixed: true, errorAfter: afterError, answers: map[string]string{"private-input-one": "private-assertion-canary", "private-input-two": "private-assertion-canary"}}
			a, opt, _ := newSessionsAppFixture(t, s)
			if err := a.EvaluateAgentBundle(opt); err == nil {
				t.Fatal("mixed models passed")
			}
			r, raw := readSessionsAppReceipt(t, opt.BundleDir)
			if r.Result != "unknown" || r.Target.Identity.Model != "" {
				t.Fatalf("mixed models promoted as common model: %+v", r)
			}
			var fields struct {
				Cases []map[string]any `json:"cases"`
			}
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for i, c := range r.Cases {
				if c.Model != "" || fields.Cases[i]["modelMixed"] != true || c.JournalHead.Seq != 6 {
					t.Fatalf("ambiguity lost: %+v JSON=%v", c, fields.Cases[i])
				}
			}
		})
	}
}

func TestSessionsEvaluationPreflightDoesNotCreateSessions(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, *EvaluateAgentBundleOptions)
	}{
		{"unknown case", "no evaluation case", func(_ *testing.T, o *EvaluateAgentBundleOptions) { o.Case = "absent" }},
		{"short timeout", "--case-timeout", func(_ *testing.T, o *EvaluateAgentBundleOptions) { o.CaseTimeout = time.Second }},
		{"no cases", "no eval/ directory", func(t *testing.T, o *EvaluateAgentBundleOptions) {
			if err := os.RemoveAll(filepath.Join(o.BundleDir, "eval")); err != nil {
				t.Fatal(err)
			}
		}},
		{"linked receipts", "receipts must be a directory", func(t *testing.T, o *EvaluateAgentBundleOptions) {
			if err := os.Symlink(t.TempDir(), filepath.Join(o.BundleDir, "receipts")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sessionsEvalServer{model: "test-model"}
			a, opt, _ := newSessionsAppFixture(t, s)
			tc.change(t, &opt)
			err := a.EvaluateAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.creates) != 0 || len(s.execs) != 0 {
				t.Fatal("preflight failure created sessions")
			}
		})
	}
}

func TestSessionsFailedOrMismatchedModelNeverPasses(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		fail        bool
	}{{"remote error", "test-model", true}, {"model mismatch", "other-model", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sessionsEvalServer{model: tc.model, fail: tc.fail, answers: map[string]string{"private-input-one": "private-assertion-canary", "private-input-two": "private-assertion-canary"}}
			a, opt, _ := newSessionsAppFixture(t, s)
			if err := a.EvaluateAgentBundle(opt); err == nil {
				t.Fatal("unproven result passed")
			}
			r, raw := readSessionsAppReceipt(t, opt.BundleDir)
			if r.Result != "unknown" || len(r.Cases) != 2 {
				t.Fatalf("receipt: %+v", r)
			}
			for _, c := range r.Cases {
				if c.Verdict != "unknown" || c.SessionUID == "" || c.Detail == "" {
					t.Fatalf("failed execution lost evidence: %+v", c)
				}
				if !tc.fail && c.Model != "other-model" {
					t.Fatalf("model not observed: %+v", c)
				}
			}
			if bytes.Contains(raw, []byte("private-remote-error-canary")) {
				t.Fatal("receipt leaked remote payload")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.creates) != 2 || len(s.execs) != 2 {
				t.Fatal("failed mutation was retried")
			}
		})
	}
}
