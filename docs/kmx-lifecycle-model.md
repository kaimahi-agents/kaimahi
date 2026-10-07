# KMX internal lifecycle model

**Status:** Experimental internal RFC evidence

**Depends on:** [KMX lifecycle interface RFC](kmx-lifecycle-interfaces.md)
and the [AgentSuite Artifact Specification](agentsuite-spec.md)

This document describes provisional values and interfaces under
`internal/kmx/lifecycle`. It is not an external Go API, is not wired to the
`kmx` CLI, and makes no compatibility commitment. The current CLI continues to
use `internal/kmx/app` and `internal/kmx/runtime`.

## Package boundary

The experiment has two internal layers:

```text
candidate KMX orchestration
            |
            +-- internal/kmx/lifecycle/model
            |      neutral values, references, requests and receipts
            |
            `-- internal/kmx/lifecycle
                   platform, AgentSuite, OCI and runtime capabilities
                   runtime-native artifacts and receipt factories
```

`model` has a standard-library-only dependency closure. The parent lifecycle
package depends only on `model` and the standard library. Neither package may
depend on Kubernetes, a cloud SDK, a runtime implementation, subprocess or
terminal code.

There is deliberately no `pkg/kmx` package. Public promotion is deferred until
real orchestration and consumers prove which values and workflow groupings are
actually common.

## Provisional workflow areas

The model currently groups operations into environment, AgentSuite shipping and
deployment areas:

| Area | Current experimental operations | Responsibility |
|---|---|---|
| Environment | `Up`, `RecoverUp`, `Register`, `Inspect`, `Down`, `RecoverDown`, `Forget` | Resolve or provision a target and ensure a runtime installation |
| AgentSuite shipping | `Validate`, `Package`, `BuildSandbox`, `Publish`, recovery operations | Validate definitions, construct OCI artifacts and publish them explicitly |
| Deployment | `BuildRevision`, `Lift`, `RecoverLift`, `Status`, `Retire`, `RecoverRetire` | Place an authored revision or sandbox image through a runtime |

These are candidate groupings, not an approved method set. In particular,
evaluation and run/session lifecycle are missing and may add or reorganize
interfaces before anything becomes public.

## Environment setup

The environment model separates target intent from runtime installation:

```text
TargetSpec + EnvironmentMode
              |
              v
resolve or provision TargetRef
              |
              v
ensure RuntimeRef on that exact target
              |
              v
UpResult + scoped receipts
```

`EnvironmentMode` is explicit:

| Mode | Meaning |
|---|---|
| `resolve` | Resolve an existing target and never provision |
| `provision` | Provision without attempting resolution |
| `resolve-or-provision` | Provision only after an established absence |

Authentication, authorization, timeout, malformed-response, throttling and
network errors are not absence. The internal `ResolveTarget` helper propagates
resolver errors unchanged and falls back to provisioning only when
`TargetResolution{Found: false}` is returned successfully.

`UpProgress` preserves partial ownership evidence. If target provisioning
succeeds but runtime setup or later persistence does not complete, recovery can
return the infrastructure receipt without claiming that setup completed.

`Register` records an existing target without claiming infrastructure
ownership. `Forget` removes that local registration and does not mutate the
target.

## Authored source, suite and image

The provisional vocabulary follows the direction in issue #328:

```text
bundle/source = material people author and review
suite         = immutable released AgentSuite artifact
image         = runnable per-agent output derived from a suite
receipt       = evidence about an operation
```

The relationship between today's single-agent bundle, the proposed composable
bundle and AgentSuite remains under discussion in issues #315, #306 and #328.
This internal model must not settle that public source contract by accident.

An `AgentSuiteArtifact` is an immutable OCI definition and is not directly
runnable. `AgentSandboxImage` identifies a runnable OCI image derived for one
suite manifest, agent, platform, build profile, composition and embedded
binding.

Publication changes only the retrieval location. The durable
`AgentSandboxRef` excludes that location and preserves immutable manifest and
suite-binding identities, so moving the same image between registries does not
change deployment identity.

## Deployables

The internal `Deployable` union currently accepts one of:

```text
AgentRevision       exact authored bytes and portable identity
AgentSandboxImage   runnable OCI image with suite binding
```

Its durable `DeployableRef` stores identity without local source bytes or a
mutable image location. Existing serialized field names and discriminator
values remain unchanged while this experiment is moved internal; this rescope
does not introduce a persistence migration.

`TargetBinding` is opaque, versioned input bound to one exact `TargetRef`.
Generic lifecycle code includes its bytes in identity but only the selected
runtime interprets them.

## Runtime artifacts

Runtime-specific translation produces an internal `RuntimeArtifact`:

```text
Deployable + RuntimeRef + TargetBinding
                    |
                    v
             RuntimeArtifact
        rendered documents + identities
                    |
                    v
                deployment
```

`RuntimeArtifact` replaces the earlier experimental name `RuntimeBundle`.
"Bundle" remains available for authored source rather than generated runtime
output.

Each rendered document is either:

- `apply`: eligible for the runtime deployer to write;
- `review`: included for human review but never written.

The artifact carries three separate identities:

| Identity | Covers |
|---|---|
| `BindingDigest` | Exact target-qualified binding input |
| `RenderedDigest` | Ordered bytes of every rendered document, including review-only documents |
| `DeployDigest` | Those ordered bytes plus each document's apply/review disposition |

Changing only a disposition leaves `RenderedDigest` unchanged but changes
`DeployDigest`. A deployer writes only `RuntimeArtifact.DeployDocuments()`.

## Internal capabilities

Southbound interfaces remain narrow so an implementation provides only the
capabilities it actually supports:

| Concern | Internal interfaces |
|---|---|
| Direct revision validation | `RevisionBuilder` |
| Existing or provisioned target | `PlatformResolver`, `PlatformProvisioner`, `PlatformInspector`, `PlatformDeprovisioner` and recovery interfaces |
| AgentSuite validation | `AgentSuiteValidator` |
| Offline OCI construction | `AgentSuiteBuilder`, `AgentSuiteBuildRecoverer` |
| Registry publication | `OCIArtifactPublisher`, `OCIArtifactPublishRecoverer` |
| Runtime installation | `RuntimeInstaller`, `RuntimeInstallRecoverer` |
| Runtime-native construction | `RuntimeBuilder` |
| Deployment | `RuntimeDeployer`, `RuntimeDeployRecoverer` |
| Observation | `RuntimeObserver` |
| Retirement | `RuntimeRetirer`, `RuntimeRetireRecoverer` |

An existing-target implementation can resolve and inspect without gaining
provision/deprovision authority. A create-only runtime can build and deploy
without implementing fake observation or retirement methods.

## Ownership evidence

Receipts remain separated by authority:

| Receipt | Records | Accepted by |
|---|---|---|
| `InfrastructureReceipt` | Platform-owned target infrastructure | Target teardown |
| `RuntimeReceipt` | Runtime installation or reconciliation | Setup recovery evidence |
| `DeploymentReceipt` | Deployable, binding, runtime artifact and deployment identity | Workload retirement |
| `RetirementReceipt` | Established workload retirement | Evidence readers |
| `TeardownReceipt` | Established target teardown | Evidence readers |

Retirement receives deployment evidence and cannot deprovision a target.
Teardown receives infrastructure evidence and cannot claim that individual
workload retirement workflows ran.

Receipts are local evidence, not authentication or cryptographic attestation.
Implementations must verify native identity and ownership against durable
external state before mutation.

## Persistence boundary

Exact-byte and local-path values remain in-process. Durable references and
validated receipts may cross processes:

```text
In process                 Persisted
----------                 ---------
AgentSource                authored source file
AgentRevision              AgentRevisionRef
AgentSuiteInput            AgentSuiteArtifact OCI identity
AgentSuiteArtifact         AgentSandboxImage OCI identity
AgentSandboxImage          AgentSandboxRef
Deployable                 DeployableRef
TargetBinding              binding source file
RuntimeBuildInput          implementation-owned operation record
RuntimeArtifact            DeploymentReceipt identities
```

In-process values reject JSON encoding and decoding rather than silently losing
source bytes, paths or runtime-native data.

## Unknown outcomes

Every remotely mutating request has a caller-known `OperationID` before its
first side effect. If a mutation may have reached a remote system but the result
cannot be established, the implementation returns `OutcomeUnknownError`.

Recovery reads durable operation and native evidence. It must not blindly
repeat an operation that may already have executed. A successful receipt is
returned only after the outcome is established.

## Missing evaluation and session model

The experiment does not yet model evaluation or run/session lifecycle. That is
a known gap, not evidence that these concerns belong outside KMX.

The existing Orka path evaluates a deployed Agent with Tasks. Issue #316
proposes evaluating through AgentSessions with one Session per case. A useful
internal proof must distinguish:

```text
deployment = establish an immutable deployable or registration
evaluation = execute cases and produce revision-bound evidence
run        = one Task or Session execution
```

An AgentSessions-shaped conformance test must cover exact registration identity,
one Session per case, revision and case-set binding, unknown outcomes, status,
and retirement that preserves historical replay. A compile-time interface
assertion is not sufficient.

## CLI relationship

No CLI path uses these packages. The following are conceptual comparisons only:

| Product operation | Internal model area |
|---|---|
| `kmx up` / `kmx down` | Environment setup and teardown |
| `kmx suite validate` | AgentSuite validation |
| `kmx agent lift/status/retire` | Deployment lifecycle |
| `kmx agent evaluate` | Not yet represented by this experiment |

Current command behavior remains defined by the CLI and production runtime
documentation, not by this RFC.

## Evidence required before public promotion

Nothing should move from these internal packages into a public Go package until:

1. Evaluation and its promotion gate have an explicit workflow and capability
   boundary.
2. An AgentSessions-shaped implementation passes meaningful conformance tests.
3. Source, bundle, suite, image and receipt vocabulary aligns with issue #328.
4. The boundary with the proposed bundle/evaluation package in issue #315 is
   explicit and there is no duplicate portable-digest or receipt model.
5. One real Orka vertical slice and an in-memory fake use the same internal
   orchestration.
6. One CLI path and a second real consumer use the same contracts.
7. Cross-process recovery and same-operation/different-input conflict are
   proved.
8. Retirement is proved unable to invoke target teardown.
9. A second real runtime validates every claimed common semantic.

Selective public promotion can then expose only the contracts demonstrated by
those consumers. The number and grouping of public interfaces remain open.
