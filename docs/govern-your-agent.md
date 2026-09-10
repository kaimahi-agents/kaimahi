# Bring-your-own seams — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. The standalone governance-plane
> onboarding tutorial is retired, while its code and CLI references remain.
> Start with the [documentation index](README.md) and [migration](migrate.md).
> A migrated application keeps its owner-managed Deployment; governed model
> traffic does not mean tool governance or transfer of workload ownership.

## Existing tool-server onboarding

`kmx tools add` still scaffolds a keyless in-cluster streamable-HTTP MCP
upstream. See [tools_commands.go](../cmd/kmx/tools_commands.go),
[toolsadd.go](../internal/kmx/app/toolsadd.go) and
[upstream_yaml.go](../internal/kmx/scaffold/upstream_yaml.go).

It reads the live Service selector and target port, validates the candidate
table against the running plane, then emits an overlay fragment, proxy
egress, server ingress and a RemoteMCPServer. A selectorless Service is
refused: write a reviewed policy pair against the actual server pods instead
of inventing labels from the URL. Named target ports require `--pod-port`.

NetworkPolicy must select the **server's pods and container port**, not
just its Service port. Server ingress admits the proxy; default
`--server-egress none` grants no egress, `dns` adds DNS, and `keep` leaves
server egress to its owner. All policies are additive; inspect the others
and use [upstream-boundary-probe.sh](../scripts/upstream-boundary-probe.sh)
with an allowed control before treating this as a boundary.

## Policy choices are not inferred

`--tool name:a,b` declares the top-level fields an approval binds.
`--tool name:` declares **no relevant arguments** and permits any arguments
under the same grant; `--tool name:*` leaves the tool undeclared and binds
the whole canonical argument object. A bare name is refused. See
[argument policy](tool-governance.md#declaring-what-arguments-mean) before
choosing. [Standing constraints](approvals.md#standing-constraints-the-calls-that-need-no-approval)
bind only named fields and take precedence over the static allowlist.

`kmx tools govern` issues the credential, sets the allowlist, waits for
seam acceptance and repoints an existing kagent agent. It does not apply
a previously unapplied custom seam file. Without the kagent CRD it skips
kagent objects and publishes client guidance and the CA instead; see
[foreign runtime](foreign-runtime.md). Discovery is a projection, not a
way to make an agent call a tool it cannot see.

## Persistence and limits

The shared `kaimahi-upstreams-extra` overlay survives plane redeployment;
colliding names are refused. `--out -` prints without applying;
`--no-apply` writes without applying. Both still need the live Service and
plane validation. A saved manifest carries the overlay resource version:
scaffold again after a conflict. Manual multi-document `kubectl apply`
is not transactional and can leave policies applied after a ConfigMap fails.

Overlays refuse `credential_file`, `credential_header`, `internet`,
`ca_file` and `extra_headers`. Keyed/hosted upstreams require reviewed
committed configuration and credential mounts, not an overlay escape hatch.
See [hosted upstreams](hosted-upstreams.md) and
[overlay.go](../plane/internal/config/overlay.go).

Allowlists are per credential/tool name, **not per upstream**: a same-named
tool on another server can become callable without changing the list.
Tool results are not filtered or redacted. The model onboarding equivalent,
`kmx models add`, has no per-credential upstream allowlist; its protocol,
classification and budget limits remain in [spend](spend.md#adding-a-model-upstream).
