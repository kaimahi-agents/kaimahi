# `kmx agent lift`

`kmx agent lift` deploys an existing on-disk agent bundle to a prepared Orka
cluster. It takes a named destination and inference Provider. Interactive
`/lift` in chat or `kmx console` selects those inputs and shows the same
read-only plan before invoking this operation. A separately confirmed
**Prepare target** action can install Orka or the quickstart Kubernetes Tool
before retrying the checks; bundle lift itself never installs them.
This command is intentionally Orka-only: a portable bundle authored by explicit
Kagent create is refused before cluster reads. Kagent create receipts do not
enable lift, status, evaluation, console bundle actions, or interactive `/lift`.

```console
kmx agent lift <bundle-dir> --to-context <ctx> [--to-namespace <ns>] --inference provider:<name> [--require-evaluated <context>] [--override-gate "reason"] [--plan]
```

The bundle contains `agent.yaml` (the portable definition) and
`bindings.yaml` (its creation-target bindings). An `agent.yaml` with no
`extensions` block or `extensions: {}` can be lifted to Orka: its name,
description, instructions and model render exactly as they do with an
apiVersion-only Orka extension. Named helper delegation can also be authored
in portable `spec.coordination.allowedAgents`. Orka-specific tools, skills,
rate limits and optional delegation limits require the Orka extension; a target
that does not consume them refuses the named fields rather than dropping them. `kmx agent create` still
writes the Orka extension. Lift reads the definition from
`agent.yaml`, resolves **new** target bindings, and renders with
`RenderOrkaBundleFile`. It does not copy a Secret or take inference from the
bundle's original bindings. The exact bytes of `agent.yaml`, including comments
and whitespace, determine the portable digest. A rendered digest identifies
the target-specific rendering. See [bundle format compatibility](bundle-format.md)
for accepted versions, strict decoding and cross-version behavior.

## Coordination in `agent.yaml`

A coordinator's delegation policy is portable behavior, not a destination
binding. A core-only coordinator names its allowed helpers in the same target
scope; no helper is implicitly permitted:

```yaml
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: coordinator
spec:
  instructions: Delegate the calculation to helper, then summarize its answer.
  model:
    name: qwen2.5:3b
  coordination:
    allowedAgents:
      - name: helper
```

The core list must be nonempty, unique, name-only and cannot include the
coordinator itself. Orka renders it as `enabled: true` with exactly those
helpers and no stated limits. The older Orka-extension form remains supported
for bundles that need explicit `enabled` or Orka-specific limits:

```yaml
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: coordinator
spec:
  instructions: Delegate the calculation to helper, then summarize its answer.
  model:
    name: qwen2.5:3b
extensions:
  orka:
    apiVersion: core.orka.ai/v1alpha1
    agent:
      coordination:
        enabled: true
        allowedAgents:
          - name: helper
        maxConcurrentChildren: 2
        maxDepth: 2
```

Do not specify both forms in one bundle, including an Orka block with
`enabled: false`. Moving an existing Orka coordinator to the core form is a
new portable revision; old authored bundles keep their digests and rendering.
Kagent refuses a core coordinator; AX lift is not available.

In the Orka-extension form, `enabled` is required when the block is present.
With `enabled: true`, list at least one allowed Agent: Orka's AI worker treats
an empty list as permission to delegate to **any** Agent.
`maxConcurrentChildren` must be positive and `maxDepth` must be 1–10. If
neither the core nor legacy block is present, Orka renders no
`spec.coordination`; omitting the limits lets Orka supply its defaults of
**5 concurrent children** and **depth 3**. kmx does not insert those defaults in the rendered Agent.
`allowedAgents` entries contain names only: when coordination is enabled, lift
looks for each Agent other than the coordinator itself in the **destination
Agent's namespace** and refuses a plan if any is absent. A `namespace` in an
entry is refused because the namespace belongs to the destination, not the
portable definition. Lift also refuses a target whose installed Agent CRD does
not support coordination. The upstream `autonomous` field is not supported yet:
it starts a repeated Job loop and needs a separate design.

`kmx agent create --coordination --allowed-agent helper` authors `enabled: true`
and the named helper; repeat `--allowed-agent` for additional helpers. Edit the
bundle for optional limits. Lift the helper before the coordinator. The pinned
Orka installation binds delegation worker RBAC only in its release namespace
(`orka-system`); kmx does not grant coordination worker permissions in another
namespace. Editing coordination changes the exact-source portable digest; a
lift updates the owned Agent and status reports cluster-side edits as drift
when the live portable digest still matches the bundle.

## From create to evaluation

For a local Ollama creation target that already has Orka, the namespace and a
Provider Secret, create a bundle like this (replace the source context and
Secret name with your own):

```bash
kmx agent create my-agent --context source-context --namespace orka-system --provider-type openai --model qwen2.5:3b --secret my-provider-secret --base-url http://ollama.ollama.svc.cluster.local:11434/v1
```

The `--base-url` must be reachable from the creation cluster; it is not inferred
from the model name. Create writes `agents/my-agent/` and scaffolds
`eval/example.yaml`. Edit that case's input and expected answer before testing
it. The destination must separately have Orka, its namespace, a Ready Provider,
the Provider's Secret and any referenced Tools; lift installs none of them.
Use `kmx agent lift agents/my-agent --to-context my-target --inference provider:local`
after replacing `my-target` and `local` with the destination
context and Ready Provider. `--plan` inspects intended changes without writing.
Then use `kmx agent status agents/my-agent --to-context my-target` to inspect
what is deployed and `kmx agent evaluate agents/my-agent --to-context my-target`
to run the cases against that deployed digest. Each evaluation creates a Task
that can have external effects and is never retried; a passing receipt is
evidence for that revision and case set, not a general safety proof. To make
this evidence a prerequisite for a later lift, use a committed
`lift-policy.yaml` or `--require-evaluated` as described below.

## Destination and inference

On first use for a bundle, name a destination with `--to-context` and inference
with `--inference provider:<name>`. A successful lift remembers those choices
locally for that bundle, not in Git. A later lift may omit them. Reusing a
remembered target requires both its context name and the UID of its
`kube-system` namespace to match the recorded identity: a missing, ambiguous,
or repointed context is refused rather than falling back to kubeconfig's
current context. Confirm the resolved context, cluster and namespace displayed
before a write. The existing remote-context guard still applies to writes.
Context names are human-readable labels, **not** cluster identities for the
evaluation gate.

`provider:<name>` identifies an **existing Ready Provider** in the destination
namespace. Lift reads that Provider's type, endpoint and Secret reference and
uses them as the render bindings. The bundle renders its own Provider named
after the Agent; the selected destination Provider is never modified. No
Foundry provisioning or "keep the source configuration" mode is available.
The referenced Secret must already exist in the destination namespace; its
value is neither copied into the bundle nor printed. For an `azure-openai`
destination, its `azure.deploymentName` must match the bundle's portable
`model.name`. Orka uses the model name to address the Azure deployment; kmx
refuses a mismatch (including `--plan`) and names both values instead of
silently changing the model. A different deployment is a different model: the
revision that passed evaluation must be the one that runs. Author and evaluate
a new revision if you intend to change deployments. If the destination
Provider omits `azure.apiVersion`, Orka's CRD can supply a default on apply;
set the version explicitly when you need a predictable choice.

## Preparation and refusal

Lift checks prerequisites; it never installs them:

- The Orka CRDs exist and the controller is Ready. Prepare a target with
  `kmx orka install` if needed.
- The destination namespace exists. Provision a target cluster with `kmx aks up`
  if needed; create a missing namespace separately.
- The selected destination Provider is Ready, and its referenced Secret exists
  in that namespace. Provision these separately before lifting.
- Every enabled Tool referenced by the bundle is Available for its current
  generation in the destination. For an HTTP Tool with an
  `outboundAccessPolicyRef`, the configured Orka AI worker in the Agent's
  namespace must also have effective `get` access to that **named policy** in
  the Tool's namespace.
  The same read-only check runs during `--plan`, deploy and bundle-less
  live-copy `/lift`; an Available Tool alone does not prove worker access.
  Prepare Tools separately before lifting.

Each missing prerequisite is reported separately with the relevant preparation
command where one exists. The quickstart Kubernetes inventory Tool requires its
same-namespace `OutboundAccessPolicy` to be Accepted before the Tool becomes
Available; a target missing or rejecting that policy is refused until repaired.
If a lift refuses despite `Tool/k8s-get-resources` being Available, inspect
the named policy-reader RoleBinding for the installed chart's actual AI worker
ServiceAccount in the Agent's namespace and its effective named-policy `get`
permission. The refusal names the account, namespace, policy and verb. For a non-allowing review
(explicit denial or NoOpinion) on the built-in policy in `orka-system`, console
offers a separately confirmed **Prepare target** action to reapply the
quickstart Tool's exact grant, then
rechecks the lift preflight. API failures, indeterminate reviews and custom
policies are not repair offers. Do not bind a guessed worker, add `list`
privileges or bypass `--plan`.
See [the quickstart Tool repair guide](orka-k8s-tool.md#repair-a-missing-policy-reader-grant).
Lift creates no AKS cluster, namespace, Orka installation, Tool, policy, Secret
or Task. It does not change or delete the source Agent. Evaluation-gate
refusals and recovery are described below; a gate refusal occurs before any
Provider or Agent deployment, not after a partial reconciliation.

## Requiring evaluation before lift

A bundle may have a `lift-policy.yaml` beside `agent.yaml`. Commit this policy
with the bundle to express the staging-before-production requirement for the
team: each rule pairs a **destination cluster UID** (the UID of its
`kube-system` Namespace) and destination namespace with an **evaluation
cluster UID** and evaluation namespace. For example, a rule for the production
cluster UID and `orka-system` can require a passing evaluation from the staging
cluster UID and `orka-system`:

```yaml
rules:
  - destination:
      clusterUID: <production-kube-system-uid>
      namespace: orka-system
    evaluated:
      clusterUID: <staging-kube-system-uid>
      namespace: orka-system
```

Replace the UID placeholders with the corresponding clusters' `kube-system`
Namespace UIDs, not kubeconfig context names. The file must be a regular file
with at least one rule; unknown fields, incomplete targets, duplicate
destination identities and rules that gate a target on itself are refused.
A destination with no matching rule is not gated by that policy. Names such as `staging` and `production` in
kubeconfig are display labels only: renaming a context does not change a
rule's identity, and repointing one must not turn another cluster's evaluation
into evidence. Policy selects the required evaluation target; it does not run
evaluation, copy credentials or create a deployment. Keep the mapping in Git;
it is separate from the portable revision digest, which covers only
`agent.yaml`.

For an ad-hoc gate without changing the policy, pass
`--require-evaluated <context>` to `kmx agent lift`. It adds a requirement for
this invocation (it does not replace a policy rule). This names an evaluation
receipt's context label to select the required evaluation target; its recorded
cluster UID and namespace establish identity. No receipt or several identities
sharing that label is refused, without contacting that cluster. A context label alone must never satisfy the gate. Run a full
`kmx agent evaluate <bundle-dir> --to-context <staging-context>` first;
`--case` produces a single-case receipt and does not satisfy a full-case gate,
even when `eval/` has only one case.
The evaluation destination must already have this same portable revision
lifted, and its receipt must say `pass` for the current complete `eval/` case
set and record a passing result for each current case. A changed `agent.yaml`
or case file requires another evaluation. If the required evaluation identity
or matching local evidence cannot be established,
the lift is refused rather than silently ungated.

The gate checks **local receipts only**. It does not contact the evaluation
cluster, re-run Tasks, confirm the evaluation Agent is still present or Ready,
or independently attest the receipt. Local receipts are forgeable and are
**not intended to be committed**; protect them accordingly. The committed
policy travels with teammates, but receipts do not: a teammate without their
own matching passing receipt must lift to staging and evaluate there before
lifting to production. Neither a lift receipt nor a `kmx agent status` display
of `pass` substitutes for full matching evaluation evidence. An evaluation
receipt's context is a label; cluster UID and namespace are the gate identity.
Receipts written before the cluster UID and full-case-run marker were recorded
cannot satisfy this gate; rerun the complete evaluation to generate new evidence.

| Gate condition | Refusal and next step |
|---|---|
| Policy cannot be decoded, or the ad-hoc context has no recorded identity or matches several | Correct the policy or run a full evaluation for an unambiguous context; do not infer identity from a matching name. |
| No local receipt for the required cluster UID and namespace, or only a different target's receipt | Lift the bundle to the required evaluation destination and run `kmx agent evaluate` there. |
| Receipt is `fail` or `unknown`, covers only a `--case` selection, or lacks passing results for every current case | Fix the cases or target and run the **full** evaluation again; an individual passing case is insufficient. |
| Receipt's portable digest or case-set digest differs from the current bundle | Lift and evaluate the changed revision or cases again. |

Multiple receipts for the same UID and namespace under different context
labels must agree on the portable digest, case-set digest and evaluated Agent
UID; disagreements refuse and list the aliases rather than picking one.
A gate refusal identifies the unsatisfied condition rather than accepting a
receipt for the wrong UID or namespace. Correct policy parsing and identity
errors rather than treating an override as validation of an ambiguous rule.

A deliberate exception uses `--override-gate "reason"` on the lift. Give an
explicit reason, not an empty flag; the CLI prints the reason and the gate
condition it bypasses to stderr and records both in the destination lift
receipt. This does not turn a failed or missing evaluation into a pass and
is not a way to skip other lift prerequisites or reconciliation refusals.
The override remains visible in that local receipt until the **next lift to
that target** overwrites it; it is not a permanent policy waiver. Treat the
reason as operator-visible local audit data, not a trusted authorization
record. Never put credentials, tokens or Secret values in the reason.

Interactive `/lift` and the console's bundle-backed lift use the same gate
check while planning. A passing gate appears in the read-only review; an
unsatisfied gate stops planning with the refusal condition, before the final
deployment review. They do not silently deploy or offer an implicit waiver: run
the required evaluation first, or use the CLI with an explicit
`--override-gate "reason"` for a deliberate exception. The separate
bundle-less live-copy fallback does not write bundle receipts and is **not**
a gated bundle lift; an existing but invalid bundle is never silently copied.

For CI, provision the target namespaces, Orka, Providers, Secrets and Tools
before this sequence; use your actual kubeconfig contexts and Providers, and
check in `lift-policy.yaml` with a rule mapping the production UID/namespace
to the staging UID/namespace:

```bash
# Bootstrap staging first: evaluate requires a deployed matching revision.
kmx agent lift agents/my-agent --to-context staging --to-namespace orka-system --inference provider:staging-provider
kmx agent evaluate agents/my-agent --to-context staging
# The committed policy requires the staging result before the production write.
kmx agent lift agents/my-agent --to-context production --to-namespace orka-system --inference provider:production-provider
```

Keep the commands in the same CI workspace so the local evaluation receipt is
available to the final lift; a fresh workspace needs to run staging and
evaluation again. These commands do not bootstrap Orka or inference resources.

## Plan, reconciliation and outcomes

`--plan` resolves the bundle, target and inference, checks prerequisites, and
inspects the same rendered Provider and Agent that execution would deploy. It
uses **the same read-only inspection and classification code as reconcile
Deploy**, including server dry-run comparison, rather than separate lift rules.
It writes no cluster resources, receipt or remembered target. If a comparison
cannot be established without a write, its result is **unknown**, not a claim
that the resource would be reused. The plan also reports the gate condition
and whether the available local evaluation receipt satisfies it (or why it
would refuse), without writing a receipt or running an evaluation. A plan that
reports an unsatisfied gate exits nonzero and is **not** permission to deploy: fix the evidence,
or explicitly supply a reasoned override at execution. `--override-gate` does
not make `--plan` record an override.

Without `--plan`, lift renders with `RenderOrkaBundleFile` and calls
`Deploy(ctx, rendered, DeployOptions{Reconcile: true})`. Reconciliation
reports an outcome for **each** resource:

| Outcome | Meaning |
|---|---|
| Created | The resource was absent and is created. |
| Reused | An owned resource already renders identically; no replacement is needed. |
| Updated | An owned resource differs; show its differing fields and replace it with a resourceVersion precondition. |
| Adopted | An identical unowned resource receives the ownership markers. |
| Refused | A conflicting, incomplete, invalid or terminating resource cannot be reconciled; lift stops with an error. |

Ordinary updates do not require a prompt; an interactive terminal may confirm
them. Ownership annotations are `kaimahi.dev/bundle`,
`kaimahi.dev/portable-digest` and `kaimahi.dev/rendered-digest`. Lift also
sets `kaimahi.dev/origin` to `created` or `adopted` on the first write of each
object and preserves it on later lifts. Older owned objects without this marker
keep it absent: kmx cannot infer whether they were originally adopted. These
annotations are applied outside the immutable rendered documents and do not
change their digest. Reconciliation refuses Tasks. A partial failure may leave resources
already written; inspect the error and rerun after addressing its cause rather
than assuming an automatic rollback.

## Receipt and Git provenance

After successful deployment, lift writes `DeployResult.Receipt` as JSON under
`<bundle-dir>/receipts/`, one receipt file per target, together with the
revision provenance. It records the Git commit when `agent.yaml` is tracked,
committed and unchanged against `HEAD`. Otherwise provenance is
`uncommitted`, with a warning once; unrelated untracked files do not affect
this test. The receipt is local deployment evidence, not part of the portable
revision: the portable digest covers **only** the bytes of `agent.yaml`, and
`receipts/` does not affect Git cleanliness of `agent.yaml`. Keep lift and
evaluation receipts local, not in Git: neither is an attestation, and an
override reason stored in a lift receipt may contain operational details.
A later `kmx agent create` rerun can reuse the bundle even when `receipts/`
is present.
No receipt is written by `--plan` or by a failed deployment. If deployment
and receipt writing succeed but saving the remembered target fails, the
command returns an error and the receipt remains on disk.

The receipt binds deployment outcomes and target identities to the portable
and rendered digests; it is not proof that an Agent answered a Task. Use
[`kmx agent evaluate`](#evaluating-a-deployed-revision) for execution evidence
bound to the same digest.

## Evaluating a deployed revision

```console
kmx agent evaluate <bundle-dir> [--to-context <ctx>] [--case <id>] [--case-timeout 5m]
```

Evaluation cases live beside the bundle in `eval/*.yaml`, one case per file:

```yaml
id: sign-off
input: Summarize today's plan in one sentence and sign off.
expectContains:
  - "— the release agent"
```

Decoding is strict: `id`, `input` and a non-empty `expectContains` list are
required, unknown fields are refused, and each file holds exactly one YAML
mapping with a unique `id`. Cases test a revision; they do not define it, so
they are **not** part of the portable digest. `kmx agent create` scaffolds one
trivial `eval/example.yaml` in a new bundle, and a create rerun accepts any
`eval/` directory without touching its cases.

Evaluate resolves its destination the way lift and status do: an explicit
`--to-context`, or the bundle's remembered target, whose `kube-system` UID must
still match. Before any case runs, the live Agent must be owned by this bundle
and carry exactly the bundle's current portable digest; otherwise evaluate
refuses with `deployed revision differs; lift first`. Results are bound to
that digest. Because every case executes a Task, evaluate then passes the same
remote-context guard as other Orka writes.

Each case is one Orka Task against that Agent, created and read the way
`kmx agent create --task` does it: the `orka-result-reader` account over a
pinned loopback port-forward (`--result-port`, default 19180). Each case waits
for a terminal state within `--case-timeout` (default 5m, at most 9m). A case
is never retried, because a Task can have side effects, and Tasks are not
cleaned up.

| Verdict | Meaning |
|---|---|
| `pass` | The Task succeeded and its answer contains every `expectContains` string (exact, case-sensitive). |
| `fail` | The Task ended `Failed` or `Cancelled`, or the answer lacks an expected string. |
| `unknown` | The outcome could not be observed: the result was unreadable, the case timed out, the create was ambiguous, the case was refused before a Task was created, or the Agent's revision changed while it ran. |

Every answer is printed to the terminal. The receipt,
`<bundle-dir>/receipts/eval-<target>.json` (the same per-context, namespace
and cluster key as the lift receipt), records the portable digest, a digest of
the case files that ran, the Git provenance, the target (including its
cluster UID) and live Agent UID,
and per case the id, verdict, matched and missing expectations, Task name and
UID, and a SHA-256 of the answer. It also marks whether the whole case set ran
without `--case`. **It never contains answer text**, but it is
forgeable local evidence, **not intended to be committed**. Store it in the
same local workspace used by a gated lift; a teammate or CI job without the
receipt must evaluate independently. `--case` runs one case; its receipt
covers only that case's file, so status does not count it as the bundle's
case set.

Evaluate is a gate: it exits non-zero unless every case passed, including when
any case is `unknown`.

## Evaluating on agentsessions

```console
kmx agent evaluate <bundle-dir> --sessions 127.0.0.1:8080 [--case <id>] [--case-timeout 5m]
```

This selects an agentsessions host instead of a deployed Orka target. No
Kubernetes reads, lift, or remembered cluster selection are needed. Start the
host yourself with the **chat** harness and a model matching `spec.model.name`.
The reference daemon registers chat when `-model` is set, but still defaults
to echo; kmx explicitly selects chat. The host must support execution config
`system_prompt` ([agentsessions #78](https://github.com/aramase/agentsessions/pull/78)).
For example, with an already-running local OpenAI-compatible Ollama endpoint:

```console
agentsessionsd -addr 127.0.0.1:8080 -journal ./evaluation.db -model qwen2.5:3b -model-base-url http://127.0.0.1:11434/v1
kmx agent evaluate agents/my-agent --sessions 127.0.0.1:8080
```

Each case creates one new session, labeled with the portable and case-set
digests, and executes its input with the bundle's exact decoded instructions
as `system_prompt`. Cases never share history or retry a mutation. The model
comes from the host, not creation bindings or session metadata. kmx observes
model-call records and refuses to pass a missing, mixed, or different model;
it does not switch models or infer equivalent aliases. `pass` requires a
completed execution and all exact, case-sensitive `expectContains` strings;
a missing expectation is `fail`, while an unproven execution is `unknown`.
The returned journal must record the exact invocation config and input before
its effects. Committed answer text is limited to 1 MiB in total; an oversized
answer is `unknown` and has no output digest. Any non-passing case makes the
command exit non-zero.

The text-only chat harness accepts core-only sources and Orka sources without
runtime-specific behavior. Kagent extensions, coordination, tools, skills and
rate limits are refused before sessions are created, rather than silently
ignored. `--sessions` cannot be combined with `--to-context` or `--result-port`.
The per-case timeout has the same 10s–9m bounds as Orka evaluation.

Only literal loopback IPs may use plaintext. Other addresses use verified TLS
with system roots; `--sessions-ca <pem-file>` adds a private trust root and also
selects TLS for loopback. There is no unverified fallback. The reference daemon
has no TLS listener or access controls: remote use requires operator-managed
TLS termination and appropriate authentication/authorization at the boundary.
Do not expose the daemon itself to an untrusted network.

The private `receipts/eval-<key>.json` records runtime `agentsessions`, digests,
Git provenance and full-case-set status. Each case records its session UID,
journal sequence/hash head, verdict, observed model and output SHA-256. The
version-1 `target.identity` labels its address, returned harness name and
journal-observed model as **`host-reported`**, not a verified descriptor or
attestation. Per-case identities remain available when observations differ.
Distinct model names within one execution set `modelMixed: true` and suppress
its singular model identity, even if a later stream error masks the mismatch.
Identity versioning leaves room for future descriptor discovery. Prompts,
answers, expectation strings, tool payloads and arbitrary server errors stay
out of this receipt; conversation content remains in the host's journal.
New receipt directories are mode 0700 and receipt files are mode 0600. Each
endpoint/chat selection replaces its prior receipt; model changes therefore
cannot leave an old passing receipt at that same filename. Keep receipts local,
not in Git, and protect the journal separately.

Sessions receipts do **not** satisfy lift-policy or status gates, including
when replay verification succeeds. Orka receipt and gate behavior is unchanged.

### Verifying a sessions receipt

```console
kmx agent verify <sessions-receipt.json> --sessions 127.0.0.1:8080 [--sessions-ca ca.pem] [--timeout 5m]
```

Verification reads each case's journal prefix through the receipt's recorded
sequence, checks the contiguous hash chain and exact sequence/hash head, then
runs agentsessions' pinned built-in **chat** harness locally with the recorded
config, input and model completions. It checks model-request fingerprints,
complete consumption of the recorded effects and the exact answer SHA-256.
There is no model endpoint configured; both the independent fail-on-call counter
and the replay controller's live-model count must remain zero. No new session,
Exec, Resume, or live journal/fence mutation occurs. Later records beyond the
receipt's head are outside this check.

`--sessions` is required: the operator always names the destination. kmx refuses
**before connecting** unless it matches the receipt's recorded address after
syntactic normalization: canonical literal IPs (including IPv4-mapped IPv6),
lowercase DNS names without a trailing dot, and decimal ports. No DNS lookup or
alias equivalence is used for comparison; `localhost` does not match
`127.0.0.1`. Only the operator-supplied address is dialed, retaining its original
spelling so normalization cannot relax transport security. Connection
security is the same as evaluation: verified TLS except for literal loopback,
with `--sessions-ca` also enabling TLS there. The deadline covers the whole
verification and must be between 10s and 9m (default 5m).

This proves **local reference replay equivalence**, not the original host
implementation/version, provider-side behavior, or a new evaluation result.
The reference includes `system_prompt` support; config-ignoring older journals
may be non-equivalent without being corrupt. Other harness names, forks,
tools, incomplete/extra executions and unsupported journal shapes never pass.
A lookalike custom `chat` harness can only be shown reference-equivalent; host
implementation/version stays unknown until
[agentsessions #87](https://github.com/aramase/agentsessions/issues/87) exposes
provenance. The locally trusted receipt anchors integrity; replacing both it
and the journal is outside this guarantee.

Existing version-1 sessions receipts need no migration. Each case must have a
completed `pass` or `fail` evaluation verdict and full session/head/model/answer
evidence; `unknown` outcomes are refused. A failed expectation can replay
equivalently without becoming a passing evaluation. Receipts are limited to
1 MiB and 1,000 cases; journal prefixes to 20,000 records and 64 MiB, with the
same 1 MiB committed-answer bound as evaluation. Conversation content stays in
memory during verification and is never printed or written into the report.

kmx leaves the source receipt unchanged and atomically writes a mode-0600
sibling `verify-<receipt-basename>.json` (for example,
`verify-eval-abc.json.json`), outside the `eval-*` gate namespace. It binds the
source receipt's SHA-256, the local reference revision, per-case session/head,
config and reconstructed-answer SHA-256, model-call count and verification
status. Host implementation remains `unknown`. Linked/non-regular input files
or a linked receipt directory are refused. Duplicate JSON members (including
case-folded spellings) and unknown fields are refused before connecting.
Every invocation recomputes the
proof, replacing any prior report. Only `equivalent` for every case exits zero;
`mismatch`, `unsupported` and `unknown` never count as passing evidence.

## Run evals in CI

Use the [KMX eval action](../.github/actions/kmx-eval/action.yml) to run a
bundle's complete case set, fail the job unless every case passes, and optionally
replay-check the receipt. **Sessions evals currently refuse tools and
coordination**, as well as skills, rate limits and Kagent-specific behavior.
Agents that use tools or coordination need the
[Orka evaluation path](#evaluating-a-deployed-revision) for now; this action does
not deploy Orka or make sessions receipts satisfy a lift gate.

Run on Linux with Git, Python 3, curl and Go 1.26 or newer. AIKit mode also
needs Docker. Check out the commit you want to test; the agent and the
complete `eval/*.yaml` set must be tracked and byte-identical to `HEAD`, including
staged changes. The action evaluates **in that checkout**, using KMX's existing
Git provenance reader: receipts name the tested `HEAD` commit, including a
GitHub PR merge commit when that is what checkout selected. No claimed revision
is substituted from an environment variable. Dirty, added or deleted cases are
refused rather than attributed to the wrong commit.

For an OpenAI-compatible endpoint, supply the model name exactly as it appears
in `agent.yaml`. Replace `REVIEWED_COMMIT_SHA` with a reviewed immutable commit
containing the action; do not leave the placeholder or use a moving branch in
production:

```yaml
jobs:
  eval:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.2'
          cache: false # This bundle repository need not have a go.sum.
      - uses: kaimahi-agents/kaimahi/.github/actions/kmx-eval@REVIEWED_COMMIT_SHA
        env:
          EVAL_MODEL_KEY: ${{ secrets.EVAL_MODEL_KEY }}
        with:
          bundle: agents/my-agent
          model-mode: endpoint
          model-name: my-model
          model-base-url: https://model.example.com/v1
          model-key-env: EVAL_MODEL_KEY
          verify: 'true'
          artifact-name: agent-eval-evidence
```

The endpoint must use HTTPS; keyless literal-loopback HTTP endpoints are also
accepted. Never put a key in an input or URL: `model-key-env` is only the name
of a secret environment variable. The runner passes its value to the daemon's
model client through the child environment, not argv or a file. It does not
print CLI, provider or daemon logs. Successful logs contain only summaries and
timing. On failure it prints the failing answer only if its exact bytes match
the receipt's digest, with known credentials redacted and terminal/workflow
commands escaped; ambiguous answers or credential-shaped text are withheld.
Failure answers can still contain sensitive application data: use public or
sanitized cases and restrict access to job logs.

For a secret-free local model, use `model-mode: aikit` instead of the endpoint
inputs. The default image is Qwen3.5-2B pinned by SHA-256, with the same CPU
[preset](../scripts/ci/eval-loop-model.yaml) used in repository CI. The bundle's
model must be `qwen-3.5-2b`. This small model and its 4096-context/64-output-token
preset are not a universal quality baseline. `aikit-image` must include
`@sha256:...`; `aikit-config` can select a model-specific LocalAI config, paired
with the corresponding `model-name`. No inherited hosted-model key is used.

The action starts agentsessions on loopback with a private, temporary journal.
Prefer a compatible released daemon archive through the paired
`sessions-archive-url` and `sessions-archive-sha256` inputs: the HTTPS archive
must contain a regular `agentsessionsd` binary and support chat, execution
`system_prompt` and the journal evidence required by KMX. A checksum proves
archive identity, **not compatibility or host implementation attestation**.
The latest inspected release, v0.1.2, predates that support, so the default is
currently a clearly reported source-build fallback at KMX's pinned module
revision, never `@latest`. The log reports install mode and elapsed runner time,
including builds; the workflow job duration additionally includes setup/upload.

`verify: 'true'` (the default) checks reference replay equivalence and zero model
calls separately from evaluation. AIKit is stopped before verification; an
external endpoint is not stopped, but replay still requires zero calls. A
failed evaluation never becomes a success through replay. Set `verify: 'false'`
to omit it. `case-timeout` defaults to `2m`; `command-timeout` is a separate
per-command deadline in seconds (default `600`). Adjust the workflow job timeout
to fit the complete case set; cases are never retried.

Only the fresh payload-free receipt and optional verification report are
uploaded, including failed results when available. Journals, raw logs, prompts
and answers are not artifacts. Each invocation uses a fresh private artifact
directory; before evaluation it removes only KMX's receipt/report for its
selected sessions endpoint, preserving evidence from other targets. The action
outputs `receipt`, `verify-report`, `artifact-dir`, `daemon-install` and
`elapsed-seconds`. Files in the output directory survive runtime cleanup.

### Other CI systems

Check out a reviewed Kaimahi revision beside your clean agent checkout, provide
Go and the prerequisites above, and call the same runner. For example, from
your agent repository, with Kaimahi checked out at `../kaimahi`:

```bash
# EVAL_MODEL_KEY is injected by your CI secret store, not assigned here.
BUNDLE_DIR="$PWD/agents/my-agent" \
MODEL_MODE=endpoint MODEL_NAME=my-model \
MODEL_BASE_URL=https://model.example.com/v1 MODEL_KEY_ENV=EVAL_MODEL_KEY \
ARTIFACT_DIR="$PWD/eval-evidence-$CI_JOB_ID" VERIFY=true \
bash ../kaimahi/scripts/ci/live-eval-loop.sh
```

`ARTIFACT_DIR` must be a fresh per-run path outside `receipts/`. Configure your
CI to retain **only** its `*.json` files, even when the runner exits nonzero.
The runner builds KMX from the reviewed Kaimahi checkout; `KMX_BIN` can reuse a
matching binary already built by your job. A nonzero runner exit is a failed
eval or missing proof, not a reason to rerun a potentially side-effecting case.
For agents requiring Orka, provision the runtime, Provider, Secrets and tools
first and use the [staging-to-production sequence](#requiring-evaluation-before-lift).

## Running an existing Agent

```console
kmx agent run agents/my-agent --prompt "Summarize the release" [--to-context <ctx>] [--wait 5m]
kmx agent run --agent my-agent [--namespace orka-system] [--context <ctx>] --prompt-file prompt.txt
kmx task result <task-name> [--context <ctx>] [--namespace orka-system] [--wait 5m]
```

`agent run` executes one AI Task against a live Agent without creating or
updating the Agent, changing the bundle, or writing a receipt. Supply exactly
one of `--prompt` and `--prompt-file`; `--prompt-file -` reads stdin. Bundle
mode selects the explicit `--to-context` or the remembered target, checking
its `kube-system` UID before running. The Agent must be present, owned by the
bundle, and Ready for its current generation. Unlike evaluate, a deployment
that is behind or drifted may still run: kmx reports its state and deployed
commit first, so the answer is not mistaken for one from the current bundle.
`--agent` selects a live Agent directly, whether or not it has a bundle;
`--namespace` defaults to `orka-system`, and `--context` selects the cluster.

For `agent run`, bundle state and commit, result authority notice, Task name
and any recovery command go to **stderr**. `task result` reports the phase
there too. Only the answer goes to **stdout**, so redirecting
`> answer.txt` will not mix it with status. The Task name is written before
creation; creation is attempted once, never retried, and the Task is not
deleted. A run waits by default up to 5m (`--wait` accepts 10s–9m, below
the result token lifetime; `--wait 0s` is refused). On timeout it prints an
exact, context-pinned
`kmx task result <task-name> --context <ctx> --namespace <ns> --wait 5m` command.

`task result` checks that the named Task exists and is an AI Task. Without
`--wait` it reads the phase immediately through kubectl; it opens a fresh
result session only when a terminal answer is available. `--wait 5m` polls
for up to 5m (accepted range: 10s–9m), using a fresh result session for the
answer. The phase is printed only when it changes. A pending Task, or a
Succeeded Task whose answer is not yet available, exits **2** (not finished).
A Failed or Cancelled Task exits **1**; a readable successful answer exits
**0**. A timed-out `agent run` also exits **2**. Access or malformed-result
errors exit **1**, not **2**.
After a timeout, the Task may still be running: retrieve it by name rather
than re-running the prompt, which would create another Task. Result sessions
use the selected ServiceAccount's full effective authority and a temporary
loopback port-forward; do not grant it more access than needed.

## Retiring a bundle from a target

```console
kmx agent retire <bundle-dir> [--to-context <ctx>] [--to-namespace <ns>] [--plan] [--delete-adopted]
```

Retire uses the bundle's remembered destination or an explicit context, pins
kubectl to it, checks cluster identity against the remembered target and lift
receipts, and applies the remote-context confirmation guard before writing.
After the remembered target is cleared, it recovers a uniquely recorded
namespace from lift receipts; if several were used under one context, supply
`--to-namespace`.

`--plan` runs the same ownership and dependent inspection as execution and
reports each proposed deletion or release without changing resources, receipts
or remembered selection. The selected destination Provider, its Secret, Tools,
policies, namespace and Orka installation are not retired.

Only an Agent and its bundle-rendered Provider with this bundle's complete
ownership markers **and a matching local lift receipt for each live UID**
qualify. A same-named bundle without that receipt, or a foreign, unmarked,
incomplete or terminating object, is refused. A partial lift with no receipt
requires operator inspection; retire cannot prove those objects' provenance.
An owned object with `kaimahi.dev/origin: created` is deleted; one with
`origin: adopted` is **released** by removing kmx's ownership annotations,
leaving its spec and other metadata intact. That means four annotations on a
newly lifted object, or three on a legacy object without an origin annotation.
Objects lifted before origin was recorded are released, never assumed to have
been created. If the Agent survives a release, its rendered Provider is also
released even if marked `created`, so the Agent's dependency remains available.
`--delete-adopted` explicitly opts into deleting adopted and legacy owned
objects. The plan identifies each object's origin case. Origin is a live
Kubernetes annotation, not a tamper-proof provenance record: an operator with
permission to hand-edit it to `created` can cause retire to delete an object
that would otherwise have been released. Treat that as the same trust level as
kubectl write access to the object.

Before either action, kmx lists Tasks, GatewayBindings, RepositoryScans,
RepositoryMonitors and other Agents across namespaces. Pending, Scheduled,
Running and Finalizing Tasks (including Tasks with no reported phase), any
GatewayBinding or repository resource referencing the Agent, and another
Agent's `coordination.allowedAgents` block retirement. When the Provider would
be deleted, other Agents using it as their primary or fallback Provider, and
active Tasks referring directly to it, also block. Execution repeats the
inventory after remote-context confirmation, before mutation. If an inventory
cannot be completed, retire names the resource and makes no changes. It names
the required cluster-wide `list` permission only on an actual authorization
refusal; size and timeout failures are reported separately. Orka v0.2.0's
Agent deletion handler deletes the Agent only; Task deletion and its associated
result/event cleanup occur on the Task deletion path, not on Agent deletion.
Task records and history therefore remain, although an in-flight Task may fail
if its Agent is removed. See Orka v0.2.0
[`internal/api/handlers.go`](https://github.com/orka-agents/orka/blob/v0.2.0/internal/api/handlers.go)
and [`internal/controller/task_controller.go`](https://github.com/orka-agents/orka/blob/v0.2.0/internal/controller/task_controller.go).

Retire records UID-bound decisions in `receipts/retire-<target>.json` before
mutation, marks it complete only after both resources have been retired, retains
the lift receipt as history, and forgets the remembered target once complete.
Status shows `not deployed`; after a release it also notes that an unmanaged
Agent of the same name remains and a later lift would adopt it if its rendered
fields still match. A repeat retire of the same objects is a no-op. A failure
after the first mutation can leave a partial retirement: inspect the target,
resolve the blocker, then rerun retire to complete it. The console has
no retire action; adding one is a separate follow-up.
## Checking deployed status

```console
kmx agent status <bundle-dir> [--to-context <ctx>] [-o table|json]
```

Status reads `agent.yaml`, the local Git history, receipts and the remembered
selection, then inspects the live Provider and Agent per target. The portable
digest covers the exact file bytes; a clean desired revision must be tracked and
unchanged against `HEAD`. A live digest is matched against at most the last 200
commits touching `agent.yaml`; a missing match is reported rather than guessed.
Readiness means Ready on the current generation and is separate from revision
or field drift. A rendered-field difference names field paths, never values.

Each target reports one state: `in sync`, `behind`, `drifted`, `not deployed`,
`belongs to another bundle`, `target changed`, or `unknown` with a reason.
Status checks a recorded target's `kube-system` UID before reading its objects;
if the context points at a different cluster, it reads no Provider or Agent.
An explicitly selected context without a receipt may be inspected: status
shows its cluster UID and `no receipt for this target`. Receipts are local
history, not a prerequisite for a read. Target failures and non-ready agents
are reported in the output rather than as a nonzero exit code. Status writes
nothing, including cluster objects, receipts, and remembered selection.
Field comparison uses the same server-side dry-run admission as lift's
`--plan`: it does not persist resources, but requires permission to dry-run
create/replace. If that permission is unavailable, the comparison is
`unknown`, not proof of an unchanged resource.

Each target also reports its evaluation (`EVAL` in the table, `evaluation` in
JSON): the recorded `pass`, `fail` or `unknown` result when an evaluation
receipt exists for that cluster, the live Agent's UID, the bundle's current
portable digest and the current `eval/` case set; otherwise `none`. A target
that is behind, or whose cases changed since the last evaluation, shows
`none`. Status also shows `GATE` (`gate` in JSON) for each target: `pass`,
`not required`, `refused: <condition>`, or `unknown` when the destination
cluster identity cannot be observed. Gate status checks the local policy and
receipts; it does not contact the evaluation cluster. Status never runs a case.

An identical render whose portable or rendered digest marker is older has its
ownership markers refreshed by lift with a resourceVersion precondition; lift
reports the resource as reused. This metadata-only update does not require a
new generation to become Ready. `--plan` predicts the same marker refresh.
