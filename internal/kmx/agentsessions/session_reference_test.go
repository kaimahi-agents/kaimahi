package agentsessions

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/controller"
	"github.com/aramase/agentsessions/eventlog"
	"github.com/aramase/agentsessions/harness/chatagent"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func referenceContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func extractedReference(t *testing.T, c *Client) runtime.SessionReference {
	t.Helper()
	r, err := c.ExtractSessionReference(referenceContext(t), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	r.Store = "127.0.0.1:9000"
	return r
}
func assertReferenceNoContent(t *testing.T, reference runtime.SessionReference) {
	t.Helper()
	data, _ := json.Marshal(reference)
	for _, s := range []string{replayAnswer, "exact input", "instructions", canary} {
		if strings.Contains(string(data), s) {
			t.Fatalf("reference contains content: %s", data)
		}
	}
}

// Detects unbounded range selection, normalization, dropped metadata, and a
// second unverified RPC; real upstream records/replay reconstruct the request.
func TestSessionReferenceExtractResolve(t *testing.T) {
	s, log := recordedChat(t, replayAnswer)
	c := replayClient(t, s)
	r, err := c.ExtractSessionReference(referenceContext(t), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Store != "" || r.SessionUID != "session-1" || r.InputSeq != 2 || r.PrefixSeq != 5 || r.PrefixHash != s.records[4].ContentHash || r.Harness != "chat" || r.HarnessVersion != ReferenceRevision || r.Model != "host-model" || r.InputDigest != testDigest("exact input\n") || r.AnswerDigest != testDigest(replayAnswer) || r.ConfigDigest != testDigest(replayConfig) {
		t.Fatalf("reference=%+v", r)
	}
	assertReferenceNoContent(t, r)
	s.mu.Lock()
	calls, rangeEnd := s.calls, s.request.ToSeq
	s.mu.Unlock()
	if calls != 1 || rangeEnd != 6 {
		t.Fatalf("extraction calls=%d toSeq=%d", calls, rangeEnd)
	}
	r.Store = "127.0.0.1:9000"
	input, err := c.ResolveSessionReference(referenceContext(t), r)
	if err != nil || input != "exact input\n" {
		t.Fatalf("resolution input=%q error=%v", input, err)
	}
	s.mu.Lock()
	calls, rangeEnd = s.calls, s.request.ToSeq
	s.mu.Unlock()
	if calls != 2 || rangeEnd != 5 {
		t.Fatalf("resolution calls=%d toSeq=%d", calls, rangeEnd)
	}
	if len(log.Snapshot()) != 5 || log.NewFence() != 2 {
		t.Fatal("source journal mutated")
	}
}

func TestSessionReferenceExtractionRejectsInvalidSources(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*replayServer)
		rechain bool
	}{
		{"expired", func(s *replayServer) { s.records = nil }, false},
		{"missing", func(s *replayServer) { s.records = s.records[:4] }, false},
		{"altered payload", func(s *replayServer) { s.records[1].Event.GetMessage().Parts[0].GetText().Text = canary }, false},
		{"extra completed turn", func(s *replayServer) {
			for _, r := range append([]*v1.LogRecord(nil), s.records...) {
				s.records = append(s.records, proto.Clone(r).(*v1.LogRecord))
			}
		}, true},
		{"tool", func(s *replayServer) { s.records[2].Event.Kind = v1.EventKind_EVENT_TOOL_CALL }, false},
		{"failed", func(s *replayServer) { s.records[4].Event.GetEnd().State = "FAILED" }, true},
		{"schema", func(s *replayServer) { s.records[2].Event.SchemaVersion = 99 }, true},
		{"configuration changed request", func(s *replayServer) {
			s.records[0].Event.GetExecutionStart().Config = []byte(`{"system_prompt":"` + canary + `"}`)
		}, true},
		{"input changed request", func(s *replayServer) { s.records[1].Event.GetMessage().Parts[0].GetText().Text = canary }, true},
		{"request fingerprint", func(s *replayServer) { s.records[2].Event.GetModel().InputHash = testDigest(canary) }, true},
		{"unsafe model", func(s *replayServer) { s.records[2].Event.GetModel().Model = "private\nmodel" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			tc.change(s)
			if tc.rechain {
				rechain(t, s)
			}
			r, err := replayClient(t, s).ExtractSessionReference(referenceContext(t), "session-1")
			assertSafeError(t, err, codes.DataLoss)
			if r != (runtime.SessionReference{}) {
				t.Fatal("failed extraction returned metadata")
			}
		})
	}
}

func TestSessionReferenceResolutionRejectsMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*replayServer, *runtime.SessionReference)
		noRPC  bool
	}{
		{"expired", func(s *replayServer, r *runtime.SessionReference) { s.records = nil }, false},
		{"remote expired", func(s *replayServer, r *runtime.SessionReference) {
			s.records = nil
			s.err = status.Error(codes.NotFound, canary)
		}, false},
		{"prefix", func(s *replayServer, r *runtime.SessionReference) { r.PrefixHash = testDigest(canary) }, false},
		{"input digest", func(s *replayServer, r *runtime.SessionReference) { r.InputDigest = testDigest(canary) }, false},
		{"answer digest", func(s *replayServer, r *runtime.SessionReference) { r.AnswerDigest = testDigest(canary) }, false},
		{"config digest", func(s *replayServer, r *runtime.SessionReference) { r.ConfigDigest = testDigest(canary) }, false},
		{"model", func(s *replayServer, r *runtime.SessionReference) { r.Model = "other-model" }, false},
		{"altered chain", func(s *replayServer, r *runtime.SessionReference) { s.records[1].PrevHash = testDigest(canary) }, false},
		{"rebound input", func(s *replayServer, r *runtime.SessionReference) {
			s.records[1].Event.GetMessage().Parts[0].GetText().Text = canary
			rechain(t, s)
			r.PrefixHash = s.records[4].ContentHash
			r.InputDigest = testDigest(canary)
		}, false},
		{"rebound config", func(s *replayServer, r *runtime.SessionReference) {
			config := `{"system_prompt":"` + canary + `"}`
			s.records[0].Event.GetExecutionStart().Config = []byte(config)
			rechain(t, s)
			r.PrefixHash = s.records[4].ContentHash
			r.ConfigDigest = testDigest(config)
		}, false},
		{"out of range", func(s *replayServer, r *runtime.SessionReference) {
			s.records = append(s.records, proto.Clone(s.records[4]).(*v1.LogRecord))
			rechain(t, s)
			s.ignoreRange = true
		}, false},
		{"unsafe UID", func(s *replayServer, r *runtime.SessionReference) { r.SessionUID = "private\nuid" }, true},
		{"unsafe store", func(s *replayServer, r *runtime.SessionReference) { r.Store = "private\nstore" }, true},
		{"unsafe model", func(s *replayServer, r *runtime.SessionReference) { r.Model = "private model" }, true},
		{"harness", func(s *replayServer, r *runtime.SessionReference) { r.Harness = "custom" }, true},
		{"compatibility", func(s *replayServer, r *runtime.SessionReference) { r.HarnessVersion = "other" }, true},
		{"input seq", func(s *replayServer, r *runtime.SessionReference) { r.InputSeq = 3 }, true},
		{"prefix seq", func(s *replayServer, r *runtime.SessionReference) { r.PrefixSeq = 6 }, true},
		{"digest format", func(s *replayServer, r *runtime.SessionReference) { r.ConfigDigest = "" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := recordedChat(t, replayAnswer)
			c := replayClient(t, s)
			r := extractedReference(t, c)
			tc.change(s, &r)
			input, err := c.ResolveSessionReference(referenceContext(t), r)
			code := codes.DataLoss
			if tc.noRPC {
				code = codes.InvalidArgument
			}
			if tc.name == "remote expired" {
				code = codes.NotFound
			}
			assertSafeError(t, err, code)
			if input != "" {
				t.Fatal("failed resolution returned content")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			want := 2
			if tc.noRPC {
				want = 1
			}
			if s.calls != want {
				t.Fatalf("calls=%d want=%d", s.calls, want)
			}
		})
	}
}

func TestSessionReferenceExtractionAndResolutionRequireDeadline(t *testing.T) {
	s, _ := recordedChat(t, replayAnswer)
	c := replayClient(t, s)
	if _, err := c.ExtractSessionReference(context.Background(), "session-1"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("extract error=%v", err)
	}
	if _, err := c.ExtractSessionReference(referenceContext(t), "private\nuid"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("uid error=%v", err)
	}
	r := extractedReference(t, c)
	if _, err := c.ResolveSessionReference(context.Background(), r); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("resolve error=%v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls != 1 {
		t.Fatalf("invalid requests reached source: %d", s.calls)
	}
}

func TestSessionReferenceBoundsInputAndAnswer(t *testing.T) {
	for _, operand := range []string{"input", "answer"} {
		for _, size := range []int{1 << 20, (1 << 20) + 1} {
			t.Run(operand+"/"+strconv.Itoa(size), func(t *testing.T) {
				input, answer := "exact input\n", replayAnswer
				if operand == "input" {
					input = strings.Repeat("x", size)
				} else {
					answer = strings.Repeat("x", size)
				}
				log := eventlog.New()
				live, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
					return api.ModelResponse{Message: *api.TextMessage("assistant", answer)}, nil
				}, controller.WithStart([]byte(replayConfig), 0))
				if err != nil {
					t.Fatal(err)
				}
				if err := live.Exec(t.Context(), chatagent.Harness{Model: "host-model"}, []api.Message{*api.TextMessage("user", input)}, 0); err != nil {
					t.Fatal(err)
				}
				s := &replayServer{}
				for _, r := range log.Snapshot() {
					s.records = append(s.records, eventlog.RecordToProto(r))
				}
				c := replayClient(t, s)
				r, err := c.ExtractSessionReference(referenceContext(t), "session-1")
				if size > 1<<20 {
					assertSafeError(t, err, codes.ResourceExhausted)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				r.Store = "127.0.0.1:9000"
				got, err := c.ResolveSessionReference(referenceContext(t), r)
				if err != nil || got != input {
					t.Fatalf("resolution error=%v", err)
				}
				if live.ModelInvocations() != 1 {
					t.Fatal("reference path called model")
				}
			})
		}
	}
}

func TestSessionReferenceStreamCancellationAndErrors(t *testing.T) {
	for _, operation := range []string{"extract", "resolve"} {
		for _, wait := range []bool{true, false} {
			t.Run(operation+"/wait="+strconv.FormatBool(wait), func(t *testing.T) {
				s, _ := recordedChat(t, replayAnswer)
				c := replayClient(t, s)
				r := extractedReference(t, c)
				s.wait = wait
				s.err = status.Error(codes.PermissionDenied, canary)
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
				defer cancel()
				var err error
				if operation == "extract" {
					_, err = c.ExtractSessionReference(ctx, "session-1")
				} else {
					_, err = c.ResolveSessionReference(ctx, r)
				}
				code := codes.PermissionDenied
				if wait {
					code = codes.DeadlineExceeded
				}
				assertSafeError(t, err, code)
			})
		}
	}
}
