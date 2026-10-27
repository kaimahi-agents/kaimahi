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
	kmxmcp "github.com/kaimahi-agents/kaimahi/internal/kmx/mcp"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

type fileCommandOptions struct {
	CaseID, Provider, Endpoint, Output string
	Model                              string
	PositionalInput                    bool
	ToolBindings                       string
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
	cmd.Flags().BoolVar(&f.Verbose, "verbose", false, "show content-free file preview diagnostics and timings on stderr (requires FILE)")
	cmd.Flags().StringVar(&f.CaseID, "case", "", "run only this saved example ID (partial local preview)")
	cmd.Flags().StringVar(&f.Provider, "provider", "", "file-model provider: copilot (default), fake (use --model fake), ollama, foundry-local or compatible; no fallback")
	cmd.Flags().StringVar(&f.Endpoint, "provider-url", "", "model endpoint (required for local providers; fixed for Copilot)")
	cmd.Flags().StringVar(&f.Model, "model", "", "override the file model for this invocation")
	cmd.Flags().BoolVar(&f.NoContent, "no-content", false, "omit answers, failure operands and judge content (requires FILE)")
	cmd.Flags().StringVar(&f.ToolBindings, "tool-bindings", "", "explicit host-owned tool binding file (requires FILE; supports local and Copilot models)")
	if cmd.Name() == "test" {
		cmd.Flags().BoolVar(&f.GitHubSummary, "github-summary", false, "append the content-free JSON report summary to $GITHUB_STEP_SUMMARY (requires FILE and -o json)")
	}
	if output {
		cmd.Flags().StringVarP(&f.Output, "output", "o", "text", "file preview output: text|json")
	}
}
func validateFileCommand(cmd *cobra.Command, state *commandState, f fileCommandOptions) error {
	if state.contextFlag != "" {
		return fmt.Errorf("file preview usage refuses --context")
	}
	var invalid string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "case", "output", "no-content", "model", "provider", "provider-url", "prompt", "prompt-file", "verbose", "tool-bindings", "github-summary":
		default:
			invalid = flag.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("file preview refuses --%s", invalid)
	}
	for _, name := range []string{"case", "model", "provider", "provider-url", "prompt-file", "tool-bindings"} {
		if flag := cmd.Flag(name); flag != nil && flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
			return fmt.Errorf("file preview requires nonempty --%s", name)
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
	for _, name := range []string{"case", "model", "provider", "provider-url", "no-content", "verbose", "tool-bindings", "github-summary"} {
		if flag := cmd.Flag(name); flag != nil && flag.Changed {
			return fmt.Errorf("--%s requires an explicit agent FILE", name)
		}
	}
	return nil
}

func runFileCommand(cmd *cobra.Command, state *commandState, file string, f fileCommandOptions, prompt, promptFile string) error {
	opt := app.FilePreviewOptions{File: file, Operation: cmd.Name(), CaseID: f.CaseID,
		Model: f.Model, DefaultCopilot: true, AdHocOnly: f.PositionalInput && f.CaseID == "", ShowFailedContent: !f.NoContent,
		IncludeAnswer:         cmd.Name() == "run" && !f.NoContent,
		IncludeFailureDetails: !f.NoContent, ToolBindings: f.ToolBindings}
	opt.RouteSelected = func(route app.FilePreviewRoute) error {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Route: %s / %s\n", route.Provider, route.Model)
		return err
	}
	if cmd.Name() == "test" {
		opt.RetentionFile = file
	}
	var windowsAgent *agentfile.Agent
	if state.deps.goos == "windows" {
		// Freeze the exact snapshot admitted by the native Windows policy before
		// any optional host input or execution setup. A source edit between Cobra
		// hooks must not turn a fake, tool-free invocation into another route.
		var err error
		windowsAgent, err = agentfile.Load(file)
		if err != nil || !windowsPackageFreeFakeAgent(windowsAgent) || f.Provider != "" || f.Endpoint != "" || f.ToolBindings != "" || f.GitHubSummary || promptFile != "" {
			return errWindowsOperationUnsupported
		}
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
	opt.PrepareRecordingSave = func(ctx context.Context, revision string) (app.RecordingSaver, error) {
		save, err := kmxmcp.FileRecordingSaver(ctx, file, revision)
		return app.RecordingSaver(save), err
	}
	a := &app.App{FileRunner: state.deps.fileRunner}
	start := time.Now()
	var result app.FilePreviewResult
	var err error
	if windowsAgent != nil {
		if cmd.Name() == "test" {
			opt = app.WithFilePreviewRetention(opt, file, windowsAgent.Revision)
		}
		result, err = a.PreviewAgentSnapshot(ctx, windowsAgent, opt)
	} else {
		result, err = a.PreviewAgentFile(ctx, opt)
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
