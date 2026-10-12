package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/controller"
	"github.com/aramase/agentsessions/eventlog"
	"github.com/aramase/agentsessions/harness/chatagent"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const referenceInputCanary = "recorded-private-input-canary"
const referenceAnswerCanary = "candidate-private-answer-canary"

type referenceEvaluationServer struct {
	*sessionsEvalServer
	records        []*v1.LogRecord
	expired        bool
	expiredSession string
}

func (s *referenceEvaluationServer) Replay(r *v1.ReplayRequest, stream v1.Sessions_ReplayServer) error {
	if s.expired || r.Session == s.expiredSession {
		return status.Error(codes.NotFound, "recorded-private-error-canary")
	}
	for _, record := range s.records {
		if record.Seq >= r.FromSeq && record.Seq <= r.ToSeq {
			if err := stream.Send(record); err != nil {
				return err
			}
		}
	}
	return nil
}
func referenceEvaluationFixture(t *testing.T) (*App, EvaluateAgentBundleOptions, *referenceEvaluationServer) {
	t.Helper()
	candidate := &sessionsEvalServer{model: "test-model", answers: map[string]string{referenceInputCanary: referenceAnswerCanary}}
	a, opt, _ := newSessionsAppFixture(t, candidate)
	log := eventlog.New()
	recorder, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", "original-private-answer-canary")}, nil
	}, controller.WithStart([]byte(`{"system_prompt":"original-private-instructions-canary"}`), 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Exec(t.Context(), chatagent.Harness{Model: "test-model"}, []api.Message{*api.TextMessage("user", referenceInputCanary)}, 0); err != nil {
		t.Fatal(err)
	}
	source := &referenceEvaluationServer{sessionsEvalServer: candidate}
	for _, record := range log.Snapshot() {
		source.records = append(source.records, eventlog.RecordToProto(record))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, source)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	opt.SourceSessions = listener.Addr().String()
	client, err := agentsessions.Dial(agentsessions.Options{Address: opt.SourceSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ref, err := client.ExtractSessionReference(ctx, "source-session-1")
	if err != nil {
		t.Fatal(err)
	}
	ref.Store = opt.SourceSessions
	authored := agentruntime.EvaluationCase{ID: "recorded", ExpectContains: []string{"candidate-private-answer-canary"}}
	c, err := agentruntime.NewSessionReferenceCase(authored, ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := agentruntime.MarshalSessionReferenceManifest(agentruntime.SessionReferenceManifest{SchemaVersion: 1, Policy: agentruntime.SessionReferencePolicy, Cases: []agentruntime.SessionReferenceCase{c}})
	if err != nil {
		t.Fatal(err)
	}
	opt.FromSessions = filepath.Join(diffTempDir(t), "references.json")
	if err := os.WriteFile(opt.FromSessions, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return a, opt, source
}

// Catches printing input, output, authored operands, source configuration or arbitrary errors.
func TestReferenceEvaluationPayloadFreeAndFreshSessions(t *testing.T) {
	for _, show := range []bool{false, true} {
		t.Run(map[bool]string{false: "private-default", true: "explicit-content"}[show], func(t *testing.T) {
			a, opt, source := referenceEvaluationFixture(t)
			t.Setenv("KAIMAHI_CONFIRM", opt.Sessions)
			opt.ShowContent = show
			if err := a.EvaluateAgentBundle(opt); err != nil {
				t.Fatal(err)
			}
			first, _ := readSessionsAppReceipt(t, opt.BundleDir)
			if err := a.EvaluateAgentBundle(opt); err != nil {
				t.Fatal(err)
			}
			second, raw := readSessionsAppReceipt(t, opt.BundleDir)
			if first.Cases[0].SessionUID == second.Cases[0].SessionUID || first.RunID == second.RunID || second.Result != "pass" {
				t.Fatalf("not fresh passed evidence: %+v", second)
			}
			source.mu.Lock()
			defer source.mu.Unlock()
			if len(source.execs) != 2 || source.execs[0].GetInputs()[0].GetParts()[0].GetText().GetText() != referenceInputCanary {
				t.Fatal("recorded input not used exactly once per fresh execution")
			}
			output := a.Out.(*bytes.Buffer).String() + a.Err.(*bytes.Buffer).String()
			if show && !strings.Contains(output, referenceAnswerCanary) {
				t.Fatal("explicit content absent")
			}
			for _, canary := range []string{referenceInputCanary, referenceAnswerCanary, "original-private-answer-canary", "original-private-instructions-canary"} {
				if bytes.Contains(raw, []byte(canary)) || (!show && strings.Contains(output, canary)) {
					t.Fatal("reference evaluation leaked content")
				}
			}
			if !strings.Contains(output, "pass") || !strings.Contains(output, second.Cases[0].SessionUID) {
				t.Fatal("missing payload-free outcome/identity")
			}
		})
	}
}

type referenceReportFailureWriter struct{ marker string }

func (w referenceReportFailureWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.marker) {
		return 0, errors.New("private-output-error-canary")
	}
	return len(p), nil
}

// Successful execution must not turn failed report delivery into command success.
func TestReferenceEvaluationReportFailureReturnsContentFreeError(t *testing.T) {
	for _, marker := range []string{"case ", ": pass (", "\nevaluation "} {
		t.Run(strings.TrimSpace(marker), func(t *testing.T) {
			a, opt, _ := referenceEvaluationFixture(t)
			t.Setenv("KAIMAHI_CONFIRM", opt.Sessions)
			a.Out = referenceReportFailureWriter{marker: marker}
			err := a.EvaluateAgentBundle(opt)
			if err == nil || strings.Contains(err.Error(), "private-output-error-canary") {
				t.Fatalf("report failure mishandled: %v", err)
			}
			receipt, _ := readSessionsAppReceipt(t, opt.BundleDir)
			if receipt.Result != "pass" {
				t.Fatal("report failure discarded valid execution evidence")
			}
		})
	}
}

// Catches endpoint selection being mistaken for permission to send recorded input.
func TestReferenceEvaluationRequiresNamedConsentBeforeExecution(t *testing.T) {
	a, opt, s := referenceEvaluationFixture(t)
	t.Setenv("KAIMAHI_CONFIRM", "wrong-destination")
	err := a.EvaluateAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "recorded inputs will be sent to "+opt.Sessions) {
		t.Fatalf("missing transfer refusal: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.creates) != 0 || len(s.execs) != 0 {
		t.Fatal("inference before consent")
	}
}

// Catches silently skipping expiry or inventing a candidate identity to make diff comparable.
func TestReferenceEvaluationExpiredIsUnknownAndDiffIncompatible(t *testing.T) {
	a, opt, s := referenceEvaluationFixture(t)
	t.Setenv("KAIMAHI_CONFIRM", opt.Sessions)
	if err := a.EvaluateAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	before, _ := readSessionsAppReceipt(t, opt.BundleDir)
	s.expired = true
	if err := a.EvaluateAgentBundle(opt); err == nil {
		t.Fatal("expired source passed")
	}
	after, raw := readSessionsAppReceipt(t, opt.BundleDir)
	if len(after.Cases) != 1 || after.Cases[0].Verdict != "unknown" || after.Cases[0].Model != "" || after.Cases[0].SessionUID != "" {
		t.Fatalf("expiry not unavailable: %+v", after)
	}
	b, ok := normalizeSessionsEvaluation(before)
	if !ok {
		t.Fatal("bad before receipt")
	}
	e, ok := normalizeSessionsEvaluation(after)
	if !ok {
		t.Fatal("bad expired receipt")
	}
	diff := compareEvaluations(b, e)
	if diff.IncompatibleReason != "runtime identity unavailable" {
		t.Fatalf("unexpected comparison: %+v", diff)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.creates) != 1 {
		t.Fatal("expired reference executed")
	}
	output := a.Out.(*bytes.Buffer).String() + a.Err.(*bytes.Buffer).String() + string(raw)
	for _, canary := range []string{referenceInputCanary, referenceAnswerCanary, "recorded-private-error-canary"} {
		if strings.Contains(output, canary) {
			t.Fatal("expiry leaked content")
		}
	}
}

// One expired source must not hide another case's real candidate evidence.
func TestReferenceEvaluationExpiredCaseDiffIsRowLocal(t *testing.T) {
	a, opt, s := referenceEvaluationFixture(t)
	t.Setenv("KAIMAHI_CONFIRM", opt.Sessions)
	raw, err := os.ReadFile(opt.FromSessions)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := agentruntime.ParseSessionReferenceManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	ref := manifest.Cases[0].Source
	ref.SessionUID = "source-session-2"
	second, err := agentruntime.NewSessionReferenceCase(agentruntime.EvaluationCase{ID: "remaining", ExpectContains: []string{referenceAnswerCanary}}, ref)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Cases = append(manifest.Cases, second)
	raw, err = agentruntime.MarshalSessionReferenceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opt.FromSessions, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.EvaluateAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	before, _ := readSessionsAppReceipt(t, opt.BundleDir)
	s.expiredSession = "source-session-1"
	if err := a.EvaluateAgentBundle(opt); err == nil {
		t.Fatal("expired source passed")
	}
	after, raw := readSessionsAppReceipt(t, opt.BundleDir)
	if len(after.Cases) != 2 || after.Cases[0].Verdict != "unknown" || after.Cases[0].SessionUID != "" || after.Cases[0].Model != "" || after.Cases[1].Verdict != "pass" || after.Cases[1].SessionUID == "" {
		t.Fatalf("invalid mixed expiry evidence: %+v", after)
	}
	out, err := diffExecute(t, before, after)
	if err == nil || err.Error() != "evaluation comparison is incomplete" || !strings.Contains(out, manifest.Cases[0].ID+"\t-\t-/-\tpass/unknown\tunknown\t") || !strings.Contains(out, second.ID+"\texpectContains.0\tcontains/contains\tpass/pass\tstable\t") || strings.Contains(out, "incompatible") {
		t.Fatalf("err=%v out=%s", err, out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.creates) != 3 || len(s.execs) != 3 {
		t.Fatal("expired reference executed or remaining reference skipped")
	}
	output := out + a.Out.(*bytes.Buffer).String() + a.Err.(*bytes.Buffer).String() + string(raw)
	for _, canary := range []string{referenceInputCanary, referenceAnswerCanary, "recorded-private-error-canary"} {
		if strings.Contains(output, canary) {
			t.Fatal("mixed expiry leaked content")
		}
	}
}

// Catches a configured judge being called without separate permission, or leaking reasoning.
func TestReferenceEvaluationJudgeConsentAndPayloadFreeReasoning(t *testing.T) {
	a, opt, s := referenceEvaluationFixture(t)
	raw, err := os.ReadFile(opt.FromSessions)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := agentruntime.ParseSessionReferenceManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	authored := judgeCase()
	authored.Input = ""
	authored.ID = "recorded-judge"
	c, err := agentruntime.NewSessionReferenceCase(authored, manifest.Cases[0].Source)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Cases = []agentruntime.SessionReferenceCase{c}
	raw, err = agentruntime.MarshalSessionReferenceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opt.FromSessions, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(json.RawMessage(judgeEnvelope(`{"score":100,"reasoning":"judge-private-reasoning-canary"}`)))
	}))
	defer judge.Close()
	opt.JudgeBaseURL = judge.URL + "/v1"
	t.Setenv("KAIMAHI_CONFIRM", opt.Sessions)
	t.Setenv("KAIMAHI_CONFIRM_JUDGE", "")
	err = a.EvaluateAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "will be sent to "+opt.JudgeBaseURL) {
		t.Fatalf("missing judge transfer refusal: %v", err)
	}
	s.mu.Lock()
	n := len(s.creates)
	s.mu.Unlock()
	if n != 0 || calls.Load() != 0 {
		t.Fatal("agent/judge invoked before judge consent")
	}
	t.Setenv("KAIMAHI_CONFIRM_JUDGE", opt.JudgeBaseURL)
	if err := a.EvaluateAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	_, receipt := readSessionsAppReceipt(t, opt.BundleDir)
	if calls.Load() != 2 {
		t.Fatal("missing two-sample judge evaluation")
	}
	for _, canary := range []string{referenceInputCanary, referenceAnswerCanary, "judge-private-reasoning-canary"} {
		if strings.Contains(a.Out.(*bytes.Buffer).String()+a.Err.(*bytes.Buffer).String()+string(receipt), canary) {
			t.Fatal("judge payload leaked")
		}
	}
}

// Catches confirmation diagnostics revealing a credential embedded in a URL path.
func TestReferenceEvaluationConsentDoesNotExposeJudgeCredential(t *testing.T) {
	a, opt, s := referenceEvaluationFixture(t)
	t.Setenv("KAIMAHI_CONFIRM", opt.Sessions)
	t.Setenv("REFERENCE_JUDGE_KEY", "judge-credential-canary")
	opt.JudgeKeyEnv = "REFERENCE_JUDGE_KEY"
	opt.JudgeBaseURL = "https://judge.example.com/%6audge-credential-canary/v1"
	err := a.EvaluateAgentBundle(opt)
	if err == nil || strings.Contains(err.Error(), "credential-canary") {
		t.Fatalf("credential exposed: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.creates) != 0 {
		t.Fatal("invalid judge created candidate")
	}
}

func TestReferenceEvaluationRefusesWrongSourceAndChangedAssertions(t *testing.T) {
	for _, mode := range []string{"wrong-source", "changed-assertions"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, s := referenceEvaluationFixture(t)
			t.Setenv("KAIMAHI_CONFIRM", opt.Sessions)
			if mode == "wrong-source" {
				opt.SourceSessions = "127.0.0.1:1"
			} else {
				raw, _ := os.ReadFile(opt.FromSessions)
				var value map[string]any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				c := value["cases"].([]any)[0].(map[string]any)
				c["expectContains"] = []string{"altered-private-assertion-canary"}
				raw, _ = json.Marshal(value)
				if err := os.WriteFile(opt.FromSessions, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := a.EvaluateAgentBundle(opt)
			if err == nil {
				t.Fatal("incompatible source/assertions executed")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.creates) != 0 {
				t.Fatal("refusal created session")
			}
			if strings.Contains(err.Error(), "private-") || strings.Contains(a.Out.(*bytes.Buffer).String(), "private-") {
				t.Fatal("refusal leaked content")
			}
		})
	}
}
