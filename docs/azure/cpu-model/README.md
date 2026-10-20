<a id="build-and-lift-a-cpu-model-agent-on-azure"></a>

# Build and Deploy a CPU-model agent on Azure

Create a release-notes agent, prove it on local Kubernetes, publish the tested
version to ACR, and Deploy that exact version to AKS. Both clusters run the same
small CPU model. No GPU, Foundry account or hosted-model API key is needed.

**Create → Prove locally → Publish → Deploy to AKS → Ask**

This adapts the [release-notes demo](https://gist.github.com/gambtho/2956e055b7bbe47562cd%36a4946589ab6)
to self-hosted inference. For the shared lifecycle contract, see
[suite workspaces](../../suite-workspace.md).

## Before you start

The main path uses **Bash on Linux**, with Azure CLI and the Kubernetes tools
in the same environment. Start in the repository root. The `kmx deploy` spelling
is unreleased; use a main checkout build, or `kmx lift` with the same arguments
on v0.8.1. See [Deploy compatibility](../../kmx.md#deploy-compatibility-unreleased).
**Windows users:** run KMX in WSL; see [Windows setup](#windows-setup) for the
PowerShell/Azure authentication handoff. Native Windows KMX is unsupported.

| Requirement | Development baseline |
|---|---|
| Local Kubernetes | Podman-backed kind, Linux/amd64; room for the model and Orka |
| AKS | One `Standard_D4as_v5` node: 4 vCPUs, 16 GiB RAM, 64-GiB OS disk |
| Model | Digest-pinned Qwen 3.5 2B Q4 AIKit; three CPU threads |
| Tools | A recorded KMX revision, kubectl 1.35, Helm 3, kind, Podman, Python 3, curl, Azure CLI and Bicep |
| Access | Approved Azure subscription, regional quota, cluster-scoped Kubernetes permissions and registry-scoped publication permissions |

AKS Free tier covers the control plane, **not** the VM, disk, networking or ACR.
Use a disposable environment and run one agent execution at a time.

**Safety rule:** stop on unreadable state, occupied names or partial writes.
Never interpret an error as absence, adopt an existing resource, or repeat an
ambiguous create. Keep credentials, answers and diagnostic payloads private.

### What depends on what

Kubernetes hosts the workloads; **AIKit serves the model**, **Orka runs the
agent**, and **KMX authors, evaluates, publishes and Deploys the suite**. Installing
AKS or Orka alone does not provide inference. ACR stores the published suite,
not a running agent or model server. See [models and inference](../../models.md)
for the endpoint contract.

| File | Supplies | Used by |
|---|---|---|
| [`aks.bicep`](aks.bicep) | Remote Kubernetes compute, networking and authentication | AKS workloads; no model or Orka installation |
| [`acr.bicep`](acr.bicep) | Registry for the tested OCI suite | KMX push and digest-based Deploy |
| [`aikit-cpu-namespace.yaml`](aikit-cpu-namespace.yaml) | Dedicated `kmx-inference` Namespace | Create before the namespaced model objects |
| [`aikit-cpu.yaml`](aikit-cpu.yaml) | ConfigMap with model settings, Deployment with pinned model server, internal Service | Orka's inference Provider, in both kind and AKS |
| [`template_test.go`](template_test.go) | Offline manifest/resource checks and model-pin parity with the CI fixture | Contributors running `go test ./docs/azure/cpu-model`; not deployed |
| [`README.md`](README.md) | Setup order, commands and verification boundaries | Operator following this demo |

The model image contains the packaged model; no separate model download command
is needed. Kubernetes still needs registry access to pull that image and Orka's
images. The agent suite does not install either dependency. Deploy the same
model manifest to both clusters **before** binding Orka and evaluating or asking
the agent. An existing compatible endpoint could replace AIKit, but that would
be a different demo configuration requiring its own evaluation.

## 1. Create AKS and ACR

The templates create infrastructure only:

- [`aks.bicep`](aks.bicep): one system node, CNI overlay/Cilium, managed identity,
  Entra authentication, Azure RBAC and Azure Policy. Local accounts and node SSH
  are disabled. No GPU, monitoring workspace or role assignments.
- [`acr.bicep`](acr.bicep): Basic registry in the **same development resource
  group**, with Entra authentication and admin/anonymous access disabled.

In **Bash**, set these selectors explicitly:

```bash
set -euo pipefail
umask 077
SUBSCRIPTION='<approved-subscription-id>'
RESOURCE_GROUP='<new-development-group>'
NODE_RESOURCE_GROUP='<new-development-group>-nodes'
CLUSTER='<new-cluster>'
REGISTRY='<globally-unique-registry-name>'
LOCATION='westus3' # Or westus2, after checking availability and quota.
PARAMETERS='<absolute-path-to-private-aks-parameters.json>'
```

Choose an available exact Kubernetes version:

```bash
az aks get-versions --subscription "$SUBSCRIPTION" --location "$LOCATION" -o table
```

Save this parameter object at `$PARAMETERS`, replacing every placeholder.
Names and region must match your selectors:

```json
{
  "clusterName": { "value": "<new-cluster>" },
  "nodeResourceGroup": { "value": "<new-development-group>-nodes" },
  "location": { "value": "westus3" },
  "kubernetesVersion": { "value": "<supported-exact-version>" },
  "tenantId": { "value": "<subscription-tenant-id>" },
  "apiServerAuthorizedIpRanges": { "value": ["<reviewed-public-ipv4>/32"] }
}
```

Review every CIDR: the template requires a nonempty list but does not validate
its security. Do not use `0.0.0.0/0` or assume ordinary web egress matches the
route to the AKS API.

Check both group names and the registry name. Require successful results of
`false`, `false`, and `true`, respectively:

```bash
az group exists --subscription "$SUBSCRIPTION" --name "$RESOURCE_GROUP"
az group exists --subscription "$SUBSCRIPTION" --name "$NODE_RESOURCE_GROUP"
az acr check-name --name "$REGISTRY" --query nameAvailable -o tsv
```

After approving the target and cost, create the group and review both plans.
Require exit code zero after **every** Azure CLI command:

```bash
az group create --subscription "$SUBSCRIPTION" --name "$RESOURCE_GROUP" --location "$LOCATION"
az deployment group what-if --subscription "$SUBSCRIPTION" \
  --resource-group "$RESOURCE_GROUP" --template-file docs/azure/cpu-model/aks.bicep \
  --parameters "@$PARAMETERS"
az deployment group what-if --subscription "$SUBSCRIPTION" \
  --resource-group "$RESOURCE_GROUP" --template-file docs/azure/cpu-model/acr.bicep \
  --parameters registryName="$REGISTRY" location="$LOCATION"
```

Approve the reviewed plans before running:

```bash
az deployment group create --subscription "$SUBSCRIPTION" \
  --resource-group "$RESOURCE_GROUP" --name kaimahi-dev-aks --mode Incremental \
  --template-file docs/azure/cpu-model/aks.bicep --parameters "@$PARAMETERS"
az deployment group create --subscription "$SUBSCRIPTION" \
  --resource-group "$RESOURCE_GROUP" --name kaimahi-dev-acr --mode Incremental \
  --template-file docs/azure/cpu-model/acr.bicep \
  --parameters registryName="$REGISTRY" location="$LOCATION"
```

Retain acknowledged resource IDs privately. The templates grant no access:
ask the resource owner to verify the necessary cluster-scoped AKS RBAC role and
registry-scoped `AcrPush` or equivalent approved data-plane permissions.
Do not enable admin credentials or self-grant broad roles to bypass a refusal.

## 2. Prepare local Kubernetes and cluster access

In Bash, define the operation's paths and targets once:

```bash
set -euo pipefail
umask 077
REPO="$PWD"
STATE="$(mktemp -d "$HOME/kmx-cpu-demo.XXXXXX")"
WORKSPACE="$STATE-workspace"
export KMX_HOME="$STATE/kmx"
export DOCKER_CONFIG="$STATE/docker"
mkdir -p "$DOCKER_CONFIG"
LOCAL_CLUSTER='<new-local-cluster>'
LOCAL_CONTEXT="kind-$LOCAL_CLUSTER"
LOCAL_KUBECONFIG="$STATE/local-kubeconfig"
AKS_KUBECONFIG="$STATE/aks-kubeconfig"
AKS_CONTEXT='<approved-AKS-context>'
REGISTRY='<approved-registry-name>'
```

Retrieve a separate AKS **user** kubeconfig, never admin credentials:

```bash
az aks get-credentials --subscription "$SUBSCRIPTION" --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER" --file "$AKS_KUBECONFIG" --context "$AKS_CONTEXT"
chmod 600 "$AKS_KUBECONFIG"
```

Use the supported Entra exec plugin (`kubelogin`) and your Azure CLI login.
Do not print raw kubeconfigs or credential responses. Windows users use the
platform-specific handoff below instead of retrieving credentials in Linux.

Use the intended Podman machine explicitly; do not rely on its default
connection. Inventory existing kind clusters before creating a new one:

```bash
export KIND_EXPERIMENTAL_PROVIDER=podman
export CONTAINER_HOST='<approved-Podman-socket-URI>'
kind get clusters
KUBECONFIG="$LOCAL_KUBECONFIG" kind create cluster --name "$LOCAL_CLUSTER" \
  --image kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0 \
  --wait 120s
```

Use the socket's authorized owner. Rootless Podman requires kind's documented
systemd delegation setup; do not disable its preflight. If cluster creation
needs an elevated shell, return the kubeconfig to your ordinary user with mode
`0600`. Run KMX as that ordinary user.

Verify authenticated access to both clusters:

```bash
KUBECONFIG="$LOCAL_KUBECONFIG" kubectl --context "$LOCAL_CONTEXT" get nodes
KUBECONFIG="$AKS_KUBECONFIG" kubectl --context "$AKS_CONTEXT" get nodes
KUBECONFIG="$AKS_KUBECONFIG" kubectl --context "$AKS_CONTEXT" get --raw=/readyz
```

## Single-node CPU inference test

Deploy the **same** [`aikit-cpu.yaml`](aikit-cpu.yaml) to both clusters.
It requests 1 CPU/3 GiB RAM and limits usage to 3 CPUs/4 GiB RAM.
Check allocatable capacity and existing requests, not just the machine's RAM.
The internal Service is unauthenticated: never expose it publicly.

Select the local target first:

```bash
export KUBECONFIG="$LOCAL_KUBECONFIG"
CTX="$LOCAL_CONTEXT"
```

Run the following block for that target. After its inference check, select
`export KUBECONFIG="$AKS_KUBECONFIG"` and `CTX="$AKS_CONTEXT"` and repeat.
Review and approve each target before creation:

```bash
TARGET_STATE="$STATE/$CTX"
mkdir "$TARGET_STATE"
kubectl --context "$CTX" describe nodes
existing="$(kubectl --context "$CTX" get namespace kmx-inference --ignore-not-found -o name)"
test -z "$existing"
kubectl --context "$CTX" create --dry-run=server -f "$REPO/docs/azure/cpu-model/aikit-cpu-namespace.yaml"
# Approve the Namespace before creating it.
kubectl --context "$CTX" create -f "$REPO/docs/azure/cpu-model/aikit-cpu-namespace.yaml" \
  -o json > "$TARGET_STATE/namespace.json"
kubectl --context "$CTX" create --dry-run=server -f "$REPO/docs/azure/cpu-model/aikit-cpu.yaml"
# Review and approve the namespaced objects before creating them.
kubectl --context "$CTX" create -f "$REPO/docs/azure/cpu-model/aikit-cpu.yaml" \
  -o json > "$TARGET_STATE/model-resources.json"
kubectl --context "$CTX" -n kmx-inference rollout status deployment/aikit-cpu --timeout=15m
```

Readiness is not inference proof. In a second Bash shell, use the same
kubeconfig/context and start a tracked loopback forward. Shell variables do
not carry across terminals: explicitly set `KUBECONFIG` and `CTX` there first:

```bash
kubectl --context "$CTX" -n kmx-inference port-forward --address 127.0.0.1 \
  service/aikit-cpu 18091:8080
```

In the first shell, send one request without retries:

```bash
HTTP_CODE="$(curl --silent --show-error --max-time 180 \
  -H 'Content-Type: application/json' \
  --data '{"model":"qwen-3.5-2b","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"Reply with OK."}]}' \
  --output "$TARGET_STATE/response.json" --write-out '%{http_code}' \
  http://127.0.0.1:18091/v1/chat/completions)"
test "$HTTP_CODE" = 200
python3 - "$TARGET_STATE/response.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    response = json.load(source)
choice = response["choices"][0]
assert response["model"] == "qwen-3.5-2b"
assert choice["finish_reason"] == "stop"
assert choice["message"]["role"] == "assistant"
assert choice["message"]["content"].strip()
print("Direct CPU inference: passed")
PY
rm "$TARGET_STATE/response.json"
```

Stop that specific forward on success **or failure**. Repeat for AKS.
Keep failed payloads private if needed for diagnosis.

## 4. Install Orka on both clusters

For each cluster, select its kubeconfig and context as above, then run:

```bash
kmx --context "$CTX" orka install --provider cpu-inference \
  --model qwen-3.5-2b \
  --model-url http://aikit-cpu.kmx-inference.svc.cluster.local:8080/v1
kubectl --context "$CTX" -n orka-system wait \
  --for=condition=Ready provider/cpu-inference --timeout=5m
```

Confirm the exact AKS context when prompted. For reviewed automation, scope
`KAIMAHI_CONFIRM="$CTX"` to the individual command; do not export it globally.
Inspect that Provider readiness describes its current generation.

The Provider uses the `openai` protocol, **not a hosted OpenAI service**.
The installer supplies the explicit noncredential placeholder required by
[Orka's keyless contract](../../orka.md#the-provider-connects-the-local-model-without-a-key).

## 5. Create and prove the agent locally

Write the instructions, then create a tool-free workspace:

```bash
cat > "$STATE/instructions.md" <<'EOF'
Write brief release notes using only the supplied facts.
Use exactly two Markdown headings: ## Changes and ## Known limitations.
Put bullets under each heading. Under Changes, begin every bullet with the
literal prefix "Added:" or "Fixed:" and mention retry and search changes.
Under Known limitations, begin every bullet with the literal prefix "Unresolved:".
Do not replace "Unresolved" with synonyms such as "not fixed".
Never invent fixes, features or measurements. Stay under 40 words.
EOF
kmx create release-notes --workspace "$WORKSPACE" --model qwen-3.5-2b \
  --instructions "$STATE/instructions.md"
```

Replace `$WORKSPACE/eval/example.yaml` with this case. Do not leave the default
math case alongside it:

```yaml
id: release-facts
input: |
  Write release notes from these facts only.
  Added retry for failed sync. Fixed search filters resetting after refresh.
  Known limitation: large CSV exports time out; not fixed.
runs: 3
minPasses: 2
assertions:
- id: changes-heading
  type: regex
  pattern: (?m)^#{1,2} Changes\s*$
- id: limitations-heading
  type: regex
  pattern: (?m)^#{1,2} Known limitations\s*$
- id: retry-fact
  type: regex
  pattern: (?i)\bretr(?:y|ies)\b
- id: search-fact
  type: regex
  pattern: (?i)search
- id: limitation-unresolved
  type: regex
  pattern: '\A[ \t]*#{1,2} Changes[ \t]*\n(?:[ \t]*\n)*-[ \t]+(?:Added|Fixed):[^\n]*(?:\n(?:[ \t]*\n)*-[ \t]+(?:Added|Fixed):[^\n]*)*\n(?:[ \t]*\n)*#{1,2} Known limitations[ \t]*\n(?:[ \t]*\n)*-[ \t]+Unresolved:[^\n]*(?:\n(?:[ \t]*\n)*-[ \t]+Unresolved:[^\n]*)*(?:\n[ \t]*)*\z'
```

Evaluate the complete workspace on **local kind**, not AKS:

```bash
KUBECONFIG="$LOCAL_KUBECONFIG" kmx eval --workspace "$WORKSPACE" \
  --to-context "$LOCAL_CONTEXT" --to-namespace orka-system \
  --inference provider:cpu-inference --out "$STATE/passing.json"
```

Require at least two passing runs, no unknown outcomes, completed candidate
cleanup and exported evidence. Do not alter source after evaluation.
Evidence binds this exact suite, runtime, model and case inventory; it is
unsigned historical observation, not destination evaluation or authentication.

## 6. Publish the tested version

Authenticate with Azure CLI and write only to this demo's private Docker config.
Pass credentials through stdin, never argv:

```bash
az acr login --subscription "$SUBSCRIPTION" --name "$REGISTRY" \
  --expose-token --output json --only-show-errors |
  python3 -c 'import sys,json,os,base64; from pathlib import Path; a=json.load(sys.stdin); p=Path(os.environ["DOCKER_CONFIG"]); credential=base64.b64encode(("00000000-0000-0000-0000-000000000000:"+a["accessToken"]).encode()).decode(); fd=os.open(p/"config.json",os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600); os.fdopen(fd,"w").write(json.dumps({"auths":{a["loginServer"]:{"auth":credential}}}))'
```

Windows users use the handoff below instead. Publish with the matching receipt:

```bash
PUBLISH_REF="$REGISTRY.azurecr.io/agents/release-notes:<unique-tag>"
kmx push --workspace "$WORKSPACE" "$PUBLISH_REF" --evaluation "$STATE/passing.json"
```

Require success and retain the returned manifest digest. Set `RELEASE_REF` to
the published repository plus that exact `@sha256:...` digest, not a tag.

<a id="7-lift-to-aks-then-ask"></a>

## 7. Deploy to AKS, then ask

Review the GET-only plan. `--promote` explicitly uses **source-only** evidence;
the destination must satisfy the same packaged runtime/model requirements:

```bash
RELEASE_REF="$REGISTRY.azurecr.io/agents/release-notes@sha256:<published-digest>"
KUBECONFIG="$AKS_KUBECONFIG" kmx deploy "$RELEASE_REF" --plan --promote \
  --to-context "$AKS_CONTEXT" --to-namespace orka-system \
  --inference provider:cpu-inference --evidence "$STATE/passing.json"
```

Approve that exact plan and remote context, then Deploy:

```bash
KUBECONFIG="$AKS_KUBECONFIG" kmx deploy "$RELEASE_REF" --promote \
  --to-context "$AKS_CONTEXT" --to-namespace orka-system \
  --inference provider:cpu-inference --evidence "$STATE/passing.json"
```

Require Ready placement. Deploy sends no model Tasks; ask separately:

```bash
cat > "$STATE/prompt.txt" <<'EOF'
Write release notes from these facts only.
Added retry for failed sync. Fixed search filters resetting after refresh.
Known limitation: large CSV exports time out; not fixed.
EOF
KUBECONFIG="$AKS_KUBECONFIG" kmx --context "$AKS_CONTEXT" run \
  --namespace orka-system --agent release-notes \
  --prompt-file "$STATE/prompt.txt" > "$STATE/answer.txt"
```

Check the five case assertions against the private answer. Exit zero alone
proves execution, not correctness. Preserve the deployed digest; do not print
answers as setup evidence.

## 8. Demonstrate a failed edit

Save the tested instructions privately. Change only the workspace instructions
to `Answer in one sentence, with no headings or bullet points.` Run a **new**
local evaluation with a new output path.

Require complete assertion failures and completed cleanup—not execution errors.
Do not publish or Deploy the edit. Independently inspect the AKS placement and
send a fresh Task: the tested digest must remain deployed and answering.
Restore the saved instructions deliberately; never relabel evidence across edits.

## Cleanup

Review retained IDs and the contents of each target before deleting anything.
Deleting a test-cluster name is not proof that everything there belongs to you.

- Delete only the approved local cluster with `kind delete cluster --name "$LOCAL_CLUSTER"`,
  using its original Podman connection. Preserve unrelated containers/machines.
- Obtain explicit data-loss approval before deleting the Azure development group:
  this removes AKS, ACR and **all published suites**. Use Azure CLI with
  the exact subscription/group; verify the development and managed node groups
  are gone. Failed deletion can continue billing.
- For partial cleanup, compare acknowledged UIDs before removing model resources
  or Provider bindings. Namespace deletion also destroys later additions.
- Remove private credentials and payloads only after retaining the nonsecret
  records you need. Scaling the model to zero does not stop node billing.

## Windows setup

This section applies **only to Windows**. Run KMX, kind, kubectl and Helm in
WSL, not native Windows. The tested setup kept Azure CLI on Windows:

1. Run the infrastructure commands in PowerShell, translating Bash assignments,
   continuation characters and file paths to PowerShell syntax. Keep the same
   explicit subscription and resource selectors; check `$LASTEXITCODE` after
   every Azure CLI call.
2. Retrieve the AKS **user** kubeconfig on Windows with `az aks get-credentials`
   using explicit subscription/group/cluster and a separate `--file`.
   Copy it to `AKS_KUBECONFIG` in WSL and set mode `0600`. Adapt the exec plugin
   to the verified Windows `kubelogin.exe` path under `/mnt/c/...`, preserving
   its full arguments and API version. Azure CLI login remains on Windows.
3. Select the dedicated Windows Podman machine's WSL socket explicitly.
   The tested setup used its rootful socket and authorized owner; rootless
   remote kind refused the delegation preflight. Do not disable that check.
   Return any elevated-created kubeconfig to your ordinary WSL user before KMX.
4. For publication, bridge Windows Azure authentication into the isolated WSL
   Docker config. A Windows login alone does not authenticate KMX in WSL.

Set the PowerShell selectors from section 1, `$WSL_DISTRIBUTION` to the
distribution running KMX, `$WSL_USER` to the ordinary WSL user running KMX, and
`$WSL_DOCKER_CONFIG` to the absolute WSL path printed by
`echo "$DOCKER_CONFIG"`. The config file must not already exist; credentials
are passed over stdin, never argv:

```powershell
$WSL_DISTRIBUTION = '<KMX-WSL-distribution>'
$WSL_USER = '<KMX-WSL-user>'
$WSL_DOCKER_CONFIG = '<absolute-WSL-path-to-demo-docker-directory>'
$auth = az acr login --subscription $SUBSCRIPTION --name $REGISTRY `
  --expose-token --output json --only-show-errors
if ($LASTEXITCODE -ne 0) { throw 'ACR authentication failed' }
$auth | wsl --distribution $WSL_DISTRIBUTION --user $WSL_USER --exec python3 -c 'import sys,json,os,base64; from pathlib import Path; a=json.load(sys.stdin); p=Path(sys.argv[1]); p.mkdir(mode=0o700,parents=True,exist_ok=True); credential=base64.b64encode(("00000000-0000-0000-0000-000000000000:"+a["accessToken"]).encode()).decode(); fd=os.open(p/"config.json",os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600); os.fdopen(fd,"w").write(json.dumps({"auths":{a["loginServer"]:{"auth":credential}}}))' $WSL_DOCKER_CONFIG
$auth = $null
if ($LASTEXITCODE -ne 0) { throw 'Private WSL credential setup failed' }
```

## Troubleshooting and verification limits

| Symptom | Action |
|---|---|
| API timeout | Check the actual VPN/tunnel route and approved public egress CIDR. Never use `0.0.0.0/0`, an empty allowlist or a private VPN adapter IP. Follow local security policy. |
| TLS-verified HTTP 401 | Network/TLS works; authenticated Kubernetes access is not yet proved. Check the exec plugin and cluster RBAC. |
| Slow cleanup; “scope changed or unknown” | Measure payload-free API timings and compare cluster/namespace UIDs. Windows authentication took 2–3 seconds per subprocess in this test, exhausting the fixed 90-second budget. An approved expiry-aware private credential cache resolved cleanup; do not raise timeouts or use static kubeconfig tokens. |
| Leftover candidates | List with `KUBECONFIG="$LOCAL_KUBECONFIG" kmx eval cleanup --to-context "$LOCAL_CONTEXT" --to-namespace orka-system`; confirm with `KUBECONFIG="$LOCAL_KUBECONFIG" kmx eval cleanup <full-run-id> --to-context "$LOCAL_CONTEXT" --to-namespace orka-system`. Do not bulk-delete Agents. |
| `unresolved` assertion fails | Require the literal prefix in instructions. The first local version ran successfully but passed 0/3; revised instructions passed 3/3 without weakening assertions. |
| Pending model or policy denial | Inspect scheduling/allocatable resources and policy-owner guidance. Do not bypass policy or infer usage from readiness. |
| Run Command needed | Use Windows `az aks command invoke` with explicit subscription/group/cluster; inspect returned `exitCode`, not just CLI success. Attached scripts need LF endings. Keep logs private. This is not a way to run the entire KMX lifecycle remotely. |

**Historical demonstration only:** a pre-rebase development KMX build against
Orka v0.2.0 demonstrated identical CPU inference on Podman-backed kind and a
single-node AKS cluster; three passing local Orka runs; evaluated ACR
publication; registry-backed AKS promotion; a separate fresh answer passing all
five checks; and a bad-instructions version failing 0/3 with cleanup completed
and no evidence exported. The exact KMX commit was not retained, so these results
do not qualify this checkout. The tested instructions were restored. A second
destination answer after that refusal has not yet been checked.

**Not demonstrated:** fresh AKS creation from `aks.bicep`, general model quality,
concurrency, sustained memory use or production availability. Bicep compilation
and `go test ./docs/azure/cpu-model` are offline checks, not deployment proof. The model
has a 4096-token context and 64-token default output budget; it is a smoke-test
preset, not a general agent-sizing recommendation.
