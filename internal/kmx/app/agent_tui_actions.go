package app

import (
	"fmt"
	"os/exec"
)

func (a *App) runAgentTUIAction(action agentTUIAction) (agentTUIEnvironment, error) {
	if action.agent.Runtime == "md" {
		if action.kind == "edit" || action.kind == "edit-tests" || action.kind == "edit-tools" {
			path := action.agent.DefinitionPath
			if action.kind == "edit-tests" {
				path = action.agent.TestsPath
			}
			if action.kind == "edit-tools" {
				path = MarkdownToolsPath(action.agent.DefinitionPath)
			}
			argv, err := markdownEditor()
			if err != nil {
				return agentTUIEnvironment{}, err
			}
			cmd := exec.CommandContext(a.operationContext(), argv[0], append(argv[1:], path)...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = a.Stdin, a.Out, a.Err
			return agentTUIEnvironment{}, cmd.Run()
		}
		if action.kind == "chat" {
			if instance, e := FindAgentInstance(action.agent.DefinitionPath); e == nil && instance.State == "running" && instance.Socket != "" {
				s, agent, err := NewAttachedAgentSession(a.operationContext(), instance.ID, a.Out, a.Err)
				if err != nil {
					return agentTUIEnvironment{}, err
				}
				defer s.Close()
				return agentTUIEnvironment{}, s.ChatTUI(a.operationContext(), agent, a.Stdin)
			}
		}
		agent, err := ReadMarkdownAgent(action.agent.DefinitionPath)
		if err != nil {
			return agentTUIEnvironment{}, err
		}
		accepted, err := SetupMarkdownProvider(a.Stdin, a.Err, false)
		if err != nil {
			return agentTUIEnvironment{}, err
		}
		s := NewMarkdownSession(a.Out, a.Err)
		defer s.Close()
		if accepted {
			s.Provider = "copilot"
		}
		if action.kind == "update" {
			plan, err := s.PlanAgentUpdate(a.operationContext(), agent)
			if err != nil {
				return agentTUIEnvironment{}, err
			}
			if err := ConfirmAgentUpdate(a.operationContext(), a.Err, a.Stdin, plan, false); err != nil {
				return agentTUIEnvironment{}, err
			}
			record, err := s.ApplyAgentUpdate(a.operationContext(), plan)
			if err != nil {
				return agentTUIEnvironment{}, err
			}
			_, err = fmt.Fprintf(a.Out, "Updated/unchanged %s · version %s\n", record.Name, record.Version)
			return agentTUIEnvironment{}, err
		}
		if action.kind == "test" {
			s.InitialTest = true
		}
		return agentTUIEnvironment{}, s.ChatTUI(a.operationContext(), agent, a.Stdin)
	}
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
		return agentTUIEnvironment{}, source.ChatWithOptions(ChatOptions{Agent: action.agent.Name, Namespace: action.agent.Namespace, Runtime: "orka"})
	}
	if action.kind == "tools" {
		if action.agent.External {
			return agentTUIEnvironment{}, fmt.Errorf("editor unavailable for this runtime")
		}
		return agentTUIEnvironment{}, fmt.Errorf("%s", agentMutationRetirement)
	}
	return agentTUIEnvironment{}, fmt.Errorf("unsupported agent action")
}
