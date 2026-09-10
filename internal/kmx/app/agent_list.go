package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// ListAgents prints the Agent resources kmx can chat with. Structured modes
// remain kubectl-native for scripts and shell completion providers.
func (a *App) ListAgents(output string) error {
	format := strings.ToLower(strings.TrimSpace(output))
	if format == "" {
		format = "table"
	}
	if format != "table" && format != "json" && format != "yaml" {
		return fmt.Errorf("agent list output %q is not supported — use table, json, or yaml", output)
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if format != "table" {
		return a.kubectlRun("-n", config_kagentNamespace, "get", "agents.kagent.dev", "-o", format)
	}
	raw, err := a.kubectlCapture("-n", config_kagentNamespace, "get", "agents.kagent.dev", "-o", "json")
	if err != nil {
		return err
	}
	var agents objectList[agentStatus]
	if err := json.Unmarshal([]byte(raw), &agents); err != nil {
		return fmt.Errorf("agents returned invalid JSON: %w", err)
	}
	rows := agentListRows(agents.Items)
	ui := cliui.New(a.Out)
	if ui.Rich() {
		fmt.Fprintln(a.Out, ui.Report("Agents", []string{"NAME", "READY", "ACCEPTED", "MODEL CONFIG", "TOOL SERVER"}, rows, cliui.ColumnText, cliui.ColumnState, cliui.ColumnState))
		return nil
	}
	fmt.Fprintln(a.Out, ui.Heading("Agents"))
	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "  none")
		return nil
	}
	humanTable(a.Out, []string{"NAME", "READY", "ACCEPTED", "MODEL CONFIG", "TOOL SERVER"}, rows)
	return nil
}

func agentListRows(agents []agentStatus) [][]string {
	rows := make([][]string, 0, len(agents))
	for _, agent := range agents {
		servers := make([]string, 0, len(agent.Spec.Declarative.Tools))
		for _, tool := range agent.Spec.Declarative.Tools {
			if tool.MCPServer.Name != "" {
				servers = append(servers, tool.MCPServer.Name)
			}
		}
		rows = append(rows, []string{
			agent.Metadata.Name,
			condition(agent.Status.Conditions, "Ready"),
			condition(agent.Status.Conditions, "Accepted"),
			agent.Spec.Declarative.ModelConfig,
			valueOr(strings.Join(servers, ","), "none"),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return rows
}
