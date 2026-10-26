package localtools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
)

func sourcesFor(mode, source string) *SourceConfig {
	return &SourceConfig{Version: 1, Tools: []ToolSource{{Name: Operation, Mode: mode, Source: source}}}
}

func TestConfigOmitsNilSettingsAndSources(t *testing.T) {
	legacy, _ := json.Marshal(Config{Instructions: "draft"})
	sourceOnly, _ := json.Marshal(Config{Instructions: "draft", Sources: sourcesFor("recorded", "recorded")})
	if string(legacy) != `{"instructions":"draft"}` || string(sourceOnly) != `{"instructions":"draft","sources":{"version":1,"tools":[{"name":"`+Operation+`","mode":"recorded","source":"recorded"}]}}` {
		t.Fatalf("encoding changed: %s / %s", legacy, sourceOnly)
	}
}

func TestSettingsAndSourcesCombineWithoutChangingDispatchCounts(t *testing.T) {
	for _, state := range []ToolState{Enabled, Disabled} {
		t.Run(string(state), func(t *testing.T) {
			settings := []ToolSetting{{Name: Operation, State: state}}
			sources := sourcesFor("recorded", "unavailable")
			config, _ := json.Marshal(Config{Instructions: "draft", ToolSettings: settings, Sources: sources})
			log, models, executions := eventlog.New(), 0, 0
			c, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				models++
				if models == 1 {
					return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "c", Tool: Operation, Args: map[string]any{}}}}}}, nil
				}
				return api.ModelResponse{Message: *api.TextMessage("assistant", "done")}, nil
			}, controller.WithSessionUID("s"), controller.WithStart(config, 0), controller.WithToolExecutor(func(_ context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
				executions++
				return api.ToolResult{ID: call.ID, Output: map[string]any{"status": "ok"}}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Exec(t.Context(), Harness{Model: "unit", ToolSettings: settings, Sources: sources}, []api.Message{*api.TextMessage("user", "input")}, 0); err != nil {
				t.Fatal(err)
			}
			trace, err := VerifyTrace(t.Context(), log.Read(1), "unit")
			want := 1
			if state == Disabled {
				want = 0 // Disabled requests are excluded; Enabled keeps the source-only count.
			}
			if err != nil || trace.ToolCalls != want || trace.CallsByName[Operation] != want || !reflect.DeepEqual(trace.Sources, sources) || models != 2 || executions != 1 || trace.Answer != "done" {
				t.Fatalf("trace=%+v err=%v models=%d executions=%d", trace, err, models, executions)
			}
		})
	}
}

func TestHarnessRejectsWrongSettingsOrSourcesBeforeModel(t *testing.T) {
	enabled := []ToolSetting{{Name: Operation, State: Enabled}}
	good := sourcesFor("recorded", "recorded")
	for name, test := range map[string]struct {
		config  Config
		harness Harness
	}{
		"wrong state":        {Config{Instructions: "d", ToolSettings: enabled, Sources: good}, Harness{ToolSettings: []ToolSetting{{Name: Operation, State: Disabled}}, Sources: good}},
		"wrong source":       {Config{Instructions: "d", ToolSettings: enabled, Sources: good}, Harness{ToolSettings: enabled, Sources: sourcesFor("recorded", "unavailable")}},
		"missing source":     {Config{Instructions: "d", ToolSettings: enabled}, Harness{ToolSettings: enabled, Sources: good}},
		"invalid live proof": {Config{Instructions: "d", ToolSettings: enabled, Sources: sourcesFor("live", "recorded")}, Harness{}},
		"source coverage":    {Config{Instructions: "d", Sources: &SourceConfig{Version: 1}}, Harness{}},
	} {
		t.Run(name, func(t *testing.T) {
			config, _ := json.Marshal(test.config)
			c, err := controller.New(eventlog.AsStore(eventlog.New()), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
				t.Fatal("rejected config reached model")
				return api.ModelResponse{}, nil
			}, controller.WithStart(config, 0))
			if err != nil {
				t.Fatal(err)
			}
			test.harness.Model = "unit"
			if c.Exec(t.Context(), test.harness, []api.Message{*api.TextMessage("user", "input")}, 0) == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}

func TestDecodeConfigIsStrictForSettingsAndSources(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate sources": `{"instructions":"d","sources":{"version":1,"tools":[]},"sources":null}`,
		"unknown field":     `{"instructions":"d","sources":{"version":1,"tools":[],"x":1}}`,
		"unknown state":     `{"instructions":"d","toolSettings":[{"name":"` + Operation + `","state":"off"}]}`,
		"unknown source":    `{"instructions":"d","sources":{"version":1,"tools":[{"name":"` + Operation + `","mode":"recorded","source":"disabled"}]}}`,
	} {
		if _, _, err := decodeConfig([]byte(raw)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
