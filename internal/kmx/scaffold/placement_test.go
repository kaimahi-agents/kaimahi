package scaffold

import (
	"strings"
	"testing"
)

// Kata is the case this package must refuse. It is a RuntimeClass, and
// kagent's CRD exposes no runtimeClassName — so scheduling onto a
// Kata-capable node buys nothing while looking like isolation.
func TestKataIsRefusedWithTheReason(t *testing.T) {
	for _, name := range []string{"kata", "kata-mshv-vm-isolation"} {
		p, err := ParsePlacement(name)
		if err == nil {
			t.Fatalf("%q must be refused, got %+v", name, p)
		}
		if !strings.Contains(err.Error(), "RuntimeClass") ||
			!strings.Contains(err.Error(), "runtimeClassName") {
			t.Errorf("the refusal must say WHY, got: %v", err)
		}
	}
}

func TestUnknownProfileIsRefusedAndLists(t *testing.T) {
	_, err := ParsePlacement("gvisor")
	if err == nil || !strings.Contains(err.Error(), "virtual-node") {
		t.Errorf("an unknown profile must name the known ones, got: %v", err)
	}
}

func TestNoneMeansNoPlacementFields(t *testing.T) {
	for _, name := range []string{"", "none"} {
		p, err := ParsePlacement(name)
		if err != nil || p != nil {
			t.Errorf("%q must produce no placement, got %+v %v", name, p, err)
		}
	}
}

// The one profile that ships works through nodeSelector and tolerations
// alone, which is exactly why it can exist.
func TestVirtualNodeUsesOnlyWhatTheCRDExposes(t *testing.T) {
	p, err := ParsePlacement("virtual-node")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.NodeSelector) == 0 || len(p.Tolerations) == 0 {
		t.Error("virtual-node must set both a selector and tolerations")
	}
	if p.Note == "" || !strings.Contains(p.Note, "not what the agent may spend") {
		t.Error("the profile must say what it does NOT protect")
	}
}

func TestPlacementNeedsAnImage(t *testing.T) {
	p, _ := ParsePlacement("virtual-node")
	_, err := Generate(Spec{
		Name: "a1", ModelConfig: "ollama", Instructions: "x", Placement: p,
	})
	if err == nil || !strings.Contains(err.Error(), "--image") {
		t.Errorf("placement on a declarative agent must be refused, got: %v", err)
	}
}

func TestBYOSkipsTheModelConfigRequirement(t *testing.T) {
	// spec.byo has no modelConfig field, so requiring one would make the
	// flag unusable.
	doc, err := Generate(Spec{Name: "a1", Image: "ghcr.io/x/a:1", Instructions: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "type: BYO") || strings.Contains(doc, "modelConfig") {
		t.Errorf("a BYO document must not carry a modelConfig:\n%s", doc)
	}
}

func TestGovernanceEnvIsInjectedForGovernedBYO(t *testing.T) {
	doc, err := Generate(Spec{
		Name: "a1", Image: "ghcr.io/x/a:1", Instructions: "x",
		Governance: GovernanceEnv(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"OPENAI_BASE_URL", ProxyBaseURL, "KAIMAHI_MCP_URL", GatewayURL(governedUpstream),
		"secretKeyRef", GovernedSecret,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the governed seams must travel as env; missing %q:\n%s", want, doc)
		}
	}
	// The credential is a reference, never a literal.
	if strings.Contains(doc, "kmh_") {
		t.Error("a token value must never be written into the manifest")
	}
}

// A declarative agent gets the plane's authority mounted by kagent, which
// reads `spec.tls` and edits the Deployment it generates. A BYO pod gets no
// such treatment — the controller builds no model config for it and mounts
// nothing — so the document has to carry the volume itself, or an image that
// honours the https seam URL cannot verify what answers.
func TestGovernedBYOCarriesTheAuthorityItIsToldToVerifyAgainst(t *testing.T) {
	doc, err := Generate(Spec{
		Name: "a1", Image: "ghcr.io/x/a:1", Instructions: "x",
		Governance: GovernanceEnv(true),
		Identity:   Identity{UID: 1001},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		CAFileEnv, byoCAMountPath + "/" + PlaneCAKey,
		"secretName: " + PlaneCASecret,
		"mountPath: " + byoCAMountPath,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("a governed BYO pod must carry the authority; missing %q:\n%s", want, doc)
		}
	}
	// One volumes key and one volumeMounts key. Two of either is a
	// duplicate YAML mapping key: kubectl keeps the last and discards the
	// first, so the pod would come up missing whichever block lost.
	if n := strings.Count(doc, "      volumes:\n"); n != 1 {
		t.Errorf("expected exactly one volumes key, found %d:\n%s", n, doc)
	}
	if n := strings.Count(doc, "      volumeMounts:\n"); n != 1 {
		t.Errorf("expected exactly one volumeMounts key, found %d:\n%s", n, doc)
	}
	// /tmp exists so the image can write under a read-only root
	// filesystem. Mounting it read-only would take away the thing it was
	// added to provide, and the pod would fail somewhere far from here.
	if strings.Contains(doc, "mountPath: /tmp\n          readOnly: true") {
		t.Errorf("/tmp was mounted read-only:\n%s", doc)
	}
}

// An ungoverned BYO pod is told to trust nothing extra, so it must not be
// handed the authority either — a mount of a Secret that may not exist would
// keep the pod out of Running for a reason it has no use for.
func TestUngovernedBYOCarriesNoAuthority(t *testing.T) {
	doc, err := Generate(Spec{
		Name: "a1", Image: "ghcr.io/x/a:1", Instructions: "x",
		Identity: Identity{UID: 1001},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, PlaneCASecret) {
		t.Errorf("an ungoverned BYO pod was given the plane's authority:\n%s", doc)
	}
	if !strings.Contains(doc, "mountPath: /tmp") {
		t.Errorf("the hardened pod lost its writable /tmp:\n%s", doc)
	}
}

func TestUngovernedBYOInjectsNothing(t *testing.T) {
	if env := GovernanceEnv(false); env != nil {
		t.Errorf("an ungoverned agent must not be given seams it does not have: %+v", env)
	}
}

// The declarative path must be untouched by any of this.
func TestDeclarativeOutputIsUnchanged(t *testing.T) {
	doc, err := Generate(Spec{Name: "a1", ModelConfig: "ollama", Instructions: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "type: Declarative") {
		t.Error("no flags must still produce a declarative agent")
	}
	for _, unwanted := range []string{"type: BYO", "nodeSelector", "tolerations", "image:"} {
		if strings.Contains(doc, unwanted) {
			t.Errorf("declarative output gained %q:\n%s", unwanted, doc)
		}
	}
}
