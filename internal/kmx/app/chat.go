package app

import (
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// ChatOptions selects the Orka Agent a chat session is opened against.
type ChatOptions struct {
	Agent          string
	Task           string
	Interactive    bool
	Verbose        bool
	Runtime        string
	Namespace      string
	AzureDiscovery string
}

// ChatWithOptions opens the interactive Orka session.
//
// Unguarded, like every other read-shaped command here. Calling it
// "read-only" would be wrong — it spends budget and writes a ledger row. It
// is unguarded because the line being drawn is not "mutates" but "can be
// aimed somewhere unintended": chat runs through kubectl carrying an explicit
// --context, so it lands wherever the rest of the invocation was already
// going to land. Prompting on the most-used command would buy nothing and
// teach people to type past confirmations.
func (a *App) ChatWithOptions(opt ChatOptions) error {
	a.chatVerbose = opt.Verbose
	if opt.AzureDiscovery != "" {
		a.azureDiscoveryMode = opt.AzureDiscovery
	}
	if err := checkChatRuntime(opt.Runtime); err != nil {
		return err
	}
	if opt.AzureDiscovery != "" && opt.AzureDiscovery != "cli" && opt.AzureDiscovery != "sdk" {
		return fmt.Errorf("unknown Azure discovery %q; use cli or sdk", opt.AzureDiscovery)
	}
	agent := opt.Agent
	if agent == "" {
		agent = config.DefaultAgent
	}
	// Orka chat is a session, not an invocation: a Task is created, polled
	// for its own result over a single connection, and never resubmitted.
	// There is no one-shot transport left to fall back to, so say which
	// command does work rather than dialing a controller that is gone.
	if !opt.Interactive {
		return fmt.Errorf("Orka chat requires --interactive:\n  %s",
			a.operationCommand("agent", "chat", "--interactive", "--namespace",
				valueOr(opt.Namespace, OrkaNamespace), agent))
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	runtime, namespace, err := a.resolveInteractiveChat(opt, agent)
	if err != nil {
		return err
	}
	opt.Runtime = runtime
	return a.openRuntimeChat(opt, agent, namespace)
}

// checkChatRuntime answers an explicit --runtime before anything reaches a
// cluster.
//
// The retired runtime is refused BY NAME rather than resolved to Orka. A
// caller who named it asked for a different platform; answering from Orka
// instead would answer a question nobody put, against an agent that is not
// the one they meant. That one word is why it appears below at all.
func checkChatRuntime(name string) error {
	switch name {
	case "", "auto", "orka":
		return nil
	case "kagent":
		return fmt.Errorf("--runtime kagent is not supported: that runtime has been removed from kmx.\n" +
			"  kmx chats with Orka Agents — drop the flag, or say --runtime orka explicitly.")
	default:
		return fmt.Errorf("unknown chat runtime %q; use auto or orka", name)
	}
}
