# The release agent

Kaimahi's first real user. Everything else in this repository is a
demonstration — a fixture ERP, a hello-world agent, a Slack channel made
for the purpose. This one helps cut releases of a real project, on a real
repository, on the schedule that project actually releases on.

That changes what "working" means. A demo is finished when it convinces
somebody. This is finished when the person who cuts the release keeps
using it, and every piece of friction on the way there is a finding worth
more than the feature.

## One command

```sh
kmx workflow run release \
  --set repo=owner/name --set version=v1.2.3 \
  --set gh_workflows=build-app-win.yml,build-app-mac.yml \
  --set ado_project=<project> --set ado_pipelines=41,42
```

That is the whole interface, and the shape of it is the design. It is not
a checklist to work through: it runs every step, blocks on the approvals
itself, and polls the builds itself. A person is interrupted exactly once
per consequential call — to approve it — and the approval can be typed in
Slack from a phone:

```
@kaimahi approve 3f1c9a2e uses=1 ttl=10m
```

Start with `--dry-run`. It reads, drafts the notes, and stops before the
first consequential call.

`--step <name>` runs one part of it instead of all of it and may be repeated.
`kmx workflow refresh release` only re-mints the Azure DevOps credential and
checks the seam.

Publishing has one additional input:
`--set notes_file=release-notes-v1.2.3.md`.
It is required even when publish is part of a full run. Captures are temporary
artifacts of one process, while `--step publish` is specifically a new-process
resume; requiring a reviewed, durable notes file gives both paths the same
reliable contract.

## What the agent does, and what it cannot

**It drafts.** It reads the last release, reads what merged since, and
writes the notes. That is the one place judgement earns its keep here:
turning forty terse pull-request titles into the five lines a user of the
product cares about is work a script does badly. Where a pull request
says nothing a user could act on, the agent is told to say so and name it
rather than invent a meaning for it.

**It proposes.** Cutting the branch and starting the builds are calls it
makes and a human approves.

**It never carries a byte.** Build artifacts do not pass through the
gateway. The request-body cap is 4 MiB
([`gateway.go`](../plane/internal/gateway/gateway.go)), and an MCP gateway
moving binaries would be the wrong tool at any size. The agent dispatches
the GitHub workflow and the Azure DevOps pipelines; they build.
This turned out to be forced rather than chosen — see
[what the GitHub server cannot do](#what-the-github-server-cannot-do).

**The last step, publishing, is the one place bytes move on this machine,
and it is weaker than the rest.** GitHub Actions cannot reach the Azure
DevOps organization and an ADO pipeline was ruled out, so the only host in
both networks is the one the driver runs on: `scripts/release-publish.sh`
creates the GitHub release with the agent's notes and streams the build
artifacts from ADO onto it (1.28 GB across five assets on the last
release). The **decision** is governed exactly like every other
consequential call — a request naming the release, a named human, a grant
welded to it — but the **transfer** is not: the gateway is not in that
path, and the credentials are the operator's own `az` and `gh`, not plane
custody. That is written down rather than glossed, in the script's own
header.

Before any workflow step or approval, kmx checks `bash` plus the publish
script's declared `az`, `gh`, `curl`, `python3`, and `unzip` dependencies.
The script checks its non-core tools and the readability of `NOTES_FILE`
again before making a network call.

**It never decides to ship.** Every consequential call is denied by
default, files an approval request carrying the exact call, and proceeds
only under a grant welded to that call ([approvals.md](approvals.md)).

**It reaches one repository.** Three independent reasons, below.

## The approval names the release, not the verb

One property matters more than any other here: approving "publish v1.2.3"
must not authorize the next release. An approval is welded to the digest of
the exact call, not to the verb, and what it binds is the `policy_fields`
the committed table declares
([`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml)).

| Call | Bound |
|---|---|
| `create_branch` | `owner`, `repo`, `branch`, `from_branch` |
| `actions_run_trigger` | `method`, `owner`, `repo`, `workflow_id`, `ref` |
| `pipelines_write` | `action`, `orgName`, `project`, `pipelineId`, `previewRun` |

**Why the dispatcher argument comes first.** Both servers the release
agent talks to consolidate several operations into one tool.
`actions_run_trigger`'s
`method` chooses between running a workflow, re-running one, **cancelling**
one and deleting its logs. `pipelines_write`'s `action` chooses between
queueing a run, creating a pipeline, renaming one and cancelling a stage.
A verb-level policy cannot tell those apart: allowlisting the name would
allow all of them. Binding the selector is what makes the tool governable
at all, and putting it first makes the audit summary read as the verb it
really is.

**Why the release notes are not bound.** `name` and `body` are prose the
model regenerates, and an LLM re-emitting semantically identical prose is
not byte-stable; binding them would make "approve, then it proceeds" fail
at random — which is exactly why the digest binds the *declared* fields
rather than the whole argument object. So
the honest statement of what a human approves is: **this version, on this
repository, from this commit** — and they read the drafted notes in the
proposal step. An agent that got approval on v1.2.3 could publish
different prose under it. The blast radius of that is editable text; the
blast radius of the alternative is an approval flow that fails randomly.

**What the driver checks before a human is asked.** A model that proposed
a *different* branch files a request too, and it would look identical in
`kmx approvals`. So between the agent proposing and a person being
shown anything, the kmx workflow driver requires a pending request whose
summary names every policy-relevant field of the call that was asked for
on the command line. If the agent proposed something else, the run stops
and says so, and nothing is approved. The accounts-payable demo learned
this the expensive way: on its first live run the agent's own turn filed a
payment for a different invoice at the same amount, and the approval landed
on the wrong one.

## What makes a destructive operation impossible

The hard guardrail is: no force-push, no tag deletion, no branch
deletion. Four layers, and one that is honestly not available.

1. **The servers are told not to offer them.** The `github-release`
   upstream sets an `X-MCP-Tools` allowlist plus `X-MCP-Exclude-Tools`;
   the `ado` upstream sets `X-MCP-Toolsets: pipelines`. A tool excluded
   this way is not in `tools/list`, is not discovered by kagent's
   controller, is not projected to the agent, and cannot be reached even
   by an approval. This is the strongest layer, because it does not
   depend on Kaimahi's own bookkeeping. It is also why the tool seam
   gained `extra_headers`.

   **Measured, and it matters:** GitHub's server honours `X-MCP-Tools`
   only when `X-MCP-Toolsets` is absent. Sending both offered 26 tools
   instead of the 10 named; dropping the toolsets header brought it to
   exactly 10, which the gateway then projects down to the 8 reads. The
   boot log line to check is
   `gateway: projected tools/list … offered=10 projected=8` — if
   `offered` is larger than the tool list you sent, the outer ring is not
   doing what you think it is.
2. **The gateway forwards to exactly two servers.** `tool_upstreams` is
   the whole reachable set.
3. **Nothing destructive is allowlisted, or selected.** The credential's
   allowlist holds reads only; `k8s/release-agent.yaml` names no
   destructive tool, so kagent wires none.
4. **GitHub's own server has no force-update.** Its only ref-creating
   tool is `create_branch`, which calls `Git.CreateRef`; `Git.UpdateRef`
   is called only inside file-write flows and hardcodes `Force: false`.
   There is no `delete_ref`, no `update_ref` and no `create_tag` tool.

**And the layer that does not exist.** The token is *not* one of these.
GitHub's fine-grained permissions put creating a branch, creating a
release and **deleting a ref** all under `Contents: write`; there is no
separate Releases permission and no way to grant creation without
deletion. The smallest token that can cut a release branch can also
delete one. So the honest sentence is: *the gateway is what stands
between this agent and a deleted branch, and the token is not a second
opinion.* Said here rather than claimed away.

## Credentials

Both in plane custody: the gateway injects them, the agent never holds
them, and neither is ever in the tree.

```sh
kmx credential capture github-release owner/name   # paste the fine-grained token
kmx credential capture ado <organization>          # paste the Entra access token
```

Capturing a credential is a human's job here and stays one. It is typed at
a prompt with the terminal's echo off, and the value is read **from a
terminal or not at all**: there is no flag, environment variable or file
that will take it, and a pipe or a redirect is refused rather than read,
because a credential that can arrive through a pipe can arrive from a
shell history or a CI log. From there it travels to exactly two places:
the upstream, in an `Authorization` header, which is how it is checked;
and the Secret. Never argv, never a temporary file, never a log, never
this command's own output. Everything after the prompt is the agent's.

This is the one path on which `kmx` accepts credential material. Every
other path still refuses it: the agent wizard screens its input against
credential shapes, and a blueprint carrying a credential-shaped key is
refused before the document is decoded.

**GitHub**: a fine-grained personal access token on ONE repository, with
Contents read+write, Pull requests read, Actions read+write. Refused if it
is not fine-grained, if it announces OAuth scopes (which makes it a
classic token), if it cannot read the repository named, or if GitHub's
answer is about a different repository. **Reported, not enforced**: the
expiry, because an answer that carries no expiry header means the answer
carried none, not that the token never dies. **Not proven at all**: which
permissions you granted, and whether the token reaches only that one
repository — GitHub exposes neither. A check for the second one used to
sit here and was removed: it asked `GET /user/repos`, which answers by the
user's affiliations rather than the token's scope, so it refused
correctly-scoped tokens and proved nothing about wrong ones. What actually
holds the blast radius is `kmx workflow govern release`, below.

**Azure DevOps**: a Microsoft **Entra access token**, not a PAT — see
[the ADO seam](#the-azure-devops-seam-is-not-a-pat). Refused if it is not
an access token, if it carries no expiry claim, if that expiry has passed,
or if the hosted server does not accept it on a real `initialize`.
**Reported, not enforced**: the audience, because Entra writes it two ways
for the same request (the resource URI or the application's identifier,
depending on the target's token version) and refusing on it refused tokens
minted by the documented command. The server is asked instead, which is
the only authority on it. The deadline is reported, because it is about an
hour: re-capture with `--replace`.

The repository demo has a checkout-only cleanup target:

```sh
make release-revoke
```

which deletes both Secrets and closes the hosted egress allowance. **Run
it at the end of any session that was only a test.** Deleting the Secret
stops Kaimahi using the GitHub token; revoking it at
github.com/settings/personal-access-tokens stops GitHub honouring it, and
the command says so.

## One repository, three ways

1. The GitHub token is fine-grained and scoped to one repository — your
   assertion when you create it, and not something the capture can check,
   because GitHub exposes no endpoint that reports a token's repository
   grant. The two bounds below are what actually hold it.
2. `kmx workflow govern release --set repo=owner/name` adds a **standing
   constraint** — a declarative bound the credential carries, checked
   before the allowlist — so the read tools are callable only with that `owner` and
   `repo`. A read naming another repository is denied at the plane and
   files a request. Only the reads: a standing constraint *admits*, and
   admitting a branch creation without a human would be the opposite of
   the design.

   It is written as an **overlay fragment**
   (`kaimahi-upstreams-extra/workflow-release.json`), not as a patch to the
   committed table, so `kmx plane` keeps it — a repository binding that
   silently disappeared on the next deploy would be worse than none. The
   overlay merges per name and refuses collisions, so it sits beside the
   accounts-payable agent's constraint without touching it, and it is
   written with a single-key merge patch because other operators'
   fragments live in the same ConfigMap.
3. The consequential calls bind `owner` and `repo` in their digest, so an
   approval for one repository cannot be spent on another. (The one closed
   exception to digest binding — grants recorded before argument binding
   existed — is in
   [tool-governance.md](tool-governance.md#enforcement-all-fail-closed); no
   new one can be created, and this credential has none.)

## Long builds: the driver polls, the agent never blocks

A kagent turn is request/response; an Azure DevOps build is minutes. The
waiting therefore lives in the kmx workflow driver, and each agent turn is
one short step.

Rejected, and why:

- **The inbound bridge** (webhook → A2A), delivering a build-completion
  callback. It needs a public HTTPS edge — a `LoadBalancer` Service, an Azure DNS
  label and ACME ([`k8s/inbound-edge.yaml`](../k8s/inbound-edge.yaml)) —
  which exists on AKS only, and a weekly release process run from a
  laptop will not stand that up. It also buys latency, not capability.
  And on the generic hooks the webhook body becomes the agent's prompt
  verbatim, which is not what to do with a build payload.
- **A polling loop inside one agent turn.** A model re-deciding "has it
  finished yet" every minute burns budget on a question the status tool
  answers exactly, and puts an unbounded wait inside a bounded turn.

## What the GitHub server cannot do

Worth knowing before you plan around it. GitHub's hosted MCP server
(read at `v1.12.0`) has **no tool that creates a release, and none that
creates a tag**. `list_releases`, `get_latest_release`,
`get_release_by_tag`, `list_tags` and `get_tag` are all read-only, and
`create_branch` is the only ref-creating tool in the registry.

So the agent cannot publish a release even if a human approved it. The
path is `actions_run_trigger` with `method: run_workflow` — the agent
dispatches the workflow, and the workflow publishes. Which is the shape
this design wanted anyway: the agent tells systems to move bytes and never
carries them. Here the tool surface enforces it rather than the design merely
intending it.

There is also **no compare tool**, so "what changed since the last
release" is `get_latest_release` for the previous tag, then
`list_pull_requests` (`state: closed`, `base`, sorted by update) reading
`merged_at` per pull request. `list_pull_requests` is deliberately
preferred over `search_pull_requests`, which takes its scope inside a
free-text `query` argument — a repository named in free text is not
something an argument-level policy can bind.

## The Azure DevOps seam is not a PAT

This seam was designed expecting one. It is not available.

`POST https://mcp.dev.azure.com/` answers `401` with
`WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource/"`,
and that document declares:

```json
{"resource": "https://mcp.dev.azure.com",
 "authorization_servers": ["https://login.microsoftonline.com/organizations/v2.0"],
 "bearer_methods_supported": ["header"],
 "scopes_supported": ["https://mcp.dev.azure.com/.default"]}
```

Microsoft Entra ID only. The **local** `@azure-devops/mcp` server does
take a PAT, but it speaks stdio and nothing else, and the gateway relays
streamable HTTP — reaching it would mean writing a shim into the
credential path, which this project declined to do.

What makes the remote server reachable anyway is
`bearer_methods_supported: ["header"]`: Entra's answer is an ordinary
bearer token in the Authorization header, which is exactly what the
gateway already injects from custody. So the ADO seam is configuration
and not code, as intended — just with a different credential than
expected.

The prerequisites, which are Microsoft's and not ours: the organization
must be backed by an Entra tenant (standalone Microsoft-account
organizations are not supported), and whichever client mints the token
must be consented in that tenant.

**Every ADO call must name the organization.** The upstream is configured
at the ORG-LESS URL (`https://mcp.dev.azure.com/`), because the
organization is an Azure identifier and this repository does not carry
one — so each tool call passes `orgName`, which Microsoft documents as
the alternative. `orgName` is therefore a bound policy field on every ADO
tool: an approval for a build names *which organization*, not just which
pipeline. Pass it to the driver as `ADO_ORG`.

Note that the hosted server's tool schema is not the local server's:
`orgName` does not exist in `@azure-devops/mcp`'s source at all. Read the
schema from a live `tools/list` before declaring policy fields against a
hosted server, rather than from the repository behind it.

**The cost, and it is larger than it looks:** an access token lives about
an hour, and a release session is longer than that. On the first real run
it expired twice. Each time the visible symptom was the agent reporting
*"there is no pipelines_build tool in my toolset"* — because the seam had
gone `Accepted=False` with `Unauthorized`, and kagent had dropped its
tools. The cause and the symptom are nowhere near each other, and nothing
in the message points at a credential.

Two things follow. The kmx workflow driver **refreshes the token itself**
before every step that touches Azure DevOps: it runs on the operator's
machine, `az` is already there, and the gateway reads the credential file
per request so no restart is needed. And it checks the seam's `Accepted`
condition first, so an expired credential says so instead of arriving as
a missing tool.

The deeper fix is not here: the plane holds a captured bearer and has no
way to renew one. A credential that outlives a human approval cycle needs
the plane to refresh it, and that capability is not built. The seam does
not break when it dies — the server answers 401, the gateway audits it, nothing is
half-done — but it is real friction, and the plane's own expiring
credentials exist for the same reason.

## Builds are bounded, not approved

`kmx workflow govern release` optionally takes `--set ado_org=...`, `--set
ado_project=...` and `--set ado_pipelines=...`. They constrain
`pipelines_write` to `action
run_pipeline` on exactly those pipeline ids in that one project of that
one organization. Those builds then run with **no human at all**,
audited; anything else on that tool — another pipeline, another project,
`create_pipeline`, `update_build_stage` — is denied and files a request.

That is not a weakening, and the first real run produced two arguments
for it. The first is judgement: a build is repeatable, reversible and
cheap, so approving each one spends a person's attention on the safest
step in the release. The second is measured. The Azure DevOps credential
is an Entra access token that lives about an hour, and the elapsed time
between filing a request, a human approving it, and the agent making the
call is *exactly that long*. On the first run the token expired in that
window and stranded three approvals that had already been given. A
release process with a human in it will lose that race routinely, not
occasionally.

The caveat, stated because a constraint ADMITS: see the next section —
this bounds WHICH pipeline runs, not WHAT it builds.

## Known gap: the ref an Azure DevOps build runs on cannot be bound

An approval for `pipelines_write` binds *which pipeline in which
project*. It cannot bind *which ref that pipeline builds*.

The branch a run builds is not a top-level argument. It lives at
`resources.repositories.<name>.refName`, and Kaimahi's policy vocabulary
is top-level argument names only — `policy.go` says so and enforces it.
`templateParameters` is nested for the same reason.

Verified against the LIVE hosted schema, not inferred from the local
server's source: `pipelines_write` on `mcp.dev.azure.com` takes `action`,
`orgName`, `project`, `pipelineId`, `pipelineVersion`, `previewRun`,
`resources`, `stagesToSkip`, `templateParameters` and `yamlOverride`, and
`resources` is a JSON object. There is no top-level ref. (Worth checking
this way round, because the hosted schema is NOT the local one — see
`orgName` above, which the local source does not have at all. The same
check on `pipelines_build` action `list` found a top-level `branchName`,
so READING builds for a branch can be bound even though running one
cannot.)

This is the first hole this project has found by pointing the plane at a
server it did not write, and it is real: an approval to build pipeline 41
does not constrain what it builds. Closing it needs dotted-path policy
fields (`resources.repositories.self.refName`), which is a plane change
and is not built.

## Proven how

**In CI, keyless.** Every decision above happens in the gateway before
the forward, so all of it runs against the synthetic hosted upstream with
no credential anywhere: a read bound to one repository proceeds with no
human and the same read elsewhere is denied and filed; cutting a branch
is denied and the request names the artifact; the approval is welded to
that release, so the next version and the same version from a different
base are denied again; an approval to run a workflow cannot be spent
cancelling one; and a tool excluded by a header is absent from both
`tools/call` and `tools/list`. The stand-in honours `X-MCP-Tools` the way
the real servers do, and its policy declarations are copied from the
committed table so they cannot drift from what ships.

**In the plane's own tests.** `k8s/plane/upstreams.yaml` is parsed by
`go test`, so a typo in the committed table fails the suite rather than a
rollout; the dispatcher tools are asserted to bind their selector first.

**For real.** The transcript is in the pull request.

## The same workflow, as a blueprint

Everything above is the release agent as first built: four layers, and
one release-specific driver. A user who wants this workflow, or a slightly
different one, should not have to reproduce that by hand — so the answer is
[a blueprint](workflows.md) — one file, applied and run by `kmx
workflow`, with `blueprints/release.yaml` reproducing exactly the
governance the old release-specific implementation produced.

Two things that generalising found, and neither is a defect in what
shipped:

- **`pipelines_write` had two postures at once.** The old binding and driver
  gave it both a standing constraint and an approval, while this document
  says those builds "run with no human at all". A standing constraint
  ADMITS, so the blueprint resolves that old
  disagreement by marking the step `kind: bounded`, which is what this
  document describes. kmx refuses a blueprint that declares a tool
  consequential and bounded at once.
- **The blueprint watches GitHub and Azure DevOps in two poll steps**
  rather than the one combined turn `do_watch` uses, because a single
  prompt naming both would carry an Azure organization into a run that
  has none.

## From zero

```sh
kmx up && kmx plane
kmx models credential copilot
kmx govern
kmx credential capture github-release owner/name
kmx credential capture ado <organization>

# Checkout-only repository orchestration: kmx does not carry these manifests.
make govern-release
kmx workflow govern release --set repo=owner/name
kmx workflow run release --set repo=owner/name --set version=v1.2.3 --dry-run
```

When done, the checkout-only `make release-down` removes the repository's
agent and both seams; `make release-revoke` deletes both tokens and closes the
allowance.
