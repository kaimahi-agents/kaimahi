<p align="center">
  <img src="brand/hero.png"
       alt="Kaimahi night worker guarding paths for AI agents"
       width="100%">
</p>

# Kaimahi

**Create an Agent locally. Prove it locally. Lift it to Kubernetes.**

`kmx` is Kaimahi's CLI-first developer entry point for agents. Its goal is to
make the path from an idea to a running Agent obvious while keeping the
underlying platform resources visible and reviewable.

Platforms run and govern Agents. `kmx` owns the developer journey around them:
authoring, explicit target selection, submission, inspection and evidence. A
developer should not have to understand platform YAML, adapters, revisions or
deployment receipts before creating something useful.

## The KMX Journey

### Day 0: Two Verbs

The intended Day 0 experience is deliberately small:

```bash
kmx agent create
kmx agent lift
```

`create` should take a developer from an idea to an editable Agent running on a
local target. The loop should be unsurprising: create, edit, run and prove.
`lift` should answer the next question, "it works here; how do I put it in my
environment?", by carrying the same Agent to AKS or another supported target.

This is the north-star interface from
[#194](https://github.com/kaimahi-agents/kaimahi/issues/194#issuecomment-5805880656),
not a claim of current command parity. Today, `kmx agent create` authors an Agent
on a prepared Orka target. A standalone `kmx agent lift` is not implemented; the
working Agent lift is `/lift` inside interactive Orka chat. The next section is
the runnable equivalent of the Day 0 journey on `main`.

## Quickstart

The current end-to-end path is the interactive Orka quickstart. The Orka commands
are on `main`; the latest tagged release, `v0.1.0`, predates them. Install Go
1.26+ and Docker or Podman, ensure the Go binary directory is on `PATH`, then:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx quickstart-wizard
```

The wizard starts local kind, model and Orka setup while the developer describes
the Agent. It generates reviewable YAML, creates the native Orka Provider and
Agent in order, and waits for both to become Ready. Choose Podman explicitly with
`kmx --container-engine podman quickstart-wizard`.

The complete current journey is:

1. Describe the Agent and choose its inference path.
2. At **Next step**, choose **Chat with agent**.
3. Send a prompt and wait for an answer before treating the local run as proved.
4. Enter `/lift`, select an existing Kubernetes or AKS target, review the target
   inference and deployment, then confirm.

Choose a local Orka model or the Agent's Provider when the proof must execute as
an Orka Task. Copilot and host Foundry choices execute through KMX's host
inference path and do not create an Orka Task; their answer proves a different
execution path.

Quickstart is more than a file scaffold. Infrastructure setup begins while the
form is open; it creates result-reader RBAC and installs a read-only Kubernetes
inventory tool with cluster-wide `get`/`list` permissions. Those resources can
exist before the final Agent confirmation, and cancellation leaves completed
setup in place. Review this boundary before starting the wizard.

The current `/lift` flow snapshots the **live** source Agent and Provider, not the
generated YAML file. It removes server-managed metadata, preserves the Agent
specification, checks or offers to install Orka on the destination, selects
target inference and handles its supported Kubernetes tool. Its final review
shows the Agent, source, destination, model, endpoint and create/reuse behavior;
it is not a rendered destination manifest or diff. After confirmation, lift
creates or reuses matching resources, waits for readiness, remembers both
locations and reconnects chat on the target. It does not replay a Task, delete
the source Agent, or clean up completed destination resources after a partial
failure. Choosing new Azure Foundry inference can create billable resources and
runs a test request. Read the [interactive lift contract](docs/interactive-lift.md).

File-only edits are not lifted today. `kmx agent edit` remains kagent-specific,
and `/lift` reads the live Orka resource. Use the native `kubectl edit` path
documented in the [current editor boundary](docs/kmx.md#existing-kagent-editor),
or recreate with a new identity. A Git-first edit and redeploy flow belongs to
the lifecycle direction below.

If no AKS target exists, there is not yet a simple Agent-scoped provisioning
command. Top-level `kmx lift --payload orka` is a separate transitional platform
workflow. It can provision billable AKS and registry resources and runs boundary,
credential, retained plane, Orka, observability and verification phases. It
creates no Provider or Agent. Use `--plan` first and read the
[AKS ownership and teardown contract](docs/aks.md).

`@main` is a moving development branch. Use a reviewed commit when a reproducible
CLI build is required. From a checkout, `make` builds `bin/kmx` without
provisioning anything. See [releases](docs/releases.md) for tagged binaries,
checksums and upgrade limits.

## Current Agent Create

`kmx agent create` is the lower-level authoring command used on a prepared Orka
target. A no-name terminal invocation opens a wizard. Automation supplies the
required values explicitly:

```bash
kmx agent create <name> \
  --namespace <namespace> \
  --provider-type <openai-or-anthropic> \
  --model <model-id> \
  --secret <existing-secret>
```

By default it writes `agents/<name>.yaml`. That review artifact contains a
metadata-only Secret skeleton, a new same-name Provider, an Agent that references
it, and an optional fresh Task. The Secret skeleton is written to the local YAML
as documentation but is never sent to the cluster. The referenced Secret and key
must already exist. Never bulk-apply the complete bundle.

For an online create, `kmx` names and guards the target, validates against the
installed Orka CRDs, refuses Provider, Agent or Task collisions, checks only the
referenced Secret key's presence, and asks the API server to strictly dry-run the
Orka resources. It then writes the artifact and performs:

1. Create Provider and wait for its current generation to become Ready.
2. Create Agent and wait for its current generation to become Ready.
3. If `--task` was supplied, create one fresh Task, wait for success and retrieve
   a nonblank answer.

Without `--task`, success proves Provider and Agent readiness, not a model
response. A live Task also requires an existing `--result-service-account`;
`agent create` creates neither that account nor its RBAC. It never automatically
resubmits the Task. A failure leaves earlier resources in place without rollback
or adoption, so an ordinary rerun can be blocked by those collisions.

Use `--out -` to validate against a pinned offline schema and print YAML,
`--no-apply` to write only the artifact, or `--dry-run` to check the selected
cluster's installed schemas, collisions, Secret-key presence and server admission
without cluster writes or Task execution. `--dry-run` still writes the local
artifact and refuses an output path that already exists.

The command does not install Orka, create a namespace, provision credentials or
RBAC, discover Tool or Skill resources, deploy an application image, translate
kagent YAML, migrate an existing Deployment, or enable the retained model-traffic
bridge. Read the complete [`agent create` contract](docs/kmx.md#kmx-agent-create).

## Day 2 Lifecycle

This section describes the proposed lifecycle in
[#194](https://github.com/kaimahi-agents/kaimahi/issues/194), not commands with
full parity on `main`.

| Simple developer action | Intended lifecycle underneath |
|---|---|
| Create | Build a Git-tracked bundle and render target-native resources |
| Run and prove | Deploy a revision, verify execution and associate evidence with it |
| Lift | Select a target adapter, preserve portable intent and rebind target-specific configuration |
| Inspect | Compare desired, deployed and target-reported state |
| Improve | Evaluate a new immutable revision before promotion |
| Recover | Redeploy and verify an earlier accepted revision |

The direction is for behavior-defining bundle inputs to produce a digest, for
deployments to point to accepted revisions, and for deployment, promotion and
restore operations to produce receipts. Rollback means deploying and verifying
an earlier revision; it cannot undo external actions an Agent already completed.

Built-in target adapters should implement a small lifecycle contract and report
unsupported or unreadable states honestly. Orka is the current reference target.
Multi-target bundle rendering and consistent adapter coverage are proposed work,
not a hidden capability beneath today's Orka-specific `agent create`.

The intended lifecycle names render, deploy, status and evaluate, with verify,
diff and rollback alongside them. Standalone command parity for those operations,
portable bundle digests and deployment receipts is not implemented on `main`.
Initially, Git and the target platform should hold the state; this direction does
not require a new KMX server or controller.

## Platform Boundary

**Orka is the first-class reference target and owns Agent execution,
orchestration and platform governance.** `kmx` is the developer-experience and
lifecycle layer around it, not another runtime or governance control plane.

| Owner | Responsibility |
|---|---|
| Developer and Git | Agent intent, reviewable definitions, environment choices and approvals |
| `kmx` | Guided authoring, explicit targets, validation, submission and observed evidence |
| Orka | Runtime, orchestration, isolation and platform enforcement |
| Future built-in adapter | Map supported lifecycle operations without hiding target differences |

`agent create` leaves its rendered Orka YAML visible. The current interactive
lift reviews a summary and deploys an in-memory bundle; destination YAML and diff
belong to the proposed lifecycle rather than the shipped flow. Remote mutations
name the target and require explicit consent. Generated artifacts contain Secret
references, never credential values. Model backends such as Ollama, Foundry and
Copilot are inference choices; they are separate from the platform target where
an Agent runs.

## Migrate Model Traffic

`kmx migrate` is a separate compatibility bridge for an application already
deployed and managed by its owner:

```bash
kmx plane
kmx migrate <deployment> --namespace <namespace> --model <provider>/<model>
```

The supported application must expose its model base URL through configurable
environment variables and trust the mounted CA. The current committed Orka route
supports non-streaming Responses API translation; it is not a universal bridge
for arbitrary model clients.

Migration governs that application's model traffic through the retained plane.
It does not adopt the Deployment, convert it into an Orka Agent, create Tasks for
its requests, or govern every tool, network connection and inbound event. The
owner reviews and applies the generated workload patch and operates the bridge's
expiring credentials. Existing credentials, caps and accounting remain while
migration paths mature; the bridge is not the KMX product direction. Read the
[migration guide](docs/migrate.md).

## Status

Kaimahi is pre-1.0 and incubating. The repository contains the new Orka path,
legacy kagent commands and the retained model-traffic bridge while the KMX
lifecycle direction is developed.

**Implemented on `main`:**

- Interactive local Orka quickstart and native Orka `agent create`.
- Orka Agent list, dependency inspection and interactive chat.
- Interactive lift of a live Orka Agent to an existing Kubernetes or AKS target.
- Orka install/status and the separate transitional AKS platform workflow.
- Model-traffic migration for the supported application and protocol shape.

**Direction, not shipped command parity:**

- Standalone `kmx agent lift`.
- A portable KMX bundle, deterministic revision digest and target renderers.
- Built-in multi-target lifecycle adapters and capability reporting.
- Revision-bound evaluation, deployment receipts, status, verify, diff and
  rollback.

AKS paths have been demonstrated with short-lived clusters, not continuously
re-proved in CI. Cloud operations can create billable resources. Current
`agent create` emits native Orka Provider, Agent and optional Task resources; it
does not translate kagent YAML, ModelConfigs, MCP wiring or bring-your-own images.
Capabilities should remain in or move to the platform that owns them rather than
growing a second runtime or control plane here.

## Documentation

| Start here | Purpose |
|---|---|
| [Getting started](docs/getting-started.md) | Prerequisites and current local paths |
| [`kmx` reference](docs/kmx.md) | Commands, safety rules and output contracts |
| [Orka](docs/orka.md) | Installation, native Agent creation and first Task |
| [Interactive lift](docs/interactive-lift.md) | Current Agent-to-target lift behavior |
| [AKS](docs/aks.md) | Billable resource ownership, provisioning and teardown |
| [Migration](docs/migrate.md) | Existing-application model-traffic bridge |
| [Direction issue #194](https://github.com/kaimahi-agents/kaimahi/issues/194) | Proposed bundle, adapters and lifecycle model |
| [Documentation index](docs/README.md) | Current paths, legacy references and maintainer material |

## Development

Read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[developer entry-point principles](docs/entry-point-principles.md). Changes land
through pull requests to `main` with checks green and verification actually run.
The project name's cultural and publication boundaries are documented in
[docs/NAMING.md](docs/NAMING.md).
