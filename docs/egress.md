# Egress enforcement

The governance plane's namespace (`kaimahi`) is default-deny in both
directions. Each pod gets back exactly the connections it needs and
nothing else. The policies live in `k8s/plane/network-policy.yaml` and
deploy with `make plane`, on kind and on a managed cluster alike. There
is no policy engine to run; this is plain Kubernetes NetworkPolicy.

Two things about that sentence matter more than the policies themselves.
A NetworkPolicy is enforced by the cluster's CNI, not by the API server,
and a CNI that ignores it leaves the objects sitting there blocking
nothing, which reads as protection. So this repo ships a probe that
proves the negative, and CI runs it on every PR. And an IP/port rule is
not a URL allowlist. Where a pod is allowed out to the internet, the
section below says exactly what that does and does not constrain.

## Who may talk to whom

| From | To | Port | Why |
|------|----|------|-----|
| agent namespace (`kagent`) | proxy | 8080 | agents on a governed preset call the LLM seam |
| agent namespace (`kagent`) | proxy | 8081 | agents call tools through the MCP gateway; the controller discovers tools through it |
| proxy | Postgres | 5432 | ledger, credentials, budgets, audit |
| proxy | ollama (namespace `ollama`) | 11434 | the keyless upstream |
| proxy | kagent's tool server (`kagent-tools`) | 8084 | the gateway's first upstream |
| proxy | Slack MCP server | 13080 | the gateway's second upstream |
| proxy | the fixture ERP (`kaimahi-erp`) | 8085 | the gateway's in-cluster upstream for the accounts-payable demo. The rule is present whether or not the ERP is deployed; an allowance to a pod that does not exist opens nothing |
| proxy | kagent's **controller** | 8083 | the inbound bridge invokes agents through the controller's A2A endpoint. The controller only — not the agent pods, not the rest of the namespace |
| proxy | CoreDNS | 53 | name resolution for all of the above |
| Slack MCP server | CoreDNS | 53 | to resolve api.slack.com |
| Slack MCP server | public addresses | 443 | Slack's API. See the caveat below |
| proxy | public addresses | 443 | **opt-in only**, for Copilot. See below |
| proxy (the gateway lives in it) | public addresses | 443 | **opt-in only**, for a hosted tool upstream (GitHub's MCP server): `k8s/egress-hosted.yaml`, the same shape as Copilot's, applied by `make github-secret`, removed by `make github-revoke` ([hosted-upstreams.md](hosted-upstreams.md#the-egress-sentence)) |
| internet | inbound edge | 8443 (443 on the load balancer) | **opt-in only**, AKS, `make inbound-expose`: the one internet ingress in the repo ([inbound.md](inbound.md#putting-it-on-the-internet)) |
| inbound edge | proxy | 8082 | the edge forwards Slack events to the bridge; besides CoreDNS, the only in-cluster peer it may reach |
| inbound edge | CoreDNS, public addresses | 53, 443 | to reach Let's Encrypt for its certificate |
| a Prometheus in a namespace called `monitoring` | proxy's ops port | 9092 | **opt-in only**: the rule matches nothing until an operator creates that namespace and runs a pod labelled as Prometheus. There is no auth on the port, so the allowance *is* the access control |
| Azure Managed Prometheus's replica pod (`rsName: ama-metrics` in `kube-system`) | proxy's ops port | 9092 | **opt-in only**, AKS: applied by `kmx lift`'s observability phase from `k8s/observability/network-policy.yaml`, never on kind. The add-on's per-node DaemonSet is deliberately **not** allowed |

The table is this project's own boundary. Pods you deploy yourself are
governed by whatever policies you write for them: if you add a `PodMonitor`
for your own metrics (see [aks.md](aks.md#the-scrape-job-is-a-podmonitor-and-yours-can-sit-beside-it))
and your namespace defaults to deny, the same allowance shape is yours to
write — `rsName: ama-metrics` in `kube-system`, to your metrics port. The
plane's allowance is scoped to the plane's pods and grants yours nothing.

Everything not in the table is denied. In particular:

- Postgres has zero egress, not even DNS. It has nobody to look up.
- Nothing in the pod network can reach the proxy's admin port (9091).
  kmx's admin commands reach it by `kubectl port-forward`, which is
  node-originated traffic (kubelet to pod), and NetworkPolicy governs
  pod traffic. Measured on kind: port-forward into a deny-all pod still
  works. The admin plane keeps its existing gate of cluster credentials
  and then the bearer token.
- The Slack MCP server accepts connections from the proxy only.
  slack-mcp-server v1.3.0 was measured to ignore its API key on the http
  transport, so until now any pod in the cluster could call it directly
  and post as the bot, around the gateway's allowlist and audit. That
  bypass is closed. The only route to Slack is through the gateway.
- Any pod that lands in `kaimahi` without matching one of the shaped
  policies gets nothing, in either direction. The probe checks this.

The ingress rules for the proxy admit the whole `kagent` namespace
rather than named pods. kagent generates the agent Deployments and
their labels; a rule keyed on a label kagent might rename would block
every agent at once. The seam is authenticated by the `kmh_` credential
anyway. The network's job is "only from where agents live".

## Proving it is enforced

```sh
make netpol-verify
```

That runs `scripts/netpol-probe.sh`. It does not check that policies
exist. It creates a few throwaway pods and asserts what each one can
and cannot reach, against a control pod in the unpoliced `default`
namespace that must reach the same targets. Every "blocked" result is
therefore attributable to policy rather than to a dead target or a
cluster without internet.

| Pod | DNS | ollama | Postgres | internet :443 | internet :80 |
|-----|-----|--------|----------|---------------|--------------|
| control (`default`, unpoliced) | ok | ok | blocked | ok | ok |
| unlabeled pod in `kaimahi` | blocked | blocked | blocked | blocked | blocked |
| pod with the proxy's labels | ok | ok | ok | blocked (ok with Copilot) | blocked |
| pod with the Slack server's labels | ok | blocked | blocked | ok | blocked |
| the real Postgres pod (`kubectl exec`) | blocked | blocked | – | blocked | blocked |

The control reaching Postgres is expected to fail: that is the ingress
rule holding from outside the namespace. The unlabeled row is the
enforcement check itself. If your CNI ignores NetworkPolicy that row
reads "ok" across, and the probe stops with:

```
NetworkPolicy is NOT ENFORCED on this cluster: a pod with no allowance reached everything.
```

The proxy is a distroless image with nothing to exec, and CI deploys no
Slack server (no token, no spare CPU), so those two rows use throwaway
pods carrying the same labels the real pods carry. The policies select
on labels, so the rules evaluated are the same rules. The Postgres row
is the real pod, and it carries its own positive: a connect to its own
listener over loopback, which no policy governs, must succeed before
its "blocked" results count. Probe pods run as non-root with no
resource requests, so they schedule even on the full CI node, and they
are deleted when the probe exits.

One assumption to know about on a multi-node cluster: the control pod
and the policed pods may land on different nodes, and the attribution
argument assumes both nodes have the same route out. On a managed
cluster with mixed node pools, or a pool missing its NAT route, a
"blocked" on the policed side could be the node rather than the policy.
Single-node kind cannot hit this, and the verified AKS run below was
also one node, so the caveat has not been exercised anywhere yet; on a
multi-node AKS cluster, pin the probe pods to one node pool if the
result looks surprising.

What was verified where:

- **kind** (kindnetd, which runs kube-network-policies): the probe's
  whole matrix passes on a fresh cluster, and CI runs it on every PR.
  The governed chat, the governed tool call, the gateway denial, the
  approval cycles, and the Slack cycle all run with the policies in
  place, because the policies deploy before any of them.
- **AKS** (Azure CNI Overlay powered by Cilium): the whole matrix
  passes on a real cluster, verified 2026-09-01 and then deleted
  (Kubernetes 1.35.7, Cilium 1.18, one `Standard_B4ms` node,
  `TARGET=aks make netpol-verify`; the full redacted output is in the
  PR that shipped it). The unlabeled pod, which is the enforcement check
  itself, was blocked on every column; the proxy-shaped pod reached 443
  because the Copilot allowance is always applied on that target; a
  governed Copilot chat completed and ledgered through the boundary
  afterwards. **Only because the cluster was provisioned with a policy
  engine.** A bare `az aks create` builds a cluster whose CNI ignores
  NetworkPolicy, which is what the first AKS run here had and what the
  probe was written to catch. `scripts/aks-up.sh` now always passes
  `--network-policy` (Cilium by default, `azure` and `calico` accepted,
  nothing else) and refuses to reuse an existing cluster on a different
  engine, since existing clusters are not migrated. The choice and the
  flags are in [aks.md](aks.md). On this target the probe skips the
  ollama column, since no ollama Service exists there, and says so.

## Copilot: the proxy's opt-in internet allowance

On kind the governed path is in-cluster ollama, and the proxy never
needs to leave the cluster. The plane's own policy does not let it. The
Copilot upstream lives on the internet, so enabling Copilot is what
opens the hole:

```sh
kmx models credential copilot # mints the token, then applies k8s/egress-copilot.yaml
make egress-copilot-off       # closes it again; governed Copilot calls then fail closed
```

`k8s/egress-copilot.yaml` is deliberately outside `k8s/plane/` so that
`make plane` never applies it. In CI and on any keyless kind cluster the
proxy is provably internet-free, and the probe asserts that. After
enabling Copilot, run the probe with `COPILOT_EGRESS=1` so it expects
the proxy to reach 443. `TARGET=aks` defaults to that, since the managed
path is Copilot-only.

## What "allowed out on 443" does and does not mean

The Slack server and (when opted in) the proxy may open TCP connections
to port 443 on any address that is not private. NetworkPolicy has no
hostname selector, so the rule cannot say "api.slack.com". Be precise
about what that buys.

It does guarantee:

- No plaintext ports. Nothing on 80, or anything else.
- No reach back into the cluster. The private ranges are excluded, so
  from the Slack pod the ledger, ollama, the tool server, the agents and
  the proxy's admin port are all unreachable, and the cloud metadata
  endpoint (169.254.169.254) is too.
- Nothing at all for a pod that loses the labels. The allowance is tied
  to the Slack server's labels as kagent generates them; a replacement
  without them gets default-deny.

It does not guarantee the destination. A compromised server image, a
poisoned dependency, or a tool argument the server passes through
unchecked could open a TLS connection to any public host on 443 and
send the workspace token there. What bounds the destination today is
the server's own code plus three non-network layers: the gateway
allowlist (which tools may be called at all), the server's
channel-ID restriction on posting, and the bot's Slack scopes. The same
holds for the proxy under the Copilot allowance, except that the proxy
is this repo's code, forwards to exactly one base URL per upstream, and
refuses redirects on keyed calls.

Closing that residual needs something that understands names or TLS
(an egress gateway or a CNI with FQDN policies). That is a real gap,
not a phrasing problem, and this file will say so until it is closed.

## What is still outside the boundary

- **The agent namespace.** `kagent` is not policed. An agent on an
  ungoverned hosted preset (`make use PRESET=anthropic`) reaches the
  internet directly, key in hand; that path is ungoverned by design
  until governed. Policing `kagent` would also mean policing kagent's
  own controller, UI, database and tool server, which is a separate
  survey.
- **The ollama namespace.** `make model` runs `ollama pull` inside the
  pod, which needs registry.ollama.ai. Left open.
- **New pods' first moments.** On kind, a freshly created pod's first
  one to two seconds of egress are unpoliced: its packets leave before
  the enforcer's pod informer has seen it (measured three times; the
  probe pods sleep past it, and the comment in the script records the
  numbers). The plane's workloads are long-lived, so this affects a pod
  that dials out immediately at start, not the proxy or the ledger. On
  the AKS Cilium run the probe was also run once with the settle wait
  set to zero and every unlabeled-pod check was still blocked; that is
  one run, not a measurement of the window, and the settle stays in.
- **IPv6.** kind is IPv4-only by default and the rules are IPv4. A
  dual-stack cluster needs a matching `::/0`-with-exceptions rule or
  the internet allowances silently do not apply to v6.
- **Adding an upstream.** A new in-cluster entry in
  `k8s/plane/upstreams.yaml` needs a matching egress rule for the proxy,
  pinned to the upstream's pod labels and port. Without one the gateway
  answers 502, which is the boundary doing its job, and the probe will
  not know to check it. A hosted entry (`internet: true`) needs the
  opt-in 443 allowance instead, and goes through the hardened dialer
  ([hosted-upstreams.md](hosted-upstreams.md)).
