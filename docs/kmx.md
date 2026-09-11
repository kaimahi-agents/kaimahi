# `kmx` — tooling for getting agents onto Orka

[Orka](orka.md) is the platform. Kaimahi's current front door is `kmx orka`
for installation/status, `kmx agent create` for native Provider + Agent authoring,
and `kmx migrate` for an existing application's **model traffic**. The Deployment
remains owner-managed. Installing Orka alone is not this migration; none of these
operations silently governs application tools.

The CLI also contains the existing **legacy kagent/plane implementation** pending
the code transition. Its commands remain documented below because they run
today, not because agent authoring has been settled. Native-Orka-only versus
kagent YAML over Orka remains open; `orka.harness.v2` is outside the direction.
A shrinking compatibility/governance bridge is success, not a reason to rebuild
Orka's platform in Kaimahi.

## Install

The current Orka commands require a development build, with Go 1.26+:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx version
kmx orka --help
```

Ensure Go's binary directory is on PATH. `@main` is a moving development
revision, not a tagged release. From a checkout, `make` builds `bin/kmx` and
prints its path without changing a cluster; use that binary to exercise edits.

The published `v0.1.0` release predates the Orka commands. `go install ...@latest`
and the release installer are **not substitutes** for the development build
above when following the Orka path:

```bash
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
```

That installer puts the selected release in `~/.local/bin`, without sudo;
`--version=v0.1.0` pins it, `--bin-dir=DIR` changes the destination, and
`--quickstart` continues into the legacy quickstart. It checks the binary against
a checksum from the **same** GitHub release over TLS: corruption detection, not
an independent signature. See [releases](releases.md) for platforms and upgrades.

Local kind commands need Docker or Podman. kmx uses kind, kubectl and Helm from
PATH first, otherwise fetches pinned, checksum-verified tools; the pinned kagent
CLI is cached too. Cached digests are rechecked before reuse. Set
`KMX_TOOLCHAIN=off` to refuse missing tools instead. No container engine or Azure
CLI is installed for you. `kmx plane` outside a checkout needs Go to fetch/build
its source; the lift plane phase preflights Go even from a checkout.

## Commands

Use `kmx --help` and `kmx <command> --help` for flags and defaults. The Cobra tree
also generates completion; this guide describes contracts rather than duplicating
every flag. Command definitions are in [`cmd/kmx`](../cmd/kmx).

### Current Orka path

| Command | Contract / reference |
|---|---|
| `kmx orka install` | verify pinned Orka installer bytes; create wrapper-auth Secret before apply; wait for both Deployments; optionally create a keyless Provider. Uses upstream manifests unmodified. [Orka](orka.md) |
| `kmx orka status` | read running controller version, Deployments, CRDs and Providers; distinguish unreadable from absent and running version from pin |
| `kmx agent create [name]` | author native Provider + Agent and optional Task; retrieve a real answer only with `--task`. [Create contract](#kmx-agent-create) |
| `kmx migrate <deployment>` | inspect workload/Provider; create seam identity and ingress; mint/reconcile credentials; write the owner-applied patch. [Migration](migrate.md) |
| `kmx ctx [context]` | show target/source/posture or remember a target in kmx's config directory |

### Existing plane and operator commands

These are the present seam implementation, including the bridge used by migrate.

| Command | Contract / reference |
|---|---|
| `kmx plane` | image, secrets, certificate, deployment; `--step` runs one of those steps; `--source` selects checkout or fetch |
| `kmx credentials` / `kmx credential renew <name>` | list expiries / extend deadline without changing token material. [Identity](identity.md) |
| `kmx credential capture <upstream> <repository\|organization>` | terminal-only verified tool credential capture; see custody below |
| `kmx credential issue <name>` | require exactly one destination: `--secret <name>` (optional namespace/TTL) or `--discard` for signed-hook identity; never print the bearer |
| `kmx models credential copilot` | native device-login/exchange into plane custody; applies egress and restarts an existing proxy |
| `kmx ledger [credential]` | newest model rows plus month-to-date totals; defaults to `$CRED` |
| `kmx flow [credential]` | model/tool/approval/inbound trails, oldest first; all credentials by default; **timeline, not causal trace** |
| `kmx audit tool\|approval [credential]` / `kmx grants [credential]` | trails / grant liveness; all credentials by default |
| `kmx audit inbound [hook]` | inbound audit, optionally filtered by hook rather than credential |
| `kmx budget [credential]` | replace monthly caps; **no cap flags clears both**; `0` is a valid cap |
| `kmx approvals` / `kmx approve <id>` / `kmx deny <id>` | inspect exact calls; grant bounded authority or refuse. [Approvals](approvals.md) |
| `kmx request <tool\|budget\|inbound> <subject>` | file a request; omitted tool `--args` means the argument-less call, not any call |
| `kmx tools add <name>` / `kmx models add <name>` | reviewable upstream/NetworkPolicy onboarding; contracts below |
| `kmx tools sidecar <upstream>` | credential-presenting loopback shim; owner applies the Deployment patch |
| `kmx tools allow <tool,tool\|->` / `kmx tools allowlist [credential]` | replace/read allowlist; `-` allows nothing without a live grant |
| `kmx backup [file]` / `kmx restore <file>` / `kmx metrics` | database backup/replacement / one replica's counters; contracts below |
| `kmx workflow list\|show\|govern\|refresh\|run` | discover/govern/run blueprints or refresh declared seam credentials; [workflows](workflows.md) |
| `kmx completion bash\|zsh\|fish` / `kmx version` | shell completion / binary and dependency versions |

Approval TTL is 1 second–30 days, uses 1–1,000,000, amount
1–1,000,000,000,000 when set; at least TTL or uses is required. Credential
issuance/renewal TTL is 60 seconds–365 days. An unbounded approval is not allowed.
`workflow show` treats only missing bindings as exploratory success; unknown keys,
invalid typed/pattern values or computed defaults fail even with other values
missing. It has no structured show mode. `workflow run --step <name>` is
repeatable to select multiple steps.

### Existing legacy kagent commands

| Command | Current behavior |
|---|---|
| `kmx quickstart` | kind + keyless Ollama + minimal kagent + hello-world + completed answer; no plane/governance enabled. [Getting started](getting-started.md#one-command-and-an-agent-that-answers) |
| `kmx up` | full local kagent profile and both demo agents; `--step` selects cluster, ollama, model, kagent, agent or tools-agent |
| `kmx lift` / `kmx lift down` | AKS legacy kagent/Copilot journey and owned cleanup; selected infrastructure phases support migration. [AKS](aks.md) |
| `kmx agent list` | readiness, acceptance, ModelConfig, tool wiring; table/JSON/YAML |
| `kmx agent show <name>` | one Orka Agent and the chain it depends on: Provider readiness, the Secret the Provider names (**presence only — the value is never read**), the model actually resolved, the tools including disabled ones, and recent Tasks. Requires `--namespace`, because Orka watches namespaces explicitly. An unread hop is reported `unknown`, never as absent (`--namespace`, `--output table\|json`, `--tasks`) |
| `kmx agent edit <name>` | edit owned local kagent source without automatic apply; not an Orka bundle editor |
| `kmx agent chat <name> [message]` | one-shot kagent invocation; `--interactive` for sessions, `--json` for raw one-shot task |
| `kmx govern [credential]` / `kmx use <preset>` | issue/reconcile model credential and switch Agent / explicitly switch preset |
| `kmx tools govern` / `kmx tools ungovern` | credential, allowlist and kagent tool routing / restore hello-tools' direct tools |
| `kmx status` | context, kagent/model wiring, runtime health, governance populations and next actions |
| `kmx down` | delete named kind cluster, **including its ledger** |

`quickstart` creates the minimal application release only after proving absence,
using install so a concurrent release is not overwritten. It reconciles its
recognized first-answer profile; deployed full/custom profiles are preserved and
the controller checked. Unreadable/malformed or non-deployed state refuses. `up` explicitly
upgrades/installs the full profile. Helm waits cover release workloads/jobs, not
all pods in a namespace. These are not read-only operations: other setup steps
still reconcile. Existing non-default model and governed tool routing are
preserved by agent reconciliation; direct routing requires an explicit switch.

## Settings

| Setting | Meaning / default |
|---|---|
| `--context`, `KUBE_CTX`, `kmx ctx` | explicit invocation, environment or remembered target; kmx does not follow changing kubectl current-context |
| `KIND_CLUSTER` | kind container cluster name, default `kaimahi-p1`; pick your own for isolated work |
| `CONTAINER_ENGINE` | `docker` or `podman`; keep consistent for every operation on a cluster |
| `KAGENT_VERSION`, `MODEL` | defaults `0.9.12`, `qwen2.5:3b` for legacy setup |
| `CHAT_PORT`, `ADMIN_PORT`, `OPS_PORT` | automatic chat port; fixed admin `19091`, ops `19092` |
| `CRED`, `CRED_TOOLS` | default model/operator credential `hello-world`, tool credential `hello-tools` |
| `KAIMAHI_CONFIRM` | explicit named-target consent, not a universal yes |
| `KMX_HOME` | kmx state/cache location; otherwise user config directory (`~/.config/kmx` on Linux) |

### Where the command will land

Mutations print context, who chose it, server and namespaces on stderr. Local
kind requires both a kind-shaped name and loopback server; remote mutations
require typed confirmation or `KAIMAHI_CONFIRM=<context>`. Non-interactive runs
without consent refuse. Read-only reports do not prompt.

The implicit `kind-kaimahi-p1` fallback refuses when kubeconfig contains other
possible targets and nobody chose this one. An empty machine, or corroboration
that the fallback is already current, permits first setup. Current-context is
never substituted as kmx's target. Kind creation/image loading also require
context `kind-$KIND_CLUSTER`; confirmation cannot override that mismatch.

`kmx down` checks the container engine because kind deletes by container name,
not kubeconfig. A listed kind cluster missing from kubeconfig requires explicit
named confirmation; no matching container cluster is a no-op. An absent context
is not proof there is nothing to delete.

Lift has cloud-specific consent: creation/BYO lift confirms cluster; created
teardown confirms **resource group**, BYO teardown confirms **cluster**. Consent
precedes deletion, including recorded resources outside the group. BYO removes
recorded monitoring, not agents/plane; unknown ownership is left alone. Read
[AKS ownership and teardown](aks.md#teardown), not just the exit status.

## Output contracts

- On capable terminals, reports use rich headings and responsive tables. Wide
  tables become labelled records. Full identifiers/digests and exact numbers
  remain available. `TERM=dumb` chooses plain; non-empty `NO_COLOR` removes ANSI
  but keeps static rich report layout. Chat additionally disables cursor effects
  and enhanced input under `NO_COLOR`.
- Redirected admin reports retain fixed-width/truncated compatibility output.
  Progress/diagnostics go to stderr. JSON/YAML, manifests, metrics, completion,
  SQL and raw chat bypass styling. Admin JSON/YAML is not implemented.
- One-shot chat at a terminal shows answer/tools/usage; a pipe gets raw A2A task
  bytes, as does `--json`. Unrecognized kagent output is not guessed into a task.
  `--interactive --json` is refused.
- Quickstart JSON has keys `ok`, `context`, `cluster`, `agent`, `manifest`,
  `question`, `answer`, `governed`, `tools`, `elapsed_seconds`, `next`. `tools` is
  null when none were provisioned. `governed: false` means **this invocation did
  not enable governance**, not that existing governance is absent. Success
  requires a completed task with a readable answer; stdout is one JSON document.
- `status -o json|yaml` carries `context`, `contextSource`, `governance`, and raw
  kubectl objects under `items`, **not** a Kubernetes List you can apply. Read
  population `state` before counts: unknown reads publish no invented zeroes.
  Model seams count agents; tool seams count RemoteMCPServers; credentials count
  Secret references, not Secret values. Unready installed planes or missing/
  unreadable required governance prevent a human ready verdict.
- Ledger/audit caller claims are unverified client assertions; observed source
  addresses and `acted for` are separate fields. Flow counts model refusals from
  `cost_source: denied`, not from any upstream HTTP error. Configuration posture
  is not proof a specific request crossed a seam.

Completion (`source <(kmx completion bash)`, similarly zsh; fish uses
`kmx completion fish | source`) performs bounded read-only lookups for contexts
and agents, no guard/download/forward/mutation. Static completion works offline.

## `kmx agent create`

**This command authors native Orka, not kagent.** Every bundle contains a new,
same-name Provider and referencing Agent in `core.orka.ai/v1alpha1`, a metadata-only
Secret skeleton, and optionally a fresh Task. It does not install Orka or adopt
the installer's shared Provider. Start with the [first-Task guide](orka.md#author-an-orka-agent-and-get-an-answer)
for a context-pinned local run, separately provisioned result account, and the
release/main authorization and connection limits.

For offline preview, without tools, kubeconfig reads or cluster calls:

```bash
kmx agent create preview --namespace orka-system \
  --provider-type openai --model qwen2.5:3b --secret local-provider-key \
  --base-url http://ollama.ollama.svc.cluster.local:11434/v1 --out -
```

Namespace, Provider type (`openai|anthropic`), actual model ID and existing Secret
name are explicit inputs; `--secret-key` defaults to `api-key`. These are names,
not credential values. `--instructions` reads a system-prompt file; `--tools` and
`--skills` name Orka references, not kagent `server:tool` selections or translated
MCP wiring. Use `kmx agent create --help` for all flags and defaults.

- `--out -` prints YAML only and implies offline; `--no-apply` writes an exclusive
  local artifact only. Default file: `agents/<name>.yaml`. Existing files are
  never overwritten; input and final YAML reject known credential shapes.
- Offline `--schema-target v0.1.3|main` selects [pinned CRD fixtures](../internal/kmx/orkaschema/README.md),
  not a network fetch. Unknown fields refuse; the pinned main snapshot lacks
  Agent/Provider rate limits and refuses those flags rather than dropping fields.
  Offline schema validation is not CEL/admission, readiness or execution proof.
- Online uses installed CRDs with **no fixture fallback**, checks Secret/key
  presence and collisions, and strictly server-dry-runs each custom resource.
  `--dry-run` writes the local artifact but no cluster resources, token or forward;
  it tests neither result access nor execution and cannot be combined with offline modes.
- **Never write the Secret skeleton or bulk-apply the bundle.** Provision the
  referenced key separately through your secret-management path. kmx never creates,
  replaces or merges that Secret. Online writes use create, not apply/patch/update:
  Provider → current-generation Ready → Agent → current-generation Ready → optional
  Task. For manual creation split out only those custom resources and preserve
  that order and the UID/generation readiness checks, using an explicit context
  and namespace. Failures leave partial state; reruns do not adopt or overwrite it.
- `--task` authorizes a model call and requires an existing
  `--result-service-account` in the selected namespace; kmx creates no account or
  RBAC. It creates the Task once and waits for Succeeded plus an actual nonblank
  answer. **Without `--task`, no model response was tested.**
- kmx requests a ten-minute token; **the API server determines its actual TTL**.
  It carries the account's full effective authority, not result-only scope;
  discarding it is not revocation. Release `v0.1.3` does not enforce Task-read RBAC;
  pinned main requires namespaced Task-get. Result bytes are not bound to a UID.
  The context-pinned loopback HTTP forward uses one TCP connection and stops on
  connection/forward loss, never redialing or resubmitting. This trades reconnect
  availability for protection against later local-port reuse; initial connection
  trust is still local. See the [full limits](orka.md#author-an-orka-agent-and-get-an-answer).

No-name terminal use offers a wizard for missing required inputs and explicit
Apply/Cancel; Escape/Ctrl-C cancel without writing. `TERM=dumb` uses linear
prompts. Non-interactive use requires a name. `hello-world` and `hello-tools`
remain reserved for embedded kagent examples.

`--image`, `--isolation` and `--run-as-user` are removed. There is no BYO image
scaffold, ModelConfig/MCP conversion, injected governance or copied kagent pod
hardening. Keep application image/placement/identity in the owner's Deployment;
[migration](migrate.md) routes its model traffic, not a BYO definition. This
native implementation does not settle the open authoring-format decision or
promote the isolated conversion spike to a supported interface.

### Existing kagent editor

`agent chat/edit/list` remain kagent-specific, not follow-ups for an Orka bundle.
`agent edit` edits a secure temporary copy of owned local YAML via `$VISUAL`/
`$EDITOR`; it rejects symlinks, concurrent edits, secrets, invalid identity or
tool wiring, then atomically replaces source. It never implicitly applies.
Invalid non-secret candidates are retained at the reported path. For direct
live-resource operations use kubectl with explicit context/namespace.

## Interactive chat

```bash
kmx agent chat --interactive hello-tools
```

`/help`, `/session`, `/sessions`, `/history`, `/resume <id>`, `/new`, `/retry`,
`/tools off|summary|verbose`, `/govern`, `/ungovern`, `/exit` are local controls.
`--session <id>` resumes history. Scanner input remains supported when raw mode
is unavailable; terminal slash completion is local. History validates the agent,
retains received session IDs after stream failure and deduplicates display events
by ID/payload, not execution. Malformed history can be skipped and ordinary
verbose payloads are display-limited; this is not an audit export.

`/govern` creates an agent-specific model credential/Secret/ModelConfig;
`/ungovern` selects direct Ollama while preserving the model name. Both affect
**only model routing**, support the existing kind/Ollama routes, preserve audit
history, wait for pod switch and clear retry history. Other providers refuse.
Remote contexts require confirmation set before chat, not a second prompt reader.
A failed Secret write after one-time token issuance needs operator recovery.

Trusted actor/action labels and indented payloads prevent tool/model prose from
impersonating controls. `[KAIMAHI ROUTE]` shows verified startup configuration,
not an allowed/ledgered receipt: kagent streams do not carry those receipts.
Possible denial text has unverified provenance; confirm with `kmx approvals`.
These records remain visible with `/tools off`.

Native kagent approvals/questions are a **different boundary**: chat may submit
a structured native decision but cannot approve its own Kaimahi request. It
refuses malformed, duplicate-ID, mixed or incomplete batches before submission;
every call needs explicit consent. Arguments above the 16 KiB inspection limit
are refused, not truncated. Choices are validated; free text preserves commas.

A working-stream disconnect polls the exact task rather than reinvoking it.
Enhanced-input resize stops chat without submitting the current message/decision
and restores terminal state; restart/resume to continue. This is safe abort,
not live reflow or undo of earlier actions. Renderers keep durable response text
and stop uncertain animation after resize.

### Retry limits

One-shot chat/quickstart still retry matching connection-refused, EOF and reset
errors up to three times, **even with an explicit session**. Ambiguous disconnects
can follow an effect: retries may duplicate tools/spend. Bounded/consequential
workflow turns retry only connection-refused; read/draft turns use the broader
policy. Question-only `ask_user` resampling is at most twice without an explicit
session, recorded tool response or another pending confirmation. Interactive
`/retry` explicitly resends; none of this promises exactly-once execution.

## How the plane gets there without a clone

The plane is a nested Go module, so root `go:embed` cannot carry its source.
Outside a checkout, kmx fetches/builds `plane/cmd/kaimahi-proxy` via the Go proxy
at its own revision, packages it on the distroless base and side-loads into kind.
Embedded manifests are applied as committed; no plane image is published.
A checkout wins, `--source <path>` selects one, and `--source -` forces fetch.
Ask `kmx metrics` for `kaimahi_build_info` rather than infer revision from a tag.

The fetched plane build targets Linux. kmx removes shell `GOBIN` for cross-builds;
if Go's environment file still sets it, use `go env -u GOBIN` or a checkout build.
AKS uses ACR instead; see [managed-cluster limitations](aks.md).

## `kmx tools add`

```bash
kmx tools add warehouse --url http://warehouse.demo:8090/mcp \
  --tool stock_get:sku --tool stock_adjust:sku,delta
```

Writes an overlay ConfigMap, proxy egress, server ingress and kagent
RemoteMCPServer, validates the table with the running plane, then applies.
Without kagent, it skips only that fourth document (the file retains it), restarts
the proxy and reports the skip. An unreadable CRD query is not absence.
See [govern your agent](govern-your-agent.md) and `kmx tools add --help`.

Each `--tool` must declare policy fields: `tool:a,b` binds those fields,
`tool:` is the weakest verb-only binding (warned in the artifact), `tool:*`
binds the whole argument object. Service selectors and post-NAT pod ports come
from the live Service; selector-less Services refuse and named target ports need
`--pod-port`. Shared selectors affect every matching pod, which kmx names.
`--server-egress none|dns|keep` defaults to none; choose deliberately.

Overlay safety: generated credentials are references, key shapes refuse,
files use exclusive create. The whole existing overlay is emitted with its
`resourceVersion` so stale apply conflicts instead of pruning concurrent work.
Only genuine NotFound means no overlay. Committed entries cannot be overridden;
`credential_file`, `credential_header`, `internet`, `ca_file`, and `extra_headers`
are forbidden in overlays. These custody-affecting entries remain reviewed code.

The plane validates the **table**, not the generated policies/seam; Kubernetes
checks those at apply/server dry-run. `--out -` mutates nothing but still validates.
`--no-apply` writes only. Multi-document apply is not transactional: a failure
can leave some objects changed. Review output and selectors before trusting it.

## `kmx models add`

```bash
kmx models add house --url http://vllm.demo:8000/v1/responses --classification free
```

The same overlay/live-Service safety applies, but there are three documents and
no kagent ModelConfig: kmx prints the seam address/CA requirements. Supply the
**whole POST URL**, including path, over in-cluster HTTP. TLS/keyed endpoints
need reviewed committed custody configuration. Explicit `free|metered` is
required; protocol is inferred only from recognized paths, otherwise declared,
and conflicting declarations refuse. Models pulling weights may need deliberate
server egress rather than default none.

Model overlays cannot carry `prices`; cents budgets refuse unpriced pairs while
token budgets can meter them. Admin contract 2 is required. **Every plane
credential can access a configured model upstream**: there is no per-credential
model allowlist. Tool allowlist intuition does not apply here.

## `kmx tools sidecar`

```bash
kmx tools sidecar warehouse --deployment client --namespace demo
```

For URL-only MCP clients, generates a loopback reverse proxy that presents the
Secret token over TLS to one gateway path, verifies the plane CA, does not buffer
SSE and returns 404 elsewhere. No token is embedded in YAML or a query string.
kmx applies config/CA; **the owner applies the strategic-merge Deployment patch**.
`--out -` prints both documents without mutation; `--no-apply` writes only.
The patch prepends its container: use `-c` for application logs/exec afterwards.
Deploy plane and tool credential first; nginx resolves its upstream at startup.

## Governing an agent

`kmx govern` remains the legacy kagent model-preset switch. It supports the
committed governed Ollama/Copilot presets and their fixed Secret reference,
refusing incompatible overrides before issuance. `tools govern` separately
changes tool authority/routing; without kagent it creates credential/allowlist/CA
and stops. A custom kagent seam must already exist with one matching
Authorization Secret reference; kmx does not reapply the operator's scaffold.
`tools ungovern` restores only hello-tools' direct tool selection.

Both use cluster access plus the admin bearer on a pod port-forward, not a
public admin Service. Tokens travel in memory/pipes to Secrets. Already-issued
credentials are reconciled, not overwritten; wrong/missing bindings refuse.
An existing Secret with no credential-binding annotation also refuses before issuance.
Only genuine Agent NotFound skips a switch, and switches wait for exactly one
pod on the new template rather than allowing old pods to answer unnoticed.

`credential capture` accepts `github`, `github-release` (owner/repository), and
`ado` (organization): terminal input with echo off, no argv/env/file/pipe input,
upstream validation before storage, and `--replace` required over an existing
Secret. Plane-side Copilot capture instead uses `kmx models credential copilot`:
GitHub device login, a private 0600 OAuth cache, and short-lived token exchange
without reading credential material from stdin. It applies egress and restarts
an existing proxy. [AKS](aks.md#the-credential-handoff) gives the explicit-context
command. Other model-key capture and the separate direct-kagent Copilot capture
remain `make model-secret` and `make copilot-secret`; Slack/inbound keys retain
their checkout helpers.

## Backup, restore, and metrics

`backup` runs pg_dump inside Postgres, no local database client/password exposure.
It writes a unique 0600 temporary file, verifies the dump trailer, then renames;
failure preserves an existing destination. Default: `backups/kaimahi-<UTC>.sql`.
Treat token hashes, budgets and audit history as sensitive database material.

`restore` **replaces all tables**, guarded. It rejects missing trailers before
mutation, scales proxies to zero, loads the dump, and attempts original replica
recovery even after failure. Recovery errors are reported; success is not
promised. An originally stopped plane stays stopped. `metrics` port-forwards
one Ready, non-terminating replica; its name goes to stderr, exposition to stdout.
It does not invent a sum across replicas. See [operations](operations.md).

## What is NOT in `kmx`

Non-native model-key capture, direct-kagent Copilot capture, Slack/inbound keys,
connector-specific helpers, committed demo/first-user agents and network probes
retain checkout paths in [models](models.md), [workflows](workflows.md),
[Slack](slack.md), [inbound](inbound.md), and [hosted upstreams](hosted-upstreams.md).
Plane-side Copilot capture and the full lift no longer need a checkout handoff.
Superseded runtime/admin make shims and shell wrappers have been removed;
use native kmx, with the Makefile only for retained repository helpers.
