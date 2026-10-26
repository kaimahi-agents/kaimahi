package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
	"github.com/spf13/cobra"
)

func newReleaseDeployCommand(state *commandState) *cobra.Command {
	return newReleaseCommand(state, "deploy")
}

func newReleaseLiftCommand(state *commandState) *cobra.Command {
	return newReleaseCommand(state, "lift")
}

// Both spellings own independent flag storage but share every operational path.
func newReleaseCommand(state *commandState, verb string) *cobra.Command {
	label := "Deploy"
	if verb == "lift" {
		label = "Lift"
	}
	var opt app.ReleaseLiftOptions
	var planOnly bool
	var output string
	var destination string
	var selection destinationSelection
	var evaluation app.SuiteWorkspaceEvaluationOptions
	var verbose bool
	var provenance = app.SuitePushProvenance{Enabled: true}
	cmd := &cobra.Command{
		Use: verb + " [<ref>@<digest>]", Short: "Deploy a workspace or an evaluated packaged suite",
		Long: "Without a reference, --to NAME composes missing native Orka workspace eval, push and Lift. Requires saved publishRepository and destination evaluationTarget.\n" +
			"Freezes the source and selectors, automatically discloses source-only promotion, confirms one plan, and verifies exact-version skips read-only. Plain Lift is create-only; --update selects the retained Orka lifecycle, never adoption or repair.\n" +
			"Default workspace output is four stage lines (Plan/Eval/Push/Lift) plus compact evaluation failure detail on stderr; -v shows stage diagnostics, --show-content additionally prints sanitized bounded failure excerpts, expected values and judge rationale (never evidence); -o json emits one result. Nonterminal confirmation must name the concrete contexts disclosed in Plan.\n" +
			"Evaluation sends case inputs to the runtime/model; llmJudge additionally transfers inputs, answers and rubrics. Cleanup does not erase retained upstream data.\n" +
			"With an explicit digest, deploy the original suite from a registry or --from-layout OCI directory without source, builds, repacking or publishing.\n" +
			"Requires explicit private full-passing --evidence for exactly this suite and either --to NAME from workspace-root kmx.yaml or explicit --to-context; Orka uses Ready --inference provider:NAME, kagent uses author-owned --bindings FILE.\n" +
			"Named selectors resolve once; explicit flags override. No evalTarget or remembered/current-context fallback for Lift. Without --to, reference Lift never discovers a workspace or kmx.yaml.\n" +
			"Runtime/model come from the packaged member: native Orka or exact v0.10.3 Go kagent with openai-type inference. No runtime/model override.\n" +
			"Default evidence must match the exact destination. Candidate evidence requires --promote even in the same environment and a Ready Provider defaultModel matching the packaged model. Plain Lift requires absent canonical resources; --update requires an eligible retained pair.\n" +
			"--promote explicitly allows source-only evidence for the same suite, member, model and runtime,\n" +
			"with the packaged model declaration (Orka: Ready Provider; kagent: rendered ModelConfig). Evidence proves the source only, not destination evaluation or authenticated approval.\n" +
			"Kagent creates new ModelConfig/Agent only; occupied names and --update always refuse. Plain Orka Lift may reuse exact evaluated UID/generation/spec, never update or adopt.\n" +
			"--update changes only new-version retained Orka placements to a passing digest, or rolls back with original passing evidence without evaluation. Older/unrecorded placements must be explicitly recreated; no enrollment.\n" +
			"Requires an operator-arranged quiet window. A pair-wide reservation refuses concurrent updates; unknown outcomes stay blocked. No automatic retry, takeover, undo or deletion.\n" +
			"Created/recreated resources need fresh evaluation before later receipt-bound reuse. Partial resources may remain; readiness is not execution.\n" +
			"--plan is GET-only: no mutation guard, admission, writes, Tasks or placement records. Digest update plans inspect retained proof and a bounded cluster-wide Agent list. Cold workspace plans stay offline and do not establish live update eligibility. JSON reports contain identities, never prompts or credentials.\n" +
			"With --to selecting an AgentSessions destination, create an immutable Agent with the package digest, exact member and operator default model binding. --name defaults to the member; changed inputs need a new name. Same acknowledged spec is GET-only and reports existing status. Waits at most two minutes for Registered (last observed admission, not live availability or target evaluation). No updates or Sessions host calls.\n" +
			"AgentSessions --harness-image NAME@sha256:<digest> --bindings FILE (AgentSessionsBinding) --evidence FILE --plan renders a substrate ActorTemplate and HarnessRegistration offline.\n" +
			"The binding's model.name must equal the packaged model; the evidence's recorded binding may name another environment serving the same model. No cluster configuration, substrate or host calls, no apply and no --update.",
		Example: "  kmx lift --to prod\n  kmx lift --to prod --plan\n  kmx lift --to prod --update\n" +
			"  kmx lift registry.example/suite@sha256:<previous-passing-digest> --to prod --evidence ../private/original-pass.json --promote --update\n" +
			"  kmx lift registry.example/suite@sha256:<digest> --to prod --evidence ../private/pass.json --promote --plan\n" +
			"  kmx lift registry.example/suite@sha256:<digest> --to-context prod --inference provider:inference --evidence ../private/pass.json --promote --plan\n" +
			"  kmx lift suite@sha256:<digest> --from-layout ./oci --to-context kind-demo --inference provider:inference --evidence ../private/pass.json --promote -o json\n" +
			"  kmx lift suite@sha256:<digest> --from-layout ./oci --harness-image registry.example/harnessnode@sha256:<digest> --bindings binding.yaml --evidence ../private/pass.json --plan -o json",
		Args: usageArgs(0, 1, "kmx "+verb+" --to NAME | kmx "+verb+" <ref>@<digest> (--to NAME | --to-context <ctx>) (--inference provider:NAME | --bindings FILE) --evidence FILE [--from-layout DIR] [--promote] [--update] [--plan] | kmx "+verb+" <ref>@<digest> --to <AgentSessions-destination> --evidence FILE [--name NAME] [--from-layout DIR] [--plan] | kmx "+verb+" <ref>@<digest> --harness-image IMAGE --bindings FILE --evidence FILE --plan"),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateContainerEngineFlag(cmd, args); err != nil {
				return err
			}
			if cmd.Flags().Changed("show-content") && (len(args) != 0 || planOnly) {
				return fmt.Errorf("--show-content requires executing workspace evaluation")
			}
			if state.contextFlag != "" || cmd.Flags().Changed("context") {
				return fmt.Errorf("--context cannot be used with suite %s; use --to-context", verb)
			}
			for _, name := range []string{"to", "to-context", "to-namespace", "inference", "bindings", "evidence", "from-layout", "output", "result-port", "judge-base-url", "judge-key-env", "name", "orka-chart", "orka-chart-sha256"} {
				if f := cmd.Flag(name); f.Changed && strings.TrimSpace(f.Value.String()) == "" {
					return fmt.Errorf("%s requires non-empty --%s", verb, name)
				}
			}
			if output != "text" && output != "json" {
				return fmt.Errorf("%s output must be text or json", verb)
			}
			if len(args) == 0 {
				if cmd.Flags().Changed("orka-chart") || cmd.Flags().Changed("orka-chart-sha256") {
					return fmt.Errorf("local Orka chart selection requires explicit digest Deploy")
				}
				for _, name := range []string{"evidence", "from-layout", "bindings", "harness-image", "name"} {
					if cmd.Flags().Changed(name) {
						return fmt.Errorf("workspace %s refuses --%s; use explicit digest %s", verb, name, label)
					}
				}
				if destination == "" {
					return fmt.Errorf("workspace %s requires explicit --to NAME", verb)
				}
				if provenance.Require && !provenance.Enabled {
					return fmt.Errorf("--require-provenance cannot be used with --provenance=false")
				}
				return nil
			}
			for _, name := range []string{"case-timeout", "result-port", "judge-base-url", "judge-key-env", "provenance", "require-provenance"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("explicit-reference %s refuses --%s", verb, name)
				}
			}
			if !cmd.Flags().Changed("evidence") {
				return fmt.Errorf("%w: explicit-reference %s requires --evidence FILE", app.ErrReleaseLift, verb)
			}
			if cmd.Flags().Changed("harness-image") {
				if strings.TrimSpace(opt.HarnessImage) == "" {
					return fmt.Errorf("%s requires non-empty --harness-image", verb)
				}
				if !planOnly {
					return fmt.Errorf("AgentSessions %s is plan-only; pass --plan (no substrate or host writes are implemented)", verb)
				}
				for _, name := range []string{"to", "to-context", "to-namespace", "inference", "promote"} {
					if cmd.Flag(name).Changed {
						return fmt.Errorf("--harness-image refuses --%s; AgentSessions placement comes from --bindings", name)
					}
				}
				if cmd.Flags().Changed("update") {
					return fmt.Errorf("--harness-image refuses --update; AgentSessions %s is plan-only with no retained placement", verb)
				}
				if !cmd.Flags().Changed("bindings") {
					return fmt.Errorf("--harness-image requires --bindings FILE (AgentSessionsBinding)")
				}
				if _, err := agentsessions.HarnessImageDigest(opt.HarnessImage); err != nil {
					return fmt.Errorf("--harness-image: %w", err)
				}
				opt.Reference, opt.ToNamespace = args[0], ""
				return app.ValidateReleaseLiftOptions(opt)
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
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				composed, err := resolveWorkspaceLiftOptions(cmd, destination, opt, evaluation)
				if err != nil {
					return err
				}
				composed.PlanOnly, composed.Verbose, composed.JSON = planOnly, verbose, output == "json"
				composed.Provenance = provenance
				if provenance.Enabled {
					info, ok := state.deps.buildInfo()
					if v := version.Resolve(info, ok).Version; v != version.Unknown {
						composed.Provenance.BuilderVersion = v
					}
				}
				if cmd.Flags().Changed("promote") {
					promote := opt.Promote
					composed.Promote = &promote
				}
				return runWorkspaceLiftCommand(cmd, state, composed)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if opt.HarnessImage != "" {
				return runAgentSessionsLiftPlan(ctx, cmd, opt, output)
			}
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
			parts := []string{"KIND_CLUSTER=" + quoteShell(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + quoteShell(a.Cfg.ContainerEngine), quoteShell(state.executable())}
			for _, arg := range deploymentRecoveryArgs(state.argv) {
				parts = append(parts, quoteShell(arg))
			}
			a.InvocationCommand = strings.Join(parts, " ")
			a.DeployPresentation = verb == "deploy"
			a.TestPresentation = verb == "deploy"
			prepared, err := a.PrepareReleaseLift(ctx, opt)
			if err != nil {
				return err
			}
			summary := prepared.Summary()
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Runtime: %s (from suite %s, evaluated on source)\n", summary.Runtime, summary.SuiteDigest); err != nil {
				return err
			}
			if summary.Gate == "development-source" {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Evidence: passed on a development host, not evaluated on this target; modelBindingRef=default is operator-owned. Registered means last controller-observed admission, not live availability."); err != nil {
					return err
				}
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
			return reportReleaseLiftResult(cmd, output, summary, result, applyErr)
		},
	}
	// Help is authored CLI copy, not operation data. Both routes teach the
	// canonical spelling while the hidden route retains its own usage/flags.
	cmd.Long = strings.NewReplacer("Lift", "Deploy", "lift", "deploy").Replace(cmd.Long)
	cmd.Example = strings.ReplaceAll(cmd.Example, "kmx lift", "kmx deploy")
	if verb == "lift" {
		cmd.Hidden = true
		noticed := false
		notice := func() error {
			if noticed {
				return nil
			}
			noticed = true
			_, err := fmt.Fprintln(cmd.ErrOrStderr(), "kmx lift is deprecated; use kmx deploy (same arguments).")
			return err
		}
		args := cmd.Args
		cmd.Args = func(cmd *cobra.Command, values []string) error {
			return errors.Join(notice(), args(cmd, values))
		}
		cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
			return errors.Join(notice(), err)
		})
		help := cmd.HelpFunc()
		cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
			_ = notice()
			help(cmd, args)
		})
	}
	registerDestinationFlag(cmd, &destination)
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "explicit destination kubeconfig context")
	cmd.Flags().StringVar(&opt.ToNamespace, "to-namespace", app.OrkaNamespace, "destination namespace")
	cmd.Flags().StringVar(&opt.Inference, "inference", "", "existing Ready inference Provider, as provider:NAME")
	cmd.Flags().StringVar(&opt.OrkaChart, "orka-chart", "", "exact local chart of the already installed kmx-owned Orka candidate")
	cmd.Flags().StringVar(&opt.OrkaChartSHA256, "orka-chart-sha256", "", "exact SHA256 of --orka-chart; must match evaluated runtime proof")
	cmd.MarkFlagsRequiredTogether("orka-chart", "orka-chart-sha256")
	_ = cmd.MarkFlagFilename("orka-chart")
	cmd.Flags().StringVar(&opt.Bindings, "bindings", "", "author-owned KagentBindings (kagent) or AgentSessionsBinding (agentsessions, with --harness-image) file")
	cmd.Flags().StringVar(&opt.AgentName, "name", "", "AgentSessions Agent resource name (default: packaged member); changed inputs require a new name")
	_ = cmd.RegisterFlagCompletionFunc("name", staticCompletion(nil))
	cmd.Flags().StringVar(&opt.HarnessImage, "harness-image", "", "digest-pinned AgentSessions harnessnode image; plan-only AgentSessions render (requires --plan and --bindings)")
	_ = cmd.MarkFlagFilename("bindings")
	cmd.Flags().StringVar(&opt.Evidence, "evidence", "", "explicit private full-passing native evidence file")
	cmd.Flags().StringVar(&opt.FromLayout, "from-layout", "", "read exact digest from a local OCI directory")
	cmd.Flags().BoolVar(&opt.PlainHTTP, "plain-http", false, "allow plain HTTP registry transport (not with --from-layout)")
	cmd.Flags().BoolVar(&opt.Promote, "promote", false, "explicit same-suite/model/runtime promotion; source-only evidence")
	cmd.Flags().BoolVar(&opt.Update, "update", false, "update an eligible retained Orka placement, or roll back with original passing evidence; no enrollment")
	cmd.Flags().BoolVar(&planOnly, "plan", false, "GET-only review; no admission or placement")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output: text|json")
	cmd.Flags().BoolVar(&evaluation.ShowContent, "show-content", false, "print sanitized bounded evaluation failure content on stderr (workspace execution only; not evidence)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show workspace stage diagnostics")
	cmd.Flags().DurationVar(&evaluation.CaseTimeout, "case-timeout", 5*time.Minute, "workspace evaluation deadline per run, at most 9m")
	cmd.Flags().StringVar(&evaluation.ResultPort, "result-port", "", "workspace evaluation loopback result-forward port (omitted: choose automatically)")
	cmd.Flags().StringVar(&evaluation.JudgeBaseURL, "judge-base-url", "", "workspace evaluation judge endpoint; HTTPS except keyless literal loopback")
	cmd.Flags().StringVar(&evaluation.JudgeKeyEnv, "judge-key-env", "", "workspace evaluation judge credential environment variable name")
	cmd.Flags().BoolVar(&provenance.Enabled, "provenance", true, "attach unsigned suite provenance during workspace publication")
	cmd.Flags().BoolVar(&provenance.Require, "require-provenance", false, "refuse workspace publication if provenance cannot be attached")
	_ = cmd.MarkFlagDirname("from-layout")
	_ = cmd.MarkFlagFilename("evidence")
	return cmd
}

// Keep reporting separate from effects: output errors join the operation error,
// and machine results never pass through human presentation formatting.
func reportReleaseLiftResult(cmd *cobra.Command, output string, summary app.ReleaseLiftSummary, result app.ReleaseLiftResult, applyErr error) error {
	if output == "json" {
		writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		if writeErr != nil && summary.Runtime == "agentsessions" {
			return writeErr
		}
		return errors.Join(applyErr, writeErr)
	}
	if summary.Runtime == "agentsessions" && summary.SessionsAgent != nil {
		_, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "Agent: %s\nPackage: %s\nMember: %s\nTarget: %q/%q\n", result.State, summary.SuiteDigest, summary.Member, summary.Target.Context, summary.Target.Namespace)
		for _, r := range result.Resources {
			if writeErr == nil {
				_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "Agent/%s: %s (UID=%q)\n", r.Name, r.Outcome, r.UID)
			}
		}
		if writeErr != nil {
			return writeErr
		}
		return applyErr
	}
	label := "Lift"
	if cmd.Name() == "deploy" {
		label = "Deploy"
	}
	_, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\nSuite: %s\nMember: %s\nTarget: %q/%q\n", label, result.State, summary.SuiteDigest, summary.Member, summary.Target.Context, summary.Target.Namespace)
	for _, r := range result.Resources {
		if writeErr != nil {
			break
		}
		_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %s (ready=%t, UID=%q)\n", r.Kind, r.Name, r.Outcome, r.Ready, r.UID)
	}
	return errors.Join(applyErr, writeErr)
}

// runAgentSessionsLiftPlan renders the plan-only AgentSessions deploy. It loads
// no cluster configuration and makes no substrate or host calls.
func runAgentSessionsLiftPlan(ctx context.Context, cmd *cobra.Command, opt app.ReleaseLiftOptions, output string) error {
	prepared, err := app.PrepareAgentSessionsReleaseLift(ctx, opt)
	if err != nil {
		return err
	}
	summary := prepared.Summary()
	if summary.AgentSessions == nil {
		return app.ErrReleaseLift
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Runtime: %s (from suite %s, evaluated on an operator-observed development host)\nPlan-only: no substrate or AgentSessions host writes.\n", summary.Runtime, summary.SuiteDigest); err != nil {
		return err
	}
	if output == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
	}
	s := summary.AgentSessions
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s\nSuite: %s\nMember: %s\nModel: %s\nBinding: %s\nEvidence binding: %s\nAtespace: %s\nGate: %s\n", summary.PlanDigest, summary.SuiteDigest, summary.Member, summary.Model, summary.BindingDigest, s.EvidenceBindingDigest, s.ActorTemplate.Metadata.Atespace, summary.Gate); err != nil {
		return err
	}
	for _, r := range summary.Resources {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s/%s: %s\n", r.Kind, r.Name, r.Action); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "Rendered documents: -o json")
	return err
}
