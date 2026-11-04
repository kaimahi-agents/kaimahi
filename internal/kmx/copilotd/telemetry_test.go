package copilotd

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

func TestTraceTelemetryUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage   string
		reports       int
		input, output *int64
	}{
		{name: "absent"},
		{name: "model only", usage: `{"model":"model"}`, reports: 1},
		{name: "zero", usage: `{"model":"model","inputTokens":0,"outputTokens":0}`, reports: 1, input: token(0), output: token(0)},
		{name: "one sided", usage: `{"model":"model","outputTokens":7}`, reports: 1, output: token(7)},
		{name: "negative withheld", usage: `{"model":"model","inputTokens":-1,"outputTokens":4}`, reports: 1, output: token(4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{})
			if tc.usage != "" {
				if err := tr.accept(event(t, 1, `{"type":"chat/usage","turnId":"turn","usage":`+tc.usage+`}`)); err != nil {
					t.Fatal(err)
				}
			}
			got := tr.telemetry()
			same := func(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
			if got.UsageReports != tc.reports || !same(got.InputTokens, tc.input) || !same(got.OutputTokens, tc.output) {
				t.Fatalf("usage presence lost: %+v", got)
			}
			if tc.usage != "" {
				if err := tr.accept(event(t, 2, `{"type":"chat/usage","turnId":"turn","usage":`+tc.usage+`}`)); err != nil {
					t.Fatal(err)
				}
				got = tr.telemetry()
				if got.UsageReports != 2 || got.InputTokens != nil || got.OutputTokens != nil {
					t.Fatal("ambiguous reports selected or summed", got)
				}
			}
		})
	}
}
func token(n int64) *int64 { return &n }

func TestTraceTelemetryAttribution(t *testing.T) {
	for _, kind := range []string{"channel", "turn", "rejected", "model", "sequence", "missing-model"} {
		t.Run(kind, func(t *testing.T) {
			tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{})
			e := event(t, 1, `{"type":"chat/usage","turnId":"turn","usage":{"model":"model","inputTokens":8}}`)
			switch kind {
			case "channel":
				e.Channel = "ahp-chat:/other"
			case "turn":
				e = event(t, 1, `{"type":"chat/usage","turnId":"other","usage":{"model":"model","inputTokens":8}}`)
			case "rejected":
				s := "no"
				e.RejectionReason = &s
			case "model":
				e = event(t, 1, `{"type":"chat/usage","turnId":"turn","usage":{"model":"wrong","inputTokens":8}}`)
			case "sequence":
				e.ServerSeq = 0
			case "missing-model":
				e = event(t, 1, `{"type":"chat/usage","turnId":"turn","usage":{"inputTokens":8}}`)
			}
			_ = tr.accept(e)
			if got := tr.telemetry(); got.UsageReports != 0 || got.InputTokens != nil {
				t.Fatal("foreign/invalid usage admitted", got)
			}
		})
	}
}

func TestTraceTelemetryDrainBarrier(t *testing.T) {
	tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{})
	entered, release := make(chan struct{}), make(chan struct{})
	tr.observe = func(o agentpreview.Observation) error {
		if o.Event == "tool.finished" {
			close(entered)
			<-release
		}
		return nil
	}
	for i, a := range []string{
		`{"type":"chat/turnStarted","turnId":"turn","startedAt":"2026-11-01T00:00:00Z","message":{"text":"read","origin":{"kind":"user"}}}`,
		`{"type":"chat/usage","turnId":"turn","usage":{"model":"model"}}`,
		`{"type":"chat/toolCallStart","turnId":"turn","toolCallId":"private-call","toolName":"view","displayName":"View"}`,
	} {
		if err := tr.accept(event(t, i+1, a)); err != nil {
			t.Fatal(err)
		}
	}
	finished := make(chan error, 1)
	go func() {
		finished <- tr.accept(event(t, 4, `{"type":"chat/toolCallComplete","turnId":"turn","toolCallId":"private-call","result":{"success":true,"content":[]}}`))
	}()
	<-entered
	terminal := make(chan error, 1)
	go func() {
		terminal <- tr.accept(event(t, 5, `{"type":"chat/turnComplete","turnId":"turn","duration":1}`))
	}()
	select {
	case <-tr.done:
		t.Fatal("terminal released before tool sink drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-terminal; err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.done:
	default:
		t.Fatal("drained terminal never released")
	}
}

func TestTraceTelemetryFailureAndBound(t *testing.T) {
	t.Run("sink failure", func(t *testing.T) {
		tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{})
		tr.observe = func(agentpreview.Observation) error { return errors.New("private sink failure") }
		err := tr.accept(event(t, 1, `{"type":"chat/turnStarted","turnId":"turn","startedAt":"2026-11-01T00:00:00Z","message":{"text":"read","origin":{"kind":"user"}}}`))
		if !errors.Is(err, agentpreview.ErrDiagnosticsFailed) || !errors.Is(tr.failure(), agentpreview.ErrDiagnosticsFailed) {
			t.Fatal("sink failure lost diagnostic attribution", err)
		}
	})
	for _, name := range []string{"view", "task", "agent"} {
		t.Run(name, func(t *testing.T) {
			tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{})
			tools := 0
			tr.observe = func(o agentpreview.Observation) error {
				if strings.HasPrefix(o.Event, "tool.") {
					tools++
				}
				return nil
			}
			for i := 1; i <= 600; i++ {
				err := tr.accept(event(t, i, fmt.Sprintf(`{"type":"chat/toolCallStart","turnId":"turn","toolCallId":"c%d","toolName":%q,"displayName":"View"}`, i, name)))
				if name != "view" {
					if !errors.Is(err, ErrDelegationUnverified) {
						t.Fatal(err)
					}
					break
				} else if err != nil {
					t.Fatal(err)
				}
			}
			_ = tr.accept(event(t, 601, `{"type":"chat/usage","turnId":"turn","usage":{"model":"model"}}`))
			if tools != 0 {
				t.Fatal("overflow/delegated starts escaped", tools)
			}
		})
	}
}
