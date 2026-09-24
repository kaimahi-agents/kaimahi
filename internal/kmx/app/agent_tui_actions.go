package app

import (
	"context"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

func (a *App) runAgentTUIAction(action agentTUIAction) (agentTUIEnvironment, error) {
	source := action.source.app(a)
	source.InvocationCommand = ""
	if action.kind == "chat" {
		if !action.source.Local {
			source.chatInference = "local"
			source.foundryClient = nil
			source.copilotCLI = ""
			source.copilotModel = ""
		}
		saved, err := loadConsoleInference(action.source, action.agent)
		if err != nil {
			return agentTUIEnvironment{}, err
		}
		if saved != nil {
			source.chatInference = saved.Kind
			if saved.Kind == "foundry" {
				client, err := newFoundryChatClient(foundryChatConfig{Endpoint: saved.Endpoint, Deployment: saved.Model, Tenant: saved.Tenant})
				if err != nil {
					return agentTUIEnvironment{}, err
				}
				source.foundryClient = client
				defer client.http.CloseIdleConnections()
			} else {
				source.copilotCLI = detectCopilotCLI()
				source.copilotModel = saved.Model
			}
		}
		return agentTUIEnvironment{}, source.ChatWithOptions(ChatOptions{Agent: action.agent.Name, Namespace: action.agent.Namespace, Runtime: action.agent.Runtime, Interactive: true})
	}
	if action.kind == "tools" {
		if action.agent.External || action.kind == "tools" && action.agent.Runtime != "orka" {
			return agentTUIEnvironment{}, fmt.Errorf("editor unavailable for this runtime")
		}
		b := &orkaChatBackend{app: source, agent: action.agent.Name, namespace: action.agent.Namespace}
		defer b.Close()
		r := newChatRenderer(source.Out)
		r.enterFullScreen()
		defer r.leaveFullScreen()
		return agentTUIEnvironment{}, b.pickTools(source.operationContext(), r)
	}
	if action.kind != "lift" || !action.agent.canLift() || !action.source.Local {
		return agentTUIEnvironment{}, fmt.Errorf("unsupported agent action")
	}
	target := action.target
	if action.create != nil {
		// The existing cluster phase owns cloud account review, confirmation,
		// provisioning records and retry advice. Run it with the terminal restored.
		worker := action.source.app(a)
		worker.InvocationCommand = ""
		if err := worker.Lift(*action.create); err != nil {
			return target, err
		}
		target = agentTUIEnvironment{Name: action.create.Cluster}
	}
	if target.Name == "" || target.Local || target.Name == action.source.Name {
		return target, fmt.Errorf("lift requires a different remote environment")
	}
	b := &orkaChatBackend{app: source, agent: action.agent.Name, namespace: action.agent.Namespace}
	defer b.Close()
	renderer := newChatRenderer(a.Out)
	renderer.enterFullScreen()
	defer renderer.leaveFullScreen()
	ctx := a.operationContext()
	chosen := chatLiftTarget{Context: target.Name, Kubeconfig: target.Kubeconfig}
	if action.create != nil {
		var err error
		chosen, err = source.agentTUICreatedTarget(ctx, *action.create)
		if err != nil {
			return target, err
		}
	}
	err := b.liftAgentTo(ctx, renderer, chosen)
	// The lift flow saves a durable kubeconfig even for newly provisioned AKS.
	if locations, loadErr := loadAgentLocations(); loadErr == nil {
		for _, l := range locations {
			if l.Context == chosen.Context && l.Agent == action.agent.Name && l.Namespace == action.agent.Namespace {
				target = agentTUIEnvironment{Name: l.Context, Kubeconfig: l.Kubeconfig}
				break
			}
		}
	}
	return target, err
}

func (a *App) agentTUICreatedTarget(ctx context.Context, opt lift.Options) (chatLiftTarget, error) {
	if err := ctx.Err(); err != nil {
		return chatLiftTarget{}, err
	}
	account, err := a.azAccount()
	if err != nil {
		return chatLiftTarget{}, err
	}
	return chatLiftTarget{Context: opt.Cluster, Subscription: account.ID, ResourceGroup: opt.ResourceGroup, Cluster: opt.Cluster, Location: opt.Location}, nil
}
