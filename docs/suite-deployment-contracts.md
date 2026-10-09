# Full-suite deployment contracts

Status: internal contract proposal with an experimental image-build and suite-lift
implementation. See [the executable workflow](agentsuite-image-lift.md).

The current update adds capability-based inference with deployment-mounted
configuration, a local source workspace and shared CLI/TUI publication, lift,
status and image-backed invocation. ACR pulls and two-model image reuse were
exercised on kind and AKS. Open deployment-contract questions and existing issue
owners are tracked in [#356](https://github.com/kaimahi-agents/kaimahi/issues/356).
The local image association/placement records are experimental implementation
data, not the proposed portable association schema or durable operation API.

Container registry operations remain OCI/vendor-neutral. ACR is a tested registry
integration, with Azure-specific publisher login and workload pull-access setup
above the common transport. See the
[registry strategy boundary](agentsuite-image-lift.md#container-registry-integration);
provider setup ownership remains with #223, not the suite or deployment identity.

Sequencing: this branch is rebased onto main through #340 and includes the merged
#339 buildx backend, #357 cancellation/harness checks and #360 archive inspection,
SBOM/provenance and distinct runnable-image/index identities. Image publication
preserves the attestation-bearing graph; deployment uses the platform digest.
The implementation uses a narrow inline execution profile and experimental image
declaration; the five broader extensions below remain proposed follow-up design.

## Implemented scope

- `suite build --suite-ref` verifies the published source and emits image metadata.
- `suite push-image` publishes the built OCI archive as a runnable image.
- `lift <suite-reference> --environment <file>` resolves every member image,
  prepares and inspects the complete suite, and deploys through the standalone
  Kubernetes HTTP adapter on kind or AKS. Single-image input is also supported.
- A reproducible live smoke verifies an authenticated response and repeated
  deployment preserving resource UIDs. It passed on kind and private ACR/AKS.

The richer `suitedeploy` contracts in this document remain review scaffolding;
the working orchestration is `runtime.SuiteDeploymentAdapter` plus `imagelift`.
Reconciliation of these with the merged lifecycle APIs remains open. The working
path does not implement native agent-platform registration, ToolProviders,
delegation, persistent release history or evaluation promotion gates. It must not
be used as a substitute for a governed bundle promotion workflow.

## Decision

Use one suite lifecycle above runtime-specific deployment adapters. Both local
kind and remote AKS select a target and invoke the same adapter `Deploy` action.
Lift is the user-facing lifecycle verb; deployment is its runtime effect.

```text
AgentSuite directory / registry reference
  -> Resolver: complete validated suite + OCI/logical identities
  -> Builder: one exact member/platform implementation (#339)
  -> member image set + execution contracts
  -> target bindings + observed runtime installation
  -> Adapter.Plan: every member, shared resource and declared relationship
  -> evaluation gate + exact-plan review
  -> Adapter.Deploy
  -> aggregate receipt and per-member/resource outcomes
  -> Adapter.Observe / runtime executions
```

`internal/kmx/suitedeploy` is an experimental contract proposal. It supplies
types, interfaces, plan snapshots, topology/compatibility checks and tests. It
does not implement resolvers, builders, runtime adapters, orchestration, image
metadata publication, CLI commands or console actions. Its Go values are
in-process contracts, not a stable persistence format. No new CRD is required.

## Handoff to the image builder

The executable prototype adds optional inline `BuildProfile.execution` and
closed execution/image-deployment schemas. `agentsuite.Suite` remains unchanged.
The `suitedeploy.Suite` and `Execution` types here are richer proposed handoff
values, not the prototype's wire schema. The
[proposed AgentSuite extensions](#proposed-agentsuite-extensions-after-339) below
specify that work for review; their JSON examples are not accepted wire formats
in the current validator.

#339's `SandboxPlan` and `SandboxBuilder` remain the starting implementation.
The proposed `BuildOutput` extends its archive result with:

- the actual runnable platform-image descriptor and a separate retrieval location;
- source-suite OCI descriptor, exact agent, platform, composition and build profile;
- execution metadata and its descriptor;
- the existing AgentSuite sandbox-binding descriptor;
- explicit experimental/conformant status and diagnostics.

The archive is a transport output; its checksum is not the image manifest digest.
`Suite.LogicalDigest` hashes canonical `agentsuite.json`; the OCI suite manifest
digest identifies the packaged artifact. Neither substitutes for the other.

The builder selects an execution contract through its build profile and emits
matching metadata. `Execution` separates configuration/harness ABI, supported
agent-serving modes, lifecycle, named runtime inputs and required capabilities.
The mode protocol is not the inference protocol. AgentKit's protocol selector
must be mapped by a qualified adapter, not passed as an arbitrary environment
override. Fixed model URLs remain fixed until the harness supports runtime binding.

The image metadata schema, media type and placement must be agreed with #339.
This proposal does not add a competing image label or modify the current
AgentSuite wire format. Experimental #339 images are not automatically accepted
as conformant; completing and qualifying its output remains an integration gate.
Image-to-suite verification, descriptor bytes and execution metadata must be
validated by the resolver/builder integration, not inferred from these Go structs.

## Proposed AgentSuite extensions after #339

The five extensions below form one build-to-deployment chain:

```text
Suite S -> composition C -> build profile P -> execution contract E
                    | build
                    v
Image I -> existing sandbox binding -> S, C, P and E

Separate member-image association A -> S + [(agent, platform, P) -> C, E, I]
                    | select and verify
                    v
Lift plan -> selected mode + typed inputs + installed runtime capabilities
```

Names, media types and paths below are proposed. These are portable artifact
contracts; cluster/context names, installed runtime versions, credential values
and deployment receipts remain outside the suite.

### 1. Execution-contract schema and build-profile reference

**Proposed contract:** introduce a closed `execution-contract.schema.json` record
with its own schema version and media type. A build profile selects it by a
suite-relative path and canonical digest, following existing manifest-reference
conventions. Require it for deployable image builds; profiles without it remain
valid definition/build inputs but do not establish deployable compatibility.

Illustrative build-profile fragment:

```json
{
  "id": "service-profile",
  "execution": {
    "path": "execution/service-v1.json",
    "digest": "sha256:<execution-contract-digest>"
  }
}
```

The referenced contract declares:

- its versioned semantics and harness/configuration ABI;
- named supported invocation modes with exact protocol revisions;
- each mode's lifecycle (service or session), activation mechanism, readiness
  and termination semantics, and result/cancellation interface;
- typed, mode-scoped runtime input slots and delivery mechanisms;
- required behavioral capabilities and filesystem/network requirements.

Reference established protocol/ABI specifications rather than duplicating their
wire protocols. Image OS/architecture, agent-serving protocol, in-image framework
and inference protocol remain separate fields. A mode selector is a controlled
contract input, not permission to override arbitrary process environment.

Validation must reject unresolved paths, stale digests, unknown required semantics,
duplicate mode/input names and references to undeclared modes. The selected
harness must implement every advertised mode; builders must not claim support
merely because the source record lists it.

**Open questions:** inline versus referenced execution records; which protocol
profiles to qualify first; whether readiness and process controls belong directly
in this schema or in referenced harness profiles; exact version/media-type names.

### 2. Linkage through the existing sandbox binding

**Proposed contract:** extend the existing Agent sandbox binding with an
`execution` descriptor, and add an exact build-profile descriptor alongside its
identifier. Preserve `suiteDigest`, agent, platform, composition and inventory
linkage. This uses the existing binding file and digest-label mechanism, not a
second deployment-specific image identity.

Proposed additional binding fields:

```json
{
  "buildProfile": "service-profile",
  "buildProfileDescriptor": {
    "mediaType": "application/vnd.agentsuite.build.profile.v1+json",
    "digest": "sha256:<profile-digest>",
    "size": 1234
  },
  "execution": {
    "mediaType": "application/vnd.agentsuite.execution.contract.v1+json",
    "digest": "sha256:<execution-contract-digest>",
    "size": 512
  }
}
```

For these JSON descriptors, digest and size should cover canonical JCS UTF-8
bytes. This requires an explicit common descriptor-byte rule for the existing
composition/binding records too. OCI image descriptors continue to cover exact
registry bytes; the two digest domains are not interchangeable.

Embed the selected execution record unchanged in canonical form, for example at
`/.agentsuite/execution.json`. Image verification compares its descriptor with
the binding, the suite's profile reference and the selected composition. The
binding label must match the binding bytes, and image platform metadata must
agree. Include execution content in the final inventory; define exclusions for
inventory/binding self-reference with #307.

Matching descriptors establish consistency, not authenticity or proof of runtime
enforcement. Signed build provenance and qualification remain separate evidence.

**Open questions:** whether the profile descriptor is necessary given the suite
back-reference; final embedded path; migration/versioning of the binding schema;
whether attached metadata may substitute for embedded bytes and under which rules.

### 3. Separate member-image association record

**Proposed contract:** a versioned, immutable build-output record identifies one
source suite OCI descriptor and maps exact selections to runnable image
descriptors. It is published after image construction, outside the source suite.

```text
MemberImageAssociation
  schemaVersion / mediaType
  suite: OCI descriptor
  members[]:
    agent
    platform
    buildProfile: identifier + descriptor
    composition: descriptor
    execution: descriptor
    image: OCI platform-manifest descriptor
    sandboxBinding: descriptor
```

The association must not change the source suite already embedded in its images.
Its descriptor is recorded in the deployment plan/receipt. Retrieval hints may
identify repositories, but authoritative selection uses descriptors, never tags.
An index output must resolve to one exact platform image before deployment.

Reject duplicate selection keys, foreign suite/member references, missing blobs,
incorrect media types and mismatched image back-bindings. An association may
publish partial build results, but a full-suite lift must find exactly one
qualified image for every requested image-backed member. It must not silently
deploy only the available subset or select the newest of conflicting results.

An OCI referrer is a discovery candidate, not automatically an authoritative or
trusted result. Resolve and pin one association before planning; define explicit
selection/trust policy when several referrers match. Registry copy must preserve
the association and required image graph, not assume referrers are copied by a
plain suite-manifest copy.

**Open questions:** one suite-wide association versus per-build records; referrer
discovery versus an explicit association reference; signing/trust requirements;
repository relocation and copy behavior; how native members are represented.

### 4. Typed runtime inputs and compatibility rules

**Proposed contract:** replace opaque input names with typed slots whose semantics
are explicit. Each slot declares name, purpose, value schema, required/default
behavior, applicable modes, delivery (environment/file/protocol configuration)
and whether it accepts a value or a credential reference.

Examples of purposes are inference endpoint, inference credential, agent endpoint
authentication and ToolProvider connection. A credential slot accepts a reference
at deployment; it never places the credential value in a suite, image, plan or
receipt. Non-secret value schemas can constrain URL schemes, enums and ranges.
Defaults must be explicit immutable contract content, not host-derived values.

Fixed build settings should be identified separately from bindable inputs. In
particular, #339's fixed model base URL cannot be rebound until the harness
supports it. Instructions, model requirements, Tool grants and invocation edges
remain behavior-defining suite content rather than arbitrary environment overrides.

Planning validates every required slot for the selected mode, rejects unknown or
unused bindings, validates value types, and checks collisions in environment/file
delivery. The adapter checks the installation's exact execution schema, harness
ABI, supported protocol/lifecycle combination, platform and required capabilities.
Compatibility is a supported combination, not the cross-product of unrelated
lists. Recheck the installation and binding identities before deployment.

**Open questions:** shared vocabulary for input purposes; JSON Schema subset and
external-reference policy; per-mode versus shared slots; credential rotation and
binding identity; where an adapter's version-qualified compatibility evidence lives.

### 5. Multiple profiles per agent/platform

**Proposed decision for review: support multiple explicit build profiles for the
same agent/platform, keyed by `(agent, platform, buildProfile)`.** This lets one
suite carry different harness implementations or execution contracts for the same
OS/architecture without overloading OCI platform fields.

This requires coordinated updates to composition references, validator indexes,
build selection, association keys and diagnostics. A digest-bound composition
still identifies one exact result. Build and lift may omit the profile only when
exactly one candidate remains after explicit requirements and compatibility
filtering. Ambiguity is an error; array order, profile names, runtime brand guesses
and silent rebuilds are not selection policies. Record the chosen profile and
composition descriptors in the final plan.

This differs from #326's proposed one-composition-per-agent/platform rule and
from current validator indexing. If that narrower rule is retained, different
profile builds must instead be published as separate suites, and the APIs must
say so. Neither behavior should be implied by the current schema accidentally.

**Open questions:** accept the explicit triple-key proposal or retain the narrower
rule; whether a suite may declare a default profile; whether compatibility-only
selection is sufficient or deployment environments must always name a profile.

### Contract implementation checklist

| Extension | Proposed Go contract impact | Still needed after this draft |
|---|---|---|
| Execution record | `Execution`, `Mode`, `Input`; build-profile reference resolved by `Builder` | Closed schema, typed delivery/mode activation, profile field and semantic validation |
| Sandbox binding | `BuildOutput.Binding`, `BuildProfile`, `ExecutionDescriptor` | Binding schema extension, canonical descriptor-byte rules, builder emission and image verification |
| Member-image association | Collection of `BuildOutput` selections tied to one suite | Association record/schema, resolver/publisher, plan and receipt association identity |
| Typed inputs and compatibility | `TargetBinding`, `Installation`, `Selection` | Typed slot checking, qualified compatibility combinations and pre-deploy revalidation |
| Profile selection | Exact profile/composition descriptors in build requests/results | Triple-key graph references/indexes, explicit selectors and ambiguity tests |

The current Go draft only implements basic selection, descriptor-shape and
list-based compatibility checks. It does not implement these wire schemas,
input-schema evaluation, full graph verification or association discovery. After
decisions land, add round-trip fixtures, stale binding/digest tests, missing-member
tests, multi-mode compatibility tests and multiple-profile ambiguity tests, then
prove one real builder-to-adapter handoff on kind and AKS.

## Suite and implementation selection

`Suite` retains all agents, verified instructions, ToolProviders, compositions,
build profiles and invocation edges. A resolver must verify every graph reference
and instruction digest before returning it. Native and image execution are
explicit alternatives per member. A native adapter must refuse image inputs
rather than discard them and render a different implementation.

`Request` selects every member exactly once. A missing image, incompatible member,
unconsumed binding or unsupported invocation bound fails whole-suite planning.
`Request.Validate` checks basic topology and descriptor shape only; it is not a
replacement for suite validation, image verification or adapter semantic checks.

The suite-to-image association is an independent build-output record. Its
discovery and publication contract remain open; adding image digests to the same
source suite already embedded in those images would create circular identity.

## Target and binding

`Target` separates context (access locator), cluster UID, namespace and selected
agent runtime. Inference is a binding, not a runtime selector. `Installation`
reports the observed runtime version, identity and capability revision plus
accepted execution contracts, binding formats and reconciliation support.

`TargetBinding` is a versioned adapter-owned document containing configuration
and Secret references, never credential values. Its digest must cover the exact
canonical format and payload. Binding decoding/digest verification belongs to
the adapter; generic orchestration must not interpret vendor-specific bytes.
Operation stores own persistence and rehydration. A receipt is not a replay recipe.

## Plan and deployment

The adapter plans the whole suite, not a loop that creates one agent before
validating the next. It owns shared-resource deduplication, naming, dependency
ordering and activation of invocation relationships. Invocation edges are not
necessarily an acyclic startup graph. Unsupported semantics must be refused.

`Plan` freezes ordered native documents, apply/review classification, member
selection identities, target and installation facts. `NewPlan` checks complete
selection and declared compatibility; only a qualified adapter can establish
that the native documents faithfully represent the full suite. The plan digest
covers apply/review disposition so a reviewed document cannot silently become a
mutation. Accessors return independent copies.

The application evaluates the promotion gate and obtains review for that exact
plan. `Authorization` records the plan match, not cluster authentication or a
trust decision about supplied evidence. Adapters recheck installation, cluster,
resource and binding identities immediately before effects. They never provision
or delete infrastructure. No automatic retry follows an ambiguous mutation.

Receipts contain per-member/resource outcomes, including unattempted members and
unknown effects. Partial success is not atomic suite installation. Readiness is
not proof of an answer or a passing evaluation. Observe must distinguish absence
from unreadable state and retain a retired state for session registrations.
Retirement/reclamation APIs and durable operation recovery are follow-ups.

## Existing implementation and integration order

1. Reuse AgentSuite validation and #339 resolution; expose a complete snapshot.
2. Agree build-output/execution metadata with #339 and verify real image bindings.
3. Implement shared Lift orchestration and typed whole-suite planning (#279).
4. Wrap existing runtime Render/Deploy machinery in suite adapters. Orka already
   reconciles single-agent resources. The second adapter's exact-version
   create-only path must not be advertised as reconciliation support (#294/#238).
5. Prove local kind and remote AKS against each qualified runtime/version pair.
   Registry credentials used by KMX and node image-pull credentials are separate.
6. Integrate existing evaluation policy and #341's session evidence without
   treating host-reported identity as a verified image descriptor.
7. Build the console around Suite / Images / Deployments / Runs using these shared
   operations, not printed-plan parsing or local-live-agent prerequisites.

This follows #326's internal-first maturity direction. There is no exact public
interface-count commitment, and existing `agent.yaml` workflows remain intact.
The executable standalone HTTP-image path is included in this branch; its narrow
contracts are documented separately from the broader proposals here.

## Review decisions

- Review the five proposed AgentSuite extensions above, especially the binding
  linkage, association discovery and explicit multiple-profile decision.
- Should execution metadata be embedded alongside the sandbox binding or exposed
  by a defined OCI attachment, and which component verifies the association?
- What concrete harness/mode combinations will the first two adapters qualify?
- How will one suite publish/discover a complete, unambiguous image set?
- Which binding identities and observations must be rechecked before Deploy?
- How should native and image members participate in one suite's evaluation gate?

## Tests

`go test ./internal/kmx/suitedeploy` checks complete selection, incompatible
contracts and bindings, create-only refusal, immutable plan snapshots, disposition
identity, local/remote target separation and exact-plan gate authorization. These
tests do not claim cluster deployment or builder conformance.
