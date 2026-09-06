package guard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// The same three contexts scripts/kube-guard-test.sh builds, chosen to
// separate NAME from ADDRESS:
//
//	kind-real    kind-named  + loopback   -> local, no confirmation
//	kind-sneaky  kind-named  + remote     -> remote (a name proves nothing)
//	aks-remote   other name  + remote     -> remote
//
// A fourth name, kind-not-created-yet, is tested precisely by being ABSENT
// from this fixture — that is `kmx up` on an empty machine.
//
// The fixture is the JSON shape of `kubectl config view -o json`, so these
// tests exercise the same decode path the binary uses.
const fixture = `{
  "clusters": [
    {"name": "c-local",  "cluster": {"server": "https://127.0.0.1:36453"}},
    {"name": "c-remote", "cluster": {"server": "https://example.invalid:443"}}
  ],
  "contexts": [
    {"name": "kind-real",   "context": {"cluster": "c-local"}},
    {"name": "kind-sneaky", "context": {"cluster": "c-remote"}},
    {"name": "aks-remote",  "context": {"cluster": "c-remote"}}
  ],
  "current-context": "kind-real"
}`

func load(t *testing.T) *Kubeconfig {
	t.Helper()
	cfg, err := ParseKubeconfig([]byte(fixture))
	if err != nil {
		t.Fatalf("fixture did not parse: %v", err)
	}
	return cfg
}

// Every case from scripts/kube-guard-test.sh, with the same verdicts. The
// shell tests run the guard with no stdin, because "no TTY" is the CI and
// scripted case and the guard must never hang waiting for an answer nobody
// can give; a nil *os.File here is the same condition.
func TestGuardDecisions(t *testing.T) {
	cases := []struct {
		name    string
		context string
		confirm string
		wantErr string // "" means the guard must proceed
	}{
		{"local kind proceeds", "kind-real", "", ""},
		{"absent kind- context is 'about to be created'", "kind-not-created-yet", "", ""},
		{"absent non-kind context is a typo", "prod-oops", "", "not in the kubeconfig"},
		{"kind-named remote still needs confirmation", "kind-sneaky", "", "no TTY to ask"},
		{"remote without confirmation is refused", "aks-remote", "", "no TTY to ask"},
		{"confirmation for another context is refused", "aks-remote", "kind-real", "does not name this context"},
		{"exact confirmation admits a remote context", "aks-remote", "aks-remote", ""},
		{"empty context is refused", "", "", "refusing to act on an unnamed cluster"},
	}
	cfg := load(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Check(cfg, Request{
				Action:  tc.name,
				Context: tc.context,
				Source:  config.SourceKubeCtx,
				Confirm: tc.confirm,
				Command: "kmx up",
			}, &out, nil)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want proceed, got refusal: %v\n%s", err, out.String())
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want refusal containing %q, got proceed\n%s", tc.wantErr, out.String())
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("refusal %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// The banner is the other half of the contract: even when it proceeds
// without asking, the guard must say where the action is going. namespace(s)
// is part of that contract, not decoration.
func TestBannerNamesWhereTheActionLands(t *testing.T) {
	var out bytes.Buffer
	if err := Check(load(t), Request{
		Source:  config.SourceKubeCtx,
		Action:  "banner check",
		Context: "kind-real",
		Command: "kmx up",
	}, &out, nil); err != nil {
		t.Fatalf("local kind must proceed: %v", err)
	}
	for _, needle := range []string{"kind-real", "127.0.0.1", "about to:", "namespace(s):", "posture:"} {
		if !strings.Contains(out.String(), needle) {
			t.Errorf("banner is missing %q:\n%s", needle, out.String())
		}
	}
}

// A refusal must still have printed the banner first — the operator learns
// where the command was aimed even when nothing happens.
func TestRefusalStillPrintsTheBanner(t *testing.T) {
	var out bytes.Buffer
	if err := Check(load(t), Request{
		Source:  config.SourceKubeCtx,
		Action:  "delete the cluster",
		Context: "aks-remote",
		Command: "kmx down",
	}, &out, nil); err == nil {
		t.Fatal("a remote context with no confirmation must be refused")
	}
	if !strings.Contains(out.String(), "REMOTE / non-kind") {
		t.Errorf("banner did not name the remote posture:\n%s", out.String())
	}
}

// The hint an operator is given has to be the command they typed, not a
// generic one: `KAIMAHI_CONFIRM=<ctx> make <target>` was right for the
// Makefile and is wrong here.
func TestRefusalHintNamesTheCommand(t *testing.T) {
	var out bytes.Buffer
	err := Check(load(t), Request{
		Source:  config.SourceKubeCtx,
		Action:  "create an agent",
		Context: "aks-remote",
		Command: "kmx agent create billing",
	}, &out, nil)
	if err == nil {
		t.Fatal("want refusal")
	}
	if !strings.Contains(err.Error(), "KAIMAHI_CONFIRM=aks-remote kmx agent create billing") {
		t.Errorf("hint does not name the command: %v", err)
	}
}

// A context name is cosmetic; the address is the substantive check. Both
// directions matter, so assert the classification itself as well as the
// decision it produces.
func TestClassifyChecksNameAndAddress(t *testing.T) {
	cfg := load(t)
	for _, tc := range []struct {
		context   string
		wantLocal bool
		wantLabel string
	}{
		{"kind-real", true, "local kind"},
		{"kind-sneaky", false, "REMOTE / non-kind"},
		{"aks-remote", false, "REMOTE / non-kind"},
		{"kind-not-created-yet", true, "local kind (context not created yet)"},
	} {
		p, err := Classify(cfg, tc.context)
		if err != nil {
			t.Fatalf("%s: %v", tc.context, err)
		}
		if p.Local != tc.wantLocal || p.Label != tc.wantLabel {
			t.Errorf("%s: got local=%v label=%q, want local=%v label=%q",
				tc.context, p.Local, p.Label, tc.wantLocal, tc.wantLabel)
		}
	}
}

func TestUnreadableKubeconfigRefuses(t *testing.T) {
	if _, err := ParseKubeconfig([]byte("not json")); err == nil {
		t.Fatal("an unparseable kubeconfig must refuse, not default to something")
	}
}

// The finding this closes: with nothing set anywhere, kmx acted on a name it
// made up and said nothing about having made it up. A banner that names a
// cluster nobody picked is not a safety net — it is the wrong answer,
// printed confidently.
func TestAContextNobodyChoseIsRefusedWhenThereAreClustersToConfuseItWith(t *testing.T) {
	var out bytes.Buffer
	err := Check(load(t), Request{
		Action:  "install kagent",
		Context: "kind-kaimahi-p1",
		Source:  config.SourceDefault,
		Command: "kmx up",
	}, &out, nil)
	if err == nil {
		t.Fatal("acted on a cluster nobody chose")
	}
	for _, want := range []string{"nothing chose a cluster", "kmx ctx <name>", "kind-kaimahi-p1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q:\n%s", want, err)
		}
	}
	// The most likely intended target is named, and named as something kmx
	// did NOT follow — leaving it off a screen whose job is naming the
	// target would be the same omission in a different place.
	if !strings.Contains(err.Error(), "kind-real") || !strings.Contains(err.Error(), "does not follow it") {
		t.Errorf("refusal does not name the current context it declined to follow:\n%s", err)
	}
}

// The other half of the rule, and the one that keeps one-command bring-up
// working: on a machine with no clusters at all the made-up kind name is the
// only thing it could mean, so it stands.
func TestTheDefaultStandsOnAMachineWithNoClusters(t *testing.T) {
	empty, err := ParseKubeconfig([]byte(`{"clusters":[],"contexts":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Check(empty, Request{
		Action:  "create the cluster",
		Context: "kind-kaimahi-p1",
		Source:  config.SourceDefault,
		Command: "kmx up",
	}, &out, nil); err != nil {
		t.Fatalf("refused on an empty machine, which breaks bring-up: %v", err)
	}
}

// A context somebody actually named is not affected, whichever way they
// named it. This is what keeps CI and `KIND_CLUSTER=mine kmx up` unchanged.
func TestAChosenContextIsNeverRefusedForBeingUnchosen(t *testing.T) {
	for _, source := range []string{
		config.SourceFlag, config.SourceKubeCtx, config.SourceSelected, config.SourceKindCluster,
	} {
		var out bytes.Buffer
		if err := Check(load(t), Request{
			Action:  "install kagent",
			Context: "kind-real",
			Source:  source,
			Command: "kmx up",
		}, &out, nil); err != nil {
			t.Errorf("source %q was refused: %v", source, err)
		}
		if !strings.Contains(out.String(), "chosen by: "+source) {
			t.Errorf("banner does not say who chose the cluster for source %q:\n%s", source, out.String())
		}
	}
}

// The other case that is not the failure: the made-up name is exactly the
// context the operator is already pointed at. Nobody is being surprised —
// kmx would act on the same cluster their own kubectl would — and refusing
// there is friction rather than a safety net. It caught a real CI step that
// names no cluster on a machine whose current context is the one it means.
//
// This is not following current-context. kmx still acts only on the name it
// resolved; where that name and the current context DIFFER, the current one
// is never substituted, which the test above holds.
func TestTheDefaultStandsWhenItIsAlreadyTheContextYouArePointedAt(t *testing.T) {
	var out bytes.Buffer
	if err := Check(load(t), Request{
		Action:  "install kagent",
		Context: "kind-real", // the fixture's current-context
		Source:  config.SourceDefault,
		Command: "kmx up",
	}, &out, nil); err != nil {
		t.Fatalf("refused a default that matches the current context: %v", err)
	}
	if !strings.Contains(out.String(), "chosen by: "+config.SourceDefault) {
		t.Errorf("the banner stopped saying nothing chose this cluster:\n%s", out.String())
	}
}
