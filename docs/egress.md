# Legacy reference: network boundaries

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. These are the policies of the
> existing Kaimahi workloads, not Orka's network posture or a cluster-wide
> isolation promise. Current onboarding starts at the [index](README.md).

## Scope and enforcement

[k8s/plane/network-policy.yaml](../k8s/plane/network-policy.yaml) applies
default-deny ingress and egress in namespace `kaimahi`, then adds workload
allowances. **NetworkPolicy is additive and CNI-enforced**: another policy
can widen access, and an installed object proves nothing if the CNI ignores
it. Pod/node identity and Kubernetes API authority are separate boundaries.

The base policy does not isolate the `kagent` or `ollama` namespaces.
A direct-provider agent, or a client connecting around the gateway, is not
made governed by the existence of plane policies. An application migrated
in its own namespace keeps its owner's network responsibilities.

## Connections in the committed policy

| From | To | Port |
|---|---|---|
| `kagent` namespace | proxy's model and MCP seams | 8080, 8081 |
| proxy | Postgres | 5432 |
| proxy | ollama | 11434 |
| proxy | Orka API pods selected in `orka-system` | 8080 |
| proxy | kagent tool server | 8084 |
| proxy | Slack MCP server | 13080 |
| proxy | fixture ERP | 8085 |
| proxy | kagent controller for inbound A2A | 8083 |
| proxy and Slack MCP server | CoreDNS | UDP/TCP 53 |
| Slack MCP server | public addresses excluding listed ranges | TCP 443 |
| Prometheus-labeled pods in `monitoring` | proxy ops listener | 9092 |

Postgres and the fixture ERP have no granted egress. Their ingress admits
the proxy alone on their serving ports. Slack likewise admits only the
proxy. This matters because the [pinned Slack server](slack.md) ignores
its HTTP API key: that key is not a fallback boundary if policy is widened.
The default seam ingress matches a namespace, not a verified agent runtime.

Additional configured allowances are separate objects:

- [Copilot](../k8s/egress-copilot.yaml) and
  [hosted tools](../k8s/egress-hosted.yaml): proxy to public TCP 443.
  They select the same pod; either can keep that port reachable.
- [Inbound edge](../k8s/inbound-edge.yaml): optional public 443 maps to
  edge 8443; edge reaches only bridge 8082, DNS and public 443 for ACME.
  Only its configured Slack route is forwarded, not the other data ports.
- [Managed metrics](../k8s/observability/network-policy.yaml): the selected
  Azure metrics replica in `kube-system` reaches proxy 9092, not every
  metrics DaemonSet pod or another application's metrics port.
- Operator-added tool/model upstream policies use live Service pod selectors
  and container ports. They must be reviewed alongside existing policies.

## Admin and monitoring are different doors

Admin port 9091 has no Service or pod-network ingress allowance. Its normal
path is `kubectl port-forward`, then the admin bearer. Port-forward and
kubelet probes are node-originated; a pod-network deny rule does not revoke
Kubernetes permission to use those paths.

Ops port 9092 has no application authentication. Its allowed scraper
selectors are therefore the access control. It is not exposed by a Service;
a port-forward can still reach it. See [operations](operations.md#metrics).

## Proving it is enforced

The existing probe is [scripts/netpol-probe.sh](../scripts/netpol-probe.sh).
It creates temporary pods, tests denied connections **against allowed
controls**, and cleans them up. A timeout alone could be a dead upstream,
a DNS failure or a node missing its route, not a security boundary.

```sh
bash scripts/netpol-probe.sh
```

Review the script's `KUBECTL` and `COPILOT_EGRESS` settings for your
context and deployed allowances before running it. It tests unlabeled
plane pods, proxy-shaped pods, Slack-shaped pods and the real Postgres pod.
Proxy/Slack stand-ins carry the real labels because NetworkPolicy selects
labels; the probe is not testing their application implementations.

For a newly onboarded server use
[upstream-boundary-probe.sh](../scripts/upstream-boundary-probe.sh), with
the governed successful call as the other direction's positive evidence.
A new upstream entry without matching egress normally fails on connection;
a gateway 502 is not proof that the whole new boundary is correct.

## What public TCP 443 does not guarantee

It is an IP/port allowance, **not a hostname or TLS-content rule**. A
compromised Slack server or proxy can send data to another public host on
443. The proxy's [hardened dialer](hosted-upstreams.md) adds HTTPS, configured
hosts, checked DNS answers and redirect refusal for requests made through
that client. It does not constrain arbitrary code running in the pod.

The IPv4 exceptions block the configured private/link-local ranges, including
metadata destinations, on those public allowances. Separate explicit
in-cluster rules still grant their named paths. The policies do not supply
an egress gateway, FQDN filtering or tool-result redaction.

## Residual limitations

- Policy enforcement must be measured on the actual CNI/configuration.
  The checked-in AKS provisioning path requests an engine; an arbitrary
  existing cluster is not automatically equivalent.
- On kind, a brief startup window before the enforcer sees a new pod was
  observed; probe settling must not be interpreted as first-packet isolation.
- Multi-node probes assume comparable routes. A blocked pod on a node with
  no NAT route can mislead unless the control shares the relevant topology.
- Public allowances are IPv4. Dual-stack needs separately reviewed IPv6
  rules; the current files do not promise matching IPv6 reach or policy.
- Selectors and destination ports can drift with workloads; retest after
  changing an upstream, controller chart, labels or networking engine.

Historical one-cluster matrices are removed rather than presented as
current proof for every deployment. No policy or runtime code changes in
this documentation retirement.
