package app

import (
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

// Ctx selects the context every later kmx command lands on.
//
// Selecting is not itself a mutation, but it decides where every mutation
// goes, so it runs the same guard: name AND API-server address must agree
// for "local kind", anything else needs a confirmation naming the context,
// and a non-interactive shell with no KAIMAHI_CONFIRM refuses rather than
// records a choice nobody made.
func (a *App) Ctx(context string) error {
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if context == "" {
		return a.showCtx()
	}
	kubeconfig, err := a.kubeconfig()
	if err != nil {
		return err
	}
	if err := guard.Check(kubeconfig, guard.Request{
		Action:     "select " + context + " as the context kmx acts on",
		Context:    context,
		Source:     config.SourceSelected,
		Namespaces: config.GuardNamespaces,
		Confirm:    a.Cfg.Confirm,
		Command:    "kmx ctx " + context,
	}, a.Err, a.Stdin); err != nil {
		return err
	}
	path, err := config.WriteSelectedContext(context)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "kmx will act on context %q (recorded in %s).\n", context, path)
	fmt.Fprintf(a.Err, "KUBE_CTX in the environment, or --context, still overrides this for one command.\n")
	return nil
}

// showCtx reports the resolved context and its posture without changing
// anything.
func (a *App) showCtx() error {
	kubeconfig, err := a.kubeconfig()
	if err != nil {
		return err
	}
	posture, err := guard.Classify(kubeconfig, a.Cfg.KubeContext)
	if err != nil {
		return fmt.Errorf("kube-guard: %w", err)
	}
	host := posture.Host
	if host == "" {
		host = "<not created yet>"
	}
	fmt.Fprintf(a.Out, "context: %s\nsource:  %s\nserver:  %s\nposture: %s\n",
		posture.Context, a.Cfg.ContextSource, host, posture.Label)
	return nil
}
