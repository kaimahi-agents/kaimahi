# Kaimahi documentation

**KMX is the Agent Builder CLI. Runtimes execute and govern agents.** Start with
the current creation and lift paths below; use the runtime contract when extending
or comparing implementations.

## Current operator paths

| I want to… | Read |
|---|---|
| Set up the current development CLI and understand prerequisites | [Getting started](getting-started.md) |
| Inspect compiled runtime support and qualification evidence with `kmx targets`; understand runtime and inference boundaries | [Runtime contract and support matrix](runtime-adapters.md) |
| Install Orka, inspect before applying, or see the version actually running | [Orka](orka.md) |
| Author an agent: default native Orka, or explicit create for preinstalled exact Kagent v0.10.2 | [Native Orka create](orka.md#author-an-orka-agent-and-get-an-answer), [CLI contract](kmx.md#kmx-agent-create) |
| Define or validate a portable OCI AgentSuite with bundled or remote ToolProviders and standalone provider sandbox images | [AgentSuite Artifact Specification](agentsuite-spec.md) |
| Discover models and configure native inference | [Models](models.md), [Copilot inference](copilot-inference.md), [Foundry inference](local-foundry-inference.md) |
| Chat with an existing Orka Agent | [Interactive chat](interactive-chat.md) |
| Lift, inspect, run, evaluate, verify or retire a portable bundle | [Bundle lifecycle](agent-lift.md) |
| Lift from chat or console | [Interactive lift](interactive-lift.md) |
| Use an existing AKS cluster or provision a disposable one | [AKS](aks.md) |
| Find a command, its safety contract, and supported output modes | [kmx reference](kmx.md) |
| Install a tagged release or understand version and upgrade limits | [Releases](releases.md) |
| Diagnose installation, inference, or runtime problems | [FAQ](FAQ.md) |

Native Orka authoring remains the default and the recommendation in
[orka.md](orka.md). The explicit Kagent v0.10.2 create adapter is a direct,
create-only target for a preinstalled runtime, not a YAML translation over Orka
and not a restoration of the former broad command surface. Whether that narrow
adapter should ever become a general cross-runtime authoring or lifecycle
surface remains open and unsupported. Existing retired commands/manifests and
the removed conversion spike are not evidence of one.

## Maintainer references

- [Development](development.md): source boundaries, verification and operational traps.
- [Repository map](repository-map.md): where the retained files belong, including dormant legacy scaffolding.
- [Entry-point principles](entry-point-principles.md): delegation, reviewable artifacts and ownership.
- [CLI presentation](cli-ux-plan.md): current terminal and automation contracts.
- [Command conventions](command-conventions.md): naming, flag, output and compatibility rules for kmx commands.
- [Charm boundary](charm-ux-followup-plan.md): the implemented creation wizard and its limits.
- [Interactive agent TUI](interactive-agent-tui-plan.md): two-environment overview, keyboard navigation, slash-command completion and demo mode.
- [KMX lifecycle interface decision](kmx-lifecycle-interfaces.md): rationale for the experimental target, platform, runtime, deployment and receipt boundaries.
- [KMX application API](kmx-application-api.md): how another Go layer uses AgentEnvironment, AgentSuites and AgentDeployments while management ports remain internal.
- [KMX public interface summary](kmx-public-interface.md): the current layering, `kmx up` equivalent, recovery, teardown and CLI-to-Go mapping.
- [Naming](NAMING.md): the name and publication constraints.

## Assessments that inform current work

- [Full chat latency profile](chat-performance-profile.md): local, AKS/Foundry
  and Copilot measurements and optimization priorities.
- [Local Foundry inference](local-foundry-inference.md): interactive host inference
  with Azure login, and the separate native Orka integration proposal.
- [Orka composition](reviews/2026-09-09-orka-composition.md): version-qualified
  historical findings; not a current ownership or authoring ruling.
- [Substrate evaluation boundary](reviews/2026-09-10-substrate-evaluation.md):
  scoped research, not a platform substitution decision.

Superseded lane prompts, old platform proposals and retired review snapshots
are not maintained as public documentation. Git retains their history.
