package scaffold

import "fmt"

// Placement decides where a BYO agent's pod runs.
//
// The honest scope of this file, stated once: node PLACEMENT is not the same
// as pod ISOLATION, and on Kubernetes they are selected differently.
//
//   - Placement — `nodeSelector` and `tolerations` — chooses a node. kagent's
//     Agent CRD exposes both, so kmx can set them.
//   - Isolation — `runtimeClassName` — chooses the sandbox a pod runs in
//     (Kata micro-VM, gVisor, runwasi/WASM). kagent's CRD does NOT expose it,
//     for either `declarative` or `byo`. Verified against the live CRD.
//
// That distinction decides which profiles can exist here at all.
//
// AKS Kata (`kata-mshv-vm-isolation`) is a RuntimeClass. Landing a pod on a
// Kata-capable node WITHOUT `runtimeClassName` runs an ordinary container on
// that node — it looks isolated and is not. So there is deliberately no
// `kata` profile: shipping one would be the overclaiming this package exists
// to avoid. Reaching Kata needs the field upstream, which is a contribution
// to kagent, not a flag here.
//
// Virtual nodes are different, and that is why one profile ships. Pods
// scheduled to an ACI virtual node run as Hyper-V isolated container groups,
// and they are selected by nodeSelector and toleration alone — no
// RuntimeClass. The mechanism kagent exposes is sufficient for exactly this
// case.
type Placement struct {
	// Name is the profile as the operator typed it.
	Name string
	// NodeSelector and Tolerations are what the profile expands to.
	NodeSelector map[string]string
	Tolerations  []Toleration
	// Note is printed after generation. Isolation claims are easy to make
	// and hard to verify, so each profile says what it does and does not buy.
	Note string
}

// Toleration is the subset of the Kubernetes toleration the profiles need.
type Toleration struct {
	Key      string
	Operator string
	Effect   string
}

// placements is the whole set. Small on purpose: a profile earns its place by
// working through the mechanism kagent actually exposes.
var placements = map[string]Placement{
	"virtual-node": {
		Name: "virtual-node",
		// The selector AKS documents for its ACI virtual node. Kept as data
		// rather than built from strings so it is greppable and reviewable.
		NodeSelector: map[string]string{
			"kubernetes.io/os":   "linux",
			"type":               "virtual-kubelet",
			"kubernetes.io/role": "agent",
		},
		Tolerations: []Toleration{
			{Key: "virtual-kubelet.io/provider", Operator: "Exists"},
			{Key: "azure.com/aci", Effect: "NoSchedule"},
		},
		Note: "virtual-node: the pod runs as a Hyper-V isolated container group on an " +
			"ACI virtual node. It bounds the process, not what the agent may spend or " +
			"call — the plane still does that.",
	},
}

// ParsePlacement resolves a profile name.
//
// "none" and "" mean today's behaviour: no placement fields at all. An
// unknown name is refused with the reason and the list, because a
// mis-typed profile that silently degraded to normal scheduling would look
// exactly like success.
func ParsePlacement(name string) (*Placement, error) {
	switch name {
	case "", "none":
		return nil, nil
	case "kata", "kata-mshv-vm-isolation":
		return nil, fmt.Errorf(
			"isolation %q cannot be set this way: Kata is a RuntimeClass, and kagent's "+
				"Agent CRD does not expose runtimeClassName (checked on declarative and byo). "+
				"Scheduling onto a Kata-capable node without it runs an ordinary container "+
				"there — isolated in appearance only. Known profiles: %s", name, known())
	}
	p, ok := placements[name]
	if !ok {
		return nil, fmt.Errorf("unknown isolation profile %q. Known profiles: %s", name, known())
	}
	return &p, nil
}

func known() string { return "virtual-node, none" }

// GovernanceEnv is the seam configuration a BYO image needs to stay governed.
//
// A declarative agent gets this by REFERENCE: `modelConfig: governed-ollama`
// points at the proxy, and the tools block points at the gateway. spec.byo
// has neither field, so the same seams have to travel as environment.
//
// The variable names follow the OpenAI convention because that is what the
// proxy speaks and what most agent images already read. kmx cannot verify an
// image honours them — that is why the caller prints what was injected and
// says the ledger is the proof.
func GovernanceEnv(governed bool) []EnvVar {
	if !governed {
		return nil
	}
	return []EnvVar{
		{Name: "OPENAI_BASE_URL", Value: ProxyBaseURL},
		{Name: "OPENAI_API_KEY", Value: "api-key", SecretRef: GovernedSecret},
		{Name: "KAIMAHI_MCP_URL", Value: GatewayURL(governedUpstream)},
	}
}

// The seams, as the committed manifests define them.
const (
	ProxyBaseURL = "http://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/ollama/v1"
	// The gateway URL is DERIVED, not spelled out: GatewayURL is the one
	// shape a governed tool seam may have, and a second literal here would
	// be a copy that stops agreeing with it the first time it moves.
	governedUpstream = "kagent-tools"
	GovernedSecret   = "kaimahi-governed-token"
)
