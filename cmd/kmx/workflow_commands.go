package main

// `kmx workflow` — the answer to "how does a user express a governed
// workflow, or a slightly different one".
//
// A group rather than a flag on `kmx agent create`, because the two make
// different promises. `agent create` scaffolds ONE object and reaches
// neither the plane's policy nor any orchestration. A workflow is
// governance over seams that already exist, plus the ordered steps that
// use them — and its artifacts are a credential's allowlist and its
// standing bounds, not a CRD.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
)

func newWorkflowCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{
		Use:   "workflow",
		Short: "Declare, govern and run a governed workflow",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newWorkflowListCommand(state),
		newWorkflowShowCommand(state),
		newWorkflowGovernCommand(state),
		newWorkflowRefreshCommand(state),
		newWorkflowRunCommand(state),
	)
	return group
}

// blueprintFlags are the ones every subcommand but `list` takes.
//
// `--set name=value`, repeated, and deliberately not a values FILE: the
// values are somebody's real repository and real Azure organization, and
// a file invites committing them. A public repository is the wrong place for another
// project's identifiers — and typing them, or keeping them in the
// operator's own shell history or wrapper script, keeps that decision
// theirs rather than this repository's.
func blueprintFlags(cmd *cobra.Command, opt *app.WorkflowOptions, set *[]string) {
	cmd.Flags().StringVar(&opt.File, "file", "", "a blueprint you wrote (default: one kmx carries, named)")
	cmd.Flags().StringArrayVar(set, "set", nil, "parameter, as name=value; repeat for each")
	_ = cmd.MarkFlagFilename("file", "yaml", "yml")
}

// parseSet turns repeated --set name=value into the parameter map. A
// value is taken verbatim after the FIRST `=`, so a value containing one
// survives.
func parseSet(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range values {
		name, value, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("--set %q: want name=value", v)
		}
		name = strings.TrimSpace(name)
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--set %s given twice", name)
		}
		out[name] = value
	}
	return out, nil
}

func newWorkflowListCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "List the blueprints this kmx carries", Args: cobra.NoArgs,
		RunE: appRun(state, func(a *app.App) error { return a.ListWorkflows() }),
	}
}

func newWorkflowShowCommand(state *commandState) *cobra.Command {
	var opt app.WorkflowOptions
	var set []string
	cmd := &cobra.Command{
		Use:   "show [blueprint]",
		Short: "Render a blueprint and print what it would govern and do",
		Args:  usageArgs(0, 1, "kmx workflow show [<blueprint>] [--file <path>] [--set name=value]"),
	}
	blueprintFlags(cmd, &opt, &set)
	cmd.RunE = appRun(state, func(a *app.App) error {
		var err error
		if opt.Set, err = parseSet(set); err != nil {
			return err
		}
		return a.ShowWorkflow(cmd.Flags().Arg(0), opt)
	})
	cmd.ValidArgsFunction = completeBlueprints
	return cmd
}

func newWorkflowGovernCommand(state *commandState) *cobra.Command {
	var opt app.WorkflowOptions
	var set []string
	cmd := &cobra.Command{
		Use:   "govern [blueprint]",
		Short: "Apply a blueprint's allowlist and standing bounds",
		Args:  usageArgs(0, 1, "kmx workflow govern [<blueprint>] [--file <path>] --set name=value"),
	}
	blueprintFlags(cmd, &opt, &set)
	cmd.Flags().BoolVar(&opt.Replace, "replace", false,
		"overwrite governance already on the cluster that does not match this blueprint")
	cmd.RunE = appRun(state, func(a *app.App) error {
		var err error
		if opt.Set, err = parseSet(set); err != nil {
			return err
		}
		return a.GovernWorkflow(cmd.Flags().Arg(0), opt)
	})
	cmd.ValidArgsFunction = completeBlueprints
	return cmd
}

func newWorkflowRunCommand(state *commandState) *cobra.Command {
	var opt app.RunOptions
	var set []string
	cmd := &cobra.Command{
		Use:   "run [blueprint]",
		Short: "Run a workflow: the agent proposes, a human approves, the driver files the request",
		Args:  usageArgs(0, 1, "kmx workflow run [<blueprint>] [--file <path>] --set name=value"),
	}
	blueprintFlags(cmd, &opt.WorkflowOptions, &set)
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false,
		"read and draft, then stop before the first call with consequences")
	cmd.Flags().StringArrayVar(&opt.Steps, "step", nil, "run only this step, by name; repeat to select multiple steps")
	cmd.Flags().StringVar(&opt.Approver, "approver", "",
		"require this identity in the approval trail (CLI approvals carry no identity)")
	cmd.Flags().StringVar(&opt.AdminPort, "admin-port", app.DefaultWorkflowAdminPort,
		"the driver's own admin port-forward; the default admin port stays free for kmx approve")
	cmd.Flags().IntVar(&opt.HumanSeconds, "wait", 900, "seconds to wait for a person on each approval")
	cmd.RunE = appRun(state, func(a *app.App) error {
		var err error
		if opt.Set, err = parseSet(set); err != nil {
			return err
		}
		return a.RunWorkflow(cmd.Flags().Arg(0), opt)
	})
	cmd.ValidArgsFunction = completeBlueprints
	return cmd
}

func newWorkflowRefreshCommand(state *commandState) *cobra.Command {
	var opt app.WorkflowOptions
	cmd := &cobra.Command{
		Use:   "refresh [blueprint]",
		Short: "Refresh the expiring seam credentials declared by a workflow",
		Args:  usageArgs(0, 1, "kmx workflow refresh [<blueprint>] [--file <path>]"),
	}
	cmd.Flags().StringVar(&opt.File, "file", "", "a blueprint you wrote (default: one kmx carries, named)")
	_ = cmd.MarkFlagFilename("file", "yaml", "yml")
	cmd.RunE = appRun(state, func(a *app.App) error {
		return a.RefreshWorkflow(cmd.Flags().Arg(0), opt)
	})
	cmd.ValidArgsFunction = completeBlueprints
	return cmd
}

// completeBlueprints offers the blueprints this binary carries.
func completeBlueprints(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	bundles, err := blueprint.Carried(kaimahi.Blueprints)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, b := range bundles {
		names = append(names, b.Name+"\t"+b.Summary)
	}
	return filterCompletions(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}
