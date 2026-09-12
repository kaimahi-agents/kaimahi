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
A direct-provider agent or direct MCP client is not made governed by the
existence of plane policies. An application migrated
in its own namespace keeps its owner's network responsibilities.

## Connections in the committed policy

| From | To | Port |
|---|---|---|
| `kagent` namespace | proxy's model seam | 8080 |
| proxy | Postgres | 5432 |
| proxy | ollama | 11434 |
| proxy | Orka API pods selected in `orka-system` | 8080 |
| proxy | CoreDNS | UDP/TCP 53 |
| Prometheus-labeled pods in `monitoring` | proxy ops listener | 9092 |

Postgres has no granted egress; its ingress admits the proxy alone on 5432.
The default model-seam ingress matches a namespace, not a verified agent runtime.
The gateway, Slack and ERP fixture allowances are removed from the shipped
policy; existing clusters still require deliberate cleanup.

Additional configured allowances are separate objects:

- [Copilot](../k8s/egress-copilot.yaml): proxy to public TCP 443.
  Old hosted-tool policies selected the same pod; if left behind, they can
  independently keep that port reachable.
- [Managed metrics](../k8s/observability/network-policy.yaml): the selected
  Azure metrics replica in `kube-system` reaches proxy 9092, not every
  metrics DaemonSet pod or another application's metrics port.
- Operator-added model upstream policies use live Service pod selectors and
  container ports. Review them alongside existing policies and any retired
  tool-generated allowances.

Gateway/tool, inbound A2A and public-edge allowances are retired. **Apply does
not prune omitted objects**; upgraded installations must follow the
[retirement cleanup](operations.md#upgrading-after-gateway-retirement), not infer
absence of old reach or exposure from the new policy files.

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
context and deployed allowances before running it. Its stand-ins use workload
labels because NetworkPolicy selects labels; it does not test the application
implementation or an arbitrary owner's tool boundary.

For a newly onboarded model server, test actual traffic plus denied connections
against an allowed control. The old tool-upstream probe is retired. A model
upstream entry without matching egress normally fails on connection; a 502 is
not proof that the whole boundary is correct.

## What public TCP 443 does not guarantee

It is an IP/port allowance, **not a hostname or TLS-content rule**. A
compromised proxy can send data to another public host on
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
current proof for every deployment. Retest the remaining boundaries after
retirement; old probe results do not verify the new deployment.
