package lift

import (
	"strings"
	"testing"
)

func created() Options {
	return Options{ResourceGroup: "rg", Registry: "kaimahidemo", Cluster: "kaimahi-demo",
		Location: "westus3", NodeSize: "Standard_B4ms", NodeCount: 1, NetworkPolicy: "cilium", Observability: true}
}

func byo() Options {
	return Options{BringYourOwn: true, ResourceGroup: "rg", Registry: "kaimahidemo", Cluster: "kaimahi-demo", Observability: true}
}

func TestAWellFormedRequestIsAccepted(t *testing.T) {
	if err := created().Validate(); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if err := byo().Validate(); err != nil {
		t.Fatalf("bring-your-own branch: %v", err)
	}
}

func TestEveryRequiredParameterIsRefusedByName(t *testing.T) {
	// A cloud target is slow. Finding out about a missing parameter should
	// cost a second, not eight minutes and a half-created resource group.
	for _, tc := range []struct {
		name   string
		mutate func(*Options)
		says   string
	}{
		{"no resource group", func(o *Options) { o.ResourceGroup = "" }, "--resource-group"},
		{"no cluster", func(o *Options) { o.Cluster = "" }, "--cluster"},
		{"no registry", func(o *Options) { o.Registry = "" }, "--registry"},
		{"registry is not alphanumeric", func(o *Options) { o.Registry = "kaimahi-demo" }, "alphanumeric"},
		{"registry too short", func(o *Options) { o.Registry = "kmx" }, "--registry"},
		{"unusable region", func(o *Options) { o.Location = "West US 3" }, "--location"},
		{"unusable cluster name", func(o *Options) { o.Cluster = "-nope-" }, "--cluster"},
		{"unknown step", func(o *Options) { o.Step = "observabilty" }, "--step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := created()
			tc.mutate(&o)
			err := o.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("the refusal does not name %q: %v", tc.says, err)
			}
		})
	}
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	o := Options{}
	err := o.Validate()
	if err == nil {
		t.Fatal("an empty request was accepted")
	}
	for _, want := range []string{"--resource-group", "--cluster", "--registry"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("only some problems were reported; %q is missing from: %v", want, err)
		}
	}
}

func TestAPolicyEngineThatDoesNotEnforceIsRefused(t *testing.T) {
	for _, engine := range []string{"none", "None", "", "kubenet", "off"} {
		o := created()
		o.NetworkPolicy = engine
		if engine == "" {
			// An unset value is the default, not a refusal — the caller fills
			// it in. An explicitly empty one is caught where it is read.
			continue
		}
		if err := o.Validate(); err == nil {
			t.Fatalf("--network-policy %q was accepted; without an engine AKS ignores NetworkPolicy entirely", engine)
		}
	}
}

func TestBringYourOwnRefusesFlagsItCannotHonour(t *testing.T) {
	// Accepting --node-size on somebody else's cluster would mean accepting a
	// parameter that changes nothing, which is how an operator comes to
	// believe they configured something they did not.
	for flag, mutate := range map[string]func(*Options){
		"--location":       func(o *Options) { o.Location = "westus3" },
		"--node-size":      func(o *Options) { o.NodeSize = "Standard_B4ms" },
		"--node-count":     func(o *Options) { o.NodeCount = 3 },
		"--network-policy": func(o *Options) { o.NetworkPolicy = "cilium" },
	} {
		o := byo()
		mutate(&o)
		err := o.Validate()
		if err == nil {
			t.Fatalf("%s was accepted with --byo", flag)
		}
		if !strings.Contains(err.Error(), flag) || !strings.Contains(err.Error(), "never reshapes it") {
			t.Fatalf("%s: the refusal does not explain itself: %v", flag, err)
		}
	}
}

// The one phase the bring-your-own branch does not have. A full run drops it,
// but an explicit --step went straight past that and would have run the
// provisioning script — creating a cluster on the branch whose whole contract
// is that it creates none, and which teardown would then refuse to remove.
func TestBringYourOwnRefusesTheOnePhaseThatCreatesACluster(t *testing.T) {
	o := byo()
	o.Step = "cluster"
	err := o.Validate()
	if err == nil {
		t.Fatal("--byo --step cluster was accepted; that phase creates a cluster")
	}
	if !strings.Contains(err.Error(), "never creates one") {
		t.Fatalf("the refusal does not explain itself: %v", err)
	}
	// Every other phase stays available on this branch.
	for _, step := range Steps[1:] {
		ok := byo()
		ok.Step = step
		if err := ok.Validate(); err != nil {
			t.Errorf("--byo --step %s was refused: %v", step, err)
		}
	}
}

func TestBringYourOwnStillNeedsARegistryAndSaysWhy(t *testing.T) {
	o := byo()
	o.Registry = ""
	err := o.Validate()
	if err == nil {
		t.Fatal("accepted with no registry")
	}
	if !strings.Contains(err.Error(), "Nothing is published") {
		t.Fatalf("the refusal should say why there is no public image to fall back on: %v", err)
	}
}

func TestTheCreateBranchBuildsAClusterAndTheOtherDoesNot(t *testing.T) {
	if got := created().StepsToRun(); got[0] != "cluster" {
		t.Fatalf("the create branch must start by creating the cluster, got %v", got)
	}
	for _, step := range byo().StepsToRun() {
		if step == "cluster" {
			t.Fatal("the bring-your-own branch tried to create a cluster")
		}
	}
}

func TestObservabilityOffDropsOnlyThatPhase(t *testing.T) {
	o := created()
	o.Observability = false
	steps := strings.Join(o.StepsToRun(), " ")
	if strings.Contains(steps, "observability") {
		t.Fatal("observability ran despite being switched off")
	}
	for _, want := range []string{"cluster", "boundary", "plane", "agents", "verify"} {
		if !strings.Contains(steps, want) {
			t.Fatalf("switching observability off also dropped %q: %v", want, steps)
		}
	}
}

func TestOneStepRunsOnlyThatStep(t *testing.T) {
	o := created()
	o.Step = "observability"
	if got := o.StepsToRun(); len(got) != 1 || got[0] != "observability" {
		t.Fatalf("--step ran %v", got)
	}
}

func TestEveryStepSaysWhatItIsFor(t *testing.T) {
	for _, s := range Steps {
		if strings.TrimSpace(StepPurpose[s]) == "" {
			t.Fatalf("phase %q has no description, so the banner cannot say what it will do", s)
		}
	}
}

func TestTheBannerNamesTheTargetAndTheTeardownRule(t *testing.T) {
	// The context guard is not optional when the target is a cloud
	// subscription: what is about to happen, and where, is on screen first.
	b := created().Banner("someone@example.com", "Some Subscription")
	for _, want := range []string{"Some Subscription", "someone@example.com", "rg", "kaimahi-demo", "kaimahidemo", "westus3", "cilium", "kmx lift down"} {
		if !strings.Contains(b, want) {
			t.Fatalf("the create banner does not say %q:\n%s", want, b)
		}
	}

	y := byo().Banner("someone@example.com", "Some Subscription")
	if !strings.Contains(y, "NEVER DELETED") {
		t.Fatalf("the bring-your-own banner does not state the rule that matters:\n%s", y)
	}
	if strings.Contains(y, "comes back down with it") {
		t.Fatalf("the bring-your-own banner claims the resource group is torn down:\n%s", y)
	}
}

func TestTheBannerNeverPrintsASubscriptionID(t *testing.T) {
	// Subscription ids are identifiers this project keeps out of terminals,
	// and a banner is exactly what gets pasted into a pull request.
	b := created().Banner("someone@example.com", "Some Subscription")
	if strings.Contains(b, "00000000-0000-0000-0000-000000000000") {
		t.Fatal("the banner has somewhere to put a subscription id")
	}
	// The signature is the guard: there is no id parameter to leak.
	_ = Options.Banner
}

func TestAClusterWithNoPolicyEngineIsRefusedBeforeAnythingIsInstalled(t *testing.T) {
	for _, engine := range []string{"", "none", "None", "  "} {
		err := PolicyEngineVerdict(engine, true)
		if err == nil {
			t.Fatalf("engine %q was accepted", engine)
		}
		if !strings.Contains(err.Error(), "inert") {
			t.Fatalf("the refusal does not explain that policies would be present and inert: %v", err)
		}
	}
	for _, engine := range []string{"cilium", "azure", "calico", "Cilium"} {
		if err := PolicyEngineVerdict(engine, true); err != nil {
			t.Fatalf("engine %q was refused: %v", engine, err)
		}
	}
	if err := PolicyEngineVerdict("something-new", true); err == nil {
		t.Fatal("an unrecognised engine was assumed to enforce")
	}
}

func TestAnUnreadableEngineIsNotAnAnswer(t *testing.T) {
	// The same rule the resource-group checks follow: a question that could
	// not be asked is never read as a "yes" or a "no".
	err := PolicyEngineVerdict("cilium", false)
	if err == nil {
		t.Fatal("an unreadable answer was treated as an enforcing cluster")
	}
	if !strings.Contains(err.Error(), "Not claiming") {
		t.Fatalf("the refusal should claim nothing in either direction: %v", err)
	}
}
