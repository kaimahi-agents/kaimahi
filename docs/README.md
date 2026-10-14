# Kaimahi documentation

**KMX is the Agent Builder CLI. Runtimes execute and govern agents.** Start with
the first-answer or suite-workspace path below. Native Orka and exact-v0.10.3 Go
kagent workspaces can be evaluated, published, then lifted by packaged digest;
the PortableAgent bundle guide documents the legacy deployment path. Use the
runtime contract when extending or comparing implementations.

## Current operator paths

| I want to… | Read |
|---|---|
| Set up the current development CLI and understand prerequisites | [Getting started](getting-started.md) |
| Inspect compiled runtime support and qualification evidence with `kmx targets`; understand runtime and inference boundaries | [Runtime contract and support matrix](runtime-adapters.md) |
| Install Orka, inspect before applying, or see the version actually running | [Orka](orka.md) |
| Create live resources and bundles: `kmx create --live` for Orka, or add `--runtime kagent` for preinstalled exact v0.10.2 | [Native Orka create](orka.md#author-an-orka-agent-and-get-an-answer), [CLI contract](kmx.md#kmx-create) |
| Define or validate a portable OCI AgentSuite with bundled or remote ToolProviders and standalone provider sandbox images | [AgentSuite Artifact Specification](../agentsuite/spec.md) |
| Author one native suite member offline, evaluate the Orka or exact kagent profile, publish the exact OCI graph, and lift that packaged digest | [Suite workspace](suite-workspace.md) |
| Discover models and configure native inference | [Models](models.md), [Copilot inference](copilot-inference.md), [Foundry inference](local-foundry-inference.md) |
| Chat with an existing Orka Agent | [Interactive chat](interactive-chat.md) |
| Operate the legacy PortableAgent lift/status/evaluate/verify/retire path | [Legacy bundle lifecycle](agent-lift.md) |
| Use the current bundle-backed lift from chat or console | [Interactive lift](interactive-lift.md) |
| Use an existing AKS cluster or provision a disposable one | [AKS](aks.md) |
| Find a command, its safety contract, and supported output modes | [kmx reference](kmx.md) |
| Install a tagged release or understand version and upgrade limits | [Releases](releases.md) |
| Diagnose installation, inference, or runtime problems | [FAQ](FAQ.md) |

Default create authors offline workspace source; explicit `--live` selects
resource/bundle creation and its previews. Native Orka remains the recommendation
in [orka.md](orka.md). Bounded kagent profiles target preinstalled exact v0.10.2
for create and exact v0.10.3 for declarative legacy bundle
lift/status/evaluation plus packaged workspace authoring, `eval --bindings`, and
evaluated publication and create-only digest Lift. Neither is a YAML translation
over Orka or a restoration of the former broad command surface. For kagent,
standalone run/chat, packaged status/retirement, updates and image placement
remain unsupported.
Existing retired commands/manifests and the removed conversion spike are not
evidence of one.

## Maintainer references

- [Development](development.md): source boundaries, verification and operational traps.
- [Codebase overview](codebase-overview.md): architecture, package responsibilities, core data flows and safety invariants.
- [Interface baseline](interface-baseline.md): exact current command tree, workspace/live split, runtime limits and alpha Go surfaces.
- [Repository map](repository-map.md): native tooling, packaging, compatibility data and test scaffolding.
- [Entry-point principles](entry-point-principles.md): delegation, reviewable artifacts and ownership.
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
