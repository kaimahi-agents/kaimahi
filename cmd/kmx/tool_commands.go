package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newToolsCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "tools", Short: "Suggest and review scoped local tool bindings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }, PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
			return errors.New("local tool bindings do not use cluster selectors")
		}
		return nil
	}}
	var repo, model, output string
	suggest := &cobra.Command{Use: "suggest FILE", Short: "Suggest up to three scoped CLI tools from agent instructions", Long: "Suggest bounded CLI bindings with fixed arguments and operand limits. Missing scope is reported, never guessed. Review adds selected declarations; tools approve separately grants execution. Optional --repo uses the fixed GitHub-template proposal flow instead.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if output != "text" && output != "json" {
			return errors.New("output must be text, json or yaml")
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Suggesting tools: sends this agent definition to Copilot; no tool execution or grants.")
		proposal, err := app.SuggestMarkdownTools(cmd.Context(), args[0], repo, model)
		if err != nil {
			return err
		}
		if output == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(proposal)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Proposal: %s\nRepository: %s\n", proposal.ID, proposal.Repository)
		for _, choice := range proposal.Choices {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s — %s\n", choice.Template, choice.Reason)
		}
		for _, choice := range proposal.CLIChoices {
			if err := app.RenderCLIChoice(cmd.OutOrStdout(), choice); err != nil {
				return err
			}
		}
		for _, gap := range proposal.Gaps {
			fmt.Fprintln(cmd.OutOrStdout(), "Unsupported need:", gap)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Review: %s tools review %s\n", quoteShell(state.executable()), proposal.ID)
		return err
	}}
	suggest.Flags().StringVar(&repo, "repo", "", "optional: use repository-bound GitHub templates instead of general CLI suggestions")
	suggest.Flags().StringVar(&model, "model", "", "suggestion model override (otherwise definition/default)")
	suggest.Flags().StringVarP(&output, "output", "o", "text", "output: text|json|yaml")
	var reviewRepo, reviewOutput string
	var selected []string
	var apply, yes bool
	review := &cobra.Command{Use: "review PROPOSAL_ID", Short: "Review an exact repository-scoped grant; --apply saves after confirmation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if reviewOutput != "text" && reviewOutput != "json" {
			return errors.New("output must be text, json or yaml")
		}
		if yes && !apply {
			return errors.New("--yes requires --apply")
		}
		p, err := app.ReadMarkdownToolProposal(args[0])
		if err != nil {
			return err
		}
		if p.SchemaVersion == 2 {
			if reviewRepo != "" {
				return errors.New("--repo only applies to GitHub-template proposals")
			}
			if !apply {
				if reviewOutput == "json" {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
				}
				for _, choice := range p.CLIChoices {
					if err := app.RenderCLIChoice(cmd.OutOrStdout(), choice); err != nil {
						return err
					}
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Use --apply --select NAME,... to add declarations; no execution permission is granted.")
				return err
			}
			names, err := app.CLIProposalSelection(p, selected)
			if err != nil {
				return err
			}
			for _, choice := range p.CLIChoices {
				if choice.Tool != nil && containsToolName(names, choice.Tool.Name) {
					if err = app.RenderCLIChoice(cmd.ErrOrStderr(), choice); err != nil {
						return err
					}
				}
			}
			if !yes {
				in := state.deps.stdin
				if in == nil || !term.IsTerminal(int(in.Fd())) {
					return errors.New("adding suggested tools requires a terminal or --apply --yes")
				}
				fmt.Fprint(cmd.ErrOrStderr(), "Add these declarations to the agent file? [y/N]: ")
				scanner := bufio.NewScanner(createPromptReader{in})
				if !scanner.Scan() {
					return errors.New("review cancelled")
				}
				answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
				if answer != "y" && answer != "yes" {
					return errors.New("review declined")
				}
			}
			if err = app.AddSuggestedCLITools(cmd.Context(), p, names); err != nil {
				return err
			}
			if reviewOutput == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"source": p.Source, "added": names, "granted": false})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Added tool declarations. Review executable resolution before running:\n  %s tools approve %s --apply\n", quoteShell(state.executable()), quoteShell(p.Source))
			return err
		}
		g, err := app.PrepareToolGrant(p, reviewRepo, selected)
		if err != nil {
			return err
		}
		if !apply {
			if reviewOutput == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"proposal": p, "grant": g, "applied": false})
			}
			if err = app.RenderToolReview(cmd.OutOrStdout(), p, g); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Use --apply to approve this exact binding; --select chooses a subset, --repo adjusts scope.")
			return err
		}
		if err = app.RenderToolReview(cmd.ErrOrStderr(), p, g); err != nil {
			return err
		}
		if !yes {
			in := state.deps.stdin
			if in == nil || !term.IsTerminal(int(in.Fd())) {
				return errors.New("tool approval requires an interactive terminal or explicit --apply --yes")
			}
			fmt.Fprint(cmd.ErrOrStderr(), "Approve these tools for this definition and repository? [y/N]: ")
			scanner := bufio.NewScanner(createPromptReader{in})
			if !scanner.Scan() {
				return errors.New("tool review cancelled; no grant applied")
			}
			answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
			if answer != "y" && answer != "yes" {
				return errors.New("tool grant declined")
			}
		}
		if err = app.ApplyToolGrant(p, g); err != nil {
			return err
		}
		if reviewOutput == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"grant": g, "applied": true})
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Approved local binding; the agent definition was not edited.\nStart: %s run %s\nUpdate an existing instance: %s run %s --update\n", quoteShell(state.executable()), quoteShell(p.Source), quoteShell(state.executable()), quoteShell(p.Source))
		return err
	}}
	review.Flags().StringVar(&reviewRepo, "repo", "", "explicit repository adjustment for review")
	review.Flags().StringSliceVar(&selected, "select", nil, "subset: github_pr_list,github_pr_read")
	review.Flags().BoolVar(&apply, "apply", false, "apply the displayed tool binding after explicit consent")
	review.Flags().BoolVar(&yes, "yes", false, "approve displayed scope noninteractively (requires --apply)")
	review.Flags().StringVarP(&reviewOutput, "output", "o", "text", "output: text|json|yaml")
	var cliApply, cliYes bool
	var cliOutput string
	approve := &cobra.Command{Use: "approve FILE", Short: "Review same-file CLI bindings and optionally validate/approve them", Long: "Resolve command names through PATH without running them. --apply asks before executing declared validation probes and saving an exact definition/executable grant. Probes are arbitrary local commands, not automatically trusted health checks. Default approval is no; --yes explicitly confirms for scripts.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if cliOutput != "text" && cliOutput != "json" {
			return errors.New("output must be text, json or yaml")
		}
		if cliYes && !cliApply {
			return errors.New("--yes requires --apply")
		}
		p, err := app.PrepareCLIToolApproval(args[0])
		if err != nil {
			return err
		}
		if !cliApply {
			if cliOutput == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
			}
			return app.RenderCLIApproval(cmd.OutOrStdout(), p)
		}
		if err = app.RenderCLIApproval(cmd.ErrOrStderr(), p); err != nil {
			return err
		}
		if !cliYes {
			in := state.deps.stdin
			if in == nil || !term.IsTerminal(int(in.Fd())) {
				return errors.New("CLI tool approval requires a terminal or explicit --apply --yes")
			}
			fmt.Fprint(cmd.ErrOrStderr(), "Run these probes and approve these exact local commands? [y/N]: ")
			scanner := bufio.NewScanner(createPromptReader{in})
			if !scanner.Scan() {
				return errors.New("approval cancelled")
			}
			answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
			if answer != "y" && answer != "yes" {
				return errors.New("approval declined")
			}
		}
		if err = app.ApplyCLIToolApproval(cmd.Context(), &p); err != nil {
			return err
		}
		if cliOutput == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
		}
		for _, result := range p.ProbeResults {
			fmt.Fprintln(cmd.OutOrStdout(), result)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Approved local tools. Start with run FILE, or activate on an existing agent with run FILE --update.\n")
		return err
	}}
	approve.Flags().BoolVar(&cliApply, "apply", false, "execute reviewed probes and save the grant after confirmation")
	approve.Flags().BoolVar(&cliYes, "yes", false, "explicitly approve reviewed local execution without prompting")
	approve.Flags().StringVarP(&cliOutput, "output", "o", "text", "output: text|json|yaml")
	group.AddCommand(suggest, review, approve)
	return group
}

func containsToolName(names []string, name string) bool {
	for _, v := range names {
		if v == name {
			return true
		}
	}
	return false
}
