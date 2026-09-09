package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A missing CRD does not say "NotFound" — kubectl says the server has no
// such resource type, which isNotFound deliberately refuses to read as
// absence (notfound_test.go). So the probe asks a positive question about
// an object that IS a core resource, and the three answers stay apart.
func TestTheKagentSeamProbeSeparatesAbsentFromUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		noKagent  string
		installed bool
		wantErr   bool
	}{
		{"installed", "", true, false},
		{"never installed", "1", false, false},
		{"cannot be asked", "boom", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAddFixture(t, warehouseService, "notfound", nil)
			t.Setenv("KMX_TEST_NO_KAGENT", tc.noKagent)
			installed, err := f.app.kagentSeamInstalled()
			if tc.wantErr {
				if err == nil {
					t.Fatal("a cluster that could not be asked must not read as absent")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if installed != tc.installed {
				t.Fatalf("installed=%v, want %v", installed, tc.installed)
			}
		})
	}
}

// The whole finding: onboarding succeeded and reported failure, and
// because it stopped it never restarted the proxy — so the new upstream
// was applied and not loaded, and a successful onboarding read as a
// broken one.
func TestOnboardingCompletesOnAClusterWithNoKagent(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	stdin := filepath.Join(f.dir, "stdin")
	t.Setenv("KMX_TEST_STDIN", stdin)
	t.Setenv("KMX_TEST_NO_KAGENT", "1")

	opt := addOpts(f.dir)
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatalf("onboarding must complete where there is no kagent to reconcile: %v", err)
	}

	// The artifact is the whole onboarding, including the document this
	// cluster cannot accept today.
	doc, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "RemoteMCPServer") {
		t.Fatalf("the written file lost the seam document:\n%s", doc)
	}

	// What was APPLIED is the three the plane needs, and not the fourth.
	applied, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kaimahi-upstreams-extra",
		"kaimahi-upstream-warehouse-egress", "kaimahi-upstream-warehouse-ingress"} {
		if !strings.Contains(string(applied), want) {
			t.Fatalf("the apply is missing %q:\n%s", want, applied)
		}
	}
	if strings.Contains(string(applied), "RemoteMCPServer") {
		t.Fatalf("the kagent CRD was applied to a cluster that has no kagent:\n%s", applied)
	}

	// And the proxy was restarted, which is what makes the entry live.
	args, err := os.ReadFile(f.argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "rollout restart deploy/kaimahi-proxy") {
		t.Fatalf("the proxy was not restarted, so the table was never re-read:\n%s", args)
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "kagent") {
		t.Fatalf("the operator is not told which document was skipped or why:\n%s", notes)
	}
}

// On a cluster that HAS kagent nothing changes: the whole file is applied
// from the path the operator can read, exactly as before.
func TestOnboardingAppliesTheWholeFileWhereKagentIsInstalled(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	stdin := filepath.Join(f.dir, "stdin")
	t.Setenv("KMX_TEST_STDIN", stdin)

	opt := addOpts(f.dir)
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(f.argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "apply -f "+opt.Out) {
		t.Fatalf("the whole file was not applied:\n%s", args)
	}
	if _, err := os.Stat(stdin); err == nil {
		t.Fatal("a cluster with kagent must not take the reduced apply path")
	}
	if !strings.Contains(string(args), "rollout restart deploy/kaimahi-proxy") {
		t.Fatalf("the proxy was not restarted:\n%s", args)
	}
}

// A cluster that could not be asked is not a cluster without kagent.
// Skipping a document because a read failed would silently half-onboard.
func TestOnboardingRefusesWhenTheClusterCannotBeAsked(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	t.Setenv("KMX_TEST_NO_KAGENT", "boom")

	err := f.app.AddUpstream(addOpts(f.dir))
	if err == nil {
		t.Fatal("an unreadable cluster must not be treated as one without kagent")
	}
}
