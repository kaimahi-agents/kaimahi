package copilotd

import (
	"encoding/json"
	"fmt"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"strings"
	"testing"
)

func event(t *testing.T, n int, action string) ahptypes.ActionEnvelope {
	t.Helper()
	var a ahptypes.ActionEnvelope
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"channel":"ahp-chat:/a","serverSeq":%d,"action":%s}`, n, action)), &a); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestTraceRequiresTurnCorrelation(t *testing.T) {
	for _, action := range []string{
		`{"type":"chat/usage","turnId":"","usage":{"model":"model"}}`,
		`{"type":"chat/toolCallStart","toolCallId":"call","toolName":"view","displayName":"View"}`,
		`{"type":"chat/toolCallReady","toolCallId":"call","toolInput":"path","invocationMessage":"view"}`,
		`{"type":"chat/toolCallComplete","toolCallId":"call","result":{"success":true,"content":[]}}`,
		`{"type":"chat/responsePart","part":{"kind":"markdown","id":"answer","content":"marker"}}`,
		`{"type":"chat/turnComplete","duration":1}`,
	} {
		t.Run(action, func(t *testing.T) {
			tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{Resource: "ahp-chat:/a"})
			if strings.Contains(action, "toolCallReady") || strings.Contains(action, "toolCallComplete") {
				tr.calls["call"] = &nativeCall{name: "view"}
			}
			if err := tr.accept(event(t, 1, action)); err == nil {
				t.Fatal("accepted uncorrelated turn action")
			}
		})
	}
	tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{Resource: "ahp-chat:/a"})
	if err := tr.accept(event(t, 1, `{"type":"chat/isReadChanged","isRead":true}`)); err != nil {
		t.Fatalf("chat-scoped event refused: %v", err)
	}
}

func TestTraceNativeRoundsAndSnapshot(t *testing.T) {
	tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{Resource: "ahp-chat:/a"})
	seq := 1
	send := func(s string) {
		t.Helper()
		if err := tr.accept(event(t, seq, s)); err != nil {
			t.Fatal(err)
		}
		seq++
	}
	send(`{"type":"chat/turnStarted","turnId":"turn","startedAt":"2026-11-01T00:00:00Z","message":{"text":"read","origin":{"kind":"user"}}}`)
	for i := 0; i < 7; i++ {
		send(`{"type":"chat/usage","turnId":"turn","usage":{"model":"model"}}`)
		send(fmt.Sprintf(`{"type":"chat/toolCallStart","turnId":"turn","toolCallId":"call%d","toolName":"view","displayName":"View"}`, i))
		send(fmt.Sprintf(`{"type":"chat/toolCallReady","turnId":"turn","toolCallId":"call%d","toolInput":"path","invocationMessage":"view","confirmed":"not-needed"}`, i))
		send(fmt.Sprintf(`{"type":"chat/toolCallComplete","turnId":"turn","toolCallId":"call%d","result":{"success":true,"content":[]}}`, i))
	}
	send(`{"type":"chat/usage","turnId":"turn","usage":{"model":"model"}}`)
	send(`{"type":"chat/responsePart","turnId":"turn","part":{"kind":"markdown","id":"answer","content":"marker"}}`)
	send(`{"type":"chat/turnComplete","turnId":"turn","duration":10}`)
	state := tr.snapshot()
	answer, models, calls, err := tr.finish(&state)
	if err != nil || answer != "marker" || len(models) != 1 || models[0] != "model" || calls["view"] != 7 {
		t.Fatalf("%q %v %v %v", answer, models, calls, err)
	}
	state.Turns = nil
	if _, _, _, err = tr.finish(&state); err == nil {
		t.Fatal("unreconciled trace passed")
	}
}
func TestTraceMissingModelAndForeignRejection(t *testing.T) {
	tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{Resource: "ahp-chat:/a"})
	a := event(t, 1, `{"type":"chat/usage","turnId":"turn","usage":{"model":"wrong"}}`)
	reason := "foreign"
	a.RejectionReason = &reason
	if err := tr.accept(a); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := tr.finish(&ahptypes.ChatState{}); err == nil {
		t.Fatal("missing evidence passed")
	}
	if err := tr.accept(event(t, 2, `{"type":"chat/usage","turnId":"turn","usage":{"model":"wrong"}}`)); err == nil {
		t.Fatal("actual model mismatch accepted")
	}
}
func TestTracePendingInputAndDuplicateCall(t *testing.T) {
	tr := newTrace("ahp-chat:/a", "turn", "model", ahptypes.ChatState{Resource: "ahp-chat:/a"})
	for i, s := range []string{
		`{"type":"chat/turnStarted","turnId":"turn","startedAt":"2026-11-01T00:00:00Z","message":{"text":"read","origin":{"kind":"user"}}}`,
		`{"type":"chat/toolCallStart","turnId":"turn","toolCallId":"call","toolName":"bash","displayName":"Bash"}`,
		`{"type":"chat/toolCallReady","turnId":"turn","toolCallId":"call","toolInput":"echo marker","invocationMessage":"bash"}`,
	} {
		if err := tr.accept(event(t, i+1, s)); err != nil {
			t.Fatal(err)
		}
	}
	name, input := tr.pending()
	if name != "bash" || input != "echo marker" {
		t.Fatalf("%q %q", name, input)
	}
	if err := tr.accept(event(t, 4, `{"type":"chat/toolCallStart","turnId":"turn","toolCallId":"call","toolName":"bash","displayName":"Bash"}`)); err == nil {
		t.Fatal("duplicate call accepted")
	}
}
