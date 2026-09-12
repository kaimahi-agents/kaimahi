# Kaimahi documentation

**Orka is the platform. Kaimahi is tooling to help people get agents onto
it.** Start with the installation, native create and migration paths below, not the
legacy governance-plane demonstrations.

## Current operator paths

| I want to… | Read |
|---|---|
| Set up the current development CLI and understand prerequisites | [Getting started](getting-started.md) |
| Install Orka, inspect before applying, or see the version actually running | [Orka](orka.md) |
| Author a native Provider + Agent, optionally run a Task and retrieve its answer | [Native create](orka.md#author-an-orka-agent-and-get-an-answer), [CLI contract](kmx.md#kmx-agent-create) |
| Route an existing application's model traffic through Orka | [Migration](migrate.md) |
| Use an existing AKS cluster or provision a disposable one | [AKS](aks.md) |
| Find a command, its safety contract, and supported output modes | [kmx reference](kmx.md) |
| Install a tagged release or understand version and upgrade limits | [Releases](releases.md) |
| Diagnose installation, routing, or existing runtime problems | [FAQ](FAQ.md) |

Installing Orka is not migration. A migrated application's Deployment stays
under its owner's management; the supported migration governs **model
traffic**, not all activity by the application.

Authoring is a separate, **open** decision: native Orka only versus also
supporting kagent YAML as an authoring surface over Orka. The native
recommendation in [orka.md](orka.md) is not a ruling. `kmx agent create`
currently authors native Orka resources, not a conversion. Existing kagent
commands/manifests and the isolated conversion spike are not evidence of a
supported translation layer.

## References for legacy code still present

These documents distinguish the surviving model seam and direct kagent wiring
from retirement pointers. They are not Orka documentation or a platform roadmap.
The plane has three listeners (model 8080, admin 9091, ops 9092); the MCP gateway,
tool/argument-bound approvals, workflows and connector fixtures are removed.
Budget approvals remain admin-operated. Historical tool/inbound requests are
readable and deniable, not approvable; their grants are inactive. Existing
installations need the [retirement upgrade steps](operations.md#upgrading-after-gateway-retirement).
All twelve SQL migrations and stored data are retained.

| Area | Reference |
|---|---|
| kagent model presets and credential wiring | [Models](models.md) |
| Existing MCP tools agent | [Tools](tools.md) |
| Plane model proxy, metering and budget limits | [Spend](spend.md) |
| Retired gateway and tool onboarding | [Tool governance](tool-governance.md), [govern your agent](govern-your-agent.md), [foreign runtime](foreign-runtime.md) |
| Budget approvals and inactive historical tool/inbound grants | [Approvals](approvals.md) |
| Attribution and expiring credentials | [Identity](identity.md) |
| Plane NetworkPolicy and residual exposure | [Egress](egress.md) |
| Hosted model dialing and credential custody | [Hosted upstreams](hosted-upstreams.md) |
| Plane operations, database recovery and metrics | [Operations](operations.md) |
| Existing isolation mechanisms and limits | [Isolation](isolation.md) |
| Retired blueprint runner | [Workflows](workflows.md) |
| Retired webhook bridge and public-edge cleanup | [Inbound](inbound.md) |
| Retired Slack posting fixture | [Slack](slack.md) |
| Retired release driver | [Release agent](release-agent.md) |
| Retired gateway scenarios and current demo pointers | [Demo](demo.md), [accounts payable](ap-demo.md) |

The [legacy plane diagram](assets/architecture.svg), with
[Mermaid source](assets/architecture.mmd), is a **historical pre-retirement**
view, including the removed gateway and inbound paths. It is not the current
listener inventory, Orka architecture or the project's future shape.

## Maintainer references

- [Development](development.md): source boundaries, verification and operational traps.
- [Repository map](repository-map.md): where the retained files belong.
- [Coordination](COORDINATION.md): mission, binding rulings, lane status and upstream filings.
- [Entry-point principles](entry-point-principles.md): delegation, reviewable artifacts and ownership.
- [CLI presentation](cli-ux-plan.md): current terminal and automation contracts.
- [Charm boundary](charm-ux-followup-plan.md): the implemented creation wizard and its limits.
- [Naming](NAMING.md): the name and publication constraints.

## Assessments that inform current work

- [Orka composition](reviews/2026-09-09-orka-composition.md): version-qualified
  findings that informed migration; not a current ownership or authoring ruling.
- [Substrate evaluation boundary](reviews/2026-09-10-substrate-evaluation.md):
  scoped research, not a platform substitution decision.

Superseded lane prompts, old platform proposals and retired review snapshots
are not maintained as public documentation. Git retains their history.
