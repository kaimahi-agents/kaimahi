package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// ListAgents prints the Orka Agents in one namespace.
//
// These are the agents `kmx agent create` writes and `kmx agent show`
// inspects. Orka watches namespaces explicitly, so the namespace is part of
// the question: an omitted one reads the namespace the pinned installer uses,
// which is the same default `kmx agent chat` resolves against.
func (a *App) ListAgents(output, namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = OrkaNamespace
	}
	return a.listOrkaAgents(output, namespace)
}

// listOrkaAgents reports the Orka Agents in one namespace.
//
// The namespace is always resolved by the caller above, which defaults it to
// the one the pinned installer uses. It is a parameter rather than a lookup
// here so that this stays a report about a named namespace: Orka watches
// namespaces explicitly, and a report that chose its own would answer about a
// namespace nobody meant.
func (a *App) listOrkaAgents(output, namespace string) error {
	format, err := agentListFormat(output)
	if err != nil {
		return err
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if format != "table" {
		return a.kubectlRun("-n", namespace, "get", "agents.core.orka.ai", "-o", format)
	}
	raw, err := a.kubectlCapture("-n", namespace, "get", "agents.core.orka.ai", "-o", "json")
	if err != nil {
		// Only one failure needs translating here, and it is not the obvious
		// one. A cluster that does not serve the kind has no Orka on it at
		// all; kubectl says "doesn't have a resource type" for that, which
		// isNotFound does not match — hence noSuchResourceType.
		//
		// A NAMESPACE that does not exist is deliberately not handled, because
		// kubectl does not report it here: listing a namespaced kind in an
		// absent namespace returns an empty list and exit 0, not NotFound.
		// Verified against a live cluster. A branch for it would be dead code
		// implying an error that cannot arrive.
		//
		// Anything else is unread, and is returned as it arrived.
		if noSuchResourceType(err) {
			return fmt.Errorf("no Orka Agent kind on this cluster, so nothing here is an Orka agent.\n"+
				"  Install Orka with `%s`",
				a.operationCommand("orka", "install"))
		}
		return err
	}
	var agents objectList[orkaAgentSpec]
	if err := json.Unmarshal([]byte(raw), &agents); err != nil {
		return fmt.Errorf("Orka Agents returned invalid JSON: %w", err)
	}
	rows := orkaAgentListRows(agents.Items)
	heading := "Orka Agents in " + namespace
	ui := cliui.New(a.Out)
	if ui.Rich() {
		fmt.Fprintln(a.Out, ui.Report(heading, orkaAgentColumns, rows, cliui.ColumnText, cliui.ColumnState))
		return nil
	}
	fmt.Fprintln(a.Out, ui.Heading(heading))
	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "  none")
		a.notef("  Nothing in %s. `%s` makes one.", namespace,
			a.operationCommand("agent", "create", "<name>", "--namespace", namespace))
		return nil
	}
	humanTable(a.Out, orkaAgentColumns, rows)
	return nil
}

var orkaAgentColumns = []string{"NAME", "READY", "PROVIDER", "MODEL"}

func orkaAgentListRows(agents []orkaAgentSpec) [][]string {
	rows := make([][]string, 0, len(agents))
	for _, agent := range agents {
		model := agent.Spec.Model.Name
		if strings.TrimSpace(model) == "" {
			// An Agent with no model of its own resolves the Provider's
			// default. Printing an empty cell would read as "no model".
			model = "(the Provider's default)"
		}
		rows = append(rows, []string{
			agent.Metadata.Name,
			boolState(agent.Status.Ready),
			valueOr(agent.Spec.ProviderRef.Name, "none"),
			model,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return rows
}

func boolState(ready bool) string {
	if ready {
		return "yes"
	}
	return "no"
}

// agentListFormat validates the output format.
func agentListFormat(output string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(output))
	if format == "" {
		format = "table"
	}
	if format != "table" && format != "json" && format != "yaml" {
		return "", fmt.Errorf("agent list output %q is not supported — use table, json, or yaml", output)
	}
	return format, nil
}
