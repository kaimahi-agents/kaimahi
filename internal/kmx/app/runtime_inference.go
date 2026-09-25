package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Host execution consumes a resolved definition and an injected tool executor.
// It knows neither Kubernetes identity nor Orka resource discovery. A future
// platform can supply its own resolution/executor without changing model code.
type hostTurnDefinition struct {
	Instructions string
	Tools        []copilotTool
	Unavailable  []string
}
type hostToolExecutor func(context.Context, copilotTool, []byte) ([]byte, error)
type hostInference interface {
	Run(context.Context, hostTurnDefinition, string, hostToolExecutor, *chatRenderer, string) (string, error)
}
type copilotHostInference struct{ executable, model string }

func (p copilotHostInference) Run(ctx context.Context, definition hostTurnDefinition, message string, execute hostToolExecutor, r *chatRenderer, agent string) (string, error) {
	return copilotToolLoop(ctx, definition.Instructions, message, definition.Tools, func(ctx context.Context, prompt string) (string, error) {
		return copilotPromptModel(ctx, p.executable, p.model, prompt)
	}, func(ctx context.Context, tool copilotTool, args []byte) (string, error) {
		r.assistantOperation(agent, "TOOL CALL", tool.Name, colorBlue, "Executing registered HTTP tool")
		raw, err := execute(ctx, tool, args)
		if err != nil {
			return "", fmt.Errorf("tool %s failed; no automatic retry: %w", tool.Name, err)
		}
		if len(raw) > 32<<10 {
			return "", fmt.Errorf("tool result exceeds 32 KiB")
		}
		return string(raw), nil
	})
}

type foundryHostInference struct{ client *foundryChatClient }

func (p foundryHostInference) Run(ctx context.Context, definition hostTurnDefinition, message string, execute hostToolExecutor, r *chatRenderer, agent string) (string, error) {
	return runFoundryTurn(ctx, p.client, definition.Instructions, message, definition.Tools, execute, r, agent)
}

func (a *App) hostInferenceStrategy() (hostInference, bool, error) {
	switch a.chatInference {
	case "", "local":
		return nil, false, nil
	case "copilot":
		return copilotHostInference{executable: a.copilotCLI, model: a.copilotModel}, true, nil
	case "foundry":
		if a.foundryClient == nil {
			return nil, true, fmt.Errorf("configure Foundry with /inference-foundry")
		}
		return foundryHostInference{client: a.foundryClient}, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported inference source %q", a.chatInference)
	}
}

// This method is the Orka-specific resolver only. Model execution is delegated
// to a strategy that does not read Agent resources itself.
func (b *orkaChatBackend) sendHostTurn(ctx context.Context, message string, r *chatRenderer, inference hostInference) error {
	if err := b.app.requireLocalHostInference(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	raw, err := b.app.orkaCapture(ctx, nil, "-n", b.namespace, "get", "agents.core.orka.ai", b.agent, "-o", "json")
	if err != nil {
		return err
	}
	instructions, err := b.copilotInstructionsFromAgent(ctx, raw)
	if err != nil {
		return err
	}
	tools, unavailable, err := b.copilotToolsFromAgent(ctx, raw)
	if err != nil {
		return err
	}
	if len(unavailable) > 0 {
		r.assistantOperation(b.agent, "TOOLS", "", colorYellow, "Unavailable in host inference: "+strings.Join(unavailable, ", "))
	}
	answer, err := inference.Run(ctx, hostTurnDefinition{Instructions: instructions, Tools: tools, Unavailable: unavailable}, message, b.executeCopilotTool, r, b.agent)
	if err != nil {
		return err
	}
	r.assistant(b.agent, answer, true)
	return nil
}
