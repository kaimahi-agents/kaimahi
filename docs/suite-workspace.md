# Author and publish a one-member native suite

A workspace is editable source. Its prepared AgentSuite is an immutable OCI
artifact, identified by its manifest digest. Native Orka and exact-v0.10.3 Go
kagent declarations require no member images; authoring and publication do not contact Kubernetes,
run a model or invoke Docker/buildx. The pinned AgentSessions chat profile also
supports offline authoring, preparation, loading and explicitly unevaluated push
(`--unevaluated`) with no member image. Full packaged evaluation and exact-suite evaluated push
require the explicit [development host](#agentsessions-development-evaluation)
boundary. Lift, chat/status and Orka/kagent operational consumers refuse it.

**Implemented:** offline creation, preparation and publication, and full-inventory
native Orka or exact kagent v0.10.3 Go/linux-amd64 evaluation of one tool-free
member with an exact OpenAI-compatible model. Orka remains the authoring default;
`--runtime kagent` authors the private profile. Root eval derives that packaged
runtime and uses explicit author-owned bindings; evaluated push accepts only
matching runtime-specific passing evidence. Root digest Lift supports both
profiles. Kagent Lift is create-only: any occupied primary or generated child
refuses, even with matching evaluated UIDs. Suite status and retirement remain
unavailable. Public Orka eval creates fresh disposable candidates and cleans them
up before exporting proof. Kagent evaluation refuses occupied targets without a
complete caller-retained observation; qualified reuse is read-only, not
update/adoption or restart recovery.
Orka evaluation has hermetic command/Task tests and a pinned three-case native
workspace CI qualifier. Packaged kagent evaluation has hermetic coverage and
pinned exact-v0.10.3 CLI qualification. Root exact-digest Lift retrieves the
package from a registry or local layout and separately consumes matching private
full-passing evidence. Orka uses `--inference`; kagent uses author-owned
`--bindings`. Bounded CI qualifies Orka exact registry/layout reuse and kagent
fresh-target creation/occupied refusal. Standalone registry-package evaluation
remains unavailable. Legacy user `agent.yaml` creation, evaluation, lift and
receipts are removed; no conversion or migration is provided.

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
kmx eval --to dev --out ../private/passing.json
kmx push --evaluation ../private/passing.json registry.example/team/agents:v1
kmx lift registry.example/team/agents@sha256:<digest> --to prod \
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

`eval --to NAME` and digest `lift --to NAME` resolve concrete selectors once,
print them and their source, then retain those values for the entire invocation.
Editing `kmx.yaml` during confirmation cannot change that operation. Existing
source/binding-file/runtime/remote-state checks still apply. Explicit `--to-context`,
`--to-namespace`, `--inference` or `--bindings` overrides the corresponding saved
value; inference and bindings remain mutually exclusive. Saved relative bindings
paths are config-relative; explicit flag paths remain invocation-relative.
Runtime is an expectation checked against the package, never a runtime switch.

Eval may use saved `evalTarget` when no destination is explicitly selected; it
never guesses from a name, kubectl current-context or remembered access selection.
Lift has no default destination. Explicit-reference Lift without `--to` remains
source/workspace-independent. AgentSessions development evaluation ignores these
cluster defaults and refuses named destination selection.

Root workspace registry push and named Lift may use an **exact registry-host**
`plainHTTP` policy; explicit `--plain-http=false` wins. TLS remains the default;
there is no HTTP fallback after TLS failure, and existing credential restrictions
remain. Local layouts and extracted-directory `suite push` are unchanged.
The config is closed, bounded and credential-free (Secret references only);
invalid or unreadable nearest state refuses rather than falling back. No user
config merging. Operational config is excluded from prepared S, source fingerprint
and published content.

This is selector configuration, not environment installation or deployment
identity. `publishRepository` supplies bare push and composed Lift's repository;
`evaluationTarget` explicitly selects where a destination's missing proof runs.
Root eval keeps managed passing proof with optional `--out`; explicit push
references are preserved. Explicit-reference Lift remains deploy-only and requires
a digest/private evidence and explicit `--promote` for source-only evidence.
**Kagent stays out of composed Lift until its own candidate-scoped evaluation
and ownership-safe cleanup are qualified**; standalone explicit kagent operations
retain their existing boundaries. Named selectors never bypass remote confirmation.

## Composed workspace Lift

With the Orka prerequisites and named configuration above already prepared:

```sh
kmx lift --to prod --plan
kmx lift --to prod
```

Without a positional reference, Lift discovers the nearest workspace and freezes
its exact packaged version and source fingerprint. It requires `publishRepository`
and the destination's explicit `evaluationTarget`; it never substitutes `evalTarget`
or a current/remembered context. It runs missing evaluation, publication and
placement in that order, using the existing disposable candidate and release gates.
Promotion is selected automatically and disclosed as **passed on the evaluation
target, not evaluated on the destination**; explicit `--promote=false` stays strict.
No annotations, guided authoring, tag-based Lift, destination update or rollback
are included.

The default stage transcript has four stderr lines: Plan, Eval, Push and Lift;
failures may add a safe refusal or critical cleanup warning. Skipped stages keep
their line. Counts are case runs (Tasks), also named `caseRuns` in JSON; a completed
evaluation means each case's threshold was met, not that every run passed.
`-v` shows diagnostic detail; `-o json` writes one
structured result to stdout, including stage and partial outcomes. Plan names the
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
can be skipped by composed Lift. Older publications without a local completion
record do not confer skip authority. A recorded publication is verified by
immutable digest, not a tag. With `--require-provenance`, bounded registry reads
and temporary local extraction also verify existing provenance; missing or
unverifiable attachments refuse without republishing or repair.
A recorded successful Lift is rechecked
GET-only against destination inputs, ownership markers, resource UIDs, whole specs
and current readiness. Private completion records grant only skip authority, never
Apply, adoption or replacement. Missing, drifted or uncertain destination completion
refuses rather than repairing an occupied or partially changed target. Full receipts
and completion observations stay in private KMX state, outside authored source.

A failed or uncertain evaluation/cleanup stops publication and placement. A failed
publication can already have transferred content; inspect its incomplete/unknown
outcome rather than assuming nothing reached the registry. Partial Lift resources
may remain. No later stage runs as an automatic repair, retry or rollback.

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
execution consent and reviews the exact retained Lift plan before Apply/Verify.
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
No registry, authentication, Lift, chat/status or remote-host support is added.

```sh
kmx eval --workspace ./sessions-workspace --development-host ./private/host.json \
  --out ./private/passing.json --case-timeout 30s
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

The address must be literal loopback, not `localhost`. All cluster, namespace,
inference, kagent bindings and result-port selectors refuse, including explicitly
supplied defaults. No declaration or transport failure triggers fallback. Before
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
terminal success and EOF. Deltas and Session metadata are not answers. Host-file
and source snapshots are rechecked around each run, after judging and before export.
Only completed attributable assertion failures count against the authored threshold;
unknown/partial/invalid evidence, drift, cancellation or output failure before
publication stops later execution with no new pass. The shared publisher can
leave a complete passing receipt after its exclusive link if a later directory
sync, readback or cancellation fails; evaluation returns nonzero and reports
**“output may exist”**. Inspect the file rather than assuming rollback removed it.
No incomplete receipt is accepted, and no published destination is automatically
deleted. Every Session and execution identity must be unique; no retry, recovery
or implicit old pass is selected.

Only a full policy pass exports the `kmx.agentsessions-suite.passing/v1` private proof,
including exact S/G/member/profile/config/model/case identities, operator host
observations, fresh Session/execution IDs and verified journal heads. Prompts,
answers, config bytes, endpoint locators and journal bodies are excluded. Push
consumes that explicit proof for exact S; evidence is not embedded in the suite
or published as a referrer. Orka/kagent evidence cannot authorize this member,
and Sessions evidence is refused by Lift and receipt diff. Read-only Replay can
support diagnosis with zero model calls but cannot become fresh candidate proof.

The pinned daemon path has been demonstrated once with an instruction-sensitive,
keyless OpenAI-compatible fixture: two-case pass/evaluated push, instruction
failure with no export, and Replay/reference reconstruction with unchanged model
counter. That is runtime/config/journal qualification, not real-LLM quality or
production/authenticated host qualification. No model or host is installed by KMX.

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
A destination context (explicit `--to-context`, named `--to`, or saved `evalTarget`)
is required. Root eval stores completed passing metadata privately under KMX
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

Passing private receipts store contiguous per-run rows, with `run` ordinals and
the declared `runs`/`minPasses`, actual pass/fail verdicts, assertion evidence and
unique native identities. Only complete groups meeting the threshold export.
Default one-run receipt encoding is unchanged; older strict readers refuse
repeat rows. Diff compares per-run outcomes without treating different Task IDs,
answer hashes or judge scores as identity changes; the authored repeat policy
and judge configuration remain comparison identities.

The field names, defaults and validation follow
[AgentSuite §8.2.2 — Case documents and assertions](../agentsuite/spec.md#822-case-documents-and-assertions).
KMX uses private YAML authoring and receipts rather than the spec's embedded
JSON format. Its 100-run cap is an additional execution resource limit, allowed
by [§8.2.1](../agentsuite/spec.md#821-index-and-bounds); it refuses larger policies
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
prevents a case pass and export. Reasoning is discarded, never printed.
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
identity makes receipt comparisons incompatible. Offline push/lift do not select
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
kmx eval --workspace ./kagent-workspace --to-context kind-demo \
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
reconnect. Prompts, answers, assertion operands and reasoning stay out of KMX
terminal summaries and receipts, but runtime/model storage and Kubernetes audit
policy may retain them. Published suites themselves contain authored inputs.

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
kmx eval cleanup --to dev
kmx eval cleanup <full-run-id> --to dev
# Explicit access also works without the original workspace or inference:
kmx eval cleanup <full-run-id> --to-context kind-demo --to-namespace orka-system
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
their own verified-ownership process, then rerun read-only `kmx eval cleanup`.
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
`--evaluation` flag. `eval diff` reads strict passing suite receipts and
compares only compatible same-runtime subjects/case inventories; Orka/kagent
comparison refuses as incompatible, never as equivalent behavior. Comparison
also requires identical observed ownership. Fresh Orka candidate runs and separate
kagent CLI runs have different identities, so their receipts are incompatible
rather than a cross-run behavior comparison. Root Lift dispatches only matching runtime evidence;
cross-runtime evidence refuses. Old Orka/kagent/AgentSessions bundle receipts are
unreadable. `eval from-sessions` remains private reference extraction/export only,
with no native manifest consumer. RecordedExecution/legacy replay/judges are removed.

### Qualification boundary

The existing Orka runtime CI shard uses the pinned CPU AIKit workload, two text
cases and one configured rubric judge in exclusively owned kind clusters. It
checks historical API identity/spec/input/terminal metadata, actual UID/RV delete
preconditions and independent root/child absence after candidate evaluation.
Source answers, assertion verdicts and judge scores are product-observed after
cleanup, not independently re-fetched from deleted Tasks. Exact S transfers through
a local layout and pinned registry; source-withheld Lift requires explicit
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
kmx eval                             # saved evalTarget, no --out needed
kmx push ghcr.io/me/my-agents:v1       # supplied tag stays v1
kmx push                             # saved publishRepository:s-<full-S-hex>
```

Root push requires completed full-passing evidence for the exact prepared OCI
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

### Managed passing proof

Managed state is outside workspace/preparation cache, under
`KMX_HOME/passing/<workspace-root-hash>/<S-hex>/` (or the platform KMX state
location without `KMX_HOME`). Directories are 0700, records immutable 0600, bounded,
closed and symlink-refusing. Each completion record embeds the existing canonical
private receipt; no prompts, answers, credentials or binding bytes are added.
Existing explicit receipt formats are unchanged and are not automatically imported.
Use `--out` when a standalone file is needed for digest Lift or offline receipt diff;
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

One OS-held per-workspace lock excludes concurrent root eval/push; process exit
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
digest, a digest-pinned registry reference and `Required member images: []`.
The manifest digest binds member content, cases and private native metadata.

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
remedy to rerun `kmx eval` and `kmx push`. There is no migration reader.

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
Unsigned provenance is informational, not evaluation or promotion evidence.

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
root digest lift consumes that explicit private file before deployment.

## Digest lift

```sh
kmx lift registry.example/suite@sha256:<digest> --to-context prod \
  --inference provider:inference --evidence ../private/passing.json --promote --plan
kmx lift suite@sha256:<digest> --from-layout ./oci --to-context kind-demo \
  --inference provider:inference --evidence ../private/passing.json --promote -o json
```

Use a complete SHA256 digest, not a mutable tag. Destination runtime must already
be installed. `--to NAME` may supply the destination selectors from the workspace;
without it, explicit-reference Lift does not inspect workspace configuration. Orka uses `--inference provider:NAME`; packaged kagent instead uses
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
content to 9 MiB, raw decompressed tar to 10 MiB, 256 entries, 136 regular files,
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
lift reuse. Readiness does not prove a fresh answer or destination evaluation.
Opaque in-process success results support read-only invocation revalidation via
`VerifyReleaseLiftPlacement`; serialized JSON does not confer placement authority.

Unsigned private receipts establish supplied observations, not signer
authentication or authenticated deployment approval. General promotion policy/
signing remains separate. Orka uses native `openai` Providers; kagent uses explicit
OpenAI ModelConfig bindings. Host Foundry/Copilot execution and image lift are not
covered. Legacy bundle lift is removed. Hermetic tests alone do not establish
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

The existing pinned kind shard configures create, three-case evaluation (two text
cases and an open-ended rubric case), exact local
publication, source-free GET-only lift planning, explicit new-target promotion and
occupied repeat refusal, with zero additional native execution traffic.

Preparation state is outside authored source, under the KMX state directory
(`KMX_HOME` overrides it). Cache corruption refuses reuse. Keep this state private;
it is not a receipt, evaluation pass or promotion authorization.
