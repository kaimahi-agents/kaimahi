# `kmx agent lift`

`kmx agent lift` deploys an existing on-disk agent bundle to a prepared Orka
cluster. It takes a named destination and inference Provider. Interactive
`/lift` in chat or `kmx console` selects those inputs and shows the same
read-only plan before invoking this operation. A separately confirmed
**Prepare target** action can install Orka or the quickstart Kubernetes Tool
before retrying the checks; bundle lift itself never installs them.

```console
kmx agent lift <bundle-dir> --to-context <ctx> [--to-namespace <ns>] --inference provider:<name> [--require-evaluated <context>] [--override-gate "reason"] [--plan]
```

The bundle contains `agent.yaml` (the portable definition) and
`bindings.yaml` (its creation-target bindings). Lift reads the definition from
`agent.yaml`, resolves **new** target bindings, and renders with
`RenderOrkaBundleFile`. It does not copy a Secret or take inference from the
bundle's original bindings. The exact bytes of `agent.yaml`, including comments
and whitespace, determine the portable digest. A rendered digest identifies
the target-specific rendering.

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
- Every Tool referenced by the bundle is Available in the destination. Prepare
  Tools separately before lifting.

Each missing prerequisite is reported separately with the relevant preparation
command where one exists. The quickstart Kubernetes inventory Tool requires its
same-namespace `OutboundAccessPolicy` to be Accepted before the Tool becomes
Available; a target missing or rejecting that policy is refused until repaired.
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
`kaimahi.dev/portable-digest` and `kaimahi.dev/rendered-digest`. These are
applied outside the immutable rendered documents and do not change their
digest. Reconciliation refuses Tasks. A partial failure may leave resources
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
