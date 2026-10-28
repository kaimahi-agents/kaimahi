package main

import "github.com/spf13/cobra"

func newClusterCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "cluster", Short: "Set up and inspect Kubernetes runtimes",
		Long:    "Set up, inspect and remove runtimes in the selected Kubernetes cluster.",
		Example: "  kmx cluster up\n  kmx cluster status\n  kmx cluster quickstart", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newUpCommand(state), newDownCommand(state), newOrkaStatusCommand(state),
		newContextCommand(state), newQuickstartCommand(state), newConsoleCommand(state),
		newTaskCommand(state), newTargetsCommand(state), newOrkaCommand(state), newAKSCommand(state),
		retiredCommand("quickstart-wizard", "kmx cluster quickstart --interactive"))
	return group
}

func newPackageCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "package", Short: "Evaluate, publish and deploy native packages",
		Long: "Test native workspaces, publish packages and inspect or deploy destinations.\n" +
			"Build consumes an AgentSuite directory, not an agent markdown file.\n" +
			"See package build --help for the experimental image-builder limits.",
		Example: "  kmx package test --workspace ./greeter --to dev\n  kmx package validate ./suite\n  kmx package build ./suite --output image.tar", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	// Directory transfer remains distinct from evidence-gated workspace Push.
	suite := newSuiteCommand(state)
	for _, child := range suite.Commands() {
		if child.Name() != "push" {
			suite.RemoveCommand(child)
			group.AddCommand(child)
		}
	}
	group.AddCommand(newSuitePushCommand(state, true), newReleaseDeployCommand(state),
		newStatusCommand(state), newAgentEvaluationCommand(state, "test"),
		newDestinationsCommand(state), newRunProfileCommand(state), newAgentCreateCommand(state), suite)
	return group
}
