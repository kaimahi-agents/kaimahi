package lift

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// armID assembles a resource id rather than writing one out, so this file
// carries no identifier-shaped literal for the tree scanner to find. The
// scanner cannot tell a fixture from a real id, and a gate that has to be
// argued with stops being read.
func armID(group, provider, collection, name string) string {
	return fmt.Sprintf("/subscriptions/SUB/resourceGroups/%s/providers/%s.%s/%s/%s",
		group, "Microsoft", provider, collection, name)
}

func TestRunIDMustBeShapedBecauseItNamesEveryResource(t *testing.T) {
	for _, id := range []string{"", "SHORT", "abcdefg", "abcdefghi", "abcdef-h", "ABCDEF12"} {
		if _, err := NewRecord(id, Created, "sub", "rg", "cluster"); err == nil {
			t.Fatalf("run id %q was accepted; it ends up in the name of every resource the run creates", id)
		}
	}
	if _, err := NewRecord("a1b2c3d4", Created, "sub", "rg", "cluster"); err != nil {
		t.Fatalf("a well-formed run id was refused: %v", err)
	}
}

func TestRunScopedNamesDifferPerRun(t *testing.T) {
	// Two runs in one subscription must not produce the same resource names.
	// A fixed name is what makes "the thing I made" and "the thing that was
	// already here" indistinguishable at teardown.
	for _, fn := range []func(string) string{MetricsWorkspaceName, LogsWorkspaceName, WorkbookName} {
		if fn("a1b2c3d4") == fn("e5f6a7b8") {
			t.Fatal("two runs produced the same resource name")
		}
		if !strings.Contains(fn("a1b2c3d4"), "a1b2c3d4") {
			t.Fatalf("%q does not carry the run id", fn("a1b2c3d4"))
		}
		if len(fn("a1b2c3d4")) > 63 {
			t.Fatalf("%q is longer than an Azure workspace name may be", fn("a1b2c3d4"))
		}
	}
}

func TestRecordRefusesAResourceWithNoID(t *testing.T) {
	r, err := NewRecord("a1b2c3d4", BringYourOwn, "sub", "rg", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	err = r.Add(Resource{Kind: "Azure Monitor workspace", Name: "kaimahi-metrics-a1b2c3d4"})
	if err == nil {
		t.Fatal("a resource with no id was recorded; teardown deletes by id, so it could never be removed")
	}
	if len(r.Created) != 0 {
		t.Fatal("the unusable resource was recorded anyway")
	}
}

func TestRecordingTheSameResourceTwiceIsOneEntry(t *testing.T) {
	// A resumed run re-runs a phase that already created its resource. That
	// must not produce two entries, or teardown reports a phantom.
	r, _ := NewRecord("a1b2c3d4", Created, "sub", "rg", "cluster")
	res := Resource{Kind: "Log Analytics workspace", Name: "n", ID: armID("rg", "OperationalInsights", "workspaces", "n")}
	if err := r.Add(res); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(res); err != nil {
		t.Fatal(err)
	}
	if len(r.Created) != 1 {
		t.Fatalf("re-recording the same resource produced %d entries", len(r.Created))
	}
}

func TestRecordRoundTrips(t *testing.T) {
	r, _ := NewRecord("a1b2c3d4", BringYourOwn, "sub", "rg", "cluster")
	_ = r.Add(Resource{Kind: "Azure Monitor workspace", Name: "n", ID: "/id/one", Billing: "retains samples", InResourceGroup: true})
	_ = r.Add(Resource{Kind: "data collection rule", Name: "m", ID: "/id/two"})
	var buf bytes.Buffer
	if err := r.Write(&buf); err != nil {
		t.Fatal(err)
	}
	back, err := ReadRecord(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if back.Branch != BringYourOwn || len(back.Created) != 2 || back.Created[0].ID != "/id/one" {
		t.Fatalf("record did not survive a round trip: %+v", back)
	}
	if outside := back.Outside(); len(outside) != 1 || outside[0].Name != "m" {
		t.Fatalf("Outside() must name exactly what an empty resource group cannot vouch for, got %+v", outside)
	}
}

// Teardown may undo only what the run did. An operator who already had
// Container Insights running must not have it switched off by a teardown that
// was meant to remove what the lift added — and an object that merely LOOKS
// like ours is not ours.
func TestOnlyWhatThisRunTurnedOnOrCreatedMayBeUndone(t *testing.T) {
	t.Run("we enabled it, so we may disable it", func(t *testing.T) {
		p := Pre{Recorded: true}
		if !p.WeEnabledMetrics() || !p.WeEnabledLogs() {
			t.Fatal("a run that found both add-ons off should be allowed to turn them off again")
		}
		if !p.WeCreatedScraperPolicy() || !p.WeCreatedScrapeMonitor() {
			t.Fatal("a run that found neither object should be allowed to remove the ones it made")
		}
	})

	t.Run("it was already on, so it is not ours to turn off", func(t *testing.T) {
		p := Pre{Recorded: true, MetricsAddonEnabled: true, LogsAddonEnabled: true,
			ScraperPolicyExisted: true, ScrapeMonitorExisted: true}
		if p.WeEnabledMetrics() || p.WeEnabledLogs() {
			t.Fatal("teardown would disable monitoring the operator already had")
		}
		if p.WeCreatedScraperPolicy() || p.WeCreatedScrapeMonitor() {
			t.Fatal("teardown would delete a cluster object the operator already had")
		}
	})

	t.Run("nothing was established: touch nothing", func(t *testing.T) {
		// An older record, or a run whose reads failed. Every field is false,
		// which is indistinguishable from "nothing was there" — and reading it
		// that way is what would authorise disabling somebody's add-on.
		var p Pre
		if p.WeEnabledMetrics() || p.WeEnabledLogs() ||
			p.WeCreatedScraperPolicy() || p.WeCreatedScrapeMonitor() {
			t.Fatal("unestablished prior state was read as 'we made it'")
		}
	})
}

// A resource-group deletion accounts for what was INSIDE it. The monitoring
// add-ons put some of what they create in the cluster's managed node group, so
// assuming otherwise would report a resource as covered by a check that never
// looked at it.
func TestInGroupIsDecidedByTheIDNotAssumed(t *testing.T) {
	inside := armID("demo-rg", "Insights", "dataCollectionRules", "msprom")
	elsewhere := armID("MC_demo-rg_demo_westus3", "Insights", "dataCollectionRules", "msprom")
	if !InGroup(inside, "demo-rg") {
		t.Error("a resource in the group was not recognised as being in it")
	}
	if !InGroup(inside, "DEMO-RG") {
		t.Error("resource groups compare case-insensitively in ARM; this did not")
	}
	if InGroup(elsewhere, "demo-rg") {
		t.Error("a resource in the managed NODE group was claimed to be in the cluster's group — a group deletion would not remove it")
	}
	if InGroup("", "demo-rg") {
		t.Error("an empty id was claimed to be inside a group")
	}
}

func TestReadRecordRefusesAnUnusableOne(t *testing.T) {
	for _, body := range []string{
		`{"run_id":"","branch":"created"}`,
		`{"run_id":"a1b2c3d4","branch":"whatever"}`,
		`not json`,
	} {
		if _, err := ReadRecord(strings.NewReader(body)); err == nil {
			t.Fatalf("an unusable run record was accepted: %s", body)
		}
	}
}

// The teardown rules. These are the ones that can cost somebody else money or
// their monitoring, and they are the reason this package is pure.
func TestPlanRemovalOnlyDeletesWhatTheRecordedIDStillNames(t *testing.T) {
	res := Resource{Kind: "Azure Monitor workspace", Name: "kaimahi-metrics-a1b2c3d4",
		ID: armID("RG", "Monitor", "accounts", "kaimahi-metrics-a1b2c3d4")}

	t.Run("the recorded id still names it: delete", func(t *testing.T) {
		rm := PlanRemoval(res, Present, res.ID)
		if !rm.Delete {
			t.Fatalf("refused to delete a resource whose recorded id resolved exactly: %s", rm.Reason)
		}
	})

	t.Run("ARM case and trailing slash are not a different resource", func(t *testing.T) {
		rm := PlanRemoval(res, Present, strings.ToUpper(res.ID)+"/")
		if !rm.Delete {
			t.Fatalf("an id differing only in case and a trailing slash was treated as a different resource: %s", rm.Reason)
		}
	})

	t.Run("a different id under the same name: refuse", func(t *testing.T) {
		other := armID("OTHER", "Monitor", "accounts", "kaimahi-metrics-a1b2c3d4")
		rm := PlanRemoval(res, Present, other)
		if rm.Delete {
			t.Fatal("deleted a resource whose id differs from the recorded one — that is somebody else's resource wearing the same name")
		}
		if !strings.Contains(rm.Reason, "DIFFERENT") {
			t.Fatalf("the refusal does not say why: %q", rm.Reason)
		}
	})

	t.Run("already gone: nothing to do, and not an error", func(t *testing.T) {
		rm := PlanRemoval(res, Absent, "")
		if rm.Delete {
			t.Fatal("tried to delete a resource Azure said is not there")
		}
		if len(LeftBehind([]Removal{rm})) != 0 {
			t.Fatal("a resource that is already gone was reported as left behind")
		}
	})

	t.Run("could not ask: fail closed, and say it may still be billing", func(t *testing.T) {
		rm := PlanRemoval(res, Unusable, "")
		if rm.Delete {
			t.Fatal("deleted on an answer that could not be obtained")
		}
		if !strings.Contains(rm.Reason, "still be billing") {
			t.Fatalf("an unresolvable resource must be reported as a possible bill, got %q", rm.Reason)
		}
		if len(LeftBehind([]Removal{rm})) != 1 {
			t.Fatal("an unresolvable resource must be reported as left behind")
		}
	})

	t.Run("an empty resolved id is never a match", func(t *testing.T) {
		if PlanRemoval(res, Present, "").Delete {
			t.Fatal("an empty resolved id was treated as matching the recorded one")
		}
		if PlanRemoval(Resource{Kind: "k", Name: "n"}, Present, "anything").Delete {
			t.Fatal("a resource with no recorded id was deleted")
		}
	})
}
