# KMX lifecycle interface RFC

**Status:** Experimental internal design evidence

**Scope:** Provisional workflow values in `internal/kmx/lifecycle/model` and
implementation capabilities in `internal/kmx/lifecycle`. These packages are not
wired to the CLI, do not replace `internal/kmx/runtime`, and establish no public
Go API or compatibility commitment.

**Companion reference:** [KMX internal lifecycle model](kmx-lifecycle-model.md)
records the current values, capabilities, persistence boundary and promotion
criteria.

## Context

KMX needs understandable lifecycle operations across more than one runtime
without coupling every workflow to Kubernetes resources, cloud APIs,
subprocesses or terminal behavior.

The production code already demonstrates important pieces:

- strict authored agent documents and exact portable identity;
- target-bound rendering with a separate rendered identity;
- Orka lift, status, evaluation, retirement and local receipts;
- one narrow exact-version create-only alternate-runtime integration;
- a draft AgentSuite format and strict offline validator;
- explicit target pinning and ownership checks.

It also shows unresolved boundaries. Evaluation is central to promotion but does
not fit only deployment. AgentSessions performs work through Sessions rather
than a long-lived Agent. The authored bundle, released AgentSuite and runnable
image vocabulary is still converging. A wider lifecycle model should be proved
against those cases before becoming public.

## Provisional model

The experiment distinguishes definition, release, destination and placement:

```text
authored bundle/source -> AgentRevision ----------------+
                                                        |
resolved definition -> AgentSuiteArtifact               |
                       -> AgentSandboxImage -------------+-> Deployment
                                                        |
TargetSpec -> TargetRef -> RuntimeRef -------------------+
```

| Concept | Responsibility |
|---|---|
| Authored revision | Exact human-authored bytes and their portable identity |
| AgentSuite Artifact | Immutable OCI definition of agents, tools, compositions and build profiles |
| Agent Sandbox Image | Runnable OCI image derived for one suite agent and platform |
| Target | Durable destination identity supplied or resolved by a platform |
| Runtime installation | Runtime identity on one exact target |
| Deployment | One deployable placed through a runtime on a target |

This does not settle whether today's single-agent bundle or the composable
bundle proposed in issue #306 is the long-term authored source format. Issue
#315 is discussing which source, evaluation and receipt semantics should become
a shared package. AgentSuite remains a released definition, not local target
state or deployment evidence.

## Naming direction

The provisional vocabulary follows issue #328:

```text
source or bundle = what people author and review
suite            = immutable released AgentSuite artifact
image            = runnable per-agent output
receipt          = evidence about an operation
```

Generated runtime output is therefore a `RuntimeArtifact`, not a
`RuntimeBundle`. An extracted AgentSuite directory or OCI layout is an
`AgentSuiteInput`, not authored source. These names remain internal and may
change as the source-to-suite flow is proved.

## Internal layering

```text
future CLI / UI / controller orchestration
                    |
                    v
     internal/kmx/lifecycle/model
 neutral values, identities, requests, receipts
                    |
                    v
        internal/kmx/lifecycle
 platform / suite / OCI / runtime capabilities
                    |
                    v
          concrete implementations
```

The model package contains implementation-neutral values only. The parent
package owns southbound capabilities and runtime-native intermediate artifacts.
No application service or composition root is implemented by this RFC.

## Candidate workflow areas

Environment, AgentSuite shipping and deployment are useful workflow groupings,
but their current internal interfaces are not frozen. Evaluation and run/session
lifecycle may add or reorganize those groupings.

| Scope | Establish | Remove or retain |
|---|---|---|
| Environment | Resolve or provision target, install runtime | Teardown only with infrastructure evidence; otherwise forget registration |
| AgentSuite release | Validate, package, build image, publish | Registry retention policy |
| Deployment | Build runtime artifact and place it | Retire only with deployment evidence |
| Evaluation | Execute cases and produce revision-bound evidence | Evidence retention policy; design pending |
| Run/session | Invoke an established deployable | Cancellation and history semantics; design pending |

`Up` is a candidate product workflow that composes target setup with runtime
installation. Its intent is explicit: resolve, provision, or resolve then
provision only after established absence. Resolver errors never become absence.

Lift is also workflow orchestration rather than one runtime primitive:

```text
resolve target and runtime installation
    -> validate deployable and binding
    -> construct runtime-native artifact
    -> deploy or reconcile
    -> persist deployment evidence
```

The internal runtime interfaces expose installation, build, deployment,
observation and optional retirement as separate capabilities. A create-only
runtime does not implement fake status or retirement methods.

## Evaluation and AgentSessions

The current model omits evaluation, which is an explicit gap. Production KMX
already evaluates Orka deployments and uses revision-bound receipts as promotion
gates. Issue #316 proposes AgentSessions as another evaluation target, with one
Session per case and replayable evidence.

The working distinction is:

```text
deployment = establish an immutable deployable or registration
evaluation = execute cases and produce revision-bound evidence
run        = one Task or Session execution
```

For AgentSessions, the hypothesis to validate is:

```text
deployment = immutable harness/image registration
run        = Session execution
evaluation = one Session per case
status     = exact registration remains available
retire     = prevent new use while preserving replay evidence
```

A compile-time fake does not establish those semantics. Public promotion
requires meaningful conformance over identity, case execution, unknown
outcomes, status and safe retirement.

## Ownership and safety

### Receipts preserve authority boundaries

Receipts are separated by responsibility:

| Receipt | Records | Accepted by |
|---|---|---|
| `InfrastructureReceipt` | Platform-owned target infrastructure | Target teardown |
| `RuntimeReceipt` | Runtime installation or reconciliation | Setup recovery evidence |
| `DeploymentReceipt` | Deployable, binding, runtime artifact and deployment identity | Workload retirement |
| `RetirementReceipt` | Established workload retirement | Evidence readers |
| `TeardownReceipt` | Established target teardown | Evidence readers |

Neither destructive capability receives a second independently supplied target
or deployment that could disagree with its receipt. Retirement cannot invoke
platform deprovisioning. Target teardown does not fabricate workload-retirement
evidence.

Receipts are local evidence, not authentication. Before mutation,
implementations must verify receipt identity and native ownership against
durable external state.

### Identity is computed

The model computes domain-separated SHA-256 identities for exact authored
bytes, target-qualified bindings and runtime-native artifacts. Callers cannot
attach arbitrary digest strings to in-process values.

Rendered and deployed identity remain distinct:

- `RenderedDigest` covers all ordered rendered documents, including review-only
  documents;
- `DeployDigest` additionally commits to each document's apply/review
  disposition.

Publication location is not part of sandbox-image deployment identity.

### Absence and unreadability are distinct

An absent target or deployment can be a successful observation. Authentication,
authorization, timeout, malformed response, throttling and network failure are
errors and must never be reported as absence.

### Unknown outcomes require recovery

Every remotely mutating operation has a caller-known `OperationID` before its
first side effect. An operation that may have changed remote state but whose
result cannot be established returns `OutcomeUnknownError`. Recovery reads
durable evidence instead of blindly repeating a non-idempotent mutation.

## Internal architecture rules

Architecture tests enforce:

- `internal/kmx/lifecycle/model` depends only on the standard library;
- the model does not import the parent lifecycle package or concrete
  implementations;
- `internal/kmx/lifecycle` depends only on the model and standard library;
- known runtime, Kubernetes, cloud, subprocess and terminal names do not leak
  into shared model fields;
- destructive capabilities keep infrastructure and deployment receipt scopes
  separate.

The tests deliberately do not freeze an exact number of workflow interfaces or
their complete method sets.

## Why this remains internal

There is no production service, constructor or external consumer for this model.
Publishing it now would freeze names and groupings before evaluation,
AgentSessions and source/evidence ownership are settled.

The alternative of an intentionally unstable public alpha package remains
possible, but it offers little consumer value without an implementation and
still creates coordination cost. The current decision is to keep the experiment
internal and promote only proven contracts later.

## Evidence required before public promotion

1. Define evaluation and its promotion-gate relationship to deployment.
2. Prove an AgentSessions-shaped implementation with meaningful conformance.
3. Align bundle/source, suite, image and artifact vocabulary with issue #328.
4. Resolve the source, digest, evaluation-receipt and gate boundary discussed in
   issue #315 without duplicate implementations.
5. Adapt one real Orka vertical slice and an in-memory fake.
6. Route one CLI path and a second real consumer through the same orchestration.
7. Prove cross-process recovery and operation-input conflict behavior.
8. Prove retirement cannot invoke target teardown.
9. Validate every claimed common semantic against another real runtime shape.

Only demonstrated contracts should be selectively promoted. Public interface
count, grouping and package placement remain open.

## Consequences

Benefits:

- ownership and recovery invariants can be exercised without publishing an SDK;
- names and workflow grouping can change while evaluation and AgentSessions are
  explored;
- source/evaluation work in issue #315 can establish its own proven boundary;
- runtime differences remain explicit capabilities instead of unsupported
  stubs.

Costs:

- another internal lifecycle experiment temporarily overlaps production
  `internal/kmx/runtime`;
- real adapters and stores still need a composition layer;
- later public promotion may require moving and renaming types;
- internal interfaces must not become permanent merely because they compile.

## Maturity path

```text
internal RFC and model
    -> Orka vertical slice and in-memory fake
    -> evaluation and AgentSessions semantics
    -> durable recovery across processes
    -> second real runtime and second consumer
    -> selective public promotion, if still justified
```
