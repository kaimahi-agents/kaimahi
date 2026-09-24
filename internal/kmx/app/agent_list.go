package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// listPresentationRegistration pairs a runtime ID with the app-owned list
// presentation handler for it (DESIGN.md §1). List is inventory/presentation,
// not a lifecycle verb, so this lives beside ListAgents rather than inside
// LifecycleAdapter or Capabilities.
type listPresentationRegistration struct {
	id      agentruntime.ID
	handler func(output, namespace string) error
}

// listPresentationHandlers registers exactly the runtimes this build can
// list by explicit or detected ID: Orka's --namespace-scoped listing and the
// legacy runtime's own fixed-namespace list, both unchanged. kagent-v1 is
// detected by shared platform detection but not implemented in this build,
// so it has no handler here and resolves to the one shared typed
// unsupported-verb error.
func (a *App) listPresentationHandlers() []listPresentationRegistration {
	return []listPresentationRegistration{
		{id: agentruntime.Orka, handler: a.listOrkaAgents},
		{id: agentruntime.Kagent, handler: a.listKagentAgentsIn},
	}
}

// listPresentationHandler looks up the registered list handler for id. A
// runtime with none registered returns the one shared typed
// UnsupportedVerbError naming it and "list", never a fallback to a
// different runtime's handler.
func (a *App) listPresentationHandler(id agentruntime.ID) (func(output, namespace string) error, error) {
	for _, registration := range a.listPresentationHandlers() {
		if registration.id == id {
			return registration.handler, nil
		}
	}
	return nil, &agentruntime.UnsupportedVerbError{Runtime: id, Verb: agentruntime.VerbList}
}

// ListOptions are `kmx agent list`'s knobs (DESIGN.md §4).
type ListOptions struct {
	Output string
	// Runtime is the explicit runtime ID. Empty uses shared platform
	// detection: Orka first, then kagent-v1. Legacy kagent is explicit-only.
	Runtime string
	// Namespace is the namespace the selected runtime lists in. Orka
	// requires it; legacy kagent reads its own fixed namespace and refuses
	// any other.
	Namespace string
}

// ListAgents prints Agent resources. WHICH kind of agent is decided by the
// runtime, and that is not a shortcut.
//
// Several runtimes are in play, keeping different kinds in different places.
// Listing them in one table would merge different kinds under one set of
// column headings and imply they are interchangeable.
//
// DESIGN.md §4 intentionally changes the old bare-list default: with no
// --runtime, shared platform detection selects the installed platform — Orka
// first, then kagent-v1 — and a legacy-only cluster is told to install one
// of those rather than silently getting legacy kagent. `--runtime kagent`
// still reports the legacy runtime in its own fixed namespace, exactly as a
// bare list always did. After detection, Orka still requires the namespace
// it watches: kmx reports the selected platform and the missing flag rather
// than guessing a watched namespace.
func (a *App) ListAgents(opt ListOptions) error {
	format, err := agentListFormat(opt.Output)
	if err != nil {
		return err
	}
	id, detected, err := a.listRuntimeSelection(opt)
	if err != nil {
		return err
	}
	namespace := strings.TrimSpace(opt.Namespace)
	if id != agentruntime.Kagent && namespace == "" {
		return fmt.Errorf("kmx agent list selected runtime %s%s, which requires --namespace.\n"+
			"  %s watches namespaces explicitly, so a guessed one would report \"none\" about a namespace you never meant",
			id, statusSelectionSource(detected), id)
	}
	handler, err := a.listPresentationHandler(id)
	if err != nil {
		return err
	}
	return handler(format, namespace)
}

// listRuntimeSelection resolves which runtime this list reports. An explicit
// ID is proved against the shared registry, so a runtime kmx names but does
// not implement declines "list" and one it does not name at all is that
// registry's own unknown-runtime error — never a fallback to whichever
// handler happens to exist.
func (a *App) listRuntimeSelection(opt ListOptions) (agentruntime.ID, bool, error) {
	registry, err := a.lifecycleRuntimeRegistry(nil)
	if err != nil {
		return "", false, err
	}
	if id := agentruntime.ID(strings.TrimSpace(opt.Runtime)); id != "" {
		if _, err := registry.LookupVerb(id, agentruntime.VerbList); err != nil {
			return id, false, err
		}
		return id, false, nil
	}
	if err := a.preflight(depKubectl); err != nil {
		return "", true, err
	}
	ctx, cancel := context.WithTimeout(a.operationContext(), 2*time.Minute)
	defer cancel()
	id, err := a.detectPlatformRuntime(ctx)
	return id, true, err
}

// listKagentAgentsIn is the legacy runtime's registered list handler. It is
// fixed to its own namespace, so a different one is a conflict rather than
// something to silently list past.
func (a *App) listKagentAgentsIn(output, namespace string) error {
	if namespace = strings.TrimSpace(namespace); namespace != "" && namespace != config_kagentNamespace {
		return fmt.Errorf("runtime %s lists its own fixed namespace %s; --namespace %s selects something it cannot list",
			agentruntime.Kagent, config_kagentNamespace, namespace)
	}
	return a.listKagentAgents(output)
}

// listKagentAgents reports the legacy runtime's agents, in its fixed namespace.
func (a *App) listKagentAgents(output string) error {
	format, err := agentListFormat(output)
	if err != nil {
		return err
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

// listOrkaAgents reports the Orka Agents in one namespace.
//
// Orka watches namespaces explicitly, so there is no safe default to guess: a
// wrong one would report "none" about a namespace nobody meant. The caller
// names it, the same way `kmx agent show` and `kmx agent create` require it.
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
				"  Install Orka with `%s`, or list the legacy kagent runtime with `--runtime kagent`",
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

// agentListFormat validates the output format once for both runtimes.
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
