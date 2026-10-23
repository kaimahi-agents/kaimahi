package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func markdownOperation(state *commandState, name string) *cobra.Command {
	group := markdownOperations(state)
	for _, c := range group.Commands() {
		if c.Name() == name {
			c.Flags().AddFlagSet(group.PersistentFlags())
			c.PreRunE = group.PersistentPreRunE
			group.RemoveCommand(c)
			return c
		}
	}
	panic("unknown markdown operation")
}

// addAgentTypeRoute wraps existing Orka commands without resolving Kubernetes
// configuration for local md agents. Explicit type wins; file suffix is enough
// for an unambiguous path. Registered local instance names resolve before Orka
// discovery unless the caller explicitly selects a cluster/runtime.
func addAgentTypeRoute(state *commandState, cmd *cobra.Command, operation string) {
	local := markdownOperation(state, operation)
	if operation != "init" {
		cmd.ValidArgsFunction = completeTypedAgents
	}
	if operation == "run" {
		_ = cmd.RegisterFlagCompletionFunc("agent", completeTypedAgents)
	}
	var kind string
	cmd.Flags().StringVar(&kind, "type", "", "agent type: md or orka (a .agent.md path selects md)")
	_ = cmd.RegisterFlagCompletionFunc("type", staticCompletion([]string{"md", "orka"}))
	for _, name := range []string{"tests", "provider", "directory"} {
		if cmd.Flags().Lookup(name) == nil {
			cmd.Flags().AddFlag(local.Flags().Lookup(name))
		}
	}
	if cmd.Flags().Lookup("output") == nil {
		cmd.Flags().AddFlag(local.Flags().Lookup("output"))
	}
	if operation != "init" && cmd.Flags().Lookup("model") == nil {
		cmd.Flags().AddFlag(local.Flags().Lookup("model"))
	}
	if operation == "run" {
		cmd.Flags().AddFlag(local.Flags().Lookup("interactive"))
		cmd.Flags().AddFlag(local.Flags().Lookup("update"))
		cmd.Flags().AddFlag(local.Flags().Lookup("yes"))
	}
	if operation == "chat" {
		cmd.Flags().AddFlag(local.Flags().Lookup("new"))
		cmd.Flags().AddFlag(local.Flags().Lookup("label"))
		cmd.Flags().Lookup("session").Hidden = false
		cmd.Flags().Lookup("session").Usage = "resume a local md conversation by session ID"
	}
	originalArgs, originalRun, originalPreRun := cmd.Args, cmd.RunE, cmd.PreRunE
	var selectionError error
	selected := func(cmd *cobra.Command, args []string) bool {
		selectionError = nil
		if kind != "" {
			return kind == "md"
		}
		if operation == "init" {
			return false
		}
		if len(args) > 0 && strings.HasSuffix(args[0], ".md") {
			if (operation == "run" || operation == "test") && isCanonicalCharter(args[0]) {
				return false
			}
			return true
		}
		value, _ := cmd.Flags().GetString("agent")
		if strings.HasSuffix(value, ".md") {
			return true
		}
		if state.contextFlag != "" || cmd.Flags().Changed("runtime") || cmd.Flags().Changed("namespace") {
			return false
		}
		if len(args) > 0 {
			value = args[0]
		}
		if operation == "test" && len(args) == 1 && value != "" {
			return true
		}
		if value == "" {
			return false
		}
		_, err := app.FindAgentInstance(value)
		if err == nil {
			return true
		}
		if !errors.Is(err, os.ErrNotExist) {
			selectionError = err
			return true
		}
		return false
	}
	localArgs := func(cmd *cobra.Command, args []string) ([]string, error) {
		if operation == "run" && len(args) == 0 {
			agent, _ := cmd.Flags().GetString("agent")
			if agent != "" {
				args = []string{agent}
			}
		}
		if len(args) != 1 {
			return nil, fmt.Errorf("usage: kmx %s --type md NAME_OR_FILE", cmd.Name())
		}
		if operation == "init" {
			return args, nil
		}
		if !strings.HasSuffix(args[0], ".md") {
			instance, err := app.FindAgentInstance(args[0])
			if err == nil {
				if operation == "chat" {
					if instance.State != "running" {
						return nil, fmt.Errorf("agent %s is stopped; start it with: kmx run %s", instance.Name, quoteShell(instance.Source))
					}
					if instance.Socket == "" {
						return nil, fmt.Errorf("agent %s is running in a foreground session; use that terminal, or exit it and run without -i to enable attachment", instance.Name)
					}
					return []string{instance.ID}, nil
				}
				return []string{instance.Source}, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		directory, _ := cmd.Flags().GetString("directory")
		path, err := app.ResolveMarkdownAgent(directory, args[0])
		return []string{path}, err
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if kind != "" && kind != "md" && kind != "orka" {
			return errors.New("--type must be md or orka")
		}
		if selected(cmd, args) {
			if selectionError != nil {
				return selectionError
			}
			for _, name := range []string{"runtime", "runtime-version", "namespace", "result-port", "wait", "workspace", "instructions", "members", "flow", "description", "kagent-runtime"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s is not supported for md agents", name)
				}
			}
			if operation == "init" {
				if cmd.Flags().Changed("model") {
					return errors.New("edit the native model field after creating the md agent")
				}
				if len(args) != 1 {
					return errors.New("create --type md requires a name")
				}
				return nil
			}
			if operation == "run" {
				if cmd.Flags().Changed("prompt") && cmd.Flags().Changed("prompt-file") {
					return errors.New("provide only one of --prompt or --prompt-file")
				}
			}
			if operation == "chat" {
				fresh, _ := cmd.Flags().GetBool("new")
				sid, _ := cmd.Flags().GetString("session")
				label, _ := cmd.Flags().GetString("label")
				if fresh && sid != "" {
					return errors.New("--new and --session are mutually exclusive")
				}
				if cmd.Flags().Changed("session") && strings.TrimSpace(sid) == "" {
					return errors.New("--session requires an ID")
				}
				if label != "" && !fresh {
					return errors.New("--label requires --new")
				}
			}
			_, err := localArgs(cmd, args)
			return err
		}
		if operation == "test" || (operation == "run" && len(args) == 1 && isCanonicalCharter(args[0])) {
			if originalArgs != nil {
				return originalArgs(cmd, args)
			}
			return nil
		}
		for _, name := range []string{"tests", "directory"} {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s requires --type md", name)
			}
		}
		if operation == "chat" && (cmd.Flags().Changed("new") || cmd.Flags().Changed("label")) {
			return errors.New("--new/--label require a local md agent")
		}
		if operation != "init" && cmd.Flags().Changed("model") {
			return errors.New("--model requires --type md")
		}
		if operation == "run" && (cmd.Flags().Changed("interactive") || cmd.Flags().Changed("update") || cmd.Flags().Changed("yes")) {
			return errors.New("--interactive/--update/--yes currently require an md agent")
		}
		if cmd.Flags().Changed("output") && operation != "show" {
			return errors.New("structured output on this route requires a local md agent")
		}
		if originalArgs != nil {
			return originalArgs(cmd, args)
		}
		return nil
	}
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if selected(cmd, args) {
			if selectionError != nil {
				return selectionError
			}
			return nil
		}
		if originalPreRun != nil {
			return originalPreRun(cmd, args)
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !selected(cmd, args) {
			return originalRun(cmd, args)
		}
		if selectionError != nil {
			return selectionError
		}
		for _, name := range []string{"provider", "model", "case", "show-content"} {
			if cmd.Flags().Changed(name) && local.Flags().Lookup(name) != nil {
				if err := local.Flags().Set(name, cmd.Flags().Lookup(name).Value.String()); err != nil {
					return err
				}
			}
		}
		resolved, err := localArgs(cmd, args)
		if err != nil {
			return err
		}
		if local.PreRunE != nil {
			if err := local.PreRunE(cmd, resolved); err != nil {
				return err
			}
		}
		// Shared prompt flags are owned by the existing root Run command.
		if operation == "chat" && cmd.Flags().Changed("session") {
			v, _ := cmd.Flags().GetString("session")
			if err := local.Flags().Set("session", v); err != nil {
				return err
			}
		}
		if operation == "show" {
			instance, e := app.FindAgentInstance(args[0])
			if e == nil {
				output, _ := cmd.Flags().GetString("output")
				return app.InspectAgentInstance(cmd.Context(), cmd.OutOrStdout(), instance.ID, output)
			}
			if !errors.Is(e, os.ErrNotExist) {
				return e
			}
		}
		if operation == "run" {
			for _, name := range []string{"prompt", "prompt-file"} {
				if cmd.Flags().Changed(name) {
					v, _ := cmd.Flags().GetString(name)
					if err := local.Flags().Set(name, v); err != nil {
						return err
					}
				}
			}
		}
		return local.RunE(cmd, resolved)
	}
	cmd.Short = strings.ReplaceAll(cmd.Short, "Orka Agent", "agent")
	cmd.Long += "\n\nRegistered local agent names/IDs and Markdown file paths resolve locally before Orka discovery. Explicit --type or cluster/runtime selectors take precedence. Use --type md NAME_OR_FILE for source discovery with --directory DIR."
}

// Canonical Squad charters retain upstream Run/Test semantics. Copilot-native
// front matter (or plain instructions to import) uses the explicit md instance path.
func isCanonicalCharter(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1024), 64<<10)
	if !s.Scan() {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(s.Text()), "# ")
}

func newTypedAgentCommand(state *commandState, name, operation string) *cobra.Command {
	cmd := markdownOperation(state, operation)
	cmd.Use = name + " NAME_OR_FILE"
	var kind string
	cmd.Flags().StringVar(&kind, "type", "md", "agent type (md)")
	_ = cmd.RegisterFlagCompletionFunc("type", staticCompletion([]string{"md"}))
	args, run := cmd.Args, cmd.RunE
	cmd.Args = func(c *cobra.Command, a []string) error {
		if kind != "md" {
			return fmt.Errorf("%s currently supports --type md; use kmx eval for full AgentSuite evaluation", name)
		}
		if args != nil {
			return args(c, a)
		}
		return nil
	}
	cmd.RunE = func(c *cobra.Command, a []string) error {
		directory, _ := c.Flags().GetString("directory")
		path, err := app.ResolveMarkdownAgent(directory, a[0])
		if err != nil {
			return err
		}
		return run(c, []string{path})
	}
	return cmd
}

func newInspectAgentCommand(state *commandState) *cobra.Command {
	cmd := newAgentShowCommand(state)
	cmd.Use = "inspect ID_OR_NAME_OR_FILE"
	addAgentTypeRoute(state, cmd, "show")
	return cmd
}

func newListTypedAgentsCommand(state *commandState) *cobra.Command {
	var kind, directory, namespace string
	var definitions, all bool
	var output string
	cmd := &cobra.Command{Use: "list", Short: "List running agents; --all includes stopped instances", Long: "List tracked local agent instances, not source files. Use --all for stopped instances, --definitions for authoring files, and inspect ID for details. Local status is a compatibility alias of this inventory.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if output != "table" && output != "json" {
			return errors.New("list output must be table or json")
		}
		if cmd.Flags().Changed("directory") && strings.TrimSpace(directory) == "" {
			return errors.New("--directory requires a directory")
		}
		if kind != "" && kind != "md" && kind != "orka" {
			return errors.New("--type must be md or orka")
		}
		if !definitions && kind != "orka" {
			if state.contextFlag != "" || cmd.Flags().Changed("namespace") || cmd.Flags().Changed("container-engine") {
				return errors.New("local instance list does not use cluster selectors; use --type orka")
			}
			return app.ListAgentInstances(cmd.OutOrStdout(), directory, output, all)
		}
		if all {
			return errors.New("--all applies to local instances, not definitions or Orka listing")
		}
		if directory == "" {
			directory = "."
		}
		if kind == "md" {
			if state.contextFlag != "" || cmd.Flags().Changed("namespace") {
				return errors.New("md agents are local files; omit cluster selectors")
			}
			return app.ListTypedAgentsOutput(cmd.Context(), nil, state.deps.stdout, directory, "md", namespace, output)
		}
		a, err := state.application()
		if err != nil {
			return err
		}
		return app.ListTypedAgentsOutput(cmd.Context(), a, state.deps.stdout, directory, kind, namespace, output)
	}}
	cmd.Flags().StringVar(&kind, "type", "", "filter agent type: md or orka")
	cmd.Flags().StringVar(&directory, "directory", "", "filter instance source directory; definitions default to current directory")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include stopped local instances")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json (local instances)")
	cmd.Flags().BoolVar(&definitions, "definitions", false, "list source definitions rather than tracked instances")
	cmd.Flags().StringVar(&namespace, "namespace", app.OrkaNamespace, "Orka namespace")
	_ = cmd.RegisterFlagCompletionFunc("type", staticCompletion([]string{"md", "orka"}))
	return cmd
}

func newInfoCommand(state *commandState) *cobra.Command {
	var directory, output string
	cmd := &cobra.Command{Use: "info", Short: "Show local inference setup and available source definitions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(directory) == "" {
			return errors.New("--directory requires a directory")
		}
		if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
			return errors.New("info describes local setup; omit cluster selectors")
		}
		return app.LocalStatus(cmd.Context(), cmd.OutOrStdout(), directory, output)
	}}
	cmd.Flags().StringVar(&directory, "directory", ".", "source definition directory")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json")
	return cmd
}

func newStopAgentCommand(state *commandState) *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "stop NAME_OR_ID", Short: "Stop a background local agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if output != "text" && output != "json" {
			return errors.New("output must be text, json or yaml")
		}
		if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
			return errors.New("stop selects a local background instance, not a cluster")
		}
		if err := app.StopAgentInstance(cmd.Context(), args[0]); err != nil {
			return err
		}
		if output == "json" {
			return app.InspectAgentInstance(cmd.Context(), cmd.OutOrStdout(), args[0], "json")
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Stopped", args[0])
		return err
	}}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output: text|json|yaml")
	cmd.ValidArgsFunction = completeTypedAgents
	return cmd
}

func newRemoveAgentCommand(state *commandState) *cobra.Command {
	var force bool
	var output string
	cmd := &cobra.Command{Use: "rm NAME_OR_ID", Short: "Remove a stopped local agent instance", Long: "Remove a local instance from inventory. --force stops a running background instance first. Source definitions, cached versions and test history are preserved. Private generation bookkeeping prevents stale clients targeting a later instance.", Args: cobra.ExactArgs(1), ValidArgsFunction: completeTypedAgents, RunE: func(cmd *cobra.Command, args []string) error {
		if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
			return errors.New("rm selects local instances, not cluster resources")
		}
		if output != "text" && output != "json" {
			return errors.New("output must be text, json or yaml")
		}
		r, err := app.RemoveAgentInstance(cmd.Context(), args[0], force)
		if err != nil {
			return err
		}
		if output == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"id": r.ID, "name": r.Name, "state": "removed"})
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s (%s)\n", r.Name, app.ShortAgentID(r.ID))
		return err
	}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "stop a running instance before removing it")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output: text|json|yaml")
	return cmd
}
