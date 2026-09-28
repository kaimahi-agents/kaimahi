# `kmx agent lift`

`kmx agent lift` deploys an existing on-disk agent bundle to a prepared Orka
cluster. It does not read or capture a live source Agent. Unlike interactive
[`/lift`](interactive-lift.md), it takes a named destination and inference
Provider rather than discovering either through a chat session. `/lift` remains
an independent interactive path; this command does not change it.

```console
kmx agent lift <bundle-dir> --to-context <ctx> [--to-namespace <ns>] --inference provider:<name> [--plan]
```

The bundle contains `agent.yaml` (the portable definition) and
`bindings.yaml` (its creation-target bindings). Lift reads the definition from
`agent.yaml`, resolves **new** target bindings, and renders with
`RenderOrkaBundleFile`. It does not copy a Secret or take inference from the
bundle's original bindings. The exact bytes of `agent.yaml`, including comments
and whitespace, determine the portable digest. A rendered digest identifies
the target-specific rendering.

## Destination and inference

On first use for a bundle, name a destination with `--to-context` and inference
with `--inference provider:<name>`. A successful lift remembers those choices
locally for that bundle, not in Git. A later lift may omit them. Reusing a
remembered target requires both its context name and the UID of its
`kube-system` namespace to match the recorded identity: a missing, ambiguous,
or repointed context is refused rather than falling back to kubeconfig's
current context. Confirm the resolved context, cluster and namespace displayed
before a write. The existing remote-context guard still applies to writes.

`provider:<name>` identifies an **existing Ready Provider** in the destination
namespace. Lift reads that Provider's type, endpoint and Secret reference and
uses them as the render bindings. The bundle renders its own Provider named
after the Agent; the selected destination Provider is never modified. No
Foundry provisioning or "keep the source configuration" mode is available.
The referenced Secret must already exist in the destination namespace; its
value is neither copied into the bundle nor printed.

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
or Task. It does not change or delete the source Agent.

## Plan, reconciliation and outcomes

`--plan` resolves the bundle, target and inference, checks prerequisites, and
inspects the same rendered Provider and Agent that execution would deploy. It
uses **the same read-only inspection and classification code as reconcile
Deploy**, including server dry-run comparison, rather than separate lift rules.
It writes no cluster resources, receipt or remembered target. If a comparison
cannot be established without a write, its result is **unknown**, not a claim
that the resource would be reused.

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
`receipts/` does not affect Git cleanliness of `agent.yaml`. A later
`kmx agent create` rerun can reuse the bundle even when `receipts/` is present.
No receipt is written by `--plan` or by a failed deployment. If deployment and receipt writing succeed but saving the remembered target fails, the command returns an error and the receipt remains on disk.

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
the case files that ran, the Git provenance, the target and live Agent UID,
and per case the id, verdict, matched and missing expectations, Task name and
UID, and a SHA-256 of the answer. **It never contains answer text**, so it can
be committed to a public repository. `--case` runs one case; its receipt covers
only that case's file, so status does not count it as the bundle's case set.

Evaluate is a gate: it exits non-zero unless every case passed, including when
any case is `unknown`.

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
`none`. Status never runs a case.

An identical render whose portable or rendered digest marker is older has its
ownership markers refreshed by lift with a resourceVersion precondition; lift
reports the resource as reused. This metadata-only update does not require a
new generation to become Ready. `--plan` predicts the same marker refresh.
