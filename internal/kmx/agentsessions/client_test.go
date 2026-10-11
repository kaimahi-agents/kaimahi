package agentsessions

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/canon"
	"github.com/aramase/agentsessions/wire"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const canary = "PRIVATE-REMOTE-PAYLOAD"

// The fake replaces the remote daemon, not transport or adapter behavior.
type sessionsServer struct {
	v1.UnimplementedSessionsServer
	created            *v1.Session
	updates            []*v1.ExecUpdate
	createErr, execErr error
	mu                 sync.Mutex
	creates, execs     int
	createRequest      *v1.CreateSessionRequest
	execRequest        *v1.ExecRequest
	execDeadline       time.Time
}

func (s *sessionsServer) CreateSession(_ context.Context, r *v1.CreateSessionRequest) (*v1.Session, error) {
	s.mu.Lock()
	s.creates++
	s.createRequest = proto.Clone(r).(*v1.CreateSessionRequest)
	s.mu.Unlock()
	return s.created, s.createErr
}

func (s *sessionsServer) Exec(r *v1.ExecRequest, stream v1.Sessions_ExecServer) error {
	s.mu.Lock()
	s.execs++
	s.execRequest = proto.Clone(r).(*v1.ExecRequest)
	s.execDeadline, _ = stream.Context().Deadline()
	s.mu.Unlock()
	for _, u := range s.updates {
		if err := stream.Send(u); err != nil {
			return err
		}
	}
	return s.execErr
}

func (s *sessionsServer) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creates, s.execs
}

func newSession() *v1.Session {
	return &v1.Session{Metadata: &v1.ResourceMetadata{Uid: "session-1"}, Harness: "chat", Model: "metadata-is-not-model-evidence"}
}

func sessionFrame(s *v1.Session) *v1.ExecUpdate {
	return &v1.ExecUpdate{Update: &v1.ExecUpdate_Session{Session: s}}
}

func fixtureEvents() []api.Event {
	count := int64(1)
	return []api.Event{
		{Kind: api.EventExecutionStart, ExecutionStart: &api.ExecutionStart{Config: []byte(`{"system_prompt":"  exact\n\"instructions\" \u003c\u0026\u003e 世界\n"}`), InputCount: &count}},
		{Kind: api.EventInput, Message: api.TextMessage("user", "exact input\n")},
		{Kind: api.EventModelCall, ModelCall: &api.ModelCall{Model: "host-model", InputHash: "input-hash", ID: "call-1"}},
		{Kind: api.EventOutput, Message: api.TextMessage("assistant", "answer")},
		{Kind: api.EventEnd, End: &api.HarnessEnd{State: "COMPLETED"}},
	}
}

func records(t *testing.T, events []api.Event) []*v1.ExecUpdate {
	t.Helper()
	var out []*v1.ExecUpdate
	prev := ""
	for i, e := range events {
		e.ExecutionID = "turn-1"
		e.SchemaVersion = 1
		e.Timestamp = time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
		seq := int64(i + 1)
		hash, err := canon.HashRecord(prev, seq, e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &v1.ExecUpdate{Update: &v1.ExecUpdate_Record{Record: &v1.LogRecord{Seq: seq, PrevHash: prev, ContentHash: hash, Event: wire.EventToProto(e)}}})
		prev = hash
	}
	return out
}

func validServer(t *testing.T) *sessionsServer {
	t.Helper()
	s := newSession()
	return &sessionsServer{created: s, updates: append([]*v1.ExecUpdate{sessionFrame(s)}, records(t, fixtureEvents())...)}
}

func serve(t *testing.T, s *sessionsServer, cert *tls.Certificate) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var opts []grpc.ServerOption
	if cert != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12})))
	}
	server := grpc.NewServer(opts...)
	v1.RegisterSessionsServer(server, s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func caseRequest() CaseRequest {
	return CaseRequest{PortableDigest: "sha256:portable", CasesDigest: "sha256:cases", Instructions: "  exact\n\"instructions\" <&> 世界\n", Model: "host-model", Input: "exact input\n"}
}

func run(t *testing.T, s *sessionsServer) (CaseResult, error) {
	t.Helper()
	c, err := Dial(Options{Address: serve(t, s, nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.RunCase(ctx, caseRequest())
}

// Catches inferred harness/model, dropped config, unset zero-CAS, missing deadline,
// normalized input, truncated output, and returning before stream completion.
func TestRunCaseRequestAndCompleteResult(t *testing.T) {
	s := validServer(t)
	events := fixtureEvents()
	events[3].Message.Parts = append(events[3].Message.Parts, api.Part{Text: &api.TextPart{Text: " two"}})
	events = append(events[:4], api.Event{Kind: api.EventOutput, Message: api.TextMessage("assistant", " three")}, events[4])
	s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, events)...)
	s.updates = append(s.updates[:4], append([]*v1.ExecUpdate{{Update: &v1.ExecUpdate_Delta{Delta: &v1.Delta{ExecutionId: "turn-1", Chunk: "not committed"}}}}, s.updates[4:]...)...)
	c, err := Dial(Options{Address: serve(t, s, nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	got, err := c.RunCase(ctx, caseRequest())
	if err != nil {
		t.Fatal(err)
	}
	last := s.updates[len(s.updates)-1].GetRecord()
	want := CaseResult{SessionUID: "session-1", Harness: "chat", Model: "host-model", Head: JournalHead{Seq: 6, Hash: last.ContentHash}, Output: "answer two three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v; want %#v", got, want)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creates != 1 || s.execs != 1 {
		t.Fatalf("calls = %d create, %d exec", s.creates, s.execs)
	}
	session := s.createRequest.Session
	if session.Harness != "chat" || session.Model != "" || session.GetMetadata().GetUid() != "" || session.LastSeq != 0 {
		t.Fatalf("created request = %v", session)
	}
	if !reflect.DeepEqual(session.Labels, map[string]string{"kmx.portable-digest": "sha256:portable", "kmx.cases-digest": "sha256:cases"}) {
		t.Fatalf("labels = %v", session.Labels)
	}
	r := s.execRequest
	if r.Session != "session-1" || r.Harness != "chat" || r.ExpectedLastSeq == nil || *r.ExpectedLastSeq != 0 || r.ResumeFromSeq != 0 {
		t.Fatalf("exec identity/CAS = %v", r)
	}
	if string(r.Config) != `{"system_prompt":"  exact\n\"instructions\" \u003c\u0026\u003e 世界\n"}` {
		t.Fatalf("config = %s", r.Config)
	}
	if len(r.Inputs) != 1 || !proto.Equal(r.Inputs[0], wire.MessageToProto(api.TextMessage("user", "exact input\n"))) {
		t.Fatalf("inputs = %v", r.Inputs)
	}
	if r.DeadlineUnix != deadline.Unix() || s.execDeadline.IsZero() || s.execDeadline.Sub(deadline).Abs() > 200*time.Millisecond {
		t.Fatalf("deadlines = %d, %v; want %v", r.DeadlineUnix, s.execDeadline, deadline)
	}
}

func assertSafeError(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if err == nil || status.Code(err) != code {
		t.Fatalf("error = %v; want code %s", err, code)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("private remote payload escaped: %v", err)
	}
	if st, ok := status.FromError(err); !ok || len(st.Details()) != 0 {
		t.Fatalf("unsafe error details: %v", err)
	}
}

func TestRunCaseRetainsEvidenceOnRemoteFailure(t *testing.T) {
	for _, afterEnd := range []bool{false, true} {
		t.Run(map[bool]string{false: "mid stream", true: "after end"}[afterEnd], func(t *testing.T) {
			s := validServer(t)
			if !afterEnd {
				s.updates = s.updates[:5]
			}
			s.execErr = status.Error(codes.Unavailable, canary)
			got, err := run(t, s)
			assertSafeError(t, err, codes.Unavailable)
			last := s.updates[len(s.updates)-1].GetRecord()
			if got.SessionUID != "session-1" || got.Model != "host-model" || got.Output != "answer" || got.Head != (JournalHead{Seq: last.Seq, Hash: last.ContentHash}) {
				t.Fatalf("partial = %#v", got)
			}
			if creates, execs := s.counts(); creates != 1 || execs != 1 {
				t.Fatalf("retried: %d, %d", creates, execs)
			}
		})
	}
}

func TestRunCaseCreateFailureDoesNotExec(t *testing.T) {
	s := validServer(t)
	st, detailErr := status.New(codes.PermissionDenied, canary).WithDetails(&v1.Error{Description: canary})
	if detailErr != nil {
		t.Fatal(detailErr)
	}
	s.createErr = st.Err()
	got, err := run(t, s)
	assertSafeError(t, err, codes.PermissionDenied)
	if got != (CaseResult{}) {
		t.Fatalf("result = %#v", got)
	}
	if creates, execs := s.counts(); creates != 1 || execs != 0 {
		t.Fatalf("calls = %d, %d", creates, execs)
	}
}

func TestRunCaseRejectsInvalidCreatedSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*v1.Session)
	}{
		{"missing UID", func(s *v1.Session) { s.Metadata = nil }},
		{"blank UID", func(s *v1.Session) { s.Metadata.Uid = " " }},
		{"wrong harness", func(s *v1.Session) { s.Harness = canary }},
		{"nonzero head", func(s *v1.Session) { s.LastSeq = 1 }},
		{"negative head", func(s *v1.Session) { s.LastSeq = -1 }},
		{"fork", func(s *v1.Session) { s.ParentUid = "parent" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer(t)
			tc.change(s.created)
			got, err := run(t, s)
			assertSafeError(t, err, codes.FailedPrecondition)
			wantUID := s.created.GetMetadata().GetUid()
			if tc.name == "blank UID" {
				wantUID = ""
			}
			if got.SessionUID != wantUID {
				t.Fatalf("lost safe created UID: %#v", got)
			}
			if creates, execs := s.counts(); creates != 1 || execs != 0 {
				t.Fatalf("calls = %d, %d", creates, execs)
			}
		})
	}
}

func TestRunCaseRequiresModelEvidenceAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]api.Event) []api.Event
		code   codes.Code
	}{
		{"no model", func(e []api.Event) []api.Event { return append(e[:2], e[3:]...) }, codes.FailedPrecondition},
		{"empty model", func(e []api.Event) []api.Event { e[2].ModelCall.Model = ""; return e }, codes.FailedPrecondition},
		{"wrong model", func(e []api.Event) []api.Event { e[2].ModelCall.Model = canary; return e }, codes.FailedPrecondition},
		{"mixed models", func(e []api.Event) []api.Event {
			return append(e[:3], append([]api.Event{{Kind: api.EventModelCall, ModelCall: &api.ModelCall{Model: canary}}}, e[3:]...)...)
		}, codes.FailedPrecondition},
		{"repeated same model", func(e []api.Event) []api.Event { return append(e[:3], append([]api.Event{e[2]}, e[3:]...)...) }, codes.OK},
		{"no end", func(e []api.Event) []api.Event { return e[:4] }, codes.FailedPrecondition},
		{"failed end", func(e []api.Event) []api.Event { e[4].End.State = "FAILED"; return e }, codes.FailedPrecondition},
		{"canceled end", func(e []api.Event) []api.Event { e[4].End.State = "CANCELED"; return e }, codes.FailedPrecondition},
		{"unknown end", func(e []api.Event) []api.Event { e[4].End.State = canary; return e }, codes.FailedPrecondition},
		{"end error", func(e []api.Event) []api.Event {
			e[4].End.Error = &api.Error{Code: int32(codes.ResourceExhausted), Description: canary}
			return e
		}, codes.ResourceExhausted},
		{"error event", func(e []api.Event) []api.Event {
			return append(e[:4], append([]api.Event{{Kind: api.EventError, Err: &api.Error{Code: int32(codes.Aborted), Description: canary}}}, e[4:]...)...)
		}, codes.Aborted},
		{"error with OK code", func(e []api.Event) []api.Event { e[4].End.Error = &api.Error{Description: canary}; return e }, codes.Unknown},
		{"error with invalid code", func(e []api.Event) []api.Event { e[4].End.Error = &api.Error{Code: 999, Description: canary}; return e }, codes.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer(t)
			s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, tc.change(fixtureEvents()))...)
			got, err := run(t, s)
			if tc.code == codes.OK {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertSafeError(t, err, tc.code)
			}
			last := s.updates[len(s.updates)-1].GetRecord()
			if got.SessionUID != "session-1" || got.Harness != "chat" || got.Head != (JournalHead{Seq: last.Seq, Hash: last.ContentHash}) {
				t.Fatalf("lost journal evidence: %#v", got)
			}
			if tc.name == "wrong model" && got.Model != canary {
				t.Fatalf("lost safely observed mismatched model: %#v", got)
			}
			if got.ModelMixed != (tc.name == "mixed models") || got.ModelMixed && got.Model != "" {
				t.Fatalf("incorrect model ambiguity: %#v", got)
			}
		})
	}
}

func TestRunCaseRejectsMalformedStreams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sessionsServer)
		head   int64
	}{
		{"missing session", func(s *sessionsServer) { s.updates = s.updates[1:] }, 0},
		{"empty stream", func(s *sessionsServer) { s.updates = nil }, 0},
		{"wrong UID", func(s *sessionsServer) { f := newSession(); f.Metadata.Uid = canary; s.updates[0] = sessionFrame(f) }, 0},
		{"wrong harness", func(s *sessionsServer) { f := newSession(); f.Harness = canary; s.updates[0] = sessionFrame(f) }, 0},
		{"wrong initial head", func(s *sessionsServer) { f := newSession(); f.LastSeq = 3; s.updates[0] = sessionFrame(f) }, 0},
		{"duplicate session", func(s *sessionsServer) {
			s.updates = append(s.updates[:1], append([]*v1.ExecUpdate{sessionFrame(newSession())}, s.updates[1:]...)...)
		}, 0},
		{"empty update", func(s *sessionsServer) { s.updates[3] = &v1.ExecUpdate{} }, 2},
		{"record without event", func(s *sessionsServer) { s.updates[3].GetRecord().Event = nil }, 2},
		{"wrong event body", func(s *sessionsServer) { s.updates[3].GetRecord().Event.Kind = v1.EventKind_EVENT_OUTPUT }, 2},
		{"unknown event kind", func(s *sessionsServer) { s.updates[3].GetRecord().Event.Kind = 999 }, 2},
		{"missing execution ID", func(s *sessionsServer) { s.updates[3].GetRecord().Event.ExecutionId = "" }, 2},
		{"changed execution ID", func(s *sessionsServer) { s.updates[3].GetRecord().Event.ExecutionId = canary }, 2},
		{"invalid timestamp", func(s *sessionsServer) { s.updates[3].GetRecord().Event.Ts.Seconds = 999999999999 }, 2},
		{"sequence gap", func(s *sessionsServer) { s.updates[3].GetRecord().Seq++ }, 2},
		{"repeated sequence", func(s *sessionsServer) { s.updates[3].GetRecord().Seq-- }, 2},
		{"genesis link", func(s *sessionsServer) { s.updates[1].GetRecord().PrevHash = canary }, 0},
		{"broken previous hash", func(s *sessionsServer) { s.updates[3].GetRecord().PrevHash = canary }, 2},
		{"incorrect hash", func(s *sessionsServer) { s.updates[3].GetRecord().ContentHash = canary }, 2},
		{"changed payload", func(s *sessionsServer) { s.updates[4].GetRecord().Event.GetMessage().Parts[0].GetText().Text = canary }, 3},
		{"duplicate end", func(s *sessionsServer) {
			e := fixtureEvents()
			s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, append(e, e[4]))...)
		}, 5},
		{"record after end", func(s *sessionsServer) {
			e := fixtureEvents()
			s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, append(e, e[3]))...)
		}, 5},
		{"wrong delta identity", func(s *sessionsServer) {
			s.updates[4] = &v1.ExecUpdate{Update: &v1.ExecUpdate_Delta{Delta: &v1.Delta{ExecutionId: canary}}}
		}, 3},
		{"negative delta part", func(s *sessionsServer) {
			s.updates[4] = &v1.ExecUpdate{Update: &v1.ExecUpdate_Delta{Delta: &v1.Delta{ExecutionId: "turn-1", PartIndex: -1}}}
		}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer(t)
			tc.change(s)
			got, err := run(t, s)
			assertSafeError(t, err, codes.DataLoss)
			if got.SessionUID != "session-1" || got.Head.Seq != tc.head {
				t.Fatalf("partial = %#v; want verified prefix %d", got, tc.head)
			}
			if tc.head == 0 && got.Head.Hash != "" {
				t.Fatalf("unverified hash retained: %#v", got)
			}
		})
	}
}

func TestRunCaseDoesNotRetainUnsafeRemoteIdentities(t *testing.T) {
	unsafe := []string{"", " ", "line\nbreak", "control\x1bvalue", strings.Repeat("x", 257)}
	for _, shape := range secretshapes.All() {
		unsafe = append(unsafe, shape.Example)
	}
	for i, value := range unsafe {
		for _, field := range []string{"uid", "harness", "model"} {
			t.Run(field+"/"+big.NewInt(int64(i)).String(), func(t *testing.T) {
				s := validServer(t)
				switch field {
				case "uid":
					s.created.Metadata.Uid = value
				case "harness":
					s.created.Harness = value
				case "model":
					e := fixtureEvents()
					e[2].ModelCall.Model = value
					s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, e)...)
				}
				got, err := run(t, s)
				assertSafeError(t, err, codes.FailedPrecondition)
				if strings.TrimSpace(value) != "" && strings.Contains(err.Error(), value) {
					t.Fatalf("identity in error: %v", err)
				}
				if field == "uid" && got.SessionUID != "" || field == "harness" && got.Harness != "" || field == "model" && got.Model != "" {
					t.Fatalf("unsafe identity retained: %#v", got)
				}
				if field == "model" && got.Head.Seq != 5 {
					t.Fatalf("did not drain stream: %#v", got)
				}
			})
		}
	}
}

func TestRunCaseRejectsUnhashedWireContent(t *testing.T) {
	for _, field := range []string{"update", "record", "event", "model messages"} {
		t.Run(field, func(t *testing.T) {
			s := validServer(t)
			u := s.updates[3]
			switch field {
			case "update":
				u.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			case "record":
				u.GetRecord().ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			case "event":
				u.GetRecord().Event.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			case "model messages":
				u.GetRecord().Event.GetModel().Messages = []*v1.Message{wire.MessageToProto(api.TextMessage("user", canary))}
			}
			got, err := run(t, s)
			assertSafeError(t, err, codes.DataLoss)
			if got.Head.Seq != 2 {
				t.Fatalf("accepted unverified wire content: %#v", got)
			}
		})
	}
}

func TestRunCaseExecFailureBeforeSessionRetainsCreatedUID(t *testing.T) {
	s := validServer(t)
	s.updates = nil
	s.execErr = status.Error(codes.Aborted, canary)
	got, err := run(t, s)
	assertSafeError(t, err, codes.Aborted)
	if got != (CaseResult{SessionUID: "session-1", Harness: "chat"}) {
		t.Fatalf("partial = %#v", got)
	}
	if creates, execs := s.counts(); creates != 1 || execs != 1 {
		t.Fatalf("retried: %d, %d", creates, execs)
	}
}

func TestRunCaseCanceledContextDoesNotMutate(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "expired"}[expired], func(t *testing.T) {
			s := validServer(t)
			c, err := Dial(Options{Address: serve(t, s, nil)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			timeout := time.Second
			if expired {
				timeout = -time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			cancel()
			_, err = c.RunCase(ctx, caseRequest())
			code := codes.Canceled
			if expired {
				code = codes.DeadlineExceeded
			}
			assertSafeError(t, err, code)
			if creates, execs := s.counts(); creates != 0 || execs != 0 {
				t.Fatalf("mutated: %d, %d", creates, execs)
			}
		})
	}
}

func TestRunCaseRequiresBoundedContextAndModelBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		request  CaseRequest
		deadline bool
	}{
		{"no deadline", caseRequest(), false},
		{"no expected model", CaseRequest{Input: "input"}, true},
		{"invalid instructions UTF8", CaseRequest{Model: "host-model", Instructions: string([]byte{0xff})}, true},
		{"invalid input UTF8", CaseRequest{Model: "host-model", Input: string([]byte{0xff})}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer(t)
			c, err := Dial(Options{Address: serve(t, s, nil)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx := context.Background()
			if tc.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			_, err = c.RunCase(ctx, tc.request)
			assertSafeError(t, err, codes.InvalidArgument)
			if creates, execs := s.counts(); creates != 0 || execs != 0 {
				t.Fatalf("mutated: %d, %d", creates, execs)
			}
		})
	}
}

func TestDialRejectsNonHostPortTargets(t *testing.T) {
	for _, address := range []string{"", "localhost", ":443", "localhost:http", "localhost:0", "localhost:65536", "localhost:-1", "localhost:+443", "localhost: 443", " localhost:443", "dns:///localhost:443", "passthrough:///localhost:443", "https://localhost:443", "user:password@localhost:443", "localhost:443/path", "localhost:443?query", "localhost:443#fragment", "[::1]", "::1:443", "[::1%lo]:443", "bad..host:443", "-host:443", "[hostname]:443", "127.0.0.1:443\n"} {
		t.Run(address, func(t *testing.T) {
			c, err := Dial(Options{Address: address})
			if c != nil {
				c.Close()
			}
			assertSafeError(t, err, codes.InvalidArgument)
		})
	}
}

func TestDialAcceptsOnlyLiteralLoopbackAsPlaintext(t *testing.T) {
	for _, address := range []string{"127.0.0.1:1234", "127.0.0.2:1234", "[::1]:1234", "[::ffff:127.0.0.1]:1234"} {
		t.Run(address, func(t *testing.T) {
			c, err := Dial(Options{Address: address})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	s := validServer(t)
	address := serve(t, s, nil)
	_, port, _ := net.SplitHostPort(address)
	c, err := Dial(Options{Address: net.JoinHostPort("localhost", port)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = c.RunCase(ctx, caseRequest())
	if err == nil {
		t.Fatal("hostname incorrectly used plaintext")
	}
	if creates, _ := s.counts(); creates != 0 {
		t.Fatal("sent mutation to plaintext hostname")
	}
}

func certificate(t *testing.T, dns string, ip net.IP) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "throwaway CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: dns}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if dns != "" {
		leaf.DNSNames = []string{dns}
	}
	if ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func writeCA(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDialVerifiesTLSAndCA(t *testing.T) {
	for _, tc := range []struct {
		name, dns                      string
		ip                             net.IP
		hostname, trusted, wantSuccess bool
	}{
		{"loopback with CA uses TLS", "", net.ParseIP("127.0.0.1"), false, true, true},
		{"hostname with CA", "localhost", nil, true, true, true},
		{"hostname system roots rejects private CA", "localhost", nil, true, false, false},
		{"wrong hostname", "other.invalid", nil, true, true, false},
		{"wrong IP SAN", "", net.ParseIP("192.0.2.1"), false, true, false},
		{"DNS SAN is not IP SAN", "127.0.0.1", nil, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, ca := certificate(t, tc.dns, tc.ip)
			s := validServer(t)
			address := serve(t, s, &cert)
			if tc.hostname {
				_, port, _ := net.SplitHostPort(address)
				address = net.JoinHostPort("localhost", port)
			}
			opts := Options{Address: address}
			if tc.trusted {
				opts.CAFile = writeCA(t, ca)
			}
			c, err := Dial(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_, err = c.RunCase(ctx, caseRequest())
			if tc.wantSuccess && err != nil {
				t.Fatal(err)
			}
			if !tc.wantSuccess {
				if err == nil {
					t.Fatal("unverified TLS accepted")
				}
				if creates, _ := s.counts(); creates != 0 {
					t.Fatal("mutation reached unverified host")
				}
			}
		})
	}
}

func TestDialRejectsUnreadableOrInvalidCA(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), canary), writeCA(t, []byte(canary)), writeCA(t, []byte("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----\n"))} {
		c, err := Dial(Options{Address: "127.0.0.1:1234", CAFile: path})
		if c != nil {
			c.Close()
		}
		assertSafeError(t, err, codes.InvalidArgument)
	}
}
