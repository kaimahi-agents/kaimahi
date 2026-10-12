# Author, evaluate and publish a one-member native-Orka suite

A workspace is editable source. Its prepared AgentSuite is an immutable OCI
artifact, identified by its manifest digest. The native-Orka execution profile
requires no member images; authoring and publication do not contact Kubernetes,
run a model or invoke Docker/buildx.

**Supported:** offline creation, exact preparation/publication and native Orka
evaluation of one tool-free member with an exact OpenAI-compatible model.
Registry-reference **release Lift is future work**, not a current command.
Existing `agent.yaml` creation, evaluation and bundle lift remain separate and
unchanged. Public AgentSuite/PortableAgent schemas and legacy receipts are unchanged.

## Create

```sh
mkdir my-agents && cd my-agents
git init
printf '%s\n' 'Answer briefly and in plain text.' > instructions.md

kmx agent create greeter --workspace . \
  --model qwen-3.5-2b --instructions ./instructions.md
```

`--instructions` names a file, not inline text. Its nonblank bytes are copied
unchanged into the member. `--description` is optional and single-line. Instructions use the native block-text
policy: LF and tabs are allowed; CR and other control characters are refused,
not normalized. This mode refuses cluster,
Provider, runtime, output, tools and Task flags, rather than ignoring them.
Creation refuses existing workspace/member directories, including PortableAgent
bundles; it does not overwrite or migrate them.

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

## Evaluate the exact candidate

Prepare the destination first: the namespace must exist and be the exact namespace
watched by pinned Orka **v0.2.0 harness-v2**, with its pinned controller/AI-worker
and matching AI worker account. Select an existing current-generation Ready
OpenAI Provider and its existing Secret/key in that namespace. KMX reads that
Provider as a binding source; it does not update it, copy credential values,
write a Secret or substitute its `defaultModel` for the packaged member model.
The model endpoint must serve the exact authored model.

An existing `orka-result-reader` ServiceAccount, namespaced Task-get grant and
controller API Service must permit short-lived token/result access. Native setup
or `orka install` provisions result access on its documented path; **evaluation
itself provisions no account/RBAC or runtime**. A token carries the account's
full effective authority, not result-only scope; discarding it is not revocation.
Orka v0.2.0 Task-read enforcement remains unverified. See [native result access](kmx.md#kmx-agent-create).

Use all selectors explicitly, without a positional bundle or global `--context`:

```sh
mkdir -m 700 ../evidence
kmx agent evaluate --workspace . \
  --to-context kind-proof --to-namespace orka-system \
  --inference provider:local --out ../evidence/run-1.json
```

KMX freezes the prepared OCI manifest **S**, loads only its exact packaged
instructions/model/cases, places only the suite-owned Provider and Agent, waits
for UID/generation/marker-bound Ready and executes one native **`type: ai` Task**
per selected case, serially and without resubmission. It refuses adoption of
unmarked, bundle-owned or mixed-owned resources. Conversely, legacy create/lift
refuse suite-owned pairs. Namespace, Provider/Secret, runtime and owned-pair drift
make attribution unavailable, not a pass. Readiness alone is not execution proof.
No member image build, AgentRuntime, application Deployment or model override
is part of this mode; `requiredImages: []` is explicit.

Only a complete **full passing** run exits zero. `--case ID` is diagnostic and
always exits nonzero, even for a passing one-case suite. Unsupported tool
assertions, unavailable results, failures and incomplete coverage cannot qualify;
unknown is never skip/pass. `--case-timeout` defaults to 5m (nondefault 10s–9m)
and includes result access and any judging. `--result-port` must name a free
loopback port; another listener is refused, not hijacked. Cases with `llmJudge`
require the explicit existing judge transport/credential-name selectors and
send input/answer/rubric to that judge; see [judge privacy](agent-lift.md#explicit-llm-judge).

Default terminal output contains verdicts, IDs/digests and private paths, not
inputs, answers or reasoning. `--show-content` deliberately opens the answer/judge
stdout sink; the receipt still stores no such payloads. Kubernetes/model/Orka
journals and audit policy can retain execution content independently of KMX.
Do not publish private model inputs or operational bindings accidentally.

`--out` must be a **new** file with an existing parent outside authored source and
cache. Export and immutable history under `KMX_HOME/suite-evidence/runs/` are
private (0600 files/0700 history), explicit suite-evaluation metadata, not the
legacy bundle receipt format. No latest-receipt discovery is trusted. They bind
S, exact case/input/assertion identities, target/Provider/Secret/runtime snapshots,
owned UIDs/generations, Task UIDs and observed result digests, never prompt,
answer, rubric, reasoning, token or Secret values. They are **unsigned
operator-owned evidence**, not authenticated provenance or destination execution.

Evaluation also selects a durable private candidate under
`KMX_HOME/suite-candidates/`. Selection is not a pass: failed/diagnostic runs can
select S but cannot qualify it. Publication preserves that selected descriptor
and package bytes. If model, instructions or cases changed since selection,
push refuses with rerun-evaluation advice; explicitly rerun evaluation against
the intended source and use a new export, rather than rebuilding silently on push.
Corrupt/missing cache state refuses; it is not repaired by publication.
Competing candidate writers fail immediately. After a crash, inspect any reported
stale lock and establish writer quiescence before manually removing it; KMX does
not guess authority or retry. Errors can leave honest history or already-created
resources; inspect before acting, never infer rollback from a nonzero exit.

## Publish

```sh
kmx suite push --workspace . ghcr.io/me/my-agents:v1
```

The command resolves an allowlisted snapshot into the existing AgentSuite format,
validates it, prepares a private local OCI layout under KMX state, then copies
that exact descriptor graph to the registry. After evaluation it loads the
selected candidate, checks authored-source drift and publishes only that same S.
It does not re-archive the prepared artifact during publication. Output includes the suite digest, a digest-pinned
registry reference and `Required member images: []`.

The content includes exact member instructions and authored case files under
`metadata/kmx/`, plus a versioned native execution declaration. Cases are published
content: use deliberately shareable inputs. Git internals, local receipts,
Provider bindings, promotion policies and unrelated workspace files are not
packaged. Changes to model, instructions or cases produce a different suite
identity; old evidence cannot qualify the changed content.

The OCI manifest digest binds all the content-layer bytes, including cases.
The canonical `agentsuite.json` graph digest alone does not bind supplemental
metadata. No outer package envelope is used. The public format still
requires a composition and pinned build-profile records; this tool-free native
profile emits a composition with no ToolProviders. Those profile records
are declared image-build inputs, not dependencies fetched or executed by native
Orka placement, and do not imply sandbox-image conformance.

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
kmx suite push --workspace . --to-layout ../suite-layout my-agents:v1
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
Existing extracted-directory transfer remains `suite push DIRECTORY REFERENCE`;
`--workspace` instead takes only one positional destination reference.

Preparation state is outside authored source, under the KMX state directory
(`KMX_HOME` overrides it). Cache corruption refuses reuse. Keep this state private;
it is not a receipt, evaluation pass or promotion authorization.

## Promotion policy and future release Lift

The implemented **private pure gate** validates exact native evidence against an
independently established expected subject and an operator-owned **JSON** policy
(`promotion.json`, not YAML). Its closed `suite-promotion` v1 shape pins the
canonical receipt digest, RunID, full evaluated and destination subjects, plus
an explicit `allowedTransitionPaths` list. No wildcard, unused permission,
model override, partial run, latest-selector or unsigned OCI referrer can stand
in for those pins. Native image requirements and execution pins cannot transition.
A permission decision always leaves `destinationEvaluated: false`.

This is an internal validation boundary exercised by qualification/tests, **not
an exposed policy-generation command or current registry-reference Lift flag**.
Do not treat a copied receipt/policy as a signature; the operator controls their
contents and trust. Future release Lift must independently prepare its destination,
review permitted target/binding transitions, preserve evaluated S and distinguish
permission from destination Task execution. Current `agent lift <bundle-dir>`
remains the documented bundle operation, not that future suite-release path.

## Qualification scope

The dedicated `e2e-suite-native` CI shard runs the actual checkout-built CLI on
an owned kind cluster with pinned Orka 0.2.0 and a digest-pinned CPU AIKit model.
It verifies real native results, strict full-case evidence, read-only source
bindings, pure policy decisions, exact-S local transfer and fail-closed controls.
Model warmup is unscored. Its Docker/kind/Orka/AIKit infrastructure is not a
member image dependency, a general runtime-equivalence proof, terminal Job
lineage attestation, hosted-model guarantee or destination execution claim.
See [local qualification](../CONTRIBUTING.md#native-suite-qualification).
