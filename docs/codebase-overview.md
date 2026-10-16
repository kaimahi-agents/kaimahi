# Codebase overview

Kaimahi is an experimental Agent Builder and lifecycle CLI for Kubernetes. It
authors portable content, prepares targets, invokes supported runtimes and records
evidence; it is not itself an agent runtime or a second control plane. Runtimes
and harnesses own execution, enforcement, model access and retention.

This guide explains how current source fits together. Use the
[repository map](repository-map.md) for exact tracked-file inventory, the
[`kmx` reference](kmx.md) for command behavior, and the
[runtime support matrix](runtime-adapters.md#revision-pinned-support-and-qualification-matrix)
for qualified scope.

## System shape

The repository contains one Go module and one installed binary:

```text
cmd/kmx
   |
   v
internal/kmx/app ----------------------------+
   |                                         |
   +-> runtime, scaffold, guard, config       +-> external tools and APIs
   +-> agentsuite/workspace + oras            |   kind, kubectl, Helm, OCI,
   +-> agentsessions + runview                |   Kubernetes and Azure
   +-> lift, portforward, toolchain            |
                                             |
external suite     public portable format   |
pkg/kmx/            alpha Go contracts       |
internal/kmx/lifecycle experimental SPI -----+
```

There is no nested plane module. Plane administration, SQL, image helpers, model
overlays and migration commands are removed; historical source remains in Git
history. Residual plane-named ownership fields exist only so old cloud records
can still be torn down conservatively.

## Main packages

| Area | Responsibility |
|---|---|
| `cmd/kmx/` | Cobra command tree, argument and flag validation, help/completion, stream contracts and lazy dependency construction |
| `internal/kmx/app/` | Production orchestration and composition root: setup, runtime operations, AgentSuite workflows, receipts, terminal UI and external tools |
| `github.com/agent-primitives/suite` (external) | Public AgentSuite object model, canonical JSON, schemas, OCI/content safety, validation and semantic diff; imported as `agentsuite` |
| `internal/kmx/agentsuite/workspace/` | Closed one-member native workspace authoring, discovery, source snapshotting, preparation and packaged loading |
| `internal/kmx/agentsuite/oras/` | Deterministic OCI packing, layouts, registry transfer, provenance and bounded exact-digest artifact reads |
| `internal/kmx/agentsuite/agentkit/` | Experimental AgentKit/buildx adapter; not yet a conformant full-suite builder |
| `internal/kmx/runtime/` | Runtime-neutral contract used by the installed CLI: portable agent parsing, render/deploy/status/evaluate, sessions, identities and evidence |
| `internal/kmx/lifecycle/` | Experimental deployment SPI supporting the alpha `pkg/kmx` design; not the installed command path |
| `pkg/kmx/` | Alpha, standard-library-only Go contracts: setup-only `AgentEnvironment`, image-backed `AgentSuites`, and `AgentDeployments`; no concrete implementation or CLI wiring |
| `internal/kmx/runview/` | Bounded, redacted in-memory run projection; not durable history or attestation |
| `internal/kmx/guard/` | Context posture and named mutation confirmation; unknown or unreadable state fails closed |
| `internal/kmx/config/` | Context/config precedence and KMX state/cache paths |
| `internal/kmx/toolchain/`, `run/`, `portforward/` | Pinned tool acquisition, subprocess execution and owned loopback forwards |
| `internal/kmx/lift/` | Cloud-independent AKS planning and historical ownership records |
| `k8s/`, `scripts/`, `embed.go` | Embedded native assets plus checkout-only hygiene, release and qualification tooling |

## Product objects

These names describe different things and should not be used interchangeably:

| Object | Identity and role |
|---|---|
| Workspace | Mutable authored source rooted by `workspace.yaml`; normally Git-backed |
| AgentSuite graph | Canonical `agentsuite.json` and its digest-bound records |
| AgentSuite artifact | Immutable OCI manifest plus content layer; its manifest digest also binds KMX native metadata. Authored cases are public evaluation cases bound through each member |
| Environment | Logical setup identity in the experimental Go API; not an exact target, runtime or context |
| Context | Kubeconfig access selector; never durable cluster identity |
| Target | Exact runtime, physical destination and namespace used by an operation |
| Deployment | Runtime placement of one exact source on one exact target |
| Run | One Task, Session or equivalent invocation; not another deployment |
| Receipt | Local evidence about observed identities and outcomes; unsigned and not authentication |

Inference selection is independent from runtime and target selection.

## Core flows

### Local environment and first answer

```text
kmx local up
  -> kind cluster
  -> Ollama
  -> configured model
  -> pinned Orka runtime and keyless Provider

kmx quickstart
  -> the same local setup
  -> fixed live Agent
  -> one fresh Task
  -> readable answer
```

Bare `local up` creates no Agent. The setup-only `pkg/kmx.AgentEnvironment`
contract maps conceptually to this operation but is not wired to it. Quickstart
adds live creation and execution and therefore is not only environment setup.

### Workspace create, prove and publish; packaged-digest lift

```text
kmx create NAME
  -> editable one-member workspace

workspace.Prepare
  -> frozen selected source
  -> public AgentSuite graph
  -> public evaluation index and cases, plus private native profile
  -> validated private OCI layout in KMX state

kmx eval
  -> exact prepared suite
  -> observed target, runtime and runtime-specific model binding
  -> Orka Provider/Agent or kagent ModelConfig/Agent placement
  -> one fresh, never-retried Orka or blocking A2A Task per ordered case
  -> private receipt only after a full pass

kmx push [--evaluation RECEIPT] REF
  -> exact prepared OCI graph copied to layout or registry
  -> optional receipt coherence check
  -> optional unsigned provenance on registry publication

kmx lift REF@sha256:DIGEST --to-context CTX \
  --evidence RECEIPT (--inference provider:NAME | --bindings FILE)
  -> source-free bounded retrieval of that exact OCI graph
  -> evidence and packaged identity revalidation
  -> GET-only plan or guarded create/exact reuse on an explicit target
```

The public graph digest, authored-source fingerprint and OCI manifest digest are
different identities. Publication uses the OCI manifest digest because it also
binds supplemental native metadata; cases are bound by both. Push never deploys.

Native Orka workspace evaluation continuously qualifies one fixed two-case
scenario. Exact-v0.10.3 Go kagent workspace evaluation, runtime-specific passing
evidence, evaluated publication and same-runtime receipt comparison are also
implemented and qualified. Root digest Lift supports both packaged profiles;
kagent packaged status and retirement are not implemented.

### Native digest Lift

Root digest Lift is available in v0.7.0.
Root `kmx lift` and `App.PrepareReleaseLift` ingest one exact registry or read-only OCI-layout
digest, validate the packaged native suite, require a matching private passing
receipt, observe the selected target and inspect desired resources with GETs.
Preparation returns an immutable identity-only plan. `--plan` stops there,
without a mutation guard, admission, writes, Tasks or placement records.

Apply revalidates the artifact, evidence, prerequisites and owned-resource proof,
then guards the target, preflights both absent creates and consumes the plan once.
Orka creates absent resources or exactly reuses receipt-bound resources; kagent
creates absent ModelConfig/Agent resources only and refuses any occupied primary
or generated child. Neither path updates, adopts, retries ambiguous writes, rolls
back or cleans up. Partial outcomes retain acknowledged identities. `--promote`
explicitly permits an eligible different destination with the same exact packaged
identity and pinned runtime profile; Orka requires a Ready `openai` Provider,
while kagent retains the packaged model declaration and exact runtime inventories.
Evidence proves the source only.
Created resources need fresh evaluation before later receipt-bound reuse.
Bounded CI qualifies Orka exact registry/local-layout reuse, registry promotion
into an absent pair on a second owned cluster, and kagent fresh-target
creation/occupied refusal. The Orka promotion scenario shares a real model
endpoint and separately checks a fresh Task; source evidence remains source-only.
Partial CLI outcomes are not established by these fixed scenarios.

### Retired PortableAgent operations

User `agent.yaml` readers/writers/receipts, `bundle` commands, live create,
run bundle mode and `--bundles` flags are removed. Old receipts are unreadable;
no conversion/migration or saved-file/resource deletion occurs. Suite-owned
objects retain legacy invocation/adoption/copy refusals. Public AgentSuite/
ToolProvider implementation-bundle graphs/build/pull and alpha Go interfaces
remain. Independent live Orka run/chat and Task/result reads remain, without
claiming suite-workspace run/chat or suite status/retirement.

## Safety invariants

- Every Kubernetes operation is pinned to an explicit resolved context.
- A context name is access, not proof of physical cluster identity.
- Absent, present, unreadable and unknown are distinct states.
- Portable, rendered, binding, deployment and artifact identity domains remain
  separate. Digest identities are computed from exact bytes; opaque deployment
  IDs are validated but are not presented as content digests.
- Credential-specific inputs accept references, never values. Authored artifacts,
  receipts and diagnostics retain references or key presence only; operators
  must likewise keep credentials out of general prompt/task arguments.
- Effectful Tasks and A2A messages are never automatically retried after an
  ambiguous result.
- Ready does not mean answered; answered does not mean evaluated.
- Workload retirement grants no environment or cloud teardown authority.
- Failed multi-resource placement is not automatically rolled back.
- Receipts and provenance are evidence, not signatures or authentication.

## Testing and CI

The root module is tested with `go test ./...`. Repository hygiene additionally
checks formatting, vet, staticcheck, documentation links, secret shapes,
retired commands, repository-map claims and deliberate checker mutations.

Required live shards prove bounded scenarios rather than universal support:

- quickstart from installation to a real answer;
- Orka runtime, Kubernetes Tool and the fixed native workspace qualifier;
- stock Orka Helm integration;
- exact kagent v0.10.3 raw upstream Tool smoke/direct single A2A MCP proof, separately packaged
  two-case workspace evaluation/evaluated publication, and source-free digest
  Lift into a fresh target with occupied-repeat refusal.

The `e2e-hello-world` job fail-closed aggregates the four retained shards:
kagent Lift, Orka runtime, stock Orka Helm and quickstart. Final retirement Go
build/live checks are required before declaring new runtime qualification. A green shard qualifies its
fixed versions and scenario; it is not a production-readiness claim.

## Where to start

1. Read [development](development.md) for build and verification commands.
2. Read the [interface baseline](interface-baseline.md) before changing commands
   or public Go contracts.
3. Use the [repository map](repository-map.md) before changing a file found by
   search; fixtures and historical compatibility records are not current entry
   points.
4. Follow the contribution rules in [CONTRIBUTING.md](../CONTRIBUTING.md),
   [command conventions](command-conventions.md), and [AGENTS.md](../AGENTS.md)
   for ownership, vocabulary and safety.
