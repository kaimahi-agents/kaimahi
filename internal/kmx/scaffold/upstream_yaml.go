package scaffold

import (
	"fmt"
	"sort"
	"strings"
)

// Server egress postures. The scaffolded ingress policy always says
// "only the proxy may reach this server". What it says about the
// server's OWN egress is a real choice with a real consequence, so it is
// named rather than defaulted silently.
const (
	// EgressNone: policyTypes lists Egress with no rules — the server
	// may open no connection to anything. The strongest statement
	// available that a tool server holds no credential and reaches no
	// other system, and the shape this repo's fixture ERP carries.
	EgressNone = "none"
	// EgressDNS: the same, plus cluster DNS.
	EgressDNS = "dns"
	// EgressKeep: an Ingress-only policy. kmx says nothing about the
	// server's egress and changes nothing about it.
	EgressKeep = "keep"
)

// ServerEgressModes is the flag's vocabulary, in the order the usage
// prints them.
var ServerEgressModes = []string{EgressNone, EgressDNS, EgressKeep}

// GenerateUpstream renders the four documents that make an MCP server a
// governed upstream, in the order they must be applied:
//
//  1. the overlay fragment (the gateway learns the server exists),
//  2. the proxy's egress allowance to it,
//  3. the server's ingress allowance from the proxy — and nothing else,
//  4. the RemoteMCPServer whose URL is the gateway.
//
// Nothing here carries a credential; document 4 names a Secret and the
// generator refuses any key-shaped byte in its own output.
func GenerateUpstream(spec UpstreamSpec) (string, error) {
	if err := ValidateUpstreamName(spec.Name); err != nil {
		return "", err
	}
	if err := ValidateNamespace(spec.ServiceNamespace); err != nil {
		return "", fmt.Errorf("upstream %q: %w", spec.Name, err)
	}
	if len(spec.PodLabels) == 0 {
		return "", fmt.Errorf("upstream %q: no pod labels — the NetworkPolicy pair has nothing to pin", spec.Name)
	}
	if spec.PodPort <= 0 || spec.PodPort > 65535 {
		return "", fmt.Errorf("upstream %q: pod port %d is out of range", spec.Name, spec.PodPort)
	}
	if len(spec.Tools) == 0 {
		return "", fmt.Errorf("upstream %q: no tools declared", spec.Name)
	}
	var b strings.Builder
	for _, part := range []func(UpstreamSpec) (string, error){
		overlayDocument, proxyEgressDocument, serverIngressDocument, remoteServerDocument,
	} {
		doc, err := part(spec)
		if err != nil {
			return "", err
		}
		b.WriteString(doc)
	}
	out := b.String()
	if err := RefuseKeyShapes(out); err != nil {
		return "", err
	}
	return out, nil
}

func overlayDocument(spec UpstreamSpec) (string, error) {
	var b strings.Builder
	b.WriteString(`# 1. The gateway's upstream table, as an OVERLAY.
#
# This repo's own four upstreams live in the committed ConfigMap
# kaimahi-upstreams and are not touched by onboarding: the proxy merges
# every fragment here over that table at boot, refusing any fragment
# that would redefine an entry rather than resolving it by precedence.
# That is what makes this survive the next ` + "`kmx plane`" + `, which
# re-applies the committed table and would otherwise discard it.
#
# The policy_fields lists below are the governance-critical part, and
# the only part kmx did not decide: they say which of each tool's
# arguments an approval BINDS to and the audit line NAMES.`)
	verb := []string{}
	for _, t := range spec.Tools {
		if t.VerbLevel() {
			verb = append(verb, t.Name)
		}
	}
	if len(verb) > 0 {
		fmt.Fprintf(&b, "\n#\n# WEAKEST SETTING IN USE: %s declared `policy_fields: []`\n"+
			"# — a verb-level binding. Approving one such call approves the verb\n"+
			"# for ANY arguments until the grant is spent, and the audit line says\n"+
			"# only that the verb ran. Correct only where no argument matters.\n",
			strings.Join(verb, ", "))
	} else {
		b.WriteString("\n")
	}
	b.WriteString(`apiVersion: v1
kind: ConfigMap
metadata:
  name: ` + OverlayConfigMap + `
  namespace: ` + PlaneNamespace + `
`)
	if spec.OverlayVersion != "" {
		version, err := quote(spec.OverlayVersion)
		if err != nil {
			return "", err
		}
		b.WriteString(`  # The version this overlay was READ at. It makes the apply
  # conditional: if anyone has changed the overlay since — another
  # onboarding, a hand-added standing constraint — kubectl refuses this
  # with a Conflict and changes nothing, rather than replacing their
  # work with a snapshot taken before it existed. Scaffold again to
  # pick their change up.
  resourceVersion: ` + version + "\n")
	}
	b.WriteString("data:\n")
	keys := make([]string, 0, len(spec.Fragments))
	for k := range spec.Fragments {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		quoted, err := quote(k)
		if err != nil {
			return "", err
		}
		block, err := literalBlock(quoted, spec.Fragments[k], 2, 4)
		if err != nil {
			return "", err
		}
		b.WriteString(block)
	}
	return b.String(), nil
}

func proxyEgressDocument(spec UpstreamSpec) (string, error) {
	sel, err := matchLabels(spec.PodLabels, 14)
	if err != nil {
		return "", err
	}
	ns, err := quote(spec.ServiceNamespace)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`---
# 2. The proxy may reach this server — and this is an ADDITIVE policy,
# so the plane's committed boundary (k8s/plane/network-policy.yaml) is
# not edited to make room for it. The allowance is exactly one
# destination on exactly one port.
#
# The pod selector is the Service's OWN selector, read from the cluster,
# and the port is the Service's resolved targetPort — not its published
# port. NetworkPolicy is evaluated on the post-NAT POD address, so a
# rule written against a Service port that differs from the container's
# blocks every call while looking correct.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: %s
spec:
  podSelector:
    matchLabels:
      %s: %s
  policyTypes: [Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
%s
      ports:
        - protocol: TCP
          port: %d
`, spec.EgressPolicyName(), PlaneNamespace, ProxySelectorKey, ProxySelectorValue, ns, sel, spec.PodPort), nil
}

func serverIngressDocument(spec UpstreamSpec) (string, error) {
	sel, err := matchLabels(spec.PodLabels, 6)
	if err != nil {
		return "", err
	}
	ns, err := quote(spec.ServiceNamespace)
	if err != nil {
		return "", err
	}
	types, egress, note := "[Ingress, Egress]", "", ""
	switch {
	case spec.ServerDNS:
		note = `#
# Out: cluster DNS and nothing else (--server-egress dns). The server may
# resolve names; it may open no connection to anything else.`
		egress = `  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
`
	case spec.ServerEgressKeep:
		types = "[Ingress]"
		note = `#
# This policy says NOTHING about the server's own egress
# (--server-egress keep): whatever it could reach before, it still can.
# If this server holds a credential or talks to the internet, that path
# is unbounded and is yours to bound.`
	default:
		note = `#
# Out: NOTHING. Egress is listed with no rules on purpose (--server-egress
# none, the default) — the strongest statement available that this server
# holds no credential and reaches no other system: there is no path for
# one to be used. If it needs cluster DNS, edit this document (or delete
# it and this upstream's overlay key, then scaffold again with
# --server-egress dns). Anything wider than DNS is yours to write.`
	}
	return fmt.Sprintf(`---
# 3. Only the proxy may reach this server.
#
# This is the half of the pair that makes governance a boundary rather
# than a convention. Without it any pod in the cluster could call the
# server directly — around the allowlist, around the standing
# constraints, around the call-bound grants and around the audit — and
# every claim the plane makes about this tool would hold only for
# callers who chose to come through the front door.
%s
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: %s
spec:
  podSelector:
    matchLabels:
%s
  policyTypes: %s
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
              %s: %s
      ports:
        - protocol: TCP
          port: %d
%s`, note, spec.IngressPolicyName(), ns, sel, types,
		PlaneNamespace, ProxySelectorKey, ProxySelectorValue, spec.PodPort, egress), nil
}

func remoteServerDocument(spec UpstreamSpec) (string, error) {
	name, err := quote(spec.ServerName())
	if err != nil {
		return "", err
	}
	secret, err := quote(spec.Secret)
	if err != nil {
		return "", err
	}
	desc, err := quote(fmt.Sprintf("Kaimahi-governed %s — behind the enforcing MCP gateway", spec.Name))
	if err != nil {
		return "", err
	}
	url, err := quote(GatewayURL(spec.Name))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`---
# 4. The seam an agent is pointed at.
#
# The URL is the GATEWAY, never the server: %s.
# Getting that one string wrong points an agent at a different upstream
# or at a 404, and neither says so — which is why kmx derives it from
# the upstream's name rather than asking anyone to retype it.
#
# headersFrom resolves the agent's credential from a Secret holding ONLY
# a Kaimahi kmh_ token (the plane stores its sha256). This scaffold never
# writes that value; `+"`kmx tools govern`"+`
# mints it into this Secret.
#
# This object is inert until then: kagent's controller discovers tools
# THROUGH the gateway with that credential, so status.discoveredTools is
# the allowlist projection — an agent never sees a tool it may not call.
apiVersion: kagent.dev/v1alpha2
kind: RemoteMCPServer
metadata:
  name: %s
  namespace: %s
spec:
  description: %s
  protocol: STREAMABLE_HTTP
  url: %s
  # The gateway serves TLS under the plane's own authority, which no
  # public trust store knows about. This names the authority's
  # certificate — public material that says who to trust and confers
  # nothing — and both clients read it: kagent's controller, which
  # discovers tools through the gateway, and the agent pod, which calls
  # them. `+"`kmx plane`"+` publishes the Secret; the controller mounts it
  # into the agent for you.
  #
  # Verification is NOT disabled here and must not be. A seam whose
  # client skips verification costs the same certificate machinery and
  # buys nothing, while looking like it bought something.
  tls:
    caCertSecretRef: %s
    caCertSecretKey: %s
  timeout: 30s
  sseReadTimeout: 5m0s
  terminateOnClose: true
  headersFrom:
    - name: Authorization
      valueFrom:
        type: Secret
        name: %s
        key: api-key
`, GatewayURL(spec.Name), name, AgentNamespace, desc, url, PlaneCASecret, PlaneCAKey, secret), nil
}

// matchLabels renders a label map as YAML, keys sorted, both sides
// quoted — a label value is cluster-supplied text and quoting is what
// stops a crafted one from closing the mapping.
func matchLabels(labels map[string]string, indent int) (string, error) {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pad := strings.Repeat(" ", indent)
	var b strings.Builder
	for i, k := range keys {
		qk, err := quote(k)
		if err != nil {
			return "", err
		}
		qv, err := quote(labels[k])
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s%s: %s", pad, qk, qv)
	}
	return b.String(), nil
}
