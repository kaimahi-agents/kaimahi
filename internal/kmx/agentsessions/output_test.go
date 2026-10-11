package agentsessions

import (
	"strings"
	"testing"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func serverWithOutputs(t *testing.T, messages [][]string) *sessionsServer {
	t.Helper()
	s := validServer(t)
	e := fixtureEvents()
	events := append([]api.Event{}, e[:3]...)
	for _, parts := range messages {
		message := &api.Message{Role: "assistant"}
		for _, text := range parts {
			message.Parts = append(message.Parts, api.Part{Text: &api.TextPart{Text: text}})
		}
		events = append(events, api.Event{Kind: api.EventOutput, Message: message})
	}
	events = append(events, e[4])
	s.updates = append([]*v1.ExecUpdate{sessionFrame(s.created)}, records(t, events)...)
	return s
}

func TestRunCaseAcceptsExactOneMiBCommittedAnswer(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(map[bool]string{false: "single message", true: "multiple messages and UTF8 parts"}[multiple], func(t *testing.T) {
			answer := strings.Repeat("a", 1<<20)
			messages := [][]string{{answer}}
			if multiple {
				left := strings.Repeat("a", (1<<19)-1)
				right := strings.Repeat("b", (1<<19)-1)
				messages = [][]string{{left, "é"}, {right}}
				answer = left + "é" + right
			}
			s := serverWithOutputs(t, messages)
			got, err := run(t, s)
			if err != nil {
				t.Fatal(err)
			}
			if got.Output != answer || len(got.Output) != 1<<20 {
				t.Fatalf("answer bytes = %d; want exact 1 MiB answer", len(got.Output))
			}
			last := s.updates[len(s.updates)-1].GetRecord()
			if got.Head != (JournalHead{Seq: last.Seq, Hash: last.ContentHash}) {
				t.Fatalf("head = %#v; want full chain", got.Head)
			}
		})
	}
}

func TestRunCaseOutputOverflowClearsAnswerAndDrainsJournal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages [][]string
	}{
		{"multiple messages", [][]string{{strings.Repeat("a", 1<<19)}, {strings.Repeat("b", 1<<19)}, {"c"}, {"must not reaccumulate"}}},
		{"multiple parts", [][]string{{strings.Repeat("a", 1<<20), "b"}, {"must not reaccumulate"}}},
		// Rune count fits the cap; byte count exceeds it by one.
		{"UTF8 byte overflow", [][]string{{strings.Repeat("a", (1<<20)-1), "é"}, {"must not reaccumulate"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := serverWithOutputs(t, tc.messages)
			got, err := run(t, s)
			assertSafeError(t, err, codes.ResourceExhausted)
			if status.Convert(err).Message() != "agentsessions: committed answer exceeds 1 MiB" {
				t.Fatalf("overflow diagnostic = %v", err)
			}
			if got.Output != "" {
				t.Fatalf("overflow retained %d answer bytes", len(got.Output))
			}
			last := s.updates[len(s.updates)-1].GetRecord()
			if got.SessionUID != "session-1" || got.Model != "host-model" || got.Head != (JournalHead{Seq: last.Seq, Hash: last.ContentHash}) {
				t.Fatalf("lost verified evidence: session %q, model %q, head %#v", got.SessionUID, got.Model, got.Head)
			}
			if creates, execs := s.counts(); creates != 1 || execs != 1 {
				t.Fatalf("calls = %d, %d", creates, execs)
			}
		})
	}
}

func TestRunCaseOutputOverflowStillReadsFinalStreamError(t *testing.T) {
	s := serverWithOutputs(t, [][]string{{strings.Repeat("a", 1<<20)}, {"overflow"}, {"must not reaccumulate"}})
	s.execErr = status.Error(codes.Unavailable, canary)
	got, err := run(t, s)
	assertSafeError(t, err, codes.Unavailable)
	if got.Output != "" {
		t.Fatalf("overflow retained %d answer bytes", len(got.Output))
	}
	last := s.updates[len(s.updates)-1].GetRecord()
	if got.Head != (JournalHead{Seq: last.Seq, Hash: last.ContentHash}) {
		t.Fatalf("did not drain verified journal: %#v", got.Head)
	}
}
