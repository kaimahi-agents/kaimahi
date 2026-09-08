package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
)

// What `kmx status` can say about governance, and how it refuses to guess.
//
// The ungoverned state is COUNTABLE rather than warned about once: "3 tool
// servers, 0 governed" is harder to ignore than a message that scrolls past.
// The fast path — one command to a working agent — is ungoverned by design,
// which makes the count matter more, not less: the ungoverned path is the
// DEFAULT one and nothing else in the tree tells an operator how much of
// their system is on it.
//
// Two rules hold this together:
//
//  1. **The test is local.** A seam is governed when it POINTS AT the
//     plane's Service. That is read off the cluster objects themselves, so
//     it needs no plane, no plane credential and nothing off the cluster —
//     only the API server `kmx status` was already talking to. It is a
//     claim about wiring, never about a plane answering: the plane line
//     beside the counts is what says whether anything is actually in front
//     of them.
//
//  2. **A zero is never invented.** The audit trail already draws this
//     distinction on `acted_for`: `none` is a known nothing, `unknown` is
//     "we cannot say". A population that could not be listed, a URL that could
//     not be read and a dangling ModelConfig reference each get counted as
//     what they are — never folded into "0 governed" or into "direct".

// The plane's in-cluster seams, mirrored from k8s/plane/proxy.yaml: the
// model seam is the proxy, the tool seam is the MCP gateway. They are
// constants rather than a lookup because status must classify wiring on a
// cluster where the plane is not installed at all.
const (
	planeNamespace      = "kaimahi"
	planeProxyService   = "kaimahi-proxy"
	planeGatewayService = "kaimahi-mcp-gateway"
	// planeWorkload is the Deployment k8s/plane/proxy.yaml creates. The
	// plane is INSTALLED when this exists — not when a pod happens to be
	// running, because a scaled-to-zero or mid-rollout plane is a plane
	// that is down, which is a different fact from one that was never
	// deployed and a different thing to tell an operator to do about it.
	planeWorkload = "kaimahi-proxy"
)

// The answers a population can carry. `none` and `unknown` are the audit
// trail's words for `acted_for`, kept rather than reinvented.
const (
	stateCounted   = "counted"
	stateNone      = "none"
	stateUnknown   = "unknown"
	stateInstalled = "installed"
)

// How one seam classified. `unresolved` is the third answer rule 2 requires:
// a seam whose destination could not be read is not a seam pointing
// elsewhere.
const (
	seamGoverned   = "governed"
	seamDirect     = "direct"
	seamUnresolved = "unresolved"
)

// seamPopulation counts one KIND of governable thing. Agents, tool servers
// and credentials are three different populations and a single number that
// mixed them would be worse than three honest ones, so each is counted and
// printed on its own.
type seamPopulation struct {
	// State is counted, none (nothing of this kind exists) or unknown.
	State string `json:"state"`
	// Total, Governed and Direct are meaningful only when State is counted
	// or none; Unresolved counts members whose seam could not be read —
	// they are excluded from Governed and Direct rather than assumed, and
	// UnresolvedRefs names them.
	Total      int `json:"total"`
	Governed   int `json:"governed"`
	Direct     int `json:"direct"`
	Unresolved int `json:"unresolved"`
	// Plaintext counts GOVERNED seams still addressed over http. They are
	// governed — the plane enforces on the credential, never on the scheme —
	// and since the seams began serving TLS their calls also fail closed.
	//
	// Counted rather than reclassified, because "not governed" and "cannot
	// connect" are different problems with different fixes, and saying the
	// first about a seam that is enforcing would be its own false claim.
	// After an upgrade this is the count that matters, and without it the
	// governed count and the certificate line are two reassuring answers
	// about a plane no agent can reach.
	Plaintext int `json:"plaintext"`
	// UnresolvedRefs names what could not be resolved, one entry per member
	// counted in Unresolved.
	UnresolvedRefs []string `json:"unresolvedRefs,omitempty"`
	// Reason belongs to State unknown and to nothing else — a counted
	// population explains itself through UnresolvedRefs, so a consumer
	// never has to decide which meaning a `reason` carries.
	Reason string `json:"reason,omitempty"`
}

// credentialPopulation is the third population: the Secrets the governed
// seams NAME. Only names are read — never a Secret's value — because a
// governed seam whose token Secret is missing is a real broken state that
// otherwise surfaces as a runtime failure much later.
type credentialPopulation struct {
	State    string   `json:"state"`
	Required int      `json:"required"`
	Present  int      `json:"present"`
	Missing  []string `json:"missing"`
	// Partial marks a count taken over only part of the population — one
	// half of the seams could not be listed, so what they require is not in
	// Required. The count that WAS taken is still published, because
	// throwing away a countable half to report `unknown` would hide a
	// missing credential that is genuinely known to be missing.
	Partial bool   `json:"partial,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// planePresence is whether anything is in front of the governed seams.
type planePresence struct {
	// State is installed, none (no plane on this cluster) or unknown.
	State string `json:"state"`
	// Ready and Desired are the PROXY Deployment's replicas, not the
	// namespace's pods: the plane's namespace also holds Postgres, and
	// counting that would report a proxy scaled to zero as "1/1 ready".
	// An installed plane with nothing ready is DOWN, and saying "not
	// installed" about it would be the same false absence rule 2 forbids.
	Ready   int    `json:"ready"`
	Desired int    `json:"desired"`
	Reason  string `json:"reason,omitempty"`
}

// governance is the whole answer, and the shape `-o json` publishes.
type governance struct {
	Plane       planePresence        `json:"plane"`
	ModelSeams  seamPopulation       `json:"modelSeams"`
	ToolSeams   seamPopulation       `json:"toolSeams"`
	Credentials credentialPopulation `json:"credentials"`
	// Certificate is the one the plane serves both seams with. It is
	// reported here rather than left to a dashboard because it expires
	// whether or not anyone is watching, and an expiry nobody is warned
	// about is an outage scheduled in advance.
	Certificate SeamCertificate `json:"certificate"`
}

// An `unknown` population publishes NO counts.
//
// This is the whole finding expressed in the wire format: a caller reading
// `.governed` off a population nobody could read would get 0, which is the
// false zero the table refuses to print. Absent forces the reader to look at
// `state` first. A `none` population DOES carry its zeros, because there
// they are a counted fact.
func (p seamPopulation) MarshalJSON() ([]byte, error) {
	type counted struct {
		State          string   `json:"state"`
		Total          int      `json:"total"`
		Governed       int      `json:"governed"`
		Direct         int      `json:"direct"`
		Unresolved     int      `json:"unresolved"`
		Plaintext      int      `json:"plaintext"`
		UnresolvedRefs []string `json:"unresolvedRefs,omitempty"`
	}
	type bare struct {
		State  string `json:"state"`
		Reason string `json:"reason,omitempty"`
	}
	if p.State == stateUnknown {
		return json.Marshal(bare{State: p.State, Reason: p.Reason})
	}
	return json.Marshal(counted{p.State, p.Total, p.Governed, p.Direct, p.Unresolved, p.Plaintext, p.UnresolvedRefs})
}

func (p credentialPopulation) MarshalJSON() ([]byte, error) {
	type counted struct {
		State    string   `json:"state"`
		Required int      `json:"required"`
		Present  int      `json:"present"`
		Missing  []string `json:"missing"`
		Partial  bool     `json:"partial,omitempty"`
		Reason   string   `json:"reason,omitempty"`
	}
	type bare struct {
		State  string `json:"state"`
		Reason string `json:"reason,omitempty"`
	}
	if p.State == stateUnknown {
		return json.Marshal(bare{State: p.State, Reason: p.Reason})
	}
	missing := p.Missing
	if missing == nil {
		// Always a list, never null: `missing` is read by iterating it.
		missing = []string{}
	}
	return json.Marshal(counted{p.State, p.Required, p.Present, missing, p.Partial, p.Reason})
}

func (p planePresence) MarshalJSON() ([]byte, error) {
	type counted struct {
		State   string `json:"state"`
		Ready   int    `json:"ready"`
		Desired int    `json:"desired"`
	}
	type bare struct {
		State  string `json:"state"`
		Reason string `json:"reason,omitempty"`
	}
	if p.State == stateUnknown {
		return json.Marshal(bare{State: p.State, Reason: p.Reason})
	}
	return json.Marshal(counted{p.State, p.Ready, p.Desired})
}

// classifySeam says where one seam points: at the plane, somewhere else, or
// somewhere that could not be read.
//
// The third answer is not pedantry. A URL kmx cannot parse — empty, or an
// authority with no scheme, which Go reads as a scheme with an opaque body
// and no host at all — is a seam whose destination is unknown, and counting
// it `direct` would be a confident claim built from a failed read.
func classifySeam(rawURL, service string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		// An absent field is a positive fact for a ModelConfig: the
		// provider's own seam, not the plane's. Callers that cannot tell
		// absence from unreadable pass a non-empty value.
		return seamDirect
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return seamUnresolved
	}
	return planeHost(parsed.Hostname(), service)
}

// seamPlaintext reports whether a seam URL still addresses the plane over
// http. It answers the scheme only; whether the URL is a seam at all is
// classifySeam's question, and both are asked before this counts anything.
//
// It exists for one cluster state, and it is the state this release creates:
// a seam written before the seams carried a certificate is governed,
// unchanged, and no longer reachable. Nothing else in `kmx status` can say
// so — the seam counts are about wiring and the certificate line is about the
// plane's own material.
func seamPlaintext(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && parsed != nil && parsed.Scheme == "http"
}

// planeHost classifies a host against one of the plane's Services.
//
// kagent resolves these through the cluster DNS, so every form the cluster
// itself produces has to count: `svc.ns`, `svc.ns.svc`, and the fully
// qualified `svc.ns.svc.cluster.local`, with or without the root's dot.
//
// The fourth shape is the interesting one. `svc.ns.svc.<something else>` is
// EITHER the same Service under a non-default cluster domain OR an external
// host wearing the Service's name — `kaimahi-proxy.kaimahi.svc.evil.com` is
// a registrable domain anyone can own, and calling that governed would be
// rule 2 inverted: a DIRECT seam reported as governed, which is the one
// direction of error that matters. kmx cannot tell the two apart from the
// URL alone, so it says so: `unresolved`, counted as neither.
func planeHost(host, service string) string {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(host), "."), ".")
	if len(labels) < 2 || labels[0] != service || labels[1] != planeNamespace {
		return seamDirect
	}
	switch {
	case len(labels) == 2:
		return seamGoverned
	case labels[2] != "svc":
		return seamDirect
	case len(labels) == 3:
		return seamGoverned
	case len(labels) == 5 && labels[3] == "cluster" && labels[4] == "local":
		return seamGoverned
	}
	return seamUnresolved
}

// modelSeams counts agents by where their model calls actually go.
//
// The agent names a ModelConfig; the ModelConfig names a base URL. An agent
// whose ModelConfig is not on the cluster is UNRESOLVED, not ungoverned:
// the object it points at is missing, and "direct" would be a claim about
// a seam nobody can read.
//
// Only spec.openAI.baseUrl is consulted, which is exact today and is pinned
// by more than convention: `kmx govern` routes the model seam through the
// proxy by applying an OpenAI-shaped preset (k8s/models/governed-*.yaml),
// and there is no other governed model route. A ModelConfig on another
// provider therefore IS direct rather than unclassified. The day the plane
// gains a non-OpenAI upstream, this is the function that has to learn it.
func modelSeams(agents []agentStatus, models []modelStatus) seamPopulation {
	byName := make(map[string]modelStatus, len(models))
	for _, model := range models {
		byName[model.Metadata.Name] = model
	}
	population := seamPopulation{State: stateCounted, Total: len(agents)}
	if len(agents) == 0 {
		population.State = stateNone
		return population
	}
	for _, agent := range agents {
		name := strings.TrimSpace(agent.Spec.Declarative.ModelConfig)
		model, ok := byName[name]
		if !ok {
			population.Unresolved++
			population.UnresolvedRefs = append(population.UnresolvedRefs,
				fmt.Sprintf("%s→%s", agent.Metadata.Name, valueOr(name, "(none)")))
			continue
		}
		switch classifySeam(model.Spec.OpenAI.BaseURL, planeProxyService) {
		case seamGoverned:
			population.Governed++
			if seamPlaintext(model.Spec.OpenAI.BaseURL) {
				population.Plaintext++
			}
		case seamUnresolved:
			population.Unresolved++
			population.UnresolvedRefs = append(population.UnresolvedRefs,
				fmt.Sprintf("%s→%s (unreadable baseUrl)", agent.Metadata.Name, name))
		default:
			population.Direct++
		}
	}
	sort.Strings(population.UnresolvedRefs)
	return population
}

// toolSeams counts tool servers by whether their URL is the MCP gateway.
// A read that failed arrives as a reason and produces unknown.
func toolSeams(servers []toolServerStatus, reason string) seamPopulation {
	if reason != "" {
		return seamPopulation{State: stateUnknown, Reason: reason}
	}
	population := seamPopulation{State: stateCounted, Total: len(servers)}
	if len(servers) == 0 {
		population.State = stateNone
		return population
	}
	for _, server := range servers {
		// A RemoteMCPServer with no URL is not a server pointing elsewhere;
		// it is one whose destination cannot be read.
		switch classifySeam(valueOr(server.Spec.URL, "-"), planeGatewayService) {
		case seamGoverned:
			population.Governed++
			if seamPlaintext(server.Spec.URL) {
				population.Plaintext++
			}
		case seamUnresolved:
			population.Unresolved++
			population.UnresolvedRefs = append(population.UnresolvedRefs,
				server.Metadata.Name+" (unreadable url)")
		default:
			population.Direct++
		}
	}
	sort.Strings(population.UnresolvedRefs)
	return population
}

// credentialSeams checks that every Secret the governed seams name is
// actually there. `present` is the list of Secret NAMES in the namespace —
// no value is read, and none is needed to answer this.
//
// secretErr means the Secrets could not be listed at all, which makes the
// whole answer unknown: an empty list would otherwise become a confident
// accusation naming Secrets that may well exist. seamErr means one half of
// the seams could not be listed, which makes the answer PARTIAL — what the
// other half requires is still known, and a genuinely missing token is
// worth more than a tidy `unknown`.
func credentialSeams(models []modelStatus, servers []toolServerStatus, present []string, secretErr, seamErr string) credentialPopulation {
	if secretErr != "" {
		return credentialPopulation{State: stateUnknown, Reason: secretErr}
	}
	required := map[string]bool{}
	for _, model := range models {
		if classifySeam(model.Spec.OpenAI.BaseURL, planeProxyService) == seamGoverned && model.Spec.APIKeySecret != "" {
			required[model.Spec.APIKeySecret] = true
		}
	}
	for _, server := range servers {
		if classifySeam(valueOr(server.Spec.URL, "-"), planeGatewayService) != seamGoverned {
			continue
		}
		for _, header := range server.Spec.HeadersFrom {
			if strings.EqualFold(header.ValueFrom.Type, "Secret") && header.ValueFrom.Name != "" {
				required[header.ValueFrom.Name] = true
			}
		}
	}
	population := credentialPopulation{State: stateCounted, Required: len(required)}
	if seamErr != "" {
		population.Partial, population.Reason = true, seamErr
	}
	if len(required) == 0 && !population.Partial {
		// A known nothing: no governed seam names a credential. This is
		// `none`, and it is not the same answer as `unknown`.
		population.State = stateNone
		return population
	}
	have := make(map[string]bool, len(present))
	for _, name := range present {
		have[name] = true
	}
	for name := range required {
		if have[name] {
			population.Present++
		} else {
			population.Missing = append(population.Missing, name)
		}
	}
	sort.Strings(population.Missing)
	return population
}

// writeGovernance prints the section. Every line is a count or a stated
// "cannot say"; none of it changes the readiness verdict above it, because
// the ungoverned fast path is a supported one — an ungoverned
// cluster is not a broken cluster, it is an ungoverned one, and status says
// which without calling it a fault.
func writeGovernance(out io.Writer, g governance) {
	fmt.Fprintln(out, "\nGovernance")
	switch g.Plane.State {
	case stateUnknown:
		fmt.Fprintf(out, "  plane:        unknown — %s\n", g.Plane.Reason)
	case stateInstalled:
		switch {
		case g.Plane.Desired == 0:
			fmt.Fprintln(out, "  plane:        installed but SCALED TO ZERO — nothing behind it is being enforced")
		case g.Plane.Ready == 0:
			fmt.Fprintf(out, "  plane:        installed but DOWN (0/%d replicas ready) — nothing behind it is being enforced\n", g.Plane.Desired)
		default:
			fmt.Fprintf(out, "  plane:        installed (%d/%d replicas ready)\n", g.Plane.Ready, g.Plane.Desired)
		}
	default:
		fmt.Fprintln(out, "  plane:        not installed — nothing is enforced in front of these seams (`kmx plane`)")
	}
	fmt.Fprintf(out, "  model seams:  %s\n", seamLine(g.ModelSeams, "agents", "agent"))
	fmt.Fprintf(out, "  tool seams:   %s\n", seamLine(g.ToolSeams, "tool servers", "tool server"))
	fmt.Fprintf(out, "  credentials:  %s\n", credentialLine(g.Credentials))
	fmt.Fprintf(out, "  certificate:  %s\n", g.Certificate.Line)
	if g.Certificate.State == "expiring" || g.Certificate.State == "expired" {
		fmt.Fprintln(out, "                Both seams stop answering when it does; `kmx plane --step certificate` renews it.")
	}
	fmt.Fprintln(out, "  Governed = the seam points at the plane, read from the cluster objects — no")
	fmt.Fprintln(out, "  plane, credential or internet needed. The plane line says whether one is there.")
}

func seamLine(p seamPopulation, plural, singular string) string {
	switch p.State {
	case stateUnknown:
		return "unknown — " + p.Reason
	case stateNone:
		return fmt.Sprintf("none — no %s on this cluster", plural)
	}
	noun := plural
	if p.Total == 1 {
		noun = singular
	}
	line := fmt.Sprintf("%d of %d %s governed, %d direct", p.Governed, p.Total, noun, p.Direct)
	if p.Unresolved > 0 {
		line += fmt.Sprintf(", %d unknown (%s)", p.Unresolved, strings.Join(p.UnresolvedRefs, ", "))
	}
	// Said on the seam's own line rather than left to the certificate line
	// below, because it is a fact about the SEAM: it is governed, and it is
	// addressed over a scheme the plane stopped answering.
	if p.Plaintext > 0 {
		line += fmt.Sprintf("\n                %d governed over PLAINTEXT — the seams serve TLS, so those calls fail closed", p.Plaintext)
	}
	return line
}

func credentialLine(p credentialPopulation) string {
	switch p.State {
	case stateUnknown:
		return "unknown — " + p.Reason
	case stateNone:
		return "none — no governed seam names one"
	}
	line := fmt.Sprintf("%d of %d present", p.Present, p.Required)
	if len(p.Missing) > 0 {
		line += ", missing: " + strings.Join(p.Missing, ", ")
	}
	if p.Partial {
		line += " (partial — " + p.Reason + ")"
	}
	return line
}
