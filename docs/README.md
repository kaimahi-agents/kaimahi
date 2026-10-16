# Kaimahi documentation

**KMX is the Agent Builder CLI. Runtimes execute and govern agents.** Start with
the first-answer or suite-workspace path below. Native Orka and exact-v0.10.3 Go
kagent workspaces can be evaluated, published, then lifted by packaged digest;
legacy user bundles/readers/receipts and live create are removed outright. Use the
runtime contract when extending or comparing implementations.

> [!NOTE]
> The [browser documentation](https://kaimahi-agents.github.io/kaimahi/)
> renders `main` and may
> include unreleased behavior. Tagged Markdown is the stable historical record;
> see the [documentation-site version boundary](docs-site.md#version-boundary).

## Current operator paths

| I want to… | Read |
|---|---|
| Set up the current development CLI and understand prerequisites | [Getting started](getting-started.md) |
| Inspect compiled runtime support and qualification evidence with `kmx targets`; understand runtime and inference boundaries | [Runtime contract and support matrix](runtime-adapters.md) |
| Install Orka, inspect before applying, or see the version actually running | [Orka](orka.md) |
| Define or validate a portable OCI AgentSuite with bundled or remote ToolProviders and standalone provider sandbox images | [AgentSuite Artifact Specification](https://github.com/agent-primitives/suite/blob/main/spec.md) |
| Resolve canonical deny-write policy bytes and a digest offline, without enforcement | [Portable policy](portable-policy.md) |
| Author one native suite member offline, evaluate the Orka or exact kagent profile, publish the exact OCI graph, and lift that packaged digest | [Suite workspace](suite-workspace.md) |
| Inspect deployed agents, per-object Lift provenance and readiness without writes | [Destination status](destination-status.md) |
| Discover models and configure native inference | [Models](models.md), [Copilot inference](copilot-inference.md), [Foundry inference](local-foundry-inference.md) |
| Chat with an existing Orka Agent | [Interactive chat](interactive-chat.md) |
| Use an existing AKS cluster or provision a disposable one | [AKS](aks.md) |
| Find a command, its safety contract, and supported output modes | [kmx reference](kmx.md) |
| Install a tagged release or understand version and upgrade limits | [Releases](releases.md) |
| Diagnose installation, inference, or runtime problems | [FAQ](FAQ.md) |

Default create authors offline source. Exact-v0.10.3 Go/linux-amd64 kagent
supports packaged eval/evaluated publication/create-only digest Lift; Orka remains
default. No user bundle conversion/migration or saved-file/resource deletion
occurs. Old receipts are unreadable. Destination status is read-only placement
inspection, not legacy bundle status. Retirement, kagent standalone run/chat,
Python and RemoteMCP execution remain unavailable. Public AgentSuite/ToolProvider
implementation-bundle formats, build/pull and extracted-directory transfer remain.
See [format boundaries](bundle-format.md) and [current CLI contracts](kmx.md).

## Maintainer references

- [Development](development.md): source boundaries, verification and operational traps.
- [Documentation site](docs-site.md): one-command local preview, source-of-truth, versioning and publication boundaries.
- [Codebase overview](codebase-overview.md): architecture, package responsibilities, core data flows and safety invariants.
- [Interface baseline](interface-baseline.md): exact current command tree, workspace/live split, runtime limits and alpha Go surfaces.
- [Repository map](repository-map.md): native tooling, packaging, compatibility data and test scaffolding.
- [CLI presentation](cli-ux-plan.md): current terminal and automation contracts.
- [Command alignment](command-semantics.md): implemented entry points, compatibility policy and remaining vocabulary decisions.
- [Command conventions](command-conventions.md): naming, flag, output and compatibility rules for kmx commands.
- [Charm boundary](charm-ux-followup-plan.md): the implemented creation wizard and its limits.
- [Interactive agent TUI](interactive-agent-tui-plan.md): two-environment overview, keyboard navigation, slash-command completion and demo mode.
- [Suite-first lifecycle direction](suite-first-lifecycle.md): workspace, release, deployment and evaluation objects; partly implemented, not an operator recipe.
- [KMX lifecycle interface decision](kmx-lifecycle-interfaces.md): rationale for the experimental target, platform, runtime, deployment and receipt boundaries.
- [KMX application API](kmx-application-api.md): how another Go layer uses AgentEnvironment, AgentSuites and AgentDeployments while management ports remain internal.
- [KMX public interface summary](kmx-public-interface.md): the current layering, setup-only environment contract and CLI-to-Go mapping; environment recovery and teardown remain deferred.
- [Naming](NAMING.md): the name and publication constraints.

## Assessments that inform current work

- [Full chat latency profile](chat-performance-profile.md): local, AKS/Foundry
  and Copilot measurements and optimization priorities.
- [Local Foundry inference](local-foundry-inference.md): interactive host inference
  with Azure login, and the separate native Orka integration proposal.

Maintained documentation provides current guidance.
For superseded material, consult Git history.
