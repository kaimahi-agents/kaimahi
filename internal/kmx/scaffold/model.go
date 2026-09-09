package scaffold

// Scaffolding the MODEL seam for an endpoint this repo did not deploy.
//
// `kmx tools add` solved this problem once, for tool servers, and this
// is deliberately the same shape rather than a second one: an overlay
// fragment merged over the committed table, a NetworkPolicy pair pinned
// to the live Service's own selector and container port, one reviewable
// YAML file, applied behind the guard. Where it diverges from the tool
// seam it is because the seams genuinely differ, and each divergence is
// named at the place it happens:
//
//  1. THREE documents, not four. The tool seam's fourth is a
//     RemoteMCPServer — a kagent CRD. A model seam's equivalent would be
//     a ModelConfig, also a kagent CRD, and a foreign runtime has
//     neither. The agent-side wiring for a model is one base URL in
//     whatever the adopter's framework reads, so kmx PRINTS it instead
//     of emitting a custom resource that half its users cannot apply.
//
//  2. A protocol, and a classification. A tool upstream is one URL. A
//     model upstream is a base URL, exactly one forwarded path, the wire
//     protocol that path speaks (where the meter reads token counts) and
//     an explicit free/metered classification. The first three come out
//     of one --url; the classification is the operator's and is never
//     inferred, because a $0 by inference is a budget that cannot be
//     exhausted.
//
//  3. No policy_fields, and therefore no allowlist. This is the
//     uncomfortable one and it is stated rather than glossed: the model
//     seam has no per-credential allowlist at all. Every credential the
//     plane has issued can call every upstream in the table, so adding
//     one WIDENS what existing credentials reach. Budgets still bound
//     them, and an overlay upstream is in-cluster and keyless — but the
//     command says this out loud before it applies anything.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ProxyHost is the in-cluster address of the metering model proxy —
// the value an adopter's OPENAI_BASE_URL becomes, plus the upstream's
// name. https for the same reason the gateway is: the prompt and the
// completion cross this wire and are recorded in no other place (the
// ledger holds counts and a cost, never content).
const ProxyHost = "https://kaimahi-proxy.kaimahi.svc.cluster.local:8080"

// SeamBaseURL is what a client is pointed at. The client appends the
// protocol's own path — `v1/responses` or `v1/chat/completions` — which
// is why this stops at the upstream name and carries no `/v1`.
func SeamBaseURL(upstream string) string { return ProxyHost + "/upstream/" + upstream }

// ModelSpec is everything the three documents are rendered from.
type ModelSpec struct {
	// Name is the upstreams key, and a URL path segment.
	Name string
	// BaseURL and Path are the entry: the origin the proxy dials, and
	// the ONE forwarded remainder it will accept on it.
	BaseURL string
	Path    string
	// Protocol is the wire shape, resolved from Path where Path names
	// one and supplied by the operator where it does not. It is always
	// written into the fragment explicitly, so the file a human reviews
	// states it rather than leaving it to be worked out again.
	Protocol string
	// Classification is "free" or "metered", and is the operator's.
	Classification string
	// Service, ServiceNamespace, PodPort and PodLabels locate the
	// endpoint for the NetworkPolicy pair. PodPort is the CONTAINER
	// port, because NetworkPolicy is evaluated on the post-NAT address.
	Service          string
	ServiceNamespace string
	PodPort          int
	PodLabels        map[string]string
	// ServerDNS / ServerEgressKeep are the same postures the tool seam
	// names, and mean the same things.
	ServerDNS        bool
	ServerEgressKeep bool
	// OverlayVersion and Fragments: exactly as UpstreamSpec's, and for
	// the same reason — the emitted ConfigMap is WHOLE, and carries the
	// version it was read at as an apply precondition, so a file
	// scaffolded on Monday cannot prune a fragment added on Tuesday.
	OverlayVersion string
	Fragments      map[string]string
}

// FragmentKey is this upstream's key in the overlay ConfigMap. Model and
// tool upstreams share one ConfigMap, so the key carries which seam it
// is — `model-house.json` beside `warehouse.json` — and two upstreams of
// different kinds may share a name without colliding on a key.
func (s ModelSpec) FragmentKey() string { return "model-" + s.Name + ".json" }

func (s ModelSpec) EgressPolicyName() string  { return "kaimahi-model-" + s.Name + "-egress" }
func (s ModelSpec) IngressPolicyName() string { return "kaimahi-model-" + s.Name + "-ingress" }

// Fragment renders the overlay entry: the JSON the proxy merges over the
// committed table. It carries no custody field — not because kmx chose
// to omit them, but because the plane refuses them in an overlay.
func (s ModelSpec) Fragment() (string, error) {
	entry := map[string]any{
		"base_url":       s.BaseURL,
		"path":           s.Path,
		"protocol":       s.Protocol,
		"classification": s.Classification,
	}
	doc := map[string]any{"upstreams": map[string]any{s.Name: entry}}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// Classifications is the flag's vocabulary. It mirrors the plane's own
// constants; a value this accepts and the plane refuses would be a
// scaffold that produces a config which will not load.
var Classifications = []string{"free", "metered"}

// Protocols is the model seam's wire-protocol vocabulary, and
// PathProtocol names the one a path IS. Both mirror
// plane/internal/config — kmx and the plane are separate modules, so
// this is a deliberate duplicate rather than an import, and
// TestTheScaffoldsProtocolVocabularyMatchesThePlane pins them together.
var Protocols = []string{"chat_completions", "responses"}

// PathProtocol returns the protocol a forwarded path names, or "".
func PathProtocol(path string) string {
	p := strings.Trim(path, "/")
	switch {
	case strings.HasSuffix(p, "chat/completions"):
		return "chat_completions"
	case strings.HasSuffix(p, "responses"):
		return "responses"
	}
	return ""
}

// ParseModelURL takes the endpoint's own URL apart into the two halves
// the table stores separately: the origin the proxy dials, and the ONE
// path it will forward on it.
//
// The split is where an operator would otherwise guess. The committed
// table's own entries split at the same place — base
// `http://ollama.ollama.svc.cluster.local:11434`, path
// `v1/chat/completions` — and getting it wrong produces an upstream that
// loads cleanly and 403s every call with "path not allowed".
func ParseModelURL(raw string) (base, path, service, namespace string, port int, err error) {
	u, parseErr := url.Parse(raw)
	if parseErr != nil || u.Scheme != "http" || u.Host == "" {
		return "", "", "", "", 0, fmt.Errorf("--url %q: want the endpoint's own in-cluster URL including the path "+
			"its clients POST to, e.g. http://<service>.<namespace>:<port>/v1/responses", raw)
	}
	if u.User != nil {
		return "", "", "", "", 0, fmt.Errorf("--url %q: a URL carrying credentials is refused", raw)
	}
	path = strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return "", "", "", "", 0, fmt.Errorf("--url %q: no path. The table allows exactly ONE forwarded path per "+
			"upstream and cannot infer it — give the whole URL a client posts to, e.g. .../v1/responses", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", "", "", "", 0, fmt.Errorf("--url %q: a query string or fragment is not part of a forwarded path", raw)
	}
	base = u.Scheme + "://" + u.Host
	host := u.Hostname()
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	} else {
		port = 80
	}
	host = strings.TrimSuffix(host, ".svc.cluster.local")
	host = strings.TrimSuffix(host, ".svc")
	parts := strings.Split(host, ".")
	switch len(parts) {
	case 1:
		// A bare Service name resolves in the caller's own namespace, and
		// the caller is the proxy. Stated rather than assumed.
		return base, path, parts[0], PlaneNamespace, port, nil
	case 2:
		return base, path, parts[0], parts[1], port, nil
	default:
		return "", "", "", "", 0, fmt.Errorf("--url %q: %q is not an in-cluster Service name "+
			"(want <service>, <service>.<namespace> or <service>.<namespace>.svc.cluster.local). "+
			"A hosted model endpoint holds a real API key, which an overlay may not name — that one is a "+
			"reviewed entry in k8s/plane/upstreams.yaml. See docs/hosted-upstreams.md", raw, u.Hostname())
	}
}

// ValidateModelName holds the name to the shape it has to satisfy
// everywhere it is used, and refuses the committed table's own names so
// the message names the reason rather than arriving as a merge collision.
func ValidateModelName(name string) error {
	if !upstreamNameRE.MatchString(name) || len(name) > 40 {
		return fmt.Errorf("%q is not a usable upstream name: lowercase letters, digits and dashes, "+
			"starting and ending alphanumeric, at most 40 characters — it becomes a URL path segment, "+
			"a ConfigMap key and part of two object names", name)
	}
	for _, committed := range []string{"ollama", "copilot"} {
		if name == committed {
			return fmt.Errorf("%q is one of this repo's committed model upstreams — an overlay may not redefine it "+
				"(the plane refuses a redefinition rather than resolving it by precedence). Choose another name", name)
		}
	}
	return nil
}
