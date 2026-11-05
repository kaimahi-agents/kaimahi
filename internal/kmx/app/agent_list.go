package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// ListAgents prints the Orka Agents in one namespace.
//
// These are live Orka Agents. Orka watches namespaces explicitly, so the
// namespace is part of the question; an omitted one reads the namespace the
// pinned installer uses.
func (a *App) ListAgents(output, namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = OrkaNamespace
	}
	if scaffold.ValidateNamespace(namespace) != nil {
		return fmt.Errorf("Orka Agent list requires a valid namespace")
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
	if err := a.preflightOrkaInspection(); err != nil {
		return err
	}
	raw, err := a.readOrkaAgentList(namespace)
	if err != nil {
		return a.agentListReadError(err)
	}
	if format != "table" {
		body := raw
		if format == "yaml" {
			var document yaml.Node
			if err := yaml.Unmarshal(raw, &document); err != nil {
				return fmt.Errorf("Orka Agents returned invalid JSON")
			}
			body, err = yaml.Marshal(&document)
			if err != nil {
				return fmt.Errorf("cannot render Orka Agents as YAML")
			}
		}
		var out io.Writer = io.Discard
		if a.Run.Stdout != nil {
			out = a.Run.Stdout
		} else if a.Out != nil {
			out = a.Out
		}
		n, err := out.Write(body)
		if err == nil && n != len(body) {
			err = io.ErrShortWrite
		}
		return err
	}
	var agents objectList[orkaAgentSpec]
	if err := json.Unmarshal(raw, &agents); err != nil {
		return fmt.Errorf("Orka Agents returned invalid JSON: %w", err)
	}
	rows := orkaAgentListRows(agents.Items)
	heading := "Orka Agents in " + orkaHumanField(namespace)
	ui := cliui.New(a.Out)
	if ui.Rich() {
		fmt.Fprintln(a.Out, ui.Report(heading, orkaAgentColumns, rows, cliui.ColumnText, cliui.ColumnState))
		return nil
	}
	fmt.Fprintln(a.Out, ui.Heading(heading))
	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "  none")
		a.notef("  Nothing in %s. Author workspace source with `%s`, prove it with eval, then Lift its immutable digest.", orkaHumanField(namespace),
			a.operationCommand("create", "<name>", "--workspace", "<directory>", "--model", "<exact-model>", "--instructions", "<file>"))
		return nil
	}
	humanTable(a.Out, orkaAgentColumns, rows)
	return nil
}

func (a *App) readOrkaAgentList(namespace string) ([]byte, error) {
	const pageLimit, pageCountLimit, itemLimit = 128, 32, 4096
	const responseLimit = 4 << 20
	ctx := a.operationContext()
	seen := map[string]bool{}
	cursor := ""
	totalBytes := 0
	items := make([]json.RawMessage, 0, pageLimit)
	var first, firstMetadata map[string]json.RawMessage
	var firstRaw []byte
	for pageCount := 0; pageCount < pageCountLimit; pageCount++ {
		path := fmt.Sprintf("/apis/core.orka.ai/v1alpha1/namespaces/%s/agents?limit=%d", url.PathEscape(namespace), pageLimit)
		if cursor != "" {
			path += "&continue=" + url.QueryEscape(cursor)
		}
		raw, err := a.orkaCapture(ctx, nil, "get", "--raw", path)
		if err != nil {
			return nil, err
		}
		if len(raw) > responseLimit-totalBytes {
			return nil, fmt.Errorf("Orka Agent list exceeded safe response limits")
		}
		totalBytes += len(raw)
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
			return nil, fmt.Errorf("Orka Agents returned invalid JSON")
		}
		var pageItems []json.RawMessage
		if value, ok := envelope["items"]; !ok || json.Unmarshal(value, &pageItems) != nil || len(pageItems) > pageLimit || len(items)+len(pageItems) > itemLimit {
			return nil, fmt.Errorf("Orka Agent list exceeded safe item limits")
		}
		items = append(items, pageItems...)
		var metadata map[string]json.RawMessage
		if value, ok := envelope["metadata"]; ok && (json.Unmarshal(value, &metadata) != nil || metadata == nil) {
			return nil, fmt.Errorf("Orka Agents returned invalid metadata")
		}
		if first == nil {
			first = envelope
			firstMetadata = metadata
			firstRaw = append([]byte(nil), raw...)
		}
		next := ""
		if value, ok := metadata["continue"]; ok {
			if len(value) > 4096 || strings.TrimSpace(string(value)) == "null" || json.Unmarshal(value, &next) != nil {
				return nil, fmt.Errorf("Orka Agent list returned an invalid continuation token")
			}
		}
		if next == "" {
			if pageCount == 0 {
				return firstRaw, nil
			}
			if firstMetadata != nil {
				delete(firstMetadata, "continue")
				delete(firstMetadata, "remainingItemCount")
				metadataRaw, marshalErr := json.Marshal(firstMetadata)
				if marshalErr != nil {
					return nil, fmt.Errorf("cannot render Orka Agent list metadata")
				}
				first["metadata"] = metadataRaw
			}
			itemsRaw, marshalErr := json.Marshal(items)
			if marshalErr != nil {
				return nil, fmt.Errorf("cannot render Orka Agent list")
			}
			first["items"] = itemsRaw
			complete, marshalErr := json.Marshal(first)
			if marshalErr != nil || len(complete) > responseLimit {
				return nil, fmt.Errorf("Orka Agent list exceeded safe response limits")
			}
			return complete, nil
		}
		if len(next) > 4096 || !utf8.ValidString(next) || strings.IndexFunc(next, unicode.IsControl) >= 0 || seen[next] || next == cursor || pageCount == pageCountLimit-1 {
			return nil, fmt.Errorf("Orka Agent list exceeded safe pagination limits")
		}
		seen[next] = true
		cursor = next
	}
	return nil, fmt.Errorf("Orka Agent list exceeded safe pagination limits")
}

func (a *App) agentListReadError(err error) error {
	if noSuchResourceType(err) {
		return a.missingOrkaInspectionKind()
	}
	return err
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
			orkaHumanField(agent.Metadata.Name),
			boolState(agent.Status.Ready),
			valueOr(orkaHumanField(agent.Spec.ProviderRef.Name), "none"),
			orkaHumanField(model),
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
		return "", fmt.Errorf("orka list output %q is not supported — use table, json, or yaml", output)
	}
	return format, nil
}
