# `kmx agent lift`

> **Status: proposed. Nothing in this document is built.** Behaviour described
> as existing cites the file and line that establishes it. Behaviour proposed
> here says "would". No performance, cost or reliability claim is made.

`kmx agent lift` deploys one agent's definition to a destination that can run
it, without a terminal session. It is the non-interactive form of the `/lift`
slash command described in [`interactive-lift.md`](interactive-lift.md).

## Why this is not configuration or an upstream contribution

[`CONTRIBUTING.md`](../CONTRIBUTING.md) requires this section.

**Not configuration.** `/lift` is reachable only from inside a chat session and
refuses a non-terminal outright: `chat_orka_controls.go:81-83` returns
`/lift requires an interactive terminal`. There is no flag, env var or file
that turns the existing flow into a scriptable one.

**Not Orka.** Orka deploys what it is given. Selecting a destination, checking
that destination's prerequisites and resolving where inference comes from are
operations across two platforms. Orka is one of them.

**Not `kubectl apply`.** A bundle is not a manifest. It renders to one, against
a target whose CRDs and namespace differ from the source's, with a `providerRef`
and `secretRef` rebound in the process.

**Not already built.** `README.md:36` states it: *"A standalone
`kmx agent lift` is not implemented yet."* `scripts/check-readme-front-door.py:36`
requires the command to appear on the front page, and it does not exist.

## The definition comes from a bundle, not from the cluster

This is the central decision.

[#194](https://github.com/kaimahi-agents/kaimahi/issues/194) decision 1 makes a
bundle in git the definition of an agent, identified by a digest of the inputs
that define behaviour. Draft PR #202 implements that as `PortableAgent`
(`internal/kmx/runtime/portable.go`) with `PortableBundleDigest` and
`RenderedBundleDigest` as separate identities.

Lifting a bundle rather than a cluster read buys four things:

1. **The deployment has an identity.** Under #194 deployments point to accepted
   revisions and receipts bind to one. An Agent read out of a cluster has no
   digest, so lifting it produces a deployment nothing can name, record,
   evaluate against or roll back to.
2. **The source cluster stops being a prerequisite.** A bundle can be deployed
   when the source is unreachable, retired, or was never the same shape. The
   same revision can reach several destinations without several reads.
3. **Drift does not travel.** A live Agent carries whatever the API server
   defaulted and whatever anyone edited in place. Copying that to a destination
   presents an accident as the definition.
4. **It is a composition, not a parallel path.** Lift becomes target selection
   and prerequisites around #202's `Render` and `Deploy`, rather than a second
   route from one cluster to another.

**This makes the command depend on #202.** That is a real cost, and it is
worth paying for the four properties above.

## Capture

Nothing today produces a bundle from a running agent. `portable.go` parses one
(`ParsePortableAgent`); there is no cluster-to-portable direction. So an agent
that has no bundle yet needs one made first, and that is the new work here.

Capture reads a running Agent and its Provider and writes a portable bundle: a
file the operator reviews and commits, since #194 decision 4 puts revisions in
git. It is a translation, not an export — the portable document's spec is
instructions and a model name, and everything platform-specific belongs in an
extension block.

Two rules follow from that:

**It refuses rather than warns.** A live Agent can hold fields the portable
format has no block for. #202's `Loss` type already states the rule: *"Losses
are normally empty because lossy mappings fail."* A bundle missing a field
silently is not the agent.

**It carries no secret values.** A captured bundle names a `secretRef`;
`refusePortableSecretShapes` already refuses credential-shaped input, and the
destination must hold the Secret itself.

Whether capture is a flag on lift, a separate `kmx agent capture`, or both, is
[open](#open-questions). It is useful on its own: it is the on-ramp for every
agent that exists today into the #194 lifecycle.

## What this proposes

```
kmx agent lift <bundle> \
  --to-context <ctx> | --subscription <id> --resource-group <rg> --cluster <name> \
  --to-namespace <ns> \
  --inference provider:<name> | keep-bundle \
  [--install-orka] [--install-k8s-tool] \
  [--plan]
```

and, for an agent that has no bundle yet, a capture step that produces one from
a running agent before the same deployment flow runs.

## What this does not claim

Listed before the design, because they bound its value.

- **Not a replacement for `/lift`.** The interactive flow discovers: it searches
  subscriptions, fuzzy-matches AKS clusters, floats remembered choices, and
  offers verified alternatives when quota is exhausted. A flag cannot browse.
  This command is for a destination you can already name.
- **Not cluster provisioning.** It creates no AKS cluster. `kmx lift` is that
  command and stays separate ([naming](#open-questions)).
- **Not Azure Foundry provisioning.** See [scope](#scope-v1-refuses-foundry-creation).
- **A captured bundle is not the revision the agent was deployed from.**
- **Not a move.** Nothing at the source is changed or deleted.
- **Not a task replay.** No Task is created, before or after.
- **Not transactional.** A failure after the Provider is created leaves the
  Provider. `interactive-lift.md` records the same property for `/lift`:
  *"A partial deployment reports an error without deleting resources."*
- **Not a secret copy.** Secret values never travel.

## Scope: v1 refuses Foundry creation

Ten of `/lift`'s twenty decision points (`chat_lift_foundry.go`,
`chat_lift_quota.go`) provision Azure AI Services accounts and model
deployments, with quota preflight, three-region fan-out and two billing
confirmations. **`--inference foundry` would be refused, naming `/lift`
instead.**

The quota flow is not a confirmation a `--yes` could replace: it is a search
across regions for a combination that exists. A flag cannot conduct that
search, and a command that picked one silently would be choosing a region and
a bill on the operator's behalf. `kmx lift`'s `--payload` is required with no
default for the same reason (`lift/plan.go:116-130`).

Existing Foundry-backed Providers stay usable through
`--inference provider:<name>`. What v1 refuses is *creating* them.

## Design

### The steps

`/lift` runs Target → Orka → Inference → Tools → Deploy → Connect
(`interactive-lift.md`). Non-interactively, Connect has nothing to reconnect and
Definition replaces the source read:

| Step | What it does |
|---|---|
| Definition | Resolve the supplied bundle. An agent with no bundle needs a separate, reviewed capture first |
| Target | Resolve the destination context, via kubeconfig or AKS |
| Orka | Check destination CRDs and controller readiness; install only if permitted |
| Inference | Resolve the destination Provider |
| Tools | Check referenced tools; install only if permitted |
| Deploy | Render the bundle for the target, then create and wait for Ready |

Lift would be **defined in terms of deploy, not beside it**: the Deploy step is
#202's `Render` and `Deploy` for the resolved target, unchanged. Lift owns only
what deploy should not — definition resolution, target resolution, prerequisite
installation, and the stricter guard below. With `--plan`, resolve the bundle
and inspect destination resources and prerequisites, using server-side dry-run
where a comparison is possible; do not install prerequisites, create
Provider/Agent resources, or write a captured bundle as a side effect. Where
missing CRDs or other prerequisites prevent a reuse comparison, report that
outcome as unknown until prerequisites are installed, rather than installing
just to make the plan definitive.

That matters for more than tidiness. There is one implementation of
deployment, so there is nothing to drift; and if lift later proves
unnecessary, removing it costs nothing, because it holds no deployment logic of
its own.

The difference between the two is their **preconditions**. Deploy assumes a
destination that is already ready. Lift makes one ready. Whether that justifies
a separate verb is [open](#open-questions).

### Flags and the decisions they replace

| Decision in `/lift` | Where asked | Flag |
|---|---|---|
| Target source, then context or subscription+cluster | `chat_lift.go:88,103,124,144` | `--to-context` XOR `--subscription`+`--resource-group`+`--cluster` |
| Orka CRDs missing — install? | `chat_lift_prerequisites.go:55` | `--install-orka`, else refuse |
| Orka controller unavailable — repair? | `chat_lift_prerequisites.go:75` | `--install-orka`, else refuse |
| Which inference | `chat_lift_prerequisites.go:136` | `--inference`, **required** |
| Kubernetes tool missing — install? | `chat_lift_prerequisites.go:170` | `--install-k8s-tool`, else refuse |
| Final deployment review | `chat_lift.go:257-262` | `--plan`, then re-run without it |
| Retry after failure | `chat_lift_deploy.go:60-63` | re-run the command |

`--inference` has **no default**, for the reason `--payload` has none. Keeping
the bundle's own configuration is frequently wrong: a Provider naming a
cluster-local Ollama Service resolves to nothing at the destination. The
command would not guess which of those two an operator meant.

The existing Kubernetes tool installer writes resources into `orka-system`
(`orka_k8s_tool.go:24-48`) and `prepareLiftTools` already refuses installation
outside that namespace (`chat_lift_prerequisites.go:167-169`). The proposed
`--install-k8s-tool` keeps that limit: installation stays confined to
`orka-system` unless namespace-aware installation is separately built; in any
other `--to-namespace`, the tool must already be present there or the command
refuses. `--to-namespace` continues to select where the Agent itself deploys.

### The guard is not bypassed

`/lift` sets `worker.guarded = true` (`chat_lift.go:240`), which suppresses
`guardOrkaCreate` (`orka_create_online.go:60-88`). That is correct there: the
operator selected the destination from a list of live clusters, saw it in the
header, and confirmed a review pane naming it.

**A flag is not a picker.** A mistyped `--cluster`, a stale shell variable or a
copied command line are exactly the cases `internal/kmx/guard` exists for, and
its rule is that kmx never follows an ambient context (`guard.go:58-66`).

So this command would run the guard against the destination, with the same
behaviour every other writing command has: a local kind cluster proceeds with a
banner, anything else requires typed confirmation naming it or
`KAIMAHI_CONFIRM=<context>`.

### What already exists

| Piece | Location | State |
|---|---|---|
| Portable bundle, digests | `internal/kmx/runtime/portable.go`, `digest.go` | **Draft PR #202** |
| Render / Deploy per target | `internal/kmx/runtime/lifecycle.go` | **Draft PR #202** |
| Deploy core | `orka_create_online.go:90-267` | Headless. Only seam is `App.operationProgress` (`app.go:42-43`), which may be nil |
| Reuse compare | `lift_reconcile.go:12-54` | Headless, gated by `App.liftReuse` |
| Orka CRD check | `chat_lift_prerequisites.go:33-46` | Already an `*App` method, no TUI |
| Endpoint probe Job | `chat_lift_endpoint.go:105-153` | Headless, already tested with a fake kubectl |
| Install Orka | `orka.go:166` | Headless |
| Install k8s tool | `orka_k8s_tool.go:24` | Headless |
| **Cluster to portable (capture)** | — | **Does not exist** |

Apart from capture, the work is a resolved-options struct, a headless driver
over these pieces, and refusals where `/lift` asks.

### Two definitions of ready

`OrkaReady()` (`orka.go:467`) checks the controller *and* the wrapper
Deployment, generation-aware. `/lift` instead inlines
`rollout status deploy/orka-controller-manager --timeout=10s`
(`chat_lift_prerequisites.go:71`).

These disagree. This command would use `OrkaReady()`, because a destination
missing the wrapper is a destination where an agent will not run, and reporting
it at deploy time rather than at first Task is the earlier failure.

Whether `/lift` should converge on the same check is a separate change and is
not proposed here.

### `--plan`

Prints the resolved bundle and its digest, the destination, the inference
selection, every prerequisite that is absent, and what would be created versus
reused — then stops. The planning pass may read the destination and use
server-side dry-run for reuse comparisons, but never installs prerequisites,
creates resources, or writes a captured bundle; this document does not
implement `--plan` itself.

The precedent is `kmx lift --plan`: *"print what would be created, where, and
stop"* (`lift_commands.go`).

### Outcomes

Three are distinguishable and would be reported distinctly, because they need
different responses:

| Outcome | Meaning |
|---|---|
| Created | The destination had neither Provider nor Agent; both were created and became Ready |
| Reused | An identical spec was already present; it was not replaced |
| Conflict | A resource with that name exists with a different spec, or is terminating |

Reuse is decided by a server-side dry-run `replace` compare
(`lift_reconcile.go:12-54`) so that API defaults do not read as differences.
Nothing is ever replaced.

kmx returns one exit code today (`cmd/kmx/main.go` exits `1` for every
failure), so Conflict and an unreachable destination are currently
indistinguishable to a caller. The distinction lives in the message until a
classified exit status exists.

## The `az` seam

`liftDiscovery` and `liftAzureWrite` called `exec.CommandContext` directly,
bypassing `App.Run`, so they carried no `Runner.Env` and no `Runner.Unset`: a
caller that removed a variable was not obeyed, and `az` could only be
substituted in a test by shadowing `PATH`. Every kubectl call goes through
`a.Command` → `a.Run` (`orka_create_online.go:20-40`, `app.go:142-150`).

AKS target resolution needs `az`, so those calls now prepare through the runner
and take their own deadline, which is the shape `orkaCapture` uses. That change
is phase 1 below and is independent of everything else in this document.

## Implementation plan

**1 — Route `az` through `App.Run`.** Independent of the definition question
and of #202. AKS target resolution needs `az`, and the seam above is the
prerequisite for testing any of it.

**2 — Capture: a running agent to a portable bundle.** Optional on-ramp for
agents without a bundle, not a prerequisite when a bundle is supplied. Refuses
rather than warns on anything the portable format cannot carry, names a
`secretRef` rather than a value, and writes a bundle the operator reviews and
commits. Depends on #202 for `PortableAgent`.

**3 — `kmx agent lift`.** Resolve a supplied bundle → target → prerequisites →
render → deploy, with the guard, `--plan` and outcome reporting. Depends on
#202's `Render`/`Deploy`, not on capture; a bundle that already exists deploys
without it.

**4 — Point `/lift` at the same path.** Not proposed here, and David's call:
the TUI would supply the same resolved options from its pickers, removing the
duplicate sequencing rather than adding a second one.

Phases 1 to 3 leave `/lift` untouched.

## Testing

`chat_lift_endpoint_test.go:44-81` is the template: it drives
`verifyFoundryEndpoint` end to end against a fake `kubectl`, asserting the
argv, the create/get/delete sequence and that no Secret is deleted. No cluster,
no terminal.

The same approach covers: capture refusing a field the portable format cannot
carry; capture emitting a `secretRef` and never a value; flag validation and
mutual exclusion; a refusal when Orka is absent without `--install-orka`;
`--plan` writing nothing; created versus reused versus conflict; the guard
refusing an unconfirmed remote context; and `--inference foundry` refusing with
a message naming `/lift`.

Note that `liftAgent`, `chooseLiftTarget`, `prepareLiftOrka`,
`selectLiftInference` and `prepareLiftTools` have **no tests today**, so this
work is unguarded by existing coverage and must bring its own.

## Open questions

1. **How does lift know a bundle already exists for a running agent?**
   *Blocking for the capture-if-absent branch.* If deploy records the portable
   digest on the Agent, lift can tell. If nothing links them, "capture when
   missing" is really "always capture", and the command should say so rather
   than imply a lookup it cannot perform.
2. **Is capture a flag on lift, a separate `kmx agent capture`, or both?**
   Separate is more useful — it is the on-ramp into the #194 lifecycle for
   every agent that exists today — but it is a second command to justify.
3. **Is lift a verb, or is it `deploy` with a destination?** *Turns on whether
   `deploy` is single-target.* Deploy assumes a ready destination; lift makes
   one ready, and confirms because it acts on a cluster that is not the
   configured one. That justifies a separate verb only while deploy stays
   single-target: once deploy grows `--to-context` for promotion between
   environments, lift's remaining content is prerequisite installation.
   #194's opening names `kmx agent lift` explicitly as using the same bundle
   and lifecycle operations as the rest of the design, so that issue does not
   argue against a separate verb. #203 names the active cluster-provisioning
   command `kmx aks up`, retaining `kmx lift` as a deprecated alias; neither
   PR has merged into this one. For a separate verb: `README.md:28` commits to
   `kmx agent lift` and `scripts/check-readme-front-door.py:36` enforces the
   line, so reversing is a product decision.
4. **Does v1 refuse Foundry, or accept fully-specified Foundry?** This document
   proposes refusing. An alternative is accepting an account and deployment
   that already exist while refusing to *create* either. That is a narrower
   refusal and may be the better line.
5. **Should `/lift` converge on `OrkaReady()`?** Not proposed here, but the two
   definitions should not both survive indefinitely.

## Rejected alternatives

**Reading the live Agent as the source of truth.** A cluster read has no
digest, so the deployment it produces cannot be recorded or rolled back to; it
requires the source cluster to be reachable; and it carries server defaults and
in-place edits to the destination as though they were the definition.

**A `--yes` flag that answers every prompt.** The Foundry quota flow is a
search, not a confirmation. There is no correct answer for `--yes` to supply.

**Defaulting `--inference` to the bundle's own configuration.** A Provider
naming a cluster-local Ollama Service resolves to nothing at the destination,
and the failure arrives at first Task rather than at deploy.

**Bypassing the guard because `/lift` does.** `/lift` earns that through a live
picker and a review pane naming the destination. A command line has neither.

**Reimplementing render or deploy.** #202 defines both per target, and
`createOrkaOnline` already carries schema validation, server admission, reuse
comparison and generation-aware readiness waits. A second implementation would
be the behaviour the prime directive exists to prevent.
