package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// How long to wait for each data path to produce its first record.
//
// The two are very different and the numbers say so. A Prometheus sample is
// scraped on a 30-second interval and is queryable almost immediately after.
// Container Insights batches, and its first records routinely take several
// minutes to become queryable — which is precisely why this waits rather than
// declaring a miss: a log path reported as broken because it was asked too
// early is worse than no check, because somebody would go and "fix" it.
const (
	metricsArrivalWait = 5 * time.Minute
	logsArrivalWait    = 12 * time.Minute
	arrivalPoll        = 20 * time.Second
)

// liftVerify is the phase that turns "it is installed" into "it works".
//
// Three claims, checked separately because they fail separately: the agent
// answers through the plane, a metric the plane exposed has arrived in Azure
// Monitor, and a log line the plane wrote has arrived in Container Insights.
// The last two are the ones worth insisting on — enabling an add-on and
// having its data arrive are different things, and only one of them is
// visible from the Azure CLI's exit status.
func (a *App) liftVerify(opt lift.Options) error {
	if err := a.kubectlRun("-n", "kagent", "rollout", "status",
		"deploy/hello-world", "--timeout=300s"); err != nil {
		return err
	}
	a.notef("asking the agent a question, through the plane")
	if err := a.Chat("hello-world", "Say hello in one short sentence."); err != nil {
		return err
	}
	if err := a.Ledger(a.Cfg.Credential); err != nil {
		return err
	}
	if !opt.Observability {
		return nil
	}
	if err := a.verifyMetricsArrived(opt); err != nil {
		return err
	}
	return a.verifyLogsArrived(opt)
}

// verifyMetricsArrived queries Managed Prometheus for a series only the plane
// produces, and waits for it rather than asking once.
//
// kaimahi_build_info is the right probe: the plane sets it at startup, so it
// exists whether or not any agent has called anything. A panel that is empty
// because nobody has used the system yet would otherwise be
// indistinguishable from a scrape that is not landing.
func (a *App) verifyMetricsArrived(opt lift.Options) error {
	endpoint, err := a.prometheusQueryEndpoint(opt)
	if err != nil {
		return err
	}
	token, err := a.azToken("https://prometheus.monitor.azure.com")
	if err != nil {
		return err
	}
	a.notef("waiting for the plane's own metrics to arrive in Azure Monitor (scrape interval is 30s)")
	deadline := a.timeNow().Add(metricsArrivalWait)
	for {
		samples, err := a.promQuery(endpoint, token, "kaimahi_build_info")
		if err == nil && samples > 0 {
			a.notef("Managed Prometheus returned %d sample(s) of kaimahi_build_info — the scrape is landing.", samples)
			return nil
		}
		if a.timeNow().After(deadline) {
			return fmt.Errorf(`Managed Prometheus is enabled but no sample from the plane has arrived within %s.

  Enabled and arriving are different claims, and this is the second one
  failing. The usual causes, in the order worth checking:

    - the scrape job is not merged into %s in %s
    - the NetworkPolicy allowance is missing, so the scraper cannot reach
      the pod: kubectl --context %s -n %s get networkpolicy kaimahi-proxy-metrics-azure
    - the add-on's replica pod is reporting a config error:
      kubectl --context %s -n kube-system logs -l rsName=ama-metrics -c prometheus-collector --tail=50`,
				metricsArrivalWait, scrapeConfigMap, scrapeConfigNamespace,
				a.Cfg.KubeContext, "kaimahi", a.Cfg.KubeContext)
		}
		time.Sleep(arrivalPoll)
	}
}

// verifyLogsArrived asks Log Analytics for an actual log line from a plane
// pod — not for the table's existence, and not for a count of zero.
func (a *App) verifyLogsArrived(opt lift.Options) error {
	workspaceGUID, err := a.logAnalyticsCustomerID(opt)
	if err != nil {
		return err
	}
	a.notef("waiting for a plane log line to arrive in Container Insights (first records take several minutes)")
	deadline := a.timeNow().Add(logsArrivalWait)
	query := "ContainerLogV2 | where PodNamespace == 'kaimahi' | project TimeGenerated, PodName, LogMessage | order by TimeGenerated desc | take 1"
	for {
		out, err := a.Run.Capture("az", "monitor", "log-analytics", "query",
			"--workspace", workspaceGUID, "--analytics-query", query, "-o", "json")
		if err == nil && countJSONRows(out) > 0 {
			a.notef("Container Insights returned a log line from the plane's namespace — collection is landing.")
			return nil
		}
		if a.timeNow().After(deadline) {
			return fmt.Errorf(`Container Insights is enabled but no plane log line has arrived within %s.

  Enabled and arriving are different claims, and this is the second one
  failing. Check that the add-on's collector pods are running:

    kubectl --context %s -n kube-system get pods -l dsName=ama-logs`,
				logsArrivalWait, a.Cfg.KubeContext)
		}
		time.Sleep(arrivalPoll)
	}
}

func (a *App) prometheusQueryEndpoint(opt lift.Options) (string, error) {
	name, err := a.recordedWorkspaceName(opt, "Azure Monitor workspace (Managed Prometheus)")
	if err != nil {
		return "", err
	}
	out, err := a.Run.Capture("az", "monitor", "account", "show", "--name", name,
		"--resource-group", opt.ResourceGroup, "--query", "metrics.prometheusQueryEndpoint", "-o", "tsv")
	if err != nil {
		return "", fmt.Errorf("cannot read the Managed Prometheus query endpoint: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("the Azure Monitor workspace reported no query endpoint")
	}
	return strings.TrimRight(strings.TrimSpace(out), "/"), nil
}

func (a *App) logAnalyticsCustomerID(opt lift.Options) (string, error) {
	name, err := a.recordedWorkspaceName(opt, "Log Analytics workspace (Container Insights)")
	if err != nil {
		return "", err
	}
	out, err := a.Run.Capture("az", "monitor", "log-analytics", "workspace", "show", "--name", name,
		"--resource-group", opt.ResourceGroup, "--query", "customerId", "-o", "tsv")
	if err != nil {
		return "", fmt.Errorf("cannot read the Log Analytics workspace id: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("the Log Analytics workspace reported no id")
	}
	return strings.TrimSpace(out), nil
}

// recordedWorkspaceName reads the name out of this run's own record rather
// than deriving it, so a verify that runs against a resumed lift asks about
// the workspace that lift actually created.
func (a *App) recordedWorkspaceName(opt lift.Options, kind string) (string, error) {
	record, err := a.readLiftRecord(opt.ResourceGroup, opt.Cluster)
	if err != nil {
		return "", err
	}
	for _, res := range record.Created {
		if res.Kind == kind {
			return res.Name, nil
		}
	}
	return "", fmt.Errorf("this run has no recorded %s — run `kmx lift --step observability` first", kind)
}

func (a *App) readLiftRecord(group, cluster string) (*lift.Record, error) {
	path, err := liftRecordPath(group, cluster)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("no record of a lift onto %s/%s (looked in %s): %w", group, cluster, filepath.Dir(path), err)
	}
	defer f.Close()
	return lift.ReadRecord(f)
}

// azToken gets a bearer for one audience. The token is passed to the query in
// a header and is never written anywhere: not to a file, not to argv beyond
// the single call that uses it, and not to the log.
func (a *App) azToken(audience string) (string, error) {
	out, err := a.Run.Capture("az", "account", "get-access-token", "--resource", audience, "--query", "accessToken", "-o", "tsv")
	if err != nil {
		return "", fmt.Errorf("cannot get a token for %s: %w", audience, err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("no token returned for %s", audience)
	}
	return strings.TrimSpace(out), nil
}

// promQuery runs one instant PromQL query and returns how many series came
// back. It counts rather than parsing values: the claim being checked is
// "something the plane produced is in there", and a count answers it exactly.
func (a *App) promQuery(endpoint, token, query string) (int, error) {
	quiet := *a.Run
	quiet.Echo = false
	out, err := quiet.Capture("curl", "-sS", "--fail-with-body",
		"-H", "Authorization: Bearer "+token,
		"--get", "--data-urlencode", "query="+query,
		endpoint+"/api/v1/query")
	if err != nil {
		return 0, err
	}
	var reply struct {
		Status string `json:"status"`
		Data   struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return 0, err
	}
	if reply.Status != "success" {
		return 0, fmt.Errorf("query returned status %q", reply.Status)
	}
	return len(reply.Data.Result), nil
}

func countJSONRows(out string) int {
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return 0
	}
	return len(rows)
}

// managedFile reads one of the carried files out of the materialised tree.
func (a *App) managedFile(work, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(work, filepath.FromSlash(name)))
}

// liftNextSteps says what the operator now has, and — the part that is not
// optional — what it costs until they take it down.
func (a *App) liftNextSteps(opt lift.Options, record *lift.Record) {
	fmt.Fprintf(a.Err, `
  The same agent you ran locally is now running on AKS, governed.

    kmx agent chat hello-world "..."      ask it something
    kmx ledger                            what it spent
    kmx flow                              the whole audit trail

  The dashboard is the workbook named "Kaimahi governance plane (%s)" in
  the %s resource group, under Monitoring > Workbooks on the cluster.

`, record.RunID, opt.ResourceGroup)

	if opt.BringYourOwn {
		fmt.Fprintf(a.Err, `  THIS COSTS MONEY UNTIL YOU REMOVE IT. Your cluster and resource group
  are not ours to delete and never will be, so what bills on is what this
  run added: the two monitoring workspaces. Remove exactly those with

    kmx lift down --byo %s

  It deletes only resources whose recorded id still names them, and leaves
  anything it cannot prove is its own, saying which.

`, liftIdentityFlags(opt))
		return
	}
	fmt.Fprintf(a.Err, `  THIS COSTS MONEY UNTIL YOU REMOVE IT — the node, the load balancer, the
  registry and the two monitoring workspaces. All of it is inside one
  resource group and comes out together:

    KAIMAHI_CONFIRM=%s kmx lift down %s

`, opt.ResourceGroup, liftIdentityFlags(opt))
}
