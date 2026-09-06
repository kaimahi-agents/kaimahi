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
	"time"

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

type Upstream struct {
	// BaseURL is the upstream origin plus any path prefix it expects.
	BaseURL string `json:"base_url"`
	// Path is the single allowed forwarded remainder (no leading slash) —
	// exactly what kagent's OpenAI client appends to the governed preset's
	// baseUrl (e.g. "v1/chat/completions").
	Path           string `json:"path"`
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

// Inbound authentication modes. Every mode binds the caller to the hook's
// configured credential; they differ in how the caller PROVES it.
const (
	// AuthBearer: the caller presents the credential's own kmh_ token as
	// a bearer (for sources that can set a header but cannot sign).
	AuthBearer = "bearer"
	// AuthKaimahiHMAC: Kaimahi's generic signed-webhook scheme — HMAC-SHA256
	// over "v1:<timestamp>:<delivery-id>:<body>" with a shared signing
	// secret (Secret-mounted, plane-side only). Preferred over a bearer:
	// the secret never travels, the body is bound, and the timestamp plus
	// signed delivery id give replay protection.
	AuthKaimahiHMAC = "kaimahi-hmac"
	// AuthSlack: Slack's request signing for the Events API —
	// "v0:<timestamp>:<body>" under the app's signing secret.
	AuthSlack = "slack"
)

// InboundHook is one webhook the plane accepts: who may fire it (a
// credential and a proof mode), what it triggers (a kagent agent, invoked
// through the controller's A2A endpoint), whose budget the resulting
// spend lands in, and its ingress bounds. The committed table is the
// whole inbound surface: the plane accepts nothing it does not name.
type InboundHook struct {
	// Credential is the plane credential this hook is bound to: the
	// identity that is granted ('inbound' grants, subject = hook
	// name) and audited. A bearer caller must present THIS credential's
	// token; an HMAC caller proves it via the signing secret.
	Credential string `json:"credential"`
	// Auth is one of the Auth* modes above.
	Auth string `json:"auth"`
	// SigningSecretFile, required for the HMAC modes, is a Secret-mounted
	// file holding the shared signing secret; read per request so
	// rotation needs no restart. Unreadable at request time fails the
	// hook closed (503) — never open.
	SigningSecretFile string `json:"signing_secret_file,omitempty"`
	// AgentNamespace/Agent name the kagent Agent the event triggers.
	AgentNamespace string `json:"agent_namespace"`
	Agent          string `json:"agent"`
	// SlackChannelsFile (slack auth only) names a Secret-mounted file
	// listing the channel IDs whose mentions may trigger this hook —
	// comma- or newline-separated, read per request. The committed table
	// names the FILE because a channel ID is a workspace identifier this
	// public repo never carries; the demo mounts the same Secret key
	// that restricts where the Slack MCP server may post
	// (SLACK_MCP_ADD_MESSAGE_TOOL), so "the private test channel" has one
	// source of truth for both directions. Unreadable, empty, or a
	// server-side "anywhere" value fails the hook closed (503). Required
	// for slack auth: a hook without it would trigger from any room the
	// app is mentioned in, and an ingress that widens because a key was
	// dropped from the table is the silent failure this file refuses.
	SlackChannelsFile string `json:"slack_channels_file,omitempty"`
	// SlackApproversFile (slack auth only) names a Secret-mounted
	// file listing the Slack USER ids who may approve or deny an approval
	// request by mentioning the bot (`@kaimahi approve <id> …`), comma-
	// or newline-separated, read per request. Channel membership alone
	// is not authority: the room is where the demo lives, the list
	// is who may decide. Like the channel file it names a FILE because a
	// user id is a workspace identifier this public repo never carries.
	// Unreadable or empty fails a COMMAND closed (503) and leaves every
	// other mention exactly as it was. Required for slack auth: a hook
	// that would treat "approve" as a question when the list went
	// missing is the quiet failure this refuses.
	SlackApproversFile string `json:"slack_approvers_file,omitempty"`
	// SlackDefaultUses / SlackDefaultTTL bound a Slack approval that names
	// no bounds (`@kaimahi approve <id>` alone). Defaults: one use, 15
	// minutes — the tightest grant that still lets the retried action
	// through, because an approval typed into a chat is the least
	// deliberate form an approval takes, and a wider default would make
	// the tightest grant the one nobody types. Explicit uses=/ttl= win;
	// the store still refuses a grant with no bound at all.
	SlackDefaultUses int    `json:"slack_default_uses,omitempty"`
	SlackDefaultTTL  string `json:"slack_default_ttl,omitempty"`
	// BudgetCredential is the governed credential the triggered agent
	// spends under (its governed preset's credential). An event is
	// refused at the door when that budget is already exhausted, so the
	// agent is not invoked only to be denied at the proxy.
	BudgetCredential string `json:"budget_credential"`
	// ToolCredential, when set, is the credential the triggered agent's
	// TOOL calls carry (its governed MCP gateway credential) — usually a
	// different one from BudgetCredential, which is what it spends
	// MODEL tokens under. Identity on the call needs both: a run is
	// opened per credential the agent authenticates with, or half the
	// turn would reach the audit trail unattributed. Empty means the
	// agent calls no tools.
	ToolCredential string `json:"tool_credential,omitempty"`
	// MaxBodyBytes bounds the event payload (default 64 KiB).
	MaxBodyBytes int64 `json:"max_body_bytes,omitempty"`
	// RatePerMinute/Burst size the per-hook token bucket (defaults 60/10).
	RatePerMinute int `json:"rate_per_minute,omitempty"`
	Burst         int `json:"burst,omitempty"`
}

const (
	DefaultInboundMaxBody = 64 << 10
	DefaultInboundRate    = 60
	DefaultInboundBurst   = 10
	maxInboundBody        = 4 << 20
	maxInboundRate        = 100_000
	DefaultSlackUses      = 1
	DefaultSlackTTL       = "15m"
	maxSlackUses          = 1_000_000
)

// ApprovalNotifier is how the plane tells a human that a request
// is waiting: a post into the pinned Slack channel, THROUGH the plane's
// own MCP gateway under the plane's OWN credential — so custody, the
// allowlist, the channel pin and the tool audit apply to the plane's
// message exactly as they apply to an agent's. The credential is
// configuration (the plane is the trust root), issued like an agent's
// and allowlisted to the one posting tool.
type ApprovalNotifier struct {
	// ToolUpstream names the tool_upstreams entry to post through (the
	// Slack MCP server); Tool is the posting tool on it.
	ToolUpstream string `json:"tool_upstream"`
	Tool         string `json:"tool"`
	// CredentialFile is the Secret-mounted file holding the plane's own
	// kmh_ token for the gateway, read per post. Unreadable: the post is
	// not attempted (recorded, and `make approvals` still lists the
	// request) — a notification is never worth a fail-open.
	CredentialFile string `json:"credential_file"`
	// ChannelFile is the channel to post into: the same Secret key that
	// pins the MCP server's posting tool and bounds the inbound hook
	// (SLACK_MCP_ADD_MESSAGE_TOOL), so all three agree by construction.
	ChannelFile string `json:"channel_file"`
}

type Config struct {
	Upstreams map[string]Upstream `json:"upstreams"`
	// ToolUpstreams is the MCP gateway's table. Optional: a config with
	// only LLM upstreams still parses; an absent table relays nothing.
	ToolUpstreams map[string]ToolUpstream `json:"tool_upstreams,omitempty"`
	// InboundHooks is the inbound bridge's table. Optional: absent
	// means the inbound listener accepts nothing.
	InboundHooks map[string]InboundHook `json:"inbound_hooks,omitempty"`
	// ApprovalNotifier is optional: absent means nobody is told
	// when a request is filed, exactly as before.
	ApprovalNotifier *ApprovalNotifier `json:"approval_notifier,omitempty"`
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
	for name, h := range c.InboundHooks {
		if !dnsLabel.MatchString(name) {
			return Config{}, fmt.Errorf("config: inbound hook %q: name must be a lowercase DNS label", name)
		}
		if h.ToolCredential != "" && !dnsLabel.MatchString(h.ToolCredential) {
			return Config{}, fmt.Errorf("config: inbound hook %q: tool_credential must be a lowercase DNS label", name)
		}
		if !dnsLabel.MatchString(h.Credential) || !dnsLabel.MatchString(h.BudgetCredential) {
			return Config{}, fmt.Errorf("config: inbound hook %q: credential and budget_credential must be lowercase DNS labels", name)
		}
		if !dnsLabel.MatchString(h.Agent) || !dnsLabel.MatchString(h.AgentNamespace) {
			return Config{}, fmt.Errorf("config: inbound hook %q: agent and agent_namespace must be lowercase DNS labels", name)
		}
		if h.Auth != AuthSlack && (h.SlackChannelsFile != "" || h.SlackApproversFile != "" ||
			h.SlackDefaultUses != 0 || h.SlackDefaultTTL != "") {
			return Config{}, fmt.Errorf("config: inbound hook %q: slack_* fields are meaningless with %s auth", name, h.Auth)
		}
		if h.SlackDefaultUses < 0 || h.SlackDefaultUses > maxSlackUses {
			return Config{}, fmt.Errorf("config: inbound hook %q: slack_default_uses out of range [0, %d]", name, maxSlackUses)
		}
		if h.SlackDefaultTTL != "" {
			if _, err := ParseTTL(h.SlackDefaultTTL); err != nil {
				return Config{}, fmt.Errorf("config: inbound hook %q: slack_default_ttl: %v", name, err)
			}
		}
		switch h.Auth {
		case AuthBearer:
			if h.SigningSecretFile != "" {
				return Config{}, fmt.Errorf("config: inbound hook %q: signing_secret_file is meaningless with bearer auth", name)
			}
		case AuthKaimahiHMAC, AuthSlack:
			// A signed hook with nothing to sign against would have to
			// fail closed on every request; refuse the config instead so
			// the mistake is loud at rollout, not silent at first event.
			if h.SigningSecretFile == "" {
				return Config{}, fmt.Errorf("config: inbound hook %q: %s auth requires signing_secret_file", name, h.Auth)
			}
			if h.Auth == AuthSlack && h.SlackChannelsFile == "" {
				return Config{}, fmt.Errorf("config: inbound hook %q: slack auth requires slack_channels_file (the channel(s) the hook may be triggered from)", name)
			}
			if h.Auth == AuthSlack && h.SlackApproversFile == "" {
				return Config{}, fmt.Errorf("config: inbound hook %q: slack auth requires slack_approvers_file (who may approve from Slack; an empty file means nobody)", name)
			}
		default:
			return Config{}, fmt.Errorf("config: inbound hook %q: auth must be %q, %q or %q", name, AuthBearer, AuthKaimahiHMAC, AuthSlack)
		}
		if h.MaxBodyBytes < 0 || h.MaxBodyBytes > maxInboundBody {
			return Config{}, fmt.Errorf("config: inbound hook %q: max_body_bytes out of range [0, %d]", name, maxInboundBody)
		}
		if h.RatePerMinute < 0 || h.RatePerMinute > maxInboundRate || h.Burst < 0 || h.Burst > maxInboundRate {
			return Config{}, fmt.Errorf("config: inbound hook %q: rate_per_minute/burst out of range [0, %d]", name, maxInboundRate)
		}
	}
	if n := c.ApprovalNotifier; n != nil {
		// The notifier posts through a tool upstream the table names —
		// nowhere else — with a credential and a channel it reads from
		// files. Every field is required: a half-configured notifier
		// would fail at the first filing, silently, in a goroutine.
		if _, ok := c.ToolUpstreams[n.ToolUpstream]; !ok {
			return Config{}, fmt.Errorf("config: approval_notifier: tool_upstream %q is not in tool_upstreams", n.ToolUpstream)
		}
		if !toolName.MatchString(n.Tool) {
			return Config{}, fmt.Errorf("config: approval_notifier: invalid tool %q", n.Tool)
		}
		if n.CredentialFile == "" || n.ChannelFile == "" {
			return Config{}, fmt.Errorf("config: approval_notifier: credential_file and channel_file are required")
		}
	}
	p, err := buildPolicy(c)
	if err != nil {
		return Config{}, err
	}
	c.policy = p
	return c, nil
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

// ParseTTL parses a grant lifetime the way the CLI does: an integer with
// an optional s/m/h/d suffix (bare = seconds). Bounded to a month: a
// longer grant is a config change in disguise (the admin surface's rule).
func ParseTTL(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty ttl")
	}
	unit := time.Second
	switch s[len(s)-1] {
	case 's':
		s = s[:len(s)-1]
	case 'm':
		unit, s = time.Minute, s[:len(s)-1]
	case 'h':
		unit, s = time.Hour, s[:len(s)-1]
	case 'd':
		unit, s = 24*time.Hour, s[:len(s)-1]
	}
	if s == "" || len(s) > 9 {
		return 0, fmt.Errorf("want e.g. 90, 90s, 5m, 2h, 1d")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("want e.g. 90, 90s, 5m, 2h, 1d")
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	// Bound BEFORE multiplying: 999999999d overflows int64 nanoseconds
	// and could wrap back into range.
	if n < 1 || n > int64(MaxTTL/unit) {
		return 0, fmt.Errorf("ttl out of range [1s, 30d]")
	}
	return time.Duration(n) * unit, nil
}

// MaxTTL is the longest grant any path mints.
const MaxTTL = 30 * 24 * time.Hour

// dnsLabel is the shape shared by credential names (the admin surface's
// rule), Kubernetes object names, and hook names: interpolated into
// URLs, audit rows and grant subjects, so keep it to a plain identifier.
var dnsLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Bounded returns the hook with its defaults applied.
func (h InboundHook) Bounded() InboundHook {
	if h.MaxBodyBytes == 0 {
		h.MaxBodyBytes = DefaultInboundMaxBody
	}
	if h.RatePerMinute == 0 {
		h.RatePerMinute = DefaultInboundRate
	}
	if h.Burst == 0 {
		h.Burst = DefaultInboundBurst
	}
	if h.SlackDefaultUses == 0 {
		h.SlackDefaultUses = DefaultSlackUses
	}
	if h.SlackDefaultTTL == "" {
		h.SlackDefaultTTL = DefaultSlackTTL
	}
	return h
}

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
