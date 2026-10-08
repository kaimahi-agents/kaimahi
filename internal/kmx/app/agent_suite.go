package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentsuitecore "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
)

// SuiteBuildResult identifies one built sandbox image archive.
type SuiteBuildResult struct {
	Path      string
	Agent     string
	Platform  string
	MediaType string
	Warnings  []string
}

// ValidateSuite validates an extracted AgentSuite or OCI image layout
// entirely offline.
func (a *App) ValidateSuite(path, output string) error {
	report, err := agentsuitecore.ValidatePath(path)
	if err != nil {
		return fmt.Errorf("AgentSuite is not conformant: %w", err)
	}
	switch output {
	case "text":
		capabilities := strings.Join(report.Capabilities, ",")
		if capabilities == "" {
			capabilities = "none"
		}
		_, err = fmt.Fprintf(a.Out, "AgentSuite %s: conformant (agents=%d toolProviders=%d compositions=%d toolProviderCompositions=%d capabilities=%s)\n",
			report.Name, report.Agents, report.ToolProviders, report.Compositions, report.ToolProviderCompositions, capabilities)
		return err
	case "json":
		return json.NewEncoder(a.Out).Encode(report)
	default:
		return fmt.Errorf("unsupported output %q; use text or json", output)
	}
}

// PushSuite pushes one extracted AgentSuite directory to an OCI image layout.
func (a *App) PushSuite(
	ctx context.Context,
	source string,
	target string,
	reference string,
) (agentsuite.PushResult, error) {
	return agentsuite.Push(ctx, source, target, reference)
}

// PushSuiteRegistry pushes one extracted AgentSuite directory to a registry.
func (a *App) PushSuiteRegistry(
	ctx context.Context,
	source string,
	reference string,
	plainHTTP bool,
	force bool,
) (agentsuite.PushResult, error) {
	return agentsuite.PushRegistry(ctx, source, reference, plainHTTP, force)
}

// PullSuite pulls one referenced AgentSuite from an OCI image layout and
// extracts it into a new directory.
func (a *App) PullSuite(
	ctx context.Context,
	source string,
	reference string,
	output string,
) (agentsuite.PullResult, error) {
	return agentsuite.Pull(ctx, source, reference, output)
}

// PullSuiteRegistry pulls one referenced AgentSuite from a registry and
// extracts it into a new directory.
func (a *App) PullSuiteRegistry(
	ctx context.Context,
	reference string,
	output string,
	plainHTTP bool,
) (agentsuite.PullResult, error) {
	return agentsuite.PullRegistry(ctx, reference, output, plainHTTP)
}

// BuildSuite resolves one AgentSuite composition and atomically publishes the
// image archive emitted by the selected provider-neutral builder.
func (a *App) BuildSuite(
	ctx context.Context,
	source string,
	output string,
	selection agentsuitecore.BuildSelection,
	builder agentsuitecore.SandboxBuilder,
) (SuiteBuildResult, error) {
	if builder == nil {
		return SuiteBuildResult{}, fmt.Errorf("AgentSuite sandbox builder is required")
	}
	plan, err := agentsuitecore.ResolveSandboxPlan(source, selection)
	if err != nil {
		return SuiteBuildResult{}, fmt.Errorf("resolve AgentSuite sandbox plan: %w", err)
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return SuiteBuildResult{}, err
	}
	if info, err := os.Lstat(outputPath); err == nil && info.IsDir() {
		return SuiteBuildResult{}, fmt.Errorf("AgentSuite build output %s is a directory", outputPath)
	} else if err != nil && !os.IsNotExist(err) {
		return SuiteBuildResult{}, fmt.Errorf("inspect AgentSuite build output: %w", err)
	}
	parent := filepath.Dir(outputPath)
	info, err := os.Stat(parent)
	if err != nil {
		return SuiteBuildResult{}, fmt.Errorf("stat AgentSuite build output parent: %w", err)
	}
	if !info.IsDir() {
		return SuiteBuildResult{}, fmt.Errorf("AgentSuite build output parent %s is not a directory", parent)
	}

	stage, err := os.CreateTemp(parent, "."+filepath.Base(outputPath)+"-*")
	if err != nil {
		return SuiteBuildResult{}, fmt.Errorf("create staged AgentSuite build output: %w", err)
	}
	stagePath := stage.Name()
	publish := true
	defer func() {
		if publish {
			_ = os.Remove(stagePath)
		}
	}()
	result, buildErr := builder.Build(ctx, *plan, stage)
	if buildErr != nil {
		_ = stage.Close()
		return SuiteBuildResult{}, buildErr
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return SuiteBuildResult{}, fmt.Errorf("sync AgentSuite build output: %w", err)
	}
	if err := stage.Close(); err != nil {
		return SuiteBuildResult{}, fmt.Errorf("close AgentSuite build output: %w", err)
	}
	if err := os.Chmod(stagePath, 0o644); err != nil {
		return SuiteBuildResult{}, fmt.Errorf("set AgentSuite build output mode: %w", err)
	}
	if err := os.Rename(stagePath, outputPath); err != nil {
		return SuiteBuildResult{}, fmt.Errorf("publish AgentSuite build output: %w", err)
	}
	publish = false
	return SuiteBuildResult{
		Path:      outputPath,
		Agent:     plan.Agent.ID,
		Platform:  plan.Composition.Platform.String(),
		MediaType: result.MediaType,
		Warnings:  result.Warnings,
	}, nil
}
