package agentsessions

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/canon"
	"github.com/aramase/agentsessions/controller"
	"github.com/aramase/agentsessions/eventlog"
	"github.com/aramase/agentsessions/wire"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func toolPair(id, name string) []api.Event {
	return []api.Event{
		{Kind: api.EventToolCall, ToolCall: &api.ToolCall{ID: id, Tool: name, Mediation: api.MediationControllerMediated, IdempotencyKey: "key-" + id, Args: map[string]any{"private": canary}}},
		{Kind: api.EventToolResult, Result: &api.ToolResult{ID: id, Output: map[string]any{"private": canary}}},
	}
}

func toolServer(t *testing.T, tools []api.Event) *sessionsServer {
	t.Helper()
	s := validServer(t)
	e := fixtureEvents()
	events := append(append(append([]api.Event{}, e[:3]...), tools...), e[3:]...)
	s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, events)...)
	return s
}

// Catches counting intent/proposals, accepting ambiguous correlations, and
// conflating an executor's logical error result with no invocation.
func TestRunCaseToolEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tools    []api.Event
		complete bool
		want     map[string]int
	}{
		{"complete zero", nil, true, map[string]int{}},
		{"matched invocation", toolPair("t1", "inventory"), true, map[string]int{"inventory": 1}},
		{"repeat named invocation", append(toolPair("t1", "inventory"), toolPair("t2", "inventory")...), true, map[string]int{"inventory": 2}},
		{"unresolved intent", toolPair("t1", "inventory")[:1], false, nil},
		{"orphan result", toolPair("t1", "inventory")[1:], false, nil},
		{"result before call", []api.Event{toolPair("t1", "inventory")[1], toolPair("t1", "inventory")[0]}, false, nil},
		{"duplicate pending ID", []api.Event{toolPair("t1", "inventory")[0], toolPair("t1", "other")[0], toolPair("t1", "inventory")[1]}, false, nil},
		{"reused completed ID", append(toolPair("t1", "inventory"), toolPair("t1", "inventory")...), false, nil},
		{"duplicate result", append(toolPair("t1", "inventory"), toolPair("t1", "inventory")[1]), false, nil},
		{"unnamed call", toolPair("t1", ""), false, nil},
		{"missing ID", toolPair("", "inventory"), false, nil},
		{"unsafe name", toolPair("t1", "line\nbreak"), false, nil},
		{"mismatched result ID", []api.Event{toolPair("t1", "inventory")[0], toolPair("t2", "inventory")[1]}, false, nil},
		{"interleaved results", []api.Event{toolPair("t1", "inventory")[0], toolPair("t2", "other")[0], toolPair("t2", "other")[1], toolPair("t1", "inventory")[1]}, true, map[string]int{"inventory": 1, "other": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(t, toolServer(t, tc.tools))
			if err != nil {
				t.Fatal(err)
			}
			if got.Output != "answer" || got.ToolEvidenceComplete != tc.complete || !reflect.DeepEqual(got.ToolCalls, tc.want) {
				t.Fatalf("projection = %#v", got)
			}
		})
	}
	for _, change := range []string{"error result", "unmediated", "in harness", "approval", "no idempotency key", "model proposal"} {
		t.Run(change, func(t *testing.T) {
			p := toolPair("t1", "inventory")
			complete := false
			var want map[string]int
			switch change {
			case "error result":
				p[1].Result.IsError = true
				p[1].Result.Error = canary
				complete = true
				want = map[string]int{"inventory": 1}
			case "unmediated":
				p[0].ToolCall.Mediation = ""
			case "in harness":
				p[0].ToolCall.Mediation = api.MediationInHarnessReported
			case "approval":
				p[0].ToolCall.Mediation = api.MediationRequiresApproval
			case "no idempotency key":
				p[0].ToolCall.IdempotencyKey = ""
			case "model proposal":
				p = []api.Event{{Kind: api.EventOutput, Message: &api.Message{Role: "assistant", Parts: []api.Part{{Data: map[string]any{"tool_calls": []any{map[string]any{"id": "t1", "name": "inventory", "arguments": canary}}}}}}}}
			}
			got, err := run(t, toolServer(t, p))
			if err != nil {
				t.Fatal(err)
			}
			if got.ToolEvidenceComplete != complete || !reflect.DeepEqual(got.ToolCalls, want) {
				t.Fatalf("projection = %#v", got)
			}
		})
	}
}

// Catches a nil complete-zero map being interpreted as unavailable by the
// assertion consumer, without deriving tool capability from journal evidence.
func TestRunCaseToolEvidenceZeroEvaluatesAssertions(t *testing.T) {
	result, err := run(t, toolServer(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	c := runtime.EvaluationCase{Assertions: []runtime.EvaluationAssertion{
		{ID: "called", Type: "toolCalled", Tool: "inventory"},
		{ID: "not-called", Type: "toolNotCalled", Tool: "inventory"},
	}}
	for _, tc := range []struct {
		name      string
		supported bool
		want      []runtime.EvaluationVerdict
	}{
		{"supported", true, []runtime.EvaluationVerdict{runtime.EvaluationFail, runtime.EvaluationPass}},
		{"disabled", false, []runtime.EvaluationVerdict{runtime.EvaluationUnknown, runtime.EvaluationUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := runtime.EvaluateAssertions(c, runtime.EvaluationEvidence{
				ToolSupported: tc.supported, ToolEvidenceComplete: result.ToolEvidenceComplete, ToolCalls: result.ToolCalls,
			})
			if len(rows) != len(tc.want) {
				t.Fatalf("assertion rows = %v", rows)
			}
			for i, row := range rows {
				if row.Verdict != tc.want[i] {
					t.Errorf("%s verdict = %s (%s); want %s", row.ID, row.Verdict, row.Reason, tc.want[i])
				}
				if !tc.supported && row.Reason != "runtime does not support tools" {
					t.Errorf("%s reason = %q", row.ID, row.Reason)
				}
			}
		})
	}
}

// Catches exposing successful-prefix evidence after stream/model/journal failure.
func TestRunCaseToolEvidenceRequiresSuccessfulWholeInvocation(t *testing.T) {
	for _, mode := range []string{"truncated", "remote after end", "wrong model", "failed end", "prior turn", "unknown call body", "unknown result body", "unknown args body", "unknown output body"} {
		t.Run(mode, func(t *testing.T) {
			s := toolServer(t, toolPair("t1", "inventory"))
			code := codes.FailedPrecondition
			switch mode {
			case "truncated":
				s.updates = s.updates[:len(s.updates)-1]
			case "remote after end":
				s.execErr = status.Error(codes.Unavailable, canary)
				code = codes.Unavailable
			case "wrong model":
				e := fixtureEvents()
				e[2].ModelCall.Model = "other-model"
				s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, append(append(e[:3], toolPair("t1", "inventory")...), e[3:]...))...)
			case "failed end":
				last := s.updates[len(s.updates)-1].GetRecord()
				last.Event.GetEnd().State = "FAILED"
				hash, err := canon.HashRecord(last.PrevHash, last.Seq, wire.EventFromProto(last.Event))
				if err != nil {
					t.Fatal(err)
				}
				last.ContentHash = hash
			case "prior turn":
				s.updates[5].GetRecord().Event.ExecutionId = "prior-turn"
				code = codes.DataLoss
			case "unknown call body":
				s.updates[4].GetRecord().Event.GetTool().ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
				code = codes.DataLoss
			case "unknown result body":
				s.updates[5].GetRecord().Event.GetResult().ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
				code = codes.DataLoss
			case "unknown args body":
				s.updates[4].GetRecord().Event.GetTool().GetArgs().ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
				code = codes.DataLoss
			case "unknown output body":
				s.updates[5].GetRecord().Event.GetResult().GetOutput().ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
				code = codes.DataLoss
			}
			got, err := run(t, s)
			assertSafeError(t, err, code)
			if got.ToolEvidenceComplete || got.ToolCalls != nil {
				t.Fatalf("partial tool evidence = %#v", got)
			}
		})
	}
}

func TestToolEvidenceBoundsReleaseRetainedState(t *testing.T) {
	for _, mode := range []string{"records", "bytes", "pending", "execution mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var c toolEvidenceCollector
			feed := func(e api.Event) {
				e.ExecutionID = "turn-1"
				r := &v1.LogRecord{Event: wire.EventToProto(e)}
				if c.reserve(r) {
					c.observe(r.Event)
				}
			}
			for _, e := range toolPair("initial", "inventory") {
				feed(e)
			}
			switch mode {
			case "records":
				for i := 2; i <= 20000; i++ {
					feed(api.Event{Kind: api.EventUsage, Usage: &api.Usage{}})
				}
			case "bytes":
				e := api.Event{Kind: api.EventOutput, Message: api.TextMessage("assistant", strings.Repeat("x", 1<<20))}
				for i := 0; i < 64; i++ {
					feed(e)
				}
			case "pending":
				for i := 0; i < 257; i++ {
					feed(toolPair(fmt.Sprint(i), "inventory")[0])
				}
			case "execution mismatch":
				c.observe(&v1.Event{ExecutionId: "other", Kind: v1.EventKind_EVENT_TOOL_RESULT, Body: &v1.Event_Result{Result: &v1.ToolResult{Id: "initial"}}})
			}
			if !c.unavailable || c.pending != nil || c.seen != nil || c.calls != nil {
				t.Fatalf("unavailable projection retained state before finish: %#v", c)
			}
			calls, complete := c.finish(true)
			if complete || calls != nil || c.pending != nil || c.seen != nil || c.calls != nil {
				t.Fatalf("overflow retained state: %#v", c)
			}
		})
	}
}

// Exact limits remain usable, including 256 interleaved calls. Result counts
// cannot exceed records, and payload-sized byte accounting is never retained.
func TestToolEvidenceExactBounds(t *testing.T) {
	for _, mode := range []string{"records", "bytes", "pending"} {
		t.Run(mode, func(t *testing.T) {
			var c toolEvidenceCollector
			feed := func(e api.Event) {
				e.ExecutionID = "turn-1"
				r := &v1.LogRecord{Event: wire.EventToProto(e)}
				if !c.reserve(r) {
					t.Fatal("within-bound record refused")
				}
				c.observe(r.Event)
			}
			switch mode {
			case "records":
				for i := 0; i < 10000; i++ {
					for _, e := range toolPair(fmt.Sprint(i), "inventory") {
						feed(e)
					}
				}
			case "pending":
				for i := 0; i < 256; i++ {
					feed(toolPair(fmt.Sprint(i), "inventory")[0])
				}
				for i := 255; i >= 0; i-- {
					feed(toolPair(fmt.Sprint(i), "inventory")[1])
				}
			case "bytes":
				// protobuf field 2: 1-byte tag + 4-byte varint + payload = exactly 64 MiB.
				r := &v1.LogRecord{PrevHash: strings.Repeat("x", (64<<20)-5)}
				if !c.reserve(r) || c.bytes != 64<<20 {
					t.Fatal("exact byte bound refused")
				}
			}
			calls, complete := c.finish(true)
			want := map[string]int{}
			if mode == "records" {
				want = map[string]int{"inventory": 10000}
			}
			if mode == "pending" {
				want = map[string]int{"inventory": 256}
			}
			if !complete || !reflect.DeepEqual(calls, want) {
				t.Fatalf("exact bound projection = %v, %v", calls, complete)
			}
		})
	}
}

func TestRunCaseToolEvidenceRecordOverflowPreservesText(t *testing.T) {
	s := validServer(t)
	e := fixtureEvents()
	events := append(e[:3:3], toolPair("t1", "inventory")...)
	for i := 0; i < 19995; i++ {
		events = append(events, api.Event{Kind: api.EventUsage, Usage: &api.Usage{}})
	}
	events = append(events, e[3:]...)
	s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, events)...)
	got, err := run(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Head.Seq != 20002 || got.Output != "answer" || got.ToolEvidenceComplete || got.ToolCalls != nil {
		t.Fatalf("overflow affected text: %#v", got)
	}
}

// Test-only harness follows pinned conformance's host-mediated tool path. No
// production tool capability, provider, daemon or harness registration changes.
type evidenceHarness struct{}

func (evidenceHarness) Describe(context.Context) (api.Descriptor, error) {
	return api.Descriptor{ID: "chat"}, nil
}
func (evidenceHarness) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	if _, err := sink.Model(ctx, api.ModelRequest{Model: "host-model", Messages: start.Inputs}); err != nil {
		return err
	}
	_, err := sink.ToolCall(ctx, api.ToolCall{ID: "producer-call", Tool: "inventory", Mediation: api.MediationControllerMediated, IdempotencyKey: "producer-key", Args: map[string]any{"private": canary}})
	return err
}
func TestRunCaseToolEvidenceFromPinnedController(t *testing.T) {
	log := eventlog.New()
	model := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", "answer")}, nil
	}
	tool := func(context.Context, api.ToolCall) (api.ToolResult, error) {
		return api.ToolResult{ID: "executor-wrong-id", IsError: true, Error: canary, Output: map[string]any{"private": canary}}, nil
	}
	c, err := controller.New(eventlog.AsStore(log), model, controller.WithToolExecutor(tool), controller.WithStart([]byte(`{"system_prompt":"  exact\n\"instructions\" \u003c\u0026\u003e 世界\n"}`), 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(context.Background(), evidenceHarness{}, []api.Message{*api.TextMessage("user", "exact input\n")}, 0); err != nil {
		t.Fatal(err)
	}
	recs := log.Read(1)
	s := validServer(t)
	s.updates = []*v1.ExecUpdate{sessionFrame(s.created)}
	for _, r := range recs {
		s.updates = append(s.updates, &v1.ExecUpdate{Update: &v1.ExecUpdate_Record{Record: eventlog.RecordToProto(r)}})
	}
	got, err := run(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ToolEvidenceComplete || !reflect.DeepEqual(got.ToolCalls, map[string]int{"inventory": 1}) || got.Output != "answer" {
		t.Fatalf("producer projection = %#v", got)
	}
}
