package app

import (
	"fmt"
)

func (a *App) runAgentTUIAction(action agentTUIAction, requirement *orkaTaskEnforcement) (agentTUIEnvironment, error) {
	if action.kind == "lift" {
		return agentTUIEnvironment{}, fmt.Errorf("%s", interactiveLiftRetirement)
	}
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
		// Console agents are native Orka Agents; naming the runtime explicitly
		// keeps chat off the discovery path rather than re-resolving the agent.
		return agentTUIEnvironment{}, source.chatWithPreparedEnforcement(ChatOptions{Agent: action.agent.Name, Namespace: action.agent.Namespace, Runtime: "orka"}, requirement)
	}
	if action.kind == "tools" {
		if action.agent.External {
			return agentTUIEnvironment{}, fmt.Errorf("editor unavailable for this runtime")
		}
		return agentTUIEnvironment{}, fmt.Errorf("%s", agentMutationRetirement)
	}
	return agentTUIEnvironment{}, fmt.Errorf("unsupported agent action")
}
