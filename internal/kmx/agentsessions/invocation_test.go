package agentsessions

import (
	"testing"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The hash-valid journal must describe the exact dispatched invocation, not just
// a self-consistent model answer to some different instructions or input.
func TestRunCaseBindsInvocationPrefixToExactRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]api.Event) []api.Event
	}{
		{"wrong config", func(e []api.Event) []api.Event {
			e[0].ExecutionStart.Config = []byte(`{"system_prompt":"` + canary + `"}`)
			return e
		}},
		{"equivalent JSON with different bytes", func(e []api.Event) []api.Event {
			e[0].ExecutionStart.Config = append([]byte(" "), e[0].ExecutionStart.Config...)
			return e
		}},
		{"missing config", func(e []api.Event) []api.Event { e[0].ExecutionStart.Config = nil; return e }},
		{"nonzero resume cursor", func(e []api.Event) []api.Event { e[0].ExecutionStart.ResumeFromSeq = 1; return e }},
		{"negative resume cursor", func(e []api.Event) []api.Event { e[0].ExecutionStart.ResumeFromSeq = -1; return e }},
		{"missing input count", func(e []api.Event) []api.Event { e[0].ExecutionStart.InputCount = nil; return e }},
		{"zero input count", func(e []api.Event) []api.Event { *e[0].ExecutionStart.InputCount = 0; return e }},
		{"extra input count", func(e []api.Event) []api.Event { *e[0].ExecutionStart.InputCount = 2; return e }},
		{"negative input count", func(e []api.Event) []api.Event { *e[0].ExecutionStart.InputCount = -1; return e }},
		{"missing start", func(e []api.Event) []api.Event { return e[1:] }},
		{"missing input", func(e []api.Event) []api.Event { return []api.Event{e[0], e[2], e[3], e[4]} }},
		{"missing whole prefix", func(e []api.Event) []api.Event { return e[2:] }},
		{"wrong input text", func(e []api.Event) []api.Event { e[1].Message = api.TextMessage("user", canary); return e }},
		{"wrong input role", func(e []api.Event) []api.Event { e[1].Message.Role = "assistant"; return e }},
		{"normalized input text", func(e []api.Event) []api.Event { e[1].Message = api.TextMessage("user", "exact input"); return e }},
		{"input with extra content", func(e []api.Event) []api.Event {
			e[1].Message.Parts = append(e[1].Message.Parts, api.Part{Text: &api.TextPart{Text: canary}})
			return e
		}},
		{"duplicate start", func(e []api.Event) []api.Event { return []api.Event{e[0], e[0], e[1], e[2], e[3], e[4]} }},
		{"duplicate input", func(e []api.Event) []api.Event { return []api.Event{e[0], e[1], e[1], e[2], e[3], e[4]} }},
		{"late duplicate start", func(e []api.Event) []api.Event { return []api.Event{e[0], e[1], e[2], e[3], e[0], e[4]} }},
		{"late duplicate input", func(e []api.Event) []api.Event { return []api.Event{e[0], e[1], e[2], e[3], e[1], e[4]} }},
		{"input before start", func(e []api.Event) []api.Event { return []api.Event{e[1], e[0], e[2], e[3], e[4]} }},
		{"model before start", func(e []api.Event) []api.Event { return []api.Event{e[2], e[0], e[1], e[3], e[4]} }},
		{"output before start", func(e []api.Event) []api.Event { return []api.Event{e[3], e[0], e[1], e[2], e[4]} }},
		{"model before input", func(e []api.Event) []api.Event { return []api.Event{e[0], e[2], e[1], e[3], e[4]} }},
		{"output before input", func(e []api.Event) []api.Event { return []api.Event{e[0], e[3], e[1], e[2], e[4]} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer(t)
			s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, tc.change(fixtureEvents()))...)
			got, err := run(t, s)
			assertSafeError(t, err, codes.DataLoss)
			last := s.updates[len(s.updates)-1].GetRecord()
			if got.SessionUID != "session-1" || got.Head != (JournalHead{Seq: last.Seq, Hash: last.ContentHash}) || got.Output != "answer" {
				t.Fatalf("semantic mismatch lost verified evidence: %#v", got)
			}
		})
	}
}

func TestRunCaseMissingInvocationPrefixAtEOF(t *testing.T) {
	for _, onlyStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "session only", true: "start only"}[onlyStart], func(t *testing.T) {
			s := validServer(t)
			s.updates = s.updates[:1]
			if onlyStart {
				s.updates = append(s.updates, records(t, fixtureEvents()[:1])...)
			}
			got, err := run(t, s)
			assertSafeError(t, err, codes.DataLoss)
			wantSeq := int64(0)
			if onlyStart {
				wantSeq = 1
			}
			if got.SessionUID != "session-1" || got.Head.Seq != wantSeq {
				t.Fatalf("partial = %#v", got)
			}
		})
	}
}

func TestRunCaseMixedModelEvidenceIsSticky(t *testing.T) {
	for _, remoteFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed stream", true: "remote stream error"}[remoteFailure], func(t *testing.T) {
			s := validServer(t)
			e := fixtureEvents()
			// A -> B -> A must never restore A as a singular identity.
			events := []api.Event{e[0], e[1], e[2], {Kind: api.EventModelCall, ModelCall: &api.ModelCall{Model: "other-model"}}, e[2], e[3], e[4]}
			s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, events)...)
			wantCode := codes.FailedPrecondition
			if remoteFailure {
				s.execErr = status.Error(codes.Unavailable, canary)
				wantCode = codes.Unavailable
			}
			got, err := run(t, s)
			assertSafeError(t, err, wantCode)
			last := s.updates[len(s.updates)-1].GetRecord()
			// Whole-value comparison also keeps CaseResult's comparable contract.
			want := CaseResult{SessionUID: "session-1", Harness: "chat", ModelMixed: true, Head: JournalHead{Seq: last.Seq, Hash: last.ContentHash}, Output: "answer"}
			if got != want {
				t.Fatalf("mixed evidence = %#v; want %#v", got, want)
			}
		})
	}
}
