package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newReleaseLiftCommand(state *commandState) *cobra.Command {
	var opt app.ReleaseLiftOptions
	var planOnly bool
	var output string
	var destination string
	var selection destinationSelection
	cmd := &cobra.Command{
		Use: "lift <ref>@<digest>", Short: "Lift an evaluated packaged suite by immutable digest",
		Long: "Deploy the original suite from a registry or --from-layout OCI directory without source, builds, repacking or publishing.\n" +
			"Requires explicit private full-passing --evidence for exactly this suite and either --to NAME from workspace-root kmx.yaml or explicit --to-context; Orka uses Ready --inference provider:NAME, kagent uses author-owned --bindings FILE.\n" +
			"Named selectors resolve once; explicit flags override. No evalTarget or remembered/current-context fallback for Lift. Without --to, reference Lift never discovers a workspace or kmx.yaml.\n" +
			"Runtime/model come from the packaged member: native Orka or exact v0.10.3 Go kagent with openai-type inference. No runtime/model override.\n" +
			"Default evidence must match the exact destination. --promote explicitly allows another target/binding only for the same suite, member, model and runtime,\n" +
			"with the packaged model declaration (Orka: Ready Provider; kagent: rendered ModelConfig). Evidence proves the source only, not destination evaluation or authenticated approval.\n" +
			"Kagent creates new ModelConfig/Agent only; occupied names always refuse. Orka may reuse exact evaluated UID/generation/spec. Never update, adopt or retry ambiguous writes.\n" +
			"Created/recreated resources need fresh evaluation before later receipt-bound reuse. Partial resources may remain; readiness is not execution.\n" +
			"--plan is GET-only: no mutation guard, admission, writes, Tasks or placement records. JSON reports contain identities, never prompts or credentials.",
		Example: "  kmx lift registry.example/suite@sha256:<digest> --to prod --evidence ../private/pass.json --promote --plan\n" +
			"  kmx lift registry.example/suite@sha256:<digest> --to-context prod --inference provider:inference --evidence ../private/pass.json --promote --plan\n" +
			"  kmx lift suite@sha256:<digest> --from-layout ./oci --to-context kind-demo --inference provider:inference --evidence ../private/pass.json -o json",
		Args: usageArgs(1, 1, "kmx lift <ref>@<digest> (--to NAME | --to-context <ctx>) (--inference provider:NAME | --bindings FILE) --evidence FILE [--from-layout DIR] [--promote] [--plan]"),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateContainerEngineFlag(cmd, args); err != nil {
				return err
			}
			if state.contextFlag != "" || cmd.Flags().Changed("context") {
				return fmt.Errorf("--context cannot be used with suite lift; use --to-context")
			}
			for _, name := range []string{"to", "to-context", "to-namespace", "inference", "bindings", "evidence", "from-layout", "output"} {
				if f := cmd.Flag(name); f.Changed && strings.TrimSpace(f.Value.String()) == "" {
					return fmt.Errorf("lift requires non-empty --%s", name)
				}
			}
			if output != "text" && output != "json" {
				return fmt.Errorf("lift output must be text or json")
			}
			opt.Reference = args[0]
			if !cmd.Flags().Changed("to-namespace") {
				opt.ToNamespace = ""
			}
			selection = destinationSelection{Context: opt.ToContext, Namespace: opt.ToNamespace, Inference: opt.Inference, Bindings: opt.Bindings, PlainHTTP: opt.PlainHTTP}
			reference := opt.Reference
			if opt.FromLayout != "" {
				reference = ""
			}
			var err error
			selection, _, err = resolveDestinationSelection(cmd, destination, "", reference, false, selection)
			if err != nil {
				return err
			}
			opt.ToContext, opt.ToNamespace, opt.ExpectedRuntime = selection.Context, selection.Namespace, selection.ExpectedRuntime
			opt.Inference, opt.Bindings, opt.PlainHTTP = selection.Inference, selection.Bindings, selection.PlainHTTP
			return app.ValidateReleaseLiftOptions(opt)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := displayDestinationSelection(cmd, selection); err != nil {
				return err
			}
			selected := *state
			selected.contextFlag = opt.ToContext
			a, err := selected.operationApplication(cmd)
			if err != nil {
				return err
			}
			if selection.Source != "" && !cmd.Flags().Changed("to-context") {
				a.Cfg.ContextSource = selection.Source
			}
			// Named guard retry advice must preserve this digest, evidence and selectors,
			// not synthesize the legacy create route or refused global --context flag.
			parts := []string{"KIND_CLUSTER=" + quoteShell(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + quoteShell(a.Cfg.ContainerEngine), "kmx"}
			for _, arg := range state.argv {
				parts = append(parts, quoteShell(arg))
			}
			a.InvocationCommand = strings.Join(parts, " ")
			prepared, err := a.PrepareReleaseLift(ctx, opt)
			if err != nil {
				return err
			}
			summary := prepared.Summary()
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Runtime: %s (from suite %s, evaluated on source)\n", summary.Runtime, summary.SuiteDigest); err != nil {
				return err
			}
			if summary.Gate == "promotion" {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Evidence: passed on %q/%q (cluster %q, namespace UID %q), not evaluated on this target\n", summary.SourceTarget.Context, summary.SourceTarget.Namespace, summary.SourceTarget.ClusterUID, summary.SourceTarget.NamespaceUID); err != nil {
					return err
				}
			}
			if planOnly {
				if output == "json" {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s\nSuite: %s\nMember: %s\nModel: %s\nTarget: %q/%q\nGate: %s\n", summary.PlanDigest, summary.SuiteDigest, summary.Member, summary.Model, summary.Target.Context, summary.Target.Namespace, summary.Gate)
				if err != nil {
					return err
				}
				for _, r := range summary.Resources {
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %s\n", r.Kind, r.Name, r.Action); err != nil {
						return err
					}
				}
				return nil
			}
			result, applyErr := a.ApplyReleaseLift(ctx, prepared, summary.PlanDigest)
			if output == "json" {
				return errors.Join(applyErr, json.NewEncoder(cmd.OutOrStdout()).Encode(result))
			}
			_, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "Lift: %s\nSuite: %s\nMember: %s\nTarget: %q/%q\n", result.State, summary.SuiteDigest, summary.Member, summary.Target.Context, summary.Target.Namespace)
			for _, r := range result.Resources {
				if writeErr != nil {
					break
				}
				_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %s (ready=%t, UID=%q)\n", r.Kind, r.Name, r.Outcome, r.Ready, r.UID)
			}
			return errors.Join(applyErr, writeErr)
		},
	}
	registerDestinationFlag(cmd, &destination)
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "explicit destination kubeconfig context")
	cmd.Flags().StringVar(&opt.ToNamespace, "to-namespace", app.OrkaNamespace, "destination namespace")
	cmd.Flags().StringVar(&opt.Inference, "inference", "", "existing Ready inference Provider, as provider:NAME")
	cmd.Flags().StringVar(&opt.Bindings, "bindings", "", "author-owned KagentBindings file for the packaged kagent runtime")
	_ = cmd.MarkFlagFilename("bindings")
	cmd.Flags().StringVar(&opt.Evidence, "evidence", "", "explicit private full-passing native evidence file")
	cmd.Flags().StringVar(&opt.FromLayout, "from-layout", "", "read exact digest from a local OCI directory")
	cmd.Flags().BoolVar(&opt.PlainHTTP, "plain-http", false, "allow plain HTTP registry transport (not with --from-layout)")
	cmd.Flags().BoolVar(&opt.Promote, "promote", false, "explicit same-suite/model/runtime promotion; source-only evidence")
	cmd.Flags().BoolVar(&planOnly, "plan", false, "GET-only review; no admission or placement")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output: text|json")
	for _, name := range []string{"evidence"} {
		_ = cmd.MarkFlagRequired(name)
	}
	_ = cmd.MarkFlagDirname("from-layout")
	_ = cmd.MarkFlagFilename("evidence")
	return cmd
}
