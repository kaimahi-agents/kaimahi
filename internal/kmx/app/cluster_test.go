package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The Podman recovery #53 added to `make cluster` moved into kmx when that
// recipe became a delegation. Its fail-closed rule is the part that can be
// tested without a Podman machine: kind listing a cluster that Podman has no
// containers for is a disagreement, not an empty start list.
func TestPodmanNodesFailsClosedOnAnEmptyListing(t *testing.T) {
	for _, listing := range []string{"", "\n", "   \n\t\n"} {
		if _, err := podmanNodes(listing, "kaimahi-p1"); err == nil {
			t.Errorf("an empty node listing (%q) must refuse, not start nothing and carry on", listing)
		} else if !strings.Contains(err.Error(), "kaimahi-p1") {
			t.Errorf("the refusal must name the cluster: %v", err)
		}
	}
}

func TestPodmanNodesReturnsEveryNode(t *testing.T) {
	// A multi-node cluster lists one name per line; all of them get started.
	names, err := podmanNodes("p1-control-plane\np1-worker\np1-worker2\n", "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p1-control-plane", "p1-worker", "p1-worker2"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", names, want)
	}
}

// #37 found this and it is the reason it is pinned here: a `kind get
// clusters` that FAILS is not "no clusters". A broken container engine, a
// kind that cannot reach its socket, an RBAC-less machine — every one of
// them would otherwise read as "the cluster is missing" and send kmx into a
// create that fails later, less clearly, and about something else.
//
// The stub kind fails whatever it is asked, so a regression that read the
// error as absence would go on to `kind create cluster` and still return an
// error. Only the REFUSAL distinguishes the two, which is what this asserts.
func TestClusterRefusesRatherThanReadingAFailedListingAsAbsence(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "kind")
	script := "#!/bin/sh\necho 'ERROR: failed to list clusters: permission denied while trying to connect to the Docker daemon socket' >&2\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	var out bytes.Buffer
	a := &App{
		Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx, ContainerEngine: "docker"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}
	err := a.stepCluster()
	if err == nil {
		t.Fatal("a failed `kind get clusters` was not an error at all")
	}
	if !strings.Contains(err.Error(), "refusing to guess") {
		t.Fatalf("the failure was not refused as unknowable — it reads as an absent cluster:\n%v", err)
	}
	if !strings.Contains(err.Error(), "kaimahi-p1") || !strings.Contains(err.Error(), "docker") {
		t.Errorf("the refusal must name the cluster and the engine: %v", err)
	}
	if strings.Contains(out.String(), "create cluster") {
		t.Errorf("kmx went on to create a cluster after it failed to look:\n%s", out.String())
	}
}
