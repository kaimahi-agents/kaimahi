package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// liftVerify checks native Orka readiness. It neither makes a model call nor
// asserts telemetry arrival: this lifecycle creates no Provider and installs
// no application-specific scraping.
func (a *App) liftVerify(opt lift.Options) error {
	// OrkaStatus returns nil for absent Orka, so readiness must be checked
	// strictly before printing the view.
	if err := a.OrkaReady(); err != nil {
		return err
	}
	if err := a.OrkaStatus(); err != nil {
		return err
	}
	a.notef("Orka is installed and its controllers are ready. No model call was made:\n" +
		"  this AKS setup creates no Provider, so there is nothing yet that could answer.")
	if !opt.Observability {
		a.notef("observability is disabled; Azure metrics and log arrival were not checked.")
	} else {
		a.notef("Azure monitoring enablement is separate from telemetry arrival; metrics and log arrival were not checked.")
	}
	return nil
}

func (a *App) readLiftRecord(group, cluster string) (*lift.Record, error) {
	path, err := liftRecordPath(group, cluster)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("no AKS infrastructure record for %s/%s (looked in %s): %w", group, cluster, filepath.Dir(path), err)
	}
	defer f.Close()
	return lift.ReadRecord(f)
}

// liftNextSteps explains the owner-created model binding and ongoing costs.
func (a *App) liftNextSteps(opt lift.Options) {
	fmt.Fprintf(a.Err, `
  Orka is running on AKS. It has no Provider and no Agent yet — those are
  yours to create, because kmx holds no credential for a hosted model and
  this cluster runs no in-cluster one.

    %s      what is installed, and what it resolves

  Create a Secret you control, then an Agent against it. The Secret is read
  from stdin so the key stays out of argv and shell history:

    printf %%s "$ORKA_API_KEY" | kubectl --context %s -n %s \
        create secret generic <name> --from-file=api-key=/dev/stdin
    %s

`, a.operationCommand("orka", "status"),
		shellArg(a.Cfg.KubeContext), OrkaNamespace,
		a.operationCommand("agent", "create", "<agent>", "--namespace", OrkaNamespace,
			"--provider-type", "openai", "--model", "<model>", "--secret", "<name>",
			"--base-url", "<endpoint>"))
	if opt.Observability {
		fmt.Fprintln(a.Err, "  Azure monitoring add-ons and their owned resources were configured.\n"+
			"  Azure metrics and logs were not checked; application telemetry remains yours to configure.")
	} else {
		fmt.Fprintln(a.Err, "  Observability was disabled for this invocation. Azure metrics and logs were not checked.")
	}

	if opt.BringYourOwn {
		fmt.Fprintf(a.Err, `  Your existing cluster and registry continue to cost money; they are not
  ours to delete. Any monitoring resources recorded by this AKS setup can also
  incur charges until removed. Remove only the recorded monitoring with

    KAIMAHI_CONFIRM=%s %s

  It deletes only resources whose recorded id still names them, and leaves
  anything it cannot prove is its own, saying which. It does not remove
  Orka or your agents from your cluster.

`, shellArg(opt.Cluster), a.liftCommand(opt, true))
		return
	}
	fmt.Fprintf(a.Err, `  THIS COSTS MONEY UNTIL YOU REMOVE IT — the node, the load balancer, the
  registry, and any monitoring resources this AKS setup created. Teardown removes
  the resource group and separately handles recorded resources outside it:

    KAIMAHI_CONFIRM=%s %s

`, shellArg(opt.ResourceGroup), a.liftCommand(opt, true))
}
