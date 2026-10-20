# Guidance for Azure

## Development registry with Bicep

[`acr.bicep`](acr.bicep) creates a Basic-tier Azure Container Registry in an
**existing, explicitly selected resource group**. Use the same operator-owned
development resource group as AKS, not another group just for the registry.
The template enables the public HTTPS registry endpoint with Entra
authentication, disables admin credentials, and creates no role assignments,
passwords, AKS resources or model endpoint. ACR adds ongoing cost.

Obtain approval for the exact subscription, group, region and globally unique
registry name. Require successful reads to prove the name is absent; an
unreadable target is not absent. Do not deploy over a registry you cannot prove
you own. Review what-if, then separately approve the incremental deployment:

```bash
az deployment group what-if --subscription "$SUBSCRIPTION" \
  --resource-group "$RESOURCE_GROUP" \
  --template-file docs/azure/acr.bicep \
  --parameters registryName="$REGISTRY" location="$LOCATION"
az deployment group create --subscription "$SUBSCRIPTION" \
  --resource-group "$RESOURCE_GROUP" --name kaimahi-dev-acr \
  --mode Incremental --template-file docs/azure/acr.bicep \
  --parameters registryName="$REGISTRY" location="$LOCATION"
```

Set all uppercase selectors explicitly before running these examples. Retain
the deployment result and registry ID privately. Authenticate publication using
the operator's existing Entra identity and a private Docker credential store;
never enable admin credentials or put access tokens in argv, templates or logs.
The template grants neither the operator nor AKS registry permissions.

For a disposable environment, deleting the development group deletes its ACR
and all stored suites along with AKS; require explicit data-loss approval.
AKS also creates a separate Azure-managed node resource group. Do not put ACR
there or assume its contents are individually operator-owned. Group deletion
does not preserve evaluation records, volumes or Orka snapshot keys automatically.

## Single-node CPU inference test

Use [`aikit-cpu.yaml`](aikit-cpu.yaml) on an **existing** Linux/amd64 AKS dev
cluster to serve **`qwen-3.5-2b`**, matching the authored test agent. It is a
checkout-only, operator-owned template, not an Ollama tag or a KMX installer.
Cluster creation, Orka installation and its Provider are separate operations.
See [AKS](../aks.md), [Orka](../orka.md) and [models](../models.md).

### Capacity and boundaries

- One replica, digest-pinned Qwen 3.5 2B Q4 AIKit image, CPU backend, three threads.
- Requests: 1 CPU, 3 GiB memory, 1 GiB ephemeral storage. Limits: 3 CPUs, 4 GiB
  memory, 4 GiB ephemeral storage. No GPU, PVC or additional model pool.
- The 4-vCPU / 16-GiB single-node baseline should leave room for system/runtime
  pods; check **actual allocatable capacity and existing requests**, not VM RAM.
  Test one agent execution at a time. No latency, concurrency or availability
  guarantee is implied.
- `Recreate` avoids a second model replica during rollout; updates have downtime.
  Startup has a ten-minute probe budget. Readiness is not answer proof; no
  liveness restart loop is added for slow CPU inference.
- The preset uses a 4096-token context and 64-token default output budget for
  smoke tests, not general agent quality. The image/configuration come from the
  pinned CI fixture. The smoke check recorded below does not qualify this
  three-CPU dev preset for agent evaluation or production use.
- The ClusterIP endpoint is **not authenticated or network-isolated**. Do not
  expose it with public ingress/load balancing or a non-loopback forward.
  Image/model storage is node-local; node replacement may require image pulls.

### 1. Inspect, obtain consent and create

Use Bash/WSL from the repository root, with a compatible `kubectl` and the
intended kubeconfig. Obtain operator approval for the exact context, dedicated
namespace and workload resources. No command below chooses a subscription or
creates cloud infrastructure.

Stop on unreadable state, occupied names, failed admission or partial writes.
Do not adopt resources, weaken requests, enlarge the node or retry an ambiguous
create to make the walkthrough pass. For another namespace, review private
copies and change **all** namespace references, commands and the Provider URL.

```bash
set -euo pipefail
umask 077
CTX='<approved-existing-context>'
STATE='<new-private-directory-outside-the-checkout>'
mkdir "$STATE"
kubectl --context "$CTX" get nodes
kubectl --context "$CTX" describe nodes
EXISTING_NAMESPACE="$(kubectl --context "$CTX" get namespace kmx-inference --ignore-not-found -o name)"
test -z "$EXISTING_NAMESPACE"
kubectl --context "$CTX" create --dry-run=server -f docs/azure/aikit-cpu-namespace.yaml
```

After approving that Namespace, create it and retain its acknowledged identity.
Only then dry-run the namespaced objects: a dry-run Namespace is not persisted.

```bash
kubectl --context "$CTX" create -f docs/azure/aikit-cpu-namespace.yaml \
  -o json > "$STATE/namespace.json"
kubectl --context "$CTX" create --dry-run=server -f docs/azure/aikit-cpu.yaml
```

Review the objects, then separately authorize their creation:

```bash
kubectl --context "$CTX" create -f docs/azure/aikit-cpu.yaml \
  -o json > "$STATE/model-resources.json"
kubectl --context "$CTX" -n kmx-inference rollout status deployment/aikit-cpu --timeout=15m
kubectl --context "$CTX" -n kmx-inference get pods
```

Retain acknowledged UIDs even if only some objects were created. A failed
create/timeout can leave resources; inspect the exact target instead of repeating
the multi-object command. Inspect scheduling events for Pending pods, model logs
for startup failures and memory pressure/OOM for restarts. Logs may contain
payloads; keep diagnostics private.

### Restricted API access: AKS Run Command

If direct `kubectl` connections fail, do not broaden the API allowlist or treat
the failure as absence. An authorized operator can use AKS Run Command, which
creates a temporary command pod and uses management-plane permissions. Pin the
subscription, resource group and cluster on **every** invocation:

```bash
SUBSCRIPTION='<approved-subscription-id>'
RESOURCE_GROUP='<approved-resource-group>'
CLUSTER='<approved-cluster-name>'
az aks command invoke --subscription "$SUBSCRIPTION" \
  --resource-group "$RESOURCE_GROUP" --name "$CLUSTER" \
  --command 'kubectl get nodes' -o json > "$STATE/access-check.json"
```

The CLI succeeding is insufficient: require the returned `exitCode` to be zero,
and inspect its `logs` privately. Run the same absence checks, staged dry-runs
and creates above through this path; retain each returned create result and
acknowledged UID. Attach reviewed manifests with `--file`; their **basenames**
are available in the command pod. Attach an operator-reviewed, **LF-terminated**
shell script and invoke `sh script.sh` for multi-step commands. On Windows this
also avoids `az.cmd` misinterpreting nested shell quotes. Inspect state after
any failure; do not repeat a possibly partial create.

For the answer check, the command pod can call the internal Service directly
with `curl`; no port-forward is needed. Attach a request JSON file using the
body below, send it once with `--fail --max-time 180`, and redirect the complete
Run Command JSON response to a private local file. Validate its `exitCode` and
parse `logs` as the answer JSON with local Python; do not assume Python exists
inside the command pod. Apply the same exact-model/completion/content checks
below and delete successful response payloads. Run Command output can contain
the answer, so never print it or use it as a public setup receipt.

### 2. Prove one answer

In a second shell with the same explicit context/kubeconfig, start a tracked
forward on a free loopback port:

```bash
kubectl --context "$CTX" -n kmx-inference port-forward --address 127.0.0.1 \
  service/aikit-cpu 18091:8080
```

Back in the first shell, send one request with no retry. Require HTTP 200, valid
JSON, the exact model, a nonempty assistant answer and successful completion.
The response contains model output; do not print or share it as setup evidence.

```bash
HTTP_CODE="$(curl --silent --show-error --max-time 180 \
  -H 'Content-Type: application/json' \
  --data '{"model":"qwen-3.5-2b","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"Reply with OK."}]}' \
  --output "$STATE/model-response.json" --write-out '%{http_code}' \
  http://127.0.0.1:18091/v1/chat/completions)"
test "$HTTP_CODE" = 200
python3 - "$STATE/model-response.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    response = json.load(source)
assert response["model"] == "qwen-3.5-2b"
choice = response["choices"][0]
assert choice["finish_reason"] == "stop"
assert choice["message"]["role"] == "assistant"
assert choice["message"]["content"].strip()
print("Direct CPU inference: passed")
PY
rm "$STATE/model-response.json"
```

Stop the specific forward process on success **or failure**. A failed/truncated
answer is not proof of inference success and does not authorize a model switch.
Retain failed payloads privately only if needed for diagnosis.

### 3. Bind Orka and evaluate

The in-cluster OpenAI-compatible base URL is:

```text
http://aikit-cpu.kmx-inference.svc.cluster.local:8080/v1
```

With Orka installed, separately create an owner-reviewed `openai` Provider in
`orka-system` with that `baseURL` and `defaultModel: qwen-3.5-2b`. Its required
`secretRef` must name an independently created Secret in the same namespace.
For this keyless endpoint, use the explicit **noncredential** placeholder in
[Orka's keyless Provider contract](../orka.md#the-provider-connects-the-local-model-without-a-key).
Do not put real credentials in template YAML, argv, logs or ConfigMaps.

Require current-generation Provider readiness, then use
[full workspace evaluation and Lift](../suite-workspace.md#evaluate-the-whole-workspace).
Direct inference, ordered evaluation and Lift placement are different proofs;
report them separately. The template does not evaluate, publish or Lift an agent.

### 4. Stop and remove

With approval, scale only the owned `aikit-cpu` Deployment to zero to release its
CPU/memory requests; this does **not** stop node billing. Before deleting anything,
compare recorded namespace/object UIDs and contents and remove separate Provider
bindings deliberately. Delete only the owned resources from the explicit context.
Namespace deletion also removes anything later placed there: require separate
data-loss approval, and never infer ownership from a name or label.

Template checks run offline with `go test ./docs/azure`. They do not create
Kubernetes resources or prove live model execution.

The pinned three-CPU template was exercised on a single `Standard_D4as_v5`
AKS node: one Ready replica, no restarts at inspection, and one completed
exact-model assistant answer through the internal Service using Run Command.
This is a basic inference smoke check, not an Orka evaluation, Lift proof,
concurrency benchmark or sustained-memory qualification. Azure Policy may warn
about the image allowlist even when admission succeeds; a deny must be resolved
with the policy owner, never bypassed. If `kubectl top` fails, report resource
usage as unknown rather than inferring it from Ready state.
