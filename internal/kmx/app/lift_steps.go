package app

import "github.com/kaimahi-agents/kaimahi/internal/kmx/lift"

// aimAtTheCluster points every later read and write at the managed cluster.
//
// kmx passes an explicit --context on every kubectl and helm invocation, so
// this one assignment is what makes the reused command implementations act on
// AKS instead of on kind. It is set from the cluster name because that is
// what `az aks get-credentials` writes the context as, and it is set once,
// centrally, rather than being threaded through every call — a path that
// resolved the context per command would eventually have a command that
// forgot, and that command would silently act on the operator's local
// cluster.
func (a *App) aimAtTheCluster(opt lift.Options) {
	a.Cfg.KubeContext = opt.Cluster
	a.Cfg.ContextSource = "the cluster named on the command line"
}

// liftCredentials writes the kubeconfig entry for the cluster. Run on both
// branches: on a created cluster aks-up.sh has already done it, and doing it
// again is how a resumed --step gets a context without re-running the create.
func (a *App) liftCredentials(opt lift.Options) error {
	return a.Run.Run("az", "aks", "get-credentials", "--name", opt.Cluster,
		"--resource-group", opt.ResourceGroup, "--overwrite-existing", "--output", "none")
}

// liftOrka reuses the pinned native installer and its readiness waits.
// The Provider stays owner-created: this path installs no model server and
// must not invent a Provider that either carries our credential or resolves
// nothing.
func (a *App) liftOrka(opt lift.Options) error {
	resume := opt
	resume.Step = "orka"
	if err := a.Guard("install Orka", a.liftCommand(resume, false)); err != nil {
		return err
	}
	if err := a.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		return err
	}
	a.notef("\nNOTE  No Provider was created, because this cluster has no in-cluster model\n" +
		"      server and kmx holds no credential for a hosted one. Orka refuses every\n" +
		"      model call until one exists. Create it with a Secret you control:")
	// Name the cluster explicitly: aiming this process does not change the
	// operator's current-context. Read the key from stdin to keep it out of
	// argv and shell history.
	a.notef("  printf %%s \"$ORKA_API_KEY\" | kubectl --context %s -n %s \\\n"+
		"      create secret generic <name> --from-file=api-key=/dev/stdin",
		shellArg(a.Cfg.KubeContext), OrkaNamespace)
	a.notef("  %s", a.operationCommand("agent", "create", "<agent>",
		"--namespace", OrkaNamespace, "--provider-type", "openai",
		"--model", "<model>", "--secret", "<name>", "--base-url", "<endpoint>"))
	a.notef("\n  printf keeps the trailing newline out of the Secret; a newline there\n" +
		"  corrupts the Authorization header on every request.")
	return nil
}
