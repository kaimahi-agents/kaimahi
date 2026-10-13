# Author and publish a one-member native suite

A workspace is editable source. Its prepared AgentSuite is an immutable OCI
artifact, identified by its manifest digest. Native Orka and exact-v0.10.3 Go
kagent declarations require no member images; authoring and publication do not contact Kubernetes,
run a model or invoke Docker/buildx.

**Implemented:** offline creation, preparation and publication, and full-inventory
native Orka evaluation of one tool-free member with an exact OpenAI-compatible model.
Orka remains the authoring default; `--runtime kagent` authors the private Go
v0.10.3/linux-amd64 profile only. Both `eval` and `push --evaluation` refuse it
with `kagent evaluation on the suite path isn't available yet`.
Private kagent preparation, new-object placement and observation helpers consume
this packaged profile. They are not wired to a CLI or the release-lift seam;
suite evaluation, status and promotion remain unavailable. Occupied targets
refuse without a complete caller-retained observation; qualified repeat is
read-only, not update/adoption or restart recovery.
Orka evaluation has hermetic command/Task tests and a pinned two-case native
workspace CI qualifier. Standalone registry-package evaluation and registry-reference lift
are not available. Existing `agent.yaml` creation, evaluation and lift remain
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
and console creation remain live. See the [live contract](kmx.md#default-orka-contract).

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

Provision the pinned Orka v0.2.0 harness-v2 runtime, a Ready OpenAI-compatible
inference Provider with its referenced Secret, and the existing result-access
ServiceAccount/Task-get grant separately. Evaluation does not install or upgrade
anything, build images or write Secrets. The packaged member model is exact;
the selected Provider's default model does not replace it. Required member
images remain `[]`.

```sh
mkdir -m 700 ../private-evidence
kmx eval --to-context kind-demo --inference provider:inference \
  --out ../private-evidence/passing.json
kmx push --evaluation ../private-evidence/passing.json ghcr.io/me/my-agents:v1
```

Root `eval` discovers the nearest workspace, or selects an exact root without
fallback using `--workspace DIR`. It accepts no positional package reference.
`--to-context`, `--inference provider:NAME` and `--out` are required. Namespace
is `orka-system` by default (`--to-namespace`); `--result-port` defaults to
`19180`, and each case has a separate `--case-timeout` (default 5m, maximum 9m).
Explicit empty selectors and global `--context` are refused. No runtime/model
substitution, judge, case subset or `--show-content` is available. Only `contains`,
`notContains` and `regex` assertions are supported. Unsupported evaluators,
receipt-budget overflow and invalid output destinations refuse offline before
loading cluster configuration.

One mutation guard covers both ownership-safe Provider/Agent placement and all
ordered evaluation Tasks. Remote targets require the existing named confirmation;
local kind targets retain the existing banner. Only a same-owner native pair can
be reused or updated; legacy/unowned/foreign resources are not adopted. Every
case sends one fresh Task exactly once, using the exact authored input. Ambiguous
writes are never recovered, retried or resent. Placement, runtime, target and
binding observations bracket execution; source and placement are checked again
before export. These observations do not prove drift absence or authentication.

The temporary result session uses the selected ServiceAccount's **full effective
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
does not gain this flag. `eval diff/from-sessions` and `bundle evaluate` retain
their existing formats and behavior; native receipts are not bundle lift gates.

### Qualification boundary

The existing Orka runtime CI shard now includes a checkout-only native scenario
using the pinned CPU AIKit model, the two public capital/arithmetic cases and the
installer's existing Secret reference in its exclusively owned kind cluster.
It attempts one full evaluation without retry, independently reads live identity,
spec and result evidence, transfers exact S through a local layout, and checks
ownership, case-mutation and unsupported-evaluator refusals. Only passing receipt
metadata and a scalar qualification report may be uploaded. Runtime/model and
Kubernetes audit storage can still retain inputs. A successful run qualifies this
fixed scenario; hermetic tests alone do not establish runtime/model execution.
Release-reference Lift and standalone package evaluation remain unavailable.

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
registry release lift remains unavailable.

## Internal digest-lift planning boundary

Internal release planning can read an exact SHA256 suite manifest from a registry
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
current-generation readiness. Replacement, drift, adoption and unknown state
refuse. Planning makes GETs only: no server dry-run/admission, Tasks, writes or
placement records. Returned summaries cannot mutate the retained plan.

This is an internal read-only boundary, not a runnable `kmx lift` command or write
authority. Guarded apply and explicit cross-target promotion are not available.
Unsigned private receipts establish supplied observations, not signer
authentication or destination execution proof. Native `openai` Providers are the
only inference binding supported here; host Foundry/Copilot execution is not covered.

Preparation state is outside authored source, under the KMX state directory
(`KMX_HOME` overrides it). Cache corruption refuses reuse. Keep this state private;
it is not a receipt, evaluation pass or promotion authorization.
