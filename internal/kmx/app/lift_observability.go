package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// The ConfigMap Azure's metrics add-on reads custom scrape jobs from. It is
// cluster-wide and singular, which is why the bring-your-own branch treats an
// existing one as somebody else's property rather than as something to
// overwrite.
const (
	scrapeConfigMap       = "ama-metrics-prometheus-config"
	scrapeConfigNamespace = "kube-system"
)

// liftObservability wires Azure-managed monitoring to a plane that is already
// running, and records every resource it creates as it creates it.
//
// Two independent data paths, because "enabled" and "arriving" are different
// claims and they fail separately: Managed Prometheus scrapes the plane's own
// /metrics, and Container Insights collects its logs. Each gets its own
// workspace, and the workbook at the end reads both — so an empty metric
// panel and an empty log panel mean different things and point at different
// fixes.
func (a *App) liftObservability(opt lift.Options, record *lift.Record, save func() error, work string) error {
	a.aimAtTheCluster(opt)
	if err := a.Guard("wire Azure-managed observability", "kmx lift --step observability "+liftIdentityFlags(opt)); err != nil {
		return err
	}

	clusterID, state := a.resourceID("aks", "show", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup)
	if state != lift.Present {
		return fmt.Errorf("kmx lift: cannot resolve the cluster's resource id — refusing to wire monitoring to something that could not be identified")
	}

	// Everything created here goes INSIDE the resource group the cluster is
	// in. On the branch that creates that group, `az group exists` returning
	// false afterwards is then a complete proof of cleanup. On the other
	// branch it proves nothing — the group is not ours to delete — which is
	// exactly why each resource's id is recorded.
	location, err := a.groupLocation(opt.ResourceGroup)
	if err != nil {
		return err
	}
	inGroup := true

	metricsName := lift.MetricsWorkspaceName(record.RunID)
	metricsID, err := a.ensureRecorded(record, save, lift.Resource{
		Kind: "Azure Monitor workspace (Managed Prometheus)", Name: metricsName, InResourceGroup: inGroup,
		Billing: "retains ingested samples for 18 months; charges per sample ingested and per query",
	}, func() (string, error) {
		if err := a.Run.Run("az", "monitor", "account", "create", "--name", metricsName,
			"--resource-group", opt.ResourceGroup, "--location", location, "--output", "none"); err != nil {
			return "", err
		}
		id, state := a.resourceID("monitor", "account", "show", "--name", metricsName, "--resource-group", opt.ResourceGroup)
		if state != lift.Present {
			return "", fmt.Errorf("the Azure Monitor workspace was created but its id could not be read back — not recording a resource this run could never remove")
		}
		return id, nil
	})
	if err != nil {
		return err
	}

	logsName := lift.LogsWorkspaceName(record.RunID)
	logsID, err := a.ensureRecorded(record, save, lift.Resource{
		Kind: "Log Analytics workspace (Container Insights)", Name: logsName, InResourceGroup: inGroup,
		Billing: "charges per GB ingested and for retention beyond the included period; keeps costing while anything still sends to it",
	}, func() (string, error) {
		if err := a.Run.Run("az", "monitor", "log-analytics", "workspace", "create", "--name", logsName,
			"--resource-group", opt.ResourceGroup, "--location", location, "--output", "none"); err != nil {
			return "", err
		}
		id, state := a.resourceID("monitor", "log-analytics", "workspace", "show", "--name", logsName, "--resource-group", opt.ResourceGroup)
		if state != lift.Present {
			return "", fmt.Errorf("the Log Analytics workspace was created but its id could not be read back — not recording a resource this run could never remove")
		}
		return id, nil
	})
	if err != nil {
		return err
	}

	// Enabling either add-on makes Azure create data-collection rules and
	// endpoints on the operator's behalf. We do not choose their names and
	// they are not always in the resource group we are looking at, so what
	// appeared is established by difference: snapshot before, snapshot after,
	// record what is new. Recording the difference rather than everything
	// matching a name is the point — on a subscription we do not own, a
	// data-collection rule that was already there belongs to somebody else.
	before, err := a.dataCollectionResources(opt.ResourceGroup)
	if err != nil {
		return err
	}

	a.notef("enabling Managed Prometheus and Container Insights on the cluster (a few minutes)")
	if err := a.Run.Run("az", "aks", "update", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
		"--enable-azure-monitor-metrics", "--azure-monitor-workspace-resource-id", metricsID, "--output", "none"); err != nil {
		return err
	}
	if err := a.Run.Run("az", "aks", "enable-addons", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
		"--addons", "monitoring", "--workspace-resource-id", logsID, "--output", "none"); err != nil {
		return err
	}

	after, err := a.dataCollectionResources(opt.ResourceGroup)
	if err != nil {
		return err
	}
	for _, id := range newResources(before, after) {
		if err := record.Add(lift.Resource{
			Kind: "data collection rule or endpoint (created by the monitoring add-on)",
			Name: resourceNameFromID(id), ID: id, InResourceGroup: inGroup,
			Billing: "no standing charge of its own; it is what routes data to the workspaces, which do charge",
		}); err != nil {
			return err
		}
	}
	if err := save(); err != nil {
		return err
	}

	if err := a.wireScrape(opt, work); err != nil {
		return err
	}
	return a.deployWorkbook(opt, record, save, work, clusterID, logsID, metricsID)
}

// wireScrape opens the one hole the scraper needs and tells the add-on what
// to scrape.
//
// The allowance comes first. The port carries no authentication, so the
// NetworkPolicy IS the access control, and a scrape job pointed at a port
// nothing may reach fails in a way that looks like a broken agent rather than
// a missing rule.
func (a *App) wireScrape(opt lift.Options, work string) error {
	if err := a.applyManaged(work, "k8s/observability/network-policy.yaml"); err != nil {
		return err
	}

	// The scrape ConfigMap is cluster-wide and singular. On a cluster we
	// created, nothing else can have put one there. On yours, one that
	// already exists holds your scrape jobs, and applying ours over it would
	// delete them silently — so it is refused, and the job to merge is
	// printed instead.
	_, err := a.kubectlCapture("-n", scrapeConfigNamespace, "get", "configmap", scrapeConfigMap, "-o", "name")
	switch {
	case err == nil && opt.BringYourOwn:
		body, readErr := a.managedFile(work, "k8s/observability/scrape-config.yaml")
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf(`%s already exists in %s, and it is not this run's.

  That ConfigMap is cluster-wide and holds every custom scrape job on this
  cluster. Replacing it would delete yours without saying so, so this stops
  here instead. Add the job below to the one you have, under the same
  prometheus-config key, and re-run:

    kmx lift --step observability %s

%s`, scrapeConfigMap, scrapeConfigNamespace, liftIdentityFlags(opt), indentBlock(scrapeJobOnly(body)))
	case err == nil, isNotFound(err):
		return a.applyManaged(work, "k8s/observability/scrape-config.yaml")
	default:
		return fmt.Errorf("cannot tell whether %s already exists (refusing to overwrite a scrape configuration on a guess): %w", scrapeConfigMap, err)
	}
}

// deployWorkbook puts the operator-facing view in the same resource group as
// everything else, so a single group deletion accounts for it too.
func (a *App) deployWorkbook(opt lift.Options, record *lift.Record, save func() error, work, clusterID, logsID, metricsID string) error {
	name := lift.WorkbookName(record.RunID)
	_, err := a.ensureRecorded(record, save, lift.Resource{
		Kind: "Azure Monitor workbook (the dashboard)", Name: name, InResourceGroup: true,
		Billing: "none — a workbook is a saved query set and is not charged for",
	}, func() (string, error) {
		out, err := a.Run.Capture("az", "deployment", "group", "create",
			"--resource-group", opt.ResourceGroup,
			"--name", name,
			"--template-file", filepath.Join(work, "k8s", "observability", "workbook.json"),
			"--parameters",
			"runId="+record.RunID,
			"clusterResourceId="+clusterID,
			"logAnalyticsResourceId="+logsID,
			"azureMonitorWorkspaceResourceId="+metricsID,
			"--query", "properties.outputs.workbookResourceId.value", "-o", "tsv")
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(out)
		if id == "" {
			return "", fmt.Errorf("the workbook deployment returned no resource id — not recording a resource this run could never remove")
		}
		return id, nil
	})
	return err
}

// ensureRecorded creates a resource if the record does not already have one
// of that kind, and records its id the moment it exists.
//
// The record is written before the next resource is created, not at the end
// of the phase. A phase that dies halfway has still put things in somebody's
// subscription, and a record written only on success would be a record of
// exactly the runs that did not need one.
func (a *App) ensureRecorded(record *lift.Record, save func() error, res lift.Resource, create func() (string, error)) (string, error) {
	for _, existing := range record.Created {
		if existing.Kind == res.Kind && existing.Name == res.Name {
			a.notef("%s %s is already recorded for this run; keeping it.", res.Kind, res.Name)
			return existing.ID, nil
		}
	}
	id, err := create()
	if err != nil {
		return "", err
	}
	res.ID = id
	if err := record.Add(res); err != nil {
		return "", err
	}
	if err := save(); err != nil {
		return "", fmt.Errorf("created %s but could not record it (%w) — remove it by hand: az resource delete --ids %s", res.Name, err, id)
	}
	return id, nil
}

func (a *App) groupLocation(group string) (string, error) {
	out, err := a.Run.Capture("az", "group", "show", "--name", group, "--query", "location", "-o", "tsv")
	if err != nil {
		return "", fmt.Errorf("cannot read the resource group's region: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("the resource group reported no region — refusing to create resources without knowing where")
	}
	return strings.TrimSpace(out), nil
}

// dataCollectionResources lists the data-collection rules and endpoints in a
// resource group, as a set of ids.
func (a *App) dataCollectionResources(group string) (map[string]bool, error) {
	found := map[string]bool{}
	for _, kind := range []string{"Microsoft.Insights/dataCollectionRules", "Microsoft.Insights/dataCollectionEndpoints"} {
		out, err := a.Run.Capture("az", "resource", "list", "--resource-group", group,
			"--resource-type", kind, "--query", "[].id", "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("cannot list %s in %s — refusing to enable monitoring without knowing what was there first: %w", kind, group, err)
		}
		var ids []string
		if err := json.Unmarshal([]byte(out), &ids); err != nil {
			return nil, fmt.Errorf("cannot read the list of %s: %w", kind, err)
		}
		for _, id := range ids {
			found[strings.ToLower(id)] = true
		}
	}
	return found, nil
}

func newResources(before, after map[string]bool) []string {
	var out []string
	for id := range after {
		if !before[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func resourceNameFromID(id string) string {
	parts := strings.Split(strings.TrimRight(id, "/"), "/")
	if len(parts) == 0 {
		return id
	}
	return parts[len(parts)-1]
}

// scrapeJobOnly reduces the carried ConfigMap to the part an operator has to
// merge, so the message is something to paste rather than something to read.
func scrapeJobOnly(body []byte) string {
	text := string(body)
	if i := strings.Index(text, "scrape_configs:"); i >= 0 {
		return strings.TrimRight(text[i:], "\n")
	}
	return strings.TrimRight(text, "\n")
}

func indentBlock(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}
