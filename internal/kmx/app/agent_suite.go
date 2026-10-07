package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

// ValidateSuite validates an extracted AgentSuite or OCI image layout
// entirely offline.
func (a *App) ValidateSuite(path, output string) error {
	report, err := agentsuite.ValidatePath(path)
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
