package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Concrete values only: later stages retain no config pathname or reread hook.
type destinationSelection struct {
	Context, Namespace, ExpectedRuntime, Inference, Bindings string
	Registry                                                 string
	PlainHTTP                                                bool
	Source                                                   string
}

func resolveDestinationSelection(cmd *cobra.Command, name, workspace, reference string, evaluation bool, opt destinationSelection) (destinationSelection, string, error) {
	// Explicit source-free reference Lift must not even discover a workspace.
	// Explicit evaluation context likewise bypasses the optional evalTarget.
	if name == "" && (!evaluation || opt.Context != "") {
		return opt, workspace, nil
	}
	root, doc, err := resolveDestinations(workspace)
	if err != nil {
		return opt, workspace, err
	}
	if name == "" && doc != nil {
		name = doc.EvalTarget
	}
	if name == "" {
		return opt, root, nil
	}
	target, err := doc.Target(name)
	if err != nil {
		return opt, root, err
	}
	if !cmd.Flags().Changed("to-context") {
		opt.Context = target.Context
	}
	if !cmd.Flags().Changed("to-namespace") {
		opt.Namespace = target.Namespace
	}
	opt.ExpectedRuntime = target.Runtime
	// Inference/bindings are a tagged union. An explicit arm replaces the saved
	// arm; both explicit arms remain invalid, never silently choose one.
	if !cmd.Flags().Changed("inference") && !cmd.Flags().Changed("bindings") {
		opt.Inference, opt.Bindings = target.Inference, target.Bindings
	}
	if evaluation {
		opt.Registry = doc.PublishRepository
		opt.PlainHTTP = doc.RegistryHTTP(opt.Registry)
	} else if reference != "" {
		opt.Registry = reference
	}
	if reference != "" && !cmd.Flags().Changed("plain-http") {
		opt.PlainHTTP = doc.RegistryHTTP(reference)
	}
	opt.Source = "kmx.yaml (--to " + name + ")"
	return opt, root, nil
}
func displayDestinationSelection(cmd *cobra.Command, opt destinationSelection) error {
	if opt.Source == "" {
		return nil
	}
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Destination: %s; context=%q; namespace=%q; expected-runtime=%q; inference=%q; bindings=%q; registry=%q; plain-http=%t\n", opt.Source, opt.Context, opt.Namespace, opt.ExpectedRuntime, opt.Inference, opt.Bindings, opt.Registry, opt.PlainHTTP)
	return err
}
func registerDestinationFlag(cmd *cobra.Command, name *string) {
	cmd.Flags().StringVar(name, "to", "", "resolve a named destination from workspace-root kmx.yaml; explicit selectors override")
	_ = cmd.RegisterFlagCompletionFunc("to", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
}

// Registry policy applies only to root workspace registry publication. Layout
// and extracted-directory transfer never inspect this operational document.
func resolveWorkspacePushTransport(cmd *cobra.Command, root, reference string, plainHTTP bool) (string, string, bool, error) {
	selected, doc, err := resolveDestinations(root)
	if err != nil {
		return "", "", false, err
	}
	var repository string
	if doc != nil {
		repository = doc.PublishRepository
	}
	if reference == "" {
		if repository == "" {
			return "", "", false, fmt.Errorf("bare push requires saved publishRepository in workspace kmx.yaml")
		}
		reference = repository
	}
	if !cmd.Flags().Changed("plain-http") {
		plainHTTP = doc.RegistryHTTP(reference)
	}
	return selected, repository, plainHTTP, nil
}
