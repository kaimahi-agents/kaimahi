# Inspect a destination

`kmx status --to NAME` inventories deployed Orka and kagent Agents throughout one
named destination namespace. It does not select this workspace's suite, evaluate
an agent, repair resources, or report controller installation qualification.

```bash
kmx status --to prod
kmx status --to prod --workspace ./operator-workspace -o json
```

Use `kmx orka status` for the existing table-only Orka runtime report. Bare
`kmx status` now refuses before application configuration and names both routes.
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
content, passing receipts, or local Lift history. An operator can retain only the
descriptor and destination configuration on another machine; the member paths need
not exist for status. `--workspace` selects an exact root without fallback;
otherwise the nearest ancestor workspace is selected.

A saved namespace is required: status never derives it from bindings or the
kubeconfig's mutable namespace. An explicit global `--context` overrides the saved
context for this invocation; otherwise the destination context wins over ambient
or remembered selections. Concrete selectors are frozen before observation.
AgentSessions destinations refuse before cluster inspection.

Both supported Agent APIs are inspected when served: Orka
`core.orka.ai/v1alpha1` and kagent `kagent.dev/v1alpha2`. An optional saved runtime
is an expectation, **not a filter**: an unavailable expected API or any observed
Agent of a different runtime makes the report incomplete without hiding readable
rows. An API being served does not qualify its controller, images or authentication.

The caller needs GET access to API discovery and the `kube-system` and selected
Namespace identities, plus GET/list access to Agents and their namespace-local
prerequisites. Candidate-root inventory also lists served Orka Providers/Tasks or
kagent ModelConfigs. Kagent readiness additionally reads Deployments and Services,
and lists/reads ReplicaSets and Pods in that namespace. Status grants no permissions.
It reads no Secret contents or metadata, Nodes, controller proxies or auth probes.

## Provenance is per object

| Reported state | Meaning |
|---|---|
| `recorded` | A valid, applicable unsigned create-time Lift observation is present. |
| `provenance not recorded` | Native suite ownership is valid, but this object has no Lift annotation. This is normal for older objects or no-write reuse; it does not prove the object's age. |
| `unknown` | Ownership or provenance is incomplete, malformed, conflicting, unsupported, changed during inspection, or cannot be observed safely. |
| `not kmx-owned` | No native suite or legacy reservation markers are present. A copied Lift annotation alone cannot change this classification. |

The table shows agent/member, runtime, shortened OCI suite and consumed-receipt
digests, evaluation environment and actual gate, UTC **attempt** time, optional
actor, provenance and readiness. JSON retains full digests and identities. The
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

## GitOps and spec drift

Follow the [one-writer ownership boundary](suite-first-lifecycle.md#gitops-ownership)
when using GitOps alongside KMX. Status compares current specs only against each
object's applicable recorded expected spec digest, not against Git's desired
state. It neither fetches that desired state nor repairs GitOps changes.

- With a preserved, valid and applicable Lift annotation, a changed Agent or
  Provider/ModelConfig spec digest produces per-resource `spec=drifted`, drifted
  readiness and exit **1**.
- Status/resourceVersion-only changes and unrelated metadata edits are not spec
  drift. Status is not a blanket metadata drift detector: changes to suite
  ownership or provenance markers can instead make ownership/provenance `unknown`.
- Removing the Lift annotation while retaining valid suite ownership yields
  `provenance not recorded` and `spec=not recorded`: there is no recorded spec
  baseline, not a claimed match. Removing ownership markers changes classification;
  no native or legacy reservation markers means `not kmx-owned`, even with a
  copied Lift annotation. Incomplete, malformed or conflicting markers can yield
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
| `0` | Complete inventory; deployed Agents ready, or verified empty. |
| `2` | Complete observation with known pending deployed placement. |
| `1` | Incomplete/unknown, unhealthy or drifted placement, runtime mismatch, or output failure. This takes precedence over pending. |

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
