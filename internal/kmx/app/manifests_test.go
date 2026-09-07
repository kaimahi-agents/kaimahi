package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
)

// kmx applies manifests from inside the binary, because it is installed with
// `go install` and run outside a clone. Two things can silently break that: a
// mistyped embed pattern (the file is simply absent at run time, on the
// operator's machine, half way through `kmx up`), and an edit to k8s/ that
// nobody rebuilt against. Assert both here, where it costs nothing.
func TestEmbeddedManifestsAreTheOnesInTheTree(t *testing.T) {
	for _, name := range []string{"ollama.yaml", "kagent-values.yaml", "hello-world.yaml", "tools-agent.yaml"} {
		embedded, err := manifest(name)
		if err != nil {
			t.Errorf("k8s/%s is not embedded in the binary: %v", name, err)
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join("..", "..", "..", "k8s", name))
		if err != nil {
			t.Fatalf("k8s/%s: %v", name, err)
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("k8s/%s differs from the embedded copy", name)
		}
	}
}

// Milestone 2 puts the plane's manifests and the two governed presets in the
// binary, for the same reason as the runtime ones: `kmx plane` and
// `kmx govern` run outside a clone, with no k8s/ on disk to point kubectl at.
func TestThePlanesManifestsTravelInTheBinary(t *testing.T) {
	for _, name := range []string{
		"plane/namespace.yaml", "plane/postgres.yaml", "plane/proxy.yaml",
		"plane/upstreams.yaml", "plane/network-policy.yaml",
		"models/governed-ollama.yaml", "models/governed-copilot.yaml",
	} {
		embedded, err := manifest(name)
		if err != nil {
			t.Errorf("k8s/%s is not embedded in the binary: %v", name, err)
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join("..", "..", "..", "k8s", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("k8s/%s: %v", name, err)
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("k8s/%s differs from the embedded copy", name)
		}
	}
}

// Milestone 3 adds two things to the binary, and each is there because a
// kmx command applies it: the governed RemoteMCPServer `kmx tools govern`
// puts the tools agent behind, and EVERY model preset, because `kmx use` is
// `make use` and `make use PRESET=anthropic` has always been a documented
// flow. A preset NAMES a Secret; it never carries a key, so this puts no
// credential anywhere near kmx — minting that Secret is still
// `make model-secret` and the scripts.
func TestMilestoneThreesManifestsTravelInTheBinary(t *testing.T) {
	names := []string{"kaimahi-tools.yaml"}
	presets, err := os.ReadDir(filepath.Join("..", "..", "..", "k8s", "models"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range presets {
		names = append(names, "models/"+e.Name())
	}
	for _, name := range names {
		embedded, err := manifest(name)
		if err != nil {
			t.Errorf("k8s/%s is not embedded in the binary: %v", name, err)
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join("..", "..", "..", "k8s", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("k8s/%s: %v", name, err)
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("k8s/%s differs from the embedded copy", name)
		}
	}
}

// Every preset in the tree is a preset `kmx use` will name, and nothing
// else is. This is the list an operator sees when they mistype one, so a
// preset added to k8s/models/ that never reached the binary would be
// advertised and then fail to apply.
func TestUseOffersExactlyTheEmbeddedPresets(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "k8s", "models"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range entries {
		want = append(want, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	// Sorted on the NAME, not the filename: ReadDir orders
	// "openai-compatible.yaml" before "openai.yaml" ('-' sorts below '.'),
	// and what an operator is offered is the name.
	sort.Strings(want)
	got := presetNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("kmx use offers %v, k8s/models/ holds %v", got, want)
	}
	for _, preset := range got {
		name, err := presetManifest(preset)
		if err != nil {
			t.Errorf("preset %q is offered but does not resolve: %v", preset, err)
			continue
		}
		if _, err := manifest(name); err != nil {
			t.Errorf("preset %q resolves to %s, which is not embedded: %v", preset, name, err)
		}
	}
	// And a name that is not a preset is refused rather than turned into a
	// path: the preset name reaches both the embedded filesystem and the
	// object the agent is patched onto.
	for _, bad := range []string{"", "../plane/proxy", "nope", "governed-ollama.yaml"} {
		if _, err := presetManifest(bad); err == nil {
			t.Errorf("presetManifest(%q) was accepted", bad)
		}
	}
}

// What kmx carries is still a decision, not a directory listing. The Slack,
// GitHub, inbound and accounts-payable families must NOT ride along: their
// targets are the Makefile's, and a manifest in the binary that no kmx
// command applies is a claim kmx cannot honour.
//
// `egress-hosted.yaml` left this list when a kmx command started applying it
// — the credential capture, whose whole point is that an operator with no
// checkout can hand the plane a token for an upstream on the internet. Which
// is the rule working, not an exception to it: the test below is what keeps
// the list honest in the other direction. `egress-copilot.yaml` left it for
// the same reason: `kmx lift` applies it, so claiming nothing did was simply
// false — and it stayed false unnoticed because kmx has TWO embedded
// filesystems and this only ever looked in one of them.
func TestTheConnectorFamiliesAreNotEmbedded(t *testing.T) {
	for _, name := range []string{
		"kaimahi-slack.yaml", "slack-agent.yaml", "slack-mcp.yaml",
		"kaimahi-github.yaml", "github-agent.yaml",
		"inbound-edge.yaml",
		"ap-agent.yaml", "kaimahi-erp.yaml", "erp-mcp.yaml",
		"release-agent.yaml", "kaimahi-release-github.yaml", "kaimahi-release-ado.yaml",
	} {
		// The premise first: this asserts an EXCLUSION, and an exclusion
		// passes for free once the thing it excludes stops existing. If a
		// manifest moves, this test must fail and be rewritten against
		// wherever it went — not quietly keep passing.
		if _, err := os.Stat(filepath.Join("..", "..", "..", "k8s", filepath.FromSlash(name))); err != nil {
			t.Fatalf("k8s/%s is gone, so this exclusion no longer proves anything: %v", name, err)
		}
		if _, err := manifest(name); err == nil {
			t.Errorf("k8s/%s is embedded in kmx's manifest filesystem, but no kmx command applies it", name)
		}
		// The managed path carries its own embedded filesystem. Checking only
		// the first one is how a manifest can be in the binary while a test
		// says it is not.
		if _, err := kaimahi.Managed.ReadFile("k8s/" + name); err == nil {
			t.Errorf("k8s/%s is embedded in kmx's managed filesystem, but no kmx command applies it", name)
		}
	}
}

// And the inverse of that list: a manifest a kmx command DOES apply has to be
// in the binary, or the command works in a clone and fails everywhere else —
// which is the failure mode that is invisible to everyone who develops here.
func TestTheHostedEgressAllowanceTravelsInTheBinary(t *testing.T) {
	if _, err := manifest("egress-hosted.yaml"); err != nil {
		t.Fatalf("kmx applies k8s/egress-hosted.yaml when it captures a credential for a "+
			"hosted upstream, but it is not embedded: %v", err)
	}
}
