# Inspect a destination

`kmx package status --to NAME` inventories deployed Orka, kagent and AgentSessions Agents throughout one
named destination namespace. It does not select this workspace's suite, evaluate
an agent, repair resources, or report controller installation qualification.

```bash
kmx package status --to prod
kmx package status --to prod --workspace ./operator-workspace -o json
```

Use `kmx cluster orka status` for the existing table-only Orka runtime report. Bare
`kmx package status` now refuses before application configuration and names both routes.
There is no new `suite status` or legacy bundle-status command.

## Selection and access

Define the destination in workspace-root `kmx.yaml`:

```yaml
version: 1
targets:
  prod:
    context: production
    namespace: agents
    runtime: orka
```

The workspace resolver reads a valid `workspace.yaml` descriptor and `kmx.yaml`
once. It does not load member files, build profiles, bindings, inference, registry
content, passing receipts, or local Deploy history. An operator can retain only the
descriptor and destination configuration on another machine; the member paths need
not exist for status. `--workspace` selects an exact root without fallback;
otherwise the nearest ancestor workspace is selected.

A saved namespace is required: status never derives it from bindings or the
kubeconfig's mutable namespace. An explicit global `--context` overrides the saved
context for this invocation; otherwise the destination context wins over ambient
or remembered selections. Concrete selectors are frozen before observation.

All supported Agent APIs are inspected when served: Orka
`core.orka.ai/v1alpha1`, kagent `kagent.dev/v1alpha2` and AgentSessions
`agents.aks-durable-agents.io/v1alpha1`. An optional saved runtime
is an expectation, **not a filter**: an unavailable expected API or any observed
Agent of a different runtime makes the report incomplete without hiding readable
rows. An API being served does not qualify its controller, images or authentication.

The caller needs GET access to API discovery and the `kube-system` and selected
Namespace identities, plus GET/list access to Agents and their namespace-local
prerequisites. Candidate-root inventory also lists served Orka Providers/Tasks or
kagent ModelConfigs. Kagent readiness additionally reads Deployments and Services,
and lists/reads ReplicaSets and Pods in that namespace. Owned deployed Orka rows
also GET only their exact hashed `kmx-release-…` ConfigMap; ConfigMap listing is not
required. An unreadable record is unknown, never proof of absence or completion.
Status grants no permissions.
It reads no Secret contents or metadata, Nodes, controller proxies or auth probes.

## Provenance is per object

| Reported state | Meaning |
|---|---|
| `recorded` | A valid, applicable unsigned Deploy attempt observation is present (v1 create or Orka v2 mutation). |
| `provenance not recorded` | For Orka/kagent, native suite ownership is valid but this object has no Deploy annotation. For AgentSessions, the contract carries no kmx ownership/evaluation proof. This is normal for older objects or no-write reuse; it does not prove the object's age. |
| `unknown` | Ownership or provenance is incomplete, malformed, conflicting, unsupported, changed during inspection, or cannot be observed safely. |
| `not kmx-owned` | No native suite or legacy reservation markers are present. A copied Deploy annotation alone cannot change this classification. |

The table shows agent/member, runtime, shortened OCI suite and consumed-receipt
digests, evaluation environment and actual gate, UTC **attempt** time, optional
actor, provenance, release state, Deploy attempt version/ID and readiness. JSON
retains full digests and identities. The
per-object resource rows keep Agent and Provider/ModelConfig observations separate:
an unstamped reused prerequisite never inherits its Agent's annotation.

Promotion explicitly says **not evaluated on this target**. `same-subject` describes
a previous pass in the recorded environment, not fresh evaluation of the newly
created objects. Missing actor remains JSON `null` / table `not recorded`; status
never guesses an actor, receipt, evaluation alias, attempt time or promotion gate.

Annotations are observations, not authentication, ownership authority or receipts.
Copied annotations do not prove original resource-UID continuity. Status does not
open private history or claim that continuity, even when local records exist.
Context aliases are access names; target comparison uses cluster/namespace UIDs
and the namespace, not matching kubeconfig aliases across machines.

## AgentSessions registration

AgentSessions rows read only the namespaced `Agent`, with no host, model binding,
Secret, workload or Sessions API reads. They report `registered` only when:

- The object is not deleting and `status.agentUID` equals its current UID.
- Status and both `PackageVerified=True` and `Registered=True` conditions acknowledge
  the current metadata generation.
- Status member equals the immutable spec member, and `servedDigest` and
  `packageDigest` both equal the digest in `spec.package.reference`.

Registered means **last controller-observed admission**, not current host or
inference availability, target evaluation or a qualified runtime. Pending checks
remain `pending`; malformed data or identity/spec changes across reads are unknown.
The package digest/member come from the resource, not local history. Provenance is
`provenance not recorded`: the controller contract does not carry kmx evaluation or
ownership proof. Orka/kagent provenance and candidate handling remain unchanged.

## Shared release observation

Owned deployed Orka rows optionally include `releaseRecord` (`name`, `uid` when
observed, `state`). The bounded exact-name GET validates the shared schema and
cluster/namespace identity, without opening private KMX history or creating state:

- `pending`: a shared operation still holds the pair reservation (exit 2 if the
  rest of the observation is complete and no higher-priority error applies).
- `partial`: the latest shared terminal revision is partial (exit 1).
- `completed`: the shared terminal revision, baseline, both current full pair
  fingerprints and applicable attempt annotations consistently match.
- `unknown`: absent v2 record, unreadable/invalid data, mixed revisions, changed
  fingerprints or conflicting observations (exit 1). Absent v1 legacy records
  remain ordinary per-object observations, never enrolled placements.

These are **unsigned shared-cluster observations**, not proof that the original
writer received the final API response or persisted its private acknowledgment.
Even `completed` cannot authorize an update, clear a reservation or reconstruct
lost private ownership. The updater separately requires its retained private proof.
A v2 annotation alone is an attempt, not successful pair completion; individual
resources may show different attempts after a partial mutation. Current readiness
and the currently observed Agent digest stay distinct from shared operation state.
The v0.8.1 reader does not understand v2 and reports unknown rather than attributing
an old create observation to the new revision. Future unsupported versions are
also unknown.

## GitOps and spec drift

Follow the [one-writer ownership boundary](suite-first-lifecycle.md#gitops-ownership)
when using GitOps alongside KMX. Status compares current specs only against each
object's applicable recorded expected spec digest, not against Git's desired
state. It neither fetches that desired state nor repairs GitOps changes.

- With a preserved, valid and applicable Deploy annotation, a changed Agent or
  Provider/ModelConfig spec digest produces per-resource `spec=drifted`, drifted
  readiness and exit **1**.
- Status/resourceVersion-only changes and unrelated metadata edits are not spec
  drift. Status is not a blanket metadata drift detector: changes to suite
  ownership or provenance markers can instead make ownership/provenance `unknown`.
- Removing the Deploy annotation while retaining valid suite ownership yields
  `provenance not recorded` and `spec=not recorded`: there is no recorded spec
  baseline, not a claimed match. Removing ownership markers changes classification;
  no native or legacy reservation markers means `not kmx-owned`, even with a
  copied Deploy annotation. Incomplete, malformed or conflicting markers can yield
  `unknown`.

Identity/spec/ownership changes across reads within an inspection are reported as
unknown rather than retried or repaired. Copied annotations do not authenticate
original UID continuity, even if the current spec matches. Status does not
re-evaluate an agent, check model credentials or certify model behavior.

## Readiness and incomplete results

Ready means current placement readiness, **not answered, evaluated, authenticated
or qualified runtime images**. Orka checks current-generation Agent and referenced
Provider readiness. Kagent checks current-generation Agent/ModelConfig acceptance
and Agent `DeploymentReady`, then the Deployment/Service/ReplicaSet/Pod owner-UID
chain and current rollout. Labels alone cannot establish workload ownership.

Evaluation-run annotation presence segregates candidates, including malformed
reservations. Candidate Provider/Agent/Task or ModelConfig roots are counted and
shown separately, including orphans without an Agent. Their progress does not
change deployed-Agent readiness; uncertain candidate inventory still makes the
whole inventory incomplete. It is not a candidate cleanup or evaluation-pass report.

Pagination retains readable earlier rows if later reads fail. Permission denial,
transport failure, malformed discovery, changed collection snapshots and exhausted
budgets are **unknown**, never successful empty inventory. Valid discovery and
complete listing are needed to prove absence. Each collection is bounded to four
128-item, 4 MiB pages; the whole observation has a 90-second, 2,048-GET and 64 MiB
accepted-response budget. No polling or automatic retry is performed.

| Exit | Meaning |
|---|---|
| `0` | Complete inventory; deployed Agents ready/registered, or verified empty. |
| `2` | Complete observation with known pending deployed placement. |
| `1` | Incomplete/unknown, partial shared revision, unhealthy or drifted placement, runtime mismatch, or output failure. This takes precedence over pending. |

stdout contains only the table or one unstyled versioned JSON document. Resource
reasons are fixed diagnostics, never raw specs, annotation bodies, controller
messages, receipt payloads, endpoints or subprocess errors. Fields that cannot be
proved are not populated from another object or local state. JSON includes
`version: 1`, `destination`, `target`, `complete`, `runtimes`, `agents`, separate
`candidates`, `errors` and an observation caveat. Each Agent includes per-resource
provenance/readiness/spec comparison and separate owned-workload observations.
Malformed annotations have a null record; a readable but conflicting valid record
can remain visible with state `unknown` and a reason, not as an applicable claim.

KMX makes only explicitly targeted Kubernetes GETs and local configuration reads.
It creates no KMX state, caches, receipts, locks or markers and never installs tools,
asks for mutation consent, starts forwards, sends Tasks/model calls, backfills or
repairs annotations. As with other kubectl access, authentication is supplied by
the operator's kubeconfig and its configured credential provider.
