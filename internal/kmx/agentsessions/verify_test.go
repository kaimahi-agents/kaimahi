package agentsessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/canon"
	"github.com/aramase/agentsessions/controller"
	"github.com/aramase/agentsessions/eventlog"
	"github.com/aramase/agentsessions/harness/chatagent"
	"github.com/aramase/agentsessions/model/openai"
	"github.com/aramase/agentsessions/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const replayConfig = `{"system_prompt":"  exact\n\"instructions\" <&> 世界\n"}`
const replayAnswer = "  answer\n世界  "

// Only the remote record stream is replaced; recording and reconstruction use
// the actual upstream controller, reference chat harness and canonical records.
type replayServer struct {
	v1.UnimplementedSessionsServer
	records     []*v1.LogRecord
	err         error
	wait        bool
	ignoreRange bool
	mu          sync.Mutex
	request     *v1.ReplayRequest
	calls       int
}

func (s *replayServer) Replay(r *v1.ReplayRequest, stream v1.Sessions_ReplayServer) error {
	s.mu.Lock()
	s.calls++
	s.request = proto.Clone(r).(*v1.ReplayRequest)
	s.mu.Unlock()
	if s.wait {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	for _, rec := range s.records {
		if !s.ignoreRange && (rec.Seq < r.FromSeq || rec.Seq > r.ToSeq) {
			continue
		}
		if err := stream.Send(rec); err != nil {
			return err
		}
	}
	return s.err
}
func replayClient(t *testing.T, s *replayServer) *Client {
	t.Helper()
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
	return c
}
func testDigest(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func replayRequest(s *replayServer) VerifyRequest {
	last := s.records[len(s.records)-1]
	return VerifyRequest{SessionUID: "session-1", Harness: "chat", Model: "host-model", Head: JournalHead{Seq: last.Seq, Hash: last.ContentHash}, AnswerSHA256: testDigest(replayAnswer)}
}
func recordedChat(t *testing.T, answer string) (*replayServer, *eventlog.Log) {
	t.Helper()
	log := eventlog.New()
	c, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", answer)}, nil
	}, controller.WithStart([]byte(replayConfig), 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(t.Context(), chatagent.Harness{Model: "host-model"}, []api.Message{*api.TextMessage("user", "exact input\n")}, 0); err != nil {
		t.Fatal(err)
	}
	s := &replayServer{}
	for _, rec := range log.Snapshot() {
		s.records = append(s.records, eventlog.RecordToProto(rec))
	}
	return s, log
}
func verify(t *testing.T, s *replayServer, r VerifyRequest) (VerifyResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	return replayClient(t, s).VerifyCase(ctx, r)
}
func assertReplayStatus(t *testing.T, got VerifyResult, err error, want string) {
	t.Helper()
	if err != nil || got.Status != want {
		t.Fatalf("result = %+v, error = %v; want %s", got, err, want)
	}
	if got.ModelCalls != 0 {
		t.Fatalf("live model calls = %d", got.ModelCalls)
	}
	blob, _ := json.Marshal(got)
	if strings.Contains(string(blob), canary) || strings.Contains(string(blob), "instructions") || strings.Contains(string(blob), replayAnswer) {
		t.Fatalf("payload leaked: %s", blob)
	}
}

// Catches ignoring the receipt head, normalizing answers, losing config, and
// rebuilding against a live model rather than recorded effects.
func TestVerifyCaseRecordedChat(t *testing.T) {
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Content != "  exact\n\"instructions\" <&> 世界\n" || body.Messages[1].Content != "exact input\n" {
			t.Errorf("recording did not use exact configured prompt: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": replayAnswer}}}})
	}))
	defer provider.Close()
	model, err := openai.New(openai.WithBaseURL(provider.URL), openai.WithModel("host-model"), openai.WithHTTPClient(provider.Client()))
	if err != nil {
		t.Fatal(err)
	}
	log := eventlog.New()
	live, err := controller.New(eventlog.AsStore(log), model.Model, controller.WithStart([]byte(replayConfig), 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Exec(t.Context(), chatagent.Harness{Model: "host-model"}, []api.Message{*api.TextMessage("user", "exact input\n")}, 0); err != nil {
		t.Fatal(err)
	}
	s := &replayServer{}
	for _, rec := range log.Snapshot() {
		s.records = append(s.records, eventlog.RecordToProto(rec))
	}
	before := log.Snapshot()
	r := replayRequest(s)
	got, err := verify(t, s, r)
	assertReplayStatus(t, got, err, "equivalent")
	if got.ConfigSHA256 != testDigest(replayConfig) || got.AnswerSHA256 != testDigest(replayAnswer) || got.Records != 5 {
		t.Fatalf("proof = %+v", got)
	}
	if providerCalls.Load() != 1 || live.ModelInvocations() != 1 {
		t.Fatalf("provider reached during verification: %d", providerCalls.Load())
	}
	after := log.Snapshot()
	if after[len(after)-1].Fence != before[len(before)-1].Fence || log.NewFence() != 2 {
		t.Fatal("verification fenced or mutated the live journal")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls != 1 || s.request.Session != r.SessionUID || s.request.FromSeq != 1 || s.request.ToSeq != r.Head.Seq {
		t.Fatalf("replay request = %v; calls = %d", s.request, s.calls)
	}
}
func rechain(t *testing.T, s *replayServer) {
	t.Helper()
	prev := ""
	for i, r := range s.records {
		r.Seq = int64(i + 1)
		r.PrevHash = prev
		hash, err := canon.HashRecord(prev, r.Seq, wire.EventFromProto(r.Event))
		if err != nil {
			t.Fatal(err)
		}
		r.ContentHash = hash
		prev = hash
	}
}
func TestVerifyCaseRejectsTamperedPrefix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*replayServer, *VerifyRequest)
	}{
		{"payload", func(s *replayServer, r *VerifyRequest) {
			s.records[3].Event.GetMessage().Parts[0].GetText().Text = canary
		}},
		{"previous hash", func(s *replayServer, r *VerifyRequest) { s.records[2].PrevHash = strings.Repeat("a", 64) }},
		{"genesis", func(s *replayServer, r *VerifyRequest) { s.records[0].PrevHash = strings.Repeat("a", 64) }},
		{"record hash", func(s *replayServer, r *VerifyRequest) { s.records[2].ContentHash = strings.Repeat("a", 64) }},
		{"receipt hash", func(s *replayServer, r *VerifyRequest) { r.Head.Hash = strings.Repeat("a", 64) }},
		{"truncated", func(s *replayServer, r *VerifyRequest) { s.records = s.records[:4] }},
		{"gap", func(s *replayServer, r *VerifyRequest) { s.records[2].Seq++ }},
		{"duplicate", func(s *replayServer, r *VerifyRequest) { s.records[2].Seq-- }},
		{"extra", func(s *replayServer, r *VerifyRequest) {
			s.records = append(s.records, proto.Clone(s.records[4]).(*v1.LogRecord))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			r := replayRequest(s)
			tc.change(s, &r)
			s.ignoreRange = true
			got, err := verify(t, s, r)
			assertReplayStatus(t, got, err, "mismatch")
		})
	}
}
func TestVerifyCaseMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*replayServer, *VerifyRequest)
	}{
		{"answer digest", func(s *replayServer, r *VerifyRequest) { r.AnswerSHA256 = testDigest("normalized answer") }},
		{"config divergence", func(s *replayServer, r *VerifyRequest) {
			s.records[0].Event.GetExecutionStart().Config = []byte(`{"system_prompt":"` + canary + `"}`)
		}},
		{"invalid config", func(s *replayServer, r *VerifyRequest) {
			s.records[0].Event.GetExecutionStart().Config = []byte(canary)
		}},
		{"model divergence", func(s *replayServer, r *VerifyRequest) { r.Model = "other-model" }},
		{"input fingerprint", func(s *replayServer, r *VerifyRequest) { s.records[2].Event.GetModel().InputHash = testDigest(canary) }},
		{"input divergence", func(s *replayServer, r *VerifyRequest) {
			s.records[1].Event.GetMessage().Parts[0].GetText().Text = canary
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			r := replayRequest(s)
			tc.change(s, &r)
			rechain(t, s)
			r.Head = replayRequest(s).Head
			got, err := verify(t, s, r)
			assertReplayStatus(t, got, err, "mismatch")
		})
	}
}
func TestVerifyCaseRefusesUnsupportedShapes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*replayServer)
	}{
		{"markerless", func(s *replayServer) { s.records = s.records[1:] }},
		{"no end", func(s *replayServer) { s.records = s.records[:4] }},
		{"failed end", func(s *replayServer) { s.records[4].Event.GetEnd().State = "FAILED" }},
		{"end error", func(s *replayServer) { s.records[4].Event.GetEnd().Error = &v1.Error{Description: canary} }},
		{"missing count", func(s *replayServer) { s.records[0].Event.GetExecutionStart().InputCount = nil }},
		{"incorrect count", func(s *replayServer) { n := int64(2); s.records[0].Event.GetExecutionStart().InputCount = &n }},
		{"resume", func(s *replayServer) { s.records[0].Event.GetExecutionStart().ResumeFromSeq = 1 }},
		{"extra execution", func(s *replayServer) { s.records[3].Event.ExecutionId = "second-turn" }},
		{"second invocation", func(s *replayServer) {
			for _, r := range append([]*v1.LogRecord(nil), s.records...) {
				s.records = append(s.records, proto.Clone(r).(*v1.LogRecord))
			}
		}},
		{"model params", func(s *replayServer) { s.records[2].Event.GetModel().Params = map[string]string{"temperature": "1"} }},
		{"model absent", func(s *replayServer) { s.records = append(s.records[:2], s.records[3:]...) }},
		{"extra output", func(s *replayServer) {
			s.records = append(s.records[:4], proto.Clone(s.records[3]).(*v1.LogRecord), s.records[4])
		}},
		{"tool", func(s *replayServer) {
			s.records[2].Event = wire.EventToProto(api.Event{ExecutionID: s.records[0].Event.ExecutionId, Kind: api.EventToolCall, ToolCall: &api.ToolCall{Tool: canary}})
		}},
		{"fork", func(s *replayServer) {
			s.records[2].Event = wire.EventToProto(api.Event{ExecutionID: s.records[0].Event.ExecutionId, Kind: api.EventLifecycle, Lifecycle: &api.Lifecycle{Kind: api.LifecycleFork}})
		}},
		{"error", func(s *replayServer) {
			s.records[2].Event = wire.EventToProto(api.Event{ExecutionID: s.records[0].Event.ExecutionId, Kind: api.EventError, Err: &api.Error{Description: canary}})
		}},
		{"schema skew", func(s *replayServer) { s.records[2].Event.SchemaVersion = 999 }},
		{"file output", func(s *replayServer) {
			s.records[3].Event.GetMessage().Parts = wire.MessageToProto(&api.Message{Role: "assistant", Parts: []api.Part{{File: &api.FilePart{URI: "https://example.invalid/" + canary}}}}).Parts
		}},
		{"output role", func(s *replayServer) { s.records[3].Event.GetMessage().Role = "tool" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			tc.change(s)
			rechain(t, s)
			got, err := verify(t, s, replayRequest(s))
			assertReplayStatus(t, got, err, "unsupported")
		})
	}
}
func TestVerifyCaseRejectsUnhashedWireContent(t *testing.T) {
	for _, field := range []string{"record", "event", "start", "message", "part", "text", "model messages", "timestamp", "empty actor", "unknown kind", "wrong body"} {
		t.Run(field, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			r := replayRequest(s)
			unknown := []byte{0xa0, 0x06, 0x01}
			switch field {
			case "record":
				s.records[2].ProtoReflect().SetUnknown(unknown)
			case "event":
				s.records[2].Event.ProtoReflect().SetUnknown(unknown)
			case "start":
				s.records[0].Event.GetExecutionStart().ProtoReflect().SetUnknown(unknown)
			case "message":
				s.records[3].Event.GetMessage().ProtoReflect().SetUnknown(unknown)
			case "part":
				s.records[3].Event.GetMessage().Parts[0].ProtoReflect().SetUnknown(unknown)
			case "text":
				s.records[3].Event.GetMessage().Parts[0].GetText().ProtoReflect().SetUnknown(unknown)
			case "model messages":
				s.records[2].Event.GetModel().Messages = []*v1.Message{wire.MessageToProto(api.TextMessage("user", canary))}
			case "timestamp":
				s.records[2].Event = proto.Clone(records(t, fixtureEvents())[2].GetRecord().Event).(*v1.Event)
				s.records[2].Event.Ts.Seconds = 999999999999
			case "empty actor":
				s.records[2].Event.Actor = &v1.IdentityRef{}
			case "unknown kind":
				s.records[2].Event.Kind = 999
			case "wrong body":
				s.records[2].Event.Kind = v1.EventKind_EVENT_OUTPUT
			}
			got, err := verify(t, s, r)
			assertReplayStatus(t, got, err, "unsupported")
		})
	}
}
func TestVerifyCaseValidatesBeforeRPC(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*VerifyRequest)
		deadline bool
		want     string
		code     codes.Code
	}{
		{"no deadline", func(*VerifyRequest) {}, false, "unknown", codes.InvalidArgument},
		{"unsafe uid", func(r *VerifyRequest) { r.SessionUID = "private\nuid" }, true, "unknown", codes.InvalidArgument},
		{"missing model", func(r *VerifyRequest) { r.Model = "" }, true, "unknown", codes.InvalidArgument},
		{"unsafe harness", func(r *VerifyRequest) { r.Harness = " " }, true, "unknown", codes.InvalidArgument},
		{"zero head", func(r *VerifyRequest) { r.Head.Seq = 0 }, true, "unknown", codes.InvalidArgument},
		{"bad head digest", func(r *VerifyRequest) { r.Head.Hash = canary }, true, "unknown", codes.InvalidArgument},
		{"missing answer digest", func(r *VerifyRequest) { r.AnswerSHA256 = "" }, true, "unknown", codes.InvalidArgument},
		{"uppercase digest", func(r *VerifyRequest) { r.AnswerSHA256 = strings.Repeat("A", 64) }, true, "unknown", codes.InvalidArgument},
		{"record cap", func(r *VerifyRequest) { r.Head.Seq = 20001 }, true, "unknown", codes.ResourceExhausted},
		{"non-chat", func(r *VerifyRequest) { r.Harness = "custom" }, true, "unsupported", codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			r := replayRequest(s)
			tc.change(&r)
			c := replayClient(t, s)
			ctx := context.Background()
			if tc.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			got, err := c.VerifyCase(ctx, r)
			if tc.code == codes.OK {
				assertReplayStatus(t, got, err, tc.want)
			} else {
				assertSafeError(t, err, tc.code)
				if got.Status != tc.want {
					t.Fatalf("status = %s", got.Status)
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.calls != 0 {
				t.Fatal("invalid/unsupported request reached RPC")
			}
		})
	}
}
func TestVerifyCaseLaterAppendsOnlyVerifyReceiptPrefix(t *testing.T) {
	s, log := recordedChat(t, replayAnswer)
	r := replayRequest(s)
	fence := log.NewFence()
	rec, err := log.Append(log.Head(), fence, api.Event{Kind: api.EventLifecycle, Lifecycle: &api.Lifecycle{Kind: api.LifecycleSuspend}})
	if err != nil {
		t.Fatal(err)
	}
	s.records = append(s.records, eventlog.RecordToProto(rec))
	got, err := verify(t, s, r)
	assertReplayStatus(t, got, err, "equivalent")
	if got.Records != 5 {
		t.Fatalf("used later append: %+v", got)
	}
}
func TestVerifyCaseAnswerBoundAndExactBytes(t *testing.T) {
	for _, size := range []int{0, 1 << 20, (1 << 20) + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			answer := strings.Repeat("x", size)
			s, _ := recordedChat(t, answer)
			r := replayRequest(s)
			r.AnswerSHA256 = testDigest(answer)
			got, err := verify(t, s, r)
			if size > 1<<20 {
				assertSafeError(t, err, codes.ResourceExhausted)
				if got.Status != "unknown" {
					t.Fatalf("status = %s", got.Status)
				}
			} else {
				assertReplayStatus(t, got, err, "equivalent")
				if got.AnswerSHA256 != testDigest(answer) {
					t.Fatalf("digest = %s", got.AnswerSHA256)
				}
			}
		})
	}
}
func TestVerifyCaseCancellationAndSafeRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wait, cancel bool
		timeout      time.Duration
		code         codes.Code
	}{
		{"canceled before RPC", false, true, time.Second, codes.Canceled},
		{"expired before RPC", false, false, -time.Second, codes.DeadlineExceeded},
		{"deadline during stream", true, false, 30 * time.Millisecond, codes.DeadlineExceeded},
		{"remote failure", false, false, time.Second, codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			s.wait = tc.wait
			s.err = status.Error(codes.PermissionDenied, canary)
			c := replayClient(t, s)
			ctx, cancel := context.WithTimeout(t.Context(), tc.timeout)
			defer cancel()
			if tc.cancel {
				cancel()
			}
			got, err := c.VerifyCase(ctx, replayRequest(s))
			assertSafeError(t, err, tc.code)
			if got.Status != "unknown" {
				t.Fatalf("status = %s", got.Status)
			}
		})
	}
}
func TestVerifyCaseAggregateByteBound(t *testing.T) {
	s, _ := recordedChat(t, replayAnswer)
	// Individually valid records below the gRPC default message limit; the
	// aggregate exceeds 64 MiB and must stop before semantic reconstruction.
	base := proto.Clone(s.records[0]).(*v1.LogRecord)
	base.Event.GetExecutionStart().Config = []byte(strings.Repeat("x", 1<<20))
	s.records = nil
	for range 65 {
		s.records = append(s.records, proto.Clone(base).(*v1.LogRecord))
	}
	rechain(t, s)
	// Race instrumentation makes canonicalizing this deliberately large fixture
	// substantially slower. Keep its resource-bound assertion independent of
	// the short deadlines exercised by the cancellation tests.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	got, err := replayClient(t, s).VerifyCase(ctx, replayRequest(s))
	assertSafeError(t, err, codes.ResourceExhausted)
	if got.Status != "unknown" || got.Records > 64 {
		t.Fatalf("unbounded result = %+v", got)
	}
}
