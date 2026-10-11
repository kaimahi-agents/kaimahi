package app

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// Provisioning assets travel with the binary; AKS lifecycle side effects are
// exercised with command fakes.
func TestTheManagedPathsFilesTravelInTheBinary(t *testing.T) {
	// A mistyped embed pattern is invisible until the file is missing on an
	// operator's machine, half way through a lift, with a cluster already
	// created and billing.
	for _, name := range []string{
		"scripts/aks-up.sh", "scripts/aks-down.sh", "scripts/kube-guard.sh",
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
	for _, want := range []string{"KMX_LIFT_CONTINUE", "KMX_LIFT_DOWN", "continue:  $KMX_LIFT_CONTINUE", "teardown:  $teardown"} {
		if !strings.Contains(text, want) {
			t.Errorf("embedded aks-up.sh does not carry %q", want)
		}
	}
	if !strings.Contains(text, `if [ -n "${KMX_LIFT_CONTINUE:-}" ]`) ||
		!strings.Contains(text, "This standalone script does not create a KMX ownership record") {
		t.Error("aks-up.sh no longer keeps its direct-script guidance as the fallback")
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

// Provisioning scripts keep their executable bits and their relative guard
// path, and their temporary workspace must not survive cleanup.
func TestTheCarriedScriptsGetTheLayoutTheyExpect(t *testing.T) {
	a := &App{Run: nil}
	dir, cleanup, err := a.liftWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for _, name := range []string{"aks-up.sh", "aks-down.sh", "kube-guard.sh"} {
		info, err := os.Stat(filepath.Join(dir, "scripts", name))
		if err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("carried script %s is missing or not executable: %v", name, err)
		}
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the working tree survives cleanup; it holds a copy of everything the lift applies")
	}
}

// One phase is not the journey. A `--step cluster` run that announced "the
// agent is running on a managed cluster" would be claiming later phases that
// have not happened — and the resumable shape exists precisely so that a
// half-finished lift is a normal state rather than one to paper over.
func TestOnePhaseSaysWhatIsLeftRatherThanClaimingTheJourney(t *testing.T) {
	opt := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "rg", Cluster: "c", Registry: "reg12345", Observability: true, Step: "cluster"}
	rest := remainingSteps(opt)
	if len(rest) == 0 || rest[0] != "orka" {
		t.Fatalf("after the cluster phase the next is orka, got %v", rest)
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
// sending. Creating our workspaces anyway would bill for unused resources,
// so the refusal has to come before anything is created.
//
// The property that matters just as much: this must NOT fire on a resumed run.
// An add-on this run itself enabled reads as "on" the second time through, and
// re-running a phase must not turn into a refusal.
func TestMonitoringAlreadyOnIsRefusedButAResumedRunIsNot(t *testing.T) {
	a := &App{Cfg: &config.Config{KubeContext: "unrelated"}}
	opt := lift.Options{Payload: lift.PayloadOrka, BringYourOwn: true, ResourceGroup: "rg", Cluster: "c", Registry: "reg12345"}

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
			if !strings.Contains(err.Error(), "--payload orka --observability=false") || !strings.Contains(err.Error(), "disable-addons") {
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

	banner := lift.Options{Payload: lift.PayloadOrka, ResourceGroup: "rg", Registry: "kaimahidemo", Cluster: "kaimahi-demo",
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
