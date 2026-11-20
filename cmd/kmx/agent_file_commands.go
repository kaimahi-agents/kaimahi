package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	kmxmcp "github.com/kaimahi-agents/kaimahi/internal/kmx/mcp"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

type fileCommandOptions struct {
	Runner, Host                       string
	CaseID, Provider, Endpoint, Output string
	Model, TestsFile                   string
	InputOnly                          bool
	ToolBindings                       string
	MCPServers                         []string
	NoMCPServers                       bool
	GitHubSummary                      bool
	NoContent                          bool
	Verbose                            bool
}

func fileModelRoute(provider, endpoint string) modelaccess.Route {
	if provider == "" {
		provider = "copilot"
	}
	route := modelaccess.Route{Provider: provider, Endpoint: endpoint, Auth: "none"}
	if provider == "copilot" {
		route.Auth = "copilot"
		if endpoint == "" {
			route.Endpoint = modelaccess.CopilotEndpoint
		}
	}
	return route
}

func registerFileCommandFlags(cmd *cobra.Command, f *fileCommandOptions, output bool) {
	cmd.Flags().BoolVar(&f.Verbose, "verbose", false, "Content-free diagnostics and timings on stderr")
	cmd.Flags().StringVar(&f.CaseID, "case", "", "One saved example ID (partial preview)")
	cmd.Flags().StringVar(&f.TestsFile, "tests", "", "Tests file override (FILE)")
	cmd.Flags().StringVar(&f.Provider, "provider", "", "copilot (default), fake, ollama, foundry-local, compatible")
	cmd.Flags().StringVar(&f.Endpoint, "provider-url", "", "Required local model URL; fixed for Copilot")
	cmd.Flags().StringVar(&f.Model, "model", "", "Model override for this invocation")
	cmd.Flags().BoolVar(&f.NoContent, "no-content", false, "Hide answers and failed assertion content")
	cmd.Flags().StringVar(&f.ToolBindings, "tool-bindings", "", "Fixed host binding file (takes precedence)")
	cmd.Flags().StringArrayVar(&f.MCPServers, "mcp-server", nil, "MCP server `NAME` (repeatable; replaces saved)")
	cmd.Flags().BoolVar(&f.NoMCPServers, "no-mcp-servers", false, "Clear saved and explicit MCP servers")
	if cmd.Name() == "test" {
		cmd.Flags().StringVar(&f.Runner, "runner", "embedded", "Execution runner: embedded or copilotd (FILE only)")
		cmd.Flags().StringVar(&f.Host, "host", "", "Literal loopback WebSocket host for copilotd (FILE only)")
		cmd.Flags().BoolVar(&f.GitHubSummary, "github-summary", false, "JSON summary to $GITHUB_STEP_SUMMARY (-o json)")
	}
	if output {
		help := "File output: text or json"
		if cmd.Name() == "test" {
			help = "File output: text, json or jsonl (explicit FILE)"
		}
		cmd.Flags().StringVarP(&f.Output, "output", "o", "text", help)
	}
}
func validateFileCommand(cmd *cobra.Command, state *commandState, f fileCommandOptions) error {
	if err := validateNativeFileCommand(cmd, f); err != nil {
		return err
	}
	if state.contextFlag != "" {
		return fmt.Errorf("file preview usage refuses --context")
	}
	var invalid string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "runner", "host", "case", "tests", "output", "no-content", "model", "provider", "provider-url", "prompt", "prompt-file", "verbose", "tool-bindings", "mcp-server", "no-mcp-servers", "github-summary", "once", "check", "upgrade", "new", "name", "yes", "provider-key-env", "model-profile", "require-context", "require-output", "require-reasoning":
		default:
			invalid = flag.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("file preview refuses --%s", invalid)
	}
	for _, name := range []string{"runner", "host", "case", "tests", "model", "provider", "provider-url", "prompt-file", "tool-bindings"} {
		if flag := cmd.Flag(name); flag != nil && flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
			return fmt.Errorf("file preview requires nonempty --%s", name)
		}
	}
	if !f.NoMCPServers {
		for _, name := range f.MCPServers {
			if !localtools.ValidToolName(name) {
				return fmt.Errorf("--mcp-server requires a nonempty configured server name")
			}
		}
	}
	if f.Output != "text" && f.Output != "json" && !(cmd.Name() == "test" && f.Output == "jsonl") {
		return fmt.Errorf("file preview output must be text or json")
	}
	if f.GitHubSummary && f.Output != "json" {
		return fmt.Errorf("--github-summary requires -o json")
	}
	return nil
}
func fileFlagsWithoutFile(cmd *cobra.Command) error {
	for _, name := range []string{"case", "tests", "model", "provider", "provider-url", "no-content", "verbose", "tool-bindings", "mcp-server", "no-mcp-servers", "github-summary"} {
		if flag := cmd.Flag(name); flag != nil && flag.Changed {
			return fmt.Errorf("--%s requires an explicit agent FILE", name)
		}
	}
	return nil
}

func runFileCommand(cmd *cobra.Command, state *commandState, file string, f fileCommandOptions, prompt, promptFile string) error {
	return runFileCommandWithPair(cmd, state, file, nil, f, prompt, promptFile)
}

func runFileCommandWithPair(cmd *cobra.Command, state *commandState, file string, frozen *agentfile.Pair, f fileCommandOptions, prompt, promptFile string) error {
	if f.Output == "jsonl" {
		m := state.machine
		outer := m != nil && m.file != nil
		if !outer {
			m = newFileOutput(cmd.OutOrStdout())
			state.machine = m
		}
		m.file.entered = true
		m.file.preflightStarted = true
		runner := f.Runner
		if runner == "" {
			runner = "embedded"
		}
		m.file.observer = app.NewMachineFileObserver(runner, func(p app.MachineFileProgress) error { return m.fileProgress(p) })
		if err := m.event("preflight", "active"); err != nil {
			if outer {
				return err
			}
			return m.finishFile(err)
		}
		r, err := previewFileCommand(cmd, state, file, frozen, f, prompt, promptFile, nil)
		m.file.result = r
		if outer {
			return err
		}
		return m.finishFile(err)
	}
	var summary io.WriteCloser
	result, err := previewFileCommand(cmd, state, file, frozen, f, prompt, promptFile, func() error {
		if !f.GitHubSummary {
			return nil
		}
		var err error
		summary, err = state.deps.openGitHubSummary()
		return fileUsageError(err)
	})
	if result.SchemaVersion == 0 {
		if summary != nil {
			_ = summary.Close()
		}
		return err
	}
	var outputErr error
	displayResult := result
	if f.Runner == "copilotd" {
		for _, notice := range result.Notices {
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), notice); err != nil {
				return fmt.Errorf("cannot write local preview diagnostics")
			}
		}
		if f.Output == "text" {
			displayResult.Notices = nil
		}
	}
	// Text auth failures have one actionable stderr error, not a second report.
	if f.Output == "json" || !fileSignInFailure(result.ReasonCode) {
		if cmd.Name() == "run" && f.Output == "text" {
			if result.State == "complete" {
				outputErr = app.WriteFileRunAnswers(cmd.OutOrStdout(), result)
			}
			if outputErr == nil {
				outputErr = app.WriteFilePreview(cmd.ErrOrStderr(), result, "text")
			}
		} else {
			outputErr = app.WriteFilePreview(cmd.OutOrStdout(), displayResult, f.Output)
		}
	}
	if outputErr != nil {
		if summary != nil {
			_ = summary.Close()
		}
		return fmt.Errorf("cannot write local preview output")
	}
	var summaryErr, closeErr error
	if summary != nil {
		summaryErr = app.WriteFilePreviewGitHubSummary(summary, result)
		closeErr = summary.Close()
	}
	if summaryErr != nil || closeErr != nil {
		return fmt.Errorf("cannot write GitHub Actions summary")
	}
	return err
}

// previewFileCommand preserves one checked pair and returns the shared safe result.
// beforeRun admits a presentation sink after intake but before execution effects.
func previewFileCommand(cmd *cobra.Command, state *commandState, file string, frozen *agentfile.Pair, f fileCommandOptions, prompt, promptFile string, beforeRun func() error) (app.FilePreviewResult, error) {
	opt := app.FilePreviewOptions{File: file, Operation: cmd.Name(), CaseID: f.CaseID,
		Model: f.Model, DefaultCopilot: true, AdHocOnly: f.InputOnly && f.CaseID == "", ShowFailedContent: !f.NoContent,
		SkipAssertions:        cmd.Name() == "run",
		IncludeAnswer:         cmd.Name() == "run" && !f.NoContent,
		IncludeFailureDetails: !f.NoContent, ToolBindings: f.ToolBindings}
	machine := f.Output == "jsonl" && state.machine != nil && state.machine.file != nil
	if machine {
		opt.Machine = state.machine.file.observer
		opt.ShowContent = false
		opt.ShowFailedContent = false
		opt.IncludeAnswer = false
		opt.IncludeFailureDetails = false
	}
	opt.Copilotd = f.Runner == "copilotd"
	if opt.Copilotd {
		if state.deps.goos == "windows" {
			return app.FilePreviewResult{}, errWindowsOperationUnsupported
		}
		if !machine {
			opt.SessionCreated = nativeSessionCreated(cmd.ErrOrStderr())
		}
	}
	opt.RouteSelected = func(route app.FilePreviewRoute) error {
		if machine {
			return nil
		}
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Route: %s / %s\n", route.Provider, route.Model)
		return err
	}
	if cmd.Name() == "test" {
		opt.RetentionFile = file
	}
	// Native Windows passes the exact pair admitted by the platform gate. Other
	// platforms load once here; later checks validate, never replace, this input.
	pair := frozen
	if pair == nil {
		pair = state.windowsFilePair
	}
	var pairErr error
	if state.deps.goos == "windows" {
		if pair == nil || !windowsPackageFreeFakeAgent(pair.Agent) || f.Provider != "" || f.Endpoint != "" || f.ToolBindings != "" || len(f.MCPServers) > 0 || f.NoMCPServers || f.GitHubSummary || promptFile != "" {
			return app.FilePreviewResult{}, errWindowsOperationUnsupported
		}
	} else if pair == nil {
		load := agentfile.LoadPair
		if opt.Copilotd {
			load = agentfile.LoadCopilotdPair
		}
		pair, pairErr = load(file, f.TestsFile)
	}
	if pair != nil && frozen == nil {
		opt.SourceFile = pair.AgentPath
		opt.ValidateSource = func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			selected := ""
			if pair.Tests.Selection.Explicit {
				selected = pair.Tests.Selection.Path
			}
			load := agentfile.LoadPair
			if opt.Copilotd {
				load = agentfile.LoadCopilotdPair
			}
			current, err := load(pair.AgentPath, selected)
			if err != nil || !pair.SameSourceVersion(current) {
				return agentfile.ErrRecordingChanged
			}
			return nil
		}
	}
	if state.deps.goos == "windows" && opt.ValidateSource(cmd.Context()) != nil {
		return app.FilePreviewResult{}, errWindowsOperationUnsupported
	}
	if f.Verbose && !machine {
		opt.Progress = func(message string) error {
			_, err := fmt.Fprintf(cmd.ErrOrStderr(), "[verbose] %s\n", message)
			return err
		}
	}
	if opt.Copilotd || f.Provider != "" || f.Endpoint != "" {
		route := fileModelRoute(f.Provider, f.Endpoint)
		opt.Route = &route
	}
	if flag := cmd.Flag("prompt"); flag != nil && flag.Changed {
		opt.Input = &prompt
	}
	if flag := cmd.Flag("prompt-file"); flag != nil && flag.Changed {
		input, err := readPromptInput(state, promptFile)
		if err != nil {
			return app.FilePreviewResult{}, err
		}
		opt.Input = &input
	}
	if beforeRun != nil {
		if err := beforeRun(); err != nil {
			return app.FilePreviewResult{}, err
		}
	}
	// Intake precedes signal interception: blocking stdin cannot observe cancellation.
	// User think time must not consume the preview execution budget.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if machine {
		state.machine.cancel = cancel
	}
	opt.PreparePairRecordingSave = kmxmcp.FilePairRecordingSaver
	selection := localtools.ServerSelection{Names: append([]string(nil), f.MCPServers...), Override: cmd.Flags().Changed("mcp-server"), Clear: f.NoMCPServers, Commands: true}
	ctx = context.WithValue(ctx, fileMCPIntentKey{}, fileMCPIntent{file: file, selection: selection})
	a := &app.App{FileRunner: state.deps.fileRunner}
	if opt.Copilotd {
		a.FileRunner = app.NewCopilotdFileRunner(f.Host, copilotdSnapshots(pair))
	}
	start := time.Now()
	var result app.FilePreviewResult
	var err error
	if pairErr != nil {
		// Reuse the shared refusal/redaction projection; never fall back to
		// executing an agent-only file after a selected tests source failed.
		result, err = a.PreviewAgentSnapshot(ctx, nil, opt)
		var detail *agentfile.Error
		if errors.As(pairErr, &detail) {
			result.Diagnostic = &app.FilePreviewDiagnostic{Field: detail.Field, Line: detail.Location.Line, Column: detail.Location.Column, Repair: detail.Problem}
		}
	} else {
		if cmd.Name() == "test" {
			opt = app.WithPairFilePreviewRetention(opt, pair)
		}
		result, err = a.PreviewAgentPairSnapshot(ctx, pair, opt)
	}
	if f.Verbose && !machine {
		if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "[verbose] Total elapsed=%s\n",
			time.Since(start).Round(time.Millisecond)); writeErr != nil {
			return app.FilePreviewResult{}, fmt.Errorf("cannot write local preview diagnostics")
		}
	}
	err = fileCommandResultError(result, err)
	if err != nil && fileSignInFailure(result.ReasonCode) {
		result.Reason = err.Error()
	}
	return result, err
}

func readPromptInput(state *commandState, promptFile string) (string, error) {
	var reader io.Reader = state.deps.stdin
	if promptFile != "-" {
		info, err := os.Stat(promptFile)
		if err != nil || !info.Mode().IsRegular() || info.Size() > agentfile.MaxInputBytes {
			return "", fileUsageError(fmt.Errorf("prompt file must be a readable regular file of at most 16 KiB"))
		}
		input, err := os.Open(promptFile)
		if err != nil {
			return "", fileUsageError(fmt.Errorf("cannot read prompt file"))
		}
		defer input.Close()
		reader = input
	}
	if reader == nil {
		return "", fileUsageError(fmt.Errorf("prompt stdin is unavailable"))
	}
	b, err := io.ReadAll(io.LimitReader(reader, agentfile.MaxInputBytes+1))
	if err != nil || len(b) > agentfile.MaxInputBytes || !utf8.Valid(b) {
		return "", fileUsageError(fmt.Errorf("prompt input must be readable UTF-8 of at most 16 KiB"))
	}
	return string(b), nil
}

// Keep CLI status choices out of the shared App/MCP result contract.
type fileCommandError struct {
	cause error
	exit  int
}

func (e *fileCommandError) Error() string { return e.cause.Error() }
func (e *fileCommandError) Unwrap() error { return e.cause }
func fileUsageError(err error) error {
	if err == nil {
		return nil
	}
	return &fileCommandError{cause: err, exit: 2}
}
func fileSignInFailure(code string) bool {
	return code == "copilot_auth_required" || code == "copilot_auth_revoked" || code == "copilot_keychain_unavailable"
}
func fileCommandResultError(r app.FilePreviewResult, err error) error {
	if err == nil {
		return nil
	}
	code := 2
	if r.State == "complete" && r.Verdict == "fail" || r.ReasonCode == "diagnostics_failed" {
		code = 1
	}
	var preview *app.FilePreviewError
	if errors.As(err, &preview) && preview.ExitCode() == 130 {
		code = 130
	}
	if fileSignInFailure(r.ReasonCode) {
		if strings.HasPrefix(r.Runner, "copilotd native execution") {
			err = fmt.Errorf("Copilot sign-in unavailable: unlock your keyring, or sign in with gh auth login --hostname github.com")
		} else {
			err = fmt.Errorf("Copilot sign-in unavailable: run gh auth login, or use --provider ollama --provider-url URL")
		}
	}
	return &fileCommandError{cause: err, exit: code}
}
