# Orka quickstart Kubernetes tool

The quickstart wizard installs `k8s-get-resources`, an Orka HTTP Tool that ports
the read-only listing capability of the older `hello-tools` example.
The name uses hyphens because it is a Kubernetes Tool resource name.

New quickstart agents reference this tool by default. `--tools` replaces that
default with an explicit list. Default system instructions direct cluster
questions to the tool; custom instruction files remain user-owned.

The earlier Orka v0.1.3 run refused private Service IPs as direct Tool
authorities. The current pinned installer is v0.2.0; KMX gives
the Tool the credential-free logical authority `https://1.1.1.1/resources`
and binds it to an exact same-namespace `OutboundAccessPolicy`; Orka sends the
request only to the `kmx-k8s-tool` Service on port 8080. The public IP avoids
v0.2.0's bounded DNS-answer check on the logical authority; no Tool data is
sent to that public IP. The v0.2.0 chart worker lacks read access to the
policy by default. kmx reads the installed Orka controller's configured AI
worker ServiceAccount and checks the release-namespace account's matching
Helm and AI-worker labels before granting it `get` on **only this named
policy** in `orka-system`.
This works for both kmx's `orka-api-ai-worker` and a stock Helm release's
`orka-ai-worker`; kmx refuses a missing, ambiguous or unverified worker rather
than binding a guessed account. Installation waits for the current policy
generation to be Accepted, verifies the worker's effective named-policy `get`
authorization, then waits for the current Tool generation to be Available.
A missing or invalid policy leaves the Tool unavailable rather than falling
back to the public logical URL.

Selecting an existing quickstart Agent adds the tool reference if absent,
preserving other tools and its system prompt. An explicitly disabled reference
is preserved. The update checks resourceVersion and waits for the updated Agent
generation to become Ready. Subsequent Tasks use the new tool; active workers
keep their startup configuration. This cluster update does not rewrite older
local Agent YAML files.

Try:

```text
List deployments in namespace orka-system using k8s-get-resources.
```

In Orka interactive chat, `/tools` opens a searchable list of registered tools
and existing references. The screen clears before loading. Use arrows or j/k to
select, press `/` to search, Space to toggle,
and Enter to save. Escape returns without changes; Ctrl-C exits chat. The four
automatic memory tools observed on v0.1.3 were shown as locked on because that
worker injected them independently of the Agent references; do not assume
this historical observation describes every v0.2.0 task mode.

`/agent` opens a searchable list of AI agents in the current namespace. Enter
connects to the selection, clears the visible conversation and resets `/retry`.
Tool updates take effect on the next Task and preserve other Agent configuration.

## Implementation

- `k8s/orka-k8s-tool.yaml`: Tool, exact same-namespace
  `OutboundAccessPolicy` gateway, Service, Deployment and read-only RBAC. Orka
  rejects private Service IPs as direct Tool authorities; the Tool therefore
  uses a credential-free public logical authority while Orka routes execution
  only to the named `kmx-k8s-tool` Service. The scoped Role/RoleBinding grants
  the verified live chart AI worker access to only the referenced policy.
- `scripts/orka-k8s-tool.py`: standard-library HTTP server, embedded into KMX and
  installed in the `kmx-k8s-tool` ConfigMap.
- `internal/kmx/app/orka_k8s_tool.go`: installation and existing-agent attachment.

The server permits only a fixed resource allowlist and optional namespace. It
issues Kubernetes GET requests using its own service account, verifies the API
server certificate, and returns names/namespaces plus selected status fields.
It never invokes a shell, returns Secret data, or exposes ConfigMap contents and
pod environment values. Lists are limited to 100 items and report truncation.

The status fields are chosen to answer "why is this unhealthy?":

| Resource | Added health fields |
|---|---|
| pods | `reason` (e.g. `Evicted`); `conditions` of type `Ready`, plus `PodScheduled` when it is not `True` (e.g. `Unschedulable`); `containers` (init containers marked `init: true`) with `ready`, `restartCount`, and the `waiting`, `terminated` and `lastTerminated` reasons, e.g. `ImagePullBackOff`, `CrashLoopBackOff`, `OOMKilled` |
| deployments, statefulsets | `readyReplicas`, `updatedReplicas`, `availableReplicas` |
| replicasets | `readyReplicas`, `availableReplicas` (a ReplicaSet has no updated count) |
| daemonsets | `desiredNumberScheduled`, `numberReady`, `updatedNumberScheduled`, `numberAvailable`, `numberUnavailable` (a DaemonSet has no `*Replicas` fields) |
| all four workload kinds and jobs | `conditions` as `type`, `status` and `reason`, e.g. `Available=False MinimumReplicasUnavailable`, `Progressing=False ProgressDeadlineExceeded`, `Failed=True BackoffLimitExceeded` |

Kubernetes omits a zero count; the tool states it as `0`, so a Deployment with
no ready pod reads `readyReplicas: 0` rather than lacking the field. The earlier
fields (`phase`, `readyReplicas`, `replicas`, `succeeded`, `failed`) are kept.

No `message` field is returned from any condition or container state, because
messages can echo arbitrary text. Nor are images, image IDs, container IDs, exit
codes, env, annotations or spec contents. A reason is returned only when it has
the machine-word shape Kubernetes validates for condition reasons (a letter,
then letters, digits, `_`, `,` or `:`, at most 128 characters); any other value is
dropped instead of relayed. Condition types follow the same rule, and a status
must be `True`, `False` or `Unknown`.

The Service is cluster-internal and has no application-level authentication;
its endpoint exposes only these read-only projections. It does not provide
Kaimahi tool authorization or tool auditing. The worker's own permissions are
separate from the dedicated reader service account's read-only permissions.

Supported resources: pods, services, namespaces, nodes, configmaps,
persistentvolumeclaims, deployments, statefulsets, daemonsets, replicasets,
jobs and cronjobs. Omitting namespace lists across namespaces.
For pods, the optional `phase` parameter filters at the Kubernetes API; use
`Running` to omit completed worker Jobs when listing running pods.

## Repair a missing policy-reader grant

A Tool can remain `Available` after its policy-reader RoleBinding is removed:
Tool admission is not an authorization check on the AI worker. Before lifting
an Agent that references an HTTP Tool with an `outboundAccessPolicyRef`, both
bundle `--plan` / deploy and bundle-less live-copy `/lift` check the worker's
**effective** `get` on that specific policy in the Tool's namespace, using
`system:serviceaccount:<Agent namespace>:<configured worker name>`. The
release-namespace account identifies the worker name; Orka's Task-namespace
worker account is the identity that needs permission. A refusal
names the Tool, worker account, namespace, policy and verb. Neither denied nor
indeterminate authorization permits broadening the Role or bypassing the check.
For a **non-allowing review** (explicit denial or NoOpinion) on the quickstart
Tool and its built-in policy in `orka-system`, the console offers a separately
confirmed **Prepare target** action to reapply the Tool through kmx, rediscover
the current chart account,
and reconcile the single named-policy RoleBinding. API failures, indeterminate
reviews and custom policies do not offer this repair; investigate them first.
On an unknown/foreign Orka chart or an unreadable controller/account, verify
the installation and chart version first;
kmx will not infer a worker name. Then retry `kmx agent lift --plan` before
deploying. Do not grant general policy-list access.

## Verification

```sh
python3 -B scripts/test_orka_k8s_tool.py
go test ./internal/kmx/app -run 'TestQuickstart(K8sTool|ToolDefault)'
```

CI checks both kmx-owned and stock Helm v0.2.0 releases. Its opt-in kind-only
Go entrypoint invokes the same installer as console Prepare after confirming
that its context exists and points to a local loopback kind API server. CI
asserts the exact RoleBinding subject and named-policy authorization, then runs
a deterministic health question through the Tool. CI checks the Task phase;
the scoped Task-result session projects **only** `ToolCallStarted` and
`ToolCallCompleted` metadata with top-level `toolName=k8s-get-resources`;
CI does not print the Task answer, event bodies, model output or worker logs.
The kmx-owned shard also checks the HTTP allowlist, reader RBAC and
missing-gateway refusal.
