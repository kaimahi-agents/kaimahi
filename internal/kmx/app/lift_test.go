package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
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
		"k8s/observability/network-policy.yaml", "k8s/observability/podmonitor.yaml",
		"k8s/observability/scrape-config.yaml", "k8s/observability/workbook.json",
		"k8s/egress-copilot.yaml",
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

func TestAKSUpPrintsDirectLiftCommandsWhenLiftInvokesIt(t *testing.T) {
	body, err := kaimahi.Managed.ReadFile("scripts/aks-up.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"KMX_LIFT_CONTINUE", "KMX_LIFT_DOWN", "continue:  $KMX_LIFT_CONTINUE", "teardown:  $KMX_LIFT_DOWN"} {
		if !strings.Contains(text, want) {
			t.Errorf("embedded aks-up.sh does not carry %q", want)
		}
	}
	if !strings.Contains(text, `if [ -n "${KMX_LIFT_CONTINUE:-}" ]`) || !strings.Contains(text, "make netpol-verify") {
		t.Error("aks-up.sh no longer keeps its direct-script guidance as the fallback")
	}
}

// The local path must be untouched by everything the managed path adds. This
// is the regression this work is most likely to cause: improving AKS by
// editing a manifest that kind also applies.
func TestTheLocalPathAppliesNothingFromTheManagedPath(t *testing.T) {
	for _, name := range []string{
		"observability/network-policy.yaml", "observability/podmonitor.yaml",
		"observability/scrape-config.yaml", "egress-copilot.yaml",
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
// it to be exposed — trading a security property for a dashboard. A
// ServiceMonitor is exactly that trade, so the job is a PodMonitor.
func TestTheScrapeJobIsAPodMonitorAimedAtTheOperationsPortByName(t *testing.T) {
	body, err := kaimahi.Managed.ReadFile("k8s/observability/podmonitor.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		// Azure's add-on ships the operator CRDs under its OWN group so that a
		// cluster already running the open-source Prometheus operator keeps two
		// separate sets of jobs. Under monitoring.coreos.com this object is
		// valid, applies cleanly, and is never scraped by the add-on.
		"apiVersion: azmonitoring.coreos.com/v1",
		"kind: PodMonitor",
		"name: kaimahi-plane",
		"namespace: kaimahi",
		"app: kaimahi-proxy",
		"podMetricsEndpoints:",
		// Named, not numbered: pod discovery yields one target per declared
		// container port, and without this the scraper would also try the two
		// data ports, the inbound port and the admin port.
		"- port: ops",
		"path: /metrics",
		// Azure's collector drops a whole job that exceeds these and says
		// nothing; the symptom is an empty panel.
		"labelLimit: 63",
		"labelNameLengthLimit: 511",
		"labelValueLengthLimit: 1023",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the plane's scrape job does not say %q", want)
		}
	}
	if strings.Contains(text, "kind: ServiceMonitor") {
		t.Error("the scrape job discovers Services — the operations port is on none, on purpose")
	}
	// The ops port must be a named port on the plane's own pod, or `port: ops`
	// resolves to nothing and the job scrapes an empty target set.
	proxy, err := manifest("plane/proxy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(proxy), "- name: ops\n              containerPort: 9092") {
		t.Error("the plane's container no longer declares 9092 as a port named `ops`, " +
			"so the PodMonitor's `port: ops` names nothing")
	}
}

// The ConfigMap is cluster-wide and singular, so every custom scrape job on
// the cluster shares it. This project writing it means either overwriting
// somebody's jobs or stopping to ask them to merge ours — and its teardown
// deleting it means removing jobs it never made. Now that the plane's job is a
// PodMonitor, nothing here may touch that document at all: the file is carried
// only so the refusal on a cluster with no CRD has something to print.
func TestTheLiftNeverReadsWritesOrDeletesTheClusterWideScrapeConfigMap(t *testing.T) {
	fset := token.NewFileSet()
	found := 0
	for _, file := range []string{"lift_observability.go", "lift_down.go"} {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "kubectlCapture", "kubectlRun", "kubectlQuiet", "applyManaged":
			default:
				return true
			}
			for _, arg := range call.Args {
				name := ""
				switch a := arg.(type) {
				case *ast.Ident:
					name = a.Name
				case *ast.BasicLit:
					name = a.Value
				}
				if strings.Contains(name, "scrapeConfigMap") ||
					strings.Contains(name, "ama-metrics-prometheus-config") ||
					strings.Contains(name, "scrape-config.yaml") {
					t.Errorf("%s: this reaches the cluster-wide scrape ConfigMap through %s — "+
						"it is somebody else's document and the lift no longer has business in it",
						fset.Position(call.Pos()), sel.Sel.Name)
				}
			}
			found++
			return true
		})
	}
	if found < 5 {
		t.Fatalf("only %d cluster calls were examined — the scan is passing vacuously", found)
	}
}

// The fallback text the refusal prints, which is the one place the ConfigMap
// route survives. It is not applied by anything; a test asserts that above.
func TestTheFallbackScrapeJobDiscoversPodsAndOnlyTheOperationsPort(t *testing.T) {
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
		// The plane is in this repository, so its absence is a broken
		// checkout and not a reason to declare the workbook fine.
		t.Fatalf("the plane's source is not on disk here: %v", err)
	}
	// The series are taken from the workbook, not listed here. A list would
	// pass over exactly the panel nobody remembered to add to it — the one
	// most likely to be plotting something that does not exist.
	plotted := map[string]bool{}
	for _, name := range regexp.MustCompile(`kaimahi_[a-z_]+`).FindAllString(string(body), -1) {
		plotted[name] = true
	}
	if len(plotted) == 0 {
		t.Fatal("no plane metrics found in the workbook at all; either it plots nothing or this test can no longer read it")
	}
	for name := range plotted {
		if strings.Contains(string(source), name) {
			continue
		}
		// A histogram is exported as three derived series; the plane declares
		// only the base name, so that is what has to exist.
		base := name
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			base = strings.TrimSuffix(base, suffix)
		}
		if !strings.Contains(string(source), base) {
			t.Errorf("the workbook plots %s, which the plane does not expose", name)
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

// A cluster whose monitoring was already on keeps sending where it was
// sending. Creating our own workspaces anyway would bill for something nothing
// feeds, wire the dashboard to it, and then report the scrape as broken — so
// the refusal has to come before anything is created.
//
// The property that matters just as much: this must NOT fire on a resumed run.
// An add-on this run itself enabled reads as "on" the second time through, and
// re-running a phase must not turn into a refusal.
func TestMonitoringAlreadyOnIsRefusedButAResumedRunIsNot(t *testing.T) {
	a := &App{}
	opt := lift.Options{BringYourOwn: true, ResourceGroup: "rg", Cluster: "c", Registry: "reg12345"}

	for _, tc := range []struct {
		name   string
		before lift.Pre
		says   string
	}{
		{"metrics was already on", lift.Pre{Recorded: true, MetricsAddonEnabled: true}, "Managed Prometheus"},
		{"logs were already on", lift.Pre{Recorded: true, LogsAddonEnabled: true}, "Container Insights"},
		{"both were already on", lift.Pre{Recorded: true, MetricsAddonEnabled: true, LogsAddonEnabled: true}, "and"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := a.refuseIfMonitoringWasAlreadyOn(opt, &lift.Record{Before: tc.before})
			if err == nil {
				t.Fatal("accepted; this would create a workspace nothing sends to")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not name %q: %v", tc.says, err)
			}
			// It has to say how to get past it, both ways.
			if !strings.Contains(err.Error(), "--observability=false") || !strings.Contains(err.Error(), "disable-addons") {
				t.Errorf("the refusal offers no way forward: %v", err)
			}
		})
	}

	// A cluster established to have had monitoring OFF — which is both a fresh
	// cluster and a resumed run that turned the add-ons on itself. Recorded is
	// what makes it "established"; the flags being false is what makes it
	// "off". Re-running the phase must be a no-op, not a wall.
	t.Run("monitoring established as off is not refused", func(t *testing.T) {
		err := a.refuseIfMonitoringWasAlreadyOn(opt, &lift.Record{Before: lift.Pre{Recorded: true}})
		if err != nil {
			t.Fatalf("a cluster known to have had monitoring off was refused: %v", err)
		}
	})

	// An empty record is NOT a fresh cluster — a fresh cluster is Recorded
	// with both flags off. It is a cluster nobody looked at, and the gate
	// exists to not act on an unknown. Asserting that it proceeds would pin
	// the opposite of the rule the rest of this package follows.
	t.Run("prior state never established is refused", func(t *testing.T) {
		err := a.refuseIfMonitoringWasAlreadyOn(opt, &lift.Record{})
		if err == nil {
			t.Fatal("a run whose prior monitoring state was never established was allowed to proceed")
		}
		if !strings.Contains(err.Error(), "never established") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	})
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

// A subscription id is an identifier this project keeps out of terminals, and
// the lift banner is exactly the text an operator pastes into a pull request.
// The banner itself has no id parameter, so asserting against its output alone
// proves nothing — the signed-in account it is built from is where an id is in
// scope, and the call site is where one could be handed over.
func TestNothingTheLiftPrintsBeforeItActsCarriesTheSubscriptionID(t *testing.T) {
	// A synthetic fixture, in the shape Azure uses and nothing more: four
	// distinct hex digits, which is what keeps the tree's own Azure-identifier
	// scanner able to tell an invented id from a real one.
	const fakeSubscriptionID = "aaaaaaaa-bbbb-cccc-dddd-aaaabbbbcccc"

	dir := t.TempDir()
	stub := "#!/bin/sh\necho '{\"name\":\"Some Subscription\",\"id\":\"" +
		fakeSubscriptionID + "\",\"user\":{\"name\":\"someone@example.com\"}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "az"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	a := &App{Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}}
	acct, err := a.azAccount()
	if err != nil {
		t.Fatal(err)
	}
	// Without this the rest is vacuous: a fixture carrying no id could not
	// leak one however the code behaved.
	if acct.ID != fakeSubscriptionID {
		t.Fatalf("the account under test carries no subscription id (%q), so nothing here is being tested", acct.ID)
	}

	banner := lift.Options{ResourceGroup: "rg", Registry: "kaimahidemo", Cluster: "kaimahi-demo",
		Location: "westus3", NetworkPolicy: "cilium", Observability: true}.
		Banner(acct.User.Name, acct.Name)
	if banner == "" {
		t.Fatal("the banner rendered empty; there is nothing to check")
	}
	if strings.Contains(banner, acct.ID) {
		t.Errorf("the banner carries the subscription id:\n%s", banner)
	}

	// The banner cannot leak what it is never given, so what actually has to
	// hold is at the call site: the lift hands it the subscription name and
	// the signed-in user, and never the id.
	source, err := os.ReadFile("lift.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`Banner\(([^)]*)\)`).FindStringSubmatch(string(source))
	if call == nil {
		t.Fatal("no Banner call found in lift.go; this test can no longer see the call site")
	}
	if strings.Contains(call[1], ".ID") {
		t.Errorf("the lift passes the subscription id to the banner: Banner(%s)", call[1])
	}
}
