# Legacy reference: approvals and bounded grants

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This reference remains because
> the approval code still runs and its messages point here. It describes
> that code, not the target architecture or an Orka approval contract.
> Current onboarding starts at the [documentation index](README.md).

## Existing procedure

The model is **deny-and-retry**: no held-open MCP call or durable workflow
is created by an approval. A denial can file a pending request; a human
decides it; the client must attempt the action again.

```sh
kmx approvals
kmx approve <id> --ttl 10m --uses 1
kmx deny <id>
kmx grants
kmx audit approval
kmx audit tool <credential>
```

Choose approve **or** deny, not both. Administrative commands use the
cluster context and admin bearer, recording `decided_by=admin`, not a
human identity. The [CLI definitions](../cmd/kmx/commands.go) are the
reference for flags. To pre-file a specific call:

```sh
kmx request tool k8s_get_events --credential hello-tools \
  --args '{"namespace":"default"}'
```

Omitted arguments mean the argument-less call, not permission for all
arguments. Budget requests instead name `tokens` or `cents`; approving
one requires `--amount` in those units as well as a time/use bound.

## The approval binds the call

The gateway computes a SHA-256 digest from the tool name, binding mode
and [policy-relevant arguments](tool-governance.md#declaring-what-arguments-mean).
The request, grant and tool audit carry that digest. A mismatch does not
consume an exact-call grant and files a separate pending request.

Pending requests deduplicate by credential, kind, subject and digest.
A filing failure does not reverse a denial. Approval/denial, grant creation
and their audit row commit together; a decision that cannot be recorded
is not committed. Decided requests are immutable; later denials can file
new ones. Source: [store/approvals.go](../plane/internal/store/approvals.go).

**Exact means the declared fields, not every consequence.** With explicit
`policy_fields: []`, every argument set has the same digest. With no tool
declaration, the digest binds the whole canonical object but the summary
does not disclose arbitrary undeclared arguments. Old NULL-digest grants
remain bounded verb-level permissions; new ones cannot be minted and exact
matches are consumed first. Summaries contain only declared scalar values,
clipped and sanitized, not a full request or business-data redaction.

## Standing constraints: the calls that need no approval

A credential can carry rules over a tool's declared top-level fields in
[the table](../k8s/plane/upstreams.yaml) or an allowed overlay fragment.
`eq`, `ne`, `lt`, `lte`, `gt`, `gte`, `in`, `not_in` are supported; all
clauses are ANDed. Unknown operators, empty rules, invalid numeric bounds
and undeclared fields are refused at configuration load. Missing or
wrong-typed values fail the call's constraint check.

A call inside a constraint needs no grant. Outside it, a matching live
grant is required: **the static allowlist cannot bypass an existing
constraint**. A bound on amount does not bound a payee or establish a
relationship to an invoice. Include every relevant field; nested paths
and cross-system relationships are not evaluated by this vocabulary.

## Grant lifetime and consumption

At least one positive time or use bound is required. Expiry and exhaustion
are checked in SQL at admission, across replicas; expired/exhausted rows
remain as history. A grant does not extend its credential's lifetime.

Budget headroom is the cap plus live grant amounts. Only calls that need
the overage consume budget-grant uses. Tool-grant uses are consumed before
forwarding, so an upstream failure can spend the grant without delivering
a result. Inbound grants admit events, not their later tool actions.
See [spend](spend.md), [inbound](inbound.md) and [identity](identity.md).

## Deciding from Slack

The retained [inbound command parser](../plane/internal/inbound/command.go)
accepts `@kaimahi approve <id> [uses=N] [ttl=D] [amount=N]` or
`@kaimahi deny <id>`. IDs can be an unambiguous prefix of at least eight
characters. Commands run after signature/channel checks but before the
inbound grant gate, invoke no agent, and require membership in the hook's
Secret-mounted `slack_approvers_file`. Channel membership is not authority.
Missing, empty or invalid approver data fails commands closed; ordinary
questions retain their own gates. Bot-authored events are ignored.

The hook defaults are one use and fifteen minutes when not overridden;
a budget approval still needs an amount. The decision records
`slack:<user id>` on request, grant and audit, without resolving a display
name. Synthetic signed mentions prove parser/authorization behavior, not
that a human approved. Never impersonate a real approver for a demo.

[slack-approvers.sh](../scripts/slack-approvers.sh) retains list capture;
the notifier uses its own credential and the [Slack posting path](slack.md).
Notifications are best-effort and asynchronous: known refusals may be
retried up to three attempts; ambiguous post failures are not retried to
avoid duplicates. A lost announcement never removes a filed request.
**`kmx approvals` is the queue of record**, not the Slack channel.

## Remaining limits and evidence

Discovery may lag grants, and a grant does not expand an agent's selected
`toolNames`. An invisible tool may never be attempted and thus never file
a request. Per-call gateway checks remain authoritative.

These controls govern inputs, not tool results, model reasoning or every
network route. They do not prevent prompt injection. An allowed call, an
in-bound call or a human-approved bad call can still have harmful effects.
Store tests, [gateway constraint tests](../plane/internal/gateway/constraint_test.go)
and [Slack command tests](../plane/internal/inbound/command_test.go) preserve
implementation evidence without the retired demo transcripts.
