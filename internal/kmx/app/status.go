package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seamcert"
)

// `kmx status` delegates Orka's runtime report unchanged, then adds the
// independently deployed model plane and seam certificate. OrkaStatus owns
// preflight and context handling; additional reads use the same pinned kubectl.

// A status read must not hang when the API server is unreachable.
const statusRequestTimeout = "--request-timeout=15s"

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
// document. They used to publish a governance envelope counted off kagent
// Agents and ModelConfigs. Nothing owner-managed replaces that count:
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
			"  The structured document counted kagent Agents and ModelConfigs, and that runtime is gone.\n"+
			"  Nothing replaces the count: `kmx migrate` routes your own workloads, which kmx cannot enumerate,\n"+
			"  so a document here would report what it was told rather than what is on the cluster.\n"+
			"  For machine-readable runtime facts, read the cluster directly:\n"+
			"    kubectl --context %s -n %s get deploy,%s -o json", format, a.Cfg.KubeContext, OrkaNamespace, orkaProviderKind)
	default:
		return fmt.Errorf("status output %q is not supported — use table", opt.Output)
	}
	if err := a.OrkaStatus(); err != nil {
		return err
	}
	a.statusPlane()
	return nil
}

// statusPlane diagnoses the separately deployed proxy. A missing Deployment
// is not a failed read, and Postgres pods must never count as ready proxies.
func (a *App) statusPlane() {
	fmt.Fprintln(a.Out, "\nModel plane (kaimahi-proxy)")
	deployment, err := a.kubectlCapture("-n", admin.Namespace, "get", "deploy", planeWorkload, "-o", "json", statusRequestTimeout)
	switch {
	case isNotFound(err):
		fmt.Fprintln(a.Out, "  plane:       not installed (`kmx plane`)")
	case err != nil:
		fmt.Fprintf(a.Out, "  plane:       unknown — %s\n", strings.TrimSpace(err.Error()))
	default:
		var d struct {
			Spec struct {
				Replicas int `json:"replicas"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas int `json:"readyReplicas"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(deployment), &d); err != nil {
			fmt.Fprintf(a.Out, "  plane:       unknown — unreadable Deployment: %v\n", err)
		} else {
			pods, err := a.kubectlCapture("-n", admin.Namespace, "get", "pods", "-l", "app="+planeWorkload, "-o", "json", statusRequestTimeout)
			if err != nil {
				fmt.Fprintf(a.Out, "  plane:       %d/%d replicas ready; pods unknown — %s\n", d.Status.ReadyReplicas, d.Spec.Replicas, strings.TrimSpace(err.Error()))
			} else {
				var list struct {
					Items []struct {
						Status struct {
							Conditions []struct{ Type, Status string } `json:"conditions"`
							Containers []struct {
								RestartCount int `json:"restartCount"`
							} `json:"containerStatuses"`
						} `json:"status"`
					} `json:"items"`
				}
				if err := json.Unmarshal([]byte(pods), &list); err != nil {
					fmt.Fprintf(a.Out, "  plane:       %d/%d replicas ready; pods unknown — %v\n", d.Status.ReadyReplicas, d.Spec.Replicas, err)
				} else {
					ready, restarts := 0, 0
					for _, pod := range list.Items {
						for _, condition := range pod.Status.Conditions {
							if condition.Type == "Ready" && condition.Status == "True" {
								ready++
							}
						}
						for _, container := range pod.Status.Containers {
							restarts += container.RestartCount
						}
					}
					fmt.Fprintf(a.Out, "  plane:       %d/%d replicas ready; %d/%d pods ready, %d restarts\n", d.Status.ReadyReplicas, d.Spec.Replicas, ready, len(list.Items), restarts)
				}
			}
		}
	}
	// Read only the public serving certificate, never the Secret's private key.
	encoded, err := a.kubectlCapture("-n", admin.Namespace, "get", "secret", config.PlaneSeamTLSSecret, "-o", "jsonpath={.data.tls\\.crt}", statusRequestTimeout)
	switch {
	case isNotFound(err):
		fmt.Fprintln(a.Out, "  certificate: none — serving Secret not installed")
	case err != nil:
		fmt.Fprintf(a.Out, "  certificate: unknown — %s\n", strings.TrimSpace(err.Error()))
	default:
		pem, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			fmt.Fprintf(a.Out, "  certificate: unknown — invalid certificate encoding: %v\n", err)
			return
		}
		cert, err := seamcert.ParseCertificate(pem)
		if err != nil {
			fmt.Fprintf(a.Out, "  certificate: unknown — unreadable serving certificate: %v\n", err)
			return
		}
		report := seamcert.Expiry(cert, a.timeNow())
		fmt.Fprintf(a.Out, "  certificate: %s\n", report.Line())
		if report.State == seamcert.Expiring || report.State == seamcert.Expired {
			fmt.Fprintln(a.Out, "               The model seam stops answering when it expires; `kmx plane --step certificate` renews it.")
		}
	}
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
