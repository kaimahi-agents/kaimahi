package main

import (
	"bytes"
	"strings"
	"testing"
)

// The lift acts on a cloud subscription, so its refusals have to arrive
// BEFORE anything is contacted — a missing parameter should cost a second,
// not eight minutes and a half-created resource group. These drive the real
// CLI and pass no Azure credentials of any kind, which is also what makes
// them runnable in CI.
func TestTheLiftRefusesAnUnusableRequestBeforeTouchingAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says []string
	}{
		{
			"nothing at all",
			[]string{"lift"},
			[]string{"--resource-group", "--cluster", "--registry"},
		},
		{
			"a cluster shape flag on somebody else's cluster",
			[]string{"lift", "--byo", "--resource-group", "rg", "--cluster", "c", "--registry", "reg12345", "--node-count", "3"},
			[]string{"--node-count", "never reshapes it"},
		},
		{
			"a policy engine that does not enforce",
			[]string{"lift", "--resource-group", "rg", "--cluster", "c", "--registry", "reg12345", "--network-policy", ""},
			[]string{"present but inert"},
		},
		{
			"a registry name Azure would reject",
			[]string{"lift", "--resource-group", "rg", "--cluster", "c", "--registry", "not-alphanumeric"},
			[]string{"alphanumeric"},
		},
		{
			"a phase that does not exist",
			[]string{"lift", "--resource-group", "rg", "--cluster", "c", "--registry", "reg12345", "--step", "observabilty"},
			[]string{"--step", "boundary"},
		},
		{
			"teardown with no idea which lift",
			[]string{"lift", "down"},
			[]string{"--resource-group", "--cluster"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Never the operator's real state directory: a test that reads
			// or writes run records would be reading somebody's account of
			// live cloud resources.
			t.Setenv("KMX_HOME", t.TempDir())
			var out, errOut bytes.Buffer
			deps, _ := testDependencies(&out, &errOut)
			err := execute(tc.args, deps)
			if err == nil {
				t.Fatalf("accepted %v", tc.args)
			}
			for _, want := range tc.says {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not mention %q:\n%v", want, err)
				}
			}
		})
	}
}

// --network-policy left alone and --network-policy set to "" are different
// requests. The first takes an engine that enforces; the second is the AKS
// default that does not, and it has to reach its own refusal rather than
// being quietly replaced by something that works.
func TestAnExplicitlyEmptyPolicyEngineIsNotTheDefault(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	err := execute([]string{"lift", "--resource-group", "rg", "--cluster", "c",
		"--registry", "reg12345", "--network-policy", ""}, deps)
	if err == nil || !strings.Contains(err.Error(), "is not a policy engine") {
		t.Fatalf("an explicitly empty engine was not refused on its own terms: %v", err)
	}
}

// Teardown's branch is named, never remembered from a flag file or inferred.
// The two branches have opposite rules about deleting a resource group, and
// the record refuses a mismatch rather than reconciling it — but the flag has
// to exist for the operator to state which they mean.
func TestTeardownTakesTheSameBranchFlagAsTheLift(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	for _, path := range [][]string{{"lift"}, {"lift", "down"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		for _, flag := range []string{"byo", "resource-group", "cluster", "registry"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("%v has no --%s", path, flag)
			}
		}
	}
}

// The lift is a sibling of quickstart, not a flag on `up`. `up` builds
// something free that is deleted by removing a container; this bills money
// until it is torn down, and putting both behind one word would hide that.
func TestTheLocalBringUpHasNoCloudFlag(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	up, _, err := root.Find([]string{"up"})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"resource-group", "registry", "byo", "cluster"} {
		if up.Flags().Lookup(flag) != nil {
			t.Errorf("`kmx up` grew a --%s; the managed path is its own command for a reason", flag)
		}
	}
}
