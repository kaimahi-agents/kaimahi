# `kmx` — one command for creating and running agents

`kmx` is the developer journey as a single Go binary: a cluster, a local
model, kagent, an agent, a conversation — and then the governance plane, a
governed credential, and the ledger that shows what it spent. It needs no
clone and no Makefile.

It is also the only implementation of that journey. Thirty-five Makefile
targets are one-line recipes that call this binary on the kind path — the
runtime (`up`, `cluster`, `ollama`, `model`, `kagent`, `agent`,
`tools-agent`, `chat`, `status`, `down`), the plane (`plane`, `plane-image`,
`plane-secrets`, `govern`), the reads (`ledger`, `grants`, `tool-audit`,
`approval-audit`, `approvals`, `tool-allowlist`, `plane-metrics`,
`backup`), and the operator verbs (`use`, `use-ollama`, `budget`,
`approve`, `deny`, `request`, `govern-tools`, `ungovern-tools`,
`tool-allow`, `restore`) and the credential capture (`github-secret`,
`release-secret`, `ado-secret`) — so CI proves the code you actually run.
What is left in the Makefile is the Slack and inbound connector families,
the model-key capture, AKS and the probes.

**Status: milestone 3.** Nothing is published. `kmx` is a provisional name,
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
| Go 1.26+ | only for `kmx plane` (it builds the plane's image locally) and for `go install` |

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
| `kmx quickstart` | the shortest honest path to a working agent: equip the machine, create the cluster, deploy Ollama and pull the model, install kagent **without the components a first question cannot reach**, deploy one agent, ask it a question and print the answer. `--output json` for a machine, `--agent`/`--task` to change what is asked. Safe to run twice. Deploys no plane, and says so |
| `kmx up` | check all host dependencies in one pass before the guard or first use, create the kind cluster, deploy Ollama, pull the pinned model, install kagent by helm, apply both agents, wait for each to be Ready, print status |
| `kmx up --step <step>` | one step only: `cluster`, `ollama`, `model`, `kagent`, `agent`, `tools-agent` |
| `kmx lift` | the same agent, on AKS: create the resource group, a private registry and a cluster with a policy engine, **prove the boundary is enforced before putting anything behind it**, install the runtime, the plane and the agents, wire Azure-managed monitoring, then check the agent answers and that its metrics and logs actually arrived. Names what it will do and where, and refuses without confirmation naming the cluster. Bills money until `kmx lift down` ([aks.md](aks.md)) |
| `kmx lift --byo` | the same, onto a cluster you already have. **Your cluster and resource group are never created, deleted or adopted**; it refuses to install if the cluster has no NetworkPolicy engine, refuses to overwrite an existing scrape configuration, and refuses to grant itself `AcrPull` |
| `kmx lift --plan` | print what would be created, where, and stop |
| `kmx lift --step <step>` | one phase only — every phase is re-runnable, so a failure is resumed rather than unpicked: `cluster`, `boundary`, `kagent`, `credential`, `plane`, `agents`, `observability`, `verify` |
| `kmx lift down` | remove what the lift created. On a cluster it created, the whole resource group, proven gone. On yours, only the resources it recorded the id of, deleted by that id and never by name — anything it cannot prove is its own is left alone and named |
| `kmx agent list [-o table\|json\|yaml]` | list agents with readiness, acceptance, active ModelConfig, and tool-server wiring |
| `kmx agent create [<name>]` | scaffold an Agent manifest; without a name, run the guided wizard beginning `Describe this agent:` |
| `kmx agent edit <name> [--file <path>]` | edit and validate owned local Agent source; never edits the live resource implicitly |
| `kmx agent chat <name> [message]` | ask an agent one question, through `kagent invoke` |
| `kmx agent chat <name> --json` | the raw A2A task instead of the readable view (piped output is always raw) |
| `kmx agent chat --interactive <name>` | live streamed chat in one session; shows active tools, tool calls/results, and supports session history/resume |
| `kmx plane` | build the proxy image, bootstrap the plane's secrets, deploy the plane, wait for it to serve |
| `kmx plane --step <step>` | one step only: `image`, `secrets`, `deploy` |
| `kmx plane --source <path>` | build the plane from a checkout instead of fetching it (`-` forces the fetch) |
| `kmx govern [<credential>]` | issue the governed credential (default `$CRED`), apply the governed presets, switch the agent onto one. `--ttl` sets the credential's lifetime; the plane defaults one, and there is no way to ask for "never" |
| `kmx credentials` | the governed credentials and when each one expires, soonest first, with the state an operator scans: `EXPIRED`, `EXPIRING`, `ok`, or `no expiry` (the legacy class) ([identity.md](identity.md)) |
| `kmx credential renew <name> [--ttl 720h]` | extend a credential's deadline. It moves a **date**, not material: the token does not change, so no Secret is rewritten and no credential bytes travel. Rotating the token is still `kmx govern` |
| `kmx credential capture <upstream> <repository\|organization>` | store the credential an upstream needs, in plane custody. `github` and `github-release` take `owner/name`, `ado` takes an organization. The value is **typed at a prompt with the echo off**: there is no flag, environment variable or file that takes it, and a pipe or a redirect is refused — a credential that can arrive through a pipe can arrive from a shell history or a CI log. It is checked against the upstream first (nothing is stored if that fails) and written straight into the Secret the gateway reads; it never reaches argv, a file or a log. An upstream that already has one is refused unless `--replace` |
| `kmx ledger [<credential>]` | the spend ledger, newest first, plus month-to-date totals. The last column is `acted for`: who the call was made for |
| `kmx grants [<credential>]` | grants, with liveness — an expired grant is not a grant |
| `kmx audit tool\|approval [<cred>]` | the enforcement points' audit trails |
| `kmx flow [<credential>]` | the ledger, the tool audit, the approval audit and the inbound audit as one chronological reading, oldest first — what triggered a run, what it spent, what it called, what it was refused and what a human let through. Defaults to every credential. It is a **timeline, not a trace**: the four trails share only the credential and the timestamp, so rows are ordered by time and never linked causally, and it says so under every rendering |
| `kmx use <preset>` | switch an agent onto a preset from `k8s/models/` (`--agent`, default `hello-world`); waits until exactly one pod is on the new template |
| `kmx budget [<credential>] [--cents n\|-] [--tokens n\|-]` | replace the monthly caps. No flags **clears** both — the same as `make budget` with no `CAP_*` |
| `kmx approvals` | the requests waiting for a decision, each with the CALL it is about |
| `kmx approve <id> [--ttl 10m] [--uses 1] [--amount n]` | mint the bounded grant. At least one of `--ttl`/`--uses` is required — an unbounded grant is a config change, not an approval |
| `kmx deny <id>` | refuse a pending request |
| `kmx request <tool\|budget\|inbound> <subject>` | file one explicitly. `--args '<json>'` (tool requests only) names the CALL to pre-approve; omitting it means the **argument-less** call, never "any call" |
| `kmx tools add <name>` | onboard **your own** MCP server as a governed upstream: scaffold the table entry, the NetworkPolicy pair and the gateway seam as reviewable YAML, validate them against the running plane, apply (`--url`, `--tool`, `--server-egress`, `--pod-port`, `--secret`, `--out`, `--no-apply`, `--dry-run`) |
| `kmx tools govern` | issue the gateway credential, set the allowlist, wire the governed `RemoteMCPServer`, repoint the agent (`--tools`, `--credential`, `--agent`, `--secret`, `--server`). It APPLIES the committed seam (`kaimahi-tools`); a seam scaffolded by `kmx tools add` is the operator's file, so it is not re-applied here — if you used `--no-apply` or `--dry-run`, apply it first, and kmx says so rather than waiting on an object that is not there |
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

`kmx agent chat` prints two different shapes on purpose. A terminal gets the
reply, any tools the agent called, and the token cost. A pipe gets the raw
A2A task, byte for byte — because things parse it: CI captures this output
in eight places and `scripts/verify-chat.py` asserts on `status.state`, the
`function_call` and the `function_response` payload. `--json` forces the raw
form when a terminal wants it. If the output is not a task kmx recognises —
a transport error, a usage message — it prints what `kagent` printed rather
than guessing at a shape that is not there.

Reading, updating and deleting agents are not kmx's job — kubectl and the
kagent CLI already do them, and `kmx agent list` says so and prints the
commands. Scaffolding is the only letter of CRUD with a real gap
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
| `KAIMAHI_CONFIRM` | unset | confirm a non-kind context, by name |
| `KMX_HOME` | `~/.config/kmx` | where the selected context and the cached kagent binary live |

`--context <name>` overrides `KUBE_CTX` for one command.

## Where the command will land

Every mutating command prints where it is about to act, and refuses anything
that is not a local kind cluster without an explicit confirmation naming the
context:

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

```bash
kmx agent create fleet-reporter \
  --description "Reports what is running in the cluster." \
  --instructions ./fleet.md \
  --tools kagent-tool-server:k8s_get_resources
```

It writes `agents/fleet-reporter.yaml` — reviewable YAML that *is* the
agent, the same file you would have written by hand — then applies it behind
the guard and waits for Ready.

| Flag | Meaning |
|---|---|
| `--model <preset>` | the ModelConfig to think with (default: the plane's governed preset if it exists on the cluster, else the keyless in-cluster one) |
| `--instructions <file>` | file whose contents become the system message |
| `--description <text>` | one-line description |
| `--tools <server>:<tool>[,<tool>…]` | MCP wiring; **the allowlist is mandatory** |
| `--namespace <ns>` | default `kagent` |
| `--out <path>` | where to write it (`-` for stdout) |
| `--no-apply` | write the manifest and stop |
| `--dry-run` | server-side dry run against the live CRDs |
| `--image <ref>` | run **your own** image instead of a declarative agent. kagent deploys it and expects A2A on `:8080`. `spec.byo` has one property, `deployment` — no `modelConfig`, no `tools` — so the governed seams that a declarative agent gets by reference travel as environment instead, and kmx prints each one it injected ([isolation.md](isolation.md)) |
| `--isolation virtual-node\|none` | where a BYO agent schedules. Needs `--image`. There is no `kata` profile: Kata is a RuntimeClass and kagent's Agent CRD exposes no `runtimeClassName`, so scheduling onto a Kata-capable node without it runs an ordinary container there — `--isolation kata` is refused with that reason rather than shipping the appearance of a VM boundary |
| `--run-as-user <uid>\|root` | the user a `--image` runs as. kmx will not guess it: see the safety table below |

### Safety properties, and why each exists

| Property | Why |
|---|---|
| **This command accepts no credential** — no flag, no environment variable, no file | The generator emits Secret *references*. A scaffolder that can take a key is a scaffolder that can leak one into a file you are about to commit. (`kmx credential capture` is the one command that takes a credential, and it takes it from a terminal and writes it to a Secret — never to a file you might commit.) |
| **Refuses key-shaped output** | Fail closed: if anything matching a known key shape reaches the manifest — including through an instructions file — writing stops. |
| **The tool allowlist is mandatory, and validated** | `--tools server` is refused; you name the tools. Naming a server alone grants every tool it offers, today and after its next release. Names must be identifiers and are quoted on emission, so a newline cannot close the YAML sequence and append a tool nobody reviewed. |
| **Block scalars indent uniformly** | A hostile instructions file cannot dedent out of the system message and become a sibling key. Multi-line values in single-line positions are refused rather than escaped. |
| **Won't overwrite** | The manifest is written with an exclusive create, so a file you have edited is never clobbered. There is no `--force`. |
| **Blast radius is the guard** | Applying goes through the same context guard as every other mutation. |
| **Preflight on the ModelConfig** | A missing ModelConfig is admitted by the API server and then fails to reconcile in silence — the Agent exists, never goes Ready, and nothing says why. kmx checks first and prints the fix. |
| **Preflight on tools** | Before apply/dry-run, the referenced RemoteMCPServer must exist, be Accepted, and currently discover every allowlisted tool. A typo cannot become an Agent that silently never reaches Ready. |
| **A BYO agent is CONFIGURED, NOT PROVEN** | Environment injection is an intention. kmx cannot see inside your image to check it reads `OPENAI_BASE_URL`, so it says so instead of implying the agent is governed because the variables are present. A row in `kmx ledger` is what makes it proven. |
| **BYO hardening splits on what kmx can know** | The declarative pod is pinned `runAsNonRoot` at UID **1001** with a read-only root filesystem, because that is the user of *kagent's* image — which spells its user as the name `python`, and Kubernetes refuses to start a `runAsNonRoot` container whose user it cannot prove is non-root. Your image is a different image, so a BYO pod always gets the half of the posture no image can invalidate (`drop: [ALL]`, no privilege escalation, `RuntimeDefault` seccomp) and the half that depends on the image only when you state it. `--run-as-user <uid>` buys the full posture; `--run-as-user root` runs as root because you said so; **neither** leaves `runAsNonRoot` and `readOnlyRootFilesystem` unset, with a comment in the manifest saying which two were left off and how to find the number. Pinning 1001 by default would fail your pod at `CreateContainer` over a UID you never chose, in a message that never names your image. |
| **Governed by default where a plane exists** | If the plane's governed preset is on the cluster, the new agent is metered, budgeted and ledgered from its first call. On a fresh `kmx up` cluster there is no plane, so the keyless preset is used and the ungoverned warning is printed. |

The reserved names `hello-world` and `hello-tools` are refused: they are
`kmx up`'s own agents, and scaffolding over one would replace a committed
artifact that the next `kmx up` would replace right back.

Running `kmx agent create` without a name on a terminal starts a local wizard.
It asks `Describe this agent:`, proposes a Kubernetes-safe name, and asks once
before creating and applying it. Enter accepts the default and applies, matching
named `agent create`; answering no cancels without writing. `--no-apply` remains
the explicit artifact-only path. The
description seeds both metadata and the agent's instructions. Namespace, model,
tools, instructions file, and output customization remain available through the
existing flags rather than turning the common path into a questionnaire. The
ordinary create guard, preflights, apply, and Ready wait run unchanged.
Non-interactive use still requires a name.

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

The uncolored `CHAT STATUS` header names the active agent and shows its effective
selected/discovered tools, descriptions, and whether each model/tool seam is
direct or Kaimahi-governed. A horizontal rule ends the header before the first
message, so startup posture cannot be mistaken for chat. A governed label
requires current MCP discovery plus ready plane replicas and Service endpoints;
unknown posture refuses rather than claiming governance. Every user message is
labelled `You`; each reply carries the active agent name. Text and correlated
tool call/completion events render as kagent streams them. The returned context
ID is reused for each turn.

Commands: `/session`, `/sessions`, `/history`, `/resume <id>`, `/new`, `/retry`,
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
`--session <id>` resumes a known session and displays its history. If a stream
closes while a task is still working, kmx polls that exact task ID; it never
resends the tool call. A Kaimahi governance denial still requires a separate
operator approval, followed by explicit `/retry`.

Capable terminals color conversational and operational labels without relying
on color alone: `YOU` is cyan, `AGENT (<name>)` is green, tool activity is magenta,
and approval/governance activity is yellow. The status header is never colored.
Messages use actor labels; non-message interactions use trusted bracketed labels
such as `[TOOL CALL]`, `[TOOL RESULT]`, `[NATIVE APPROVAL]`, and
`[KAIMAHI ROUTE]`. Dynamic tool names appear only in their indented fields.
Every payload line is indented, with arguments and
results nested one level further, so model/tool text cannot impersonate a
trusted label. Set `NO_COLOR=1` or use `TERM=dumb` for plain output.

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
| **An overlay may not carry custody** | `credential_file`, `credential_header`, `internet` and `ca_file` are refused in an overlay fragment, by the plane, not just by kmx. Together they name any path the proxy can read and any host it may be sent to — a ConfigMap that could set them would hand the plane's admin token to an attacker on the first relayed call. Keyed and hosted upstreams stay in the committed table. |
| **The apply is conditional** | The emitted ConfigMap carries the `resourceVersion` it was read at, so a manifest applied later (`--no-apply` invites exactly that) fails with a `Conflict` rather than pruning a fragment somebody added in the meantime — which would leave the upstream that fragment constrained running unbounded. |
| **A shared Service selector is named, not hidden** | The ingress policy governs every pod the selector matches. kmx lists them, and says plainly when there is more than one. |
| **Won't overwrite the manifest** | Exclusive create, no `--force`. This is about the FILE: `kubectl apply` will happily update a same-named `NetworkPolicy` or `RemoteMCPServer` in the cluster. That can happen without anyone doing anything odd — `kubectl apply -f` applies each document independently and does not roll back, so an apply that failed on the ConfigMap leaves the other three behind, and the upstream is then absent from the overlay while its objects exist. The apply output names everything it changed; read it. |

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
| **A dump with no trailer is not a backup** | `pg_dump` writes its trailer last, so its presence is the only well-formed positive. kmx writes to `<file>.partial` and renames only once it is there; a dump that stopped half way leaves nothing behind. `restore` checks the same trailer **before** it touches the plane. |
| **The backup is 0600 from the moment it exists** | It holds credential names and token hashes (never a token), the caps, the ledger, the audit trails and the grants. Keep it as you would the database. |
| **`restore` quiesces the plane** | The proxies are scaled to zero — in-flight calls drain — the tables are replaced, and the proxies are scaled back. A proxy admitting calls during a `--clean` restore could write ledger rows the restore then discards, or decide a budget against a half-loaded ledger. Whatever happens, the proxies come back. |
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
