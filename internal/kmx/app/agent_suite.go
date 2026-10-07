package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentsuitecore "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
)

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
