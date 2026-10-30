package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// Companion loading adds authored inputs to the canonical runner, never selects
// a provider or creates a persistent instance. Snapshot-only MCP callers stay inert.
type previewCompanions struct {
	agent  *MarkdownAgent
	digest string
	// Nonempty companion tests/tools cannot use single-file authoring/retention.
	multiFileExecution bool
}

func loadPreviewCompanions(parsed *agentfile.Agent, path string) (*agentfile.Agent, *previewCompanions, error) {
	found := false
	for _, p := range []string{MarkdownToolsPath(path), MarkdownTestsPath(path)} {
		if _, err := os.Lstat(p); err == nil {
			found = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
	}
	if !found {
		return parsed, nil, nil
	}
	abs, err := canonicalMDSource(path)
	if err != nil {
		return nil, nil, err
	}
	raw, err := mdRead(abs, markdownAgentLimit)
	if err != nil || mdHash(raw) != parsed.Revision {
		return nil, nil, agentfile.ErrRecordingChanged
	}
	a := &MarkdownAgent{Path: abs, Name: parsed.Identity.ID, Model: parsed.Model, Source: raw, Revision: parsed.Revision, Tools: slices.Clone(parsed.Tools), Mocks: map[string][]MarkdownMock{}, Lines: map[string]int{}}
	if err := loadMarkdownTools(a); err != nil {
		return nil, nil, err
	}
	a.TestsPath = MarkdownTestsPath(abs)
	copy := *parsed
	copy.Examples = slices.Clone(parsed.Examples)
	if b, e := mdRead(a.TestsPath, 128<<10); e == nil {
		if err := loadMarkdownTestFiles(a, b); err != nil {
			return nil, nil, err
		}
		seen := map[string]bool{}
		for _, ex := range copy.Examples {
			seen[ex.ID] = true
		}
		for _, c := range a.Examples {
			if seen[c.ID] {
				return nil, nil, errors.New("duplicate inline/companion test ID")
			}
			seen[c.ID] = true
			ex := agentfile.Example{ID: c.ID, Input: c.Input, Assertions: runtime.EvaluationCaseAssertions(c), Location: agentfile.Location{File: a.CasePaths[c.ID], Line: a.Lines[c.ID], Column: 1}}
			for range ex.Assertions {
				ex.AssertionLocations = append(ex.AssertionLocations, ex.Location)
			}
			if mocks := a.Mocks[c.ID]; len(mocks) > 0 {
				ex.Mocks = &agentfile.Mocks{}
				for _, m := range mocks {
					// The shared recorded-tool contract has one object-valued result.
					// Refuse incompatible fixtures rather than silently wrapping them.
					if len(m.Calls) != 1 {
						return nil, nil, errors.New("canonical recorded tests require exactly one call per tool")
					}
					raw, err := json.Marshal(m.Calls[0].Result)
					if err != nil {
						return nil, nil, err
					}
					var result map[string]any
					if json.Unmarshal(raw, &result) != nil || result == nil {
						return nil, nil, errors.New("canonical recorded tool results must be JSON objects")
					}
					ex.Mocks.Tools = append(ex.Mocks.Tools, agentfile.MockTool{Name: m.Name, Mode: "recorded", Description: m.Description, Parameters: m.Parameters, Calls: []agentfile.MockCall{{Arguments: m.Calls[0].Arguments, Result: result}}})
				}
			}
			copy.Examples = append(copy.Examples, ex)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, nil, e
	}
	if len(copy.Examples) > 32 {
		return nil, nil, errors.New("at most 32 combined examples")
	}
	if err := a.CheckCurrent(); err != nil {
		return nil, nil, err
	}
	composed := &previewCompanions{agent: a, digest: mdJSONHash(struct{ Agent, Tools, Tests string }{a.Revision, a.ToolsRevision, a.TestsRevision}), multiFileExecution: len(a.Examples) > 0 || len(a.CLITools) > 0}
	return &copy, composed, nil
}

// Resolve only tools needed live by this invocation. Replay and disabled tools
// do not need installed executables or grants. No probes or subprocesses run here.
func (p *previewCompanions) resolveTools(ctx context.Context, names []string) (*localtools.Host, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(p.agent.CLITools) == 0 {
		return nil, nil
	}
	grant, _, err := readCurrentToolGrant(p.agent.Path)
	if err != nil {
		return nil, agentpreview.ErrToolsUnavailable
	}
	if grant == nil || grant.Adapter != cliToolAdapter || grant.AgentDigest != p.agent.Revision || grant.ToolsDigest != p.agent.ToolsRevision {
		return nil, nil
	}
	var defs []api.ToolDefinition
	approved := map[string]ApprovedCLITool{}
	for _, tool := range grant.CLI {
		if !slices.Contains(names, tool.Spec.Name) {
			continue
		}
		if !slices.ContainsFunc(p.agent.CLITools, func(spec CLIToolSpec) bool {
			return spec.Name == tool.Spec.Name && mdJSONHash(spec) == mdJSONHash(tool.Spec)
		}) {
			return nil, agentpreview.ErrToolsUnavailable
		}
		if err := checkCLIIdentity(tool); err != nil {
			return nil, agentpreview.ErrToolsUnavailable
		}
		approved[tool.Spec.Name] = tool
		defs = append(defs, api.ToolDefinition{Name: tool.Spec.Name, Description: tool.Spec.Description, InputSchema: cliToolParameters(tool.Spec)})
	}
	if len(defs) == 0 {
		return nil, nil
	}
	check := func(values ...string) error {
		for _, s := range values {
			if secretshapes.Match(s) != nil {
				return agentpreview.ErrUnsafeToolContent
			}
		}
		return nil
	}
	return localtools.NewTrustedHost(defs, func(ctx context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
		tool, ok := approved[call.Tool]
		if !ok {
			return api.ToolResult{}, agentpreview.ErrToolsUnavailable
		}
		if err := p.agent.CheckCurrent(); err != nil {
			return api.ToolResult{}, err
		}
		text, err := executeCLITool(ctx, tool, call.Args)
		code := 0
		var exit *cliCommandExitError
		if err != nil {
			if !errors.As(err, &exit) {
				return api.ToolResult{}, err
			}
			code = exit.code
		}
		// A completed process is a successful host observation of an exit status,
		// not a claim that the command succeeded. Abnormal execution returns error.
		return api.ToolResult{ID: call.ID, Output: map[string]any{"exitCode": code, "succeeded": code == 0, "output": text}}, nil
	}, check)
}
