# Proposal: a kaimahi scaffolder CLI

**Superseded by [`kmx`](kmx.md).** A command-line entry point was built after
all, in Go rather than Node, and it does more than scaffold: `kmx` owns the
whole developer journey (`up`, `agent create`, `agent chat`, `status`,
`down`), and the Makefile targets are thin aliases that call it, so CI
proves the code a developer runs. Two of the rulings recorded below —
scaffold-only, and the Makefile owns bring-up — are therefore superseded;
the survey still stands and still binds, which is why `kmx agent chat` is a
passthrough to `kagent invoke` and there is no `kmx install`. The safety
table this document's prototype established is carried across in full.

**Status of this document: considered, prototyped, not built.** The five
open decisions at the end were ruled: no npm publish yet, scaffold-only,
the Makefile owns bring-up, a zero-dependency Node toolchain accepted.
A working prototype was reviewed in pull request #16 and closed by its
author unmerged; nothing from it is on main. This document stays as the
survey and the design record. Anyone reopening the idea starts from those
rulings and the review notes on that pull request.

A net-new CLI was not to be built without a written survey against kagent's
existing CLI first. This document is that survey plus a proposed surface.
It ends in the decisions that were open when it was written; all have
since been ruled on.

The ask, as given: `npx kaimahi create agent --options ...` plus CRUD, an
install path "if we really need", and the background security aspects.

## Survey: what kagent's CLI already ships

Verified by running the pinned binary, not from docs — `kagent v0.9.12`
(commit `b459905`, built 2026-07-20), the version this repo installs:

```
kagent --help
```

| Command | What it does | Overlaps our ask? |
|---|---|---|
| `init [framework] [language] [name]` | **scaffolds a new agent project** — flags `--model-provider`, `--model-name`, `--instruction-file`, `--description` | **directly** — this is a `create agent` |
| `install --profile minimal\|demo` | installs kagent into the current cluster | **directly** — this is an `install` |
| `deploy <dir> --env-file .env` | reads `kagent.yaml`, creates Secrets from the `.env`, creates the Agent CRD; `--dry-run` emits YAML | create/update |
| `get agent\|session\|tool` | read/list | the **R** in CRUD |
| `invoke --agent --task` | converse (streaming, sessions) | — |
| `add-mcp` | add an MCP server to `kagent.yaml`, wizard or flags | MCP tool connectors |
| `run <dir>` | run the project locally via docker-compose + chat | — |
| `build <dir> --image --push` | build/push the agent image | — |
| `uninstall`, `dashboard`, `bug-report`, `version`, `completion` | — | `install`'s inverse; UI |

**This is the finding that matters: `kagent init` and `kagent install`
already exist.** Two of the three things asked for are shipped upstream. A
proposal that ignored this is exactly the mistake the prime directive was
written to prevent.

## What survives the survey

Three gaps are real. They are narrow, and they are the *only* defensible
justification for net-new CLI code.

**1. `kagent init` scaffolds a code project, not declarative YAML.**
It generates an ADK framework project — source tree, Dockerfile,
`kagent.yaml` — which you then `build` and `deploy`. This repo's artifact is
the opposite: a `type: Declarative` Agent + ModelConfig in one applyable
file, no image, no build step ([`k8s/hello-world.yaml`](../k8s/hello-world.yaml)).
Nothing upstream scaffolds *that*. The declarative path is the one leadership
asked for ("agent as code — ideally yaml template").

**2. `kagent install` assumes a cluster already exists.**
It installs kagent into your current context. It does not create the kind
cluster, stand up the in-cluster Ollama server, or pull a model. The
zero-to-ready journey — the thing `make up` encodes and the thing a new
person actually needs — is uncovered.

**3. `kagent deploy` takes API keys from a `.env` file on disk.**
Its help is explicit: the `.env` *must* contain `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, or `GOOGLE_API_KEY`, and the command loads it into a
Secret. That is precisely the custody model this project rejects — keys go
in via stdin and nowhere else, never a file, argv, env listing, or log
([`docs/models.md`](models.md)). There is also no preset switching
(`make use PRESET=`) upstream.

Note what gap 3 means: this is not a convenience gap, it is a security
disagreement with upstream. That is the strongest argument in the proposal
and the one worth leading with.

**A fourth, same shape as the first.** `kagent add-mcp` writes MCP server
entries into a project's `kagent.yaml` — again the code-project path. The
declarative equivalent is `spec.declarative.tools[]` with a `toolNames`
allowlist, now shipped in
[`k8s/tools-agent.yaml`](../k8s/tools-agent.yaml). Nothing upstream
scaffolds that either, so a `--tools` flag falls out
of gap 1 rather than being a separate ask.

## Uncomfortable question the survey raises

All three gaps are already filled — by the Makefile. `make up` does gap 2,
`k8s/hello-world.yaml` plus `make use` do gaps 1 and 3. So the honest
question is not "should we build a CLI" but **"what does a CLI buy over the
Makefile?"**

One answer holds up: **you have to clone the repo to use a Makefile.**
`npx` is consumption without a clone, from an empty directory, in someone
else's project. That is the "consume anywhere" property, and it is the
whole case. If we are not willing to publish to npm, the case collapses and
the Makefile is sufficient — and publishing means claiming the name, which
needs explicit approval and, at the time this was written, two gates that
were still open: a cultural read and trademark counsel
([NAMING.md](NAMING.md)).

## Proposed surface

Scaffold-only. Everything kagent already does is delegated, not wrapped.

```bash
npx kaimahi create agent <name> [--options]
```

| Flag | Purpose |
|---|---|
| `--model <preset>` | `ollama` (default, keyless) \| `anthropic` \| `openai` \| `openrouter` \| `github-copilot` \| `azure-foundry` \| `openai-compatible` |
| `--model-name <id>` | override the model ID within the preset |
| `--base-url <url>` | for `openai-compatible` / `azure-foundry` |
| `--instructions <file>` | system message from a file (mirrors `kagent init --instruction-file`) |
| `--description <text>` | Agent description |
| `--namespace <ns>` | default `kagent` |
| `--out <dir>\|-` | write YAML to a directory, or `-` for stdout (GitOps-friendly) |
| `--tools <server>[:<tool>,...]` | wire `spec.declarative.tools[]`; **allowlist required**, never "all tools" |
| `--apply` | apply to the current context (off by default — generate, don't mutate) |
| `--dry-run` | server-side dry-run against live CRDs |
| `--yes` | non-interactive; without flags, an `add-mcp`-style wizard runs |

Default behaviour is **generate a file and print the next command**. It does
not touch a cluster unless you ask it to. That keeps it a scaffolder.

### CRUD — deferred, and here is the data for the ruling

You skipped this question; it is left open deliberately. The survey now
constrains it more than it did when asked:

- **R** is `kagent get agent` and `kubectl get agents`. Two shipped
  implementations already.
- **D** is `kubectl delete agent <name>`.
- **U** is `kubectl apply` of the edited YAML — which is the point of agent
  as code; a CLI `update` command competes with `git diff`.
- **C** is the only letter with a genuine gap (declarative YAML scaffolding).

So "CRUD" as a whole is close to a re-implementation of `kubectl`. The
options remain: scaffold-only on local YAML; scaffold plus apply/delete;
or full lifecycle against the cluster. My read is that anything past
scaffold-plus-apply spends the project's credibility on the exact mistake
that caused the restart — but this needs your ruling, not mine.

### `install` — recommend not building

`kagent install` exists. The uncovered part is the cluster underneath it. If
anything is built here it should be honest about being that and nothing
else — a `kaimahi up`-shaped command that creates kind, stands up Ollama,
pulls the model, then **calls `kagent install`** rather than reimplementing
it. Under the answer you gave ("install if we really need"), the survey says
we do not need `install`; we might need `up`.

## Background: security aspects

A scaffolder that people run with `npx` is a supply-chain component and a
credential-adjacent tool. Both need answering before code, not after.

**Supply chain — `npx` runs remote code on the user's machine.**

- Publish with npm provenance (Sigstore attestation), so the tarball is
  traceable to a signed CI run in this repo.
- **No install scripts.** No `preinstall`/`postinstall`; document
  `npx --ignore-scripts` as safe.
- Pin and lock transitive dependencies; prefer a near-zero-dependency
  implementation. A scaffolder that emits YAML does not need a large tree.
- Document a pinned invocation (`npx kaimahi@<version>`); bare `npx` always
  resolves to latest, which is a mutable remote-code channel.
- Any binary the CLI fetches (e.g. the kagent CLI) must be checksum-verified
  before execution — the Makefile already does this and the CLI must not
  regress it.

**Credential custody — the differentiator; do not weaken it.**

- The CLI **never** accepts a key as a flag, an environment variable, or a
  file path. Stdin only, straight into a Kubernetes Secret.
- It must never call `kagent deploy --env-file`, and must never generate a
  `.env`. That would import upstream's weaker custody model into ours and
  discard the reason this component exists.
- Generated YAML contains Secret *references*, never Secret *values*. A
  scaffolder that can write a key into a file it also suggests committing is
  a credential-leak generator.
- Nothing is cached to disk except what already is (the Copilot OAuth token,
  0600, per [`scripts/copilot-secret.sh`](../scripts/copilot-secret.sh)).

**Blast radius — it holds a kubeconfig.**

- Print the target context and namespace, and require explicit confirmation
  before mutating anything that is not a local kind cluster. `--apply` on a
  production context by accident is the obvious foot-gun.
- Fail closed: a partial scaffold applies nothing; a failed validation
  writes nothing.
- No telemetry. If that ever changes it is opt-in and ruled on the board.

**Note the ordering problem.** A scaffolder is the natural place to *ask*
for a key, and at the time this was written there was no budget, metering,
or ledger behind it. Making it easy to point a fresh agent at a billed
endpoint, with no governance, is a real risk the CLI introduces. Until a
governance plane exists, the CLI should
default to the keyless Ollama preset and print the ungoverned-spend warning
whenever a hosted preset is selected.

## The five decisions, as they stood

These were open when this document was written; the status note at the top
records how they were ruled. They are kept as posed, because the reasoning
under each is the part worth reading.

1. **Publish or not.** The `npx` case requires npm publication, which is an
   outward-facing name claim needing explicit approval, and the cultural
   read and trademark counsel on the name were still open. Without
   publication, use
   `npx github:kaimahi-agents/kaimahi` for dev and treat the CLI as internal.
2. **The CRUD line** (deferred above).
3. **`up` vs. `install`** — build the cluster-zero command, or leave that to
   the Makefile and ship only `create agent`?
4. **Sequencing.** The MCP tools work has merged, so the contention for
   `k8s/` and the Makefile is gone — but the governance plane is the next
   piece of work, and it mounts at the same seams a CLI would scaffold.
   Building the CLI first risks scaffolding a shape the plane then has to
   change; building it after means it can generate governed agents from day
   one.
5. **Language/runtime.** `npx` implies Node; the rest of the repo is YAML +
   shell + a little Python. A Node dependency is net-new surface.

## Explicitly not proposed

Anything with a working upstream implementation: `invoke`, `get`, `run`,
`build`, `dashboard`, `add-mcp`, agent runtimes, CRDs, a web UI. If a user
needs those, the CLI should print the `kagent` command rather than grow one.
