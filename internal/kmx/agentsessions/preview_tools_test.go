package agentsessions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestToolPreviewProofBindsSubmittedInstructionsAndInput(t *testing.T) {
	config, _ := json.Marshal(map[string]string{"instructions": "actual instructions"})
	log := eventlog.New()
	model := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", "Draft")}, nil
	}
	live, err := controller.New(eventlog.AsStore(log), model, controller.WithStart(config, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err = live.Exec(t.Context(), localtools.Harness{Model: "unit"}, []api.Message{*api.TextMessage("user", "actual input")}, 0); err != nil {
		t.Fatal(err)
	}
	records := log.Read(1)
	for _, tc := range []struct {
		name   string
		config []byte
		input  string
		valid  bool
	}{
		{"matching request", config, "actual input", true},
		{"different input", config, "different input", false},
		{"different instructions", []byte(`{"instructions":"different instructions"}`), "actual input", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof, err := verifyToolPreviewRequest(t.Context(), records, "unit", tc.config, tc.input)
			if (err == nil) != tc.valid {
				t.Fatalf("request binding: %+v %v", proof, err)
			}
		})
	}
}

func TestToolInspectionPreservesCancellationAndIncompleteState(t *testing.T) {
	initial := PreviewSession{Outcome: "incomplete", Model: "unit"}
	for _, deadline := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		expected := context.Canceled
		if deadline {
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			expected = context.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(t.Context())
			cancel()
		}
		out, err := inspectToolPreview(ctx, initial, nil)
		cancel()
		if !errors.Is(err, expected) || out.Outcome != "incomplete" {
			t.Fatalf("context swallowed: %+v %v", out, err)
		}
	}
	if out, err := inspectToolPreview(t.Context(), initial, nil); err != nil || out.Outcome != "incomplete" {
		t.Fatal("invalid trace should remain incomplete", out, err)
	}
}
