<a id="aks-free-tier-demo-create-prove-lift"></a>
<a id="aks-free-tier-demo-create-prove-deploy"></a>

# AKS Free-tier demo: Create, Test, Deploy

This copy-paste walkthrough uses one shell session to author a native Orka
workspace, test the exact package on local kind, provision a disposable AKS
target, publish to Azure Container Registry (ACR), and Deploy the immutable digest.

```text
Create source -> Test locally -> Provision destination -> Publish digest -> Deploy
```

AKS uses the **Free control-plane tier**, but its worker VM, disk, ACR,
networking, and model calls can incur charges. Azure creation intentionally comes
after local proof, so source, model access, Docker, and evaluation failures do not
leave a cloud environment billing before there is a passing package.

> [!IMPORTANT]
> Deploy verifies placement and readiness, not an answer from AKS. It sends no
> Task or model request. A separate root `run --agent NAME` or supported Orka
> `chat NAME`, with the destination context and namespace, can invoke the deployed
> Agent. That fresh invocation is not suite evaluation or proof of destination
> behavior from Deploy itself.

## Prerequisites

You need:

- an authenticated Azure CLI with permission to create AKS, ACR, and role assignments;
- Docker, `kubectl`, and enough local capacity for one kind cluster;
- one exact KMX main-checkout executable implementing this guide (`test` and
  `deploy` are unreleased; v0.8.1 uses `kmx eval` and `kmx lift` with the same arguments);
- an OpenAI-compatible HTTPS endpoint reachable from kind and AKS;
- its exact model/deployment name and a non-empty API key.

From this checkout, build KMX first:

```bash
go build -o bin/kmx ./cmd/kmx
```

Authenticate Azure and select the intended subscription:

```bash
az login
az account show --query '{subscription:name,user:user.name}' --output table
# If needed:
# az account set --subscription '<subscription-name-or-id>'
```

Load the key into the shell without putting it in command arguments. Choose the
command for your shell:

```bash
# Zsh:
# IFS= read -rs 'ORKA_API_KEY?Provider API key: '; printf '\n' >&2

# Bash:
# IFS= read -rs -p 'Provider API key: ' ORKA_API_KEY; printf '\n' >&2
```

Never paste API keys, subscription IDs, resource IDs, endpoints, registry names,
public IPs, or private evidence into issues or pull requests.

## 1. Set and validate variables

Paste this block in the shell used for the whole demo. Existing non-empty values
are preserved. For Azure OpenAI/Foundry v1, the model ID is the deployment name.
The conservative model and URL alphabets make the later Provider heredoc data,
not YAML structure.

```bash
set_demo_variables() {
  local acr_default
  export KMX="${KMX:-$(pwd -P)/bin/kmx}"
  export AZURE_LOCATION="${AZURE_LOCATION:-westus3}"
  export RESOURCE_GROUP="${RESOURCE_GROUP:-kmx-demo-rg}"
  export AKS_CLUSTER="${AKS_CLUSTER:-kmx-demo-aks}"
  export LOCAL_CLUSTER="${LOCAL_CLUSTER:-kmx-aks-eval}"
  export LOCAL_CONTEXT="kind-${LOCAL_CLUSTER}"
  export AGENT="${AGENT:-greeter}"
  export INFERENCE_PROVIDER="${INFERENCE_PROVIDER:-inference}"
  export MODEL_SECRET="${MODEL_SECRET:-inference-provider-key}"
  export DEMO_ROOT="${DEMO_ROOT:-$HOME/kaimahi-demos/aks-greeter-demo}"

  if [ -z "${ACR_NAME:-}" ]; then
    acr_default="kmxdemo$(date +%s)"
    printf 'ACR name [%s]: ' "$acr_default" >&2
    IFS= read -r ACR_NAME
    export ACR_NAME="${ACR_NAME:-$acr_default}"
  fi
  if [ -z "${MODEL_ID:-}" ]; then
    printf 'Exact model/deployment name: ' >&2
    IFS= read -r MODEL_ID
    export MODEL_ID
  fi
  if [ -z "${MODEL_BASE_URL:-}" ]; then
    printf 'HTTPS OpenAI-compatible base URL: ' >&2
    IFS= read -r MODEL_BASE_URL
    export MODEL_BASE_URL
  fi

  export ACR_LOGIN_SERVER="${ACR_NAME}.azurecr.io"
  export WORKSPACE="$DEMO_ROOT/workspace"
  export PRIVATE_DIR="$DEMO_ROOT/private"
  export EVIDENCE="$PRIVATE_DIR/local-passing.json"
  export TAGGED_REF="$ACR_LOGIN_SERVER/agentsuites/$AGENT:v1"

  test -x "$KMX" || { printf 'KMX is not executable: %s\n' "$KMX" >&2; return 1; }
  printf '%s\n' "$ACR_NAME" | grep -Eq '^[[:alnum:]]{5,50}$' || {
    printf 'ACR name must be 5-50 alphanumeric characters.\n' >&2; return 1
  }
  case "$MODEL_ID$MODEL_BASE_URL$AGENT$INFERENCE_PROVIDER$MODEL_SECRET" in
    *$'\n'* | *$'\r'*)
      printf 'Model, endpoint, Agent, Provider, and Secret values must be one line.\n' >&2
      return 1 ;;
  esac
  printf '%s' "$MODEL_ID" | grep -Eq '^[[:alnum:]._:/-]+$' || {
    printf 'Model/deployment name must be concrete single-line identifier text.\n' >&2; return 1
  }
  printf '%s' "$MODEL_BASE_URL" |
    grep -Eq '^https://[[:alnum:].-]+(:[0-9]{1,5})?(/[[:alnum:]._~%+,:=@/-]*)?$' || {
    printf 'Endpoint must be HTTPS with no credentials, query, fragment, spaces, quotes, or placeholders.\n' >&2
    return 1
  }
  if [ "${#AGENT}" -gt 63 ] || ! printf '%s' "$AGENT" |
    grep -Eq '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'; then
    printf 'Agent must be a lowercase Kubernetes label without dots.\n' >&2; return 1
  fi
  for name in "$INFERENCE_PROVIDER" "$MODEL_SECRET"; do
    if [ "${#name}" -gt 63 ] || ! printf '%s' "$name" |
      grep -Eq '^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$'; then
      printf 'Provider and Secret names must be lowercase Kubernetes names.\n' >&2
      return 1
    fi
  done
  [ "$AGENT" != "$INFERENCE_PROVIDER" ] || {
    printf 'Agent and inference Provider names must differ for promotion.\n' >&2; return 1
  }
  test -n "${ORKA_API_KEY:-}" || {
    printf 'Load a non-empty ORKA_API_KEY before any setup step.\n' >&2; return 1
  }
  "$KMX" local up --help 2>/dev/null | grep -q -- '--step' || {
    printf 'KMX does not implement this guide: %s\n' "$KMX" >&2; return 1
  }

  mkdir -p "$DEMO_ROOT" "$PRIVATE_DIR" || return
  chmod 700 "$DEMO_ROOT" "$PRIVATE_DIR" || return
  printf 'KMX=%s\nACR=%s\nMODEL=%s\nWORKSPACE=%s\n' \
    "$KMX" "$ACR_NAME" "$MODEL_ID" "$WORKSPACE"
}

if set_demo_variables; then
  unset -f set_demo_variables
else
  unset -f set_demo_variables
  printf 'Variable setup failed; stop before step 2.\n' >&2
  false
fi
```

Do not mix bare `kmx` and `"$KMX"`; an older executable on `PATH` may implement
a different command surface.

The walkthrough writes more than authored source: the build creates `bin/kmx`;
KMX uses its normal state/cache directories and kubeconfig; evaluation registers
managed private proof; and `az acr login` can update Docker's credential store.
The demo workspace and explicit receipt stay under mode-0700 `DEMO_ROOT`.

## 2. Create or reuse source offline

`kmx create` is intentionally non-idempotent. This block reuses existing source
only when its member ID, runtime, protocol, and exact model match.

```bash
ensure_demo_workspace() {
  local member="$WORKSPACE/agents/$AGENT/member.yaml"

  if [ -e "$WORKSPACE/workspace.yaml" ]; then
    if [ -f "$WORKSPACE/workspace.yaml" ] && [ ! -L "$WORKSPACE/workspace.yaml" ] && \
      [ -f "$member" ] && [ ! -L "$member" ] && \
      grep -Fqx -- "name: $AGENT" "$WORKSPACE/workspace.yaml" && \
      grep -Fqx -- "member: agents/$AGENT/member.yaml" "$WORKSPACE/workspace.yaml" && \
      grep -Fqx -- "id: $AGENT" "$member" && \
      grep -Fqx -- '    protocol: openai-compatible' "$member" && \
      grep -Fqx -- "    model: $MODEL_ID" "$member" && \
      grep -Fqx -- '    id: orka' "$member"; then
      printf 'Exact workspace already exists; reusing %s\n' "$WORKSPACE" >&2
      return 0
    fi
    printf 'Workspace differs; refusing to overwrite %s\n' "$WORKSPACE" >&2
    return 1
  fi

  if [ ! -e "$DEMO_ROOT/instructions.md" ]; then
    printf '%s\n' 'Answer accurately, briefly, and in plain text.' \
      > "$DEMO_ROOT/instructions.md" || return
  fi
  "$KMX" create "$AGENT" \
    --workspace "$WORKSPACE" \
    --model "$MODEL_ID" \
    --instructions "$DEMO_ROOT/instructions.md"
}

if ensure_demo_workspace; then
  unset -f ensure_demo_workspace
else
  unset -f ensure_demo_workspace
  false
fi
```

The scaffold case asks `What is 2+2?` and requires `4`. Replace it with a
deterministic, meaningful case for a real agent.

## 3. Prepare local Orka

```bash
prepare_local_orka() {
  KIND_CLUSTER="$LOCAL_CLUSTER" \
    "$KMX" --context "$LOCAL_CONTEXT" local up --step cluster || return
  "$KMX" --context "$LOCAL_CONTEXT" orka install --provider -
}

if prepare_local_orka; then
  unset -f prepare_local_orka
else
  unset -f prepare_local_orka
  false
fi
```

## 4. Configure the local Provider

The helpers reuse an existing Secret carrying `api-key` and an exact Provider
without reading the Secret value. Missing keys or changed specs refuse instead
of being overwritten.

```bash
ensure_provider_secret() {
  local context="$1" marker
  # shellcheck disable=SC2016 # Kubernetes evaluates this Go template.
  marker=$(kubectl --context "$context" -n orka-system \
    get secret "$MODEL_SECRET" --ignore-not-found=true \
    -o 'go-template={{if .metadata.name}}secret:{{range $key, $_ := .data}}{{if eq $key "api-key"}}present{{end}}{{end}}{{end}}') || return

  if [ "$marker" = secret:present ]; then
    printf 'Secret already has api-key on %s; reusing it.\n' "$context" >&2
    return 0
  fi
  if [ -n "$marker" ]; then
    printf 'Secret exists without the required api-key on %s; refusing to overwrite it.\n' "$context" >&2
    return 1
  fi
  printf '%s' "$ORKA_API_KEY" | kubectl --context "$context" -n orka-system \
    create secret generic "$MODEL_SECRET" --from-file=api-key=/dev/stdin
}

configure_provider() {
  local context="$1" observed expected
  ensure_provider_secret "$context" || return
  # shellcheck disable=SC2016 # Kubernetes evaluates this Go template.
  observed=$(kubectl --context "$context" -n orka-system \
    get provider "$INFERENCE_PROVIDER" --ignore-not-found=true \
    -o 'go-template={{if .metadata.name}}{{.spec.type}}{{"\n"}}{{.spec.baseURL}}{{"\n"}}{{.spec.defaultModel}}{{"\n"}}{{.spec.secretRef.name}}{{"\n"}}{{.spec.secretRef.key}}{{end}}') || return
  expected=$(printf 'openai\n%s\n%s\n%s\napi-key' \
    "$MODEL_BASE_URL" "$MODEL_ID" "$MODEL_SECRET")

  if [ -n "$observed" ]; then
    [ "$observed" = "$expected" ] || {
      printf 'Provider differs on %s; refusing to update it.\n' "$context" >&2
      return 1
    }
    printf 'Provider already matches on %s; reusing it.\n' "$context" >&2
  else
    kubectl --context "$context" create -f - <<EOF || return
apiVersion: core.orka.ai/v1alpha1
kind: Provider
metadata:
  name: "${INFERENCE_PROVIDER}"
  namespace: orka-system
spec:
  type: openai
  baseURL: "${MODEL_BASE_URL}"
  defaultModel: "${MODEL_ID}"
  secretRef:
    name: "${MODEL_SECRET}"
    key: api-key
EOF
  fi

  kubectl --context "$context" -n orka-system wait \
    --for=condition=Ready "providers.core.orka.ai/$INFERENCE_PROVIDER" \
    --timeout=5m
}

configure_provider "$LOCAL_CONTEXT" || false
```

Provider Ready is configuration evidence, not proof of a model answer.

<a id="5-prove-locally"></a>

## 5. Test locally

Evaluation creates a fresh candidate and Task. Existing evidence avoids another
model call, but is still checked against the current exact package before Azure
creation by publishing to a transient local OCI layout.

```bash
check_evidence_coherence() {
  local check_root rc
  check_root=$(mktemp -d "$PRIVATE_DIR/coherence.XXXXXX") || return
  "$KMX" push \
    --workspace "$WORKSPACE" \
    --evaluation "$EVIDENCE" \
    --to-layout "$check_root/layout" \
    "agentsuites/${AGENT}:coherence-check"
  rc=$?
  rm -rf "$check_root"
  return "$rc"
}

prove_demo_workspace() {
  unset KMX_DEMO_PROOF_READY
  if [ -f "$EVIDENCE" ] && [ ! -L "$EVIDENCE" ]; then
    printf 'Existing evidence found; checking exact workspace coherence.\n' >&2
  elif [ -e "$EVIDENCE" ]; then
    printf 'Evidence path is not a regular non-symlink file.\n' >&2
    return 1
  else
    "$KMX" test \
      --workspace "$WORKSPACE" \
      --to-context "$LOCAL_CONTEXT" \
      --to-namespace orka-system \
      --inference "provider:$INFERENCE_PROVIDER" \
      --out "$EVIDENCE" || return
  fi
  check_evidence_coherence || return
  export KMX_DEMO_PROOF_READY="$WORKSPACE|$EVIDENCE"
}

if prove_demo_workspace; then
  unset -f prove_demo_workspace
else
  unset KMX_DEMO_PROOF_READY
  unset -f prove_demo_workspace
  false
fi
```

Only a full pass writes evidence. The local push refuses stale evidence without
registry access; a source change therefore requires fresh proof. It creates no
durable layout, registry artifact, deployment, or model call.

## 6. Review and create AKS Free tier

Plan reads Azure account state but writes nothing. The function refuses unless
the same-shell local coherence check completed and passes again immediately
before Azure creation:

```bash
provision_demo_aks() {
  if [ "${KMX_DEMO_PROOF_READY:-}" != "$WORKSPACE|$EVIDENCE" ] ||
    ! check_evidence_coherence; then
    unset KMX_DEMO_PROOF_READY
    printf 'Local exact-package proof was not checked in this shell; refusing Azure creation.\n' >&2
    return 1
  fi
  "$KMX" aks up --plan --payload orka \
    --resource-group "$RESOURCE_GROUP" \
    --registry "$ACR_NAME" \
    --cluster "$AKS_CLUSTER" \
    --location "$AZURE_LOCATION" \
    --node-count 1 \
    --node-size Standard_B4ms \
    --observability=false || return
  KAIMAHI_CONFIRM="$AKS_CLUSTER" "$KMX" aks up --payload orka \
    --resource-group "$RESOURCE_GROUP" \
    --registry "$ACR_NAME" \
    --cluster "$AKS_CLUSTER" \
    --location "$AZURE_LOCATION" \
    --node-count 1 \
    --node-size Standard_B4ms \
    --observability=false
}

if provision_demo_aks; then
  unset KMX_DEMO_PROOF_READY
  unset -f provision_demo_aks check_evidence_coherence
else
  unset KMX_DEMO_PROOF_READY
  unset -f provision_demo_aks check_evidence_coherence
  false
fi
```

This uses AKS `--tier free`, one `Standard_B4ms` worker, a 64 GiB OS disk,
Cilium, ACR Basic, and pinned Orka v0.2.0. Interrupted phases can leave billable
resources; use the exact recovery command printed by the same executable.

## 7. Configure the AKS Provider

```bash
if configure_provider "$AKS_CLUSTER"; then
  unset ORKA_API_KEY
  unset -f ensure_provider_secret configure_provider
else
  unset ORKA_API_KEY
  unset -f ensure_provider_secret configure_provider
  false
fi
```

## 8. Publish to ACR

Push checks evidence against the exact prepared graph and prints an immutable
manifest reference. It creates no helper file.

```bash
publish_demo_suite() {
  local output ref count
  az acr login --name "$ACR_NAME" || return
  output=$("$KMX" push \
    --workspace "$WORKSPACE" \
    --evaluation "$EVIDENCE" \
    "$TAGGED_REF") || return
  printf '%s\n' "$output"

  ref=$(printf '%s\n' "$output" | sed -n 's/^Pin this AgentSuite: //p')
  count=$(printf '%s\n' "$ref" | grep -Ec '^[^[:space:]]+@sha256:[0-9a-f]{64}$')
  [ "$count" -eq 1 ] || {
    printf 'Push did not return one immutable suite reference.\n' >&2
    return 1
  }
  export SUITE_REF="$ref"
}

if publish_demo_suite; then
  unset -f publish_demo_suite
else
  unset -f publish_demo_suite
  false
fi
```

Never Deploy the mutable `:v1` tag.

<a id="9-plan-and-apply-lift-once"></a>

## 9. Plan and apply Deploy once

```bash
plan_and_lift_demo() {
  "$KMX" deploy "$SUITE_REF" \
    --to-context "$AKS_CLUSTER" \
    --to-namespace orka-system \
    --inference "provider:$INFERENCE_PROVIDER" \
    --evidence "$EVIDENCE" \
    --promote \
    --plan || return

  KAIMAHI_CONFIRM="$AKS_CLUSTER" \
  "$KMX" deploy "$SUITE_REF" \
    --to-context "$AKS_CLUSTER" \
    --to-namespace orka-system \
    --inference "provider:$INFERENCE_PROVIDER" \
    --evidence "$EVIDENCE" \
    --promote
}

if plan_and_lift_demo; then
  unset -f plan_and_lift_demo
else
  unset -f plan_and_lift_demo
  false
fi
```

Promotion requires canonical `Provider/$AGENT` and `Agent/$AGENT` to be absent.
Do not repeat source-evidence promotion after success; occupied resources refuse.

## 10. List and inspect on AKS

```bash
"$KMX" --context "$AKS_CLUSTER" orka list --namespace orka-system
"$KMX" --context "$AKS_CLUSTER" orka show "$AGENT" --namespace orka-system
```

These are live Orka views. Readiness is not an answer.

## 11. Teardown

```bash
teardown_demo() {
  local cloud_rc=0 local_rc=0
  KAIMAHI_CONFIRM="$RESOURCE_GROUP" \
    "$KMX" aks down \
      --resource-group "$RESOURCE_GROUP" \
      --cluster "$AKS_CLUSTER" || cloud_rc=$?
  KIND_CLUSTER="$LOCAL_CLUSTER" \
    "$KMX" --context "$LOCAL_CONTEXT" local down || local_rc=$?
  [ "$cloud_rc" -eq 0 ] || return "$cloud_rc"
  return "$local_rc"
}

if teardown_demo; then
  unset -f teardown_demo
else
  unset -f teardown_demo
  false
fi
```

Complete AKS teardown removes its infrastructure ownership record; incomplete
cleanup retains that record for retry. It does not delete the workspace,
explicit or managed evaluation evidence, preparation caches, Docker
credential-store entries, or an SSH key pair Azure CLI may have generated.
Review the Azure billing inventory; deleting one resource group is not a
complete subscription billing audit.

## Repeatability

| Stage | Repeat behavior |
|---|---|
| Variables | Existing non-empty values are preserved; all sensitive inputs are checked before setup. |
| Source | Exact source reuses; identity/model drift refuses; no overwrite. |
| Local setup | Exact reruns inspect/reuse the named kind target. |
| Providers | Exact resources reuse; missing keys or drift refuse; no overwrite. |
| Eval | Existing receipt avoids another model call; changed source needs fresh proof. |
| AKS setup | Reruns verify recorded ownership, identities, network policy and AcrPull before reuse; they do not prove every Azure size/SKU field. Interrupted phases print recovery. |
| Push | Same exact digest/tag is a no-op and prints the immutable reference again. |
| Deploy | Plan before first Apply; promoted Apply is one-time; occupied repeats refuse. |
| List/show | Read-only and repeatable. |
| Teardown | Ownership-recorded and confirmation-bound; inspect the final report. |
