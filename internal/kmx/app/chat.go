package app

import "fmt"

// ChatOptions selects the Orka Agent a chat session is opened against.
type ChatOptions struct {
	Agent          string
	Task           string
	Verbose        bool
	Runtime        string
	Namespace      string
	AzureDiscovery string
	Bundles        string
}

// ChatWithOptions opens the interactive Orka session. Task, when set, is
// sent as the first turn.
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
	a.bundleRoot = opt.Bundles
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
	// Orka chat is always a session: a Task is created, polled for its own
	// result over a single connection, and never resubmitted. An initial
	// message is the first turn of that session, not a one-shot; the one-shot
	// Task is kmx agent run.
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
