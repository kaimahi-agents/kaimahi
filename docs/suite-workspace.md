# Author and publish a one-member native suite

A workspace is editable source. Its prepared AgentSuite is an immutable OCI
artifact, identified by its manifest digest. Native Orka and exact-v0.10.3 Go
kagent declarations require no member images; authoring and publication do not contact Kubernetes,
run a model or invoke Docker/buildx. The pinned AgentSessions chat profile also
supports offline authoring, preparation, loading and ordinary unevaluated push
with no member image. It does not yet support suite eval, evaluated push, Lift,
chat or status; Orka/kagent operational consumers refuse it.

**Implemented:** offline creation, preparation and publication, and full-inventory
native Orka or exact kagent v0.10.3 Go/linux-amd64 evaluation of one tool-free
member with an exact OpenAI-compatible model. Orka remains the authoring default;
`--runtime kagent` authors the private profile. Root eval derives that packaged
runtime and uses explicit author-owned bindings; evaluated push accepts only
matching runtime-specific passing evidence. Root digest Lift supports both
profiles. Kagent Lift is create-only: any occupied primary or generated child
refuses, even with matching evaluated UIDs. Suite status and retirement remain
unavailable. Evaluation placement refuses occupied targets without a complete
caller-retained observation; qualified repeat is read-only, not update/adoption
or restart recovery.
Orka evaluation has hermetic command/Task tests and a pinned two-case native
workspace CI qualifier. Packaged kagent evaluation has hermetic coverage and
pinned exact-v0.10.3 CLI qualification. Root exact-digest Lift retrieves the
package from a registry or local layout and separately consumes matching private
full-passing evidence. Orka uses `--inference`; kagent uses author-owned
`--bindings`. Bounded CI qualifies Orka exact registry/layout reuse and kagent
fresh-target creation/occupied refusal. Standalone registry-package evaluation
remains unavailable. Existing `agent.yaml` creation, evaluation and lift remain
separate and unchanged.

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
These selectors do not change the `--live --runtime kagent` v0.10.2 path.
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
`metadata/kmx/native-agentsessions.json` binds the runtime/config contract,
member identities and complete ordered raw case inventory in manifest S.
The public graph digest alone does not identify execution semantics. The
build profile remains inert; `requiredImages` is explicitly empty. No host,
journal, model endpoint, credential or passing evidence is packaged or contacted.
The Go SDK pin includes upstream #120 and #124, but does not establish live
host qualification. Existing Orka/kagent evidence cannot authorize this profile.

`--instructions` names a file, not inline text. Its nonblank bytes are copied
unchanged into the member. `--description` is optional and single-line.
Instructions use the native block-text policy: LF and tabs are allowed; CR and other control characters are refused,
not normalized. `--model` is required and exact; there is no inferred default.
This mode refuses cluster, Provider, output, tools and Task flags with a
`--live` remedy, rather than ignoring them or inferring live mode.
`--workspace` and `--live` conflict; an explicitly empty `--workspace` refuses.
Required fields and mode conflicts are checked before initialization.

A workspace supports only one member. Creating a second member refuses and
suggests a new `--workspace` directory, preserving authored files, metadata and
cases. Existing member directories, including PortableAgent bundles, are never
overwritten or migrated. For the same first creation using discovery instead
of an exact root, omit `--workspace`:

```sh
kmx create greeter --model qwen-3.5-2b --instructions ./instructions.md
```

A name is required; there is no offline authoring wizard. For the separate live
Orka wizard use `kmx create --live`. Live creation bypasses workspace discovery,
even below an invalid marker. `--live --no-apply` and `--live --out -` retain the
old resource/PortableAgent bundle formats, not this workspace format. Quickstart
creation composes fixed source, full eval, exact local publication, reviewed Apply and placement Verify before a fresh requested answer. Console creation selects an already authored native-Orka workspace, reviews instructions, exact model and every case, and selects a named Ready openai Provider. It requires explicit execution consent (typed destination context for remote targets), then reviews the retained exact Lift plan before single-use Apply/Verify. The explicit source root/fingerprint and complete API URI are rechecked; Ready placement is not an answer. Evaluation/partial errors may leave resources, without automatic retry or cleanup. Scaffold arithmetic is not quality proof for arbitrary instructions; author appropriate public cases. See the [live contract](kmx.md#default-orka-contract).

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
kmx eval --to-context kind-demo --inference provider:inference \
  --out ../private-evidence/passing.json
kmx push --evaluation ../private-evidence/passing.json ghcr.io/me/my-agents:v1
```

Root `eval` discovers the nearest workspace, or selects an exact root without
fallback using `--workspace DIR`. It accepts no positional package reference.
`--to-context` and `--out` are required. Orka requires `--inference provider:NAME`;
kagent instead requires `--bindings FILE`, never both. For Orka, namespace
is `orka-system` by default (`--to-namespace`); `--result-port` defaults to
`19180`, and each case has a separate `--case-timeout` (default 5m, maximum 9m).
Explicit empty selectors and global `--context` are refused. No runtime/model
substitution, judge, case subset or `--show-content` is available. Only `contains`,
`notContains` and `regex` assertions are supported. Unsupported evaluators,
receipt-budget overflow and invalid output destinations refuse offline before
loading cluster configuration.

One mutation guard covers runtime-specific placement and all ordered evaluation
Tasks: Orka `Provider/Agent`, or kagent `ModelConfig/Agent`. Remote targets require
the existing named confirmation; local kind targets retain the existing banner.
Orka can reuse/update only a same-owner pair; kagent creates new objects and
refuses occupied targets without complete caller-retained proof. Neither adopts
legacy/unowned/foreign resources. Every case sends one fresh Task exactly once,
using the exact authored input. Ambiguous writes are never recovered, retried or
resent. Placement, runtime, target and binding observations bracket execution;
source and placement are checked again before export. These observations do not
prove drift absence or authentication.

Kagent uses a closed author-owned `KagentBindings` file (namespace, OpenAI
provider/baseURL, separately provisioned Secret name/explicit key). The model
comes only from the packaged member. Explicit `--to-namespace` must agree with
bindings; `--result-port` refuses. For example:

```sh
kmx eval --workspace ./kagent-workspace --to-context kind-demo \
  --bindings ./private/kagent-bindings.yaml --out ../private-evidence/kagent-pass.json
```

Kagent evaluation requires **auth: unsecure only, trusted-proxy refused** before
admission and before native sends. It creates new suite-owned ModelConfig/Agent
objects; occupied unrecorded targets refuse, never adopt/update/delete-recreate.
Each case sends one fresh blocking A2A Task with no prior Task/context IDs.
Ambiguous transport, foreign history, reused native identities or changed
placement/runtime/binding stop later sends. Consent discloses full prompt,
answer and history retention in kagent's database (upstream default unlimited),
Service-proxy audit bodies and model transfers. No auth settings are modified.

For Orka, the temporary result session uses the selected ServiceAccount's **full effective
authority**. Discarding its bearer is not revocation; Task-read enforcement is
unverified for v0.2.0. One pinned loopback connection is opened per case, without
reconnect. Prompts, answers, assertion operands and reasoning stay out of KMX
terminal summaries and receipts, but runtime/model storage and Kubernetes audit
policy may retain them. Published suites themselves contain authored inputs.

Failure, unknown results, drift, cancellation or output failure stops later cases
and exits nonzero. No incomplete/diagnostic receipt is exported; resources may
remain, with no automatic retry or cleanup. Only a full ordered pass publishes one
new private receipt file (0600) into an existing private directory (0700), outside
workspace and cache trees. Existing destinations are never replaced. A late
publication or summary failure can leave a complete output file; inspect it rather
than assuming failure removed it. Receipts contain identities, digests and
assertion verdicts only, not payloads or evaluation history.

`push --evaluation FILE` reads that strict private file and requires the exact
prepared manifest digest plus full offline case/subject coherence before transfer
or provenance. Equivalent authored spelling with the same artifact identity is
allowed; a changed case/model/instruction artifact is refused. Unsigned receipts
are observations, not authentication, policy gates or release authorization.
Ordinary push remains explicitly unevaluated. Extracted-directory `suite push`
does not gain this flag. `eval diff` reads strict passing suite receipts and
compares only compatible same-runtime subjects/case inventories; Orka/kagent
comparison refuses as incompatible, never as equivalent behavior. Comparison
also requires identical observed ownership. Separate kagent CLI runs create
fresh resources, so their receipts are incompatible rather than a cross-run
behavior comparison. Root Lift dispatches only matching runtime evidence;
cross-runtime evidence refuses. `eval from-sessions` and `bundle evaluate`
retain their separate formats and behavior; native receipts are not bundle gates.

### Qualification boundary

The existing Orka runtime CI shard now includes a checkout-only native scenario
using the pinned CPU AIKit model, the two public capital/arithmetic cases and the
installer's existing Secret reference in its exclusively owned kind cluster.
It attempts one full evaluation without retry, independently reads live identity,
spec and result evidence, and transfers exact S through a local layout and a
pinned private Distribution registry. With authored source withheld, root CLI
`lift --plan`, apply and repeat apply must reuse the exact evaluated pair without
admission, writes or Tasks for both transports. A separate `run --agent` creates
one fresh Task per transport; authored text assertions, input/result digests and
pair identity are independently checked. This is separate execution qualification,
not invocation authorization conferred by serialized lift output.

Refusals cover missing evidence, different S with the same G, a coherent private
namespace-UID counterfactual (not destructive namespace replacement), foreign
ownership, a distinct Ready inference identity and terminal spec drift. Altered
evidence has an original-identity positive control. The product audit records
request intent before forwarding, distinguishes admission from persistent writes
and Task attempts, and refuses incomplete/failed requests. Only passing receipt
metadata and a payload-free qualification report may be uploaded. Runtime/model and
Kubernetes audit storage can still retain inputs. A successful run qualifies this
fixed scenario; hermetic tests alone do not establish runtime/model execution.
The same shard also prepares a second exclusively owned kind cluster with the
same pinned Orka in `orka-system`. It shares the real AIKit model through a
fixture-only NodePort on the private kind network, without a host port mapping;
the destination has its own inference Provider and Secret. With source withheld,
registry `lift --promote --plan` must be GET-only and apply must complete both
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
source-free digest Lift into a fresh prepared namespace, including GET-only
planning, ordered creation and occupied repeat refusal without another native
execution. Standalone package evaluation remains unavailable.

## Publish

```sh
kmx push ghcr.io/me/my-agents:v1
```

Root `push [--workspace DIR] REF` finds the nearest `workspace.yaml` by walking
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
digest, a digest-pinned registry reference and `Required member images: []`.
The manifest digest, not the canonical graph digest, binds authored cases as well
as member content.

The content includes exact member instructions and authored case files under
`metadata/kmx/`, plus a versioned native execution declaration. Cases are published
content: use deliberately shareable inputs. Git internals, local receipts,
Provider bindings, promotion policies and unrelated workspace files are not
packaged. Changes to model, instructions or cases produce a different suite
identity; old evidence cannot qualify the changed content.

For example, the authored `eval/example.yaml` becomes:

```text
metadata/kmx/native-orka.json
metadata/kmx/eval/example.yaml
```

`native-orka.json` inventories the member, renderer version, required images,
and every packaged case path and digest. The alternative kagent declaration is
`metadata/kmx/native-kagent.json`, binding runtime/version/engine and an immutable
execution-profile identity to the same complete inventory. Exactly one supported
native profile is required. Explicit Orka and omitted historical Orka retain the
same prepared content/manifest identity. These are KMX native-profile supplemental
files in the OCI content layer, not public AgentSuite record types. Runtime
selection changes the OCI manifest identity even when the public graph digest
is unchanged; no public runtime field or critical extension is introduced.

The OCI manifest digest binds all the content-layer bytes, including cases.
The canonical `agentsuite.json` graph digest alone does not bind supplemental
metadata. No outer package envelope is used. The public format still
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
Unsigned provenance is informational, not evaluation or promotion evidence.

For offline transfer, the local target's parent directory must exist:

```sh
kmx push --to-layout ../suite-layout my-agents:v1
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
and does not discover a workspace. Root push takes only one positional destination
reference; explicit selection is also available from anywhere:

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
root digest lift consumes that explicit private file before deployment.

## Digest lift

```sh
kmx lift registry.example/suite@sha256:<digest> --to-context prod \
  --inference provider:inference --evidence ../private/passing.json --promote --plan
kmx lift suite@sha256:<digest> --from-layout ./oci --to-context kind-demo \
  --inference provider:inference --evidence ../private/passing.json -o json
```

Use a complete SHA256 digest, not a mutable tag. Destination runtime must already
be installed. Orka uses `--inference provider:NAME`; packaged kagent instead uses
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
content to 9 MiB, raw decompressed tar to 10 MiB, 256 entries, 135 regular files,
1 MiB per file and 8 MiB total file content before public/native validation.
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
Kagent Agent must also be Ready. Only conditional CREATE or Orka exact receipt-bound reuse is
allowed: no update, adoption, marker repair, execution retry, rollback or cleanup.
Owned status-only resource-version churn is tolerated; selected inference Provider
and Secret versions remain exact. Before each write and after each Ready wait,
the pair and prerequisites are rechecked, including before each server dry-run.

`--promote` is required when target/binding differs. Packaged S/G/member/model/
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
lift reuse. Readiness does not prove a fresh answer or destination evaluation.
Opaque in-process success results support read-only invocation revalidation via
`VerifyReleaseLiftPlacement`; serialized JSON does not confer placement authority.

Unsigned private receipts establish supplied observations, not signer
authentication or authenticated deployment approval. General promotion policy/
signing remains separate. Orka uses native `openai` Providers; kagent uses explicit
OpenAI ModelConfig bindings. Host Foundry/Copilot execution and image lift are not
covered. Existing bundle lift is unchanged. Hermetic tests alone do not establish
real cluster lift qualification.

### Kagent digest lift

```sh
kmx lift suite@sha256:<digest> --from-layout ./oci --to-context prod \
  --bindings ./prod-bindings.yaml --evidence ../private/kagent-passing.json --promote --plan
```

The supported profile is exact v0.10.3 Go/linux-amd64, tool-free and OpenAI-compatible,
with no member images. Lift uses the same `PrepareReleaseLift`, `ApplyReleaseLift`
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
or reuse authority. Kagent suite status/retirement and authenticated promotion
policy remain unavailable; evidence never crosses runtimes.

The existing pinned kind shard exercises create, two-case evaluation, exact local
publication, source-free GET-only lift planning, explicit new-target promotion and
occupied repeat refusal, with zero additional native execution traffic.

Preparation state is outside authored source, under the KMX state directory
(`KMX_HOME` overrides it). Cache corruption refuses reuse. Keep this state private;
it is not a receipt, evaluation pass or promotion authorization.
