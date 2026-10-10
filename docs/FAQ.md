# FAQ and troubleshooting

Start with [getting started](getting-started.md) for Orka installation, native
Agent creation, chat and bundle lifecycle.

## Why is `kmx orka` missing?

Check `kmx version` and which binary is on your `PATH`. The historical v0.1.0
release predates Orka; v0.2.0 and newer provide these commands. Install the
pinned release as described in [getting started](getting-started.md), then
check `kmx orka --help`. `@latest` and the installer default select the newest
published tag, not the tip of `main`.

## Does installing Orka govern my application?

No. Installation creates platform resources and, by default, a local Provider.
It does not adopt an existing application or redirect its traffic. Author an
Agent with [`kmx agent create`](kmx.md#kmx-agent-create); use a Task to prove an
answer. Execution and enforcement belong to the selected runtime.

## Why does status show a version different from the pin?

`kmx orka status` reads the running controller image. The pin is what kmx
would install, not proof of what someone else installed. An unreadable
cluster is not an empty cluster. See [Orka installation](orka.md) for
inspection, no-write planning, dry-run and upgrade limits.

## What Kagent support exists now?

Orka remains the default. The only current Kagent capability is explicit
`kmx agent create --runtime kagent <name>` against an already-installed exact
v0.10.2. KMX never installs or upgrades it. This path renders a review artifact
and portable bundle; `--out -` omits the bundle unless `--bundle-path` is set.
Offline rendering needs neither an installation nor a cluster Secret. Online
creation requires both and can create a new ModelConfig then a new Agent; it
does not adopt/update existing objects or roll back partial writes. An optional
`--task` sends one A2A message as the final step of that same create invocation;
it is not a standalone or resumable chat capability.

No Kagent chat, list, show, status, lift, evaluate, console, interactive
`/lift`, quickstart, `up`, installer, or AKS payload was restored. Those
commands remain Orka-only where applicable, and historical lift records remain
teardown-only. See the [Kagent create contract](kmx.md#explicit-kagent-v0102-create).

There is still no supported translation from Kagent YAML into an Orka Agent or
BYO image. Native Orka resources remain the recommendation in
[orka.md](orka.md); a general cross-runtime authoring/lifecycle surface remains
open and unsupported.

## Why did my Kagent create rerun refuse existing objects?

Kagent create is intentionally create-only. A failure can leave a ModelConfig
or Agent that was created before the later check failed, and KMX performs no
rollback. Inspect the named resources and their ownership/readiness, then remove
only what you deliberately want recreated. A rerun will not adopt, reconcile,
update, or delete them for you.

If `--task` was used and the result was ambiguous, assume the message may have
executed: KMX sends once and never retries ambiguity. Kagent stores the full
prompt/history/answer in its database with unlimited upstream default session
retention, and cluster audit policy may also capture Service-proxy bodies.
Trusted-proxy task submission is unsupported because KMX accepts no Kagent
bearer credential.

## Model and tool troubleshooting

### Empty replies and `input-required`

This was the former Kagent chat path's human-in-the-loop state. The current
create-only task path accepts only a completed answer and has no continuation
operation for `input-required`; Orka turns likewise complete or fail.
Historical note, for transcripts that still show it: the small-model path
used to re-sample a question-only response up to twice, but not after a tool call or with
an explicit session;
there is no guarantee that a system instruction suppresses questions.
Malformed `ask_user` arguments from smaller models can also fail invocation.
The committed keyless model is `qwen2.5:3b`; test any replacement by invoking it.

### The tool worked but the answer is wrong

A model may misquote valid tool results. Compare the task's actual tool
call/result with the prose summary. CI asserting a tool path is not proof
that a model reasons correctly or copies identifiers faithfully.

### Hosted model authentication fails

For native Orka, provision the Provider's named Secret and key separately,
without a trailing newline. Provider readiness is not proof of a successful
model call. Inspect the Task and the model/deployment identifier as well as
Provider and Agent readiness. See [models.md](models.md) for native configuration.

Host Copilot authentication is managed by the installed Copilot CLI, not a KMX
cluster token exchange; see [Copilot inference](copilot-inference.md). Host
Foundry uses its own endpoint and credential path; see
[Foundry inference](local-foundry-inference.md). Never put token values in
command arguments or committed YAML.

### A local model disappeared after a restart

The local Ollama model cache is an `emptyDir`; a pod restart may require another
model pull. Pod restarts are not cluster deletion. `kmx down` removes the whole
named kind cluster and its persistent data. Export needed resources, Secrets and
volumes before replacing it; see [Orka's data-retention limits](orka.md#limits-stated).
