# `kmx` — one command for creating and running agents

`kmx` is the developer journey as a single Go binary: a cluster, a local
model, kagent, an agent, a conversation — and then the governance plane, a
governed credential, and the ledger that shows what it spent. It needs no
clone and no Makefile.

It is also the only implementation of that journey. Thirty-seven Makefile
targets are one-line recipes that call this binary on the kind path — the
runtime (`up`, `cluster`, `ollama`, `model`, `kagent`, `agent`,
`tools-agent`, `chat`, `status`, `down`), the plane (`plane`, `plane-image`,
`plane-secrets`, `govern`), the reads (`ledger`, `grants`, `tool-audit`,
`approval-audit`, `approvals`, `tool-allowlist`, `plane-metrics`,
`backup`), and the operator verbs (`use`, `use-ollama`, `budget`,
`approve`, `deny`, `request`, `govern-tools`, `ungovern-tools`,
`tool-allow`, `restore`, `credentials`, `credential-renew`) and the
credential capture (`github-secret`,
`release-secret`, `ado-secret`) — so CI proves the code you actually run.
What is left in the Makefile is the Slack and inbound connector families;
the agents this repository wires from committed manifests — the release
agent, the accounts-payable demo and the hosted-GitHub agent, whose
`Agent` and `RemoteMCPServer` documents `kmx` does not carry
([workflows.md](workflows.md) has the checkout table); the model-key
capture; and the network probes. The managed-cluster path is `kmx lift`;
the Makefile's `TARGET=aks` targets still exist and do the same work step
by step.

**Status.** v0.1.0 is released ([releases.md](releases.md)); no
package-manager namespace is claimed. `kmx` is a provisional name,
like `kaimahi` itself, and is not claimed anywhere
([NAMING.md](NAMING.md)).

**Local unless you say otherwise.** Everything below assumes a local kind
cluster, except `kmx lift`, which puts the same agent on AKS: it builds the
image in a private registry, renders the manifest for it, and wires
Azure-managed monitoring ([aks.md](aks.md)). The Makefile's `TARGET=aks`
path still exists and does the same work step by step.

The one thing on that path kmx does **not** do is capture the model
credential. A managed cluster runs a hosted model, and the model key is not
one of the upstream credentials `kmx credential capture` knows how to check —
storing a credential it cannot vet is the thing that path exists to avoid.
`kmx lift` checks for the Secret, stops if it is missing, and names
`make plane-copilot-secret`, which needs a checkout. That is the only step
that still does.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
```

The script works out your platform, downloads that release binary, **checks it
against the release's published sha256 before installing it**, and puts it in
`~/.local/bin` — no sudo, and nothing outside your home directory.
`--quickstart` carries straight on into `kmx quickstart`; `--version=v0.1.0`
pins a version; `--bin-dir=DIR` installs elsewhere.

Stated rather than implied: the binary and its checksum come from the same
GitHub release over TLS, so this proves the download was not corrupted or
truncated. It is not an independent signature. `go install` is the other
route, and it goes through the Go module proxy and the Go checksum database
instead:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest
```

`@latest` is the newest tagged release; `@v0.1.0` pins one. No Homebrew tap,
no npm, crates or PyPI package — the Go module proxy and its checksum
database are the whole distribution, and no namespace of ours is claimed
(see [NAMING.md](NAMING.md)).

Without a Go toolchain, each release also carries checksum-verified binaries
for linux and macOS on amd64 and arm64. The download, the version scheme and
the upgrade path — including what happens when a migration fails — are in
[releases.md](releases.md).

From a clone, `make bin/kmx` builds the same binary and every `make` target
below uses it.

Plain `make` is build-only and prints the resulting binary path. It never
creates or changes a cluster; provisioning requires the explicit command:

```bash
make       # build bin/kmx
make up    # build if stale, then create/update the local runtime
```

| Prerequisite | Why |
|---|---|
| Docker **or** Podman | kind runs Kubernetes in containers. **The only thing you must install.** |
| kind, kubectl, Helm | kmx downloads them if the machine has none, pinned and checksum-verified; a copy already on PATH is preferred and never shadowed |
| Go 1.26+ | only for the two commands that build the plane's image — `kmx plane` (and then only outside a checkout, where it fetches the source from the Go module proxy) and `kmx lift` (whenever its plane phase runs) — and for `go install` |

kmx has always fetched the pinned kagent CLI itself, checksum-verified, the
first time you chat. The cluster tools now work the same way: pinned
versions, the publisher's own sha256 file, the digest re-checked on every
later use rather than only at download — because "checksum-verified" has to
mean the bytes about to run with your kubeconfig, not the bytes that arrived
some other day. They land in `~/.config/kmx/bin`, and `~/.config/kmx/path`
holds the plain-named symlinks kmx puts on PATH for the length of one
command.

`KMX_TOOLCHAIN=off` turns the fetching off entirely: a missing tool goes back
to being an error that names its install page. A clone uses the same verified
kmx cache unless `KAGENT=<path>` is explicitly supplied; the Makefile's
`bin/kagent` remains for legacy action-oriented helpers such as `slack-post`.

## The journey

```bash
kmx up                                   # kind + Ollama + the model + kagent + two agents
kmx agent chat hello-world "Who are you?"

kmx plane                                # the governance plane: proxy + Postgres ledger
kmx govern hello-world                   # issue the credential, put the agent behind it
kmx agent chat hello-world "Who are you?"  # the same question, now metered
kmx ledger                               # what it cost

kmx status
kmx down
```

Once the plane is up, the verbs an operator reaches for:

```bash
kmx budget hello-world --tokens 200000   # the cap it spends under
kmx tools govern --tools k8s_get_resources   # the tools agent, behind the gateway
kmx approvals                            # what is waiting for a human, and the CALL each is about
kmx approve <id> --ttl 10m --uses 1      # bounded, or it is a config change
kmx backup                               # the ledger and the audit trails, to a local file
kmx metrics                              # one replica's Prometheus exposition
```

Between the two chats nothing about the agent changed except which model
preset it thinks through. That is the whole point: governance is a preset
swap plus a credential the agent cannot read past.

## Commands

| Command | What it does |
|---|---|
| `kmx ctx` | print the context kmx will act on, where that came from, and its posture |
| `kmx ctx <context>` | select that context for later commands (recorded in kmx's config directory — `~/.config/kmx/context` on Linux; set `KMX_HOME` to put it elsewhere) |
| `kmx quickstart` | the shortest honest path to a working agent: equip the machine, create the cluster, deploy Ollama and pull the model, install kagent **without the components a first question cannot reach**, deploy one agent, ask it a question and print the answer. It preserves any deployed full or custom kagent release rather than disabling components, installs the minimal profile only after proving the release absent, and refuses unreadable or non-deployed Helm states rather than guessing. A terminal gets a clear six-step view while native command output remains visible; redirected output keeps durable phase lines. `--output json` emits one document on stdout for a machine, and `--task` changes what is asked. Does not enable governance or assess existing governance, and deploys no plane |
| `kmx up` | check all host dependencies in one pass before the guard or first use, create the kind cluster, deploy Ollama, pull the pinned model, install kagent by helm, apply both agents, wait for each to be Ready, print status |
| `kmx up --step <step>` | one step only: `cluster`, `ollama`, `model`, `kagent`, `agent`, `tools-agent` |
| `kmx lift` | the same agent, on AKS: create the resource group, a private registry and a cluster with a policy engine, **prove the boundary is enforced before putting anything behind it**, install the runtime, the plane and the agents, wire Azure-managed monitoring, then check the agent answers and that its metrics and logs actually arrived. Names what it will do and where, and refuses without confirmation naming the cluster. Bills money until `kmx lift down` ([aks.md](aks.md)) |
| `kmx lift --byo` | the same, onto a cluster you already have. **Your cluster and resource group are never created, deleted or adopted**; it refuses to install if the cluster has no NetworkPolicy engine, never touches the cluster-wide scrape ConfigMap, and refuses to grant itself `AcrPull` |
| `kmx lift --plan` | print what would be created, where, and stop |
| `kmx lift --step <step>` | one phase only — every phase is re-runnable, so a failure is resumed rather than unpicked: `cluster`, `boundary`, `kagent`, `credential`, `plane`, `agents`, `observability`, `verify` |
| `kmx lift down` | remove what the lift created. On a cluster it created, the whole resource group, proven gone. On yours, only the resources it recorded the id of, deleted by that id and never by name — anything it cannot prove is its own is left alone and named |
| `kmx agent list [-o table\|json\|yaml]` | list agents with readiness, acceptance, active ModelConfig, and tool-server wiring |
| `kmx agent create [<name>]` | create an Orka Provider and Agent, optionally run a Task; explicit namespace, Provider type, model ID and Secret required (wizard prompts when omitted) |
| `kmx agent edit <name> [--file <path>]` | edit and validate owned local Agent source; never edits the live resource implicitly |
| `kmx agent chat <name> [message]` | ask an agent one question, through `kagent invoke` |
| `kmx agent chat <name> --json` | the raw A2A task instead of the readable one-shot view (piped one-shot output is always raw); refused together with `--interactive` |
| `kmx agent chat --interactive <name>` | live streamed chat in one session; shows active tools, tool calls/results, and supports session history/resume |
| `kmx plane` | build the proxy image, bootstrap the plane's secrets, deploy the plane, wait for it to serve |
| `kmx plane --step <step>` | one step only: `image`, `secrets`, `certificate`, `deploy`. `certificate` mints or renews what the two data seams serve with, and restarts the plane onto it ([operations.md](operations.md)) |
| `kmx plane --source <path>` | build the plane from a checkout instead of fetching it (`-` forces the fetch) |
| `kmx govern [<credential>]` | issue the governed credential (default `$CRED`), apply the governed presets, switch the agent onto one. `--ttl` sets the credential's lifetime; the plane defaults one, and there is no way to ask for "never" |
| `kmx credentials` | the governed credentials and when each one expires, soonest first, with the state an operator scans: `EXPIRED`, `EXPIRING`, `ok`, or `no expiry` (the legacy class) ([identity.md](identity.md)) |
| `kmx credential renew <name> [--ttl 720h]` | extend a credential's deadline. It moves a **date**, not material: the token does not change, so no Secret is rewritten and no credential bytes travel. Rotating the token is still `kmx govern` |
| `kmx credential capture <upstream> <repository\|organization>` | store the credential an upstream needs, in plane custody. `github` and `github-release` take `owner/name`, `ado` takes an organization. The value is **typed at a prompt with the echo off**: there is no flag, environment variable or file that takes it, and a pipe or a redirect is refused — a credential that can arrive through a pipe can arrive from a shell history or a CI log. It is checked against the upstream first (nothing is stored if that fails) and written straight into the Secret the gateway reads; it never reaches argv, a file or a log. An upstream that already has one is refused unless `--replace` |
| `kmx ledger [<credential>]` | the spend ledger, newest first, plus month-to-date totals. The last column is `acted for`: who the call was made for. The two before it say who *called* — `caller (claimed)`, the client's own unverified word for itself, and `from (observed)`, the address the plane saw |
| `kmx grants [<credential>]` | grants, with liveness — an expired grant is not a grant |
| `kmx audit tool\|approval [<cred>]` | the enforcement points' audit trails. The tool trail carries the same two caller columns the ledger does |
| `kmx flow [<credential>]` | the ledger, the tool audit, the approval audit and the inbound audit as one chronological reading, oldest first — what triggered a run, what it spent, what it called, what it was refused and what a human let through. Defaults to every credential. It is a **timeline, not a trace**: the four trails share only the credential and the timestamp, so rows are ordered by time and never linked causally, and it says so under every rendering |
| `kmx use <preset>` | switch an agent onto a preset from `k8s/models/` (`--agent`, default `hello-world`); waits until exactly one pod is on the new template |
| `kmx budget [<credential>] [--cents n\|-] [--tokens n\|-]` | replace the monthly caps. No flags **clears** both — the same as `make budget` with no `CAP_*` |
| `kmx approvals` | the requests waiting for a decision, each with the CALL it is about |
| `kmx approve <id> [--ttl 10m] [--uses 1] [--amount n]` | mint the bounded grant. At least one of `--ttl`/`--uses` is required — an unbounded grant is a config change, not an approval |
| `kmx deny <id>` | refuse a pending request |
| `kmx request <tool\|budget\|inbound> <subject>` | file one explicitly. `--args '<json>'` (tool requests only) names the CALL to pre-approve; omitting it means the **argument-less** call, never "any call" |
| `kmx tools add <name>` | onboard **your own** MCP server as a governed upstream: scaffold the table entry, the NetworkPolicy pair and the gateway seam as reviewable YAML, validate them against the running plane, apply (`--url`, `--tool`, `--server-egress`, `--pod-port`, `--secret`, `--out`, `--no-apply`, `--dry-run`) |
| `kmx models add <name>` | onboard **your own** model endpoint as a governed upstream: scaffold the table entry and the NetworkPolicy pair as reviewable YAML, validate them against the running plane, apply (`--url`, `--classification`, `--protocol`, `--server-egress`, `--pod-port`, `--out`, `--no-apply`, `--dry-run`) |
| `kmx orka install` | install [Orka](https://github.com/orka-agents/orka) on the cluster kmx is pointed at: fetch their `deploy/orka.yaml` at the pinned tag and **refuse bytes that do not hash to the digest kmx pins**, create the `harness-wrapper-auth` Secret their own instructions ask an operator to make by hand — before the manifest, because the wrapper mounts it at start — apply their installer unmodified, wait for both Deployments, and create a keyless `Provider` at the in-cluster model server so their fourth prerequisite (an API key) is not one (`--provider`, `--model`, `--model-url`, `--no-apply`, `--dry-run`; [orka.md](orka.md)). Installing governs nothing: `kmx migrate` does |
| `kmx orka status` | what is installed and what it can resolve — the version **running** (read off the controller's image, and named as a skew when it disagrees with the pin), both Deployments, the CRD count, and the `Provider` list, with "none — a model call would be refused" said rather than left as an empty column. An unreadable cluster is reported as unread, never as absent |
| `kmx migrate <deployment>` | put an application **you did not write** onto Orka with its model traffic authenticated, Provider-scoped and recorded, and without changing the application: read what the workload reads today, refuse a model Orka has no ready `Provider` for, create the identity the seam presents to Orka and the seam's allowance for your namespace, mint both credentials into Secrets through a pipe, and write the four environment variables and one mounted file as a patch **kmx does not apply** — it does not mutate a Deployment this project does not own (`--namespace`, `--model`, `--container`, `--upstream`, `--credential`, `--secret`, `--orka-namespace`, `--service-account`, `--token-duration`, `--base-url-var`, `--key-var`, `--model-var`, `--out`, `--no-apply`, `--dry-run`; [migrate.md](migrate.md)) |
| `kmx tools govern` | issue the gateway credential, set the allowlist, wire the governed `RemoteMCPServer`, repoint the agent (`--tools`, `--credential`, `--agent`, `--secret`, `--server`). It APPLIES the committed seam (`kaimahi-tools`); a seam scaffolded by `kmx tools add` is the operator's file, so it is not re-applied here — if you used `--no-apply` or `--dry-run`, apply it first, and kmx says so rather than waiting on an object that is not there. On a cluster with no kagent there is no seam to accept and no Agent to repoint, so it writes the credential, the allowlist and the authority and stops there |
| `kmx tools sidecar <upstream>` | scaffold the credential shim for a client that cannot set a header: an in-pod reverse proxy that presents the token from the Secret the plane wrote (`--deployment`, `--namespace`, `--secret`, `--out`, `--no-apply`). kmx applies the config; the Deployment patch is yours to apply |
| `kmx tools allow <tool,tool\|->` | replace the allowlist. `-` is the **empty** allowlist: nothing callable without a live grant |
| `kmx tools allowlist [<credential>]` | read it back, sorted |
| `kmx tools ungovern` | put the agent back on the ungoverned tool server |
| `kmx backup [<file>]` | `pg_dump` the plane's database to a local file (default `backups/kaimahi-<UTC>.sql`, mode 0600) |
| `kmx restore <file>` | **replace** the plane's database from a backup — every table dropped and recreated |
| `kmx metrics [--pod <name>]` | one proxy replica's Prometheus exposition; the replica's name goes to stderr so stdout stays machine-readable |
| `kmx status` | grouped context, agent/model wiring, runtime health, restarts, how much of the system is governed, and next actions |
| `kmx status -o json\|yaml` | the same document for automation: `context`, `contextSource`, a `governance` block, and the kubectl objects verbatim under `items`. **Changed:** the top-level kubectl `apiVersion`/`kind` are gone, so this can no longer be piped into `kubectl apply`; `jq '.items[]'` is unchanged |
| `kmx down` | delete the kind cluster kmx created |
| `kmx completion bash\|zsh\|fish` | print shell completion for commands, flags, fixed values, kube contexts, and live agent names |
| `kmx version` | the pinned kagent and model versions, the plane's image tag, and the revision `kmx plane` would fetch it at |

Quickstart reports success only for a completed task with a readable answer.
Its JSON key set is unchanged; `governed: false` describes this invocation, not
cluster state. A rerun can preserve existing governance. See the
[result example](getting-started.md#one-command-and-an-agent-that-answers).

Quickstart preserves any deployed kagent application release, not just one with
recognizable full-profile values. It checks the controller rollout without
upgrading that release. A valid empty listing alone permits the first-answer
profile via `helm install`; a concurrent install fails rather than being
overwritten. Other release states, invalid identities/shapes, and query failures
are refused. The query explicitly selects `--deployed --failed --pending
--superseded --uninstalling --uninstalled`, which works with Helm 3 and 4 without
the removed Helm 4 `--all` flag. This is release preservation, not a read-only
quickstart: the other setup steps still reconcile, and a new application install
uses the shared CRD upgrade/install step. `kmx up` explicitly upgrades/installs
the full application profile. Both new minimal installs and full-profile
upgrades wait with `--wait --wait-for-jobs --timeout 420s` rather than waiting on
every pod in the namespace, which may include unrelated agents.

`kmx workflow show` succeeds with parameter help when binding problems are only
missing values, including a partially supplied valid set. Unknown keys, invalid
typed values, pattern failures, or invalid computed defaults return an error,
even when other required values are also missing. The successful human layout
and embedded JSON remain unchanged; there is still no structured show mode.

Enable completion for the current shell:

```bash
# Bash
source <(kmx completion bash)

# Zsh
source <(kmx completion zsh)

# Fish
kmx completion fish | source
```

Completion queries are read-only and side-effect-free: they never run guards,
downloads, port-forwards, or mutations. Static command/flag completion works
offline. Kube-context and agent-name completion use bounded read-only `kubectl`
queries and quietly fall back when kubectl or the selected cluster is unavailable.
Commands, nested help pages, flag definitions, and generated shell completion
come from the same Cobra command tree, so those surfaces cannot drift apart.
This is shell completion for `kmx ...`. Interactive chat also provides local,
network-free slash-command IntelliSense on capable terminals: typing `/` shows
the available commands, each additional character narrows the list through a
prefix trie, and Tab completes a unique or common prefix. `NO_COLOR`,
`TERM=dumb`, redirected input, and pipes retain the ordinary line-input path.
Unavailable raw mode also falls back to a scanner; `--interactive` is not a
TTY-only command. Enhanced editing handles wrapped prompts and grapheme-width
backspace, bounds escape-sequence waits, and restores terminal settings on exit.

### Output contracts

On a capable destination terminal, status, agent list, and admin reports use
rich headings, fields, and counted tables; tables too wide for the terminal
become labeled records. Admin reports include ledger, credentials, pending
approvals, grants, tool/approval audits, and flow; tool allowlists use fields.
Rich views retain full identifiers and call digests where legacy tables truncate
them, with exact numeric values and explicit state-column styling.

Redirected admin output keeps its fixed-width columns, truncation, and empty-case
wording for existing parsers. `TERM=dumb` selects plain presentation. A non-empty
`NO_COLOR` removes ANSI but retains static rich layout on a capable terminal;
chat separately disables cursor effects and uses ordinary line input under it.
JSON/YAML, manifest stdout, metrics, completion, backup SQL, and one-shot raw chat
bypass human styling. Progress and diagnostics stay on stderr.

Plain compatibility does not freeze incorrect safety claims: unknown status and
sandbox reads, readiness verdicts, flow refusal totals, setup summaries, and
recovery commands intentionally change in both modes. Flow counts a model
refusal from `cost_source: denied`, not an upstream HTTP error alone. Admin
JSON/YAML and a structured `workflow show` are still unimplemented; the wizard
and uncommon operator paths have not had a comprehensive rich presentation pass.
See [cli-ux-plan.md](cli-ux-plan.md) for the audit and remaining scope.

One-shot `kmx agent chat` prints two different shapes on purpose. A terminal gets the
reply, any tools the agent called, and the token cost. A pipe gets the raw
A2A task, byte for byte — because things parse it: CI captures this output
and `scripts/verify-chat.py` asserts on `status.state`, the
`function_call` and the `function_response` payload. `--json` forces the raw
form when a terminal wants it. If the output is not a task kmx recognises —
a transport error, a usage message — it prints what `kagent` printed rather
than guessing at a shape that is not there. `--interactive --json` is refused
before application loading rather than silently choosing one format.

Reading, updating and deleting agents are not kmx's job — kubectl and the
kagent CLI already do them. `kmx agent list` is the one read kmx does
carry, because it joins readiness, acceptance, the active ModelConfig and
the tool wiring into one table; it prints that table and nothing else, so
the kubectl commands for update and delete are here rather than in its
output:

```bash
kubectl --context <context> -n kagent get agents.kagent.dev <name> -o yaml  # read
kubectl --context <context> -n kagent edit agents.kagent.dev <name>         # update
kubectl --context <context> -n kagent delete agents.kagent.dev <name>       # delete
```

Scaffolding is the only letter of CRUD with a real gap
([CLI-PROPOSAL.md](CLI-PROPOSAL.md) is the survey that established that).

## Settings

kmx reads the names this repository already uses — the Makefile's, and
`ADMIN_PORT` from `scripts/plane-admin.sh` — with the same defaults, so
`KIND_CLUSTER=mine make up` and `KIND_CLUSTER=mine kmx up` are the same run.

| Variable | Default | Meaning |
|---|---|---|
| `KIND_CLUSTER` | `kaimahi-p1` | the cluster `up` creates and `down` deletes |
| `KUBE_CTX` | `kind-$KIND_CLUSTER` | the context to act on. With nothing set anywhere the name still falls back to `kind-kaimahi-p1`, but that is a name nobody chose and mutating commands refuse it — see [Where the command will land](#where-the-command-will-land) |
| `CONTAINER_ENGINE` | `docker` | `docker` or `podman` (sets `KIND_EXPERIMENTAL_PROVIDER`) |
| `KAGENT_VERSION` | `0.9.12` | pinned kagent chart **and** CLI |
| `MODEL` | `qwen2.5:3b` | model pulled into Ollama |
| `CHAT_PORT` | automatic | local port for the controller forward; set a number for deterministic automation |
| `ADMIN_PORT` | `19091` | local port for the plane's admin forward |
| `OPS_PORT` | `19092` | local port for a replica's metrics forward |
| `CRED` | `hello-world` | the credential `govern` issues, and the one `ledger` and `budget` read/write by default (`grants` and `audit` default to **all** credentials) |
| `CRED_TOOLS` | `hello-tools` | the credential the MCP gateway admits — what `kmx tools` acts on, and what a `tool` request is filed against |
| `KAIMAHI_CONFIRM` | unset | confirm a context the guard will not proceed on by itself, by name. That is a non-kind context, and — for `kmx down` only — a `kind-*` cluster the container engine lists but the kubeconfig does not describe, which is how a half-created cluster is removed |
| `KMX_HOME` | `~/.config/kmx` | where the selected context and the cached kagent binary live |

`--context <name>` overrides `KUBE_CTX` for one command.

## Where the command will land

Every command that changes a cluster or the plane prints where it is about
to act, and refuses anything that is not a local kind cluster without an
explicit confirmation naming the context. That includes `kmx workflow run`,
which files approval requests and performs the calls a human approves, and
`kmx credential renew`, which moves an expiry. The read-only views —
`ledger`, `grants`, `flow`, `audit`, `credentials`, `status` — do not print
a banner: they land wherever the invocation was already going and change
nothing when they get there.

One command is guarded differently, and it is named here rather than
covered by the sentence above: `kmx lift down` deletes Azure resources,
identified by resource group rather than by a kube context, so it prints its
own banner listing what it created. Created-cluster teardown confirms the
**resource group**; `--byo` teardown confirms the **cluster**. Consent precedes
every deletion, including recorded resources outside the group. Incomplete
in-cluster cleanup retains the recovery record. It refuses unattended just as
the context guard does, and does not claim that unrecorded resources or all
subscription billing were checked. BYO teardown removes recorded monitoring,
not the agents or governance plane; unknown ownership is left unchanged.

Confirmation/recovery commands preserve the selected target and relevant
invocation flags with shell-safe argument quoting. Kind-specific creation and
image loading also require `--context`/`KUBE_CTX` to equal `kind-$KIND_CLUSTER`;
confirmation cannot waive a mismatch between the container cluster and context.

The banner:

```
----------------------------------------------------------------
  about to: bring up the kmx runtime (kind, Ollama, kagent, agents)
  context:  kind-kaimahi-p1
  chosen by: KIND_CLUSTER
  server:   127.0.0.1
  namespace(s): kagent, kaimahi, ollama
  posture:  local kind
----------------------------------------------------------------
```

The banner goes to **stderr**, so redirecting or piping a command's output
does not take it with them, and it is printed whether or not anything is
asked.

`chosen by` is the load-bearing line. Every source above is somebody's
decision except one: with nothing set anywhere, the name falls back to
`kind-kaimahi-p1`, which nobody picked. **kmx refuses to act on that** when
your kubeconfig holds contexts it could have meant instead:

```console
$ kmx up
kube-guard: nothing chose a cluster, so kmx will not act on one.
  It would have used "kind-kaimahi-p1", which is a name kmx made up, and your kubeconfig
  holds 3 context(s) it could have meant instead.
  Your current context is "aks-prod"; kmx does not follow it, because a tool that
  rewrites it (`az aks get-credentials` does) would silently re-aim kmx.
  Nothing was applied. Choose one, and it is remembered:
    kmx ctx <name>            # kubectl config get-contexts lists them
    kmx --context <name> ...  # or just this once
```

Two cases are not that failure, and the default stands in both:

- **A machine with no clusters at all** — there is nothing to confuse the
  made-up name with, so one-command bring-up works. Creating that cluster
  records it, so later bare commands resolve through a real choice.
- **The made-up name is already the context you are pointed at** — kmx would
  act on the same cluster your own `kubectl` would, so nobody is being
  surprised.

**kmx does not follow your current context, deliberately.** A bare `kubectl`
does, and `az aks get-credentials` rewrites it without asking, so a command
meant for kind could quietly aim at a managed cluster. kmx pins an explicit
context on every call instead. The refusal names your current context so the
most likely intended answer is in front of you; choosing it is still yours.

The second exception above is not a hole in that. kmx still acts only on the
name it resolved; where that name and your current context **differ**, the
current one is never substituted for it. A match is corroboration that nobody
is being surprised, not a source kmx reads a target from.

"Local kind" is two independent checks, because a context **name** is
cosmetic — anyone can name a production context `kind-prod`. The substantive
check is the API-server address: kind publishes its API server on loopback.
Both must agree. Anything else needs `KAIMAHI_CONFIRM=<context>` or a typed
confirmation, and a non-interactive shell with neither refuses rather than
guessing. An absent `kind-*` context is admitted as "about to be created" —
that is `kmx up` on an empty machine; an absent context by any other name is
a typo, and typos are what this exists to catch.

### `kmx down` does not take that allowance

`kind delete cluster` deletes by **container** name and never opens the
kubeconfig, so "this context is not in my kubeconfig" is not evidence that
there is nothing to delete — it is a stale or re-pointed `KUBECONFIG`, and
taking the bring-up allowance there deletes a real cluster under a banner
saying it was never created. So `kmx down` asks the container engine first
and refuses what the kubeconfig cannot vouch for:

```console
$ kmx down
kind get clusters
----------------------------------------------------------------
  about to: DELETE the kind cluster "kaimahi-p1"
  context:  kind-kaimahi-p1
  chosen by: KUBE_CTX
  server:   <none yet>
  namespace(s): kagent, kaimahi, ollama
  posture:  kind-named, but this kubeconfig does not describe it
----------------------------------------------------------------
kube-guard: nothing in this kubeconfig describes "kind-kaimahi-p1", so kmx cannot tell whether it is
  the local cluster you mean or another one with the same name, and there is no TTY to ask.
  to proceed:  KAIMAHI_CONFIRM=kind-kaimahi-p1 kmx down
```

Three outcomes, and only the first is new:

- The kubeconfig does not describe the cluster, but the container engine
  has one by that name. Confirm it by name — `KAIMAHI_CONFIRM=<context>`,
  or type the context at the prompt. **This is the half-created case**: a
  `kmx up` that died before the kubeconfig entry was written leaves node
  containers behind, and one confirmation removes them.
- No kind cluster by that name at all: `no kind cluster named "…" — nothing
  to delete`, and nothing is deleted or asked.
- An ordinary local cluster the kubeconfig knows: the banner, no question,
  as before. This is what CI's teardown does, and it stays
  non-interactive.

Long `kmx up` and `kmx plane` runs delimit each logical phase with its position,
outcome, and elapsed time. Native Docker, Helm, kind, kubectl, and Ollama output
continues to stream between those boundaries, so progress remains visible and
failures retain their original diagnostics. Concurrent agent output remains
tagged by lane and is summarized as one parallel phase. Phase markers are
written to stderr and do not add content to stdout; native tools retain their
existing stdout and stderr behavior.

This is [`scripts/kube-guard.sh`](../scripts/kube-guard.sh) ported to Go,
case for case; the script stays for the scripts that still use it, and both
are tested against the same cases.

## `kmx agent create`

**This command now authors Orka, not kagent.** It emits a value-free Secret
skeleton, a same-name Provider and referencing Agent in `core.orka.ai/v1alpha1`,
and optionally a fresh-name Task. Existing `agent chat`, `agent edit` and
`agent list` remain kagent-specific; they are not follow-ups for this bundle.
`quickstart`, `up` and the kagent governance walkthroughs are unchanged.

For a local model, first [install Orka](orka.md#the-command) in the same context
as Ollama. The separate [first-Task example](orka.md#author-an-orka-agent-and-get-an-answer)
creates a fresh Agent named `orka-hello`.
The installer separately provisions `local-provider-key`; create references
that Secret but creates its **own** Provider named `my-agent`, not `local`:

```bash
kmx --context kind-kaimahi-p1 agent create my-agent \
  --namespace orka-system --provider-type openai --model qwen2.5:3b \
  --secret local-provider-key \
  --base-url http://ollama.ollama.svc.cluster.local:11434/v1 \
  --description "Answers short questions."
```

This writes `agents/my-agent.yaml` exclusively and creates Provider → Agent
behind the context guard. **No `--task` means no model response was tested.**

### Inputs and modes

| Flag | Meaning |
|---|---|
| `--namespace <ns>` | required, explicitly choose a namespace the Orka controller watches; never inferred |
| `--provider-type openai\|anthropic` | required; `openai` also covers OpenAI-compatible custom endpoints. Azure OpenAI's separate deployment/version fields are not scaffolded |
| `--model <id>` | required actual Provider model ID, not a kagent ModelConfig or a `provider/model` routing alias |
| `--secret <name>` / `--secret-key <key>` | required existing Secret name in that namespace; key defaults to `api-key`. Neither is a credential value |
| `--base-url <url>` | optional HTTP(S) endpoint; no userinfo, query or fragment. Plain HTTP is useful for local models |
| `--description <text>` | one-line `kaimahi.dev/description` annotation |
| `--instructions <file>` | file whose contents become the Agent system prompt; never put credentials in it |
| `--tools <name,...>` / `--skills <name,...>` | explicit Orka references; not `server:tool`, not an MCP allowlist or translation |
| `--agent-requests-per-minute` / `--provider-requests-per-minute` | optional positive int32 limits; omitted when unset |
| `--agent-tokens-per-minute` / `--provider-tokens-per-minute` | optional positive int64 limits; omitted when unset |
| `--task <prompt>` | optional first AI Task; applying authorizes a model call |
| `--result-service-account <name>` | existing account in the selected namespace, required when applying a Task; kmx creates no account or RBAC |
| `--orka-api-service <name>` / `--result-port <port>` | result API Service (default `orka-api`, port 8080) and free loopback forwarding port (default `19180`) |
| `--out <path>` | exclusive output file (default `agents/<name>.yaml`); `-` writes YAML only to stdout and implies offline |
| `--no-apply` | offline artifact only; no tools, kubeconfig reads or cluster calls |
| `--schema-target v0.1.3\|main` | offline only; defaults to `v0.1.3`, `main` is an immutable fixture snapshot, not a fetch |
| `--dry-run` | installed-schema and strict server admission checks; writes the local artifact, but no cluster writes, token or forward. Tests neither result access nor execution; incompatible with offline modes |

Without a name on an interactive terminal, the inline Bubbles wizard asks for
description, name, and any missing namespace/Provider/model/Secret references.
Supplied native flags are preserved; there is no namespace or model preset
default. When applying `--task`, it also asks for the existing result account.
Enter at the final Apply/Cancel selection creates resources; arrows or Tab
select Cancel, and Escape/Ctrl-C cancel without writing. `TERM=dumb` retains
linear prompts with a Y/n confirmation. Non-interactive use requires a name.
Custom/local endpoints use `--base-url` (also explained in the wizard), never a
model-name inference. Other customization stays in flags.

### Offline is schema validation, not a runtime proof

```bash
kmx agent create preview --namespace orka-system \
  --provider-type openai --model qwen2.5:3b --secret local-provider-key \
  --base-url http://ollama.ollama.svc.cluster.local:11434/v1 \
  --schema-target main --out -
```

All emitted custom-resource fields are checked against the served v1alpha1
OpenAPI schemas; unknown fields are refused rather than silently pruned.
The same validator reads installed CRDs online, with **no fixture fallback**.
Offline fixtures are byte-exact upstream files, with [digests and attribution](../internal/kmx/orkaschema/README.md):

- `v0.1.3`: release commit `b07d42c0b9e52fe511b434827a342b4720f5d422`.
- `main`: snapshot `7c4753c2c68a510112ea2bb25b60a406d9c45686`.

Both accept the ordinary Provider/Agent/Task bundle. Release supports Agent and
Provider `spec.rateLimit`; this main snapshot lacks both, so each of the four
rate flags is refused with the resource and `spec.rateLimit` path. Fields are
never dropped to make output pass. Offline does not evaluate CEL, admission,
controller defaulting, readiness, rate enforcement or model execution.

### Ordered creation, not bulk apply

The Secret document is **metadata-only**, naming an external prerequisite.
It contains no `data`, `stringData`, credential placeholder or embedded prompt.
**Never write that skeleton to the cluster.** Provision its key separately
through your normal secret-management path, without putting values in argv,
source files or this bundle. Online kmx checks key presence without printing it;
it never creates, replaces or merges a Provider Secret.

Online preflight reads installed schemas, refuses local-file and live-resource
collisions, checks the Secret/key, and server-dry-runs each custom document with
strict validation before emitting or creating anything. Task mode also proves
API access first. Writes use **create**, never apply/patch/update:

1. Create the new Provider and wait for Ready for the created UID and current
   generation.
2. Create the referencing Agent and wait for the same identity/generation
   readiness checks.
3. If requested, create its fresh Task once; wait for Succeeded and an available,
   nonblank answer. Check Task UID, spec and generation around result retrieval.

Failure stops the sequence; already-created resources are not rolled back.
A rerun does not adopt or overwrite them. Review partial state before choosing
new names or explicitly deleting resources you own.

For manual use, review and split out **only** the Provider, Agent and optional
Task into separate files. For the local example, run
`kubectl --context kind-kaimahi-p1 -n orka-system create -f provider.yaml`,
check that Provider's UID/generation and current-generation Ready, then
`kubectl --context kind-kaimahi-p1 -n orka-system create -f agent.yaml` and
check likewise, then
`kubectl --context kind-kaimahi-p1 -n orka-system create -f task.yaml`.
Substitute your explicitly chosen context and namespace together. Do not run
`kubectl apply -f agents/<name>.yaml`: the bundle includes a Secret skeleton and
its document order does not express waits. A plain Ready condition without
checking its observed generation is not the same guarantee.

### Task result authority and limits

The [local example](orka.md#author-an-orka-agent-and-get-an-answer) provisions a
dedicated result-reader ServiceAccount separately. Pinned main requires
namespaced `get` on `tasks.core.orka.ai`. **v0.1.3 authenticates result reads but
does not enforce ordinary Task-read RBAC**; that Role does not narrow release
result access. The caller also needs permission to request a token for the
chosen account and establish the port-forward.

kmx requests a temporary ten-minute token and keeps it in memory, using a
context-pinned loopback HTTP forward, not a public endpoint. This is the
account's **full effective authority**, not a result-only token. Discarding it
or closing the forward is not revocation. There is no token flag/env/file input.
The operation is bounded by a five-minute deadline; it does not retry Task
creation after an ambiguous failure. Fresh Task names and UID/spec/generation
checks reduce stale-result risk, but **the API does not bind returned result
bytes to a Kubernetes UID**. Dry-run proves neither authorization nor execution.

### Retired create behavior

`--image`, `--isolation` and `--run-as-user` are removed. This command does not
build or deploy application images, translate ModelConfigs or MCP wiring,
inject governance environment variables, or copy kagent pod hardening.
Creating an Orka Agent is not a governance claim. Keep an application's own
Deployment/chart for image, identity and placement; there is no replacement
application-scaffold command. [Migrate](migrate.md) handles an owned
application Deployment's model seam, not a kagent BYO definition, and does not
create Tasks. Native authoring avoids pretending that an API-group change
translates model, tool and workload dependencies; it is not a permanent ban on
other authoring formats. The [isolation survey](isolation.md) retains the
historical BYO design, not current create instructions.

Input and final YAML still reject known credential shapes; values are safely
encoded and existing files are never overwritten. `hello-world` and
`hello-tools` remain reserved names for the embedded kagent examples.

### Existing kagent editor (not an Orka bundle editor)

`kmx agent edit <name>` treats `agents/<name>.yaml` as the source of truth and
opens a secure temporary copy with `$VISUAL` or `$EDITOR`. It refuses symlinks
and concurrent source changes, rejects key-shaped content, validates the Agent
identity and explicit tool allowlists, and checks referenced ModelConfigs and
RemoteMCPServers before atomically replacing the source. It does not apply the
edit automatically; review the diff and run the printed `kubectl apply` command.
Invalid non-secret candidates are retained at the reported temporary path so
editor work is not lost. For a direct live-resource edit, use `kubectl edit`.

## Interactive chat

```bash
kmx agent chat --interactive hello-tools
# from make: INTERACTIVE=1 make chat AGENT=hello-tools
```

On capable terminals, a compact startup view leads with the agent name and a
subdued context line, followed by model posture and tools. Verified governed
model routing is green; direct model routing is yellow. These labels describe
the model seam, not blanket governance of the agent. Startup shows only a short
input hint; `/help` opens the grouped command reference. Plain output retains
the `CHAT STATUS` report and command list. Both views show effective
selected/discovered tools and descriptions. A governed label
requires current MCP discovery plus ready plane replicas and Service endpoints;
unknown posture refuses rather than claiming governance. Every user message is
labelled `You`; each reply carries the active agent name. Text and correlated
tool call/completion events render as kagent streams them. The returned context
ID is reused for each turn.

Commands: `/help`, `/session`, `/sessions`, `/history`, `/resume <id>`, `/new`, `/retry`,
`/tools off|summary|verbose`, `/govern`, `/ungovern`, `/exit`.
`/govern` gives the active agent a dedicated `kmx-model-<agent>` plane
credential plus an agent-specific Secret and governed Ollama ModelConfig, then
puts only its **model seam** behind the kind plane. This avoids combining model
spend with another agent or with the separate tool-governance credential.
`/ungovern` switches that model seam to an agent-specific direct Ollama
ModelConfig while preserving the active model name. Neither command changes
tool routing, deletes credentials, or erases ledger/grant/audit history.
Both wait for the serving pod switch, print a fresh status header, and clear
`/retry` history so an old message is not silently replayed across a trust
boundary. On a non-kind context, set `KAIMAHI_CONFIRM=<context>` before starting
chat; the slash command will not start a second input reader for confirmation.
They currently support Ollama and an existing Kaimahi Ollama route; other model
providers are refused rather than silently redirected to a different model.
Credential issuance has the same custody boundary as `kmx govern`: the token is
shown only once by the plane and immediately written to its Secret. If that
Secret write fails, the credential can require operator recovery because the
plane currently exposes no token rotation or deletion API.
`--session <id>` resumes a known session and displays its history. `/sessions`
reads the controller's `agent_id` field, accepts direct or wrapped session lists,
and reports empty/null lists as `Sessions: none`; unknown response shapes are
errors, not empty results. History uses the active renderer and closes existing
actor/prompt output before replay and before returning to input. Replayed tool
events with IDs are deduplicated for display; different IDs or changed payloads
remain visible. This is not execution deduplication or a complete audit export.
Malformed history events are still skipped and verbose ordinary tool/history
payloads remain display-limited, unlike native approval inspection.

Received session IDs survive stream failures. A successful `/resume` clears the
old retry message only after history validates the session's agent. If an
interactive stream closes while a task is still working, kmx polls that exact
task ID rather than reinvoking it. A Kaimahi governance denial still requires a
separate operator approval, followed by explicit `/retry`.

Capable terminals color conversational and operational labels without relying
on color alone: `YOU` is cyan, `AGENT (<name>)` is green, tool activity is magenta,
and approval/governance activity is yellow. The rich startup view uses the same
palette; the plain status report remains uncolored.
Messages use actor labels; non-message interactions use trusted bracketed labels
such as `[TOOL CALL]`, `[TOOL RESULT]`, `[NATIVE APPROVAL]`, and
`[KAIMAHI ROUTE]`. Dynamic tool names appear only in their indented fields.
Every payload line is indented, with arguments and
results nested one level further, so model/tool text cannot impersonate a
trusted label. Set `NO_COLOR=1` to disable chat colors/cursor effects and enhanced
input, or `TERM=dumb` for plain presentation. The actor/operation hierarchy remains.

Rich output announces connection and posture checks before waiting, and reports
waiting for task completion or continuing after a native decision. The working
indicator can continue after tool activity, but never clears durable response
text or runs over an input prompt. If its row has reflowed after a resize,
animation is disabled instead of erasing uncertain screen coordinates.
Rich exit notices distinguish explicit exit, closed input, and cancellation;
plain output retains `[CHAT] Status: ended`.

Native approvals and questions show a static details callout above the trusted
interaction label and editable prompt. The callout does not consume input or
truncate approval arguments; the existing decision validation and terminal
editor remain responsible for consent and submission.

If terminal dimensions change during enhanced input, chat stops without
submitting the current message or native approval, cancels the input reader,
and restores terminal settings. It avoids erasing with stale coordinates; it
does not attempt live reflow. Restart chat to continue, using a known session ID
if resuming. This does not undo an earlier submitted turn or decision.

Route checks, tool calls, tool results, and possible governance-denial signals
are actions taken while producing the current assistant turn, so they render as
children of one assistant heading rather than as peer messages:

```text
AGENT (hello-tools)
    [KAIMAHI ROUTE]
      Seam: model proxy
      Configuration: verified through ready plane at chat start

    [TOOL CALL]
      Tool: k8s_get_resources
      Status: running

    [TOOL RESULT]
      Tool: k8s_get_resources
      Status: completed

  | pod-a
  | pod-b
```

Native approval/questions and local `[CHAT]` controls remain top-level because
they interrupt the agent turn and require user or client action.
Trusted child action labels use four spaces and their fields use six. Agent
response text uses the shallower `  | ` rail, preserving authored whitespace
while preventing model text quoting `[TOOL RESULT]` from impersonating a real
tool record in plain output.

Native kagent `requireApproval` pauses keep their answer prompt inside a
`[NATIVE APPROVAL]` interaction and resume with a structured approve/reject
response. `[NATIVE QUESTION]` similarly groups choices and the answer prompt.
Malformed, incomplete, duplicate-ID, or mixed question/approval requests are
refused as a whole before a decision is submitted. Approval arguments over the
16 KiB per-call inspection limit are refused, not silently truncated; accepted
requests display the call ID and full arguments. A batch requires an explicit
decision for every call. Free-text answers retain commas; single-choice answers
must match one offered choice, and multiple-choice answers use comma-separated
values (quote a choice containing commas). Empty/invalid answers are not sent.
Kaimahi route information uses a separate `[KAIMAHI ROUTE]` interaction and
names the affected tool in an indented field when its server route is
unambiguous. Possible denial signals use `[POSSIBLE KAIMAHI DENIAL]`. They
remain a separate security boundary: chat cannot approve its own Kaimahi
request.

When the live serving configuration proves that a model or unambiguous tool
route uses a ready Kaimahi plane, interactive chat emits `[KAIMAHI ROUTE]`
records. These remain visible with `/tools off`. They describe verified startup
configuration, not proof that a particular request reached an enforcement
point. Current kagent streams do not propagate positive decision, grant,
ledger, or audit receipts, so the UI says that explicitly rather than inventing
an `allowed` or `ledgered` result. Failed agent/model responses or correlated
unambiguous tool responses matching the plane's denial vocabulary are marked
`[POSSIBLE KAIMAHI DENIAL]` with unverified provenance; reported approval
filing must still be verified through `kmx approvals`.

### Retry limits

The interactive fixes do not narrow the existing one-shot retry policy. One-shot
chat and quickstart still retry matching controller connection-refused, EOF, and
connection-reset errors up to three times. An EOF/reset can occur after the agent
acted, so retrying a tool-capable turn can repeat effects or spend. An explicit
one-shot `--session` does not disable transport retries. Workflow bounded and
consequential steps use the narrower connection-refused-only policy; read/draft
turns retain the broader policy.

Separately, one-shot question-only `ask_user` resampling remains at most twice,
only without an explicit session and with no recorded tool response or other
pending confirmation. Interactive `/retry` explicitly resends the last message;
it is not an exactly-once guarantee. These policies were not redesigned by the
presentation/HITL audit.

## How the plane gets there without a clone

`kmx plane` has to produce a container image of a Go program whose source is
**not** in the binary. It cannot be: the plane is a separate Go module under
`plane/`, and `go:embed` refuses to cross a module boundary — "cannot embed
directory: in different module". The same nested module is what makes the
answer work.

1. kmx reads **its own revision** out of its build info — the pseudo-version
   a `go install` binary carries, or the VCS revision a checkout build does.
2. It runs `go install
   github.com/kaimahi-agents/kaimahi/plane/cmd/kaimahi-proxy@<that revision>`.
   The module resolves through the public Go proxy at any commit on `main`,
   and Go's checksum database verifies what comes back.
3. It packages that binary onto the same distroless base `plane/Dockerfile`
   uses, and side-loads the image into the kind cluster with `kind load`.
4. The manifests come out of the kmx binary and are applied **as committed**.

Nothing is published to do this: no registry, no release, no tap. The plane
image never leaves your machine.

A checkout always wins. Inside a clone — or with `--source <path>` — kmx
builds `plane/Dockerfile` from the working tree instead, which is what the
Makefile passes (`--source .`) and what keeps CI proving the code a pull
request changes rather than whatever the proxy last published. `--source -`
forces the fetch even inside a checkout.

Which revision is actually running is not inferred from the image tag (the
tag is fixed, because the manifest is applied unrendered). Ask the plane:

```bash
make plane-metrics | grep kaimahi_build_info
```

### If `go install` says it cannot cross-compile

```
go: cannot install cross-compiled binaries when GOBIN is set
```

The plane's binary is built for **Linux** (that is what the kind node runs),
so on macOS every plane build is a cross-compile — and mise, asdf and
`go env -w GOBIN=…` all set `GOBIN`. kmx removes `GOBIN` from the
environment it hands the toolchain, which covers the shell-set case. A
`GOBIN` in Go's own environment file survives that, and kmx says so and
names the fix:

```bash
go env -u GOBIN         # clear it, or
kmx plane --source .    # build the plane from a checkout instead
```

## `kmx tools add`

```bash
kmx tools add warehouse \
  --url http://acme-warehouse.acme:8090/mcp \
  --tool stock_get:sku \
  --tool stock_adjust:sku,delta
```

It writes `upstreams/warehouse.yaml` — four documents that *are* the
onboarding: the gateway's table entry (as an overlay fragment), the
proxy's egress to that server, that server's ingress from the proxy
alone, and the `RemoteMCPServer` whose URL is the gateway. Then it
validates them against the running plane and applies them behind the
guard. The full walkthrough, including what to choose for `policy_fields`
and why, is [govern-your-agent.md](govern-your-agent.md).

**On a cluster with no kagent**, the fourth document is the one nothing
can read — it exists to tell a kagent controller where the seam is — so
it is not applied. The other three are, the proxy is restarted so the
new entry is loaded, and the command exits 0, naming what it skipped.
The file still carries all four: install kagent later and
`kubectl apply -f upstreams/warehouse.yaml` picks the seam up. A cluster
that could not be *asked* whether the CRD is there is an error, never an
absence. See [foreign-runtime.md](foreign-runtime.md).

| Flag | Meaning |
|---|---|
| `--url <url>` | the server's OWN in-cluster endpoint, `http://<service>.<namespace>:<port>/mcp` |
| `--tool <tool>:<fields>` | one per tool. `tool:a,b` declares those fields policy-relevant; `tool:` declares that none are (a verb-level binding — the weakest); `tool:*` declares nothing (the whole-argument-object binding) |
| `--server-egress none\|dns\|keep` | what the scaffolded policy lets the SERVER reach. Default `none` |
| `--pod-port <n>` | the container port, for the one case kmx will not guess: a Service targeting a NAMED port |
| `--secret <name>` | agent-side Secret **name** the seam resolves its credential from (default `kaimahi-<name>-token`) |
| `--out <path>` | where to write it (`-` for stdout) |
| `--no-apply` | write the manifest and stop |
| `--dry-run` | server-side dry run against the live CRDs |

### Safety properties, and why each exists

| Property | Why |
|---|---|
| **This command accepts no credential** — no flag, no environment variable, no file | The same rule as `agent create`. `--secret` names a Secret RESOURCE; `kmx tools govern` is what mints a token into it. The generated document is scanned for key shapes before it is written. |
| **A tool named without a declaration is REFUSED** | `policy_fields` decides what an approval binds to and what the audit says. kmx will not choose it, and prints what each of the three answers costs at the point of choosing. |
| **The weakest setting announces itself in the file** | `policy_fields: []` is a verb-level binding and the shortest thing to type. The manifest carries a `WEAKEST SETTING IN USE` banner naming the tools, so a reviewer sees it too. |
| **The policy pair is read from the live Service** | Its selector is the labels that actually route to those pods, and its resolved `targetPort` is the port they listen on. A policy written against a Service's PUBLISHED port blocks every call while reading as correct — policy is evaluated on the post-NAT pod address. A selector-less Service is refused: a policy pinned to no labels selects the whole namespace. |
| **The committed table is never edited** | Onboarded upstreams live in `kaimahi-upstreams-extra`, merged over `k8s/plane/upstreams.yaml` at boot. `kmx plane` re-applies the committed table and so cannot discard your entry; an overlay that would redefine a committed entry is refused rather than resolved by precedence. |
| **The overlay is emitted WHOLE** | A ConfigMap apply replaces `data`, so a map missing an existing key would silently un-onboard somebody else's server. An overlay read that is anything but a genuine `NotFound` aborts rather than reading as "nothing is onboarded". |
| **Validated by the plane, not by a copy of it** | The candidate table goes to `POST /admin/config/validate`, which merges it over the committed one and calls the same `config.Parse` the proxy booted with. Nothing is written or applied until it says yes, and its refusal is the plane's own message. |
| **What that validation does and does not cover** | It is the TABLE: the URL shape, the `policy_fields` declarations, the constraint rules, the custody exclusions. The `NetworkPolicy` and `RemoteMCPServer` documents are checked only by the Kubernetes API at apply (`--dry-run` does that early). Their content is derived from the live Service, so the thing to read before applying is the pod selector — kmx prints the pods it will govern, and a shared selector governs all of them. |
| **`--out -` mutates nothing** | Generate-don't-mutate, as `agent create` has it. Validation still runs: it is a read. |
| **An overlay may not carry custody** | `credential_file`, `credential_header`, `internet`, `ca_file` and `extra_headers` — five fields — are refused in an overlay fragment, by the plane, not just by kmx. The first four name any path the proxy can read and any host it may be sent to: a ConfigMap that could set them would hand the plane's admin token to an attacker on the first relayed call. `extra_headers` decides what the proxy SENDS under a credential it holds, which on a keyless in-cluster server would let an overlay forge whatever header that server trusts. Keyed and hosted upstreams stay in the committed table. |
| **The apply is conditional** | The emitted ConfigMap carries the `resourceVersion` it was read at, so a manifest applied later (`--no-apply` invites exactly that) fails with a `Conflict` rather than pruning a fragment somebody added in the meantime — which would leave the upstream that fragment constrained running unbounded. |
| **A shared Service selector is named, not hidden** | The ingress policy governs every pod the selector matches. kmx lists them, and says plainly when there is more than one. |
| **Won't overwrite the manifest** | Exclusive create, no `--force`. This is about the FILE: `kubectl apply` will happily update a same-named `NetworkPolicy` or `RemoteMCPServer` in the cluster. That can happen without anyone doing anything odd — `kubectl apply -f` applies each document independently and does not roll back, so an apply that failed on the ConfigMap leaves the other three behind, and the upstream is then absent from the overlay while its objects exist. The apply output names everything it changed; read it. |

## `kmx models add`

```bash
kmx models add house \
  --url http://vllm.demo:8000/v1/responses \
  --classification free
```

It writes `upstreams/model-house.yaml` — three documents: the proxy's
table entry (as an overlay fragment), the proxy's egress to that
endpoint, and that endpoint's ingress from the proxy alone. Then it
validates them against the running plane and applies them behind the
guard.

**Three documents, not four.** The tool seam's fourth is a
`RemoteMCPServer`, a kagent custom resource. A model's equivalent would
be a `ModelConfig`, also a kagent custom resource — and the adopter this
command exists for has no kagent at all. So the seam's address is
PRINTED instead, with the path a client appends and the certificate it
has to trust, rather than emitted as an object half its users cannot
apply.

**One URL, split.** The table stores a base URL and exactly one
forwarded path separately, and cannot infer the second from the first. So
`--url` takes the whole URL a client posts to, and kmx splits it —
getting that split wrong by hand produces an upstream that loads cleanly
and refuses every call with "path not allowed".

| Flag | Meaning |
|---|---|
| `--url <url>` | the endpoint's OWN in-cluster URL, over plain **http**, **including the path its clients post to** — e.g. `http://<service>.<namespace>:<port>/v1/responses`. An in-cluster endpoint serving TLS needs a trust anchor, and `ca_file` is one of the fields an overlay may not set, so that one is a reviewed entry in the committed table |
| `--classification free\|metered` | required, no default. `free` is an explicit $0; `metered` counts tokens always and costs only where a price is configured |
| `--protocol chat_completions\|responses` | where the meter reads token counts. Needed only when the path names neither; a value that disagrees with its own path is refused |
| `--server-egress none\|dns\|keep` | what the scaffolded policy lets the ENDPOINT reach. Default `none` — a model server that pulls weights at startup needs `keep`, deliberately |
| `--pod-port <n>` | the container port, for the one case kmx will not guess: a Service targeting a NAMED port |
| `--out <path>` | where to write it (`-` for stdout) |
| `--no-apply` | write the manifest and stop |
| `--dry-run` | server-side dry run |

### Safety properties, and why each exists

Everything in [`kmx tools add`'s table](#safety-properties-and-why-each-exists-1)
applies here — the same overlay, the same whole-map emit, the same
`resourceVersion` precondition, the same live-Service read, the same
validation by the plane's own parser. What is different:

| Property | Why |
|---|---|
| **`--classification` has no default** | A $0 by inference is a budget nothing can exhaust. The refusal states what each answer costs. |
| **A protocol is resolved or refused, never guessed** | A path ending `chat/completions` or `responses` IS that protocol. A path naming neither must declare one, and a declaration contradicting its own path is refused rather than resolved — whichever is wrong, the meter would read the wrong field and record zero tokens without saying so. |
| **An overlay may not carry custody OR a price** | The five fields `kmx tools add` refuses, plus `prices`. A price is the multiplier a cents budget is measured with and the one number in the table the plane cannot check. A metered overlay upstream works under a token budget; under a cents budget the priced-pair gate refuses it, which is correct. |
| **This seam has NO allowlist, and the command says so** | A tool upstream is unreachable until a credential allowlists a tool on it. A model upstream is reachable by every credential the plane has issued the moment it is in the table. An operator arriving from `kmx tools add` will assume otherwise, so it is stated before anything is applied. |
| **The plane must be new enough** | The overlay carrying `upstreams` is admin contract 2. An older plane refuses the fragment in its own words, which read like an operator error; kmx asks the plane what it is first and names the version gap instead. |
| **The seam address is printed, not emitted** | See above: no `ModelConfig`, because the adopter may have no kagent. |
## `kmx tools sidecar`

```bash
kmx tools sidecar warehouse --deployment acme-client --namespace acme
```

Some MCP clients cannot be told to send a header — their only
configuration is a URL — and so cannot present a credential at all. This
scaffolds the shim that presents one for them: an in-pod reverse proxy on
loopback that the client posts to, which adds the token from the Secret
the plane wrote and forwards to the gateway over TLS, verifying against
the plane's own authority.

Two files, because they are applied by different people to different
things. kmx applies the ConfigMap holding the shim's config, and
publishes the plane's authority into that namespace. The Deployment
patch it writes and does **not** apply: adding a container to somebody
else's workload is the operator's call. It is a strategic merge whose
container and volume lists merge by name, so applying it twice adds one
container, not two.

| Flag | Meaning |
|---|---|
| `--deployment <name>` | required — the workload running the MCP client, so the patch and the command that applies it name the same object |
| `--namespace <ns>` | where that workload runs (default `kagent`) |
| `--secret <name>` | Secret **name** holding the `kmh_` token (default `kaimahi-<upstream>-token`) |
| `--out <path>` | where to write the ConfigMap; the patch goes beside it as `<path>`.patch.yaml. `-` prints **both** documents to stdout and writes and applies nothing |
| `--no-apply` | write both files and stop |

| Property | Why |
|---|---|
| **This command accepts no credential either** | The generated config carries `Bearer ${KMH}`; `KMH` comes from the Secret through the pod's environment, and nginx's own entrypoint substitutes one into the other before it starts. The token is never in a manifest, a values file or an image. Both documents are scanned for key shapes before they are written. |
| **A query parameter was refused** | It would need no sidecar at all, and a URL is not a place a bearer token may live: it reaches the ingress and load-balancer access logs, the client library's request log, every proxy in between, `kubectl logs` on anything that logs a request line, and shell history. |
| **It proxies one path and returns 404 for everything else** | The shim is a credential, not a route. It listens on loopback inside the pod, so nothing else in the cluster can reach it. |
| **It verifies the seam** | `proxy_ssl_verify on` against the mounted authority, with the name the certificate actually carries. Skipping verification would pay for the whole certificate exercise and buy nothing, while looking identical from outside. |
| **It does not buffer** | The gateway relays SSE; buffering it would hold a streamed tool result until the call had finished, which for a long tool call looks exactly like a hang. |
| **The patch prepends** | A strategic merge puts the shim at `containers[0]`, so after applying it `kubectl logs deploy/<name>` and `kubectl exec deploy/<name>` reach the shim unless you name your own container with `-c`. The patch says so in its own header, because it is the first thing an operator hits afterwards. |
| **It will not start before the plane exists** | nginx resolves the seam's host once, at startup. So a shim added to a pod on a cluster with no plane crash-loops with a message naming the host, rather than starting and answering every call with a 502 — which would read as the gateway being down. Deploy the plane and run `kmx tools govern` first; both are steps before this one anyway. |

## Governing an agent

```bash
kmx plane
kmx govern hello-world
```

`govern` mints an opaque Kaimahi token, stores it as the agent-side Secret
`kaimahi-governed-token`, applies the governed model presets, and switches
the agent onto one. The agent never sees a real upstream key — the plane
keeps those, and stores only the hash of the token it issued.

| Property | Why |
|---|---|
| **Cluster credentials gate it before the admin token does** | The plane's admin port is on no Service. Reaching it takes a `kubectl port-forward` to the pod, so you must already be able to reach the cluster; the admin bearer is the second lock, not the first. |
| **Tokens travel only through pipes** | The admin bearer and the issued token exist in kmx's memory and in the cluster. Neither reaches a file, an argument list, an environment listing or a log — the Secret is rendered in memory and piped into `kubectl apply -f -`. |
| **Only a genuine `NotFound` skips the switch** | An unreachable API server, an expired credential, an RBAC denial and a wrong context all look like "the agent isn't there" if you do not look. Treating them as absence prints a reassuring note, exits 0, and leaves an agent spending **outside** the plane. Anything but a real NotFound aborts. |
| **An already-issued credential is reconciled, never overwritten** | The token is shown exactly once and cannot be recovered. If the Secret is bound to a different credential kmx refuses; if it is missing, kmx tells you how to clear the row and re-issue. |
| **The switch waits for the pods, not the object** | `rollout status` returns while the old pod is still draining, and a question that lands on it gets a plausible answer from the **old** preset. kmx waits until exactly one pod is on the new template. |

Issuance/renewal TTLs must be 60 seconds through 365 days. `kmx govern` supports
the committed `governed-ollama` and `governed-copilot` presets and their fixed
`kagent/kaimahi-governed-token` reference; incompatible preset/Secret overrides
are refused before issuance. For a custom tool server, `tools govern` checks
that the seam exists and has exactly one matching Authorization Secret reference
before issuing or changing the allowlist. `tools ungovern` restores only
`hello-tools`' direct tool selection, preserving model routing and other settings.

Approval bounds are checked before mutation: TTL 1 second through 30 days,
uses 1 through 1,000,000, and amount 1 through 1,000,000,000,000 when set. At
least TTL or uses is required; zero remains valid for a budget, not an approval.

Then:

```bash
kmx agent chat hello-world "Who are you?"
kmx ledger
```

## Backup, restore, and metrics

```bash
kmx backup                       # backups/kaimahi-<UTC>.sql, mode 0600
kmx restore backups/kaimahi-....sql
kmx metrics | grep '^kaimahi_'
```

`pg_dump` and `psql` run **inside** the Postgres pod, over its unix socket,
and the bytes travel through `kubectl exec`. The database password never
leaves the pod, nothing is written to disk in the cluster, and no local
Postgres client is needed.

| Property | Why |
|---|---|
| **A dump with no trailer is not a backup** | `pg_dump` writes its trailer last, so its presence is the completion check. kmx exclusively creates a unique 0600 temporary file in the destination directory and renames only after receiving the trailer; failure removes the temporary file and preserves any previous backup. Existing destinations are warned about before replacement. `restore` checks the same trailer **before** it touches the plane. |
| **The backup is 0600 from the moment it exists** | It holds credential names and token hashes (never a token), the caps, the ledger, the audit trails and the grants. Keep it as you would the database. |
| **`restore` quiesces the plane** | The proxies are scaled to zero, in-flight calls drain, the tables are replaced, and the original replica count is restored. Recovery is attempted even after a failed scale-to-zero request, and recovery errors are reported alongside the original failure; success is not guaranteed. An originally zero-replica plane stays stopped and is reported as not serving. |
| **`restore` is guarded; `backup` is not** | `restore` rewrites the ledger. `backup` is a read, like `ledger`. |
| **`metrics` reads ONE replica** | Each replica carries its own counters, so a merged view would be arithmetic kmx invented. The ops port is on no Service, so this is a port-forward to a **pod** — and only to one that is Ready and not terminating, because a draining pod stays Running and keeps its IP. |

## What is NOT in `kmx`

Deliberately — these stay in the Makefile and the scripts. Most are
entangled with capturing a credential of a kind `kmx credential capture`
cannot vet, and a capture that stores an unchecked value would be worse than
the script it replaced: faster at getting a broken credential into a cluster.

| Not here | Where it is |
|---|---|
| The Slack, GitHub and inbound connector families — everything but the credential capture | [slack.md](slack.md), [hosted-upstreams.md](hosted-upstreams.md), [inbound.md](inbound.md) |
| Capturing a **model** key, a Slack token or an inbound signing key | `make model-secret`, `make copilot-secret`, `make slack-secret`, `make inbound-secret` — those steps stay in standalone scripts. `kmx credential capture` covers the three tool upstreams whose credentials it can prove something about: `github`, `github-release` and `ado` |
| The **model credential** a managed cluster needs | `make plane-copilot-secret`. This is the one hand-off in `kmx lift`, and the one step on that path that still needs a checkout: the lift checks whether the Secret is there and stops if it is not, rather than pretending it can mint one |
| The network and tool probes | `scripts/*-probe.sh` |
| Publishing — a tap, a package manager namespace | nowhere. Settling the name lifted the freeze on publishing, and the first tagged release shipped checksummed binaries; `install.sh` and `go install` are the two install paths, and no npm/crates/PyPI/Homebrew namespace is claimed ([NAMING.md](NAMING.md)) |

`kmx up` says the plane is not deployed, in one line, at the end of a run,
and names the two commands that change that.
