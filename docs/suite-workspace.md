# Author and publish a one-member native suite

These docs follow main. **Test and Deploy are unreleased**; v0.8.1 uses `kmx eval` and `kmx lift`
with identical arguments. See [Test compatibility](kmx.md#test-compatibility-unreleased)
and [Deploy compatibility](kmx.md#deploy-compatibility-unreleased) for the hidden
deprecated routes and unchanged machine/API contracts.

A workspace is editable source. Its prepared AgentSuite is an immutable OCI
artifact, identified by its manifest digest. Native Orka and exact-v0.10.3 Go
kagent declarations require no member images; authoring and publication do not contact Kubernetes,
run a model or invoke Docker/buildx. The pinned AgentSessions chat profile also
supports offline authoring, preparation, loading and explicitly unevaluated push
(`--unevaluated`) with no member image. Full packaged evaluation and exact-suite evaluated push
require the explicit [development host](#agentsessions-development-evaluation)
boundary and an [`AgentSessionsBinding`](#agentsessionsbinding). Deploy is a
[plan-only render](#agentsessions-deploy-plan); chat/status and Orka/kagent
operational consumers refuse it.

**Implemented:** offline creation, preparation and publication, and full-inventory
native Orka or exact kagent v0.10.3 Go/linux-amd64 evaluation of one tool-free
member with an exact OpenAI-compatible model. Orka remains the authoring default;
`--runtime kagent` authors the private profile. Root test derives that packaged
runtime and uses explicit author-owned bindings; evaluated push accepts only
matching runtime-specific passing evidence. Root digest Deploy supports both
profiles. Kagent Deploy is create-only: any occupied primary or generated child
refuses, even with matching evaluated UIDs. Legacy status and retirement remain
unavailable. Public Orka eval creates fresh disposable candidates and cleans them
up before exporting proof. Kagent evaluation refuses occupied targets without a
complete caller-retained observation; qualified reuse is read-only, not
update/adoption or restart recovery.
Orka evaluation has hermetic command/Task tests and a pinned three-case native
workspace CI qualifier. Packaged kagent evaluation has hermetic coverage and
pinned exact-v0.10.3 CLI qualification. Root exact-digest Deploy retrieves the
package from a registry or local layout and separately consumes matching private
full-passing evidence. Orka uses `--inference`; kagent uses author-owned
`--bindings`. Bounded CI qualifies Orka exact registry/layout reuse and kagent
fresh-target creation/occupied refusal. Standalone registry-package evaluation
remains unavailable. Legacy user `agent.yaml` creation, evaluation, lift and
receipts are removed; no conversion or migration is provided.

## Optional member egress policy (offline only)

Orka members may declare `policy: policy.json` in `agents/<id>/member.yaml`.
The file is member-relative public `agentsuite.policy/v2` JSON, allowing only
literal IPv4 + TCP + explicit port; an empty allowance list denies all.
Preparation packages authored canonical bytes under `policies/<id>.json`, bound
through `Agent.policy` and the suite digest. Endpoint order and duplicates are
preserved; formatting-only changes affect source fingerprints, not packaged
identity. Omitting policy preserves existing policy-free fingerprints and bytes.
See the [policy contract](portable-policy.md) for examples and administrative
intersection semantics.

This is offline authoring/transfer, not runtime support. Root push with
`--unevaluated` retains the policy; eval, private native preparation, evaluated
publication, composed/digest Deploy and the experimental sandbox builder refuse a
declared policy before execution/export. Legacy kagent and AgentSessions policy source
declarations refuse; v3 retains policy as a shared declaration only. No enforcing CNI, installed NetworkPolicy, active worker
observation or status capability is implied. Static unsafe paths, symlinks and
nonregular policy files refuse using existing bounded reads; concurrent hostile
workspace mutation is outside their guarantees.

## Explicit runtime profiles (offline only)

Single-member `schemaVersion: 3` packages multiple public runtime/build-profile
requirements into **one OCI suite digest S**. Author this format manually; create
continues to emit the unchanged legacy format:

```yaml
schemaVersion: 3
name: greeter
member: agents/greeter/member.yaml
profiles:
  - runtimeProfile: runtime-profiles/orka.json
    buildProfile: build-profiles/orka.json
    platform: linux/amd64
  - runtimeProfile: runtime-profiles/sessions.json
    buildProfile: build-profiles/sessions.json
    platform: linux/amd64
```

Each row references a complete public AgentSuite RuntimeProfile and BuildProfile
JSON document; IDs come from those validated documents, not filenames. Runtime
profiles pin runtime/harness/renderer identities, a consumer-owned
`configurationABI` and `imageRequired`. Declarations may name unknown portable
implementations: format acceptance is not support for running or building them.
The selected build profile must cover the platform and pin the same harness
digest as the runtime profile. Build-profile `execution` is image-serving
metadata, not a runtime selector or the AgentSessions harness protocol.

Keep instructions, exact model, flat `eval/*.yaml` cases and optional member
policy once. V3 forbids `members`, the legacy top-level `buildProfile` and
`member.runtime`, including explicit empty/null declarations. Legacy v1/v2
forbid `profiles`; their bytes/digests and default Orka interpretation are
unchanged. At most 16 unique profile/platform tuples are allowed. Conflicting
same-ID documents, missing references, mixed forms, unknown fields, unsafe paths,
symlinks and nonregular/oversize inputs refuse. All selected authored profile
files enter the fingerprint and provenance closure, with the existing 1 MiB
per-file and 8 MiB aggregate source/artifact limits.

```sh
kmx push --workspace ./profiled --runtime-profile orka --platform linux/amd64 \
  --unevaluated --to-layout ./layout greeter:v1
```

`--runtime-profile` is mandatory even with one profile. Omit `--platform` only
when that explicitly selected profile has one candidate. Unknown or conflicting
selectors refuse; legacy workspace push rejects both new selectors. Selection
changes the offline view, **never S or the transferred graph**: all profiles,
compositions, cases and the shared policy remain packaged. V3 has no singleton
private native runtime metadata. Policy packaging is declaration only, not
Sessions enforcement.

Internal `LoadSelected`/`LoadArtifactSelected` expose the verified public
requirements, profile digest and platform. Ordinary `Load`/`LoadArtifact` and
both inventory loaders refuse profiled input even with one profile. The retained
verified graph resolves exact same-S composition digests without rereading source
or aliasing mutable selected bytes. There is no profiled inventory API.

`suite build DIRECTORY --runtime-profile ID [--platform OS/ARCH]` resolves an
exact composition/build profile offline, but the existing AgentKit producer
**refuses every profiled plan** before exporting or invoking Docker. There is no
producer fallback or Sessions image-build adapter.
Profiled execution, evaluated push, private receipt creation/export, native public
evidence export and Deploy all refuse, including profiles declaring `runtime.id=orka`.
Only explicit unevaluated publication is available. Independent `test verify`
can check externally produced portable evidence against separately reviewed
expectations; it grants no private push/Deploy authority.

### Detached Sessions declarations (internal, offline only)

Internal `workspace.NewSessionsAssociation` and `CheckSessionsAssociation` create
and check canonical detached `SessionsAssociationV1` JSON against the frozen
selected graph. They do not write files, alter S, discover records from caches,
read source or contact a registry. There is no CLI association flag or consumer.

The closed record requires `version: 1`, `kind: "kmx.sessions-association"`,
`suiteDigest`, `memberID`, `runtimeProfile: {id, digest}`,
`compositionDigest`, `buildProfileDigest`, `platform: {os, architecture, variant}`
(including empty `variant`), `harnessImage`, `executionKind: "harnesswire-chat-v1"`,
`configDigest` and `imageVerification: "declared-unverified"`. Inputs are at most
16 KiB and must exactly match independently derived canonical bytes; missing,
null, unknown, duplicate or mismatched fields refuse without echoing payloads.

Only a selected declaration with `runtime.id=agentsessions`, no member policy and
no build-profile `execution` contract is eligible. This selects a declaration
family, not a supported runtime. The image reference must echo the selected
build profile's exact digest-pinned harness reference; mirrored repositories,
tags and profile/build-profile harness digest mismatches refuse. Config uses
the existing framed hash of the exact JSON `system_prompt` bytes. Mutable
selected-view fields cannot change the frozen selection or its config.

**The image digest and platform are declared, not verified.** No image bytes are
read; runtime/harness/renderer versions and configuration ABI are not qualified.
Successful declaration checks grant no execution, evidence, evaluated-publication
or Deploy authority. Profiled execution and Apply remain refused.

Suite instructions are **non-confidential and must never contain secrets**.
Authorized Sessions Replay readers, journal-at-rest readers and route-config
readers may see injected instructions; credentials must stay host-side.
Source and packaged-native preflight reuse the existing credential-shape checker.
A match in valid UTF-8 instructions adds a fixed, redacted `Warning:` line before
the existing hard refusal in the returned error diagnostic; it is not an
admission mode or a separate warnings result. No matched bytes are printed.
Other credential and invalid-Unicode refusals stay in place. Clean lint does
not establish that instructions are secret-free.

### Offline private receipt consistency

The private scanner resolves each record's own composition in the same S, then
runs all existing receipt structural, runtime/host, render, member, build-profile
and case-coherence checks before deciding whether it matches the selected view.
A valid other-profile record neither corrupts the selected history nor authorizes
fallback; malformed, foreign, unknown-composition or wrong-member records refuse.
Allocation uses the global maximum sequence; companion metadata lookup uses the
selected receipt's sequence.

For this comparison only, kmx owns three **legacy receipt declaration labels**:

| `configurationABI` label | Existing declaration mapping |
|---|---|
| `kmx-native-orka-v2` | Orka, `kmx.native-orka/v2` renderer |
| `kmx-native-kagent-v2` | Exact kagent v0.10.3 Go and its existing native renderer/execution labels |
| `kmx-native-agentsessions-v2` | Exact pinned development-chat AgentSessions commit and its existing native renderer/execution labels |

These are not upstream image ABIs or runtime qualification. Mapping requires the
matching runtime ID, linux/amd64 and `imageRequired: false`; kagent/Sessions also
require their existing pinned version. Unknown mappings refuse proof lookup.
The receipt-only projection preserves the profiled marker/graph, never fills or
changes observed receipt runtime/host facts, and cannot enter execution or export.
Public implementation digests are **not qualified** by this consistency check.
Synthetic tests demonstrate only lookup/binding consistency, not two-runtime
execution. Real profiled execution/evidence integration remains unavailable.

## Provisional multi-member source (offline only)

A separate KMX-local source format supports deterministic offline preparation and
inventory loading of tool-free members. Its authoring shape is **provisional**
pending [#526](https://github.com/kaimahi-agents/kaimahi/issues/526); it is not a
new public AgentSuite schema. Existing `schemaVersion: 1` authoring and create
prompts are unchanged; both source formats use the public evaluation packaging
and receipt rules below. Earlier private-case packages/receipts are not migrated.

Author the list manually in `workspace.yaml`:

```yaml
schemaVersion: 2
name: writing-team
members:
  - agents/writer/member.yaml
  - agents/reviewer/member.yaml
buildProfile: build-profiles/default.json
```

Each `member.yaml` uses the existing member fields (`id`, `instructions`, exact
`model`, optional supported `runtime`); its ID must match its directory, and
instruction paths stay relative to that member. Keep cases in `eval/writer/`
and `eval/reviewer/`, not the singular format's flat `eval/`. Each directory must
contain regular YAML case files. For example, both members may independently
have an `example.yaml` containing case ID `example`; neither replaces the other.
The existing pinned build profile is shared. No tools or `invokes` authoring,
binding, approval, coordination or evaluation semantics are enabled here.

The bounded format accepts 2–16 members, at least one case per member and at most
128 cases in total, with independent 8 MiB source/artifact limits and 1 MiB per
file. Policy-free file budgets remain `4 shared + 4 per member + 128 cases`:
136 for one member, 140 for two and 196 for sixteen. Each optional policy adds one
file, so source-free readers admit at most 212 files (16 policy-bearing members).
Complete native graph validation enforces membership, policy bindings and case coverage.

Member order is canonical by ID; each member's case index preserves authored
filename order. Cases are packaged under `evals/<member>/index.json` and
`evals/<member>/cases/<case-id>.json`, bound through that member's agent manifest.
Every inventory uses the same packaged-case decoder as one-member evaluation.
Case edits change both the public graph digest and the suite OCI digest; comments
or formatting in authored YAML change only the source fingerprint when parsed
content/order stays the same. There is no private case list or dual-write.
Duplicate/unresolved members, mixed `member`/`members` forms, undeclared member/case
directories, symlinks, unsafe paths and graph/case digest drift refuse without
rewriting source.

Preparation uses the internal workspace API (`Prepare` then `LoadInventory`);
there is no new CLI prepare command. The execution loaders remain single-member
only. Root `test`, root workspace `push` (including `--unevaluated`) and composed
workspace Deploy refuse this format before operational state, output or target
activity. Exact-reference Deploy also refuses verified multi-member native artifacts
before target observations; existing prerequisite validation remains unchanged.
General extracted-directory `suite push` is still format transfer, not execution
or evaluated publication. None of these paths can use a one-member receipt to
qualify a larger suite. Multi-member runtime support is a separate change.

## Experimental flow authoring (offline only)

`kmx create NAME --workspace DIR --members members.json --flow flow.json`
authors a complete tool-free Orka suite using the explicitly selected
`kmx.flow.v1alpha1` contract. It does not run a pipeline, evaluate members,
publish, deploy, enforce approvals or qualify a runtime. The default
single-member command and source/artifact bytes are unchanged.

The members input is strict JSON, not a source workspace descriptor:

```json
{
  "schemaVersion": "kmx.members/v1alpha1",
  "members": [
    {"id": "writer", "model": "exact-writer", "instructions": "writer.md", "cases": ["facts.yaml"]},
    {"id": "reviewer", "model": "exact-reviewer", "instructions": "reviewer.md", "cases": ["facts.yaml"]}
  ]
}
```

Member IDs are unique; the exact model string uses the existing fixed
`openai-compatible` protocol, without discovery or inference. Instructions and
case paths are literal, confined paths relative to the members file's directory.
Each member needs reviewed YAML cases; basenames must be unique within a member,
and existing filename ordering determines evaluation order. No example cases
are invented. Instructions, cases and flow inputs are non-confidential: do not
put credentials in them.

The flow input is the closed public experimental JSON document, for example:

```json
{
  "schemaVersion": "kmx.flow/v1alpha1",
  "id": "release",
  "mode": "pipeline",
  "entry": "writer",
  "edgeRequirements": [],
  "steps": [
    {"id": "draft", "member": "writer", "input": {"source": "run.input"}},
    {"id": "review", "member": "reviewer", "input": {"source": "draft.output", "context": "run.input"}, "approval": "policy-controlled"}
  ],
  "cases": [
    {"id": "review-facts", "input": "Frozen facts and task", "assertions": [
      {"step": "review", "check": {"id": "verdict", "type": "contains", "value": "Verdict: PASS"}}
    ]}
  ]
}
```

Create requires all three flags explicitly. It never prompts or accepts the
single-member model/instructions/description/runtime selectors, including explicit
defaults. Every input and the complete generated graph is validated before
destination writes; occupied workspace/member paths refuse without replacement.
The imported files are copied into editable schemaVersion 2 source, with optional
`flow: coordination/flow.json`. Hand-authored flow paths must be confined
workspace-relative files and are allowed only in schemaVersion 2.

Internal `workspace.PrepareFlow` and `LoadFlowInventory` select the contract
before ingestion and retain every native member. The fixed flow document is
canonicalized at `coordination/flow.json`, bound by its original critical
selector digest F. Preparation checks shared storage semantics and complete
native-file regeneration, including flow, without stripping the extension.
Original authored bytes bind the source fingerprint. A semantic member/flow edit
changes the one OCI artifact S; no old evaluation authority follows that edit.

The flow file alone may be at most 4 MiB; other files remain at most 1 MiB.
Existing 8 MiB source/artifact, 212-file and 256-content-tar-entry ceilings remain.
The shared 128-case budget includes both member and flow cases. The two-step
contract does not turn the 2–16-member offline inventory into executable support.

Source-free `suiteview.LoadLayoutFlowV1Alpha1` and
`LoadArchiveFlowV1Alpha1` return original artifact identity, copied flow/F and
**every** portable member recipe in suite order. Explicit native flow inventory
readers additionally require the complete closed native graph. Ordinary
layout/archive/native loaders and current transfer/registry paths remain strict;
they do not retry or discard a required extension. Root test/push/Deploy remain
unsupported for these suites, including unevaluated push. No member receipt or
storage validation is a whole-suite execution proof.

## Named destinations

Keep operational selectors in optional workspace-root `kmx.yaml`, separate from
source `workspace.yaml`. Define them once rather than repeating cluster selectors:

```yaml
version: 1
evalTarget: dev
publishRepository: registry.example/team/agents
targets:
  dev:
    context: kind-demo-dev
    namespace: orka-system
    runtime: orka
    inference: provider:inference
  prod:
    context: production
    namespace: orka-system
    runtime: orka
    inference: provider:inference
    evaluationTarget: dev
registries:
  '127.0.0.1:5001':
    plainHTTP: true
```

```sh
kmx ctx
kmx ctx --check -o json
kmx test --to dev --out ../private/passing.json
kmx push --evaluation ../private/passing.json registry.example/team/agents:v1
kmx deploy registry.example/team/agents@sha256:<digest> --to prod \
  --evidence ../private/passing.json --promote --plan
```

`ctx` lists names, cluster contexts, namespaces, declared runtimes,
inference/bindings references and the saved registry/repository, alphabetically.
It is **list-only**, with no current/default pointer or switching; `ctx prod`
and `ctx use prod` refuse. `context show/use` still manages the separate access
selection. `targets` still reports runtime capabilities, not named destinations.
`ctx --workspace DIR` selects an exact workspace root for the report.

Listing is offline and does not load cluster configuration or inspect bindings
or Secrets. `--check` only checks local context-name presence using an installed
kubectl and anonymous registry `/v2/` reachability with bounded time/output. It
never runs an auth helper, follows a redirect, writes operational state, installs
tools or makes a model call. Authentication-required is not repository access;
runtime/inference/bindings are explicitly not checked. Independent rows remain
visible if a check is unknown or fails; the command then exits nonzero.

`test --to NAME` and digest `deploy --to NAME` resolve concrete selectors once,
print them and their source, then retain those values for the entire invocation.
Editing `kmx.yaml` during confirmation cannot change that operation. Existing
source/binding-file/runtime/remote-state checks still apply. Explicit `--to-context`,
`--to-namespace`, `--inference` or `--bindings` overrides the corresponding saved
value; inference and bindings remain mutually exclusive. Saved relative bindings
paths are config-relative; explicit flag paths remain invocation-relative.
Runtime is an expectation checked against the package, never a runtime switch.

Eval may use saved `evalTarget` when no destination is explicitly selected; it
never guesses from a name, kubectl current-context or remembered access selection.
Deploy has no default destination. Explicit-reference Deploy without `--to` remains
source/workspace-independent. AgentSessions development evaluation ignores these
cluster defaults and refuses named destination selection.

Root workspace registry push and named Deploy may use an **exact registry-host**
`plainHTTP` policy; explicit `--plain-http=false` wins. TLS remains the default;
there is no HTTP fallback after TLS failure, and existing credential restrictions
remain. Local layouts and extracted-directory `suite push` are unchanged.
The config is closed, bounded and credential-free (Secret references only);
invalid or unreadable nearest state refuses rather than falling back. No user
config merging. Operational config is excluded from prepared S, source fingerprint
and published content.

This is selector configuration, not environment installation or deployment
identity. `publishRepository` supplies bare push and composed Deploy's repository;
`evaluationTarget` explicitly selects where a destination's missing proof runs.
Root test keeps managed passing proof with optional `--out`; explicit push
references are preserved. Explicit-reference Deploy remains deploy-only and requires
a digest/private evidence and explicit `--promote` for source-only evidence.
**Kagent stays out of composed Deploy until its own candidate-scoped evaluation
and ownership-safe cleanup are qualified**; standalone explicit kagent operations
retain their existing boundaries. AgentSessions workspaces and `--harness-image`
also refuse composed Deploy before evaluation or publication; AgentSessions Deploy is
the explicit-reference [plan-only render](#agentsessions-deploy-plan). Named selectors
never bypass remote confirmation.

<a id="composed-workspace-lift"></a>

## Composed workspace Deploy

With the Orka prerequisites and named configuration above already prepared:

```sh
kmx deploy --to prod --plan
kmx deploy --to prod
```

Without a positional reference, Deploy discovers the nearest workspace and freezes
its exact packaged version and source fingerprint. It requires `publishRepository`
and the destination's explicit `evaluationTarget`; it never substitutes `evalTarget`
or a current/remembered context. It runs missing evaluation, publication and
placement in that order, using the existing disposable candidate and release gates.
Promotion is selected automatically and disclosed as **passed on the evaluation
target, not evaluated on the destination**; explicit `--promote=false` stays strict.
Newly created release objects carry the non-secret provenance described below.
Guided authoring and tag-based Deploy are not included. Destination update and
rollback require the explicit retained Orka path below; plain Deploy stays create-only.

The default stage transcript has four stage lines on stderr: Plan, Eval, Push and Deploy,
plus compact non-pass evaluation detail;
failures may add a safe refusal or critical cleanup warning. Skipped stages keep
their line. Counts are case runs (Tasks), also named `caseRuns` in JSON; a completed
evaluation means each case's threshold was met, not that every run passed.
`-v` shows diagnostic detail; `-o json` writes one
structured result to stdout, including stage and partial outcomes. JSON stage
and result keys/values keep their existing `lift` spelling; this is not a data
migration. Legacy `kmx lift` also retains its stdout and stderr stage labels,
apart from its one deprecation notice. Plan names the
concrete targets and repository before one confirmation covering evaluation Tasks,
candidate cleanup, publication and source-only promotion. Inputs may remain in
runtime/model storage after cleanup; authored cases are published content.
Scripts must supply the context-bound `KAIMAHI_CONFIRM` named by the plan:
remote context names, comma-separated in evaluation/destination order with duplicates
removed. Local evaluation plus remote production asks only for the remote context;
two remote targets share one challenge. An all-local pipeline uses its concrete
local context name(s) to confirm publication.
`--plan` does not prompt, evaluate, publish, admit resources or write completion
records, and works before a passing receipt or published artifact exists. This
no-reference plan is offline stage intent, not a destination eligibility/readiness
claim; explicit digest `--plan` retains its GET-only eligibility review.

Skips are exact-version checks, not optimistic retries. Cached proof must match
the selected evaluation environment/runtime/inference. Evaluated root registry
push also registers publication completion, so independently completed eval/push
can be skipped by composed Deploy. Older publications without a local completion
record do not confer skip authority. A recorded publication is verified by
immutable digest, not a tag. With `--require-provenance`, bounded registry reads
and temporary local extraction also verify existing provenance; missing or
unverifiable attachments refuse without republishing or repair.
A recorded successful Deploy is rechecked
GET-only against destination inputs, ownership markers, resource UIDs, whole specs
and current readiness. Private completion records grant only skip authority, never
Apply, adoption or replacement. Missing, drifted or uncertain destination completion
refuses rather than repairing an occupied or partially changed target. Full receipts
and completion observations stay in private KMX state, outside authored source.

A failed or uncertain evaluation/cleanup stops publication and placement. A failed
publication can already have transferred content; inspect its incomplete/unknown
outcome rather than assuming nothing reached the registry. Partial Deploy resources
may remain. No later stage runs as an automatic repair, retry or rollback.

<a id="cluster-visible-lift-provenance"></a>

## Cluster-visible Deploy provenance

Reference Deploy, composed Deploy and native Quickstart stamp **newly created** Orka
Provider/Agent and kagent ModelConfig/Agent objects with the same versioned JSON
annotation, `kaimahi.dev/lift-record`. Existing exact Orka reuse and verified
composed skips remain no-write: they never add, refresh or repair annotations.
A mixed create/reuse pair can therefore have different or missing provenance;
never attribute the new object's record to its reused companion.

The annotation contains only metadata:

| Field | Meaning |
|---|---|
| `version`, `liftID` | Version 1; opaque 32-hex-character attempt ID. |
| `suiteDigest`, `receiptDigest`, `receiptRunID` | Exact OCI suite digest and canonical consumed receipt digest/run—not the latest available pass. |
| `evaluation`, `target` | Concrete context, cluster UID, namespace and namespace UID for source and destination. |
| `evaluationName` | Optional frozen named evaluation alias, supplied by composed Deploy; never inferred from context names. |
| `gate`, `evaluatedOnTarget` | Actual `same-subject` or `promotion` decision. Promotion is source-only evidence, **not evaluated on this target**; a requested `--promote` alone does not determine the gate. |
| `attemptedAt` | UTC attempt time, not successful pair-completion time or Kubernetes creation time. |
| `resourceSpecs` | Expected per-kind spec digests for readback comparison. |
| `actor` | Omitted unless known; no actor is inferred from a local username or context. |

A same-subject receipt describes earlier execution in that environment, **not
fresh evaluation of newly created objects**. Readiness is separate from evaluation.
The annotations are visible to operators with object-read permission, including
on another machine without local Deploy history. They contain no receipt body,
prompts, answers, Secret values, binding bytes or local evidence paths. They are
unsigned observations: copying them cannot prove original UID continuity, signer
identity, ownership or permission to replay a placement. AgentSuite's portable
format and KMX ownership/plan digests are unchanged.

Before native admission or creates, Deploy retains the canonical receipt and intent
in separate private state at `KMX_HOME/release-lifts/<liftID>/` (under the ordinary
KMX state directory when `KMX_HOME` is unset). Immutable `intent.json`, per-create
`1.json`/`2.json`, and a final checkpoint up to `3.json` retain acknowledged
identities, outcomes and readiness, including partial/failed attempts. File modes
are 0600 under 0700 directories. Ordinary observation-only attempts retain four
slots. New complete Orka pair creates additionally reserve `4.json` as a
record-bound ownership candidate and a final `committed.json` acknowledgment
marker (below). Storage remains
bounded to 256 attempts and 64 MiB, with no automatic pruning. These intent/outcome
observations never grant skip, evaluation or replay authority; older observations
are never converted into the new explicit ownership proof.

New creation also retains a bounded, versioned **target-dependency proof**:
inference Provider UID/generation/whole-spec digest and Secret reference, Secret
UID/resourceVersion/key-presence, and exact runtime identity. Only between
separate revisions may that inference Provider's resourceVersion differ while
all authoritative fields remain unchanged. Any spec, identity, Secret reference
or runtime change refuses; within-operation prepared-input checks remain exact.
Old observations cannot reconstruct this target proof from a source receipt,
especially after promotion, and are never enrolled or backfilled.

A storage/metadata-retention error refuses further creates without retry, cleanup
or rollback; resources already created may remain. Result JSON includes `liftID`
and `provenanceState` (`recorded` or `unknown` on returned attempts). `recorded`
means private observations were persisted, not that placement or evaluation passed.
A cold composed Deploy checks known storage constraints before evaluation/publication;
late failures can still leave published content or acknowledged resources.
`--plan`, all-reuse and already-deployed paths do not open this provenance store.
[Root `status --to NAME`](destination-status.md) reads the destination namespace
and these per-object annotations without private history, writes or repair. It
preserves missing/unknown/foreign distinctions; `orka status` remains runtime-only.

## Release retention and operator recovery

**Plain Deploy remains create-only. Updates require explicit `--update` and the
retained Orka ownership proof described here.**
New complete Orka Provider/Agent pair creates now require namespace-local
ConfigMap `create`, and `get`/`patch` for the stable
`kmx-release-<40-hex-character-key>` record. The key binds suite/member/runtime;
the record binds cluster and namespace UIDs. Composed Deploy checks availability,
permissions and private capacity before evaluation/publication spend; actual
conditional creation checks again. An unreadable API, denied permission, stale
record or unavailable storage refuses—never infer absence or delete anything.
Existing exact reuse, mixed reuse and verified placement skips do not enroll,
write or repair a baseline. Kagent is unchanged.

The shared record contains only digests, resource identities, operation ID and
acknowledged outcomes: no specs, receipt bodies, prompts, answers, Secret values
or binding bytes. A CAS reservation spans both native objects. After acknowledged
creation, private `4.json` binds the exact pair to the ConfigMap UID **and the
acknowledged completed shared payload**. It is published only after the shared
completion response succeeds, but visibility alone is not commitment: publication
can still fail after its hard link. The writer installs `committed.json` as a
final exclusive hard link only after publication checks and fallible output cleanup
succeed. Readers require both files with exactly matching bytes, plus all existing
record/target/object/dependency checks. A lost shared response or failed private
checkpoint therefore cannot authorize the next attempt, even if the shared API
committed its write or `4.json` is visible.

The final marker link is the local eligibility commit point. Capacity exclusion
remains held through it. No post-link sync, cancellation or handle-cleanup failure
retroactively labels a committed acknowledgment unknown. The final directory entry
is not promised crash-durable: losing it after a crash conservatively blocks further
updates. A copied complete backup must preserve both files' exact bytes, not their
hard-link identity. These are unsigned private records, not tamper authentication;
never create a marker by hand or prune a candidate to bypass a failed checkpoint.
JSON results identify this object separately as `releaseRecord` with name, UID
when acknowledged, and `pending`, `ready` or `unknown` state.

Shared history rolls only the oldest completed entries beyond ten; pending and
partial proof never rolls. The entire shared record is capped at 64 KiB. Overflow
that cannot safely roll refuses before object effects. Back up private KMX state;
annotations, a copied ConfigMap or old observation files alone cannot replace it.
Agents lifted by **v0.8.1 or earlier cannot be updated yet**. Reused or otherwise
unenrolled placements also lack update-capable proof. Explicitly recreate the
owned pair with this version when ready; no enrollment/backfill is performed.
Unmarked proofs from unreleased #573 builds also refuse, including previously
successful test baselines: their visible `4.json` cannot distinguish successful
publication from a failed post-link checkpoint. No migration or marker repair is
performed; explicit owned-pair recreation is required.

A pre-send refusal does not mint a partial revision or change object markers.
After a reservation but before any object send, the active writer can restore only
the exact old shared data after freshly re-proving the pair and reservation.
Bounded metadata-only finalization may finish after cancellation; it never resumes
object writes or resolves an ambiguous native send/failed checkpoint.

A pending first create, crash, ambiguous native write, failed checkpoint or missing
completion acknowledgment for changed objects stays blocked without expiration or automatic takeover.
For operator recovery:

1. Stop every original writer for that context/namespace/member, including other
   hosts or automation, and arrange a quiet window. If you cannot establish that
   writes have stopped, **do not remove the reservation**.
2. Inspect the reported per-object outcomes, private attempt and named release
   ConfigMap. Verify the concrete cluster/namespace and owned resource UIDs;
   inspect other Agents for references before removing a Provider. Never delete
   an object you cannot establish belongs to this workload.
3. Explicitly tear down the owned Agent, its exclusive owned Provider and the
   named release ConfigMap using your administrative deletion procedure. Verify
   absence. Partial first creates may have only one object. This is an operator
   action, not a KMX retry/cleanup option; removing only the record is not recovery.
4. Recreate into the now-absent placement with this version and the exact passing
   evidence. Preserve the old private history; never hand-edit proof or reset
   private state to bypass a reservation. Capacity/unreadable-state errors must
   be resolved deliberately, not with automatic pruning or guessed recovery.

The opt-in Orka updater ([#561](https://github.com/kaimahi-agents/kaimahi/issues/561))
keeps model,
inference binding, runtime and names fixed, requires complete bounded cluster-wide
Agent-list access to refuse shared Providers (including model fallbacks and
potentially Task-namespace-resolved unqualified references), and conditionally patches with
UID/RV/generation/spec/owned-metadata checks. Ready is not required to inspect an
old known revision for rollback. Only fully recorded old/new states permit a
known-partial follow-up; unknown writes stay blocked. It sends no Tasks and never
re-evaluates rollback evidence. Mutation provenance uses version 2, retaining the
original create observation separately; the shipped v0.8.1 status reader treats
that version as unknown rather than attributing the old record to a new revision.
No draining, zero-downtime, in-flight-work or release-readiness claim is made.

## Update and rollback

This is the **unreleased** retained Orka lifecycle, not functionality in v0.8.1.
Arrange a quiet window before updating: no draining, zero-downtime or in-flight-work
claim is made. Plain Deploy never changes occupied resources. Kagent, old/mixed/reused
placements and missing completion acknowledgments refuse; no enrollment or repair.

```bash
# Review a desired workspace version without contacting the cluster.
kmx deploy --to prod --update --plan
# Compose missing evaluation/publication, then update the eligible retained pair.
kmx deploy --to prod --update
# Or place an already-published passing version, including a previous version.
kmx deploy registry.example/team/agents@sha256:<passing-digest> --to prod \
  --evidence ../private/original-pass.json --promote --update --plan
kmx deploy registry.example/team/agents@sha256:<passing-digest> --to prod \
  --evidence ../private/original-pass.json --promote --update
kmx status --to prod
```

Digest update/rollback consumes the exact artifact and original full passing
receipt: no evaluation, build, publication or Task send. `--promote` still means
source-only evidence, **not evaluated on the destination**. Strict evidence mode
is not bypassed by a no-op. Digest `--plan` reads retained proof, current pair,
prerequisites and a bounded cluster-wide Agent inventory; it performs no auth
probe, admission, reservation or writes. Cold workspace plans remain offline and
only describe conditional stages: they do not establish live update eligibility.

Workspace updates check current shared/private baseline, target dependencies,
exclusive Provider references, patch permissions and retention before missing
stage spend. Historical exact-version full passing evidence is reused as historical
proof, including original evidence retained with a placement if its redundant
managed-pass cache is gone; it is never relabeled as a fresh evaluation. Publication
reuse is separately checked. Previously recorded placement generations cannot skip
a needed update after rollback. Update mode never rewrites those immutable
completion observations or serializes opaque authority into them.

An already-desired update verifies acknowledged full pair fingerprints, fresh
prerequisites and current-generation Ready without writes, a new Deploy ID or marker
repair. Workspace no-op skips evaluation and publication as unnecessary; it does
not claim fresh evaluation or verify/repair a different repository's publication.
Only inference-Provider resourceVersion drift may be tolerated between revisions
after unchanged authoritative-field reproof. Within one operation, proofs remain
exact. One reservation covers the pair; conditional patches recheck UID, generation,
whole spec and owned metadata. Known partials can reconcile only recorded old/new
states; unknown effects or checkpoint failures remain blocked. See recovery above.

## Create

Root `kmx create <name>` authors offline by default. Create and root `push`
share discovery: walk upward from the current directory to the nearest
`workspace.yaml`, including across Git repository boundaries. `--workspace DIR`
selects that exact root without fallback; if no marker exists there, create
initializes it explicitly without prompting.

Without a workspace, terminal create asks on stderr:
`No workspace found in <dir>. Start one here? [Y/n]`. Enter, `y` or `yes`
(case-insensitive) initializes the current directory. `n`/`no`, EOF, invalid
responses and interruption create nothing. Nonterminal input or redirected
diagnostics refuse with a `--workspace` hint; use an explicit root in scripts.
An invalid, unreadable, symlinked, oversized or nonregular nearest marker fails
closed, without fallback or an initialization offer.

```sh
mkdir my-agents && cd my-agents
git init
printf '%s\n' 'Answer briefly and in plain text.' > instructions.md

kmx create greeter --workspace . \
  --model qwen-3.5-2b --instructions ./instructions.md
```

To author the offline kagent profile instead:

```sh
kmx create greeter --workspace . --runtime kagent --model fixture-model --instructions ./instructions.md
```

Omit `--workspace` to use the same discovery/terminal-initialization rules.
Runtime is a member declaration, not a deployment-time switch. The private
`runtime.id`, `runtime.version` and `runtime.engine` select exact `v0.10.3`/`go`.
Optional `--runtime-version v0.10.3` and `--kagent-runtime go` may state the fixed
profile explicitly; Python and unsupported/contradictory declarations refuse.
Missing runtime in existing source stays historical Orka only; push/load derive
the packaged runtime, not a live target.

To author the offline AgentSessions chat profile in a separate workspace:

```sh
kmx create greeter --workspace ./sessions-workspace --runtime agentsessions \
  --model fixture-model --instructions ./instructions.md
kmx push --workspace ./sessions-workspace --to-layout ./sessions-layout greeter:v1
```

This fixes `runtime.version` to upstream commit
`ca85f47a2ec17ca6e16029cfa69419cfe29442b3` and `runtime.engine` to `chat`.
Optional `--runtime-version` accepts only that full commit; `--kagent-runtime`
and `--live --runtime agentsessions` refuse. The closed private
`metadata/kmx/native-agentsessions.json` binds the runtime/config contract and
member identities in manifest S; the complete ordered case inventory is the
member's public evaluation index. The public graph digest alone does not
identify execution semantics. The
build profile remains inert; `requiredImages` is explicitly empty. No host,
journal, model endpoint, credential or passing evidence is packaged or contacted.
The Go SDK pin includes upstream #120 and #124, but does not establish live
host qualification. Existing Orka/kagent evidence cannot authorize this profile.

`--instructions` names a file, not inline text. Its nonblank bytes are copied
unchanged into the member. `--description` is optional and single-line.
Instructions use the native block-text policy: LF and tabs are allowed; CR and other control characters are refused,
not normalized. An exact model is required; there is no inferred default.
With visible stdin and stderr terminals, create prompts for an omitted model or
instructions file before workspace initialization. It only asks for missing inputs,
preserving supplied flags; explicit blank flags and blank/EOF responses refuse.
Enter literal paths (including spaces), not inline text or shell expansions.
Nonterminal use still requires both `--model` and `--instructions`.
Live-only flags and `--live` itself are unknown before configuration loads;
create never bootstraps runtime/model or reads cluster settings. An explicitly
empty `--workspace` refuses. Required fields are checked before initialization.

A workspace supports only one member. Creating a second member refuses and
suggests a new `--workspace` directory, preserving authored files, metadata and
cases. Existing member directories, including PortableAgent bundles, are never
overwritten or migrated. For terminal creation using discovery instead of an
exact root, omit `--workspace` and answer the model/instructions-file prompts:

```sh
kmx create greeter
```

Alternatively, scripts provide both source inputs and select an exact root when
initializing a workspace:

```sh
kmx create greeter --workspace ./greeter --model qwen-3.5-2b --instructions ./instructions.md
```

A name is required; live create/previews/wizard are removed. Quickstart composes
fixed source, full eval, exact publication, reviewed Apply and placement Verify
before a separate fresh answer Task. Console selects authored native-Orka source,
reviews instructions/model/all cases, selects a Ready openai Provider, obtains
execution consent and reviews the exact retained Deploy plan before Apply/Verify.
Source fingerprint and complete API URI are rechecked. Console Ready means
placement only. Partial/evaluation errors can leave resources without retry/cleanup.
Author appropriate cases; scaffold arithmetic is not arbitrary-agent quality proof.

The source consists of `workspace.yaml`, `agents/greeter/member.yaml`, its
member-relative instruction file, workspace-root `eval/*.yaml`, and a pinned
build profile. The member's model
is exact; there is no deployment-time model substitution. Edit source values,
instructions and authored cases, not computed digest strings. The example case
is public arithmetic using existing evaluation-case syntax, not proof that your
chosen agent/model answers it.

Tools, skills, delegation, additional members and unsupported source fields are
refused. Paths must stay within the workspace, without symlink traversal. Source
reads are bounded, and credential-shaped content is refused.

## AgentSessions development evaluation

Select a pinned, operator-owned built-in `chat` host explicitly. This path uses
one fresh, non-fork Session and one never-retried Exec per declared case run, with
exact `system_prompt` config sent every time. It loads no Kubernetes settings.
No registry, authentication, chat/status or remote-host support is added; Deploy
is a [plan-only render](#agentsessions-deploy-plan).

```sh
kmx test --workspace ./sessions-workspace --development-host ./private/host.json \
  --bindings ./private/binding.yaml --out ./private/passing.json --case-timeout 30s
kmx push --workspace ./sessions-workspace --evaluation ./private/passing.json \
  --to-layout ./sessions-layout greeter:v1
```

The host JSON is closed, at most 16 KiB, in a private directory (0700) with a
private regular file (0600), without symlinks. Replace the digest placeholders
below with actual operator observations; IDs name your durable host/journal and
inference scope, not a tunnel port or DNS name. `runtimeBinaryDigest` identifies
the inspected binary; `runtimeConfigDigest` covers its noncredential execution
configuration; `inferenceRevision` identifies the observed model-route revision.
Never hash or include credentials. The exact runtime commit and chat/config
contract must match the packaged member.

```json
{
  "schemaVersion": 1,
  "address": "127.0.0.1:8080",
  "observation": {
    "mode": "operator-observed-development",
    "hostID": "owned-host",
    "journalID": "owned-journal",
    "runtimeVersion": "ca85f47a2ec17ca6e16029cfa69419cfe29442b3",
    "runtimeBinaryDigest": "sha256:<observed-binary-digest>",
    "runtimeConfigDigest": "sha256:<observed-config-digest>",
    "inferenceID": "owned-model-route",
    "inferenceRevision": "sha256:<observed-route-digest>",
    "model": "fixture-model",
    "harness": "chat",
    "harnessVersion": "1",
    "retention": "operator-managed"
  }
}
```

The address must be literal loopback, not `localhost`. `--bindings` must name an
[`AgentSessionsBinding`](#agentsessionsbinding) whose `model.name` is exactly the
packaged model; a mismatch refuses before any Session. The receipt records the
binding's digest once for the whole evaluation, including every judged or repeated
run. The host's model endpoint is an operator declaration and is not
checked against the binding. All cluster, namespace, inference and result-port
selectors refuse, including explicitly supplied defaults. The receipt is
version 2 (`kmx.agentsessions-suite.passing/v2`); a v1 receipt does not record a
binding and is unreadable, so evaluate again. A managed v1 proof is never selected: when it is the latest record, default push refuses with an
evaluate-again error rather than using an older record, and a new evaluation
registers after it. No declaration or transport failure triggers fallback. Before
Session creation, evaluation prints:
**“Development: unauthenticated operator-observed host; not authenticated remote promotion.”**
Instructions, inputs, answers and config are retained in the host journal in
cleartext under operator-managed retention; connection cleanup does not delete
them. Keep the pinned process/journal under operator control throughout the run.
Sessions has no authoritative host/build/model-advertisement endpoint here:
declarations and local inspection are not authenticated attestations, and an
unchanged observation file cannot detect a hostile process replacement.

Execution requires an intact canonical five-record chat journal, exact
harness/version/config/input/model-request fingerprint, attributable final text,
terminal success and EOF. Deltas and Session metadata are not answers. Host-file,
binding and source snapshots are rechecked around each run, after judging and before export.
Only completed attributable assertion failures count against the authored threshold;
unknown/partial/invalid evidence, drift, cancellation or output failure before
publication stops later execution with no new pass. The shared publisher can
leave a complete passing receipt after its exclusive link if a later directory
sync, readback or cancellation fails; evaluation returns nonzero and reports
**“output may exist”**. Inspect the file rather than assuming rollback removed it.
No incomplete receipt is accepted, and no published destination is automatically
deleted. Every Session and execution identity must be unique; no retry, recovery
or implicit old pass is selected.

Only a full policy pass exports the `kmx.agentsessions-suite.passing/v2` private proof,
including exact S/G/member/profile/config/model/case identities, operator host
observations, fresh Session/execution IDs and verified journal heads. Prompts,
answers, config bytes, endpoint locators and journal bodies are excluded. Push
consumes that explicit proof for exact S; evidence is not embedded in the suite
or published as a referrer. Orka/kagent evidence cannot authorize this member;
Sessions evidence feeds only the [plan-only Deploy render](#agentsessions-deploy-plan)
and is refused by receipt diff. Read-only Replay can
support diagnosis with zero model calls but cannot become fresh candidate proof.

The pinned daemon path has been demonstrated once with an instruction-sensitive,
keyless OpenAI-compatible fixture: two-case pass/evaluated push, instruction
failure with no export, and Replay/reference reconstruction with unchanged model
counter. That is runtime/config/journal qualification, not real-LLM quality or
production/authenticated host qualification. No model or host is installed by KMX.

### AgentSessionsBinding

An `AgentSessionsBinding` is the per-environment half of an AgentSessions
deployment. It lives outside the suite, one per environment. The suite pins the
exact model; the binding says where that model is served and where sessions are
placed. Its `model.name` must be exactly the packaged model (no case folding,
prefix or version matching); a different model is a new suite version and a new
evaluation. AgentSessions eval and deploy read it with `--bindings FILE` and
refuse a binding that serves another model.

```yaml
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: AgentSessionsBinding
target: {platform: kubernetes, id: agentsuite-demo}
placement: {backend: substrate}
atespace: agentsuite-demo
sandbox: {class: microvm, configName: microvm}
snapshot: {fidelity: MEMORY, storageLocation: "gs://ate-snapshots/agentsuite-demo/"}
resources: {cpu: "1", memory: 512Mi}
workerSelector:
  matchLabels: {workload: agentsuite-demo-microvm}
model:
  name: fixture-model
  provider: openai
  baseURL: http://fakemodel.agentsuite-demo.svc:8080/v1
  credential:
    secretRef: {name: model-credentials, key: api-key}
```

| Field | Rule |
|---|---|
| `target` | `platform: kubernetes`; `id` is a Kubernetes object name. |
| `placement.backend` | `substrate` only. |
| `atespace` | RFC 1123 label. |
| `sandbox` | `class` `microvm` or `gvisor`; `configName` names the cluster SandboxConfig. |
| `snapshot` | `fidelity` `MEMORY`, `ROOTFS` (micro-VM only) or `VOLUMES`; `storageLocation` is an object-storage URI of at most 959 bytes without credentials, query, fragment or `\`, and without empty path segments or segments of only `.` and spaces (one trailing `/` is allowed). |
| `resources` | Required `cpu` and `memory`: positive Kubernetes quantities of at most 32 characters with a decimal exponent (`e`/`E`) of at most two digits, cpu at most 999.999 cores. Substrate sizes and schedules the actor by them. |
| `workerSelector.matchLabels` | Optional Kubernetes label selector of at most 10 labels; an empty `matchLabels` is the same binding (and digest) as an omitted `workerSelector`. |
| `model` | `name` is the served model; `provider: openai` (OpenAI-compatible); optional HTTP(S) `baseURL`; `credential` sets exactly one reference (below). |
| `model.credential.secretRef` | `name` and `key` of a Kubernetes Secret holding the model key. |
| `model.credential.hostRef` | A credential source the AgentSessions host owns: `type` is a lowercase source name of at most 63 characters (the stock host implements `env`; a host can add others, such as workload identity for a keyless endpoint) and `name` is the reference that source resolves, an environment variable name for `env`. KMX never resolves it. |

Parsing is strict: one YAML (or JSON) document, known fields only, no anchors,
aliases or merge keys, and credential-shaped values refuse without being echoed.
The credential is referenced, never embedded: the binding, its digest and
anything recorded from it carry only the reference. `hostRef` has the shape of
an AgentSessions host model-binding credential reference (`{type, name}`). The binding digest is the SHA-256 of
the RFC 8785 (JCS) form of its JSON mapping, so comments, key order and
YAML/JSON spelling do not change it. Evaluation records this digest in its
evidence.

### AgentSessions deploy plan

```sh
kmx deploy suite@sha256:<digest> --from-layout ./sessions-layout \
  --harness-image registry.example/agentsessions/harnessnode@sha256:<digest> \
  --bindings ./prod-binding.yaml --evidence ./private/passing.json --plan -o json
```

AgentSessions deploy is plan-only. From full-passing AgentSessions evidence for
exactly this suite, the binding and a digest-pinned AgentSessions `harnessnode`
image built from the pinned runtime commit, it renders two documents offline:
a substrate `ActorTemplate` and an AgentSessions `HarnessRegistration` with a
substrate placement. It loads no cluster configuration, contacts no substrate or
AgentSessions host, and applies nothing; `--plan` is required, and
`--to`/`--to-context`/`--to-namespace`/`--inference`/`--promote`/`--update` refuse.

| Rendered field | Value |
|---|---|
| `ActorTemplate.metadata.name` | `<suite>-<member>-<hash8>`: the first 8 hex of `sha256(renderer version + "\n" + image reference + "\n" + binding digest)`, renderer version `kmx.agentsessions-actortemplate/v2`. The image reference is the full `--harness-image` value, so the same digest from a mirror or another repository is another template. At most 63 characters; suite and member are sanitized and truncated, never the hash. |
| `metadata.atespace` | `binding.atespace` |
| `containers[0].image` | The `--harness-image` reference. |
| `containers[0].env` | `HARNESS_ADDR=:80`, `HARNESS_READYZ=:8081`, `HARNESS_KIND=chat`, `HARNESS_MODEL=<packaged model>`. No model credentials, endpoint or instructions. |
| `containers[0].wakeup_probe` | `GET /readyz` on 8081, 120s. |
| `containers[0].security_context` | `capabilities: {}`, adding none. |
| `resources` | `limits` `cpu` and `memory` from `binding.resources`: the actor's size. |
| `sandbox_config` | `binding.sandbox`; `sandbox_class` is `microvm` or `gvisor`. |
| `snapshot_config` | `binding.snapshot.fidelity`; `storage_location` is `binding.snapshot.storageLocation` + `<name>/`. |
| `worker_selector` | `binding.workerSelector` |
| `HarnessRegistration` | `metadata.name` is the member; `spec.substrate {atespace, template}`, `RESUMABILITY_STATELESS_REPLAY`, `descriptor_id: chat`. The plan reports the host's `spec_digest`. |

Both documents use proto field and enum names. The `ActorTemplate` targets
agent-substrate/substrate main at `f4f7b806255ffdc824391b857dbbf329a2556402`;
earlier revisions (before the string `sandbox_class` and required template
`resources`) refuse it. `scripts/validate-agentsessions-render.sh` checks out that
commit and runs the golden renders through its protojson decoding and
`CreateActorTemplate` validation; it needs network access and is not part of CI.
The plan also reports the host
model references (`binding.model`), the chat `configDigest` every Session must
send (the packaged instructions as `system_prompt`) and both binding digests: the
one the evaluation recorded and the one being rendered. They may differ, since
each environment has its own binding; both serve the packaged model. Evidence
remains development-host evidence, not evaluation on the rendered target.
The member is the harness name, a DNS label of at most 63 characters; `chat`
and `echo` refuse because the host serves those harnesses itself. A host serves a substrate registration only when built with a substrate
placement backend.

## Evaluate the whole workspace

For Orka, provision the pinned v0.2.0 harness-v2 runtime, a Ready
OpenAI-compatible inference Provider with its referenced Secret, and the existing
result-access ServiceAccount/Task-get grant separately. For kagent, provision
exact v0.10.3 Go, author a `KagentBindings` file, and provision its referenced
Secret. Evaluation does not install or upgrade anything, build images or write
Secrets. The packaged member model is exact; bindings do not replace it. Required
member images remain `[]`.

```sh
mkdir -m 700 ../private-evidence
kmx test --to-context kind-demo --inference provider:inference \
  --out ../private-evidence/passing.json
kmx push --evaluation ../private-evidence/passing.json ghcr.io/me/my-agents:v1
```

Root `test` discovers the nearest workspace, or selects an exact root without
fallback using `--workspace DIR`. It accepts no positional package reference.
A destination context (explicit `--to-context`, named `--to`, or saved `evalTarget`)
is required. Root test stores completed passing metadata privately under KMX
state; `--out` optionally also writes a new standalone receipt. Orka requires
`--inference provider:NAME`, directly or
through the named destination;
kagent instead requires `--bindings FILE`, never both. For Orka, namespace
is `orka-system` by default (`--to-namespace`). Without `--result-port`, kubectl
chooses and retains a free IPv4 loopback result port, with no bind-close-reuse
window. An explicit `--result-port` is honored without fallback; an occupied
port or unproven bind reports a result-port error, not an unknown evaluation.
Result port numbers are transport details, not evidence identities. Each run
has a separate `--case-timeout` (default 5m, maximum 9m).
Explicit empty selectors and global `--context` are refused. No runtime/model
substitution, case subset or `--show-content` is available. Orka/kagent and AgentSessions development evaluation support
`contains`, `notContains`, `regex` and explicitly configured `llmJudge` assertions. Unsupported evaluators,
receipt-budget overflow and invalid output destinations refuse offline before
loading cluster configuration.

Output defaults to `-o text` for all runtimes. Orka-only `-o json` writes one
payload-free outcome document on success or execution abort; run lines, guard
messages and other diagnostics go to stderr. Invalid formats and JSON selection
for kagent/AgentSessions refuse before cluster configuration. This report is not
passing evidence and does not replace `--out` or managed proof.

Orka/kagent and AgentSessions development cases optionally declare repeated execution:

```yaml
id: capital
input: What is the capital of France?
expectContains: [Paris]
runs: 5
minPasses: 4
```

`runs` defaults to 1; omitted `minPasses` requires every declared run to pass.
`minPasses` requires an explicit `runs`. Both must be integers satisfying
`1 <= minPasses <= runs <= 100`. Receipt-size
projection can impose a lower limit and refuses offline before configuration.
Each run sends a fresh Task with the same authored input, never a retry. Each
run gets the configured deadline, including its judge requests and attribution
checks. When cases repeat, preflight prints the sum of all run-time budgets
(`runs × --case-timeout`, summed across cases), excluding placement; this is an
upper bound, not a prediction, and cancellation can stop it earlier. Every
run must finish, even after enough passes or failures are known. Any unknown
assertion/judge outcome makes the case unknown. Only completed, attributable
assertion failures count as failing samples against the threshold; execution
failure, missing answer, drift or cancellation still aborts later sends.
Preflight discloses the total Task count and two judge requests per assertion
per run. AgentSessions uses fresh Session/config/Exec rather than Tasks; its default
one-run text receipt encoding is unchanged.

An unsuccessful terminal Orka Task reports `execution failed (assertions not
evaluated)`, not an assertion failure or an underlying runtime cause. Abort
summaries distinguish returned TaskUIDs from completed assertions: for example,
`1 of 3 runs executed; 0 passed; aborted on execution error; 2 not executed`,
followed by the cleanup outcome. `total` remains planned, `executed` counts
nonempty TaskUIDs, `notExecuted` is the remainder, and `aborted` identifies an
execution error rather than a threshold miss. Root test JSON includes these
counts plus `passed`, `total`, `status`, `detail`, `cleanup` and a fixed safe
`error` on failure. Composed Deploy adds the execution fields to its existing
`eval` stage; GET-only cached skips omit them rather than claiming fresh Tasks.
A fully executed threshold miss retains `N/total runs passed`; an assertion fail
followed by two passes completes a three-run, two-pass policy as `2/3`.
Abort, cleanup and no-publication behavior are unchanged; cleanup failures never
claim cleanup completed.

Passing private receipts store contiguous per-run rows, with `run` ordinals and
the declared `runs`/`minPasses`, actual pass/fail verdicts, assertion evidence and
unique native identities. Only complete groups meeting the threshold export.
Default one-run receipt encoding is unchanged; older strict readers refuse
repeat rows. Diff compares per-run outcomes without treating different Task IDs,
answer hashes or judge scores as identity changes; the authored repeat policy
and judge configuration remain comparison identities.

The field names, defaults and validation follow
[AgentSuite §8.2.2 — Case documents and assertions](https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions).
KMX uses private YAML authoring and receipts rather than the spec's embedded
JSON format. Its 100-run cap is an additional execution resource limit, allowed
by [§8.2.1](https://github.com/agent-primitives/suite/blob/main/spec.md#821-index-and-bounds); it refuses larger policies
rather than truncating them.

For an open-ended answer, author a judge assertion in the ordinary case file:

```yaml
id: explanation
input: Explain why the sky appears blue.
assertions:
  - id: quality
    type: llmJudge
    rubric: Explain scattering of sunlight without claiming the sky emits blue light.
    minScore: 70
    judgeModel: my-judge-model
    judgeVersion: pinned-v1
```

Select an explicit OpenAI-compatible endpoint with `--judge-base-url` and,
optionally, nominate its HTTPS credential environment variable with
`--judge-key-env`. There is no endpoint or agent-model fallback. HTTPS is required
except keyless HTTP at literal loopback. Input, attributed answer and rubric go
to that provider. Preflight states the cost: **two independent requests
per judge assertion per run**, sharing that run's deadline. Both
scores at/above the threshold pass, both below fail, and disagreement, malformed
or unavailable responses are unknown. There are no retries or candidate Tasks beyond the authored runs. Native
attribution is checked again after judging; late drift/deadline
prevents a case pass and export. Failure summaries include assertion IDs/types and
both judge scores versus the threshold; unavailable or split outcomes have safe
classes. Only `--show-content` prints sanitized bounded rationale and answer
excerpts on stderr, including in composed Deploy. `deploy --verbose` alone does not
opt into content. The storage-only policy wording clarification leaves the
historical v2 evidence fingerprint, scoring rules and existing receipts unchanged.
Choose a judge model strong enough for the rubric: two agreeing scores do not
make an incorrect judgment correct; validate it against known pass/fail examples.

All judges use the fixed `openai-chat-completions-v1` Chat Completions style:
`max_completion_tokens: 1024`, `stream: false`, and no `temperature` or other
sampling override. This shape is also accepted by the pinned AIKit/LocalAI CI
judge. There is no style flag, model-name inference, retry or error fallback.
The token budget includes reasoning and visible output; a truncated response
remains unknown. Receipts bind `requestStyle` and include it in the configuration
digest. Earlier judge receipts without this binding require fresh evaluation;
text-only receipts are unchanged.

The returned model must equal exactly the authored `judgeModel`, or exactly
`judgeModel + "-" + judgeVersion`. There is no prefix, substring, case-folding or
whitespace matching. Each score has an aligned `modelMatches` entry:
`requested` or `version-qualified`. A missing or mismatched sample retains
neither partial scores nor match forms. Match forms are observations, not a
change to the authored judge identity for receipt comparison.

Passing receipt rows retain the authored model/version, threshold, policy digest,
both scores and a configuration hash binding the canonical endpoint. Raw judge
URLs are omitted from receipts and refusal recovery commands; supply the endpoint
again when retrying. This is user-declared identity, not provider attestation.
Changed authored identity invalidates exact-suite evidence; changed endpoint-bound
identity makes receipt comparisons incompatible. Offline push/deploy do not select
or contact a current judge endpoint. Text-only receipt encoding stays unchanged;
upgrade receipt readers before exchanging judge receipts. AgentSessions
`--development-host` uses this same explicit judge transport and per-run deadline. The assertion fields are unchanged
and can map directly to authored JSON cases; this does not migrate the case tree.

One mutation guard covers placement, every ordered case and Orka candidate
cleanup. Public Orka eval creates a fresh digest/run-scoped Provider/Agent pair;
it never adopts, updates or reuses an earlier candidate. The logical packaged
member, model and cases stay unchanged. Every KMX-created candidate Provider,
Agent and Task carries the full run-ID annotation; Orka-created descendants are
tracked through exact owner-UID chains, not presumed annotation inheritance.
Startup reports possible leftovers and unknown discovery without deleting them
or blocking a fresh evaluation. Run liveness is unknown.

Remote evaluation retains context-naming confirmation; local kind retains its
banner. Private Quickstart strict reuse and console canonical create/update are
unchanged. Kagent placement and all runtime-specific attribution remain unchanged.
Each declared run sends one fresh Task exactly once; ambiguous writes are never recovered
by name, retried or resent. Source, runtime, target and bindings bracket execution.
These observations do not prove drift absence or authentication.

Kagent uses a closed author-owned `KagentBindings` file (namespace, OpenAI
provider/baseURL, separately provisioned Secret name/explicit key). The model
comes only from the packaged member. Explicit `--to-namespace` must agree with
bindings; `--result-port` refuses. For example:

```sh
kmx test --workspace ./kagent-workspace --to-context kind-demo \
  --bindings ./private/kagent-bindings.yaml --out ../private-evidence/kagent-pass.json
```

Kagent evaluation requires **auth: unsecure only, trusted-proxy refused** before
admission and before native sends. It creates new suite-owned ModelConfig/Agent
objects; occupied unrecorded targets refuse, never adopt/update/delete-recreate.
Each declared run sends one fresh blocking A2A Task with no prior Task/context IDs.
Ambiguous transport, foreign history, reused native identities or changed
placement/runtime/binding stop later sends. Consent discloses full prompt,
answer and history retention in kagent's database (upstream default unlimited),
Service-proxy audit bodies and model transfers. No auth settings are modified.

For Orka, the temporary result session uses the selected ServiceAccount's **full effective
authority**. Discarding its bearer is not revocation; Task-read enforcement is
unverified for v0.2.0. One pinned loopback connection is opened per run, without
reconnect. Default summaries omit prompts, answers, assertion operands and
reasoning. `test --show-content` and executing workspace `deploy --show-content`
opt into expected values/patterns, short judge rationale and answer excerpts on
stderr. Orka outcome JSON stays one payload-free document with non-pass metadata.
The flag is refused for deploy-only or plan-only Deploy. Request inputs/rubrics are
not copied into diagnostic fields, although answers can quote user text.
Receipts still contain no content or references to diagnostics.
Runtime/model storage and Kubernetes audit policy may retain inputs and answers.
Published suites themselves contain authored inputs.

Failure content is diagnostic-only, never evidence, signed data or a portable
export. Non-pass runs are retained locally under
`KMX_HOME/eval-failures/<canonical-workspace-root-hash>/`, separately from passing
proof and candidate state. Each answer is bounded to 64 KiB; rationale is bounded to 512 UTF-8 bytes per sample,
expected values to 512 bytes, and displayed excerpts to 512 bytes per assertion
and 2 KiB total per run. Forbidden-text failures show an excerpt around the match;
a missing positive match or judge failure shows a labeled leading excerpt, not
an invented failure span. Failed individual runs are reported even if their group
later meets minPasses. Unknown outcomes with unverified attribution expose no
answer or rationale.

### Inspect or clear local evaluation failures

```bash
kmx test explain --workspace ./workspace
kmx test explain f-<32-hex-digits> --workspace ./workspace
kmx test explain f-<32-hex-digits> --workspace ./workspace --show-content
kmx test explain f-<32-hex-digits> --workspace ./workspace --clear
kmx test explain --workspace ./workspace --clear
```

Saved runs print an opaque `f-` diagnostic ID. Inspection is offline: it reads the
workspace descriptor and local records, not members/cases, operational config,
Kubernetes, a registry or a model. Without `--workspace`, discover the nearest
workspace. Without an ID, list records; with an ID, show that record's assertions
and provenance. `--show-content` explicitly adds the same bounded excerpts,
expected values and rationale as live diagnostics, on stdout. Empty or withheld
rationale is identified rather than presented as an empty explanation.

Keep the last **10 individual non-pass runs per workspace across suite revisions**,
including failures in groups that later meet minPasses. Each record retains its
suite digest, runtime/member, operation, case/run and completion/attribution
metadata. The maximum is **128 KiB of serialized JSON per record** (1.25 MiB of
visible records per partition), plus one bounded staging file during a write.
Overflow deterministically removes the answer, then trailing excerpts, then
trailing rationale/operands, then trailing assertion entries, with explicit
truncation and omitted-assertion metadata. This is not a global limit across
unlimited distinct workspaces.

Managed directories are created 0700; files and the persistent nonblocking OS lock
are 0600 and owned by the current user. Insecure existing state, symlinks, hard
links, special files and corrupt/oversized records refuse rather than being
repaired or chmodded. A bounded owner-only `.pending` staging file is never read
or displayed and does not hide healthy completed records. It blocks new saves
without evicting history until explicit whole-workspace `--clear` removes it.
Other corrupt or unsafe state still refuses.

Retention is best effort and happens before diagnostic output, including on
aborts. At capacity the oldest record is removed before staging the replacement,
so a subsequent write failure can leave nine completed records; the hard space
bound takes priority. A fixed warning reports unavailable or unconfirmed retention
without changing the evaluation outcome or causing another send. If an error
occurs after publication, the observable ID is still reported, but durability
remains unconfirmed (`retentionUnavailable: true`); that flag does not prove the
record is absent. Diagnostic output errors still stop subsequent effects.

`--clear` with an ID removes only that validated record. Clearing all requires
an explicitly supplied `--workspace`; it also removes a validated owner-only
staging file, but never reaches another workspace, passing proof or candidate
state. The completion message counts removed diagnostic files, including staging. `--clear` and `--show-content` are mutually
exclusive. Clearing is ordinary local file deletion, not secure erasure of disk
snapshots, backups or upstream runtime/model history. The private lock and empty
directories may remain. These unsigned diagnostics cannot authorize publication
or Deploy, and receipts contain no diagnostic references.

Before truncation, diagnostics check full raw and terminal-normalized content for
known credential shapes and the configured judge key. Credential-shaped,
header-like and invalid-UTF-8 fields are withheld, not partially redacted. Clipped
fields are checked again so a newly formed boundary cannot introduce a credential
shape; an unsafe clipped field is withheld without dropping the whole record.
Conservative header checks can withhold consecutive Markdown labels, bare URL
lines, times and other colon-delimited text that also parses as header fields.
Clean checks do not prove arbitrary secrets or personal data are absent; only opt
into content on a private terminal. No raw transport/subprocess errors, headers
or judge endpoint URLs are retained in these diagnostic fields. Known typed
transport failures preserve fixed classes (timeout, connection-refused, EOF,
TLS or HTTP status); kagent subprocess failures use stage classes, never inferred
HTTP/network causes. Cleanup does not erase independently retained upstream data.

A missed pass threshold, unknown results, execution failure, drift, cancellation
or output failure stops later cases
and exits nonzero. Public Orka eval attempts bounded, detached cleanup after pass,
failure or cancellation. Only independently acknowledged roots can be deleted,
using fresh UID/resourceVersion preconditions; Task absence alone does not prove
Job/Pod absence. Shared inference, Secrets, namespaces and RBAC stay untouched.
A full pass exports only after verified root/child absence and final original
source/shared-input checks. Incomplete or uncertain cleanup fails that eval and
exports nothing; it does not invalidate older completed evidence or block later
publication. There is no cleanup journal, lease or automatic leftover sweep.

The private passing file is 0600 in an existing 0700 directory outside workspace
and cache trees, never replaced. A late publication or summary failure can leave
a complete output; inspect it rather than assuming failure removed it. Receipts
retain identities, digests, assertion verdicts and bounded judge metadata, not
payloads or execution history. Runtime/model/audit retention may outlive cleanup.

### Inspect and clean candidate leftovers

```sh
kmx test cleanup --to dev
kmx test cleanup <full-run-id> --to dev
# Explicit access also works without the original workspace or inference:
kmx test cleanup <full-run-id> --to-context kind-demo --to-namespace orka-system
```

Without a run ID this is read-only: no prompt, deletion or passing receipt. A full
32-character lowercase hexadecimal run ID selects one run, never a prefix or all
runs. Cleanup previews the frozen target and resource UIDs, then requires
context-naming confirmation even on local kind. Noninteractive use requires
`KAIMAHI_CONFIRM=<context>`. Named `--to` reads only copied destination selectors;
cleanup does not evaluate source, require inference or follow saved `evalTarget`.

Cleanup can interrupt a live run; no lease establishes abandonment. Identity,
ownership and scope are rechecked before UID/resourceVersion-preconditioned
root deletion and verified child absence. Unanchored descendants, missing
Task/Job links or unverifiable selection are reported as uncertain and refused,
not guessed from labels or partial names. Existing Task names retain only part
of the run ID, so some post-crash orphans require independent operator inspection.
A missing root is not itself proof of complete cleanup.

Discovery is namespace-wide, not filtered by labels that descendants may lack.
Inventories allow four pages of 128 objects per kind, at most 4 MiB per page,
and 512 objects across a snapshot, including unrelated resources. Cleanup has
one 90-second budget. Large namespaces, unreadable pages or stuck finalizers
fail closed; these limits are not proof that a selected run has no leftovers.
Prefer a dedicated evaluation namespace. If a limit prevents recovery, have the
cluster owner inspect retained resources and reduce unrelated inventory through
their own verified-ownership process, then rerun read-only `kmx test cleanup`.
Missing anchors instead require independent operator-reviewed, UID-specific
inspection/cleanup; do not infer ownership from labels, prefixes or age, or use
a namespace-wide delete to work around a refusal. KMX never authorizes shared
inference, Secret or namespace deletion through this helper.

`push --evaluation FILE` reads that strict private file and requires the exact
prepared manifest digest plus full offline case/subject coherence before transfer
or provenance. Equivalent authored spelling with the same artifact identity is
allowed; a changed case/model/instruction artifact is refused. Unsigned receipts
are observations, not authentication, policy gates or release authorization.
Root push requires matching completed evidence by default; `--unevaluated`
explicitly opts out. Extracted-directory `suite push` does not gain the
`--evaluation` flag. `test diff` reads strict passing suite receipts and
compares only compatible same-runtime subjects/case inventories; Orka/kagent
comparison refuses as incompatible, never as equivalent behavior. Comparison
also requires identical observed ownership. Fresh Orka candidate runs and separate
kagent CLI runs have different identities, so their receipts are incompatible
rather than a cross-run behavior comparison. Root Deploy dispatches only matching runtime evidence;
cross-runtime evidence refuses. Old Orka/kagent/AgentSessions bundle receipts are
unreadable. `test from-sessions` remains private reference extraction/export only,
with no native manifest consumer. RecordedExecution/legacy replay/judges are removed.

### Qualification boundary

The existing Orka runtime CI shard uses the pinned CPU AIKit workload, two text
cases and one configured rubric judge in exclusively owned kind clusters. It
checks historical API identity/spec/input/terminal metadata, actual UID/RV delete
preconditions and independent root/child absence after candidate evaluation.
Source answers, assertion verdicts and judge scores are product-observed after
cleanup, not independently re-fetched from deleted Tasks. Exact S transfers through
a local layout and pinned registry; source-withheld Deploy requires explicit
`--promote`, GET-only planning and an absent canonical destination. Fresh destination
answers and no-effect refusal audits remain independent checks.

The audit records intent before forwarding, distinguishes admission, persistent
writes and Task attempts, and refuses incomplete requests. Only passing metadata
and a payload-free qualification report may be uploaded. Runtime/model/audit
storage may retain inputs. This fixed scenario does not establish general
execution, authenticated approval or orphan recovery; hermetic tests alone are
not live runtime/model qualification.
The same shard also prepares a second exclusively owned kind cluster with the
same pinned Orka in `orka-system`. It shares the real AIKit model through a
fixture-only NodePort on the private kind network, without a host port mapping;
the destination has its own inference Provider and Secret. With source withheld,
registry `deploy --promote --plan` must be GET-only and apply must complete both
server preflights before beginning creation of an absent Provider/Agent pair.
Independent destination identity/spec/marker readbacks and one fresh Task with
authored answer assertions qualify creation and execution. A separate Ready
Provider declaring a mismatched model must refuse before admission, writes or
Task attempts, leaving the target empty. That same Provider first passes a
matching-model plan control; only its model declaration is then changed for the
refusal. The genuine receipt remains source-only evidence, not destination
evaluation or independent model-server qualification. No second model workload
or timeout increase is required. Partial CLI outcomes are not established by
this fixed scenario.

The dedicated kagent shard separately qualifies packaged evaluation and
source-free digest Deploy into a fresh prepared namespace, including GET-only
planning, ordered creation and occupied repeat refusal without another native
execution. Standalone package evaluation remains unavailable.

## Export portable evaluation evidence

```sh
kmx test --to dev
kmx test export --workspace ./workspace --member greeter --out ../evidence.json
```

`test export` is offline. It prepares the exact selected workspace snapshot,
requires its sole member to match `--member`, selects the latest completed
managed record for that workspace and OCI manifest digest, and verifies the
public projection with [suite v0.3.1's verifier](https://github.com/agent-primitives/suite/blob/v0.3.1/spec.md#824-portable-evaluation-evidence)
before writing. There are no model, cluster, registry or operational configuration
calls. Multi-member workspaces, subsets and runtime/model overrides refuse.

The detached document binds the full raw-byte OCI manifest descriptor, selected
member/composition/platform, runtime/model/harness/renderer/profile, public index
and ordered case digests, authored repeat policy, every run/assertion outcome,
judge scores/configuration, evaluator identity and evaluation times. Failed runs
absorbed by a passing `minPasses` policy remain visible. Unknown, missing, split
judging, cancellation and execution errors never become passing evidence.

**Fresh identified build provenance is required.** Managed evaluations capture
the producer's Go build info on every platform and accept either:

- A tagged module `Main.Version` plus its `Main.Sum` content checksum. Tagged
  `go install module@vX.Y.Z` builds qualify without VCS settings.
- A full `vcs.revision` plus explicit `vcs.modified: false`, as captured by release
  binaries and clean checkout builds. This also permits development or
  pseudo-version builds when the complete clean revision is recorded.

An explicitly modified producer always refuses, even with a tag/checksum.
Unstamped `(devel)` builds, missing/invalid identity and pseudo-versions without a
clean full revision refuse. Module version alone is insufficient. The captured
identity is never replaced by the exporting binary's identity. Inspect
`go version -m <binary>` rather than inferring provenance from a clean folder:
some toolchains omit VCS settings in linked worktrees. Use a tagged module build
with its checksum or a normal clean checkout in that case, then evaluate again.
The checksum identifies module content addressable through Go's checksum database;
offline export does not query that database or attest the compiled executable.

KMX-owned renderer/profile digests identify the canonical build-source projection,
not executable bytes or authenticated provenance. An optional binary digest may
be captured from reliable Linux `/proc/self/exe`; its absence is allowed on every
platform and it remains private companion metadata, not a public-schema
extension or a substitute for the required module/VCS provenance. Runtime/harness
image or declared binary identities remain distinct from the producer source
identity.

Public configuration hashes are documented allowlisted projections of exact model
protocol/identifier, supported native request contract, public suite/profile
identities, producer source provenance and evaluator/judge policy controls. They
are not existing private binding/render/judge configuration hashes: those can
contain endpoint, Secret, physical-object or prompt-derived inputs. No prompts,
answers, rubrics, reasoning, endpoints, credentials, Secret references or private
host/session/object IDs are copied into the public document. Judge response model
forms are reconstructed only from already verified exact/version-qualified matches.
Producer/model labels outside the public evidence token grammar refuse; they are
not renamed to create a match. Identifier shape checks are not secret detection.

Times are captured during evaluation, after runtime cleanup, deferred client/output
closes and final input checks for completion. Export time and file modification
time are never substituted. Older records without the companion metadata require
a fresh evaluation; export does not scan backward for an older exportable record.
The private receipt and managed completion envelope stay unchanged. Companion
records are bound to workspace, suite digest, sequence and canonical receipt hash;
only a matching committed pair is exportable. Orphans cannot authorize anything.
Companions are limited to 4 KiB each and 256 per partition (1 MiB), with no automatic
deletion. Failed capture or companion capacity/publication leaves ordinary private
passing proof usable but public export unavailable. At capacity, deliberately
select a different private `KMX_HOME` and evaluate again; do not delete a lock or
record to bypass a running operation.

`--out` names a new file in an existing safe directory outside workspace, suite
cache and managed passing state. Existing files and unsafe/symlinked destinations
refuse. Public-output parents may be readable (0755) but not group/world-writable;
files use exclusive staged publication and mode 0600. Private receipt/companion
parents remain private (0700). Results belong beside the suite, never inside its
content layer: embedding them would change the suite digest they claim to cover.

Consumers supply an independently resolved expected subject/policy to
`agentsuite.VerifyEvaluation(layoutPath, expected, reader)`. A UI should show
`evaluation: not verified` on refusal; a CI consumer should fail closed. This
checks unsigned consistency, not who ran the evaluation, whether observations
are true, model attestation, trusted time or deployment readiness. It does not
replace private `--evaluation`/`--evidence` push/Deploy gates. The offline CLI
consumer is described below; signatures and registry-referrer transport are not
implemented. Offline fixture tests are not live execution qualification.

## Verify portable evaluation evidence in CI

Commit a **reviewed `expected.json` beside the suite**. This is your acceptance
policy, not an export of the untrusted claims you are checking. For example:

```text
verification/
  suite-layout/      # local OCI layout, including all referenced blobs
  evidence.json      # detached public evidence
  expected.json      # reviewed and version-controlled ExpectedEvaluation
```

After checking out these inputs and obtaining a pinned KMX binary during CI
setup, run the gate without network access or KMX configuration/state:

```sh
set -eu
kmx test verify --suite ./verification/suite-layout \
  --evidence ./verification/evidence.json \
  --expect ./verification/expected.json -o json
```

The digest form pins the same local artifact explicitly. It **still needs the
bytes**; it never fetches them or searches a private preparation cache:

```sh
kmx test verify --suite sha256:<reviewed-manifest-digest> \
  --from-layout ./verification/suite-layout \
  --evidence ./verification/evidence.json \
  --expect ./verification/expected.json
```

The explicit digest must match `expected.subject.manifest.digest`, and shared
verification binds that descriptor to the actual raw manifest bytes and complete
content graph. Only a single-manifest OCI layout is accepted; an extracted suite
directory or multi-reference layout refuses. The pinned suite v0.3.1 loader
refuses FIFOs, devices, sockets and symlinks in OCI metadata/referenced blobs,
and symlinked blob directories; these are `artifact` refusals, not blocking reads.
Path-form `--suite` selectors cannot
contain `:` or `@`, or begin with `//`: these reference-shaped inputs refuse even
if a similarly named local directory exists. Verification does not prepare a
workspace, read a managed receipt, load operational configuration, create KMX
state, or call a runtime, model, cluster, registry or checksum database.

### The reviewed expectation file

`--expect` requires the complete shared
[`agentsuite.ExpectedEvaluation`](https://github.com/agent-primitives/suite/blob/v0.3.1/evidence_types.go),
not a subject-only JSON object and not an evidence document:

| Required section | What reviewers pin independently |
|---|---|
| `subject` | Full OCI manifest media type/digest/size, member ID/digest, composition and build-profile digests, platform OS/architecture/variant |
| `execution` | Runtime, model, harness, renderer, profile and non-secret configuration identities/digests |
| `evaluator` | Accepted evaluator ID/version and policy ID/version/configuration digest |
| `judges` | Explicit allowlist of complete model/version/policy configurations; `[]` when no judge is accepted |

Include all fields, even `platform.variant: ""` and `judges: []`; null, missing,
unknown or duplicate fields refuse. Identities and configuration digests must
come from independently reviewed artifact descriptors and accepted producer/
execution contracts, not from selecting fields out of `evidence.json`. The
[export contract](#export-portable-evaluation-evidence) explains KMX's non-secret
configuration projections; other producers need their own reviewed contract.

Review expectation changes like policy changes, using your repository's required
review or ownership rules. Do not automatically regenerate `expected.json` from
new evidence to make a red gate pass, and do not let an untrusted change rewrite
both the evidence and its acceptance policy without that review. KMX cannot prove
that your expectation file was reviewed or independently authored.

### Output and refusal contract

On success, exit status is **0**. Text output is a bounded verified summary;
`-o json` emits one shared `VerifiedEvaluation` document with the expected
subject, case/run counts, claimed completion time and `unsigned: true`.
Completion time is an unsigned claim, not a freshness guarantee. JSON is
success-only; a refusal prints no success document to stdout.

On refusal, exit status is **1**, with a fixed explanation on stderr:
`evaluation: not verified [CODE]: …`. The typed shared refusal remains available
to Go callers. Parser details, file paths and unverified payloads are not echoed.

| Code | Meaning |
|---|---|
| `missing` | Required input or semantic identity is absent |
| `malformed` | Input JSON/file shape is invalid or cannot be read safely |
| `unsupported` | Schema, policy or feature version is unsupported |
| `limit` | An input or declared evaluation exceeds supported bounds |
| `artifact` | Local suite layout/content is absent, invalid or unsupported |
| `subject` | Digest, member, platform or execution identity does not match |
| `coverage` | Ordered cases, repeats, runs or assertions are incomplete/mismatched |
| `policy` | Evaluator/judge configuration or recomputed outcomes disagree |
| `unknown` | Execution or assertion observations are unknown/unavailable |
| `not-passing` | Complete known outcomes do not pass the authored policy |

Evidence and expectation files must be regular, non-symlink inputs, each at most
4 MiB. Expectation JSON nesting/member-limit parser errors are `malformed`;
semantic verifier limits retain the shared `limit` code. Usage errors and output
write failures also exit 1, but are not verification refusals. A summary write
failure never becomes a successful gate.

This checks **unsigned consistency**, not who ran the evaluation, whether the
observations are true, model/binary attestation, trusted time or deployment
readiness. It does not authorize private evaluated push or Deploy.

## Publish

```sh
kmx test                             # saved evalTarget, no --out needed
kmx push ghcr.io/me/my-agents:v1       # supplied tag stays v1
kmx push                             # saved publishRepository:s-<full-S-hex>
```

Legacy native root push requires completed full-passing evidence for the exact prepared OCI
manifest S by default. It selects the latest completed pass for this workspace/S,
validates its packaged runtime/cases, and identifies the selected record on stderr
before transfer. It does not evaluate or contact the cluster to repair missing proof.
`--evaluation FILE` selects an explicit standalone receipt with no lookup fallback.
`--unevaluated` explicitly publishes without evidence; it cannot be combined with
`--evaluation`. Extracted-directory `suite push` remains ordinary artifact transfer.

With an explicit `REPO:TAG`, the tag is unchanged, including force/replacement rules.
Only bare push derives `s-<full-S-hex>` from the same frozen graph it transfers;
it requires `publishRepository` in workspace `kmx.yaml`. A local `--to-layout`
target still needs an explicit reference. TLS, exact-host HTTP opt-in, credential
and provenance defaults remain unchanged.

### Portable directory modes and preparation-cache upgrade

Loose-directory packaging follows AgentSuite v0.6.0: regular files default to
non-executable `0644`, directories to `0755`, and supported symlinks to `0777`.
ToolProvider payload inventories supply their exact declared modes, including
executable, zero and directory/link modes; conflicting declarations refuse.
Host execute bits do not select archive modes. Host access/security checks still
apply independently. Git executable-mode checks for source provenance are not
portable content metadata and remain separate.

Existing OCI artifacts are different: their stored tar modes and exact manifest
identity S remain authoritative. Transfer or verify the original OCI layout or
registry artifact to retain that identity. Repacking a loose extraction can
produce a different layer and S even when file bytes and the public graph match.
Do not relabel old artifacts, edit receipts, or use graph equality instead of S.
Recompute saved loose-directory diff/content identities with the selected reader.
Changing an authored inventory also requires rebinding its variant, provider,
composition/dependency pins and ancestor descriptors as applicable.

Preparation entries carry a packing-policy marker. An entry without the current
marker refuses before cache reuse, evaluation, publication or composed Lift,
with a `packaging policy` remedy. This includes older entries with identical file
bytes: byte equality alone does not establish the tar-mode policy. No entry,
receipt or deployed resource is automatically repaired or removed.

To recover, stop operations using that workspace and **move aside only the
identified prepared-cache entry**, then rerun the command. Ordinary preparation
errors name that entry under `KMX_HOME/suites/<source-fingerprint-hex>/` (or the
platform KMX state location). Public evidence export and eval use payload-free
errors; retry the workspace's ordinary preparation through `kmx push` to identify
the entry if needed—the old entry refuses before transfer. Keep
`KMX_HOME/passing/` and standalone receipts; do not clear all KMX state or add the
marker to an old entry by hand. Avoid sharing preparation entries between kmx
versions: an older binary refuses the new record with an `unknown field
packaging` error. For downgrade or mixed-version use, keep version-specific KMX
state or move aside only the selected entry with operations stopped; do not
delete passing evidence.

Normal generated native packages already used non-executable regular files, so
this cache-format upgrade preserves their artifact S and authored fingerprint.
Repreparation with the same S retains eligible exact-S managed, explicit and
public proof. If S changes, previous proof cannot qualify the new artifact:
rerun `kmx test` before evaluated `kmx push`, and obtain fresh approval/signing
where required. An explicit mismatched `--evaluation` refuses without falling
back; managed lookup considers only the new exact-S partition. Source-free digest
Lift still consumes the original artifact and its matching receipt, without
repreparing a workspace. This packaging change does not enable native Windows.

### Managed passing proof

Managed state is outside workspace/preparation cache, under
`KMX_HOME/passing/<workspace-root-hash>/<S-hex>/` (or the platform KMX state
location without `KMX_HOME`). Directories are 0700, records immutable 0600, bounded,
closed and symlink-refusing. Each completion record embeds the existing canonical
private receipt; no prompts, answers, credentials or binding bytes are added.
Existing explicit receipt formats are unchanged and are not automatically imported.
Use `--out` when a standalone file is needed for digest Deploy or offline receipt diff;
managed completion records are not standalone `--evidence`/`--evaluation` files.

A record is committed only after successful full evaluation, Orka candidate cleanup,
final input checks and evaluator/output closes. An earlier completed pass survives a
later failed or uncertain eval; changing packaged content changes S, so old proof
cannot qualify the edit. Cleaned candidates remain historical execution proof, not
current destination health, destination evaluation or authentication.

Only the exact-S partition is scanned. Per-S immutable sequence names select the
latest completed record; staging/uncommitted files are ignored and corrupt committed
state refuses rather than silently falling back. New-registration capacity (256
records or a 16 MiB partition budget, reserving a full receipt slot) is checked
before eval effects. A full partition still supports push from existing proof;
select a different private `KMX_HOME` for new evaluation registrations. No automatic
evidence deletion, cleanup sweep, journal or durable cleanup blockade is added.

One OS-held per-workspace lock excludes concurrent root test/push; process exit
releases it without deleting its stable lock file. All root push modes, including
--evaluation and --unevaluated, require safe symlink-free, lockable KMX state;
--unevaluated opts out of proof, not exclusion or private-state validation. Never delete that file to bypass
a running operation. This does not make external registry tag races atomic.

Eligibility starts when the complete completion record is exclusively published.
If a later sync, close, cancellation or reporting operation fails, the command
returns nonzero and names the completed record, disclosing persistence uncertainty
where applicable. A visible valid record remains eligible. Complete explicit output
may also survive a late error and is disclosed; precommit errors never register a
new automatic pass.

Root `push [--workspace DIR] [REF]` finds the nearest `workspace.yaml` by walking
up from the current directory, including through Git repository boundaries.
`--workspace <directory>` overrides discovery and selects that exact directory;
it never falls back to the current directory or a parent workspace.
Missing workspaces refuse with a hint to pass `--workspace`. An invalid,
unreadable, nonregular or symlinked marker also refuses rather than falling back
to a parent. Help and completion do not resolve workspaces.
It replaces workspace-mode `suite push`, which no longer accepts `--workspace`
or a single positional reference.

The command resolves an allowlisted snapshot into the existing AgentSuite format,
validates it, prepares a private local OCI layout under KMX state, then copies
that exact descriptor graph to the registry. It does not re-archive the prepared
artifact during publication. A single stderr line names the absolute workspace
path and prepared OCI manifest digest. Result stdout still includes the suite
digest and a digest-pinned registry reference. Legacy output includes
`Required member images: []`; v3 instead confirms all runtime profiles are retained
and image/policy requirements are declarations only. The manifest digest binds
member content, cases and either legacy private metadata or all public profiles.

The content includes exact member instructions, the member's authored cases as
public AgentSuite evaluation cases under `evals/<member>/`, and a versioned
native execution declaration under `metadata/kmx/`. Cases are published
content: use deliberately shareable inputs. Git internals, local receipts,
Provider bindings, promotion policies and unrelated workspace files are not
packaged. Changes to model, instructions or cases produce a different suite
identity; old evidence cannot qualify the changed content.

For example, the authored `eval/example.yaml` (case ID `example`) becomes:

```text
evals/greeter/index.json
evals/greeter/cases/example.json
metadata/kmx/native-orka.json
```

Each authored YAML case is converted to one canonical JSON case named by its
case ID; legacy `expectContains` entries keep their KMX assertion IDs
(`expectContains.0`, ...), so assertion identities and definition digests are
unchanged. The index lists cases in authored file-name order, which is the
evaluation order, and the member's agent manifest binds the index by digest.
Authored comments, formatting and file names are not packaged. Renaming a case
file changes the source fingerprint, and changes the suite only if it changes
file-name order. Case IDs must differ by more than letter case. A case that the
public format cannot represent, such as a `contains`/`notContains` value or tool
name containing a Unicode line or paragraph separator, refuses before
preparation. JSON escaping
can make a packaged case larger than its YAML, so a case near the 1 MiB per-file
or 8 MiB total limit can refuse.

Loading a packaged suite converts each case back, keeps `runs`/`minPasses`,
and refuses public features KMX cannot execute (`critical: false`, extension
assertion types, more than 100 runs) instead of ignoring them, and names that
reason in its error. Suites packaged
by v0.6.0 no longer validate; receipts recorded against them refuse with a
remedy to rerun `kmx test` and `kmx push`. There is no migration reader.

`native-orka.json` records the member, renderer version and required images.
The alternative kagent declaration is
`metadata/kmx/native-kagent.json`, binding runtime/version/engine and an immutable
execution-profile identity. Exactly one supported
native profile is required. Explicit Orka and omitted historical Orka retain the
same prepared content/manifest identity. These are KMX native-profile supplemental
files in the OCI content layer, not public AgentSuite record types. Runtime
selection changes the OCI manifest identity even when the public graph digest
is unchanged; no public runtime field or critical extension is introduced.

The OCI manifest digest binds all the content-layer bytes. The canonical
`agentsuite.json` graph digest binds the members and their cases, but not
supplemental metadata. No outer package envelope is used. The public format still
requires a composition and pinned build-profile records; this tool-free native
profile emits a composition with no ToolProviders. Those profile records
are declared image-build inputs, not dependencies fetched or executed by native
Orka/kagent placement, and do not imply sandbox-image conformance. Their
AgentKit/Python harness pins are inert declared build inputs, not Go ADK
execution dependencies; the private kagent profile names its own closed contract.

Authentication, `--plain-http` and non-atomic tag replacement with `--force` use
the same [registry policy](kmx.md#registry-authentication) as existing suite push.
An unchanged prepared suite retains its digest through publication and pull.
An existing different registry tag requires `--force`.

Registry publication also follows the shared [unsigned provenance policy](kmx.md#suite-provenance):
a referrer is attached by default before moving the tag, `--provenance=false` opts
out, and `--require-provenance` makes an attachment failure leave the tag unchanged.
The source commit/path is recorded only when the frozen authored inputs match
HEAD; generated cache files and unrelated files are excluded from that comparison.
Source commit detection requires Git 2.40+ (`check-attr --source`) to inspect HEAD's
rewriting attributes without trusting live overrides. Older Git, dirty or non-Git
source still publishes a source-less claim and explains why.
Unsigned provenance is informational, not evaluation or promotion evidence. For
artifact authentication, see [sign the published suite digest in CI](#sign-the-published-suite-digest-in-ci).

For offline transfer, the local target's parent directory must exist:

```sh
kmx push --unevaluated --to-layout ../suite-layout my-agents:v1
kmx suite pull my-agents:v1 --from-layout ../suite-layout --output ../extracted
kmx suite validate ../extracted
```

Local layouts receive no provenance. `--plain-http`, `--force`, explicit
`--provenance` and `--require-provenance` are refused with `--to-layout`.
Local layout publication excludes competing KMX writers before opening the target
index, including extracted-directory pushes. A competing invocation refuses;
references from successful publications remain intact. An interrupted writer can
leave a sibling lock directory: remove it only after verifying the publisher has
stopped. Registry tag preflight remains separately non-atomic.
Existing extracted-directory transfer remains `suite push DIRECTORY REFERENCE`
and does not discover a workspace. Root push takes at most one positional destination
reference; explicit workspace selection is also available from anywhere:

```sh
kmx push --workspace ./my-agents ghcr.io/me/my-agents:v1
```

Root push refuses two positional arguments and explicit cluster-context and
container-engine flags. Other relative paths, including `--to-layout`, remain
relative to the invocation directory, not the discovered workspace root.
Create uses the same discovery and exact-root override. Help and completion
neither resolve workspaces nor prompt, mutate files or load cluster settings.

Internally, verified packaged native inputs can be loaded without consulting live
source or the preparation record, and publication can require an expected OCI
manifest digest. This identity check alone is not evaluation evidence or release
authorization. Workspace evaluation adds passing observations through the CLI;
root digest Deploy consumes that explicit private file before deployment.

### Sign the published suite digest in CI

Sign the **pushed OCI manifest digest**, not a tag, the AgentSuite graph digest,
or the [CLI release checksum manifest](releases.md#verify-a-release). This GitHub
Actions **job fragment** assumes a trusted-branch workflow, a checked-out workspace,
pinned KMX v0.8.1, Bash/Docker, and completed matching private evaluation proof.
Replace the example repository and workflow identity with reviewed values. Grant
this workflow access to the GHCR package; `packages: write` alone does not grant
access to an existing package owned elsewhere. See [GHCR authentication](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#authenticating-in-a-github-actions-workflow).

```yaml
permissions:
  contents: read
  packages: write
  id-token: write # GitHub Actions OIDC for keyless signing
steps:
  - name: Install pinned cosign
    uses: sigstore/cosign-installer@6f9f17788090df1f26f669e9d70d6ae9567deba6 # v4.1.2
    with:
      cosign-release: v3.1.3
  - name: Publish, sign and verify the exact suite
    shell: bash
    env:
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
      GH_ACTOR: ${{ github.actor }}
      SUITE_REPOSITORY: ghcr.io/example/my-agents
      EXPECTED_WORKFLOW_IDENTITY: https://github.com/example/agents/.github/workflows/publish-suite.yml@refs/heads/main
    run: |
      set -euo pipefail
      printf '%s' "$GH_TOKEN" | docker login ghcr.io -u "$GH_ACTOR" --password-stdin
      kmx push "$SUITE_REPOSITORY:$GITHUB_SHA" > "$RUNNER_TEMP/suite-push.txt"
      SUITE_REF=$(awk 'sub(/^Pin this AgentSuite: /, "") { print }' "$RUNNER_TEMP/suite-push.txt")
      SUITE_DIGEST=${SUITE_REF#"$SUITE_REPOSITORY@"}
      [[ "$SUITE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || {
        echo 'Expected exactly one pushed suite manifest digest for this repository' >&2
        exit 1
      }
      SUITE_REF="$SUITE_REPOSITORY@$SUITE_DIGEST"
      cosign sign --yes "$SUITE_REF"
      cosign verify \
        --certificate-identity "$EXPECTED_WORKFLOW_IDENTITY" \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com \
        "$SUITE_REF"
```

KMX v0.8.1 root workspace `push` prints `Pin this AgentSuite: REPOSITORY@sha256:…`
on stdout after successful transfer. The recipe captures that invocation's output
and requires exactly one matching reference with a full digest; missing, duplicate
or wrong-repository output fails closed. It does not scrape the prepared-digest
stderr line, a provenance referrer digest, or resolve the mutable tag afterward.
This is a version-specific text parser, not a KMX machine-readable output flag.

The expected workflow URI (repository, workflow file and exact ref) and issuer are
**independent acceptance policy**: set them through review, never copy them from a
signature's claims or derive them from an arbitrary triggering workflow/ref.
For reusable workflows, review the actual certificate identity before setting
policy. See [Cosign's verification options](https://github.com/sigstore/cosign/blob/v3.1.3/doc/cosign_verify.md).
Keep any external deployment decision after successful verification, with no
`continue-on-error` or ignored exit status. This fragment deploys nothing.
Public Sigstore signing publishes certificate identity and artifact digest in
public transparency infrastructure, even for a private registry package.

The [installer is commit-pinned and verifies its downloaded binary](https://github.com/sigstore/cosign-installer/blob/6f9f17788090df1f26f669e9d70d6ae9567deba6/action.yml);
the selected [Cosign v3.1.3 release](https://github.com/sigstore/cosign/releases/tag/v3.1.3)
[lists GHCR as supported](https://github.com/sigstore/cosign/blob/v3.1.3/README.md#registry-support).
Cosign v3 uses [OCI 1.1 referring artifacts](https://docs.sigstore.dev/cosign/signing/signing_with_containers/#signature-location-and-management);
its registry client supports the [referrers fallback index tag](https://github.com/google/go-containerregistry/blob/v0.21.7/pkg/v1/remote/referrers.go)
when the API is unavailable. Allow reads/writes for signatures and that index in
the same package. This recipe has **not been qualified by a live GHCR/OIDC
push-sign-verify roundtrip**; qualify your registry permissions, referrer discovery
and retention policy before relying on it. Offline syntax/help checks cannot
establish those properties.

Private evaluation evidence, public suite provenance and `kaimahi.dev/lift-record`
remain **unsigned**. A suite signature does not sign those separate records or
prove evaluation truth or destination execution. **v0.8.1 Lift does not enforce
signatures or signer policy**, and KMX has no signing flag. Evaluation/promotion
policy is tracked in [#350](https://github.com/kaimahi-agents/kaimahi/issues/350);
pre-Lift signature/attestation checks in [#374](https://github.com/kaimahi-agents/kaimahi/issues/374).

<a id="digest-lift"></a>

## Digest deploy

```sh
kmx deploy registry.example/suite@sha256:<digest> --to-context prod \
  --inference provider:inference --evidence ../private/passing.json --promote --plan
kmx deploy suite@sha256:<digest> --from-layout ./oci --to-context kind-demo \
  --inference provider:inference --evidence ../private/passing.json --promote -o json
```

Use a complete SHA256 digest, not a mutable tag. Destination runtime must already
be installed. `--to NAME` may supply the destination selectors from the workspace;
without it, explicit-reference Deploy does not inspect workspace configuration. Orka uses `--inference provider:NAME`; packaged kagent instead uses
an author-owned `--bindings FILE`. The selectors are mutually exclusive and the
verified packaged runtime must match the evidence. Kagent derives its namespace
from the bindings; an explicit `--to-namespace` must agree. The bindings' parent,
file identity and exact bytes are rechecked around effects.

For Orka, the selected inference Provider and its referenced Secret must
already exist; that inference Provider must be Ready for its current generation,
and the Secret must contain its referenced key. The suite-owned destination
Provider may be absent and created by apply. `--to-namespace` defaults to
`orka-system`; registry HTTP requires explicit `--plain-http`, which
conflicts with `--from-layout`. There is no model/runtime override or workspace
selector. Help/completion does not load operational configuration.

Release planning reads an exact SHA256 suite manifest from a registry
or local OCI layout without authored source, a preparation fingerprint or a build.
It retains original packaged bytes; it never re-archives or publishes them.
Only the selected layout graph is read, not unrelated references. The reader
bounds the manifest to 1 MiB, the exact empty config to two bytes, compressed
content to 9 MiB, raw decompressed tar to 10 MiB, 256 entries, 212 regular files,
1 MiB per file and 8 MiB total file content before public/native validation.
A policy-free one-member native graph needs at most 136 files (137 with a policy, including its index and
128 cases); offline multi-member inventories use the derived budget above.
Links, special files, PAX records, unsafe/duplicate paths and trailing or multiple
gzip streams refuse. Existing general suite push/pull limits are unchanged.

The application `PrepareReleaseLift` operation requires an explicitly selected
private full-passing native receipt. It checks packaged coherence and exact
observed target, inference and runtime equality. It returns a typed identity-only
review: absent resources may be planned for creation; existing resources require
all exact suite markers and the evaluated UID/generation/whole-spec proof with
current-generation readiness on Orka. Kagent is create-only: any occupied primary
or generated child refuses, even if its markers and evaluated UID match. Passing
evidence is never retained in-process placement/reuse authority. Replacement, drift, adoption and unknown state
refuse. Planning makes GETs only: no server dry-run/admission, Tasks, writes or
placement records. Returned summaries cannot mutate the retained plan.

`--plan` prints the identity-only review without mutation guard or admission.
Apply consumes that opaque review and its digest once, refreshes artifact/evidence
and destination observations, runs the normal target guard, then preflights both
absent creates before the first persistent write. Orka Provider reaches current
Ready, or kagent ModelConfig current-generation Accepted, before Agent creation.
Kagent Agent must also be Ready. Without `--update`, only conditional CREATE or
Orka exact receipt-bound reuse is allowed: no update, adoption, marker repair,
execution retry, rollback or cleanup. Explicit eligible retained Orka updates and
rollback use the separate [update contract](#update-and-rollback); kagent refuses.
Owned status-only resource-version churn is tolerated; selected inference Provider
and Secret versions remain exact. Before each write and after each Ready wait,
the pair and prerequisites are rechecked, including before each server dry-run.

Candidate-source evidence requires `--promote` even in the same environment;
cleaned physical candidates cannot authorize canonical destination reuse.
Historical canonical evidence retains the exact-subject gate.
`--promote` is also required when target/binding differs. Packaged S/G/member/model/
renderer/profile/composition/images must match, as must the pinned runtime version,
images and mode. The selected Ready `openai` Provider must explicitly declare
`defaultModel` equal to the packaged model, with UID/generation/resourceVersion
coherence. This is a declaration, not a backend-equivalence or execution probe.
Destination objects must be absent: source evaluation UIDs cannot authorize their
reuse. The original evidence is never rewritten. Output discloses:
`Evidence: passed on <source target>, not evaluated on this target`.
For an identical subject, `--promote` still uses the normal exact gate and makes
no misleading source-only claim.

Text or `-o json` results report placement identities and per-resource outcomes:
`created`, `reused`, `unknown` or `not-attempted`, separately from `ready`. An
acknowledged creation can be `created` with a UID but `ready: false` if the wait
fails/cancels. Partial results exit nonzero and resources may remain. Ambiguous
CREATE returns unknown, without a recovery read, resend or invented UID.
Created/recreated UIDs require a fresh full evaluation before later receipt-bound
deploy reuse. Readiness does not prove a fresh answer or destination evaluation.
Opaque in-process success results support read-only invocation revalidation via
`VerifyReleaseLiftPlacement`; serialized JSON does not confer placement authority.

Unsigned private receipts establish supplied observations, not signer
authentication or authenticated deployment approval. General promotion policy/
signing remains separate. Orka uses native `openai` Providers; kagent uses explicit
OpenAI ModelConfig bindings. Host Foundry/Copilot execution and image deployment are not
covered. Legacy bundle lift is removed. Hermetic tests alone do not establish
real cluster deployment qualification.

<a id="kagent-digest-lift"></a>

### Kagent digest deploy

```sh
kmx deploy suite@sha256:<digest> --from-layout ./oci --to-context prod \
  --bindings ./prod-bindings.yaml --evidence ../private/kagent-passing.json --promote --plan
```

The supported profile is exact v0.10.3 Go/linux-amd64, tool-free and OpenAI-compatible,
with no member images. Deploy uses the same `PrepareReleaseLift`, `ApplyReleaseLift`
and `VerifyReleaseLiftPlacement` seam as Orka, not the legacy bundle engine. It
creates only ModelConfig and Agent; namespace, runtime and referenced Secret/key
are operator prerequisites. It sends no A2A Tasks, model calls or tool calls.

Default gating requires the exact prepared subject. Explicit `--promote` permits
only destination context/cluster/namespace identities, endpoint/Secret references,
and physical installation names/UIDs, deployment generation, controller account,
selector and watch scope to differ. Packaged identity/member/model/profile,
bindings schema/provider kind, AuthMode, controller image and full observed
controller digest/platform inventory, plus Go registry/repository/tag, remain
exact. Unknown future fields compare by default. The new ModelConfig declares
the packaged model; this is not independent proof of backend model equivalence.
Unsecure source evidence cannot authorize a trusted-proxy destination.

The private plan retains the full evaluated Go image digest/platform inventory.
Before creation it can qualify configured pins, not observe a nonexistent Agent.
Final placement and opaque Verify require the actual generated workload inventory
to match exactly. A mismatch leaves a partial result/resources, never a successful
placement, and triggers no cleanup or retry. Public result JSON is not restart
or reuse authority. Kagent retirement and authenticated promotion
policy remain unavailable; evidence never crosses runtimes.

The existing pinned kind shard configures create, three-case evaluation (two text
cases and an open-ended rubric case), exact local
publication, source-free GET-only deploy planning, explicit new-target promotion and
occupied repeat refusal, with zero additional native execution traffic.

Preparation state is outside authored source, under the KMX state directory
(`KMX_HOME` overrides it). Cache corruption refuses reuse. Keep this state private;
it is not a receipt, evaluation pass or promotion authorization.
