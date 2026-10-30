package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

const markdownAdapterVersion = "copilot-sdk-v1.0.24/native-agent-v1"

type markdownAnswer struct {
	Text, Model string
	Calls       map[string]int
	MockError   string
}
type markdownExecutor interface {
	Start(context.Context) (string, error)
	Answer(context.Context, *MarkdownAgent, string, []MarkdownMock) (markdownAnswer, error)
	Reset()
	Close()
}
type markdownCopilot struct {
	client       *copilot.Client
	session      *copilot.Session
	dir, version string
	progress     func(string)
	borrowed     bool         // shared manager owns the client process and temporary directory
	sessionID    atomic.Value // string, readable while a turn is executing
	toolBudget   atomic.Int32
}

func (c *markdownCopilot) SessionIdentity() string {
	v := c.sessionID.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

func (c *markdownCopilot) Start(ctx context.Context) (string, error) {
	if c.client != nil {
		return c.version, nil
	}
	exe := detectCopilotCLI()
	if exe == "" {
		return "", errors.New("Copilot CLI missing; install it and run copilot login")
	}
	dir, err := os.MkdirTemp("", "kmx-md-copilot-")
	if err != nil {
		return "", err
	}
	c.dir = dir
	c.client = copilot.NewClient(&copilot.ClientOptions{Connection: copilot.StdioConnection{Path: exe, Args: []string{"--no-auto-update", "--disable-builtin-mcps", "--no-custom-instructions"}}, WorkingDirectory: dir, LogLevel: "none"})
	probe, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err = c.client.Start(probe); err != nil {
		c.Close()
		return "", errors.New("Copilot SDK connection failed; check copilot --version")
	}
	auth, err := c.client.GetAuthStatus(probe)
	if err != nil || !auth.IsAuthenticated {
		c.Close()
		return "", errors.New("Copilot is not authenticated; run copilot login")
	}
	status, err := c.client.GetStatus(probe)
	if err != nil {
		c.Close()
		return "", errors.New("cannot read Copilot runtime version")
	}
	c.version = status.Version
	return c.version, nil
}
func (c *markdownCopilot) Reset() {
	c.sessionID.Store("")
	if c.session != nil {
		_ = c.session.Disconnect()
		c.session = nil
	}
}
func (c *markdownCopilot) Close() {
	c.Reset()
	if c.borrowed {
		return
	}
	if c.client != nil {
		_ = c.client.Stop()
		c.client = nil
	}
	if c.dir != "" {
		_ = os.RemoveAll(c.dir)
		c.dir = ""
	}
}

type markdownMockDispatch struct {
	mu      sync.Mutex
	counts  map[string]int
	failure string
}

func (d *markdownMockDispatch) handler(m MarkdownMock) copilot.ToolHandler {
	return func(i copilot.ToolInvocation) (copilot.ToolResult, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		n := d.counts[m.Name]
		d.counts[m.Name]++
		if n >= len(m.Calls) || mdJSONHash(i.Arguments) != mdJSONHash(m.Calls[n].Arguments) {
			d.failure = "unexpected arguments or extra call to " + m.Name
			return copilot.ToolResult{TextResultForLLM: "Mock expectation mismatch", ResultType: "failure"}, nil
		}
		b, _ := json.Marshal(m.Calls[n].Result)
		return copilot.ToolResult{TextResultForLLM: string(b), ResultType: "success"}, nil
	}
}
func (c *markdownCopilot) Answer(ctx context.Context, a *MarkdownAgent, input string, mocks []MarkdownMock) (markdownAnswer, error) {
	result := markdownAnswer{Calls: map[string]int{}}
	c.toolBudget.Store(8)
	if _, err := c.Start(ctx); err != nil {
		return result, err
	}
	if len(a.Tools) > 0 && len(mocks) == 0 && a.Grant == nil {
		return result, errors.New("agent requires tools; select a mocked test case (real tools are not configured)")
	}
	dispatch := &markdownMockDispatch{counts: map[string]int{}}
	if mocks != nil {
		c.Reset()
	}
	if c.session == nil {
		tools := []copilot.Tool{}
		available := []string{"kmx_no_tools"}
		if len(mocks) > 0 {
			available = []string{}
			for _, m := range mocks {
				available = append(available, m.Name)
				tools = append(tools, copilot.Tool{Name: m.Name, Description: m.Description, Parameters: m.Parameters, SkipPermission: true, Handler: dispatch.handler(m)})
			}
		}
		declaredTools := a.Tools
		if len(mocks) > 0 {
			declaredTools = available
		}
		if mocks == nil && a.Grant != nil {
			if validateToolGrant(*a.Grant) != nil || a.Grant.AgentDigest != a.Revision || mdJSONHash(*a.Grant) != a.BindingDigest {
				return result, errors.New("invalid frozen tool approval")
			}
			if a.Grant.Adapter == cliToolAdapter {
				tools = cliReadTools(*a.Grant, &c.toolBudget)
			} else {
				tools = githubReadTools(*a.Grant, &c.toolBudget)
			}
			available = append([]string{}, a.Grant.Tools...)
			declaredTools = available
		}
		model := a.Model
		if model != "" && model != "auto" {
			catalog, err := c.client.ListModels(ctx)
			if err != nil {
				return result, errors.New("cannot discover Copilot models")
			}
			found := false
			for _, entry := range catalog {
				if entry.ID == model {
					found = true
				}
			}
			if !found {
				return result, errors.New("selected Copilot model is unavailable; choose an available model explicitly")
			}
		}
		cfg := &copilot.SessionConfig{WorkingDirectory: c.dir, Model: model, EnableConfigDiscovery: copilot.Bool(false), EnableFileHooks: copilot.Bool(false), EnableSkills: copilot.Bool(false), EnableHostGitOperations: copilot.Bool(false), EnableSessionStore: copilot.Bool(false), SkipCustomInstructions: copilot.Bool(true), AvailableTools: available, Tools: tools, Agent: a.Name, CustomAgents: []copilot.CustomAgentConfig{{Name: a.Name, Description: a.Description, Prompt: a.Instructions, Tools: a.Tools, Model: model}}, OnPermissionRequest: func(copilot.PermissionRequest, copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
			return &rpc.PermissionDecisionDeniedInteractivelyByUser{}, nil
		}}
		var err error
		cfg.CustomAgents[0].Tools = declaredTools
		if model != "" && model != "auto" {
			cfg.CustomAgents[0].ModelPolicy = "required"
		}
		c.session, err = c.client.CreateSession(ctx, cfg)
		if err != nil {
			return result, errors.New("Copilot could not select the agent/model; check the installed runtime and model access")
		}
		c.sessionID.Store(c.session.SessionID)
	}
	turn, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if progress := c.progress; progress != nil {
		progress("Waiting for Copilot · tool-free chat / fixture-only tests · 5m turn limit")
		unsubscribe := c.session.On(func(event copilot.SessionEvent) {
			switch event.Data.(type) {
			case *copilot.AssistantTurnStartData:
				progress("Copilot turn started; waiting for the final answer")
			case *copilot.AssistantMessageData:
				progress("Copilot produced a message; waiting for turn completion")
			case *copilot.ToolExecutionStartData:
				progress("Copilot is processing a registered test tool")
			case *copilot.SessionErrorData:
				progress("Copilot reported an execution error")
			}
		})
		defer unsubscribe()
	}
	event, err := c.session.SendAndWait(turn, copilot.MessageOptions{Prompt: input})
	if err != nil {
		abort, stop := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.session.Abort(abort)
		stop()
		c.Reset()
		if turn.Err() != nil {
			return result, turn.Err()
		}
		return result, errors.New("Copilot turn failed; no retry was sent")
	}
	if event == nil {
		return result, errors.New("Copilot returned no terminal answer")
	}
	message, ok := event.Data.(*copilot.AssistantMessageData)
	if !ok || len(message.ToolRequests) > 0 || len(message.Content) > 64<<10 {
		return result, errors.New("Copilot answer incomplete or exceeds 64 KiB")
	}
	result.Text = message.Content
	if message.Model != nil {
		result.Model = *message.Model
	}
	dispatch.mu.Lock()
	defer dispatch.mu.Unlock()
	for k, v := range dispatch.counts {
		result.Calls[k] = v
	}
	result.MockError = dispatch.failure
	for _, m := range mocks {
		if dispatch.counts[m.Name] != len(m.Calls) {
			result.MockError = fmt.Sprintf("%s: expected %d calls, got %d", m.Name, len(m.Calls), dispatch.counts[m.Name])
		}
	}
	return result, nil
}
