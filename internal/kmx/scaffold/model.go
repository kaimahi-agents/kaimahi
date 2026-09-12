package scaffold

// Scaffolding the MODEL seam for an endpoint this repo did not deploy.
//
// The reviewable artifact contains an overlay fragment and a NetworkPolicy
// pair pinned to the live Service's selector and container port. Client wiring
// is printed separately: the owner's runtime need not use kagent resources.
//
// A model route names one origin, one forwarded path, its wire protocol and
// an explicit free/metered classification. Cost is never inferred. Every
// existing credential can reach a newly onboarded model, bounded by budgets;
// a keyless overlay does not prove the endpoint behind it holds no paid key.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ProxyHost is the in-cluster address of the metering model proxy —
// the value an adopter's OPENAI_BASE_URL becomes, plus the upstream's
// name. HTTPS protects prompts and completions on the wire (the
// ledger holds counts and a cost, never content).
const ProxyHost = "https://kaimahi-proxy.kaimahi.svc.cluster.local:8080"

// Shared model/owner-migration names, pinned to the committed manifests.
const (
	OverlayConfigMap   = "kaimahi-upstreams-extra"
	PlaneNamespace     = "kaimahi"
	ProxySelectorKey   = "app"
	ProxySelectorValue = "kaimahi-proxy"
)

var (
	upstreamNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	objectNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// ValidateUpstreamName preserves migration's existing name validation.
// Reserved names remain part of that compatibility contract even though
// their former tool generators have been retired.
func ValidateUpstreamName(name string) error {
	if !upstreamNameRE.MatchString(name) || len(name) > 40 {
		return fmt.Errorf("%q is not a usable upstream name: lowercase letters, digits and dashes, "+
			"starting and ending alphanumeric, at most 40 characters — it becomes a URL path segment, "+
			"a ConfigMap key and part of three object names", name)
	}
	for _, committed := range []string{"kagent-tools", "slack", "github", "erp"} {
		if name == committed {
			return fmt.Errorf("%q is one of this repo's committed upstreams — an overlay may not redefine it. Choose another name", name)
		}
	}
	return nil
}

// ValidateNamespace checks the Kubernetes RFC 1123 label shape.
func ValidateNamespace(ns string) error {
	if !upstreamNameRE.MatchString(ns) || len(ns) > 63 {
		return fmt.Errorf("%q is not a Kubernetes namespace name (RFC 1123 label)", ns)
	}
	return nil
}

// ValidateObjectName checks referenced Secret, Deployment and ConfigMap names.
func ValidateObjectName(name string) error {
	if !objectNameRE.MatchString(name) || len(name) > 253 {
		return fmt.Errorf("%q is not a Kubernetes object name (RFC 1123 subdomain: "+
			"lowercase letters, digits, dashes and dots, starting and ending alphanumeric)", name)
	}
	return nil
}

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
	// ServerDNS / ServerEgressKeep control the endpoint's own egress.
	ServerDNS        bool
	ServerEgressKeep bool
	// The emitted ConfigMap is WHOLE, and carries the
	// version it was read at as an apply precondition, so a file
	// scaffolded on Monday cannot prune a fragment added on Tuesday.
	OverlayVersion string
	Fragments      map[string]string
}

// FragmentKey preserves the existing model overlay key shape.
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
// Whole SEGMENTS, not a string suffix — see the plane's own copy for
// why `v1/xresponses` must not read as the Responses API.
func PathProtocol(path string) string {
	p := strings.Trim(path, "/")
	switch {
	case p == "chat/completions" || strings.HasSuffix(p, "/chat/completions"):
		return "chat_completions"
	case p == "responses" || strings.HasSuffix(p, "/responses"):
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
		// http only, and said rather than implied: an in-cluster endpoint
		// serving TLS is legal in the committed table but not here,
		// because the proxy would then need a trust anchor for it and
		// `ca_file` is one of the fields an overlay may not set.
		return "", "", "", "", 0, fmt.Errorf("--url %q: want the endpoint's own in-cluster URL over plain http, "+
			"including the path its clients POST to, e.g. http://<service>.<namespace>:<port>/v1/responses. "+
			"An in-cluster endpoint serving TLS needs a trust anchor, and `ca_file` is one of the fields an "+
			"overlay may not set — that one is a reviewed entry in k8s/plane/upstreams.yaml", raw)
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
	// Every model upstream k8s/plane/upstreams.yaml carries. An overlay
	// may not redefine one, so the refusal names the reason here rather
	// than arriving later as a merge collision.
	for _, committed := range []string{"ollama", "copilot", "orka", "orka-coordinator"} {
		if name == committed {
			return fmt.Errorf("%q is one of this repo's committed model upstreams — an overlay may not redefine it "+
				"(the plane refuses a redefinition rather than resolving it by precedence). Choose another name", name)
		}
	}
	return nil
}
