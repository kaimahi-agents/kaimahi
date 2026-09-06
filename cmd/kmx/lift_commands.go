package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// newLiftCommand is the managed-cluster path: the same agent you just ran
// locally, running on AKS, with Azure-managed monitoring already wired.
//
// It is a sibling of `quickstart` rather than a flag on `up`, because it is a
// different journey with different consequences. `up` builds a local cluster
// that costs nothing and is deleted by removing a container; this one acts on
// a cloud subscription, bills money for as long as it exists, and has a
// teardown that has to be run. A flag would put those two behind the same
// word.
//
// Two branches, and which one is running is always explicit. `--byo` lifts
// onto a cluster that already exists; without it, the cluster is created here
// along with everything around it. The branch is never inferred from whether
// a cluster happens to be there: the two have opposite teardown rules, and a
// typo in a cluster name must not be what decides which of them applies.
func newLiftCommand(state *commandState) *cobra.Command {
	var opt lift.Options
	cmd := &cobra.Command{
		Use:   "lift",
		Short: "Run the same agent on AKS, with Azure-managed observability",
		Args:  cobra.NoArgs,
	}
	registerLiftIdentityFlags(cmd, &opt)
	cmd.Flags().StringVar(&opt.Location, "location", "", "Azure region (default "+app.DefaultLocation+")")
	cmd.Flags().StringVar(&opt.NodeSize, "node-size", "", "node VM size (default "+app.DefaultNodeSize+")")
	cmd.Flags().IntVar(&opt.NodeCount, "node-count", 0, "how many nodes (default 1)")
	cmd.Flags().StringVar(&opt.NetworkPolicy, "network-policy", "", "NetworkPolicy engine: cilium (default), azure, calico")
	cmd.Flags().BoolVar(&opt.Observability, "observability", true, "wire Azure Monitor and Container Insights")
	cmd.Flags().StringVar(&opt.Step, "step", "", "run one phase: "+strings.Join(lift.Steps, "|"))
	cmd.Flags().BoolVar(&opt.Plan, "plan", false, "print what would be created, where, and stop")
	_ = cmd.RegisterFlagCompletionFunc("step", staticCompletion(lift.Steps))
	_ = cmd.RegisterFlagCompletionFunc("network-policy", staticCompletion([]string{"cilium", "azure", "calico"}))
	cmd.RunE = appRun(state, func(a *app.App) error {
		// An engine set to the empty string is not the same as one left
		// unset. Unset takes the default that enforces; explicitly empty is
		// the AKS default that does not, and it has to reach its own refusal
		// rather than being quietly replaced by something that works.
		opt.NetworkPolicySet = cmd.Flags().Changed("network-policy")
		return a.Lift(opt)
	})

	cmd.AddCommand(newLiftDownCommand(state))
	return cmd
}

// newLiftDownCommand removes what the lift created — and what that means
// depends entirely on which branch created it, which is why `--byo` is
// required here too rather than remembered silently. The run record says
// which branch it was, and a mismatch is refused rather than reconciled: the
// two have opposite rules about the cluster and its resource group.
func newLiftDownCommand(state *commandState) *cobra.Command {
	var opt lift.Options
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove what the lift created (and on a cluster you own, only that)",
		Args:  cobra.NoArgs,
	}
	registerLiftIdentityFlags(cmd, &opt)
	cmd.RunE = appRun(state, func(a *app.App) error { return a.LiftDown(opt) })
	return cmd
}

// registerLiftIdentityFlags declares the three flags that say WHICH lift is
// meant. They are identical on both commands on purpose: teardown has to name
// the same thing the lift named, and a shorthand that guessed one of them
// from context would be guessing about a cloud subscription.
func registerLiftIdentityFlags(cmd *cobra.Command, opt *lift.Options) {
	cmd.Flags().BoolVar(&opt.BringYourOwn, "byo", false, "act on a cluster you already have; it is never created, deleted or adopted")
	cmd.Flags().StringVar(&opt.ResourceGroup, "resource-group", "", "Azure resource group")
	cmd.Flags().StringVar(&opt.Cluster, "cluster", "", "AKS cluster name, which is also the kube-context name")
	cmd.Flags().StringVar(&opt.Registry, "registry", "", "private container registry name (globally unique, alphanumeric)")
}
