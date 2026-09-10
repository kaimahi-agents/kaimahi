# Principles for the developer entry point

`kmx` exists to shorten the distance from wanting an agent to running one
on Orka, particularly on AKS. Orka is the platform; the CLI is a means to
that outcome, not a second control plane.

## Delegate what exists

Before adding a component, inspect the relevant upstream version and
justify the gap in the PR. Prefer configuration, integration or an
upstream contribution to a parallel implementation. Success includes a
path that no longer needs this repository.

The current migration seam is a bridge. Its authentication, metering and
protocol adaptations are implementation facts, not reasons to preserve
Kaimahi as a separate platform. `orka.harness.v2` is out. Orka already
ships OTLP with GenAI conventions; do not plan an exporter as an unmet
upstream need.

## Leave reviewable artifacts and ownership with the user

Generated YAML should remain readable, diffable and usable with the
underlying tools. Do not conceal ownership inside an entry point.

For migration, the application owner keeps its Deployment and decides
whether to apply the printed patch. Governing model traffic does not mean
adopting the workload, creating an Orka Agent from it, or governing its
other traffic. See [migrate.md](migrate.md).

Authoring is still open: native Orka only, or kagent YAML as another
surface over Orka. [orka.md](orka.md) records a native-authoring
recommendation, not a decision that forbids the latter. Today's
`kmx agent create` emits native Orka Provider + Agent resources and an
optional Task; it does not translate existing kagent resources. The isolated
conversion spike does not settle the supported authoring interface.

## Show where an operation will act

Name the target before writing. Require explicit confirmation for a
non-local cluster; refuse unreadable or ambiguous state rather than
interpreting it as absence. A friendly context name is not proof that a
cluster is disposable.

Read-only inspection and no-write planning should remain useful without
claiming that validation proves readiness. A server-side dry run can show
whether resources are accepted, not whether the resulting pods work.

## Keep credentials out of artifacts

Generated files contain Secret references, not values. Never add a token
flag, print a credential, or tell a user to commit one. The existing
capture paths are deliberately separate from scaffolding; their input,
expiry and custody limits are in [identity.md](identity.md),
[models.md](models.md) and [hosted-upstreams.md](hosted-upstreams.md).
A capture exception is not permission to accept arbitrary secrets in YAML.

## One implementation and honest evidence

Use the native command instead of maintaining a parallel shell workflow.
A Make target may delegate, but must not quietly own a competing version
of the same operation. Existing checkout-only connectors are documented
as such until their code lane resolves them.

Distinguish unit and fake-service tests, continuously exercised cluster
paths, one-off cloud measurements, schema validation and proposals.
Successful admission is not downstream completion; observed readiness is
not a forecast. Do not turn an old run into a present availability claim.

Measure fewer prerequisites, a useful first result, clear recovery and
less code of our own—not command count or adoption of this repository.

## Publication and further reading

The distribution and name constraints remain in [NAMING.md](NAMING.md).
Do not claim a package-manager namespace as a convenience change.

[COORDINATION.md](COORDINATION.md) records current rulings and lanes;
[kmx.md](kmx.md) describes the implementation that exists;
[cli-ux-plan.md](cli-ux-plan.md) records terminal and automation contracts.
