package main

import (
	"context"
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
	ToolBindings                       string
	GitHubSummary                      bool
	ShowContent                        bool
	Verbose                            bool
}

func fileModelRoute(provider, endpoint string) modelaccess.Route {
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
	cmd.Flags().StringVar(&f.Provider, "provider", "", "explicit file-model provider: ollama, foundry-local, compatible or copilot; no fallback")
	cmd.Flags().StringVar(&f.Endpoint, "endpoint", "", "trusted model endpoint (required for local models; fixed for Copilot; never from the charter)")
	cmd.Flags().StringVar(&f.ToolBindings, "tool-bindings", "", "explicit host-owned tool binding file (requires FILE; supports local and Copilot models)")
	if cmd.Name() == "test" {
		cmd.Flags().BoolVar(&f.GitHubSummary, "github-summary", false, "append the content-free JSON report summary to $GITHUB_STEP_SUMMARY (requires FILE and -o json)")
	}
	if output {
		cmd.Flags().StringVarP(&f.Output, "output", "o", "text", "file preview output: text|json")
		cmd.Flags().BoolVar(&f.ShowContent, "show-content", false, "include sanitized bounded answer excerpts in file preview output")
	}
}
func validateFileCommand(cmd *cobra.Command, state *commandState, f fileCommandOptions) error {
	if state.contextFlag != "" {
		return fmt.Errorf("file preview usage refuses --context")
	}
	var invalid string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "case", "output", "show-content", "provider", "endpoint", "prompt", "prompt-file", "verbose", "tool-bindings", "github-summary":
		default:
			invalid = flag.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("file preview refuses --%s", invalid)
	}
	for _, name := range []string{"case", "provider", "endpoint", "prompt-file", "tool-bindings"} {
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
	for _, name := range []string{"case", "provider", "endpoint", "verbose", "tool-bindings", "github-summary"} {
		if flag := cmd.Flag(name); flag != nil && flag.Changed {
			return fmt.Errorf("--%s requires an explicit agent FILE", name)
		}
	}
	return nil
}

func runFileCommand(cmd *cobra.Command, state *commandState, file string, f fileCommandOptions, prompt, promptFile string) error {
	opt := app.FilePreviewOptions{File: file, Operation: cmd.Name(), CaseID: f.CaseID, ShowContent: f.ShowContent, ToolBindings: f.ToolBindings}
	if cmd.Name() == "test" {
		opt.RetentionFile = file
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
	if flag := cmd.Flag("prompt"); flag != nil && flag.Changed {
		opt.Input = &prompt
	}
	if flag := cmd.Flag("prompt-file"); flag != nil && flag.Changed {
		var reader io.Reader = state.deps.stdin
		if promptFile != "-" {
			info, err := os.Stat(promptFile)
			if err != nil || !info.Mode().IsRegular() || info.Size() > agentfile.MaxInputBytes {
				return fmt.Errorf("prompt file must be a readable regular file of at most 16 KiB")
			}
			input, err := os.Open(promptFile)
			if err != nil {
				return fmt.Errorf("cannot read prompt file")
			}
			defer input.Close()
			reader = input
		}
		if reader == nil {
			return fmt.Errorf("prompt stdin is unavailable")
		}
		b, err := io.ReadAll(io.LimitReader(reader, agentfile.MaxInputBytes+1))
		if err != nil || len(b) > agentfile.MaxInputBytes {
			return fmt.Errorf("prompt input must be readable and at most 16 KiB")
		}
		input := string(b)
		opt.Input = &input
	}
	var summary io.WriteCloser
	if f.GitHubSummary {
		var err error
		summary, err = state.deps.openGitHubSummary()
		if err != nil {
			return err
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
	result, err := a.PreviewAgentFile(ctx, opt)
	if f.Verbose {
		if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "[verbose] Total elapsed=%s\n",
			time.Since(start).Round(time.Millisecond)); writeErr != nil {
			return fmt.Errorf("cannot write local preview diagnostics")
		}
	}
	outputErr := app.WriteFilePreview(cmd.OutOrStdout(), result, f.Output)
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
