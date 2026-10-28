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

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	kmxmcp "github.com/kaimahi-agents/kaimahi/internal/kmx/mcp"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

type fileCommandOptions struct {
	CaseID, Provider, Endpoint, Output string
	Model, TestsFile                   string
	PositionalInput                    bool
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
		cmd.Flags().BoolVar(&f.GitHubSummary, "github-summary", false, "JSON summary to $GITHUB_STEP_SUMMARY (-o json)")
	}
	if output {
		cmd.Flags().StringVarP(&f.Output, "output", "o", "text", "File output: text or json")
	}
}
func validateFileCommand(cmd *cobra.Command, state *commandState, f fileCommandOptions) error {
	if state.contextFlag != "" {
		return fmt.Errorf("file preview usage refuses --context")
	}
	var invalid string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "case", "tests", "output", "no-content", "model", "provider", "provider-url", "prompt", "prompt-file", "verbose", "tool-bindings", "mcp-server", "no-mcp-servers", "github-summary":
		default:
			invalid = flag.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("file preview refuses --%s", invalid)
	}
	for _, name := range []string{"case", "tests", "model", "provider", "provider-url", "prompt-file", "tool-bindings"} {
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
	if f.Output != "text" && f.Output != "json" {
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
	opt := app.FilePreviewOptions{File: file, Operation: cmd.Name(), CaseID: f.CaseID,
		Model: f.Model, DefaultCopilot: true, AdHocOnly: f.PositionalInput && f.CaseID == "", ShowFailedContent: !f.NoContent,
		SkipAssertions:        cmd.Name() == "run",
		IncludeAnswer:         cmd.Name() == "run" && !f.NoContent,
		IncludeFailureDetails: !f.NoContent, ToolBindings: f.ToolBindings}
	opt.RouteSelected = func(route app.FilePreviewRoute) error {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Route: %s / %s\n", route.Provider, route.Model)
		return err
	}
	if cmd.Name() == "test" {
		opt.RetentionFile = file
	}
	// Native Windows passes the exact pair admitted by the platform gate. Other
	// platforms load once here; later checks validate, never replace, this input.
	pair := state.windowsFilePair
	var pairErr error
	if state.deps.goos == "windows" {
		if pair == nil || !windowsPackageFreeFakeAgent(pair.Agent) || f.Provider != "" || f.Endpoint != "" || f.ToolBindings != "" || len(f.MCPServers) > 0 || f.NoMCPServers || f.GitHubSummary || promptFile != "" {
			return errWindowsOperationUnsupported
		}
	} else {
		pair, pairErr = agentfile.LoadPair(file, f.TestsFile)
	}
	if pair != nil {
		opt.SourceFile = pair.AgentPath
		opt.ValidateSource = func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			selected := ""
			if pair.Tests.Selection.Explicit {
				selected = pair.Tests.Selection.Path
			}
			current, err := agentfile.LoadPair(pair.AgentPath, selected)
			if err != nil || !pair.SameSourceVersion(current) {
				return agentfile.ErrRecordingChanged
			}
			return nil
		}
	}
	if state.deps.goos == "windows" && opt.ValidateSource(cmd.Context()) != nil {
		return errWindowsOperationUnsupported
	}
	if f.Verbose {
		opt.Progress = func(message string) error {
			_, err := fmt.Fprintf(cmd.ErrOrStderr(), "[verbose] %s\n", message)
			return err
		}
	}
	if f.Provider != "" || f.Endpoint != "" {
		route := fileModelRoute(f.Provider, f.Endpoint)
		opt.Route = &route
	}
	if flag := cmd.Flag("prompt"); f.PositionalInput || flag != nil && flag.Changed {
		opt.Input = &prompt
	}
	if flag := cmd.Flag("prompt-file"); flag != nil && flag.Changed {
		var reader io.Reader = state.deps.stdin
		if promptFile != "-" {
			info, err := os.Stat(promptFile)
			if err != nil || !info.Mode().IsRegular() || info.Size() > agentfile.MaxInputBytes {
				return fileUsageError(fmt.Errorf("prompt file must be a readable regular file of at most 16 KiB"))
			}
			input, err := os.Open(promptFile)
			if err != nil {
				return fileUsageError(fmt.Errorf("cannot read prompt file"))
			}
			defer input.Close()
			reader = input
		}
		if reader == nil {
			return fileUsageError(fmt.Errorf("prompt stdin is unavailable"))
		}
		b, err := io.ReadAll(io.LimitReader(reader, agentfile.MaxInputBytes+1))
		if err != nil || len(b) > agentfile.MaxInputBytes {
			return fileUsageError(fmt.Errorf("prompt input must be readable and at most 16 KiB"))
		}
		input := string(b)
		opt.Input = &input
	}
	var summary io.WriteCloser
	if f.GitHubSummary {
		var err error
		summary, err = state.deps.openGitHubSummary()
		if err != nil {
			return fileUsageError(err)
		}
	}
	// Intake precedes signal interception: blocking stdin cannot observe cancellation.
	// User think time must not consume the preview execution budget.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	opt.PreparePairRecordingSave = kmxmcp.FilePairRecordingSaver
	selection := localtools.ServerSelection{Names: append([]string(nil), f.MCPServers...), Override: cmd.Flags().Changed("mcp-server"), Clear: f.NoMCPServers}
	ctx = context.WithValue(ctx, fileMCPIntentKey{}, fileMCPIntent{file: file, selection: selection})
	a := &app.App{FileRunner: state.deps.fileRunner}
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
	if f.Verbose {
		if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "[verbose] Total elapsed=%s\n",
			time.Since(start).Round(time.Millisecond)); writeErr != nil {
			return fmt.Errorf("cannot write local preview diagnostics")
		}
	}
	err = fileCommandResultError(result, err)
	if err != nil && fileSignInFailure(result.ReasonCode) {
		result.Reason = err.Error()
	}
	var outputErr error
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
			outputErr = app.WriteFilePreview(cmd.OutOrStdout(), result, f.Output)
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
		err = fmt.Errorf("Copilot sign-in unavailable: run gh auth login, or use --provider ollama --provider-url URL")
	}
	return &fileCommandError{cause: err, exit: code}
}
