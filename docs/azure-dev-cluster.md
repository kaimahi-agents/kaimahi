# Guidance for Azure: a small development cluster

Use this recipe when you need an AKS destination for native AgentSuite evaluation
and digest Lift. Azure infrastructure, runtime installation and model setup are
three separate steps. You do not need inference or an Azure cluster to develop
Kaimahi. Foundry is an inference option, not a prerequisite for AKS.

## Requirements and size

- Azure CLI authenticated to an explicitly selected subscription; permission to
  create a resource group, AKS, its managed identity and node infrastructure.
- Available regional VM quota and capacity for one non-burstable **4-vCPU,
  16-GiB, Linux amd64** node, such as `Standard_D4s_v5`.
- `kubectl`, Helm 3 and a KMX build supporting the current workspace lifecycle.
  See [build/install guidance](development.md) and [release boundaries](releases.md).
- Access to the cluster API, GitHub/Helm downloads and public runtime/model images.
  Native evaluation needs Kubernetes resource reads/creates, port-forwarding,
  ServiceAccount token requests and candidate cleanup permissions. The setup
  identity additionally needs CRD, RBAC and Helm installation privileges.
- A local free TCP result port. The default is 19180; an occupied port prevents
  evaluation even when the cluster and inference endpoint are healthy.

Start with AKS **Free** control-plane tier, one node and a **64-GiB managed OS
disk**. No GPU, dedicated inference pool, registry or paid monitoring add-on is
needed for this recipe. This is a small test configuration, not a high-availability
or production recommendation. Existing managed policies may add requirements.

## 1. Create an explicitly owned cluster

Run in Bash from the repository checkout. Choose an unused resource-group/name
pair so ownership and eventual cleanup are unambiguous:

```sh
export SUBSCRIPTION='<subscription-id>'
export REGION=westus2
export RG=kmx-dev-example
export CLUSTER=kmx-dev-example
az account set --subscription "$SUBSCRIPTION"
az account show --query '{name:name,id:id}'
az vm list-usage --location "$REGION" -o table
az vm list-skus --location "$REGION" --size Standard_D4s_v5 \
  --resource-type virtualMachines --all -o table
az group show --name "$RG"  # If it exists, inspect ownership; do not reuse blindly.

az group create --name "$RG" --location "$REGION" --tags purpose=kmx-dev
az aks create --resource-group "$RG" --name "$CLUSTER" --location "$REGION" \
  --tier free --node-count 1 --node-vm-size Standard_D4s_v5 \
  --node-osdisk-size 64 --node-osdisk-type Managed --os-sku Ubuntu \
  --enable-managed-identity --network-plugin azure --network-plugin-mode overlay \
  --no-ssh-key --tags purpose=kmx-dev

mkdir -m 700 ./private-dev
export KUBECONFIG="$PWD/private-dev/kubeconfig"
az aks get-credentials --resource-group "$RG" --name "$CLUSTER" --file "$KUBECONFIG"
kubectl --context "$CLUSTER" get nodes -o wide
```

Check SKU restrictions as well as quota; regional availability does not guarantee
allocation. `--no-ssh-key` avoids generating local SSH keys; it is not a claim
that node SSH is disabled. Select a supported Kubernetes version explicitly if
your organization requires one; the regional default changes over time.

Use a separate kubeconfig to avoid changing unrelated contexts. Do not commit
`private-dev/`, KMX state or credential files. On Windows/WSL use Azure CLI,
kubectl, Helm and KMX in the same environment with a reachable kubeconfig and free
loopback ports. This recipe was exercised from Linux; native Windows execution
and WSL networking were not separately qualified. See [AKS guidance](aks.md).

## 2. Install the supported runtime

```sh
export KAIMAHI_CONFIRM="$CLUSTER" # Explicit consent for this remote test cluster.
kmx --context "$CLUSTER" orka install --provider -
kmx --context "$CLUSTER" orka status
kubectl --context "$CLUSTER" -n orka-system get deployments,pvc
```

KMX installs its pinned Orka chart/CRDs and `orka-result-reader` ServiceAccount,
Role and RoleBinding. `--provider -` skips the default local-model Provider: AKS
does not automatically have the local Ollama service. Do not rerun installation
over a foreign/partial runtime; follow [Orka's installation boundary](orka.md).

The default chart provisions persistent storage. In the exercise below it created
1-GiB and 2-GiB Standard SSD claims in addition to the node OS disk. A functioning
storage class/CSI driver is therefore required. Installation readiness is not
an agent answer or an evaluation pass.

## 3. Optional CPU inference smoke

For an on-demand test, reuse the checkout's pinned Qwen 3.5 2B AIKit fixture:

```sh
kubectl --context "$CLUSTER" -n orka-system create configmap native-suite-aikit \
  --from-file=config.yaml=scripts/ci/eval-loop-model.yaml
kubectl --context "$CLUSTER" create -f scripts/ci/native-suite-model.yaml
kubectl --context "$CLUSTER" -n orka-system rollout status \
  deployment/native-suite-aikit --timeout=600s
```

The [fixture](../scripts/ci/native-suite-model.yaml) pins the image digest, forces
CPU execution and requests **1 CPU / 3 GiB**, with **4 CPU / 4 GiB** limits.
Its configuration is [here](../scripts/ci/eval-loop-model.yaml). Cold image pull
and first inference take additional time. This is a tested small model, not a
claim to be the globally smallest model or a promise of larger-model performance.

In a second terminal using the same kubeconfig, open a loopback forward:

```sh
kubectl --context "$CLUSTER" -n orka-system port-forward \
  service/native-suite-aikit 18081:8080
```

Then test directly and close that forward when finished:

```sh
curl --fail http://127.0.0.1:18081/readyz
curl --fail http://127.0.0.1:18081/v1/chat/completions \
  -H 'Content-Type: application/json' --data-binary @scripts/ci/eval-loop-warmup.json
```

Bind it with a separately named Provider, distinct from the member you will lift:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: dev-inference-key
  namespace: orka-system
type: Opaque
stringData:
  api-key: keyless-development-placeholder
---
apiVersion: core.orka.ai/v1alpha1
kind: Provider
metadata:
  name: dev-inference
  namespace: orka-system
spec:
  type: openai
  defaultModel: qwen-3.5-2b
  baseURL: http://native-suite-aikit.orka-system.svc.cluster.local:8080/v1
  secretRef: {name: dev-inference-key, key: api-key}
```

Save as `dev-provider.yaml`, create it with `kubectl --context "$CLUSTER" create
-f dev-provider.yaml`, and inspect `status.ready` on Provider `dev-inference`.
The placeholder is only for this unauthenticated fixture. For real inference,
provision the actual credential separately; keep values out of workspace files,
arguments and receipts. The endpoint must be reachable from runtime workloads,
not merely from your workstation. See [model guidance](models.md).

## 4. Prove the suite and lift its digest

Follow [workspace authoring](suite-workspace.md#create) to create one native Orka
member with model `qwen-3.5-2b`, instructions and appropriate cases. Native members
need no member image build or registry. From the workspace, with the same KMX
state and kubeconfig:

```sh
mkdir -m 700 ../private-evidence
kmx eval --to-context "$CLUSTER" --inference provider:dev-inference \
  --result-port 19281 --out ../private-evidence/passing.json
kmx push --evaluation ../private-evidence/passing.json \
  --to-layout ../dev-release dev-greeter:v1

# Substitute the complete manifest digest printed by push.
export SUITE_REF='dev-greeter@sha256:<full-manifest-digest>'
kmx lift "$SUITE_REF" --from-layout ../dev-release --to-context "$CLUSTER" \
  --inference provider:dev-inference --evidence ../private-evidence/passing.json \
  --promote --plan
# After reviewing, repeat the same command without --plan to apply.
```

Use a free result port; 19281 is an example, not a reserved allocation. Evaluation
now creates a run-scoped candidate and cleans it up. Moving its evidence to the
final member requires explicit `--promote`, even on the same cluster. Destination
names must be absent; source evidence is not destination evaluation. Lift does
not install the runtime/model, build, evaluate or publish. Named `--to dev`
selectors can replace repeated flags via [workspace kmx.yaml](suite-workspace.md#named-destinations).

Root `push` requires passing evidence by default on current main; `--unevaluated`
is explicit ordinary publication, not a way around lift evidence. Local layout
transfer avoids a registry requirement. Registry publication needs publisher
credentials; workload image pulls are a separate concern for image-backed profiles.
An AgentSuite authored for another runtime does not become Orka through target flags.

## Observed result and troubleshooting

**2026-10-15, main `1f2e243`, Linux CLI:** one West US 2 `Standard_D4s_v5`,
Kubernetes 1.36.4, Ubuntu, 64-GiB managed OS disk, pinned Orka v0.2.0 and the
fixture above. Installation took about three minutes; the cold model image pull
took about 2.5 minutes and the direct warmup returned in about five seconds.
Two text evaluation cases passed, candidate cleanup completed, evaluated local
publication and digest promotion created a Ready Provider/Agent pair. A separately
submitted native Task on the lifted member returned `Paris`. That separate answer
is not destination evaluation or cross-cluster qualification. No TUI, judge,
private registry, image-backed or kagent flow was qualified by this exercise.

The same cluster was subsequently upgraded through AKS's offered path to
**1.37.0**. Its single node returned Ready and the Orka/model deployments and
PVCs remained healthy. With KMX `2238a93`, a fresh native member passed evaluation,
candidate cleanup, evaluated local publication and digest promotion to Ready.
The earlier two-case workspace also passed evaluation again after the upgrade.

AgentSessions was checked separately: KMX's pinned `ca85f47` built-in chat host
ran on the development machine and used the AKS model through a loopback forward;
two cases and evaluated publication passed. Upstream AgentSessions v0.1.2's
plain-Kubernetes manifest also ran on 1.37, preserving identical replay output
across a pod restart and continuing an echo session. These are different host
versions/paths: they do not qualify KMX lift to AgentSessions or substrate
micro-VMs. The Sessions API in this reference deployment is unauthenticated and
was kept behind ClusterIP plus an operator-owned port-forward. See
[KMX's development-host boundary](suite-workspace.md#agentsessions-development-evaluation)
and [AgentSessions security](https://github.com/aramase/agentsessions/blob/main/docs/security.md).

- Initial eval attempts reported outcome unknown because another local process
  owned port 19180. Authenticated absent-Task result probing worked; selecting a
  free `--result-port` unblocked evaluation. Inspect the port owner rather than
  killing unrelated forwards. Unknown exports no passing evidence.
- Plain lift planning refused the candidate-to-final-member transition; explicit
  `--promote` with absent destination resources succeeded.
- Inspect pod events for scheduling/image-pull/storage failures before changing
  VM size. At rest after the test, node usage was about 2.7 GiB; this is not peak
  memory or a capacity guarantee.

## Costs, stop/start and cleanup

AKS Free tier does not make the node, disks or network free. The Azure retail
API returned **USD $0.192/hour** for Linux D4s v5 in West US 2 on 2026-10-15:
about **$1.54 for eight running hours**, or **$140.16 for 730 hours**, compute only.
The observed 64-GiB Premium SSD OS disk maps to P6 (about **$9.28/month** retail),
plus persistent runtime disks, applicable mount/transaction charges, Standard
Load Balancer/public-IP charges and outbound data. No paid monitoring add-on was
requested, but standard AKS outbound network resources were present. Prices,
regional availability, billing tiers and negotiated rates vary; consult the
[Azure calculator](https://azure.microsoft.com/pricing/calculator/) and
[retail pricing API](https://learn.microsoft.com/rest/api/cost-management/retail-prices/azure-retail-prices).

```sh
az aks stop  --resource-group "$RG" --name "$CLUSTER"
az aks start --resource-group "$RG" --name "$CLUSTER"
```

Stop deallocates compute; retained disks and other resources can still accrue
charges. Follow [AKS stop/start limits](https://learn.microsoft.com/azure/aks/start-stop-cluster),
including the supported stopped duration. After restart recheck runtime, bindings
and evidence rather than assuming old observations remain current.

For permanent removal, first inspect both the explicitly owned resource group
and AKS's reported `nodeResourceGroup`, and preserve anything you need. Then:

```sh
az aks show --resource-group "$RG" --name "$CLUSTER" --query nodeResourceGroup -o tsv
az resource list --resource-group "$RG" -o table
# Destructive: only for the dedicated group created for this recipe.
az group delete --name "$RG"
```

Confirm the managed node group and disks are gone afterward. Do not delete a shared
group or reuse this cleanup for existing clusters. Kubeconfig and private evidence
remain local. Runtime snapshot keys and volumes require coordinated backup if
retaining execution records; see [Orka storage](orka.md#preserve-the-charts-generated-snapshot-key).
