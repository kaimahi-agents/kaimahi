package localtools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
)

func TestLegacyV1ConfigAndModelHash(t *testing.T) {
	config, err := json.Marshal(Config{Instructions: "draft"})
	if err != nil || string(config) != `{"instructions":"draft"}` {
		t.Fatalf("legacy config changed: %s / %v", config, err)
	}
	log := eventlog.New()
	sends := 0
	c, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		sends++
		return api.ModelResponse{Message: *api.TextMessage("assistant", "done")}, nil
	}, controller.WithStart(config, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Exec(t.Context(), Harness{Model: "unit"}, []api.Message{*api.TextMessage("user", "input")}, 0); err != nil {
		t.Fatal(err)
	}
	records := log.Read(1)
	// Hash of the original v1 ModelRequest, including the fixed read definition.
	const original = "2d2f8a3e590a2d82f8be9e568de7acd32e3c195a855ae025eaedf0264d0c6fff"
	if len(records) != 5 || records[2].Event.ModelCall.InputHash != original {
		t.Fatal("legacy model request/hash changed")
	}
	trace, err := VerifyTrace(t.Context(), records, "unit")
	if err != nil || trace.Answer != "done" || trace.ToolCalls != 0 || sends != 1 {
		t.Fatalf("legacy replay failed: %+v / %v", trace, err)
	}
}
