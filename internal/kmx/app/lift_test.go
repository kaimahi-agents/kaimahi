package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// The managed path is the one that cannot be exercised in CI: it needs a
// subscription, and CI is keyless and cannot reach Azure. So what is asserted
// here is the SHAPE — that the files it will apply are in the binary, that
// they say what they must say, and above all that none of it changes the
// local path. The behaviour is proven by running it, once, by hand.

func TestTheManagedPathsFilesTravelInTheBinary(t *testing.T) {
	// A mistyped embed pattern is invisible until the file is missing on an
	// operator's machine, half way through a lift, with a cluster already
	// created and billing.
	for _, name := range []string{
		"k8s/observability/network-policy.yaml", "k8s/observability/scrape-config.yaml",
		"k8s/observability/workbook.json", "k8s/egress-copilot.yaml",
		"scripts/aks-up.sh", "scripts/aks-down.sh", "scripts/plane-deploy.sh",
		"scripts/netpol-probe.sh", "scripts/kube-guard.sh",
	} {
		embedded, err := kaimahi.Managed.ReadFile(name)
		if err != nil {
			t.Errorf("%s is not embedded in the binary: %v", name, err)
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("%s differs from the embedded copy — the binary would carry a stale one", name)
		}
	}
}

// The local path must be untouched by everything the managed path adds. This
// is the regression this work is most likely to cause: improving AKS by
// editing a manifest that kind also applies.
func TestTheLocalPathAppliesNothingFromTheManagedPath(t *testing.T) {
	for _, name := range []string{
		"observability/network-policy.yaml", "observability/scrape-config.yaml", "egress-copilot.yaml",
	} {
		if _, err := manifest(name); err == nil {
			t.Fatalf("k8s/%s is reachable through the manifest set the local path applies — "+
				"an Azure-only allowance must not be able to land on a kind cluster", name)
		}
	}
	// And the plane's own boundary, which kind DOES apply, must still open
	// 9092 to nothing but the self-hosted scraper it always named. The Azure
	// allowance is a separate object in a separate file; if it ever moves
	// into this one, kind starts carrying it.
	body, err := manifest("plane/network-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "ama-metrics") {
		t.Fatal("the plane's own NetworkPolicy now names Azure's metrics add-on — that file ships to kind and CI too")
	}
}

// The allowance is the access control: the operations port carries no
// authentication, so what this policy names is the whole of what may reach
// the plane's metrics.
func TestTheScraperAllowanceIsExactlyOneNamespaceOnePodOnePort(t *testing.T) {
	body, err := kaimahi.Managed.ReadFile("k8s/observability/network-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"name: kaimahi-proxy-metrics-azure",
		"namespace: kaimahi",
		"kubernetes.io/metadata.name: kube-system",
		"rsName: ama-metrics",
		"port: 9092",
		"policyTypes: [Ingress]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the scraper allowance does not say %q", want)
		}
	}
	// An egress rule here would be a hole in the plane's outbound boundary
	// added for a dashboard's convenience.
	if strings.Contains(text, "egress:") {
		t.Error("the scraper allowance grants egress; it exists only to let a scraper IN")
	}
	// The per-node DaemonSet runs the default targets, not the custom job.
	// Allowing it would open 9092 on every node for a scrape that never comes.
	if strings.Contains(text, "ama-metrics-node") || strings.Contains(text, "dsName") {
		t.Error("the scraper allowance opens the port to the add-on's DaemonSet as well as its replica")
	}
}

// The whole point of scraping pods rather than a Service: the operations port
// is on no Service deliberately, and a scrape job that needed one would force
// it to be exposed — trading a security property for a dashboard.
func TestTheScrapeJobDiscoversPodsAndOnlyTheOperationsPort(t *testing.T) {
	body, err := kaimahi.Managed.ReadFile("k8s/observability/scrape-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"name: ama-metrics-prometheus-config",
		"namespace: kube-system",
		"prometheus-config:",
		"role: pod",
		"regex: kaimahi-proxy",
		`regex: "9092"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the scrape job does not say %q", want)
		}
	}
	if strings.Contains(text, "role: service") || strings.Contains(text, "role: endpoints") {
		t.Error("the scrape job discovers Services or Endpoints — the operations port is on neither, on purpose")
	}
}

// The workbook is committed as a template, and the reason is a guardrail: an
// exported workbook bakes the subscription id, the resource group and the
// workspace ids into its serialized data as literals.
func TestTheWorkbookCarriesNoIdentifiersAndReadsBothDataPaths(t *testing.T) {
	body, err := kaimahi.Managed.ReadFile("k8s/observability/workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, param := range []string{
		"[parameters('azureMonitorWorkspaceResourceId')]",
		"[parameters('logAnalyticsResourceId')]",
		"[parameters('clusterResourceId')]",
	} {
		if !strings.Contains(text, param) {
			t.Errorf("the workbook does not take %s as a parameter — it would have to carry a literal", param)
		}
	}
	// The workbook's own name must be derived, not committed: a workbook name
	// is a GUID, and this repository refuses committed GUIDs.
	if !strings.Contains(text, "guid(resourceGroup().id") {
		t.Error("the workbook's name is not derived at deploy time")
	}
	// Both data paths, so an empty metric panel and an empty log panel mean
	// different things.
	if !strings.Contains(text, "kaimahi_decisions_total") {
		t.Error("the workbook does not read the plane's own decisions metric")
	}
	if !strings.Contains(text, "ContainerLogV2") {
		t.Error("the workbook does not read Container Insights logs")
	}
}

// Every metric the workbook plots must be one the plane actually exposes. A
// panel naming a series that does not exist renders empty, and an empty panel
// is indistinguishable from a scrape that is not landing — which is the exact
// confusion this lane exists to remove.
func TestEveryMetricTheWorkbookPlotsIsOneThePlaneExposes(t *testing.T) {
	body, err := kaimahi.Managed.ReadFile("k8s/observability/workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "plane", "internal", "metrics", "metrics.go"))
	if err != nil {
		t.Skipf("the plane's source is not on disk here: %v", err)
	}
	for _, metric := range []string{
		"kaimahi_decisions_total", "kaimahi_seam_degraded", "kaimahi_upstream_latency_seconds",
		"kaimahi_queue_depth", "kaimahi_queue_capacity", "kaimahi_build_info",
	} {
		if !strings.Contains(string(body), metric) {
			continue // not plotted; nothing to check
		}
		if !strings.Contains(string(source), metric) {
			t.Errorf("the workbook plots %s, which the plane does not expose", metric)
		}
	}
}

// The Azure CLI is a prerequisite and never fetched. The distinction matters
// enough to pin: the tools kmx does fetch are single binaries that hold
// nothing, and this one holds the operator's cloud credentials.
func TestTheAzureCLIIsNeverFetched(t *testing.T) {
	if depAz.fetchable {
		t.Fatal("kmx would download the Azure CLI")
	}
	if depAz.install == "" || depAz.why == "" {
		t.Fatal("a prerequisite kmx will not fetch must say what it is for and where to get it")
	}
}

// The defaults are the opinion. If one changes, the documentation that
// explains it has to change with it — an opinion nobody can find is just a
// default.
func TestTheOpinionatedDefaultsAreTheOnesTheGuideExplains(t *testing.T) {
	guide, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "aks.md"))
	if err != nil {
		t.Fatalf("docs/aks.md: %v", err)
	}
	for _, value := range []string{DefaultNodeSize, DefaultNetworkPolicy, DefaultLocation} {
		if !strings.Contains(string(guide), value) {
			t.Errorf("the managed path defaults to %q and docs/aks.md does not mention it", value)
		}
	}
}

// A resumed lift must not adopt a record written for the other branch: the
// two have opposite rules about deleting a resource group.
func TestTheRecordPathIsStablePerClusterAndSafeAsAFilename(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	a, err := liftRecordPath("My_Group (test)", "kaimahi-demo")
	if err != nil {
		t.Fatal(err)
	}
	b, err := liftRecordPath("My_Group (test)", "kaimahi-demo")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("the same cluster produced two different record paths")
	}
	if strings.ContainsAny(filepath.Base(a), " ()/") {
		t.Fatalf("the record filename is not safe: %s", filepath.Base(a))
	}
	other, _ := liftRecordPath("My_Group (test)", "other-cluster")
	if a == other {
		t.Fatal("two clusters share one record file")
	}
}

// The working tree the scripts run from has to be shaped like a checkout,
// because they resolve their neighbours relative to themselves: plane-deploy
// looks for k8s/plane one directory up, and netpol-probe execs kube-guard.sh
// from beside it. A flat directory would break both, at run time, on a
// cluster that already exists.
func TestTheCarriedScriptsGetTheLayoutTheyExpect(t *testing.T) {
	a := &App{Run: nil}
	dir, cleanup, err := a.liftWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	probe := filepath.Join(dir, "scripts", "netpol-probe.sh")
	if _, err := os.Stat(filepath.Join(filepath.Dir(probe), "kube-guard.sh")); err != nil {
		t.Error("netpol-probe.sh execs kube-guard.sh from its own directory, and it is not there")
	}
	deploy := filepath.Join(dir, "scripts", "plane-deploy.sh")
	manifests := filepath.Join(filepath.Dir(filepath.Dir(deploy)), "k8s", "plane")
	for _, name := range []string{"proxy.yaml", "postgres.yaml", "namespace.yaml", "upstreams.yaml", "network-policy.yaml"} {
		if _, err := os.Stat(filepath.Join(manifests, name)); err != nil {
			t.Errorf("plane-deploy.sh resolves k8s/plane/%s relative to itself, and it is not there", name)
		}
	}
	info, err := os.Stat(probe)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Error("the carried scripts are not executable")
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the working tree survives cleanup; it holds a copy of everything the lift applies")
	}
}

// One phase is not the journey. A `--step cluster` run that announced "the
// agent is running on a managed cluster" would be claiming six phases that
// have not happened — and the resumable shape exists precisely so that a
// half-finished lift is a normal state rather than one to paper over.
func TestOnePhaseSaysWhatIsLeftRatherThanClaimingTheJourney(t *testing.T) {
	opt := lift.Options{ResourceGroup: "rg", Cluster: "c", Registry: "reg12345", Observability: true, Step: "cluster"}
	rest := remainingSteps(opt)
	if len(rest) == 0 || rest[0] != "boundary" {
		t.Fatalf("after the cluster phase the next is boundary, got %v", rest)
	}
	for _, s := range rest {
		if s == "cluster" {
			t.Fatal("a finished phase is listed as still to do")
		}
	}
	last := opt
	last.Step = "verify"
	if got := remainingSteps(last); len(got) != 1 || !strings.Contains(got[0], "last phase") {
		t.Fatalf("the final phase should say so, got %v", got)
	}
	// With observability off, it must not be listed as remaining work.
	off := opt
	off.Observability = false
	for _, s := range remainingSteps(off) {
		if s == "observability" {
			t.Fatal("a phase that was switched off is listed as still to do")
		}
	}
}

// The phases must stay re-runnable and in an order where nothing is put
// behind a boundary before the boundary is proven.
func TestTheBoundaryIsProvenBeforeAnythingIsPutBehindIt(t *testing.T) {
	order := map[string]int{}
	for i, s := range lift.Steps {
		order[s] = i
	}
	for _, after := range []string{"credential", "plane", "agents", "observability", "verify"} {
		if order[after] < order["boundary"] {
			t.Errorf("%q runs before the boundary is proven", after)
		}
	}
	if order["credential"] > order["plane"] {
		t.Error("the model credential is checked after the plane is deployed — the proxy mounts it optionally, so a plane started first fails closed for minutes")
	}
	if order["observability"] > order["verify"] {
		t.Error("verification runs before observability is wired, so it cannot check that data arrives")
	}
}
