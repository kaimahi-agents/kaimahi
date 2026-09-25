package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// `kmx status` is the runtime report, and the runtime is Orka.
//
// It used to read the retired runtime's Agents, its model presets and its
// namespace's pods, and assemble a governance document beside them. Every one
// of those reads described a runtime kmx no longer installs, drives or
// deploys: the tables were confidently about objects this repository has
// nothing to do with, which reads as coverage and is not. What replaced that
// runtime is Orka, so that is what this reports.
//
// It DELEGATES rather than reimplements. `kmx orka status` already answers
// "what is installed, and what can it resolve", separates an unreachable
// cluster from an absent install, and reports the running version rather than
// the pin. Two readings of one cluster is two answers that can disagree about
// it, and an operator who ran both would have no way to tell which was right.
//
// Delegation is the WHOLE table path, preflight included. A context check
// bolted on in front of it made this command answer a missing context
// differently from `kmx orka status` — and skipped the toolchain fetch that
// puts kubectl on PATH, so the report an operator got depended on which of
// the two names they typed. Validating the requested FORMAT is not that: it
// decides whether there is a reading to delegate at all, and needs no
// cluster.

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
// document. They used to publish a governance envelope counted off the
// retired runtime's Agents and model presets. Nothing owner-managed replaces that count:
// `kmx migrate` points somebody's own Deployment at the seam, and those
// workloads have no discovery index — there is no query that lists them, so
// any document kmx published would be a tally of what it happened to be told
// about rather than of what is on the cluster. Keeping the old shape filled
// with zeros would be the false zero this report has always refused; keeping
// the flag and saying what it does not support is the honest half.
func (a *App) StatusWithOptions(opt StatusOptions) error {
	switch format := strings.ToLower(strings.TrimSpace(opt.Output)); format {
	case "", "table":
	case "json", "yaml":
		return fmt.Errorf("status has no %s output: use table.\n"+
			"  The structured document counted the retired runtime's Agents and model presets, and it is gone.\n"+
			"  Nothing replaces the count: `kmx migrate` routes your own workloads, which kmx cannot enumerate,\n"+
			"  so a document here would report what it was told rather than what is on the cluster.\n"+
			"  For machine-readable runtime facts, read the cluster directly:\n"+
			"    kubectl --context %s -n %s get deploy,%s -o json", format, a.Cfg.KubeContext, OrkaNamespace, orkaProviderKind)
	default:
		return fmt.Errorf("status output %q is not supported — use table", opt.Output)
	}
	return a.OrkaStatus()
}

// ---- shared table rendering ----------------------------------------------
//
// Used by `kmx agent list` as well as here; kept in one place so two
// listings cannot align their columns differently.

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
