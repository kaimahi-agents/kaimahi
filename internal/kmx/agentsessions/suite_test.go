package agentsessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func suiteRequest() SuiteCaseRequest {
	return SuiteCaseRequest{
		SuiteDigest: "sha256:" + strings.Repeat("a", 64), Member: "greeter",
		CaseDigest:   strings.Repeat("b", 64),
		Instructions: caseRequest().Instructions, Model: "host-model", Input: "exact input\n",
	}
}

// Use the actual controller's request fingerprint, invocation marker and model
// output rather than inventing a journal which could never pass reference replay.
func suiteServer(t *testing.T) *sessionsServer {
	t.Helper()
	return suiteServerForRequest(t, suiteRequest())
}

func suiteServerForRequest(t *testing.T, r SuiteCaseRequest) *sessionsServer {
	t.Helper()
	config, err := json.Marshal(struct {
		SystemPrompt string `json:"system_prompt"`
	}{r.Instructions})
	if err != nil {
		t.Fatal(err)
	}
	log := eventlog.New()
	c, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", replayAnswer)}, nil
	}, controller.WithStart(config, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(t.Context(), chatagent.Harness{Model: r.Model}, []api.Message{*api.TextMessage("user", r.Input)}, 0); err != nil {
		t.Fatal(err)
	}
	s := &sessionsServer{created: newSession(), updates: []*v1.ExecUpdate{sessionFrame(newSession())}}
	for _, rec := range log.Snapshot() {
		s.updates = append(s.updates, &v1.ExecUpdate{Update: &v1.ExecUpdate_Record{Record: eventlog.RecordToProto(rec)}})
	}
	return s
}

func runSuite(t *testing.T, s *sessionsServer, request SuiteCaseRequest) (CaseResult, error) {
	t.Helper()
	c, err := Dial(Options{Address: serve(t, s, nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	return c.RunSuiteCase(ctx, request)
}

// Rehash semantic mutations so these tests exercise invocation validation and
// reference reconstruction, not just the already-covered hash-chain check.
func rehashSuite(t *testing.T, s *sessionsServer) {
	t.Helper()
	prev := ""
	for _, u := range s.updates[1:] {
		rec := u.GetRecord()
		if rec == nil {
			continue
		}
		rec.PrevHash = prev
		hash, err := canon.HashRecord(prev, rec.Seq, wire.EventFromProto(rec.Event))
		if err != nil {
			t.Fatal(err)
		}
		rec.ContentHash, prev = hash, hash
	}
}

func TestSuiteCaseExactConfigLabelsAndProof(t *testing.T) {
	s := suiteServer(t)
	r := suiteRequest()
	got, err := runSuite(t, s, r)
	if err != nil {
		t.Fatal(err)
	}
	last := s.updates[5].GetRecord()
	config := []byte(`{"system_prompt":"  exact\n\"instructions\" \u003c\u0026\u003e 世界\n"}`)
	digest := sha256.Sum256(append([]byte("kmx.agentsessions.chat-config/v1\x00"), config...))
	wantDigest := "sha256:" + hex.EncodeToString(digest[:])
	if got.SessionUID != "session-1" || got.ExecutionID != last.Event.ExecutionId || got.HarnessVersion != "1" || got.ConfigDigest != wantDigest || got.Model != r.Model || got.Output != replayAnswer || got.Head != (JournalHead{Seq: 5, Hash: last.ContentHash}) {
		t.Fatalf("incomplete suite proof: %#v", got)
	}
	if !reflect.DeepEqual(s.createRequest.Session.Labels, map[string]string{"kmx.suite-digest": r.SuiteDigest, "kmx.member": r.Member, "kmx.case-digest": r.CaseDigest}) {
		t.Fatalf("suite labels: %#v", s.createRequest.Session.Labels)
	}
	exec := s.execRequest
	if exec.Session != got.SessionUID || exec.Harness != "chat" || exec.ExpectedLastSeq == nil || *exec.ExpectedLastSeq != 0 || exec.ResumeFromSeq != 0 || exec.DeadlineUnix == 0 || string(exec.Config) != string(config) || len(exec.Inputs) != 1 || !proto.Equal(exec.Inputs[0], wire.MessageToProto(api.TextMessage("user", r.Input))) {
		t.Fatalf("wrong invocation: %#v", exec)
	}
	if creates, execs := s.counts(); creates != 1 || execs != 1 {
		t.Fatalf("calls = %d/%d", creates, execs)
	}
}

func TestSuiteCaseRejectsSelfConsistentDifferentInvocation(t *testing.T) {
	for _, difference := range []string{"instructions", "input", "both"} {
		t.Run(difference, func(t *testing.T) {
			requested := suiteRequest()
			observed := requested
			if difference != "input" {
				observed.Instructions = "Different instructions."
			}
			if difference != "instructions" {
				observed.Input = "Different input."
			}
			s := suiteServerForRequest(t, observed)
			var records []eventlog.Record
			for _, update := range s.updates[1:] {
				records = append(records, eventlog.RecordFromProto(update.GetRecord()))
			}
			// The actual controller supplies a fully coherent alternative chain and
			// model-request fingerprint, not merely a rehashed semantic mutation.
			proof, err := referenceChatProof(t.Context(), records, observed.Model, verifySHA256([]byte(replayAnswer)))
			if err != nil || proof.Status != "equivalent" {
				t.Fatalf("alternative invocation is not self-consistent: %v, %+v", err, proof)
			}
			got, err := runSuite(t, s, requested)
			assertSafeError(t, err, codes.DataLoss)
			if got.ConfigDigest != "" || got.ExecutionID != "" || got.HarnessVersion != "" {
				t.Fatal("different invocation received passing authority")
			}
		})
	}
}

func TestConfigDigestExactEncodingAndInvalidUTF8(t *testing.T) {
	for _, instructions := range []string{"", " exact\n<&> 世界", " exact\n<&> 世界\n"} {
		config, _ := json.Marshal(struct {
			SystemPrompt string `json:"system_prompt"`
		}{instructions})
		sum := sha256.Sum256(append([]byte("kmx.agentsessions.chat-config/v1\x00"), config...))
		got, err := ConfigDigest(instructions)
		if err != nil || got != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Fatalf("digest = %q, %v", got, err)
		}
	}
	if got, err := ConfigDigest(string([]byte{0xff})); got != "" || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid UTF-8 = %q, %v", got, err)
	}
}

func TestSuiteCaseRejectsSemanticAndWireMutations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sessionsServer)
		code   codes.Code
	}{
		{"harness", func(s *sessionsServer) { s.updates[1].GetRecord().Event.GetExecutionStart().Harness = "echo" }, codes.DataLoss},
		{"version", func(s *sessionsServer) { s.updates[1].GetRecord().Event.GetExecutionStart().HarnessVersion = "2" }, codes.DataLoss},
		{"unknown version", func(s *sessionsServer) { s.updates[1].GetRecord().Event.GetExecutionStart().HarnessVersion = "" }, codes.DataLoss},
		{"config", func(s *sessionsServer) { s.updates[1].GetRecord().Event.GetExecutionStart().Config = []byte(`{}`) }, codes.DataLoss},
		{"input", func(s *sessionsServer) { s.updates[2].GetRecord().Event.GetMessage().Parts[0].GetText().Text = "other" }, codes.DataLoss},
		{"count", func(s *sessionsServer) {
			n := int64(2)
			s.updates[1].GetRecord().Event.GetExecutionStart().InputCount = &n
		}, codes.DataLoss},
		{"cursor", func(s *sessionsServer) { s.updates[1].GetRecord().Event.GetExecutionStart().ResumeFromSeq = 1 }, codes.DataLoss},
		{"model", func(s *sessionsServer) { s.updates[3].GetRecord().Event.GetModel().Model = "other" }, codes.FailedPrecondition},
		{"call ID", func(s *sessionsServer) { s.updates[3].GetRecord().Event.GetModel().Id = "" }, codes.DataLoss},
		{"unsafe call ID", func(s *sessionsServer) { s.updates[3].GetRecord().Event.GetModel().Id = "call\nprivate" }, codes.DataLoss},
		{"fingerprint semantics", func(s *sessionsServer) { s.updates[3].GetRecord().Event.GetModel().InputHash = strings.Repeat("a", 64) }, codes.DataLoss},
		{"fingerprint format", func(s *sessionsServer) { s.updates[3].GetRecord().Event.GetModel().InputHash = "bad" }, codes.DataLoss},
		{"output role", func(s *sessionsServer) { s.updates[4].GetRecord().Event.GetMessage().Role = "user" }, codes.DataLoss},
		{"nontext output", func(s *sessionsServer) {
			s.updates[4].GetRecord().Event.GetMessage().Parts[0] = wire.MessageToProto(&api.Message{Role: "assistant", Parts: []api.Part{{Data: map[string]any{"x": "private"}}}}).Parts[0]
		}, codes.DataLoss},
		{"tool", func(s *sessionsServer) {
			s.updates[3].GetRecord().Event = wire.EventToProto(api.Event{ExecutionID: s.updates[1].GetRecord().Event.ExecutionId, Kind: api.EventToolCall, ToolCall: &api.ToolCall{ID: "call", Tool: "private"}})
		}, codes.DataLoss},
		{"unsafe execution ID", func(s *sessionsServer) {
			for _, u := range s.updates[1:] {
				u.GetRecord().Event.ExecutionId = "execution\nprivate"
			}
		}, codes.DataLoss},
		{"changed execution ID", func(s *sessionsServer) { s.updates[4].GetRecord().Event.ExecutionId = "other" }, codes.DataLoss},
		{"failure", func(s *sessionsServer) { s.updates[5].GetRecord().Event.GetEnd().State = "FAILED" }, codes.FailedPrecondition},
		{"missing END", func(s *sessionsServer) { s.updates = s.updates[:5] }, codes.FailedPrecondition},
		{"extra record", func(s *sessionsServer) {
			extra := proto.Clone(s.updates[5]).(*v1.ExecUpdate)
			extra.GetRecord().Seq++
			s.updates = append(s.updates, extra)
		}, codes.DataLoss},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := suiteServer(t)
			tc.change(s)
			rehashSuite(t, s)
			got, err := runSuite(t, s, suiteRequest())
			assertSafeError(t, err, tc.code)
			if got.ExecutionID != "" || got.HarnessVersion != "" || got.ConfigDigest != "" {
				t.Fatalf("invalid evidence published suite proof: %#v", got)
			}
			if creates, execs := s.counts(); creates != 1 || execs != 1 {
				t.Fatalf("resent: %d/%d", creates, execs)
			}
		})
	}
}

// Floods never end naturally: exhausting either budget must cancel the RPC,
// including when earlier frames were invalid or contain only ephemeral deltas.
type suiteFloodServer struct {
	*sessionsServer
	frame   *v1.ExecUpdate
	stopped chan struct{}
}

func (s *suiteFloodServer) Exec(_ *v1.ExecRequest, stream v1.Sessions_ExecServer) error {
	defer close(s.stopped)
	if err := stream.Send(sessionFrame(newSession())); err != nil {
		return err
	}
	for {
		if err := stream.Send(s.frame); err != nil {
			return err
		}
	}
}

func TestSuiteCaseTotalBudgetsCancelInsteadOfDraining(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame *v1.ExecUpdate
	}{
		{"invalid frames", &v1.ExecUpdate{}},
		{"deltas", &v1.ExecUpdate{Update: &v1.ExecUpdate_Delta{Delta: &v1.Delta{ExecutionId: "turn", Chunk: "chunk"}}}},
		{"bytes", &v1.ExecUpdate{Update: &v1.ExecUpdate_Delta{Delta: &v1.Delta{ExecutionId: "turn", Chunk: strings.Repeat("x", 1<<20)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &suiteFloodServer{sessionsServer: &sessionsServer{created: newSession()}, frame: tc.frame, stopped: make(chan struct{})}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			v1.RegisterSessionsServer(server, s)
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			c, err := Dial(Options{Address: listener.Addr().String()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = c.RunSuiteCase(ctx, suiteRequest())
			assertSafeError(t, err, codes.ResourceExhausted)
			select {
			case <-s.stopped:
			case <-time.After(time.Second):
				t.Fatal("budget refusal did not cancel the stream")
			}
		})
	}
}

func TestSuiteCaseInvalidRequestsHaveNoMutations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SuiteCaseRequest)
	}{
		{"suite", func(r *SuiteCaseRequest) { r.SuiteDigest = "bad" }},
		{"case", func(r *SuiteCaseRequest) { r.CaseDigest = "sha256:" + strings.Repeat("A", 64) }},
		{"member", func(r *SuiteCaseRequest) { r.Member = "private\nmember" }},
		{"model", func(r *SuiteCaseRequest) { r.Model = "" }},
		{"instructions", func(r *SuiteCaseRequest) { r.Instructions = string([]byte{0xff}) }},
		{"input", func(r *SuiteCaseRequest) { r.Input = string([]byte{0xff}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := suiteServer(t)
			r := suiteRequest()
			tc.change(&r)
			_, err := runSuite(t, s, r)
			assertSafeError(t, err, codes.InvalidArgument)
			if creates, execs := s.counts(); creates != 0 || execs != 0 {
				t.Fatalf("mutated invalid request: %d/%d", creates, execs)
			}
		})
	}
}

func TestSuiteCaseReusedSessionRefusesBeforeExec(t *testing.T) {
	s := suiteServer(t)
	r := suiteRequest()
	r.UsedSessionUIDs = map[string]bool{"session-1": true, "other": false}
	_, err := runSuite(t, s, r)
	assertSafeError(t, err, codes.FailedPrecondition)
	if creates, execs := s.counts(); creates != 1 || execs != 0 {
		t.Fatalf("reused Session executed: %d/%d", creates, execs)
	}
	if !reflect.DeepEqual(r.UsedSessionUIDs, map[string]bool{"session-1": true, "other": false}) {
		t.Fatal("caller-owned UID map changed")
	}
}

func TestSuiteCaseLateTransportAndUnknownWireFail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sessionsServer)
		code   codes.Code
	}{
		{"transport after END", func(s *sessionsServer) { s.execErr = status.Error(codes.Unavailable, canary) }, codes.Unavailable},
		{"unknown frame", func(s *sessionsServer) { s.updates[4].ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) }, codes.DataLoss},
		{"unknown output part", func(s *sessionsServer) {
			s.updates[4].GetRecord().Event.GetMessage().Parts[0].ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}, codes.DataLoss},
		{"chain", func(s *sessionsServer) { s.updates[4].GetRecord().ContentHash = strings.Repeat("a", 64) }, codes.DataLoss},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := suiteServer(t)
			tc.change(s)
			_, err := runSuite(t, s, suiteRequest())
			assertSafeError(t, err, tc.code)
			if creates, execs := s.counts(); creates != 1 || execs != 1 {
				t.Fatalf("resent: %d/%d", creates, execs)
			}
		})
	}
}
