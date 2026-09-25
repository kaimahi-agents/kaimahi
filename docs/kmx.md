# `kmx` — tooling for getting agents onto Orka

[Orka](orka.md) is the platform. Kaimahi's current front door is `kmx orka`
for installation/status, `kmx agent create` for native Provider + Agent authoring,
and `kmx migrate` for an existing application's **model traffic**. The Deployment
remains owner-managed. Installing Orka alone is not this migration; none of these
operations silently governs application tools.

The legacy kagent runtime is no longer part of kmx. Its installer steps, its
manifests and its CLI surface are gone — `kmx up --step kagent|agent|tools-agent`
are unknown steps, and `kmx govern`, `kmx use` and `kmx agent edit` were removed
before them. A cluster that still carries that runtime is untouched and is
operated with kubectl. `orka.harness.v2` is outside the direction.
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
`--quickstart` continues into `kmx quickstart`. It checks the binary against
a checksum from the **same** GitHub release over TLS: corruption detection, not
an independent signature. See [releases](releases.md) for platforms and upgrades.

Local kind commands need Docker or Podman. kmx uses kind and kubectl from
PATH first, otherwise fetches pinned, checksum-verified tools. Helm is no
longer fetched or needed: the only thing that used it was the retired kagent
chart install. No kagent CLI is fetched or cached either. Cached digests are
rechecked before reuse. Set
`KMX_TOOLCHAIN=off` to refuse missing tools instead. No container engine or Azure
CLI is installed for you. `kmx plane` outside a checkout needs Go to fetch/build
its source; the lift plane phase preflights Go even from a checkout.

Docker remains the default for compatibility. Podman is a first-class explicit
choice:

```bash
kmx --container-engine podman quickstart
```

The global flag works before or after a subcommand and overrides
`CONTAINER_ENGINE=...`. kmx deliberately does not fall back between live daemons:
Docker and Podman have separate kind inventories and can each own a same-named
cluster, so a silent switch can target the wrong one.

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
| `kmx console` | two-column local/remote workspace for native Orka Agents, with Vim/arrow navigation, agent actions, inference details and slash-command completion; `--demo` uses sample data. [Console guide](interactive-agent-tui-plan.md) |

### Existing plane and operator commands

These are the present seam implementation, including the bridge used by migrate.

| Command | Contract / reference |
|---|---|
| `kmx plane` | image, secrets, certificate, deployment; `--step` runs one of those steps; `--source` selects checkout or fetch |
| `kmx credentials` / `kmx credential renew <name>` | list expiries / extend deadline without changing token material. [Identity](identity.md) |
| `kmx credential issue <name>` | require exactly one destination: `--secret <name>` with the `--namespace <ns>` it lands in (no default — the namespace is named, never guessed) or `--discard` to discard the one-time bearer; never print the bearer. A namespace that does not exist is refused before the token is minted |
| `kmx models credential copilot` | native device-login/exchange into plane custody; applies egress and restarts an existing proxy |
| `kmx ledger [credential]` | newest model rows plus month-to-date totals; defaults to `$CRED` |
| `kmx flow [credential]` | model ledger, oldest first; all credentials by default; **timeline, not causal trace** |
| `kmx watch [credential]` | model ledger rows **as they happen**, appended one line per event, with denials marked. Append-only rather than full-screen, so the scrollback survives and the feed pipes into `grep`. Starts from now — `--replay N` prints recent history first. A failed read prints a gap that says it is **not** an absence of activity, and a watch that cannot recover exits non-zero rather than going quiet (`--interval`, `--limit`, `--for`, `--replay`, `--json`) |
| `kmx budget [credential]` | replace monthly caps; **no cap flags clears both**; `0` is a valid cap |
| `kmx models add <name>` | reviewable model upstream/NetworkPolicy onboarding; contract below |
| `kmx backup [file]` / `kmx restore <file>` / `kmx metrics` | database backup/replacement / one replica's counters; contracts below |
| `kmx completion bash\|zsh\|fish` / `kmx version` | shell completion / binary and dependency versions |

Custom request/approval/grant commands and APIs, including approval audit, are
removed, following tool-governance/capture, workflow, inbound and notification
retirement. Historical SQL/data remain accessible through SQL/backups, not those
APIs. `flow`/`watch` read only the model ledger. Ordinary `budget` and credential
operations remain. Admin contract 5 marks this breaking removal, not compatibility
negotiation: **upgrade CLI and plane together**. See the
[upgrade procedure](operations.md#upgrading-after-approval-retirement), including
old-replica and rollback risks; older installations also need gateway cleanup.

Credential issuance/renewal TTL remains 60 seconds–365 days.

### Runtime commands

| Command | Current behavior |
|---|---|
| `kmx quickstart` | kind + keyless Ollama + pinned Orka + the fixed `hello-world-agent` Orka bundle + a fresh Task with a readable answer; no Helm, no kagent, no plane/governance enabled. [Getting started](getting-started.md#one-command-and-an-agent-that-answers) |
| `kmx quickstart-wizard` | Experimental TUI: author an Orka agent while kind, Ollama/model, and Orka start in the background; then validate, apply, and optionally run its first Task. |
| `kmx up` | the runtime and no agent: cluster, ollama, model, orka. `--step` selects exactly one of those four; the legacy `kagent`, `agent` and `tools-agent` steps are removed and are refused as unknown |
| `kmx aks up` / `kmx aks down` | Provision AKS and land Orka on it, then clean up owned resources. `--payload` defaults to `orka` and is the only payload (no Provider is created); `--payload kagent` is refused as retired, and an existing kagent lift can still be inspected and torn down. The deprecated `kmx lift` / `kmx lift down` still work; `kmx lift` still requires `--payload`. [AKS](aks.md) |
| `kmx agent list` | Orka Agents in one namespace: readiness, Provider and resolved model. `--namespace <ns>` selects it and defaults to `orka-system`; table/JSON/YAML |
| `kmx agent show <name>` | one Orka Agent and the chain it depends on: Provider readiness, the Secret the Provider names (**presence only — the value is never read**), the model actually resolved, the tools including disabled ones, and recent Tasks. Requires `--namespace`, because Orka watches namespaces explicitly. An unread hop is reported `unknown`, never as absent (`--namespace`, `--output table\|json`, `--tasks`) |
| `kmx agent chat --interactive <name>` | interactive Orka session (`--runtime auto\|orka`, `--namespace`, default `orka-system`). Orka chat is a session, so a one-shot invocation is refused and names this command; `--runtime kagent` is refused by name |
| `kmx status` | what Orka has installed on this context and what it can resolve: running version against the kmx pin, deployments, CRDs and Provider readiness. Delegates entirely to `kmx orka status` — same reads, same answer on an unreadable cluster. `-o table` only |
| `kmx down` | delete named kind cluster, **including its ledger** |

`quickstart` reuses an **exact** live match of the Provider and Agent it would
write, and nothing else: a differing spec is somebody's deliberate change, so
it refuses rather than overwrite it, and a half-finished run resumes. Every run
creates a **fresh** Task — reporting an existing completed Task's answer would
turn "the agent answered" into "the agent answered once, some time ago". The
result must be non-blank after sanitisation. These are not read-only
operations: other setup steps still reconcile. Old gateway tool references on
a cluster require an explicit owner decision, not a silent switch to direct
access.

## Settings

| Setting | Meaning / default |
|---|---|
| `--context`, `KUBE_CTX`, `kmx ctx` | explicit invocation, environment or remembered target; kmx does not follow changing kubectl current-context |
| `KIND_CLUSTER` | kind container cluster name, default `kaimahi-p1`; pick your own for isolated work |
| `--container-engine`, `CONTAINER_ENGINE` | `docker` (default) or `podman`; the flag overrides the environment; keep consistent for every operation on a cluster |
| `MODEL` | model the runtime pulls and resolves, default `qwen2.5:3b` |
| `CHAT_PORT`, `ADMIN_PORT`, `OPS_PORT` | automatic chat port; fixed admin `19091`, ops `19092` |
| `CRED` | default model/operator credential `hello-world` |
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
- Chat is a session and has no one-shot form: a non-interactive invocation is
  refused and names the command that works, so there are no task bytes to pipe
  and no `--json` chat output. `--interactive --json` is refused too.
- Quickstart JSON has keys `ok`, `context`, `cluster`, `agent`, `manifest`,
  `question`, `answer`, `governed`, `tools`, `elapsed_seconds`, `next`. `tools` is
  null when none were provisioned. `governed: false` means **this invocation did
  not enable governance**, not that existing governance is absent. Success
  requires a completed task with a readable answer; stdout is one JSON document.
- `status` has no `-o json|yaml`, and refuses them by name rather than printing
  an empty document. The structured output counted kagent Agents and
  ModelConfigs; nothing replaces that count, because `kmx migrate` routes the
  owner's own workloads and kmx cannot enumerate them. Read the cluster
  directly for machine-readable runtime facts.
- Ledger caller claims are unverified client assertions; observed source
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
  `--result-service-account` in the selected namespace; `agent create` creates no
  account or RBAC. (`kmx up --step orka` provisions `orka-result-reader`, whose
  grant is one verb on `tasks.core.orka.ai` in `orka-system`.) It creates the
  Task once and waits for Succeeded plus an actual nonblank
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
prompts. Non-interactive use requires a name. No agent name is reserved: the
embedded examples that occupied `hello-world` and `hello-tools` are gone.

`--image`, `--isolation` and `--run-as-user` are removed. There is no BYO image
scaffold, ModelConfig/MCP conversion, injected governance or copied kagent pod
hardening. Keep application image/placement/identity in the owner's Deployment;
[migration](migrate.md) routes its model traffic, not a BYO definition. This
native implementation does not settle the open authoring-format decision or
promote the isolated conversion spike to a supported interface.

### Editing an agent

There is no `kmx agent edit`. `kmx agent create` writes reviewable YAML you
own, and a live Orka Agent is edited as the Kubernetes resource it is:

```bash
kubectl --context <ctx> -n <namespace> edit agents.core.orka.ai <name>
```

`kmx agent show <name> --namespace <ns>` reads it back with the chain it
depends on.

## Interactive chat

```bash
kmx agent chat --interactive --namespace orka-system hello-world-agent
```

`/help`, `/agent`, `/tools`, `/lift`, `/inference`, `/inference-local`,
`/inference-copilot`, `/inference-foundry`, `/retry`, `/verbose-on`,
`/verbose-off`, `/exit` are local controls. Each Orka turn is a fresh Task, so
there is no resumable server-side session and no `--session`. Scanner input
remains supported when raw mode is unavailable; terminal slash completion is
local. Malformed history can be skipped and ordinary
verbose payloads are display-limited; this is not an audit export.

Trusted actor/action labels and indented payloads prevent tool/model prose from
impersonating controls. `[KAIMAHI ROUTE]` shows verified startup configuration,
not an allowed/ledgered receipt: the stream does not carry those receipts.
Possible denial text has unverified provenance; inspect `kmx ledger` for model
refusals. Cap denials file no approval request; recovery is an operator's deliberate
budget change or the UTC month reset. Direct tool activity has no Kaimahi approval path.
These records remain visible with `/tools off`.

Native approvals/questions are a **different boundary**: chat may still
submit a structured native decision, not a retired Kaimahi approval. It
refuses malformed, duplicate-ID, mixed or incomplete batches before submission;
every call needs explicit consent. Arguments above the 16 KiB inspection limit
are refused, not truncated. Choices are validated; free text preserves commas.

A working-stream disconnect polls the exact task rather than reinvoking it.
Enhanced-input resize stops chat without submitting the current message/decision
and restores terminal state; restart/resume to continue. This is safe abort,
not live reflow or undo of earlier actions. Renderers keep durable response text
and stop uncertain animation after resize.

### Retry limits

There is no one-shot chat transport and no retry policy left to describe: the
legacy runtime's invoke, its connection-refused/EOF/reset retries and its
resumable `--session` went with it. Interactive `/retry` explicitly resends;
that does not promise exactly-once execution.

`kmx quickstart` is Orka and does not participate in that policy. It creates one
Task, polls that Task's result over a single context-pinned connection, and never
resubmits: connection or forward loss ends the wait rather than repeating the
model call. A missing or whitespace-only result remains unavailable and is
polled on that same Task; once a nonblank result arrives, output with no printable
content is refused. Neither case resubmits the Task.

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

## `kmx models add`

```bash
kmx models add house --url http://vllm.demo:8000/v1/responses --classification free
```

Writes three documents: an overlay ConfigMap, proxy egress and server ingress;
no kagent ModelConfig. kmx prints the seam address/CA requirements. Supply the
**whole POST URL**, including path, over in-cluster HTTP. TLS/keyed endpoints
need reviewed committed custody configuration. Explicit `free|metered` is
required; protocol is inferred only from recognized paths, otherwise declared,
and conflicting declarations refuse. Models pulling weights may need deliberate
server egress rather than default none.

Service selectors and post-NAT pod ports come from the live Service;
selector-less Services refuse, and named target ports need `--pod-port`.
Shared selectors affect every matching pod. `--server-egress none|dns|keep`
defaults to none; choose deliberately.

Files use exclusive create and reject credential shapes. The whole existing
overlay carries `resourceVersion` so stale apply conflicts instead of pruning
concurrent work. Only genuine NotFound means no overlay. Committed entries
cannot be overridden. Overlays refuse `credential_file`, `credential_header`,
`internet`, `ca_file`, `extra_headers` and `prices`; these remain reviewed code.
Retired `tool_upstreams` and `standing_constraints` keys refuse even if empty or
null; clean old fragments up deliberately, not by silently discarding them.

The plane validates the table, not the generated policies; Kubernetes checks
those on apply/server dry-run. `--out -` mutates nothing but still validates;
`--no-apply` writes only. Multi-document apply is not transactional and can leave
partial changes. Review selectors and output before trusting the boundary.

Cents budgets refuse unpriced pairs while token budgets can meter them. Admin
contract 2 is required. **Every plane credential can access a configured model
upstream**: there is no per-credential model allowlist.

## Governing model traffic

`kmx govern` has been removed. Put an owner-managed application behind the
plane with `kmx migrate`, or issue a credential into a named destination with
`kmx credential issue <name> --secret <secret> --namespace <ns>`. Both paths
share the same pre-issue binding checks, so neither can overwrite a one-time
token, and both refuse a destination namespace that is blank or absent before
anything is minted — there is no default namespace, because a token issued
into a guessed one cannot be recovered. Legacy preset governance is gone with
the `kagent` lift payload that was its last caller.

It uses cluster access plus the admin bearer on a pod port-forward, not a
public admin Service. Tokens travel in memory/pipes to Secrets. Already-issued
credentials are reconciled, not overwritten; wrong/missing bindings refuse.
An existing Secret with no credential-binding annotation also refuses before issuance.

Tool credential capture is removed. Plane-side model Copilot capture remains
`kmx models credential copilot`: GitHub device login, a private 0600 OAuth cache
and short-lived token exchange without reading credential material from stdin.
It applies egress and restarts an existing proxy. [AKS](aks.md#the-credential-handoff)
gives the explicit-context command. Provider credentials for an onboarded
upstream are `kmx models add`; for plane-side Copilot credentials use
`kmx models credential copilot`. The retired direct-to-Copilot preset's
checkout-only Secret helper is no longer provided.

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

Standalone network probes retain checkout paths in [egress](egress.md).
Plane-side Copilot capture and the full lift do not need a checkout handoff.
The tool gateway, tool-governance/capture commands and [workflow runner](workflows.md)
are retired, not checkout alternatives. Use native kmx, with the Makefile only
for retained repository helpers.
