# FAQ and troubleshooting

Start with [getting started](getting-started.md). Orka installation and
model-traffic migration are the current paths; the final section below is
for the legacy kagent/plane implementation that still exists.

## Why is `kmx orka` missing?

The latest tagged release, `v0.1.0`, predates the Orka helpers. `@latest`
and the release installer do not mean the tip of `main`. Follow the current
build instructions in [getting started](getting-started.md), then inspect
`kmx version` and `kmx orka --help`.

## Does installing Orka govern my application?

No. Installation creates platform resources and, by default, a local
Provider. [Migration](migrate.md) is separate. The application must already
have a Deployment managed by its owner, who reviews and applies the
printed patch. This routes model traffic; it does not register an Orka
Agent or put all tool and inbound traffic under governance.

## Why does status show a version different from the pin?

`kmx orka status` reads the running controller image. The pin is what kmx
would install, not proof of what someone else installed. An unreadable
cluster is not an empty cluster. See [Orka installation](orka.md) for
inspection, no-write planning, dry-run and upgrade limits.

## Can kagent YAML create an Orka agent?

There is no supported translation in the current CLI. `kmx agent create`
emits kagent resources. Whether kagent YAML will become an authoring
surface over Orka remains open; native Orka resources are the recommendation
in [orka.md](orka.md), not a ruling that rules out future integration.

## A migrated application's turn still fails

Check the actual model route and the application's model identifier,
then the bridge credential, Orka ServiceAccount token, Provider and ledger.
A successful HTTP response alone is not a completed application turn.
The [migration limits](migrate.md#8-limits-stated) cover Responses translation,
continuation incompatibilities, token renewal and owner-applied patches.
A Helm upgrade can overwrite a one-off patch: retain the change in the
application owner's deployment source rather than assuming kmx owns it.

## Existing kagent and plane troubleshooting

The following describes retained legacy code, not Orka's contracts.

### Empty replies and `input-required`

The kagent runtime can ask a human a question or request a tool approval.
That is not an answer. `kmx agent chat --interactive <agent>` handles the
pending human step. The small-model path may re-sample a question-only
response up to twice, but not after a tool call or with an explicit session;
there is no guarantee that a system instruction suppresses questions.
Malformed `ask_user` arguments from smaller models can also fail invocation.
The committed keyless model is `qwen2.5:3b`; test any replacement by invoking it.

### The tool worked but the answer is wrong

A model may misquote valid tool results. Compare the task's actual tool
call/result with the prose summary. CI asserting a tool path is not proof
that a model reasons correctly or copies identifiers faithfully.

### Hosted model authentication fails

Create the required Secret before selecting a model preset. Copilot uses
its own device flow, not the `gh` CLI's token, and its short-lived token
needs renewal. The endpoint is not a stable public GitHub API contract.
See [models.md](models.md) for current capture and switching commands.
Never put token values in command arguments or committed YAML.

### What do plane error codes mean?

| Code | Check |
|---|---|
| 401 | Missing, expired or invalid plane credential. Inspect the response's stated cause. |
| 403 | An authenticated request is outside permitted routing or policy, or its cost cannot be admitted under the configured budget. |
| 429 | The monthly token or money budget is exhausted. Runtime retries may create multiple denied rows. |
| 502 | Upstream transport/protocol failure, including a response with no readable usage. An admitted attempt is not proof of downstream success. |
| 503 | A dependency needed for custody, authentication or accounting is unavailable; restore that dependency rather than bypassing enforcement. |

[Spend](spend.md) explains the actual model protocols and refusal boundaries.
An unpriced subscription model is not a free model; use token budgets where
no defensible money price exists.

### A governed kagent agent vanished from the ledger

Inspect its live `spec.declarative.modelConfig`. Reapplying an ungoverned
manifest outside the preserving command path can change routing without
preventing chat. Use `kmx govern <agent>` only if legacy plane governance
is the intended route. It is not the Orka migration command.

### I lost a plane token or ledger

The plane stores a token hash, not a recoverable token. Follow the refusal
and recovery instructions printed by the owning command; reissuing a
credential may require restoring its budget. See [identity.md](identity.md).

Pod restarts are not cluster deletion. Deleting kind removes its Postgres
volume and ledger; take a backup first when data matters. See
[operations.md](operations.md). The local Ollama model cache is an
`emptyDir`, so a pod restart may require pulling models again.
