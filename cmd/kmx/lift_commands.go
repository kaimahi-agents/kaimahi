package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// newLiftCommand selects image deployment only with an explicit positional
// image reference. No-argument infrastructure invocations retain the managed
// up implementation; mixing their flags into image deployment is refused.
func newLiftCommand(state *commandState) *cobra.Command {
	cmd := newManagedUpCommand(state, "lift", "", "required, and never defaulted")
	legacyRun := cmd.RunE
	cmd.Use = "lift [image-reference]"
	cmd.Short = "Lift a built AgentSuite image to a prepared Kubernetes target"
	cmd.Long = "Lift a built AgentSuite image from an OCI registry, using an explicit deployment environment.\n\nWith no image reference, the deprecated managed-cluster route is retained; use kmx aks up for infrastructure preparation."
	cmd.Example = "  kmx lift registry.example/hello-world:v1 --environment ./production.json --plan"
	var environment string
	cmd.Flags().StringVar(&environment, "environment", "", "deployment environment JSON file for image lift")
	_ = cmd.MarkFlagFilename("environment", "json")
	cmd.Args = usageArgs(0, 1, "kmx lift <image-reference> --environment <file> [--plan]")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			if cmd.Flags().Changed("environment") {
				return fmt.Errorf("--environment requires an image reference")
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "The managed-cluster lift route is deprecated; use kmx aks up instead")
			return legacyRun(cmd, args)
		}
		if environment == "" {
			return fmt.Errorf("image lift requires --environment")
		}
		for _, name := range []string{"byo", "resource-group", "cluster", "registry", "payload", "location", "node-size", "node-count", "network-policy", "observability", "step"} {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s is an infrastructure flag and cannot be used with image lift", name)
			}
		}
		a, err := state.operationApplication(cmd)
		if err != nil {
			return err
		}
		// The target is explicitly selected by the environment, not ambient config.
		a.InvocationCommand = ""
		plan, err := cmd.Flags().GetBool("plan")
		if err != nil {
			return err
		}
		return a.LiftAgentImage(cmd.Context(), args[0], environment, plan)
	}
	cmd.AddCommand(newManagedDownCommand(state, true))
	return cmd
}

func newAKSCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "aks", Short: "Provision and remove an AKS target", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	cmd.AddCommand(newManagedUpCommand(state, "up", lift.PayloadOrka, "default: orka"), newManagedDownCommand(state, false))
	return cmd
}

func newManagedUpCommand(state *commandState, name, payloadDefault, payloadHelp string) *cobra.Command {
	var opt lift.Options
	cmd := &cobra.Command{
		Use:   name,
		Short: "Provision AKS with Azure-managed observability",
		Args:  cobra.NoArgs,
	}
	registerLiftIdentityFlags(cmd, &opt)
	cmd.Flags().StringVar(&opt.Payload, "payload", payloadDefault,
		strings.Join(lift.Payloads, "|")+" — what lands on the cluster; "+payloadHelp)
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
func newManagedDownCommand(state *commandState, deprecated bool) *cobra.Command {
	var opt lift.Options
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Remove what the lift created (and on a cluster you own, only that)",
		Args:  cobra.NoArgs,
	}
	if deprecated {
		cmd.Deprecated = "use kmx aks down instead"
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
