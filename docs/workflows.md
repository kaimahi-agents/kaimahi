# Legacy reference: workflow blueprints

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. The blueprint parser and runner
> remain for existing workflows; this is their technical reference, not a
> new agent-authoring platform or an Orka workflow contract. Start at the
> [documentation index](README.md) for the current direction.

## Existing commands and prerequisites

```sh
kmx workflow list
kmx workflow show release --set repo=owner/name
kmx workflow govern release --set repo=owner/name
kmx workflow run release --set repo=owner/name --set version=v1.2.3 --dry-run
```

`show` inspects; `govern` writes policy; `run` executes. Read the rendered
policy and active steps before either write or execution. The existing
`--dry-run` still makes read/draft agent turns and can spend model tokens;
it stops before consequential/bounded actions and does not refresh upstream
credentials. It is **not offline and not zero-side-effect**.

A blueprint names an already existing credential, agent and seams. It creates
none of them. The carried release blueprint is embedded in kmx; the release
Agent and RemoteMCPServer manifests are not. Existing deployment wiring is
in the [Makefile](../Makefile); definitions are in
[release-agent.yaml](../k8s/release-agent.yaml),
[kaimahi-release-github.yaml](../k8s/kaimahi-release-github.yaml) and
[kaimahi-release-ado.yaml](../k8s/kaimahi-release-ado.yaml).
Do not infer that a successful policy apply created a working agent.

## What the document declares

[blueprints/release.yaml](../blueprints/release.yaml) is the complete
retained example; [blueprint.go](../internal/kmx/blueprint/blueprint.go)
defines parsing and validation. The top-level version is `blueprint: v1`.
It names a credential and agent, typed parameters, seams, and ordered steps.

A seam's `requires` lists the tool declarations/policy fields the workflow
depends on; `allow` supplies the credential's tool allowlist; `bound` supplies
standing constraints. `requires` is checked against the running plane's
merged table, not used to rewrite upstream declarations. An older plane
without the needed admin contract is refused rather than trusted unchecked.

A blueprint carries no credential material. Capture remains a separate
terminal-only procedure, and credential-shaped content is refused by the
parser. Bundled blueprints or a reviewed local `--file` are accepted, not a
mutable remote blueprint URL. Operator-specific identifiers belong in
`--set` values, not public examples or committed real-world configuration.

## Step kinds and authority

| Kind | Existing execution/authority |
|---|---|
| `read` | agent turn using permitted tools |
| `propose` | agent turn producing a prose artifact |
| `bounded` | consequential call admitted by standing constraints, no human grant |
| `consequential` | explicit request and human grant for the named call |
| `poll` | bounded wait through repeated turns |

A consequential tool cannot also be allowlisted or carry a standing bound;
otherwise the approval would not be the admission gate. A bounded tool must
actually have a bound. Constraints admit matching calls; they are not
approval prompts. These are checks on declared workflow policy, not a
substitute for inspecting all live policy and the upstream's behavior.

Call arguments can use parameters/literals, not an agent's captured reply.
`capture:` stores prose for later prompts or local artifacts; captures do
not become trusted policy arguments. A run resumed with `--step` cannot use
a capture that no step in that invocation produced.

## Parameters and conditional steps

`required_for: [step]` delays a parameter requirement to relevant steps.
Values known only later, such as ADO build IDs, are supplied by the operator
on a resumed run; the driver does not promote model output into authority.

`when: parameter` enables a step only when that parameter was supplied.
A guarded parameter cannot have a default, since a default would always
enable it. `for_each` expands list-valued parameters. `show` and `run` share
the active-step predicate; parameters of disabled steps are not required.
Requesting only disabled steps runs nothing and reports what is missing.

`--step <name>` selects a step, not a durable checkpoint replay. It does not
reconstruct previous captures or guarantee idempotent external side effects.
The release publish path has additional inputs and local artifact behavior;
read the rendered step and [release limitations](release-agent.md) first.

## Applying policy

`kmx workflow govern` changes the credential's allowlist and its standing
constraints in an overlay fragment. It does not reconcile continuously.
If existing policy differs, it reports the difference and refuses unless
`--replace` deliberately authorizes replacement. A colliding fragment for
the same credential must be resolved; the plane does not silently choose
precedence. Overlay policy survives a later plane deployment.

These operations are not a cross-system transaction with kagent, Kubernetes
and Postgres. Inspect rollout errors: applied policy and the serving proxy's
loaded table may differ until a successful restart. Implementation:
[workflow.go](../internal/kmx/app/workflow.go).

## Running and checking outcomes

[workflow_run.go](../internal/kmx/app/workflow_run.go) files consequential
requests from the explicit call, rejects riding a pre-existing live grant,
waits for a decision, and checks the admitted tool-audit row's grant/digest.
The driver uses admin port-forward 19291 so a separate `kmx approve` can
use the normal admin port. `--wait` bounds the wait; `--approver`
requires the recorded `decided_by` value, rather than accepting any grant.
Current CLI decisions record `admin`, not a verified person. Requiring another
value cannot be satisfied by that path now that the inbound approver is retired;
the runner does not silently relax the constraint.

The live runner can refresh configured expiring upstream credentials and
ask kagent to rediscover; dry-run uses current custody instead. An expired
seam can look like missing tools. Model prose is not a success signal:
**an admitted audit row verifies admission, not downstream completion**.
Do not repeat a possibly completed external action merely because a later
step or audit read failed.

## Local actions are not gateway-enforced

An `exec` action must explicitly carry `ungoverned:` and a reason, shown
both by inspection and before approval. The release publisher uses the
operator's `az` and `gh` to move artifacts outside the gateway. The plane
records approval of the declared decision, not the transferred bytes or a
tool-audit row for them. `release_publish` binds only owner, repo and tag;
notes, target commit and artifact selection are not part of that digest.

kmx does not install or log into `az`/`gh`; the operator supplies them.
The blueprint is therefore trusted local executable/policy input, not merely
a harmless description. Review bundled scripts as well as YAML.

[Blueprint tests](../internal/kmx/blueprint/) and
[runner tests](../internal/kmx/app/workflow_test.go) remain as implementation
evidence. The duplicated shell tutorial and historical release transcript
are retired; runtime and code ownership do not change here.
