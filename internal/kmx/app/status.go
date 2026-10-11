package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// `kmx status` delegates Orka's native report, including toolchain preflight
// and context checks, rather than implementing a second reading that could
// disagree about the cluster. Only format validation runs before delegation;
// it needs no cluster.

// StatusOptions controls the output format.
type StatusOptions struct {
	Output string
}

// Status prints the runtime report.
func (a *App) Status() error { return a.StatusWithOptions(StatusOptions{}) }

// StatusWithOptions validates the requested format and reports.
//
// `table` is the only format there is, and json/yaml are refused BY NAME
// rather than quietly falling back to the table or emitting an empty
// document. Machine-readable runtime facts are available from Kubernetes.
func (a *App) StatusWithOptions(opt StatusOptions) error {
	switch format := strings.ToLower(strings.TrimSpace(opt.Output)); format {
	case "", "table":
	case "json", "yaml":
		return fmt.Errorf("status has no %s output: use table.\n"+
			"  The structured document counted the retired runtime's Agents and model presets, and it is gone.\n"+
			"  For machine-readable runtime facts, read the cluster directly:\n"+
			"    kubectl --context %s -n %s get deploy,%s -o json", format, a.Cfg.KubeContext, OrkaNamespace, orkaProviderKind)
	default:
		return fmt.Errorf("status output %q is not supported — use table", opt.Output)
	}
	return a.OrkaStatus()
}

// ---- shared table rendering ----------------------------------------------
//
// Used by native agent listings and views; kept in one place so listings
// cannot align their columns differently.

type objectList[T any] struct {
	Items []T `json:"items"`
}

func table(out io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, value := range row {
			if len(value) > widths[i] {
				widths[i] = len(value)
			}
		}
	}
	for rowIndex, row := range append([][]string{headers}, rows...) {
		fmt.Fprint(out, "  ")
		for i, value := range row {
			if i > 0 {
				fmt.Fprint(out, "  ")
			}
			fmt.Fprintf(out, "%-*s", widths[i], value)
		}
		if rowIndex < len(rows) {
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintln(out)
}

func humanTable(out io.Writer, headers []string, rows [][]string) {
	ui := cliui.New(out)
	if !ui.Rich() {
		table(out, headers, rows)
		return
	}
	fmt.Fprintln(out, ui.Table(headers, rows))
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
