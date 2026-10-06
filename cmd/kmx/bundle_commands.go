package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/adapter/orka"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/bundle"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
)

// bundleLabel marks every rendered resource with the bundle it came from.
const bundleLabel = "kmx.kaimahi.dev/bundle"

// Both commands read only local files: no config, cluster or network access.
func newBundleCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "bundle",
		Short: "Validate and render composable bundles offline",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	var output string
	validate := &cobra.Command{
		Use:   "validate <bundle-dir>",
		Short: "Validate a composable bundle and print each Agent's dependency-aware identity",
		Args:  usageArgs(1, 1, "kmx bundle validate <bundle-dir> [-o text|json]"),
	}
	validate.Flags().StringVarP(&output, "output", "o", "text", "output: text|json")
	_ = validate.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	validate.RunE = func(cmd *cobra.Command, args []string) error {
		if output != "text" && output != "json" {
			return fmt.Errorf("unsupported output %q; use text or json", output)
		}
		b, err := bundle.Load(args[0])
		if err != nil {
			return fmt.Errorf("bundle is not valid: %w", err)
		}
		report, err := newBundleReport(b)
		if err != nil {
			return err
		}
		if output == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		return report.writeText(cmd.OutOrStdout())
	}

	var namespace, schema string
	render := &cobra.Command{
		Use:   "render <bundle-dir>",
		Short: "Render a composable bundle as Orka YAML",
		Long: `Render a whole composable bundle as reviewable Orka Provider, Tool and Agent YAML.

Each shared Provider and Tool renders once. Providers and Agents are checked
against the selected embedded Orka schema. Nothing is applied; the Secrets
listed in the output header must already exist.`,
		Args: usageArgs(1, 1, "kmx bundle render <bundle-dir> --namespace <namespace> [--orka-schema v0.2.0|v0.1.3|main]"),
	}
	render.Flags().StringVarP(&namespace, "namespace", "n", "", "target namespace (required)")
	render.Flags().StringVar(&schema, "orka-schema", "v0.2.0", "embedded Orka schema to validate against: v0.2.0, v0.1.3 or main")
	_ = render.RegisterFlagCompletionFunc("orka-schema", staticCompletion([]string{"v0.2.0", "v0.1.3", "main"}))
	render.RunE = func(cmd *cobra.Command, args []string) error {
		if namespace == "" {
			return fmt.Errorf("--namespace is required")
		}
		validator, err := orkaschema.Offline(schema)
		if err != nil {
			return err
		}
		b, err := bundle.Load(args[0])
		if err != nil {
			return fmt.Errorf("bundle is not valid: %w", err)
		}
		rendered, err := orka.Render(b.Graph, orka.Options{
			Namespace: namespace,
			Labels:    map[string]string{bundleLabel: b.Manifest.Metadata.Name},
		})
		if err != nil {
			return err
		}
		for _, doc := range rendered.Documents {
			// orkaschema embeds the Agent, Provider and Task CRDs only.
			if doc["kind"] == "Tool" {
				continue
			}
			if err := validator.Validate(doc); err != nil {
				return fmt.Errorf("%s %v does not match %s: %w", doc["kind"], doc["metadata"].(map[string]any)["name"], validator.Provenance(), err)
			}
		}
		body, err := rendered.YAML()
		if err != nil {
			return err
		}
		_, err = io.WriteString(cmd.OutOrStdout(), renderHeader(b, rendered)+body)
		return err
	}

	group.AddCommand(validate, render)
	return group
}

type bundleReport struct {
	Name      string              `json:"name"`
	Version   string              `json:"version"`
	Providers []string            `json:"providers"`
	Tools     []string            `json:"tools"`
	Agents    []bundleAgentReport `json:"agents"`
}

type bundleAgentReport struct {
	Name          string   `json:"name"`
	Identity      string   `json:"identity"`
	Provider      string   `json:"provider"`
	Tools         []string `json:"tools"`
	AllowedAgents []string `json:"allowedAgents"`
}

func newBundleReport(b *bundle.Bundle) (*bundleReport, error) {
	r := &bundleReport{
		Name: b.Manifest.Metadata.Name, Version: b.Manifest.Metadata.Version,
		Providers: []string{}, Tools: []string{}, Agents: []bundleAgentReport{},
	}
	for _, p := range b.Providers {
		r.Providers = append(r.Providers, p.Metadata.Name)
	}
	for _, t := range b.Tools {
		r.Tools = append(r.Tools, t.Metadata.Name)
	}
	for _, a := range b.Agents {
		id, err := b.AgentIdentity(a.Metadata.Name)
		if err != nil {
			return nil, err
		}
		r.Agents = append(r.Agents, bundleAgentReport{
			Name: a.Metadata.Name, Identity: id, Provider: a.Spec.Provider,
			Tools: nonNil(a.Spec.Tools), AllowedAgents: nonNil(a.Spec.AllowedAgents),
		})
	}
	return r, nil
}

func (r *bundleReport) writeText(w io.Writer) error {
	var s strings.Builder
	fmt.Fprintf(&s, "Bundle %s %s: valid (providers=%d tools=%d agents=%d)\n", r.Name, r.Version, len(r.Providers), len(r.Tools), len(r.Agents))
	for _, a := range r.Agents {
		fmt.Fprintf(&s, "  Agent %s %s\n    provider: %s\n    tools: %s\n    allowedAgents: %s\n",
			a.Name, a.Identity, a.Provider, listOrNone(a.Tools), listOrNone(a.AllowedAgents))
	}
	_, err := io.WriteString(w, s.String())
	return err
}

func renderHeader(b *bundle.Bundle, r *orka.Rendered) string {
	header := fmt.Sprintf("# Rendered by kmx bundle render from %s %s.\n", b.Manifest.Metadata.Name, b.Manifest.Metadata.Version)
	if len(r.Secrets) > 0 {
		refs := make([]string, 0, len(r.Secrets))
		for _, s := range r.Secrets {
			refs = append(refs, s.Name+"/"+s.Key)
		}
		header += "# Required Secrets (name/key), not created here: " + strings.Join(refs, ", ") + "\n"
	}
	return header
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}
