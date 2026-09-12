// Package config loads the proxy's upstream table from a mounted file
// (committed ConfigMap — no key material lives here; credential values
// come from Secret-mounted files the config only names). The table plays
// tomte-old's ProviderRoute role: one upstream base and exactly one
// allowed forwarded path per upstream is the whole blast radius.
package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
	"github.com/kaimahi-agents/kaimahi/plane/internal/pricing"
)

const (
	// ClassFree is an EXPLICIT $0 classification (in-cluster ollama).
	// Never inferred — standing guidance forbids blanket $0 by inference.
	ClassFree = "free"
	// ClassMetered counts tokens always; cost applies only when a real
	// price row is configured for the model (the priced-pair gate).
	ClassMetered = "metered"
)

// The model seam's wire protocols. An upstream declares which one it
// speaks, because the two differ in the only thing the plane reads out
// of a response: where the token counts are and what they are called.
//
//	chat_completions  POST v1/chat/completions
//	                  {"usage": {"prompt_tokens", "completion_tokens"}}
//	responses         POST v1/responses
//	                  {"usage": {"input_tokens", "output_tokens"}}
//
// Before this, the table had one shape and no name for it, and an
// operator who added a Responses-API path got a call that was forwarded,
// answered, and ledgered as `0 in / 0 out` — a budget over it could
// never be exhausted. A protocol the plane has no reader for is refused
// at load rather than forwarded: see Parse, and docs/spend.md.
const (
	ProtocolChatCompletions = "chat_completions"
	ProtocolResponses       = "responses"
)

// Protocols is the vocabulary, in the order messages print it.
var Protocols = []string{ProtocolChatCompletions, ProtocolResponses}

// PathProtocol names the protocol a forwarded path IS, or "" when the
// path names none this plane knows. It is used twice and for opposite
// reasons: to fill in an undeclared protocol (so every table written
// before protocols existed keeps working, unedited), and to REFUSE a
// declaration that disagrees with its own path — which is the mistake
// that produced the silent zero, written the other way round.
//
// It matches whole SEGMENTS, not a string suffix, and the difference is
// not pedantry: a plain `HasSuffix` reads `v1/xresponses` as the
// Responses API. That would guess a protocol for a path that names none
// — the one thing this function exists to stop — and would then refuse
// the operator who correctly declared the other one, in a message
// blaming their declaration.
func PathProtocol(path string) string {
	p := strings.Trim(path, "/")
	switch {
	case p == "chat/completions" || strings.HasSuffix(p, "/chat/completions"):
		return ProtocolChatCompletions
	case p == "responses" || strings.HasSuffix(p, "/responses"):
		return ProtocolResponses
	}
	return ""
}

type Upstream struct {
	// BaseURL is the upstream origin plus any path prefix it expects.
	BaseURL string `json:"base_url"`
	// Path is the single allowed forwarded remainder (no leading slash) —
	// exactly what kagent's OpenAI client appends to the governed preset's
	// baseUrl (e.g. "v1/chat/completions").
	Path string `json:"path"`
	// Protocol is the wire shape this upstream speaks — one of
	// Protocols. Optional ONLY when Path names it (a path ending
	// `chat/completions` or `responses`); otherwise required, because
	// the alternative is guessing where the token counts are, and a
	// wrong guess meters zero without saying so.
	Protocol string `json:"protocol,omitempty"`
	// ClientPath is the ONE path a CLIENT may post to, when that is not
	// the one path forwarded upstream. Absent — which is every upstream
	// that existed before this field — the two are the same path and
	// nothing is translated.
	//
	// It exists because the wire shape a framework sends and the wire
	// shape an endpoint serves have stopped being the same thing. One
	// current agent framework speaks the Responses API by default and
	// offers no switch; several endpoints serve chat completions and no
	// Responses route at all, and at least one of them answers the
	// unrouted path with its own dashboard's HTML under a 200. Pointing
	// that framework at that endpoint cannot be made to work by
	// configuration on either side.
	//
	// So the seam accepts `v1/responses` from the client, forwards
	// `v1/chat/completions` upstream, and translates in both directions.
	// The pairing is not general: responses -> chat_completions is the
	// only one implemented, and Parse refuses every other combination
	// rather than accepting a declaration nothing can honour. Metering is
	// untouched by the translation — usage is still read out of the
	// upstream's own body under the upstream's own protocol, so the
	// counts on the ledger row are the counts the model reported.
	ClientPath     string `json:"client_path,omitempty"`
	Classification string `json:"classification"`
	// CredentialFile, when set, is a Secret-mounted file holding the real
	// upstream credential; read per request so rotation needs no restart.
	// Empty means the upstream is keyless and requests are forwarded bare.
	CredentialFile string `json:"credential_file,omitempty"`
	// CredentialHeader is the header the credential is injected into.
	// "authorization" (the default) sends "Authorization: Bearer <v>".
	CredentialHeader string `json:"credential_header,omitempty"`
	// ExtraHeaders are set on every forwarded request (after client-header
	// passthrough, so they win). Non-secret values only.
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
	// Prices maps model name -> configured price. Only meaningful on
	// metered upstreams.
	Prices map[string]pricing.Price `json:"prices,omitempty"`
	// Internet marks an upstream that lives outside the cluster:
	// it is reached ONLY through the hardened dialer (internal/egress —
	// https, port 443, every resolved address vetted, the checked address
	// dialed, bounded and capped). Without the marker an upstream must be
	// in-cluster-shaped (see hostedShape) and keeps the plain in-cluster
	// dial. Copilot carries it, so nothing about its hardening is implicit.
	Internet bool `json:"internet,omitempty"`
	// CAFile, internet upstreams only, is a mounted PEM bundle that
	// replaces the system roots for THIS host — how CI's synthetic
	// upstream presents a test certificate. Absent, the system roots apply.
	CAFile string `json:"ca_file,omitempty"`
}

// AcceptedPath is the one forwarded remainder a CLIENT may post to this
// upstream — ClientPath where the two differ, and Path everywhere else.
// One (method, path) per upstream is still the whole blast radius; this
// says which of the two paths the door is.
func (u Upstream) AcceptedPath() string {
	if u.ClientPath != "" {
		return u.ClientPath
	}
	return u.Path
}

// Translates reports whether a request to this upstream is rewritten on
// the way out and its answer rewritten on the way back. Parse guarantees
// the only pairing this can be true for, so the handler and the
// translator never have to ask which one it is.
func (u Upstream) Translates() bool { return u.ClientPath != "" }

// ToolUpstream is one MCP tool server the gateway may relay to. The
// committed table is the whole egress surface at this layer: the gateway
// forwards nowhere it does not name (cluster-level NetworkPolicy is a
// documented limitation of this table, not built here).
type ToolUpstream struct {
	// URL is the full MCP endpoint (e.g. the in-cluster
	// http://kagent-tools.kagent:8084/mcp).
	URL string `json:"url"`
	// CredentialFile, when set, is a Secret-mounted file holding the
	// tool server's OWN bearer credential — the same proxy-side custody
	// the LLM upstreams use (Upstream.CredentialFile), applied to the
	// tool seam: the gateway injects it, so a tool server can refuse
	// every caller that did not come through the gateway. Read per
	// request, so rotation needs no restart. Empty means the upstream
	// is unauthenticated and requests are forwarded bare.
	CredentialFile string `json:"credential_file,omitempty"`
	// CredentialHeader is the header the credential is injected into.
	// "authorization" (the default) sends "Authorization: Bearer <v>".
	CredentialHeader string `json:"credential_header,omitempty"`
	// Internet and CAFile: exactly as on Upstream. A hosted MCP
	// server is reached only through the hardened dialer; an unmarked
	// entry must be in-cluster-shaped.
	Internet bool   `json:"internet,omitempty"`
	CAFile   string `json:"ca_file,omitempty"`
	// ExtraHeaders are set on every forwarded request to this tool
	// server. Non-secret values only — this is committed config.
	//
	// Why the tool seam needs them: a HOSTED server we did not
	// write decides for itself which tools it offers, and the good ones
	// let a caller narrow that. GitHub's takes X-MCP-Toolsets,
	// X-MCP-Tools and X-MCP-Exclude-Tools; Azure DevOps' takes
	// X-MCP-Toolsets, X-MCP-Tools and X-MCP-Readonly. Setting them
	// narrows the surface BEFORE discovery, so a tool the plane does not
	// want is never offered, never projected onto tools/list, and never
	// reachable even by an approval — which is a stronger guarantee than
	// an allowlist, because it does not depend on the plane's own
	// bookkeeping. The allowlist still applies underneath; this is the
	// outer of the two, not a replacement.
	//
	// Deliberately UNLIKE Upstream.ExtraHeaders on the LLM seam, which
	// is applied after the credential and could therefore overwrite it:
	// here a header naming a credential slot is refused at LOAD (see
	// Load), and the credential is injected last regardless. A committed
	// header must never be able to displace a custody-held credential.
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
	// Tools declares, per tool this server offers, which argument
	// fields are policy-relevant: the fields an approval digest binds and
	// the audit summary is built from. Optional — an undeclared
	// tool's digest binds the whole canonical argument object, which is
	// the brittle case (policy.go, docs/tool-governance.md).
	Tools map[string]ToolPolicy `json:"tools,omitempty"`
}

type Config struct {
	Upstreams map[string]Upstream `json:"upstreams"`
	// ToolUpstreams is the MCP gateway's table. Optional: a config with
	// only LLM upstreams still parses; an absent table relays nothing.
	ToolUpstreams map[string]ToolUpstream `json:"tool_upstreams,omitempty"`
	// StandingConstraints are declarative bounds a credential
	// carries on a tool's declared policy fields: credential -> tool ->
	// rules, ALL of which must hold. A call inside them proceeds with no
	// approval; a call outside them is denied and files a request. Scoped
	// per credential and tool rather than per upstream, so a constrained
	// tool cannot be reached unconstrained through another route.
	StandingConstraints map[string]map[string][]Constraint `json:"standing_constraints,omitempty"`
	// policy is the flattened, validated view of the two declarations
	// above, built by Parse.
	policy PolicySet
}

// Policy is the argument-policy surface the gateway enforces on.
func (c Config) Policy() PolicySet { return c.policy }

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (Config, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if len(c.Upstreams) == 0 {
		return Config{}, fmt.Errorf("config: no upstreams configured")
	}
	for name, u := range c.Upstreams {
		parsed, err := url.Parse(u.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, fmt.Errorf("config: upstream %q: invalid base_url %q", name, u.BaseURL)
		}
		if err := hostedShape(parsed, u.Internet, u.CAFile); err != nil {
			return Config{}, fmt.Errorf("config: upstream %q: %w", name, err)
		}
		if u.Path == "" || strings.HasPrefix(u.Path, "/") {
			return Config{}, fmt.Errorf("config: upstream %q: path must be non-empty with no leading slash", name)
		}
		// The protocol decides where the meter reads token counts. An
		// upstream whose protocol this plane cannot name is refused at
		// LOAD — the plane will not serve a seam it could only forward
		// unmetered. The three refusals below are the whole rule.
		fromPath := PathProtocol(u.Path)
		switch u.Protocol {
		case "":
			if fromPath == "" {
				return Config{}, fmt.Errorf("config: upstream %q: path %q names no protocol this plane knows, "+
					"so declare one: \"protocol\": %s. Without it the meter would have to guess where the token "+
					"counts are, and a wrong guess records zero tokens without saying so",
					name, u.Path, strings.Join(quoteEach(Protocols), " or "))
			}
			u.Protocol = fromPath
		case ProtocolChatCompletions, ProtocolResponses:
			if fromPath != "" && fromPath != u.Protocol {
				return Config{}, fmt.Errorf("config: upstream %q: protocol %q but path %q is %s — "+
					"refused rather than resolved, because whichever is wrong the meter reads the wrong field",
					name, u.Protocol, u.Path, fromPath)
			}
		default:
			return Config{}, fmt.Errorf("config: upstream %q: protocol %q is not one this plane can meter (want %s)",
				name, u.Protocol, strings.Join(quoteEach(Protocols), " or "))
		}
		// The client's half of the seam, when it is a different half.
		// Every refusal here is a declaration nothing could honour: a
		// path naming no protocol, a path naming the one already
		// forwarded, or a pairing this plane has no translator for. The
		// alternative to refusing at load is a seam that accepts a
		// request and forwards it in a shape the endpoint cannot read.
		if u.ClientPath != "" {
			if strings.HasPrefix(u.ClientPath, "/") {
				return Config{}, fmt.Errorf("config: upstream %q: client_path must have no leading slash", name)
			}
			clientProtocol := PathProtocol(u.ClientPath)
			switch {
			case clientProtocol == "":
				return Config{}, fmt.Errorf("config: upstream %q: client_path %q names no protocol this plane knows "+
					"(want a path ending %s)", name, u.ClientPath,
					strings.Join(quoteEach([]string{"chat/completions", "responses"}), " or "))
			case clientProtocol == u.Protocol:
				return Config{}, fmt.Errorf("config: upstream %q: client_path %q and path %q are both %s, so there is "+
					"nothing to translate — omit client_path", name, u.ClientPath, u.Path, u.Protocol)
			case !(clientProtocol == ProtocolResponses && u.Protocol == ProtocolChatCompletions):
				return Config{}, fmt.Errorf("config: upstream %q: this plane translates %s onto %s and no other pairing; "+
					"client_path %q is %s and path %q is %s. Refused rather than forwarded in a shape the endpoint "+
					"cannot read", name, ProtocolResponses, ProtocolChatCompletions,
					u.ClientPath, clientProtocol, u.Path, u.Protocol)
			}
		}
		// A committed extra header must not displace the credential the
		// proxy injects from custody. The model seam sets ExtraHeaders
		// AFTER the credential — the opposite of the gateway's ordering —
		// so on this type the ordering itself is the exposure, and the
		// gateway's own copy of this check has guarded the other seam
		// since it was written. This one is late rather than new: the
		// committed table has carried a keyed model upstream with headers
		// (copilot) the whole time, on the strength of review alone.
		credSlot := u.CredentialHeader
		if credSlot == "" {
			credSlot = "authorization"
		}
		for k := range u.ExtraHeaders {
			if k == "" || !validHeaderName(k) {
				return Config{}, fmt.Errorf("config: upstream %q: invalid extra header name %q", name, k)
			}
			if strings.EqualFold(k, credSlot) || strings.EqualFold(k, "authorization") {
				return Config{}, fmt.Errorf("config: upstream %q: extra header %q would displace the injected credential", name, k)
			}
		}
		c.Upstreams[name] = u
		switch u.Classification {
		case ClassFree:
			if len(u.Prices) > 0 {
				return Config{}, fmt.Errorf("config: upstream %q: free classification cannot carry prices", name)
			}
		case ClassMetered:
		default:
			return Config{}, fmt.Errorf("config: upstream %q: classification must be %q or %q (explicit — never inferred)", name, ClassFree, ClassMetered)
		}
		for model, p := range u.Prices {
			// The $10k/1M-token ceiling is far beyond any real price and
			// keeps pricing.CostCents' int64 math overflow-free for any
			// token count an HTTP response can carry.
			const maxCentsPer1M = 1_000_000
			if p.InCentsPer1M < 0 || p.OutCentsPer1M < 0 ||
				p.InCentsPer1M > maxCentsPer1M || p.OutCentsPer1M > maxCentsPer1M {
				return Config{}, fmt.Errorf("config: upstream %q model %q: price out of range [0, %d]", name, model, maxCentsPer1M)
			}
		}
	}
	for name, t := range c.ToolUpstreams {
		parsed, err := url.Parse(t.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Config{}, fmt.Errorf("config: tool upstream %q: invalid url %q (want absolute http(s))", name, t.URL)
		}
		if err := hostedShape(parsed, t.Internet, t.CAFile); err != nil {
			return Config{}, fmt.Errorf("config: tool upstream %q: %w", name, err)
		}
		// A credential header without a credential file (or the reverse
		// via a bare header name) is a misconfiguration that would fail
		// open in the confusing direction — reject it at load.
		if t.CredentialHeader != "" && t.CredentialFile == "" {
			return Config{}, fmt.Errorf("config: tool upstream %q: credential_header set without credential_file", name)
		}
		if !validHeaderName(t.CredentialHeader) {
			return Config{}, fmt.Errorf("config: tool upstream %q: invalid credential_header %q", name, t.CredentialHeader)
		}
		// A committed extra header must not be able to displace the
		// credential the gateway injects from custody, nor to smuggle a
		// second authorization in. Both are refused at load rather than
		// resolved by ordering, so the refusal is visible at rollout.
		credSlot := t.CredentialHeader
		if credSlot == "" {
			credSlot = "authorization"
		}
		for k := range t.ExtraHeaders {
			if !validHeaderName(k) || k == "" {
				return Config{}, fmt.Errorf("config: tool upstream %q: invalid extra header name %q", name, k)
			}
			if strings.EqualFold(k, credSlot) || strings.EqualFold(k, "authorization") {
				return Config{}, fmt.Errorf("config: tool upstream %q: extra header %q would displace the injected credential", name, k)
			}
		}
	}
	p, err := buildPolicy(c)
	if err != nil {
		return Config{}, err
	}
	c.policy = p
	return c, nil
}

// quoteEach renders a vocabulary for a message, one quoted word each.
func quoteEach(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strconv.Quote(w)
	}
	return out
}

// hostedShape is the load-time half of the egress rule. An
// upstream marked internet must be https on port 443 with no userinfo —
// what the hardened dialer will accept at call time, refused here so the
// mistake is loud at rollout, not at first use. An upstream NOT marked
// internet must look in-cluster: a private IP literal, a bare Service
// name, `service.namespace`, or a name under the cluster suffix. A
// public hostname without the marker (api.githubcopilot.com, say) is
// refused: it would otherwise take the plain in-cluster dial, and the
// hardening of a hosted upstream must never be implicit. ca_file is an
// internet-only field. The other half — every resolved address vetted —
// runs in main at boot (egress.Vet) and on every call.
func hostedShape(u *url.URL, internet bool, caFile string) error {
	host := u.Hostname()
	if internet {
		if u.Scheme != "https" {
			return fmt.Errorf("internet upstream must be https, got %q", u.Scheme)
		}
		if p := u.Port(); p != "" && p != "443" {
			return fmt.Errorf("internet upstream must use port 443, got %q", p)
		}
		if u.User != nil {
			return fmt.Errorf("internet upstream url must not carry userinfo")
		}
		if ip, err := netip.ParseAddr(host); err == nil {
			if why := egress.Refused(ip); why != "" {
				return fmt.Errorf("internet upstream address %s is %s", host, why)
			}
		}
		return nil
	}
	if caFile != "" {
		return fmt.Errorf("ca_file is only meaningful with internet: true")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if egress.Refused(ip) == "" {
			return fmt.Errorf("host %s is a public address; mark the upstream internet: true", host)
		}
		return nil
	}
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if strings.HasSuffix(h, ".svc.cluster.local") || strings.HasSuffix(h, ".svc") || !strings.Contains(h, ".") {
		return nil
	}
	if strings.Count(h, ".") == 1 {
		// `service.namespace` — but `github.com` has the same shape. An
		// in-cluster Service here is plain http; https to a two-label name
		// is what a public host looks like, so it must be marked (or use
		// the full .svc.cluster.local name). The boot-time vet is the
		// second layer: an unmarked name that resolves public is refused.
		if u.Scheme == "https" {
			return fmt.Errorf("host %q over https does not look in-cluster (use the .svc.cluster.local name, or mark the upstream internet: true)", host)
		}
		return nil
	}
	return fmt.Errorf("host %q does not look in-cluster (service, service.namespace, or a .svc.cluster.local name); a hosted upstream must be marked internet: true", host)
}

// InClusterHosts lists the hosts of every UNMARKED upstream, both tables,
// for the boot-time check that none of them resolves to a public address
// (the second layer under hostedShape's static rule).
func (c Config) InClusterHosts() []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		name := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, u := range c.Upstreams {
		if !u.Internet {
			add(u.BaseURL)
		}
	}
	for _, t := range c.ToolUpstreams {
		if !t.Internet {
			add(t.URL)
		}
	}
	return out
}

// InternetHosts lists every hostname the hardened dialer must know, LLM
// and tool upstreams alike, with the trust anchor each configures. The
// same host under two different ca_files is a contradiction the client
// refuses (duplicate host).
func (c Config) InternetHosts() []egress.Host {
	seen := map[string]string{}
	var out []egress.Host
	add := func(raw, caFile string) {
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		name := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		if prev, ok := seen[name]; ok && prev == caFile {
			return
		}
		seen[name] = caFile
		out = append(out, egress.Host{Name: name, CAFile: caFile})
	}
	for _, u := range c.Upstreams {
		if u.Internet {
			add(u.BaseURL, u.CAFile)
		}
	}
	for _, t := range c.ToolUpstreams {
		if t.Internet {
			add(t.URL, t.CAFile)
		}
	}
	return out
}

// toolName bounds an MCP tool name as the admin surface does.
var toolName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// dnsLabel bounds credential names as the admin surface does.
var dnsLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validHeaderName accepts an empty name (the Authorization default) or a
// well-formed RFC 7230 field-name token — the full tchar set, so a legal
// header like "X-Api.Key" is not rejected for being unusual. The value is
// operator-committed, but a malformed name would be silently dropped by
// net/http rather than enforced, so reject it at load.
func validHeaderName(name string) bool {
	if name == "" {
		return true
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			continue
		}
		return false
	}
	return true
}
