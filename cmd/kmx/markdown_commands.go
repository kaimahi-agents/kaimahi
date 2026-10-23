package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/muesli/cancelreader"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Internal operation handlers shared by typed root commands. This container is
// never registered: md is an agent type, not a command namespace.
func markdownOperations(state *commandState) *cobra.Command {
	var tests, provider, model string
	var output string
	var selectedSession, label string
	var freshSession bool
	var directory string
	group := &cobra.Command{
		Use: "md [directory]", Short: "Browse and try .agent.md files with Copilot (experimental)",
		Long: "Browse native .agent.md files paired with .agent.test.md files. Chat, edit and test through the installed Copilot CLI.\nWith redirected input, list files only. Test metadata is saved privately under KMX_HOME; Copilot owns its session retention.",
		Args: cobra.MaximumNArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			format, _ := cmd.Flags().GetString("output")
			if format != "text" && format != "table" && format != "json" && format != "yaml" {
				return errors.New("output must be text, json or yaml")
			}
			if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
				return errors.New("md is a local preview; omit cluster and container-engine selectors")
			}
			for name, value := range map[string]string{"tests": tests, "provider": provider, "model": model} {
				if cmd.Flags().Changed(name) && strings.TrimSpace(value) == "" {
					return fmt.Errorf("--%s requires a value", name)
				}
			}
			if provider != "" && provider != "copilot" && cmd.Name() != "settings" {
				return errors.New("only --provider copilot is supported")
			}
			return nil
		},
	}
	group.PersistentFlags().StringVar(&tests, "tests", "", "explicit companion test file (otherwise .github/agent-tests/NAME.agent.test.md)")
	group.PersistentFlags().StringVar(&provider, "provider", "", "one-invocation provider override: copilot")
	group.PersistentFlags().StringVar(&model, "model", "", "one-invocation model override")
	group.PersistentFlags().StringVarP(&output, "output", "o", "text", "output: text|json|yaml (noninteractive operations)")
	group.PersistentFlags().StringVar(&directory, "directory", ".", "project directory containing agents")
	input := state.deps.stdin
	if input == nil {
		input = os.Stdin
	}
	withInput := func(cmd *cobra.Command, fn func(context.Context, io.Reader) error) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		reader, err := cancelreader.NewReader(input)
		if err != nil {
			// epoll/kqueue cannot watch regular files or /dev/null. These
			// redirected inputs are finite and can be read synchronously.
			if info, statErr := input.Stat(); statErr == nil && (info.Mode().IsRegular() || input.Name() == os.DevNull) {
				return fn(ctx, input)
			}
			return err
		}
		defer reader.Close()
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				reader.Cancel()
			case <-done:
			}
		}()
		return fn(ctx, reader)
	}
	session := func() *app.MarkdownSession {
		s := app.NewMarkdownSession(state.deps.stdout, state.deps.stderr)
		s.Provider, s.Model = provider, model
		return s
	}
	setup := func() error {
		if provider != "" {
			return nil
		}
		if !term.IsTerminal(int(input.Fd())) {
			return nil
		}
		accepted, err := app.SetupMarkdownProvider(input, state.deps.stderr, false)
		if err != nil {
			return err
		}
		if accepted {
			provider = "copilot"
		}
		return nil
	}
	structured := func(cmd *cobra.Command) bool {
		value, _ := cmd.Flags().GetString("output")
		return value == "json" || value == "yaml"
	}
	report := func(cmd *cobra.Command, value any) error { return json.NewEncoder(cmd.OutOrStdout()).Encode(value) }
	loadForExecution := func(cmd *cobra.Command, path string) (*app.MarkdownAgent, error) {
		a, err := app.LoadMarkdownAgent(path, tests)
		var diagnostic *app.MarkdownDiagnostic
		if errors.As(err, &diagnostic) && diagnostic.Code == "frontmatter" && strings.Contains(diagnostic.Repair, "no agent header") && strings.HasSuffix(path, ".agent.md") {
			if !term.IsTerminal(int(input.Fd())) {
				return nil, fmt.Errorf("%w\nImport interactively: %s import %s", err, state.executable(), quoteShell(path))
			}
			var saved string
			if e := withInput(cmd, func(ctx context.Context, reader io.Reader) error {
				var e error
				saved, e = app.ImportMarkdownAgent(ctx, path, reader, state.deps.stderr)
				if e == nil {
					e = app.OfferImportedTools(ctx, saved, model, reader, state.deps.stderr)
					if e != nil {
						return fmt.Errorf("import saved to %s; tool suggestions failed: %w", saved, e)
					}
				}
				return e
			}); e != nil {
				return nil, e
			}
			return app.LoadMarkdownAgent(saved, tests)
		}
		return a, err
	}
	group.AddCommand(&cobra.Command{Use: "import FILE", Short: "Import plain instructions as a native agent (interactive)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if structured(cmd) {
			return errors.New("interactive import does not produce a structured report; use validate -o yaml after importing")
		}
		if !term.IsTerminal(int(input.Fd())) {
			return errors.New("import requires an interactive terminal; add front matter explicitly for scripts")
		}
		return withInput(cmd, func(ctx context.Context, reader io.Reader) error {
			saved, err := app.ImportMarkdownAgent(ctx, args[0], reader, state.deps.stderr)
			if err != nil {
				return err
			}
			if err = app.OfferImportedTools(ctx, saved, model, reader, state.deps.stderr); err != nil {
				return fmt.Errorf("import saved to %s; tool suggestions failed: %w", saved, err)
			}
			return nil
		})
	}})
	group.AddCommand(&cobra.Command{Use: "show FILE", Short: "Read native instructions and paired tests offline", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.LoadMarkdownAgent(args[0], tests)
		if err != nil {
			return err
		}
		if structured(cmd) {
			return report(cmd, map[string]any{"name": a.Name, "type": "md", "source": a.Path, "version": a.Revision, "model": a.Model, "instructions": a.Instructions, "tests": a.TestsPath, "testsDigest": a.TestsRevision, "cases": len(a.Examples)})
		}
		return app.ShowMarkdownAgent(state.deps.stdout, a)
	}})
	var validationJSON bool
	validate := &cobra.Command{Use: "validate FILE", Short: "Validate definition and paired tests offline", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.LoadMarkdownAgent(args[0], tests)
		if validationJSON || structured(cmd) {
			if err != nil {
				var d *app.MarkdownDiagnostic
				if errors.As(err, &d) {
					_ = json.NewEncoder(state.deps.stdout).Encode(d)
				}
				return err
			}
			return json.NewEncoder(state.deps.stdout).Encode(map[string]any{"valid": true, "agentDigest": a.Revision, "testsDigest": a.TestsRevision, "cases": len(a.Examples)})
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(state.deps.stdout, "Valid · %s · %d tests\n", a.Name, len(a.Examples))
		return err
	}}
	validate.Flags().BoolVar(&validationJSON, "json", false, "emit structured validation diagnostics")
	group.AddCommand(validate)
	settings := &cobra.Command{Use: "settings", Short: "Choose or reset the remembered local provider", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("provider") {
			choice := ""
			switch provider {
			case "copilot":
				choice = "accepted"
			case "none":
				choice = "declined"
			case "reset":
			default:
				return errors.New("use --provider copilot, none or reset")
			}
			if err := app.SetMarkdownProvider(choice); err != nil {
				return err
			}
			if structured(cmd) {
				p, err := app.ReadMarkdownPreferences()
				if err != nil {
					return err
				}
				return report(cmd, p)
			}
			return nil
		}
		if structured(cmd) || !term.IsTerminal(int(input.Fd())) {
			p, err := app.ReadMarkdownPreferences()
			if err != nil {
				return err
			}
			return json.NewEncoder(state.deps.stdout).Encode(p)
		}
		_, err := app.SetupMarkdownProvider(input, state.deps.stderr, true)
		return err
	}}
	group.AddCommand(settings)
	group.AddCommand(&cobra.Command{Use: "init NAME", Short: "Create a native agent and paired tests without overwriting files", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := app.InitMarkdownAgent(directory, args[0]); err != nil {
			return err
		}
		file := filepath.Join(directory, ".github", "agents", args[0]+".agent.md")
		if structured(cmd) {
			return report(cmd, map[string]any{"created": true, "definition": file, "tests": app.MarkdownTestsPath(file)})
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Created definition: %s\nCreated tests: %s\n\nEdit the instructions below the --- header, then run:\n  %s run -i %s\n", file, app.MarkdownTestsPath(file), state.executable(), quoteShell(file))
		return err
	}})
	chat := &cobra.Command{Use: "chat FILE", Short: "Chat with an agent; --new creates an independent conversation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if structured(cmd) {
			return errors.New("interactive chat has no report output; use run --prompt with -o json or -o yaml")
		}
		if freshSession && selectedSession != "" {
			return errors.New("--new and --session are mutually exclusive")
		}
		if cmd.Flags().Changed("session") && strings.TrimSpace(selectedSession) == "" {
			return errors.New("--session requires an ID")
		}
		if label != "" && !freshSession {
			return errors.New("--label requires --new")
		}
		if instance, e := app.FindAgentInstance(args[0]); e == nil && instance.State == "running" && instance.Socket != "" {
			if cmd.Flags().Changed("model") || cmd.Flags().Changed("provider") {
				return errors.New("attached chat uses the running instance's model/provider")
			}
			s, a, err := app.NewSelectedAgentSession(cmd.Context(), instance.ID, selectedSession, freshSession, label, state.deps.stdout, state.deps.stderr)
			if err != nil {
				return err
			}
			defer s.Close()
			if term.IsTerminal(int(input.Fd())) {
				return s.ChatTUI(cmd.Context(), a, input)
			}
			return withInput(cmd, func(ctx context.Context, reader io.Reader) error { return s.Chat(ctx, a, reader) })
		}
		a, err := loadForExecution(cmd, args[0])
		if err != nil {
			return err
		}
		if err := setup(); err != nil {
			return err
		}
		s := session()
		s.SelectedSession, s.NewSession, s.SessionLabel = selectedSession, freshSession, label
		defer s.Close()
		if term.IsTerminal(int(input.Fd())) {
			return s.ChatTUI(cmd.Context(), a, input)
		}
		return withInput(cmd, func(ctx context.Context, reader io.Reader) error { return s.Chat(ctx, a, reader) })
	}}
	chat.Flags().StringVar(&selectedSession, "session", "", "resume a session ID belonging to this agent")
	chat.Flags().BoolVar(&freshSession, "new", false, "create a new independent conversation")
	chat.Flags().StringVar(&label, "label", "", "label for a new conversation (requires --new)")
	group.AddCommand(chat)
	var prompt, promptFile string
	var interactive, update, yes bool
	run := &cobra.Command{Use: "run FILE", Short: "Ask an agent one question through Copilot", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if yes && !update {
			return errors.New("--yes requires --update")
		}
		if update && (cmd.Flags().Changed("prompt") || cmd.Flags().Changed("prompt-file")) {
			return errors.New("--update uses -i for chat; run one-shot prompts separately")
		}
		if interactive && structured(cmd) {
			return errors.New("-i cannot be combined with structured output")
		}
		if cmd.Flags().Changed("prompt") && cmd.Flags().Changed("prompt-file") {
			return errors.New("provide only one of --prompt or --prompt-file (- for stdin)")
		}
		if interactive && (cmd.Flags().Changed("prompt") || cmd.Flags().Changed("prompt-file")) {
			return errors.New("use -i for chat, or --prompt/--prompt-file for one-shot execution")
		}
		a, err := loadForExecution(cmd, args[0])
		if err != nil {
			return err
		}
		if err := setup(); err != nil {
			return err
		}
		s := session()
		defer s.Close()
		if update {
			plan, err := s.PlanAgentUpdate(cmd.Context(), a)
			if err != nil {
				return err
			}
			if !plan.Noop {
				if term.IsTerminal(int(input.Fd())) && !yes {
					err = withInput(cmd, func(ctx context.Context, r io.Reader) error {
						return app.ConfirmAgentUpdate(ctx, state.deps.stderr, r, plan, false)
					})
				} else {
					err = app.ConfirmAgentUpdate(cmd.Context(), state.deps.stderr, nil, plan, yes)
				}
				if err != nil {
					return err
				}
			}
			r, err := s.ApplyAgentUpdate(cmd.Context(), plan)
			if err != nil {
				return err
			}
			if interactive {
				attached, frozen, e := app.NewSelectedAgentSession(cmd.Context(), r.ID, "", !plan.Noop, "", state.deps.stdout, state.deps.stderr)
				if e != nil {
					return e
				}
				defer attached.Close()
				if term.IsTerminal(int(input.Fd())) {
					return attached.ChatTUI(cmd.Context(), frozen, input)
				}
				return withInput(cmd, func(ctx context.Context, reader io.Reader) error { return attached.Chat(ctx, frozen, reader) })
			}
			if structured(cmd) {
				return report(cmd, map[string]any{"instance": r, "changed": !plan.Noop})
			}
			if plan.Noop {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Unchanged: %s already uses %s\nChat: %s chat %s\n", r.Name, r.Version, quoteShell(state.executable()), r.ID)
			} else {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Updated %s (%s) · version %s\nChat: %s chat %s\n", r.Name, app.ShortAgentID(r.ID), r.Version, quoteShell(state.executable()), r.ID)
			}
			return err
		}
		perform := func(ctx context.Context, reader io.Reader) error {
			if cmd.Flags().Changed("prompt-file") {
				if strings.TrimSpace(promptFile) == "" {
					return errors.New("--prompt-file requires a file or -")
				}
				if promptFile != "-" {
					info, err := os.Lstat(promptFile)
					if err != nil || !info.Mode().IsRegular() {
						return errors.New("prompt file must be regular")
					}
					f, err := os.Open(promptFile)
					if err != nil {
						return err
					}
					defer f.Close()
					info, err = f.Stat()
					if err != nil || !info.Mode().IsRegular() {
						return errors.New("prompt file must be regular")
					}
					reader = f
				}
				data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
				if err != nil {
					return err
				}
				if len(data) > 64<<10 {
					return errors.New("prompt exceeds 64 KiB")
				}
				prompt = string(data)
			}
			if structured(cmd) {
				if strings.TrimSpace(prompt) == "" || len(prompt) > 64<<10 {
					return errors.New("provide a nonempty prompt of at most 64 KiB")
				}
				answer, err := s.AnswerAgent(ctx, a, prompt)
				if err != nil {
					return err
				}
				return report(cmd, map[string]any{"name": a.Name, "version": a.Revision, "answer": answer.Text, "model": answer.Model})
			}
			return s.Run(ctx, a, prompt)
		}
		if !cmd.Flags().Changed("prompt") && !cmd.Flags().Changed("prompt-file") {
			if !interactive {
				record, err := s.StartBackgroundAgent(cmd.Context(), a)
				if err != nil {
					if errors.Is(err, app.ErrAgentVersionChanged) {
						return markdownUpdateHint(state, cmd, a.Path, err)
					}
					return err
				}
				if structured(cmd) {
					return report(cmd, record)
				}
				_, err = fmt.Fprintf(state.deps.stdout, "Started %s (%s) · version %s\nStop: %s stop %s\nChat: %s chat %s\n", record.Name, record.ID, record.Version, quoteShell(state.executable()), record.ID, quoteShell(state.executable()), record.ID)
				return err
			}
			if term.IsTerminal(int(input.Fd())) {
				return s.ChatTUI(cmd.Context(), a, input)
			}
			return withInput(cmd, func(ctx context.Context, reader io.Reader) error { return s.Chat(ctx, a, reader) })
		}
		if cmd.Flags().Changed("prompt-file") && promptFile == "-" {
			return withInput(cmd, perform)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return perform(ctx, nil)
	}}
	run.Flags().StringVar(&prompt, "prompt", "", "question to ask")
	run.Flags().BoolVarP(&interactive, "interactive", "i", false, "open foreground chat immediately (default: start a background instance)")
	run.Flags().BoolVar(&update, "update", false, "replace the running md version and close its conversations after confirmation")
	run.Flags().BoolVar(&yes, "yes", false, "confirm --update session replacement without prompting")
	run.Flags().StringVar(&promptFile, "prompt-file", "", "read question from a file, or - for stdin (64 KiB maximum)")
	group.AddCommand(run)
	var selected string
	var showContent bool
	test := &cobra.Command{Use: "test FILE", Short: "Run paired tests with fresh native Copilot sessions", Long: "Run paired checks and deterministic mock tool fixtures. --case is explicitly partial. Exit 0 means selected tests passed; 1 means failure or incomplete execution. Local preview only.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("case") && strings.TrimSpace(selected) == "" {
			return errors.New("--case requires an example ID")
		}
		a, err := app.LoadMarkdownAgent(args[0], tests)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		if err := setup(); err != nil {
			return err
		}
		s := session()
		defer s.Close()
		if structured(cmd) {
			result, err := s.Evaluate(ctx, a, selected, nil)
			if result.ID == "" {
				return err
			}
			if showContent {
				answers := map[string]string{}
				for _, row := range result.Cases {
					answers[row.ID] = row.Answer
				}
				return errors.Join(err, report(cmd, map[string]any{"result": result, "answers": answers}))
			}
			return errors.Join(err, report(cmd, result))
		}
		return s.Test(ctx, a, selected, showContent)
	}}
	test.Flags().StringVar(&selected, "case", "", "run only this example ID (partial result)")
	test.Flags().BoolVar(&showContent, "show-content", false, "print answers alongside check results")
	group.AddCommand(test)
	return group
}

func markdownUpdateHint(state *commandState, cmd *cobra.Command, path string, cause error) error {
	parts := []string{quoteShell(state.executable()), "run", quoteShell(path), "--update"}
	for _, flag := range []string{"tests", "provider", "model"} {
		if cmd.Flags().Changed(flag) {
			value, _ := cmd.Flags().GetString(flag)
			parts = append(parts, "--"+flag, quoteShell(value))
		}
	}
	return fmt.Errorf("%w\nUpdate the agent definition with:\n  %s", cause, strings.Join(parts, " "))
}
