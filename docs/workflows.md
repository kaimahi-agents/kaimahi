# Workflows: saying what an agent does, and what may happen without you

A Kaimahi agent that does one useful thing end to end needs four
decisions, and until now they lived in four places:

| the decision | where it lived |
|---|---|
| which servers it reaches | `k8s/plane/upstreams.yaml`, the committed table |
| what its arguments MEAN | `policy_fields`, in the same table |
| which calls need no human | a Make variable, `RELEASE_TOOLS` |
| which calls need one, and in what order | 556 lines of shell |

That works — [the release agent](release-agent.md) cuts real releases
with it — and it is unrepeatable. Change the repository and you edit a
shell script; add a step and you write bash; want two approvers and there
is nowhere to say so.

A **blueprint** is one file that says all of it, and `kmx workflow` is the
command that applies and runs it.

```sh
kmx workflow list
kmx workflow show release --set repo=owner/name
kmx workflow govern release --set repo=owner/name
kmx workflow run release --set repo=owner/name --set version=v1.2.3 --dry-run
```

`--dry-run` writes nothing to the cluster, including the seam credentials
a live run re-mints — so it rides whatever is already in custody. The run
says which seams it did not refresh, because an expired one does not
announce itself: it shows up as the agent reporting those tools missing
from its toolset.

`kmx` carries the blueprints it ships, so the four commands above need no
checkout — and neither does capturing the credentials they use. What still
needs one is **provisioning the release agent itself**: its `Agent`
manifest and the two `RemoteMCPServer` objects are not carried by `kmx`,
so `make govern-release` applies them from a clone, once. After that,
running the workflow needs no checkout at any step.

**What a blueprint does not create.** A blueprint governs a credential and
an agent that already exist; it never creates either, and it cannot create
a seam. `kmx workflow govern release` refuses outright when the plane has
no `release-agent` credential, naming the command that issues one. The
release agent's own `Agent` manifest and the two `RemoteMCPServer` objects
that point it at the gateway (`k8s/release-agent.yaml`,
`k8s/kaimahi-release-github.yaml`, `k8s/kaimahi-release-ado.yaml`) are not
carried by `kmx` — `make govern-release` applies them, from a clone.

So the honest sequence is:

| step | needs a checkout? |
|---|---|
| `kmx quickstart` / `kmx up`, `kmx plane` | no — `kmx` carries the runtime and plane manifests, including the upstream table the release seams are declared in |
| `kmx credential capture github-release owner/name`, `kmx credential capture ado <organization>` | no — typed at a prompt |
| the release agent and its two seam objects (`make govern-release`, which also issues the credential and sets its allowlist) | **yes** |
| issuing the credential on its own, for an agent that already exists (`kmx tools govern --credential release-agent --agent release-agent --secret …`) | no |
| `kmx workflow list` / `show` / `govern` / `run` | no |

A blueprint you write for an agent you already run needs no checkout at
any step. It is this repository's own release agent whose manifests are
not embedded.

## What a blueprint says, and what it deliberately cannot

```yaml
blueprint: v1
name: release
summary: Cut a GitHub release from Azure DevOps and GitHub Actions builds.
credential: release-agent
agent: release-agent

parameters:
  repo: {type: github_repo, required: true, help: the repository, as owner/name}

seams:
  github-release:
    requires:                        # what this workflow DEPENDS on
      list_tags:     [owner, repo]
      create_branch: [owner, repo, branch, from_branch]
    allow: [list_tags]               # → the credential's tool allowlist
    bound:                           # → standing constraints
      list_tags:
        - {field: owner, op: eq, value: "${repo.owner}"}
        - {field: repo,  op: eq, value: "${repo.name}"}

steps:
  - name: cut
    kind: consequential              # denied by default; a human approves
    call:
      upstream: github-release
      tool: create_branch
      args: {owner: "${repo.owner}", repo: "${repo.name}",
             branch: "release/${version}", from_branch: main}
    prompt: Call create_branch with …
```

Three things are missing from that list on purpose.

**It carries no credential.** A blueprint names Secrets; it never holds a
value, and the parser refuses a document that looks like it is carrying
one. Capturing a token stays a human's job.

**It cannot create a seam.** `requires` states the `policy_fields` this
workflow depends on and kmx checks them against the running table — it
does not write them. A hosted or keyed upstream cannot live in the
operator overlay at all, because `credential_file`, `internet`, `ca_file`
and `extra_headers` together are an exfiltration primitive; the plane
refuses them there. So seams are reviewed in the committed table or
onboarded with [`kmx tools add`](govern-your-agent.md) — or, on the model
seam, [`kmx models add`](spend.md#adding-a-model-upstream) — and a
blueprint NAMES them. If the table disagrees with what a blueprint requires, the
command refuses and says which side to fix — nothing is applied.

**It cannot say "the agent decides".** There is no step kind for it, and
a `call`'s arguments may reference parameters and literals only. A
reference to something an agent turn produced is a parse error. That is
not fastidiousness: a model that proposed a different call would file an
approval request too, and it would look identical in `kmx approvals`. The
accounts-payable demo learned it the expensive way, on a live run: the
agent's own turn filed a payment for a different invoice at the same
amount, and the approval landed on the wrong one.

## The four kinds of step, which are four answers to "who is interrupted"

| kind | what happens | who is interrupted |
|---|---|---|
| `read` | an agent turn; its calls are on the allowlist | nobody |
| `propose` | an agent turn whose reply is an artifact (drafted notes) | nobody; a human reads it |
| `bounded` | a call with consequences that a **standing constraint** admits | nobody, and it is audited |
| `consequential` | denied by default; the driver files a request; a human approves that exact call | one person, once |

`poll` is the fifth and is not about authority: it is a bounded wait,
because a build is minutes and an agent turn is request/response.

kmx checks the postures against each other, which four hand-written
layers could not:

- a `consequential` tool may not be allowlisted — the approval would be
  theatre, since the agent could have made the call anyway;
- a `consequential` tool may not carry a standing bound either — **a
  constraint ADMITS**, so the call was already permitted;
- a `bounded` tool must actually have a bound, or the step is a call that
  is simply denied.

## Arguments that are only known at run time

The release workflow's publish step attaches the artifacts of builds
that an earlier step started. Their ids do not exist when the run begins.

They are a **parameter**, marked `required_for: [publish]`, and the
operator passes them on the resumed run:

```sh
kmx workflow run release --step publish --set repo=owner/name \
    --set version=v1.2.3 --set ado_builds=9001,9002
```

That is the whole answer, and it is the only one available: the value
being approved has to come from a human, not from a model's reply. What
CAN come from a previous step is prose — `capture:` stores a turn's reply
and `${capture.notes}` reads it back — because prose is not what an
approval binds. The release notes are a `capture`; the tag is a
parameter.

## What is in a run: `when:`, and what `--set` turns on

Most workflows are not one shape. The release blueprint builds on GitHub
Actions, or on Azure DevOps, or on both, and publishes only once there are
build ids to publish. Those steps carry a guard:

```yaml
  - name: build-ado
    when: ado_pipelines
    for_each: ado_pipelines
```

**`when:` asks whether you supplied that parameter.** Supply
`--set ado_pipelines=41,42` and the step is in the run; leave it out and
the step is not — it is not skipped, not failed, not reported as done.
`kmx workflow show` prints the ones you have not enabled, with the flag
that would:

```text
  build-ado                    bounded       (needs more: not in this run — needs --set ado_pipelines)
```

Two consequences worth knowing before you write one:

- **A default counts as supplied**, so a guard on a parameter that has a
  default would never be false. That is not a subtlety you have to
  remember: the parser refuses it, and says to drop one or the other.
  `--set` is the thing that turns a step on, and only a parameter with no
  default can be the switch.
- **`show` and `run` answer this the same way.** They did not always:
  `kmx workflow run` once bound *every* step regardless of its
  guard, so it demanded the parameters of steps you had not enabled. A run
  that supplied every guarded parameter still started; what could not start
  was any run that left one out — and that is the case `when:` exists for.
  The release workflow made it circular: `publish` is guarded by
  `when: ado_builds`, so the only runs that began were the ones passing
  build ids that do not exist until the build step has run. The two
  commands now share one predicate, and a test renders and binds the
  carried blueprint with the same `--set` and asserts the same step list.

Asking for one guarded step whose guard you did not supply
(`--step publish` with no `--set ado_builds`) runs nothing, and says so
with the flag that would have:

```text
nothing to run: every step asked for is conditional on a parameter that was not supplied.
  publish — needs --set ado_builds=…
```

## Where a blueprint lives

Two places, and a third that was rejected.

1. **Carried by the binary**, named: `kmx workflow govern release`. This
   is the default because the front door is `curl | sh` then
   `kmx quickstart`, with no Go and no checkout — a blueprint that could
   only be read out of a clone would put `git clone` back in the way of
   the one feature whose point is that expressing a workflow is cheap.
2. **A file you wrote**: `--file ./my-workflow.yaml`. Bundled scripts sit
   beside it.
3. **A URL — no.** A blueprint decides which tools a credential may call
   and what its standing bounds are, so fetching one is letting a remote
   party set policy. What makes kmx's fetched `kubectl` safe is a pinned
   version and a checksum, and a blueprint URL you want to keep current
   is by construction mutable. `curl -o` then `--file` is the same thing
   with the review step still visible.

Your configuration — which repository, which organization, which project
— is never in the blueprint. It is `--set`, for the reason
`scripts/release-bind.sh` states: somebody else's project is not
something a public repository commits.

## Applying it: imperative, with a precondition

`kmx workflow govern` writes two things and nothing else: the
credential's tool allowlist, and its standing constraints as a
[overlay fragment](govern-your-agent.md), so `kmx plane` — which
reapplies the base table — keeps them.

It does not reconcile. If the cluster's bounds differ from what this
blueprint and these parameters produce, it prints the difference and
refuses; `--replace` is the deliberate act that overwrites. There is no
controller here, and a command that silently reverted a bound somebody
tightened by hand would take away the operator's own escape hatch.

If another fragment already constrains the same credential — a cluster
where `make release-bind` was run — it says so and names the command that
removes it, because the plane refuses two fragments defining one
credential rather than resolving by precedence.

**It needs a plane new enough to answer.** Checking `requires` means
asking the plane what its merged table declares, and that is a field the
admin API gained in this change. Against an older plane the command
refuses rather than applying a blueprint unchecked, and says to run
`kmx plane`.

## What the driver does that a script should not have to re-learn

`kmx workflow run` is one driver for every blueprint. It carries the
properties `scripts/release-run.sh` paid for:

- **it files the approval request itself**, for the call your parameters
  name, and stops if the plane did not file exactly that one;
- **it will not ride a live grant** — a run that spent an approval given
  earlier, for a call it never described, is the failure this exists to
  prevent;
- **the plane's record is the proof**: after the call, the tool audit
  must show it admitted under the grant, or the run fails rather than
  reporting something the plane did not record;
- **it keeps its own admin port** (19291), because your `kmx approve`
  needs the default one while the run is waiting for you;
- **it refreshes an expiring upstream credential** and makes kagent look
  again, because a token that dies mid-run surfaces as *"there is no such
  tool in my toolset"* — a symptom nowhere near its cause;
- **it resumes**: `--step <name>`, which is how a run continues after a
  failed build or a person going home.

## The step that is not governed, said out loud

The most valuable step in the release workflow is the least governed one.
`scripts/release-publish.sh` moves the artifacts using your own `az` and
`gh` — 1.28 GB across five assets on the last real release — because the
gateway is deliberately not in that path.

A blueprint has to mark that, or rendering every step in one vocabulary
would launder it. So a step with an action on your machine must carry
`ungoverned:` and a reason, `kmx workflow show` prints those in their own
section, and the driver repeats it before the approval:

```text
NOT GOVERNED BY THE PLANE — these steps' ACTIONS run on this machine, outside the gateway.
The plane records that a human approved the DECISION. It meters nothing, sees no bytes,
and writes no tool-audit row for what actually moved.
```

The DECISION is governed. The TRANSFER is not. Both are true, and only
one of them is what an approval covers.

kmx does not fetch `az` or `gh`. It fetches `kubectl`, `kind` and `helm`
because those are pinned, checksum-verified release artifacts whose
identity IS the checksum; a freshly downloaded `az` is a binary with
nobody logged into it, which buys nothing and adds a supply chain. A step
that needs one names it and stops.

## See also

- [The release agent](release-agent.md) — the workflow this vocabulary
  was generalised from, and every measurement behind it.
- [Approvals and permits](approvals.md) — what a grant is welded to.
- [Tool governance](tool-governance.md) — `policy_fields` and standing
  constraints.
- [Govern your own agent](govern-your-agent.md) — onboarding a server
  this repo did not write, which is the seam half of the same job.
