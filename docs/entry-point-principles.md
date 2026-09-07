# Principles for the developer entry point

*Origin: [Sajay Antony's `kmx` proposal](https://gist.github.com/sajayantony/9c294f2f2f650fecd49dae1c2348d1c2),
relayed from leadership, accepted and then shaped further.
This document is not the proposal — the command set is settled and being
built. It is the reasoning underneath it: why an entry point exists at all,
what it must never become, and how we will know it is working.*

---

## The problem is not that Kubernetes is hard

It is that the distance between *wanting an agent* and *having one* is
measured in things you must already know.

When this was written, that distance was a clone, a prerequisite list, and
a Makefile. The Makefile is good — it is the journey in glue form, and
every step of it is proven by CI on every pull request. But it asked for
something before it gave anything: `git clone`, then Docker or Podman,
kind, kubectl, Helm, make, curl, then `make up KIND_CLUSTER=<your-name>`,
then a five-to-ten minute wait during which nothing told you whether it was
working. (The prerequisite list is now one item, a container engine: `kmx`
fetches kind, kubectl and Helm itself, checksum-verified.)

A developer who abandons at minute three has not rejected the idea. They
never got to it.

The entry point exists to close that distance. Not to make Kubernetes
easier — kagent already does the hard part — but to make the *first
successful agent* a matter of minutes and a few commands, on a machine
that had none of this yesterday.

## What it is

One binary. `kmx`, a single static Go program that shells out to kind,
kubectl, Helm and the kagent CLI exactly as the Makefile does, and does
nothing those tools already do.

That last clause is the load-bearing one, and it is why this is a set of
principles rather than a feature list.

## Six principles

### 1. Delegate everything that already exists

kagent ships `init`, `install`, `deploy`, `get`, `invoke`, `add-mcp`,
`run`, `build`, and a dashboard. `kmx agent chat` is a passthrough to
`kagent invoke`. It is not a reimplementation, a wrapper with extra
opinions, or a "better" invoke.

This is not politeness. Rebuilding what an upstream already ships is the
mistake that caused this project's only full restart, and it is written
into the prime directive as a result. A second implementation of someone
else's control surface is a maintenance liability that grows every time
they release.

The test: for any command, can you name what it does that the underlying
tool does not? If not, it should not exist — and when a developer asks for
it, the right answer is to print the command that already works.

### 2. The generated YAML is the product, not a by-product

`kmx agent create` writes `agents/<name>.yaml` and applies it. The file is
the point. It is the same YAML a developer would have written by hand, it
is theirs from that moment, and it is reviewable, diffable, committable
and portable to any conformant cluster.

An entry point that hides the artifact has built a black box with a nice
front door. The moment the tool disappears — because it is unmaintained,
or unavailable, or simply wrong for a case — the developer must still have
something that works. What they keep is the YAML.

This also sets the ceiling on cleverness: if a feature cannot be expressed
in the artifact, the artifact is the thing to fix.

### 3. Show where you are about to act, and fail closed when unsure

Every mutation names its target before it happens, and anything that is
not a local development cluster requires an explicit yes.

The repo already learned this the expensive way. An earlier scaffolder
decided "is this safe?" by matching `kind-` against the context *name* —
which is cosmetic, and sails straight through for a context called
`kind-prod` pointing at production. The replacement checks the API server
address as well, and refuses rather than guesses when it cannot tell.

The principle generalises: the dangerous state is not "wrong answer", it
is "confident wrong answer". Where the tool cannot establish the truth, it
stops.

### 4. Never hold a credential, and take one only where it can be typed

The entry point does not accept an API key as a flag, an environment
variable, or a file path. Generated manifests carry Secret *references*,
never Secret values.

A scaffolder is exactly where a key wants to be typed, and exactly where
it must not be. Anything that can take a key can leak one into a file the
developer is about to commit. So `kmx agent create`, `kmx tools add` and
the blueprint loader all screen their input against credential shapes and
refuse.

The principle has since been given one ruled exception, and the shape of
the exception is the point. `kmx credential capture <upstream>
<repository|organization>` takes the token for one of the three tool
upstreams whose value can be checked against the upstream before anything
is stored. It reads it **from a terminal or not at all**: echo off, no
flag, no environment variable, no file, and a pipe or a redirect refused
rather than read — because a credential that can arrive through a pipe can
arrive from a shell history or a CI log. It never reaches argv, a
temporary file, a log or the command's own output. Every other credential
(model keys, the Slack bot token, inbound signing keys) is still captured
by a dedicated script that reads stdin and writes a Kubernetes Secret.

What the exception buys is the last step of the journey that needed a
clone. What keeps it an exception rather than a hole is that it is one
command, for three named upstreams, on one input channel a script cannot
reach.

### 5. Work with the cluster you have, or give you a disposable one

`kmx ctx <context>` selects and validates an existing cluster.
`kmx up` creates a local kind environment and installs the dependencies.
Both are first-class.

Assuming the disposable cluster patronises the platform engineer who
already has one. Assuming the existing cluster abandons the newcomer who
does not. Neither audience is the "real" one.

### 6. One implementation, not two that drift

`kmx` implements the journey; the Makefile's `up`, `cluster`, `agent`,
`chat`, `status` and `down` become thin aliases that call it.

Two implementations of the same journey diverge — quietly, and usually in
the direction of the one CI does not exercise. Delegation means CI keeps
proving the code a developer actually runs, rather than a parallel path
that merely resembles it.

## What it must never become

- **A second control plane.** Governance lives in the plane and at
  kagent's seams. The entry point points agents at it; it does not
  enforce, meter or approve anything itself.
- **A `kubectl` with opinions.** Read, update and delete already exist and
  are better than anything shipped here would be. Scaffolding is the gap.
- **A place secrets live.** See principle 4.
- **A dependency you cannot remove.** The YAML keeps working when `kmx`
  does not.
- **A publishing commitment made by accident.** Distribution is a naming
  decision, not a convenience decision — see below.

## The uncomfortable part: the name is not ours yet

The original proposal opens with `brew install kaimahi-agents/tap/kmx`.
That line cannot ship, and the reason is worth stating plainly rather than
quietly dropping.

Publishing a package or a tap is an outward-facing claim on a name. When
this was written, PyPI `kmx` and the GitHub user `kmx` were already taken;
npm, crates and the Homebrew formula were free; and **KMX is CarMax's
ticker symbol**, which is now in the trademark counsel brief alongside the
open questions about *kaimahi* itself — a te reo Māori word still awaiting a cultural
appropriateness read.

So no package-manager namespace is claimed until those gates clear.
Installation is the project's own `install.sh` — it downloads the release
binary for the platform and verifies its published sha256 — or
`go install …/cmd/kmx@latest`. That is less convenient than a tap, and it
is the honest position. A tap created for developer convenience is still a
public claim, and reversing one is much harder than delaying it.

## How we will know it is working

Not by command count. By these:

| Question | What a good answer looks like |
|---|---|
| How long from nothing to a first reply? | Minutes, on a machine that had none of this yesterday |
| What does the developer keep? | A YAML file they understand and can apply without the tool |
| What happens on an unfamiliar cluster? | It names the target and waits for a yes |
| What happens when it does not know? | It stops, and says what it could not determine |
| How much of it is ours? | As little as possible — the rest delegated |
| Would removing it break anything? | No. The artifacts and the Makefile still work |

The first successful agent is the metric. Everything else is a means.

## What was built in slices, and what is still not here

The entry point was built in slices, and the slices were chosen so that
each one was useful alone. All of them have since shipped, in this order,
and the ordering is the part worth keeping:

**The runtime journey first.** `ctx`, `up`, `agent create`, `agent chat`,
`status`, `down` — kind, kagent, Ollama, the agents, and no governance
plane. A consequence was stated and accepted rather than discovered: on a
fresh cluster `agent create` scaffolds the keyless preset and prints the
ungoverned warning, because governed presets only exist once the plane
does.

**Then the plane.** `plane`, `govern <name>`, and the read-only views —
`ledger`, `grants`, the audit reads. Clone-free: the binary carries the
manifests and fetches the plane at its own revision. kind only, which kept
it entirely keyless.

**Then the operator verbs, and then the rest.** `use`, `budget`,
`approvals`/`approve`/`deny`/`request`, `tools`, `backup`/`restore`,
`metrics`; then `quickstart` and `install.sh`, `tools add` for a server
this repository did not write, `workflow` and blueprints, `flow`,
`credential capture`, and `lift` for the managed-cluster path. `kmx` is no
longer only a local-development entry point.

**What is still not here.** The Slack and inbound connector families,
capturing a model key, and the network probes stay in `make` and
`scripts`.

**Publishing.** A Homebrew tap, or any package, waits on the naming gates.
Nothing about the design blocks it; the decision is not a technical one.

The ordering principle is worth stating because it is the part most likely
to be argued with: **capabilities become commands only after the quick
start is simple, reliable, and useful on its own.** A command added early
is a command that must keep working while the foundation under it is still
moving. It is easier to add the tenth command than to remove the third.

### Known rough edges

These are recorded because they will be met by whoever works on this next,
and finding them a second time is wasted effort:

- `go:embed` cannot cross into `plane/` while it carries its own
  `go.mod`, so the entry point can never carry the plane's *source* — it
  fetches a built binary instead. The nested module that blocks the first
  is what makes the second work.
- A module-proxy build does not set `vcs.revision`, so a build-info
  version read that relies on it silently loses the revision on this path.
- `go install` refuses to cross-compile while `GOBIN` is set, which some
  version managers set by default. It will bite contributors on a Mac
  targeting Linux.
- Rendering and context-guarding already exist as shell scripts. Porting
  them is reasonable; ending up with two implementations of either is not
  (principle 6).

## Directions worth arguing about

**Nothing below is ruled, scheduled, or agreed.** These are seeds — several
of them cut against decisions currently in force, and that tension is the
reason they are written down rather than the reason to leave them out. Each
one is stated with the objection it has to answer.

### A provider model — the entry point as a harness

Today every agent here is `type: Declarative` on kagent, running as a pod.
That is one runtime, and the entry point is shaped around it.

The idea: make the runtime a *provider* the harness selects, so the same
scaffolding journey can target something other than a pod —
`kmx create agent` versus a Hyperlight micro-VM or a WASM target, chosen at
create time rather than baked into the tool.

The pull is real. A scaffolder that only ever emits one runtime's YAML is a
template engine with extra steps; one that abstracts the runtime is a
harness other people can build on.

The objection it must answer: **kagent is the runtime, and principle 1 says
delegate.** A provider model is only honest if the alternatives genuinely
exist and are genuinely wanted — otherwise it is an abstraction over one
implementation, which is the most expensive kind. The test is whether a
second provider ships and stays supported, not whether the interface looks
extensible.

### The consumer question: who is holding it

`kmx create agent` reads differently depending on who types it. A platform
team wants a governed default. A product team inside a large organisation —
GitHub, say, consuming this rather than building it — wants a runtime
choice and a topology they can review.

Worth resolving deliberately rather than by accident: **the entry point's
audience determines its defaults.** Governed-by-default and
choose-your-runtime pull in opposite directions, and a tool that tries to
serve both without deciding will do neither cleanly.

### Governance as a component, DX first

The provocative one. Today the framing is that governance is the product
and the developer experience is how you reach it. The inversion: the
developer experience is the product, and governance is a component it
composes — something the entry point pulls in, rather than the thing
everything else exists to serve.

The objection: this repo's differentiator *is* the governance plane, and
the scenarios that justify it are journeys where the control boundary is
the whole point. Demoting governance to a module risks becoming another
pleasant way to run agents in a market that has several.

The counter-argument deserves a fair hearing anyway: nobody adopts controls
they cannot get to. The first fifteen minutes decide whether the governance
is ever reached at all.

This one is a genuine fork in the road. It should be argued explicitly and
ruled, not drifted into.

### Topology as a first-class view

Leadership's founding ask was:

> "having an artifact that shows my agent topology — almost agent as code
> (ideally yaml template or something like that)"

The YAML delivers that for *one* agent. It does not answer it for an
estate: which agents exist, what each may call, which credential each
carries, what is governed and what is not. Today that is assembled by hand
from `kubectl`, the ledger and the audit tables.

A topology view is the natural next artifact — and it is closer than it
looks, because the plane already records the edges. It is a read over data
that exists.

### Auditing agents, not just calls

The audit tables answer "what happened". A reviewer usually wants "what
*could* happen": this agent holds that credential, reaches those tools,
draws on that budget — before it is invoked, not after.

That is a different query over the same data, and arguably where governance
becomes reviewable rather than merely recorded.

### A separate CLI surface

Raised, and worth stating precisely so it is not lost: splitting the CLI
from the implementation.

The objection is the alias condition above, and it is a strong one: **one
implementation, not two that drift.** If a split ever happens it must not
recreate the parallel path the alias condition exists to prevent — a
library plus a thin front-end is defensible; two front-ends that both implement the journey is
the failure mode already ruled against.

---

## Status

`kmx` is built and released (v0.1.0), and the whole journey it was proposed
for — a cluster, an agent, the governance plane, the operator verbs, a
governed workflow, and the same agent on a managed cluster — runs from the
installed binary with no checkout. This document describes the reasoning
those decisions encode; the decisions themselves, with their conditions and
dissents, are in [COORDINATION.md](COORDINATION.md). What each command
actually does is [kmx.md](kmx.md).
