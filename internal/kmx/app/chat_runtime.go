package app

import (
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Explicit selection avoids discovery. Auto uses the ordered adapters; an
// error never falls through to another platform with a same-named Agent.
func (a *App) resolveInteractiveChat(opt ChatOptions, name string) (string, string, error) {
	if opt.Runtime == "orka" {
		namespace := opt.Namespace
		if namespace == "" {
			namespace = OrkaNamespace
		}
		return "orka", namespace, nil
	}
	ref, err := resolveRegisteredRuntime(a.operationContext(), a.chatRuntimes(), agentruntime.Target{Context: a.Cfg.KubeContext, Namespace: opt.Namespace, Name: name})
	if err != nil {
		return "", "", err
	}
	return string(ref.Runtime), ref.Namespace, nil
}
