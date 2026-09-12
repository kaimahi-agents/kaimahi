package scaffold

import (
	"fmt"
	"sort"
	"strings"
)

// Server egress postures for an onboarded model endpoint.
const (
	EgressNone = "none"
	EgressDNS  = "dns"
	EgressKeep = "keep"
)

var ServerEgressModes = []string{EgressNone, EgressDNS, EgressKeep}

// matchLabels quotes cluster-supplied keys and values, in stable order.
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

// GenerateModel renders the three documents that make a model endpoint a
// governed upstream, in the order they must be applied:
//
//  1. the overlay fragment (the proxy learns the endpoint exists),
//  2. the proxy's egress allowance to it,
//  3. the endpoint's ingress allowance from the proxy — and nothing else.
//
// Client wiring is printed separately rather than requiring a kagent CRD.
func GenerateModel(spec ModelSpec) (string, error) {
	if err := ValidateModelName(spec.Name); err != nil {
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
	if spec.Protocol == "" {
		return "", fmt.Errorf("upstream %q: no protocol — the fragment must state it, never leave it to be guessed", spec.Name)
	}
	var b strings.Builder
	for _, part := range []func(ModelSpec) (string, error){
		modelOverlayDocument, modelEgressDocument, modelIngressDocument,
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

func modelOverlayDocument(spec ModelSpec) (string, error) {
	var b strings.Builder
	b.WriteString(`# 1. The proxy's model upstream table, as an OVERLAY.
#
# This repo's own model upstreams live in the committed ConfigMap
# kaimahi-upstreams and are not touched by onboarding: the proxy merges
# every fragment here over that table at boot, refusing any fragment
# that would redefine an entry rather than resolving it by precedence.
# That is what makes this survive the next ` + "`kmx plane`" + `, which
# re-applies the committed table and would otherwise discard it.
#
# Three fields are the governance-critical part:
#
#   path            the ONE forwarded remainder this upstream accepts.
#                   Anything else is refused before any upstream contact.
#   protocol        where the meter reads token counts. Wrong, and the
#                   plane refuses the call rather than recording zero.
#   classification  free or metered, and it is EXPLICIT. Never inferred:
#                   a $0 by inference is a budget nothing can exhaust.
#
# An overlay entry may not carry credential_file, credential_header,
# internet, ca_file, extra_headers or prices — the plane refuses them
# here, not just kmx. So this describes an in-cluster, keyless endpoint.
# A hosted or keyed model upstream is a reviewed entry in
# k8s/plane/upstreams.yaml.
`)
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
  # onboarding or a reviewed fragment edit — kubectl refuses this
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

func modelEgressDocument(spec ModelSpec) (string, error) {
	sel, err := matchLabels(spec.PodLabels, 14)
	if err != nil {
		return "", err
	}
	ns, err := quote(spec.ServiceNamespace)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`---
# 2. The proxy may reach this model endpoint — and this is an ADDITIVE
# policy, so the plane's committed boundary
# (k8s/plane/network-policy.yaml) is not edited to make room for it. The
# allowance is exactly one destination on exactly one port.
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

func modelIngressDocument(spec ModelSpec) (string, error) {
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
# Out: cluster DNS and nothing else (--server-egress dns). The endpoint
# may resolve names; it may open no connection to anything else.`
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
# This policy says NOTHING about the endpoint's own egress
# (--server-egress keep): whatever it could reach before, it still can.
# A model server that pulls weights from the internet needs this, and
# that path is unbounded and is yours to bound.`
	default:
		note = `#
# Out: NOTHING. Egress is listed with no rules on purpose
# (--server-egress none, the default) — the strongest statement
# available that this endpoint holds no credential and reaches no other
# system. A model server that pulls its weights at startup will FAIL
# under this; that is a reason to choose --server-egress keep
# deliberately, not a reason for the default to be weaker.`
	}
	return fmt.Sprintf(`---
# 3. Only the proxy may reach this model endpoint.
#
# This is the half of the pair that makes metering a boundary rather
# than a convention. Without it any pod in the cluster could call the
# endpoint directly — around the budget, around the ledger, around the
# credential — and every number the plane reports about this model would
# hold only for callers who chose to come through the front door.
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
