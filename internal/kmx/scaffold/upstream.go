package scaffold

// Scaffolding the TOOL seam for an MCP server this repo did not write.
// `kmx agent create` already treats reviewable YAML as the artifact; this
// does the same for an upstream, and for the same
// reason — the operator has to be able to read what they are about to
// obey.
//
// The division of labour is deliberate and is the whole design:
//
//   kmx owns what is MECHANICAL and error-prone — the gateway URL, the
//   headersFrom wiring, the NetworkPolicy pair and the pod selectors it
//   needs. A typo in the gateway URL points an agent at a different
//   upstream or a 404, silently; a NetworkPolicy written against a
//   Service port when the container listens on another blocks
//   everything, silently. Neither belongs in a human's fingers.
//
//   The OPERATOR owns policy — which tools exist, and what each tool's
//   arguments MEAN (policy_fields). kmx must not guess those, and does
//   not: a tool named without a declaration is refused, not defaulted.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// GatewayHost is the in-cluster address of the enforcing MCP gateway.
// Every governed RemoteMCPServer's URL is this host plus the upstream's
// name; nothing else may vary, which is why kmx derives it.
//
// https, because a tool RESPONSE is not recorded anywhere the plane keeps —
// the audit row holds the decision and a capped summary of the declared
// argument fields, never the body a tool returned. Whatever the agent read
// crosses this wire and exists in no other record.
//
// The host is the two-label form, so the certificate has to carry it: a
// certificate covering only the fully qualified name fails this URL with a
// hostname error that reads like a network fault.
const GatewayHost = "https://kaimahi-mcp-gateway.kaimahi:8081"

// GatewayURL is the one URL shape a governed tool seam may have.
func GatewayURL(upstream string) string {
	return GatewayHost + "/upstream/" + upstream + "/mcp"
}

// OverlayConfigMap is where operator-added upstreams live: beside this
// repo's committed table, never inside it.
const OverlayConfigMap = "kaimahi-upstreams-extra"

const (
	// PlaneNamespace holds the proxy, the gateway and the overlay.
	PlaneNamespace = "kaimahi"
	// AgentNamespace holds kagent's Agents and RemoteMCPServers.
	AgentNamespace = "kagent"
	// ProxySelectorKey / ProxySelectorValue are the label every plane
	// NetworkPolicy pins the proxy pod by (k8s/plane/network-policy.yaml).
	// Both generated policies are emitted FROM these, so the pinning test
	// that compares them with the committed boundary guarantees something.
	ProxySelectorKey   = "app"
	ProxySelectorValue = "kaimahi-proxy"
)

var (
	// upstreamNameRE is a tool_upstreams key. It becomes a URL path
	// segment, a ConfigMap key, and part of three object names, so it is
	// held to the strictest of those: an RFC 1123 label.
	upstreamNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// toolNameRE matches the gateway's own idea of a tool name.
	toolNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	// policyFieldRE is plane/internal/config/policy.go's `policyField`.
	// Kept identical on purpose: a field this accepts and the plane
	// refuses would be a scaffold that produces a config that will not
	// load, which is exactly what the scaffolder exists to stop.
	policyFieldRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ToolDecl is one tool of the onboarded server, and the governance-
// critical choice the operator made about it.
type ToolDecl struct {
	Name string
	// Declared distinguishes the three real answers. Declared=false is
	// "no entry in `tools` at all" — the whole-object binding.
	Declared bool
	// Fields is the policy_fields list when Declared. A non-nil empty
	// slice is a REAL answer (a verb-level binding) and is emitted as
	// `[]`, which the plane requires and distinguishes from an omission.
	Fields []string
}

// VerbLevel reports the weakest of the three settings: an explicit
// declaration that no argument matters.
func (t ToolDecl) VerbLevel() bool { return t.Declared && len(t.Fields) == 0 }

// policyFieldsHelp is printed wherever a tool is named without a
// declaration. The choice is the governance-critical moment of
// onboarding, so the consequence of each option is stated at the point
// of choosing rather than left in a document.
const policyFieldsHelp = `every tool needs a policy_fields declaration — say which of its arguments
  are policy-relevant. The declaration decides what an approval BINDS to
  and what the audit line SAYS, so kmx will not choose it for you:

    --tool %[1]s:amount_cents,payee_id   those fields are policy-relevant.
        An approval is welded to THOSE VALUES: approving a payment of
        $100 to one payee cannot be spent on another amount or another
        payee, and the audit row names both. This is the setting you
        want for anything with consequences.

    --tool %[1]s:                        NO argument is policy-relevant.
        This is a VERB-LEVEL binding and it is the WEAKEST setting:
        approving this tool once approves it for ANY arguments until the
        grant is spent, and the audit says only that the verb ran.
        Correct for a tool that takes no arguments that matter; wrong
        for anything that moves money, data or state.

    --tool %[1]s:*                       do not declare it at all.
        The approval then binds the tool's WHOLE argument object. Exact,
        and brittle: an agent re-emitting a semantically identical call
        with one extra field produces a different digest, so the
        approval a human granted will not admit the retry.`

// ParseToolDecls turns repeated --tool values into declarations, and
// refuses a bare tool name.
func ParseToolDecls(values []string) ([]ToolDecl, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("no tools named. Name each tool the server offers with --tool <name>:<fields>.\n  %s",
			fmt.Sprintf(policyFieldsHelp, "<name>"))
	}
	var out []ToolDecl
	seen := map[string]bool{}
	for _, v := range values {
		name, spec, hasSpec := strings.Cut(strings.TrimSpace(v), ":")
		if !toolNameRE.MatchString(name) {
			return nil, fmt.Errorf("--tool %q: %q is not a tool name", v, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("--tool %q: %s is named twice", v, name)
		}
		seen[name] = true
		if !hasSpec {
			return nil, fmt.Errorf("--tool %s names a tool but declares nothing.\n  %s",
				name, fmt.Sprintf(policyFieldsHelp, name))
		}
		spec = strings.TrimSpace(spec)
		if spec == "*" {
			out = append(out, ToolDecl{Name: name})
			continue
		}
		fields := []string{}
		for _, f := range strings.Split(spec, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if !policyFieldRE.MatchString(f) {
				return nil, fmt.Errorf("--tool %s: %q is not an argument field name "+
					"(top-level names only, [A-Za-z0-9_-]{1,64}); a value nested inside an object is not addressable", name, f)
			}
			for _, already := range fields {
				if already == f {
					return nil, fmt.Errorf("--tool %s: field %q is listed twice", name, f)
				}
			}
			fields = append(fields, f)
		}
		out = append(out, ToolDecl{Name: name, Declared: true, Fields: fields})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// UpstreamSpec is everything the four documents are rendered from.
// Every field is either operator-supplied or read from the live cluster;
// nothing here is inferred from a name.
type UpstreamSpec struct {
	// Name is the tool_upstreams key.
	Name string
	// URL is the server's own in-cluster MCP endpoint.
	URL string
	// Service, ServiceNamespace and PodPort locate the server. PodPort
	// is the CONTAINER port (the Service's resolved targetPort), because
	// NetworkPolicy is evaluated on the post-NAT pod address.
	Service          string
	ServiceNamespace string
	PodPort          int
	// PodLabels is the Service's own selector, read from the cluster —
	// the labels that actually route to those pods, not a guess from the
	// Service's name.
	PodLabels map[string]string
	// ServerDNS opts the server's pods into DNS egress. Everything else
	// about the server's egress is the operator's, and this scaffold
	// grants none of it.
	ServerDNS bool
	// ServerEgressKeep leaves the server's egress alone entirely: the
	// scaffolded policy then lists Ingress only.
	ServerEgressKeep bool
	Tools            []ToolDecl
	// Secret is the agent-side Secret the RemoteMCPServer resolves the
	// governed credential from. kmx NEVER writes its value — only its
	// name; `kmx tools govern` mints the token into it.
	Secret string
	// OverlayVersion is the resourceVersion the overlay ConfigMap was
	// read at, empty when it did not exist. It is emitted into the
	// manifest as an optimistic-concurrency precondition: `kubectl
	// apply` refuses a stale one with a Conflict and changes nothing.
	//
	// That matters because this file is designed to be applied LATER —
	// `--no-apply` says "review it, then apply it" — and the emitted map
	// carries every fragment as it stood when it was read. Without the
	// precondition, applying a file scaffolded on Monday would prune a
	// fragment somebody added on Tuesday, and the upstream that fragment
	// constrained would keep running, unbounded.
	OverlayVersion string
	// Fragments is the overlay as it will exist AFTER this onboarding:
	// every fragment already in the cluster's overlay ConfigMap, plus
	// this one. The emitted ConfigMap is whole, so what an operator
	// reviews is what the cluster will hold.
	Fragments map[string]string
}

// FragmentKey is this upstream's key in the overlay ConfigMap.
func (s UpstreamSpec) FragmentKey() string { return s.Name + ".json" }

// EgressPolicyName / IngressPolicyName / ServerName are derived, never
// supplied, so two upstreams can never collide on an object name.
func (s UpstreamSpec) EgressPolicyName() string  { return "kaimahi-upstream-" + s.Name + "-egress" }
func (s UpstreamSpec) IngressPolicyName() string { return "kaimahi-upstream-" + s.Name + "-ingress" }
func (s UpstreamSpec) ServerName() string        { return "kaimahi-" + s.Name }

// Fragment renders this upstream's overlay entry: the JSON the proxy
// merges over the committed table. Indented, because a human reviews it.
func (s UpstreamSpec) Fragment() (string, error) {
	entry := map[string]any{"url": s.URL}
	tools := map[string]any{}
	for _, t := range s.Tools {
		if !t.Declared {
			continue
		}
		fields := t.Fields
		if fields == nil {
			fields = []string{}
		}
		tools[t.Name] = map[string]any{"policy_fields": fields}
	}
	if len(tools) > 0 {
		entry["tools"] = tools
	}
	doc := map[string]any{"tool_upstreams": map[string]any{s.Name: entry}}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// ValidateNamespace holds a namespace read out of a URL to the shape
// Kubernetes will accept. net/url already refuses control characters and
// spaces in a host, so this is not the injection guard — quote() is —
// but a value that cannot be a namespace should be refused where the
// message can name the URL, not by kubectl three steps later.
func ValidateNamespace(ns string) error {
	if !upstreamNameRE.MatchString(ns) || len(ns) > 63 {
		return fmt.Errorf("%q is not a Kubernetes namespace name (RFC 1123 label)", ns)
	}
	return nil
}

// ValidateObjectName holds a Kubernetes object name an operator typed to
// the shape the API server will accept, so a refusal names the flag rather
// than arriving from kubectl several steps later.
func ValidateObjectName(name string) error {
	if !upstreamNameRE.MatchString(name) || len(name) > 63 {
		return fmt.Errorf("%q is not a Kubernetes object name (RFC 1123 label)", name)
	}
	return nil
}

// ValidateUpstreamName holds the name to the strictest shape it has to
// satisfy anywhere it is used.
func ValidateUpstreamName(name string) error {
	if !upstreamNameRE.MatchString(name) || len(name) > 40 {
		return fmt.Errorf("%q is not a usable upstream name: lowercase letters, digits and dashes, "+
			"starting and ending alphanumeric, at most 40 characters — it becomes a URL path segment, "+
			"a ConfigMap key and part of three object names", name)
	}
	// Four of the committed table's six names, refused here so the message
	// names the reason rather than arriving as a merge collision later.
	//
	// The list is INCOMPLETE — `github-release` and `ado` joined the table
	// later and are not below — so those two get the plane's merge refusal
	// instead of this one. Not a hole: the merge refuses a redefinition
	// outright rather than resolving it by precedence, and `kmx tools add`
	// validates through /admin/config/validate, which runs that merge. What
	// is lost is only the earlier, better-worded message.
	for _, committed := range []string{"kagent-tools", "slack", "github", "erp"} {
		if name == committed {
			return fmt.Errorf("%q is one of this repo's committed upstreams — an overlay may not redefine it. "+
				"Choose another name", name)
		}
	}
	return nil
}
