package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// The ConfigMap Azure's metrics add-on reads custom scrape jobs from. It is
// cluster-wide and singular, which is why the bring-your-own branch treats an
// existing one as somebody else's property rather than as something to
// overwrite.
const (
	scrapeConfigMap       = "ama-metrics-prometheus-config"
	scrapeConfigNamespace = "kube-system"
	// The allowance that lets the add-on's scraper reach the plane's ops
	// port. Named once: teardown decides whether it may delete this object by
	// whether the run recorded creating it, and the two must agree.
	scraperPolicy = "kaimahi-proxy-metrics-azure"

	// The scrape job itself, as a namespaced object in the plane's own
	// namespace. Named once for the same reason the allowance is: teardown
	// decides whether it may delete this object by whether the run recorded
	// creating it, and the two must agree.
	scrapeMonitor = "kaimahi-plane"
	// The fully-qualified resource name, which is also the CRD's name. It is
	// spelled out rather than shortened to `podmonitor` because Azure's
	// add-on ships its CRDs under azmonitoring.coreos.com while the
	// open-source Prometheus operator uses monitoring.coreos.com, and on a
	// cluster running both, the short name is ambiguous and kubectl picks one.
	scrapeMonitorResource = "podmonitors.azmonitoring.coreos.com"

	// The Kind strings a resource is recorded under. They are constants
	// because verification looks a workspace up in the run record by exact
	// string match, in a different file: two copies of a sentence are two
	// chances for one of them to be edited alone, and the failure would be a
	// verify step that cannot find a workspace the lift definitely created.
	kindMetricsWorkspace = "Azure Monitor workspace (Managed Prometheus)"
	kindLogsWorkspace    = "Log Analytics workspace (Container Insights)"
	kindWorkbook         = "Azure Monitor workbook (the dashboard)"
)

// How long the add-on is given to install its custom resource definitions
// after it is enabled, before the phase concludes it has none.
const (
	scrapeMonitorCRDWait = 4 * time.Minute
	scrapeMonitorCRDPoll = 10 * time.Second
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

	// What was here before we arrived, read BEFORE anything is changed.
	// Teardown consults it so that it undoes this run's work and not the
	// operator's: switching off monitoring somebody was already relying on is
	// the same class of mistake as deleting their workspace.
	if err := a.recordPreExistingState(opt, record, save); err != nil {
		return err
	}

	// Refuse BEFORE creating a workspace, not after.
	//
	// A cluster whose monitoring was already on keeps sending where it was
	// already sending — this path will not repoint it, because that would
	// silently move somebody's telemetry into a workspace this run later
	// deletes. But creating our workspace anyway and then skipping the
	// enablement produces the worst of both: a billed, empty workspace, a
	// workbook wired to it, and a verification that waits five minutes for
	// samples that are arriving somewhere else and then reports the scrape as
	// broken. The cluster is fine; the dashboard is pointed at the wrong
	// place. So this stops before anything is created.
	if err := a.refuseIfMonitoringWasAlreadyOn(opt, record); err != nil {
		return err
	}

	metricsName := lift.MetricsWorkspaceName(record.RunID)
	metricsID, err := a.ensureRecorded(record, save, lift.Resource{
		Kind: kindMetricsWorkspace, Name: metricsName, InResourceGroup: true,
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
		Kind: kindLogsWorkspace, Name: logsName, InResourceGroup: true,
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
	//
	// The cluster's MANAGED node group is watched too. Azure puts some of what
	// the add-ons create there rather than beside the cluster, and a resource
	// the snapshot never looks at is one that is never recorded — which on a
	// cluster we do not own is a resource left behind with nothing to say it
	// exists. That is the same failure the Prometheus rule groups already
	// caused once.
	groups, err := a.monitoringGroups(opt)
	if err != nil {
		return err
	}
	before, err := a.dataCollectionResourcesIn(groups)
	if err != nil {
		return err
	}

	if err := a.enableMetricsAddon(opt, metricsID); err != nil {
		return err
	}
	if err := a.enableLogsAddon(opt, logsID); err != nil {
		return err
	}

	after, err := a.dataCollectionResourcesIn(groups)
	if err != nil {
		return err
	}
	for _, id := range newResources(before, after) {
		if err := record.Add(lift.Resource{
			Kind: "created by the monitoring add-on (" + resourceTypeFromID(id) + ")",
			Name: resourceNameFromID(id), ID: id, InResourceGroup: lift.InGroup(id, opt.ResourceGroup),
			Billing: "no standing charge of its own; it routes or records data into the workspaces, which do charge",
		}); err != nil {
			return err
		}
	}
	if err := save(); err != nil {
		return err
	}

	if err := a.wireScrape(record, save, work); err != nil {
		return err
	}
	return a.deployWorkbook(opt, record, save, work, clusterID, logsID, metricsID)
}

// monitorState is what the cluster already has, read before we change it.
type monitorState struct {
	metricsEnabled bool
	// The metrics profile does not report which Azure Monitor workspace it
	// sends to, so there is deliberately no field for it: an already-enabled
	// metrics add-on is left alone rather than compared, because a comparison
	// this cannot make must not be implied by a field that is always empty.
	logsEnabled   bool
	logsWorkspace string
}

// readMonitorState asks the control plane what monitoring the cluster already
// has. It fails rather than guessing: every decision below — whether to
// enable, whether to refuse, and later whether teardown may disable — rests on
// this answer, and a wrong default is either a refusal that should not happen
// or a cluster whose monitoring we quietly take over.
func (a *App) readMonitorState(opt lift.Options) (monitorState, error) {
	var st monitorState
	out, err := a.Run.Capture("az", "aks", "show", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup, "-o", "json")
	if err != nil {
		return st, fmt.Errorf("cannot read the cluster's current monitoring configuration, so whether this would be enabling it or taking it over is unknown — refusing: %w", err)
	}
	var cluster struct {
		AzureMonitorProfile *struct {
			Metrics *struct {
				Enabled bool `json:"enabled"`
			} `json:"metrics"`
		} `json:"azureMonitorProfile"`
		AddonProfiles map[string]struct {
			Enabled bool              `json:"enabled"`
			Config  map[string]string `json:"config"`
		} `json:"addonProfiles"`
	}
	if err := json.Unmarshal([]byte(out), &cluster); err != nil {
		return st, fmt.Errorf("cannot read the cluster's monitoring configuration: %w", err)
	}
	if p := cluster.AzureMonitorProfile; p != nil && p.Metrics != nil {
		st.metricsEnabled = p.Metrics.Enabled
	}
	// The add-on profile key is case-inconsistent across API versions, so it
	// is matched case-insensitively rather than assumed.
	for name, profile := range cluster.AddonProfiles {
		if !strings.EqualFold(name, "omsagent") {
			continue
		}
		st.logsEnabled = profile.Enabled
		for k, v := range profile.Config {
			if strings.EqualFold(k, "logAnalyticsWorkspaceResourceID") {
				st.logsWorkspace = v
			}
		}
	}
	return st, nil
}

// refuseIfMonitoringWasAlreadyOn stops a run that would produce a dashboard
// pointing somewhere other than where the cluster's telemetry goes.
//
// It reads the state this run RECORDED on arrival, not the state now, and that
// distinction is what keeps the phase resumable: an add-on this run itself
// enabled reads as "on" the second time through, and re-running a phase must
// not become a refusal.
//
// Reusing the operator's existing workspace instead would be the friendlier
// answer, and it is not available for metrics: the cluster's metrics profile
// does not report which Azure Monitor workspace it feeds — the link runs
// through a data-collection rule — so there is no supported lookup to reuse.
// Rather than guess at one, this says what is wrong and what to do.
func (a *App) refuseIfMonitoringWasAlreadyOn(opt lift.Options, record *lift.Record) error {
	// Unestablished prior state is a refusal, not a pass.
	//
	// It should be unreachable — recordPreExistingState either errors or sets
	// this — and that is exactly why it is worth stating: the gate's whole job
	// is to not act on an unknown, and a gate that opens when it has been told
	// nothing is one reordering away from opening for real. The rest of this
	// package already reads unestablished state as "touch nothing"
	// (Pre.WeEnabledMetrics and its siblings); this is the same rule pointing
	// the other way.
	if !record.Before.Recorded {
		return fmt.Errorf("kmx lift: what monitoring this cluster had before this run was never established, so whether this would be enabling it or taking it over is unknown — refusing rather than proceeding blind")
	}

	var already []string
	if record.Before.MetricsAddonEnabled {
		already = append(already, "Managed Prometheus")
	}
	if record.Before.LogsAddonEnabled {
		already = append(already, "Container Insights")
	}
	if len(already) == 0 {
		return nil
	}
	return fmt.Errorf(`%s already enabled on this cluster before this run.

  This path will not repoint it: moving your telemetry into a workspace this
  run owns and later deletes would break monitoring you rely on, quietly. But
  it cannot point a dashboard at the workspace you already use either — the
  cluster does not report which one that is for metrics.

  So it would create workspaces nothing sends to, wire a dashboard to them,
  and then report the scrape as broken. Refusing instead.

  Either keep what you have and skip this phase:

    kmx lift --byo --observability=false %s

  or turn the add-on off first, if you meant this run to own it:

    az aks disable-addons --name %s --resource-group %s --addons monitoring
    az aks update --name %s --resource-group %s --disable-azure-monitor-metrics`,
		strings.Join(already, " and "), liftIdentityFlags(opt),
		opt.Cluster, opt.ResourceGroup, opt.Cluster, opt.ResourceGroup)
}

// enableMetricsAddon turns Managed Prometheus on, unless it is already on.
//
// Already on here can only be this run's own doing: a cluster that had it
// before was refused at the gate above (refuseIfMonitoringWasAlreadyOn),
// which is where somebody else's monitoring is protected. So this reads as
// a resumed run and does nothing. Unlike Container Insights, there is no
// workspace to compare — the metrics profile does not report which Azure
// Monitor workspace it feeds.
func (a *App) enableMetricsAddon(opt lift.Options, metricsID string) error {
	st, err := a.readMonitorState(opt)
	if err != nil {
		return err
	}
	if st.metricsEnabled {
		a.notef("Managed Prometheus is already enabled on this cluster; leaving it as it is.")
		return nil
	}
	a.notef("enabling Managed Prometheus on the cluster (a few minutes)")
	return a.Run.Run("az", "aks", "update", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
		"--enable-azure-monitor-metrics", "--azure-monitor-workspace-resource-id", metricsID, "--output", "none")
}

func (a *App) enableLogsAddon(opt lift.Options, logsID string) error {
	st, err := a.readMonitorState(opt)
	if err != nil {
		return err
	}
	switch {
	case st.logsEnabled && lift.SameResourceID(st.logsWorkspace, logsID):
		a.notef("Container Insights is already sending to this run's workspace; nothing to do.")
		return nil
	case st.logsEnabled:
		return fmt.Errorf(`Container Insights is already enabled on this cluster and sends to a DIFFERENT workspace.

  Enabling it again would repoint your cluster's logs at a workspace this run
  created and will later delete — so your collection would silently stop when
  this is torn down. Refusing.

  Use the workspace you already have, or turn the add-on off first if you
  meant to replace it:
    az aks disable-addons --name %s --resource-group %s --addons monitoring`,
			opt.Cluster, opt.ResourceGroup)
	}
	a.notef("enabling Container Insights on the cluster (a few minutes)")
	return a.Run.Run("az", "aks", "enable-addons", "--name", opt.Cluster, "--resource-group", opt.ResourceGroup,
		"--addons", "monitoring", "--workspace-resource-id", logsID, "--output", "none")
}

// recordPreExistingState writes down what the cluster had before this run
// touched it, so teardown can undo this run's changes and nothing else.
func (a *App) recordPreExistingState(opt lift.Options, record *lift.Record, save func() error) error {
	if record.Before.Recorded {
		return nil // a resumed run: the first pass established this
	}
	st, err := a.readMonitorState(opt)
	if err != nil {
		return err
	}
	policyExisted, err := a.objectExists("kaimahi", "networkpolicy", scraperPolicy)
	if err != nil {
		return err
	}
	monitorExisted, err := a.objectExists("kaimahi", scrapeMonitorResource, scrapeMonitor)
	if err != nil {
		return err
	}
	record.Before = lift.Pre{
		Recorded:             true,
		MetricsAddonEnabled:  st.metricsEnabled,
		LogsAddonEnabled:     st.logsEnabled,
		ScraperPolicyExisted: policyExisted,
		ScrapeMonitorExisted: monitorExisted,
	}
	return save()
}

// objectExists answers for one cluster object, and refuses to guess. Only a
// genuine NotFound is absence; an unreachable API server or an RBAC denial
// must not be recorded as "it was not there", because that is what would later
// authorise deleting it.
//
// It takes the namespace, kind and name as three named parameters rather than
// a variadic argument list, and builds the whole command line itself. That is
// the fix for a real defect: the variadic form let a caller pass
// `"-n", "kaimahi", "networkpolicy", name` with the `get` verb simply absent,
// which kubectl reads as an attempt to run a PLUGIN called `networkpolicy` and
// rejects with "flags cannot be placed before plugin name". Every lift failed
// its observability phase for that reason, and nothing but running it could
// notice, because a variadic list of strings has no shape a compiler or a
// reader checks. Three parameters have one.
func (a *App) objectExists(namespace, kind, name string) (bool, error) {
	_, err := a.kubectlCapture("-n", namespace, "get", kind, name, "-o", "name")
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err), noSuchResourceType(err):
		return false, nil
	default:
		return false, fmt.Errorf("cannot tell whether %s already exists, so whether this run would be creating it is unknown — refusing rather than recording a guess: %w",
			strings.Join([]string{"-n", namespace, kind, name}, " "), err)
	}
}

// noSuchResourceType reports the one refusal that is not a refusal: kubectl
// saying the cluster has no such KIND at all.
//
// It is not a NotFound — kubectl says "the server doesn't have a resource
// type", which isNotFound deliberately does not match, because for most reads
// a missing CRD means the operator is aimed at a cluster where the thing they
// are asking about cannot exist and guessing would be wrong. Here it is an
// answer rather than a failure: if the PodMonitor kind is not installed, no
// PodMonitor of ours is on this cluster and none can be. The distinction
// matters because the prior-state read runs BEFORE the metrics add-on is
// enabled, and the add-on is what installs the CRD — so on every fresh
// cluster, created or bring-your-own, this is the expected answer.
func noSuchResourceType(err error) bool {
	if err == nil {
		return false
	}
	// One string, and deliberately only one. kubectl's other 404 wording,
	// "the server could not find the requested resource", is also what it says
	// for a request to a namespace that does not exist — and this classifier
	// is consulted for the NetworkPolicy read too, where widening absence
	// would eventually authorise deleting an allowance this run did not make.
	return strings.Contains(err.Error(), "doesn't have a resource type")
}

// waitForScrapeMonitorCRD gives the add-on time to install its own custom
// resource definitions before concluding it has none.
//
// The bound is a real answer either way: a cluster whose add-on supports
// custom resources installs them within it, and one that does not never will,
// so waiting longer would only lengthen the wrong outcome.
func (a *App) waitForScrapeMonitorCRD() (bool, error) {
	deadline := a.timeNow().Add(scrapeMonitorCRDWait)
	told := false
	for {
		present, err := a.clusterObjectExists("crd", scrapeMonitorResource)
		if err != nil || present {
			return present, err
		}
		if a.timeNow().After(deadline) {
			return false, nil
		}
		if !told {
			a.notef("waiting for the metrics add-on to install its custom resource definitions (up to %s)", scrapeMonitorCRDWait)
			told = true
		}
		time.Sleep(scrapeMonitorCRDPoll)
	}
}

// clusterObjectExists is objectExists for an object that is not in a
// namespace. It refuses to guess for the same reason and in the same words.
func (a *App) clusterObjectExists(kind, name string) (bool, error) {
	_, err := a.kubectlCapture("get", kind, name, "-o", "name")
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("cannot tell whether the cluster has %s %s, so whether a PodMonitor would be read here is unknown — refusing rather than acting on a guess: %w",
			kind, name, err)
	}
}

// wireScrape opens the one hole the scraper needs and tells the add-on what
// to scrape.
//
// The allowance comes first. The port carries no authentication, so the
// NetworkPolicy IS the access control, and a scrape job pointed at a port
// nothing may reach fails in a way that looks like a broken agent rather than
// a missing rule.
//
// Then a PodMonitor in the plane's own namespace, and nothing at all in
// kube-system. The cluster-wide ama-metrics-prometheus-config ConfigMap is not
// read, not written and not deleted by this project any more: it is a single
// document shared by every custom scrape job on the cluster, and the previous
// arrangement made this run either the thing that overwrote somebody's jobs or
// the thing that stopped and asked them to merge ours by hand. A namespaced
// object owned by whoever created it removes both, and it is what an adopter
// copies to get their own pods scraped.
func (a *App) wireScrape(record *lift.Record, save func() error, work string) error {
	if err := a.applyManaged(work, "k8s/observability/network-policy.yaml"); err != nil {
		return err
	}

	// The add-on installs this CRD itself when managed Prometheus is enabled,
	// but not at the moment the enabling command returns. `az aks update`
	// completes when ARM says the cluster is updated; the add-on's own
	// components arrive in the cluster afterwards, on their own schedule. A
	// single read here would therefore answer "absent" on a perfectly modern
	// cluster and quietly take the fallback — applying nothing, recording
	// nothing, and leaving `verify` to report five minutes later that no
	// sample arrived. So it waits, and only a cluster that still has no CRD
	// after that is treated as one whose add-on predates custom resources.
	present, err := a.waitForScrapeMonitorCRD()
	if err != nil {
		return err
	}
	if !present {
		body, readErr := a.managedFile(work, "k8s/observability/scrape-config.yaml")
		if readErr != nil {
			return readErr
		}
		a.notef(`this cluster has no %s, so its metrics add-on cannot read a PodMonitor.

  Nothing else in this phase is affected, and the metrics panels will stay
  empty until you add the job below to the %s
  ConfigMap in %s, under the prometheus-config key. That ConfigMap holds
  every other custom scrape job on this cluster, so merging it is yours to
  do rather than ours:

    kubectl -n %s edit configmap %s

%s`, scrapeMonitorResource, scrapeConfigMap, scrapeConfigNamespace,
			scrapeConfigNamespace, scrapeConfigMap, indentBlock(scrapeJobOnly(body)))
		return nil
	}
	if err := a.applyManaged(work, "k8s/observability/podmonitor.yaml"); err != nil {
		return err
	}
	// Recorded the moment it exists, and before anything else can fail. What
	// teardown may delete is what this run did, never what it inferred.
	record.ScrapeMonitorApplied = true
	return save()
}

// deployWorkbook puts the operator-facing view in the same resource group as
// everything else, so a single group deletion accounts for it too.
func (a *App) deployWorkbook(opt lift.Options, record *lift.Record, save func() error, work, clusterID, logsID, metricsID string) error {
	name := lift.WorkbookName(record.RunID)
	_, err := a.ensureRecorded(record, save, lift.Resource{
		Kind: kindWorkbook, Name: name, InResourceGroup: true,
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

// addOnCreatedTypes are the resource types the monitoring add-ons create on
// the operator's behalf. We do not choose their names, so what appeared has to
// be established by difference rather than by matching one.
//
// The third entry is here because a live run found it. Enabling Managed
// Prometheus also writes default Prometheus RULE GROUPS — eight of them on the
// verified run — and a teardown that knew only about data-collection rules
// would have left them on somebody else's subscription, recording series and
// billing for them, with nothing in the run record to say they existed.
// Watching the subscription's own activity log is what turned that up; it is
// not in the documentation for the flag.
var addOnCreatedTypes = []string{
	"Microsoft.Insights/dataCollectionRules",
	"Microsoft.Insights/dataCollectionEndpoints",
	"Microsoft.AlertsManagement/prometheusRuleGroups",
}

// monitoringGroups is where the add-ons put things: beside the cluster, and
// in the cluster's managed node resource group.
//
// The node group is read from the cluster rather than derived from the
// documented `MC_<group>_<cluster>_<region>` shape, because that shape is a
// default an operator can override at create time — and a snapshot pointed at
// a group that does not exist would silently watch nothing.
// A failed lookup is an ERROR, not a shorter list. Silently dropping the node
// group would narrow the snapshot to the cluster's own group, and a resource
// the snapshot never looks at is one that is never recorded — which on a
// cluster we do not own is a resource left behind with nothing to say it
// exists. That is precisely how the Prometheus rule groups escaped the first
// time, and the fix must not reintroduce it one level up.
func (a *App) monitoringGroups(opt lift.Options) ([]string, error) {
	node, err := a.Run.Capture("az", "aks", "show", "--name", opt.Cluster,
		"--resource-group", opt.ResourceGroup, "--query", "nodeResourceGroup", "-o", "tsv")
	if err != nil {
		return nil, fmt.Errorf(`cannot read the cluster's managed node resource group, so what the monitoring add-ons create there could not be watched for.

  Enabling them without that snapshot would leave resources behind with
  nothing in the run record to say they exist. Refusing rather than
  half-watching: %w`, err)
	}
	groups := []string{opt.ResourceGroup}
	if n := strings.TrimSpace(node); n != "" && !strings.EqualFold(n, opt.ResourceGroup) {
		groups = append(groups, n)
	}
	return groups, nil
}

// dataCollectionResourcesIn lists everything the add-ons create across the
// given resource groups, as a set of ids.
func (a *App) dataCollectionResourcesIn(groups []string) (map[string]bool, error) {
	all := map[string]bool{}
	for _, g := range groups {
		found, err := a.dataCollectionResources(g)
		if err != nil {
			return nil, err
		}
		for id := range found {
			all[id] = true
		}
	}
	return all, nil
}

// dataCollectionResources lists everything the add-ons create in a resource
// group, as a set of ids.
func (a *App) dataCollectionResources(group string) (map[string]bool, error) {
	found := map[string]bool{}
	for _, kind := range addOnCreatedTypes {
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

// resourceTypeFromID pulls the last provider segment out of an ARM id, so a
// recorded resource says what KIND of thing it is rather than only its name —
// "prometheusRuleGroups" and "dataCollectionRules" are removed the same way
// but are worth telling apart when one is left behind.
func resourceTypeFromID(id string) string {
	parts := strings.Split(strings.TrimRight(id, "/"), "/")
	for i := len(parts) - 2; i > 0; i-- {
		if strings.EqualFold(parts[i-1], "providers") {
			return strings.Join(parts[i:len(parts)-1], "/")
		}
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return "resource"
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
