package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

func newAKSCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "aks", Short: "Provision and remove an AKS target", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	cmd.AddCommand(newManagedUpCommand(state), newManagedDownCommand(state))
	return cmd
}

func newManagedUpCommand(state *commandState) *cobra.Command {
	var opt lift.Options
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Provision AKS with Azure-managed observability",
		Args:  cobra.NoArgs,
	}
	registerLiftIdentityFlags(cmd, &opt)
	cmd.Flags().StringVar(&opt.Payload, "payload", lift.PayloadOrka,
		strings.Join(lift.Payloads, "|")+" — what lands on the cluster; default: orka")
	cmd.Flags().StringVar(&opt.Location, "location", "", "Azure region (default "+app.DefaultLocation+")")
	cmd.Flags().StringVar(&opt.NodeSize, "node-size", "", "node VM size (default "+app.DefaultNodeSize+")")
	cmd.Flags().IntVar(&opt.NodeCount, "node-count", 0, "how many nodes (default 1)")
	cmd.Flags().StringVar(&opt.NetworkPolicy, "network-policy", "", "NetworkPolicy engine: cilium (default), azure, calico")
	cmd.Flags().BoolVar(&opt.Observability, "observability", true, "wire Azure Monitor and Container Insights")
	cmd.Flags().StringVar(&opt.Step, "step", "", "run one phase: "+
		strings.Join(lift.StepsForPayload(lift.PayloadOrka), "|"))
	cmd.Flags().BoolVar(&opt.Plan, "plan", false, "print what would be created, where, and stop")
	_ = cmd.RegisterFlagCompletionFunc("payload", staticCompletion(lift.Payloads))
	_ = cmd.RegisterFlagCompletionFunc("step", staticCompletion(lift.AllSteps()))
	_ = cmd.RegisterFlagCompletionFunc("network-policy", staticCompletion([]string{"cilium", "azure", "calico"}))
	cmd.RunE = appRun(state, func(a *app.App) error {
		// An engine set to the empty string is not the same as one left
		// unset. Unset takes the default that enforces; explicitly empty is
		// the AKS default that does not, and it has to reach its own refusal
		// rather than being quietly replaced by something that works.
		opt.NetworkPolicySet = cmd.Flags().Changed("network-policy")
		return a.Lift(opt)
	})

	return cmd
}

// newManagedDownCommand removes what the provisioner created — and what that means
// depends entirely on which branch created it, which is why `--byo` is
// required here too rather than remembered silently. The run record says
// which branch it was, and a mismatch is refused rather than reconciled: the
// two have opposite rules about the cluster and its resource group.
func newManagedDownCommand(state *commandState) *cobra.Command {
	var opt lift.Options
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Tear down created AKS infrastructure, or clean up recorded BYO monitoring",
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
