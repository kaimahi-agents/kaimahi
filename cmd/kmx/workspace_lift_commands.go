package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

// Resolve the operational document once. Only destination flags override saved
// destination selectors; the evaluation environment is explicitly config-owned.
func resolveWorkspaceLiftOptions(cmd *cobra.Command, name string, destination app.ReleaseLiftOptions, evaluation app.SuiteWorkspaceEvaluationOptions) (app.WorkspaceLiftOptions, error) {
	var opt app.WorkspaceLiftOptions
	if name == "" {
		return opt, fmt.Errorf("workspace %s requires explicit --to NAME", cmd.Name())
	}
	if err := refuseMultiMemberWorkspace(""); err != nil {
		return opt, err
	}
	root, doc, err := resolveDestinations("")
	if err != nil {
		return opt, err
	}
	target, err := doc.Target(name)
	if err != nil {
		return opt, err
	}
	if target.EvaluationTarget == "" || doc.PublishRepository == "" {
		return opt, fmt.Errorf("workspace %s requires publishRepository and destination evaluationTarget in kmx.yaml", cmd.Name())
	}
	eval, err := doc.Target(target.EvaluationTarget)
	if err != nil {
		return opt, err
	}
	if !cmd.Flags().Changed("to-context") {
		destination.ToContext = target.Context
	}
	if !cmd.Flags().Changed("to-namespace") {
		destination.ToNamespace = target.Namespace
	}
	if !cmd.Flags().Changed("inference") {
		destination.Inference = target.Inference
	}
	if !cmd.Flags().Changed("plain-http") {
		destination.PlainHTTP = doc.RegistryHTTP(doc.PublishRepository)
	}
	destination.ExpectedRuntime = target.Runtime
	evaluation.Workspace = root
	evaluation.ToContext, evaluation.ToNamespace = eval.Context, eval.Namespace
	evaluation.Inference, evaluation.ExpectedRuntime = eval.Inference, eval.Runtime
	if target.Bindings != "" || eval.Bindings != "" || target.Runtime != "" && target.Runtime != "orka" || eval.Runtime != "" && eval.Runtime != "orka" {
		return opt, fmt.Errorf("composed workspace %s supports native Orka only", cmd.Name())
	}
	opt.Workspace, opt.Repository = root, doc.PublishRepository
	opt.EvalName, opt.DestinationName = target.EvaluationTarget, name
	opt.Evaluation, opt.Destination = evaluation, destination
	return opt, nil
}

func runWorkspaceLiftCommand(cmd *cobra.Command, state *commandState, opt app.WorkspaceLiftOptions) (err error) {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	prepared, err := app.PrepareWorkspaceLift(ctx, opt)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, prepared.Close()) }()
	if opt.PlanOnly {
		_, err = (&app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), DeployPresentation: cmd.Name() == "deploy", TestPresentation: cmd.Name() == "deploy"}).LiftWorkspace(ctx, prepared)
		return err
	}
	selected := *state
	selected.contextFlag = opt.Destination.ToContext
	a, err := selected.operationApplication(cmd)
	if err != nil {
		return err
	}
	// Keep recovery advice on this route; global --context is refused by Lift.
	parts := []string{quoteShell(state.executable())}
	for _, arg := range deploymentRecoveryArgs(suiteEvaluationRecoveryArgs(state.argv)) {
		parts = append(parts, quoteShell(arg))
	}
	a.InvocationCommand = strings.Join(parts, " ")
	a.DeployPresentation = cmd.Name() == "deploy"
	a.TestPresentation = cmd.Name() == "deploy"
	_, err = a.LiftWorkspace(ctx, prepared)
	return err
}

// Only the root command token changes. A context, engine, evidence path or
// reference containing "lift" remains opaque and is quoted by the caller.
func deploymentRecoveryArgs(argv []string) []string {
	args := append([]string(nil), argv...)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--container-engine", args[i] == "--context":
			i++
		case strings.HasPrefix(args[i], "--container-engine="), strings.HasPrefix(args[i], "--context="), args[i] == "--":
			continue
		default:
			if args[i] == "lift" {
				args[i] = "deploy"
			}
			return args
		}
	}
	return args
}
