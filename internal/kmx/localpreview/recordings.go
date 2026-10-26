package localpreview

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type toolPlan struct {
	definitions []api.ToolDefinition
	sources     *localtools.SourceConfig
	settings    []localtools.ToolSetting
	execute     controller.ToolFunc
}

func toolSetting(ex agentfile.Example, name string) agentfile.MockTool {
	if ex.Mocks != nil {
		for _, tool := range ex.Mocks.Tools {
			if tool.Name == name {
				return tool
			}
		}
	}
	return agentfile.MockTool{Name: name, Mode: "live"}
}

func neededLiveTools(names []string, examples []agentfile.Example, adHoc bool) []string {
	var needed []string
	for _, name := range names {
		live := adHoc
		for _, ex := range examples {
			tool := toolSetting(ex, name)
			live = live || len(tool.Calls) == 0
		}
		if live {
			needed = append(needed, name)
		}
	}
	return needed
}

func (r *runner) resolveTools(ctx context.Context, request agentpreview.Request, names []string) (*localtools.Host, error) {
	var host *localtools.Host
	var err error
	if request.ToolBindings != "" {
		binding, e := localtools.Load(request.ToolBindings)
		if e != nil || len(request.Agent.Tools) == 0 {
			return nil, agentpreview.ErrToolsUnavailable
		}
		if slices.Contains(names, localtools.Operation) {
			if r.options.Tools == nil {
				return nil, agentpreview.ErrToolsUnavailable
			}
			host, err = r.options.Tools(ctx, binding)
			if err == nil && (host == nil || !host.Matches([]string{localtools.Operation})) {
				return nil, agentpreview.ErrToolsUnavailable
			}
		}
	} else if len(names) > 0 {
		if r.options.ResolveAvailableTools != nil {
			host, err = r.options.ResolveAvailableTools(ctx, slices.Clone(names))
		} else if r.options.ResolveTools != nil {
			host, err = r.options.ResolveTools(ctx, slices.Clone(names))
			if err == nil && (host == nil || !host.Matches(names)) {
				return nil, agentpreview.ErrToolsUnavailable
			}
		}
	}
	if err != nil {
		return nil, agentpreview.ErrToolsUnavailable
	}
	if host != nil {
		defs := host.Definitions()
		if defs == nil {
			defs = []api.ToolDefinition{localtools.Definition()}
		}
		for _, def := range defs {
			if !slices.Contains(names, def.Name) {
				return nil, agentpreview.ErrToolsUnavailable
			}
		}
	}
	return host, nil
}

// Fixture definitions stay on this dispatch path, never in a trusted live Host.
func makeToolPlan(names []string, ex agentfile.Example, host *localtools.Host, check func(...string) error, validate func(context.Context) error, settings []localtools.ToolSetting) (toolPlan, error) {
	p := toolPlan{sources: &localtools.SourceConfig{Version: 1}}
	if len(names) == 0 {
		return p, nil
	}
	var live []api.ToolDefinition
	if host != nil {
		live = host.Definitions()
		if live == nil {
			live = []api.ToolDefinition{localtools.Definition()}
		}
	}
	fixtures := map[string]agentfile.MockTool{}
	for _, name := range names {
		if !localtools.ValidToolName(name) {
			return p, agentpreview.ErrToolsUnavailable
		}
		tool := toolSetting(ex, name)
		fixtures[name] = tool
		def := api.ToolDefinition{Name: name, Description: "Not available in this test.", InputSchema: map[string]any{"type": "object"}}
		source := "unavailable"
		if tool.Mode == "recorded" && len(tool.Calls) > 0 {
			source = "recorded"
			def = api.ToolDefinition{Name: name, Description: tool.Description, InputSchema: tool.Parameters}
		} else {
			for _, d := range live {
				if d.Name == name {
					def = d
					source = "live"
				}
			}
		}
		p.definitions = append(p.definitions, def)
		p.sources.Tools = append(p.sources.Tools, localtools.ToolSource{Name: name, Mode: tool.Mode, Source: source})
	}
	var err error
	p.definitions, err = localtools.CopyDefinitions(p.definitions)
	if err != nil || p.sources.Validate(p.definitions) != nil {
		return p, agentpreview.ErrToolsUnavailable
	}
	p.settings, err = localtools.CopySettings(settings, p.definitions)
	if err != nil {
		return p, agentpreview.ErrToolsUnavailable
	}
	if localtools.CheckJSON(ex.Mocks, check) != nil || localtools.CheckJSON(localtools.Config{Definitions: p.definitions, ToolSettings: p.settings, Sources: p.sources}, check) != nil {
		return p, agentpreview.ErrUnsafeToolContent
	}
	p.execute = func(ctx context.Context, scope controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
		if err := validate(ctx); err != nil {
			return api.ToolResult{}, err
		}
		if localtools.ValidateArguments(p.definitions, call.Tool, call.Args) != nil || localtools.CheckJSON(call, check) != nil {
			return api.ToolResult{}, agentpreview.ErrUnsafeToolContent
		}
		result := api.ToolResult{ID: call.ID, Output: map[string]any{"text": "not available in this test"}}
		if localtools.IsDisabled(p.settings, call.Tool) {
			result = localtools.DisabledResult(call.ID)
		} else {
			for _, source := range p.sources.Tools {
				if source.Name == call.Tool {
					switch source.Source {
					case "live":
						var err error
						result, err = host.Execute(ctx, scope, call)
						if err != nil {
							return api.ToolResult{}, err
						}
					case "recorded":
						saved := fixtures[call.Tool].Calls[0]
						args := call.Args
						if args == nil {
							args = map[string]any{}
						}
						actual, _ := json.Marshal(args)
						expected, _ := json.Marshal(saved.Arguments)
						if string(actual) != string(expected) {
							return api.ToolResult{}, agentpreview.ErrRecordingMismatch
						}
						raw, _ := json.Marshal(saved.Result)
						result.Output = nil // Decode into a fresh map, not the unavailable stub's map.
						if len(raw) > 32*1024 || !runtime.JSONNumbersExact(raw) || json.Unmarshal(raw, &result.Output) != nil {
							return api.ToolResult{}, agentpreview.ErrUnsafeToolContent
						}
					}
				}
			}
		}
		if err := validate(ctx); err != nil {
			return api.ToolResult{}, err
		}
		if localtools.CheckJSON(result, check) != nil {
			return api.ToolResult{}, agentpreview.ErrUnsafeToolContent
		}
		return result, nil
	}
	return p, nil
}

func (p toolPlan) summaries(counts map[string]int) []agentpreview.ToolSummary {
	var rows []agentpreview.ToolSummary
	if p.sources != nil {
		for _, tool := range p.sources.Tools {
			rows = append(rows, agentpreview.ToolSummary{Name: tool.Name, Mode: tool.Mode, Source: tool.Source, CallCount: counts[tool.Name]})
		}
	}
	return rows
}
