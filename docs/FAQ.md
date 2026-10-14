# FAQ and troubleshooting

Start with [getting started](getting-started.md) for installation and a first
answer and [suite workspace](suite-workspace.md) for author/eval/push/digest Lift.

## Why is `kmx orka` missing?

Check `kmx version` and which binary is on your `PATH`. The historical v0.1.0
release predates Orka; v0.2.0 and newer provide these commands. Install the
pinned release as described in [getting started](getting-started.md), then
check `kmx orka --help`. `@latest` and the installer default select the newest
published tag, not the tip of `main`.

## Does installing Orka govern my application?

No. Installation creates platform resources and, by default, a local Provider.
It does not adopt an existing application or redirect its traffic. Author an
native workspace member with [`kmx create`](kmx.md#kmx-create), then evaluate,
publish and Lift with private evidence. Placement readiness is not an answer. Execution and enforcement belong to the selected runtime.

## Why does status show a version different from the pin?

`kmx orka status` reads the running controller image. The pin is what kmx
would install, not proof of what someone else installed. An unreadable
cluster is not an empty cluster. See [Orka installation](orka.md) for
inspection, no-write planning, dry-run and upgrade limits.

## What kagent support exists now?

Orka remains default. Explicit native kagent workspaces use exact v0.10.3
Go/linux-amd64: create, `eval --bindings FILE`, evaluated push, native receipt
diff and create-only digest Lift. Every occupied target refuses, even with
matching evaluated UIDs. Eval is unsecure-only, one fresh blocking A2A Task per
case, never retried; trusted-proxy refuses before admission/send. No Python,
RemoteMCP, standalone run/chat, suite status/retirement, installer/discovery,
console/quickstart or AKS kagent payload is implied. See [native workspace
profiles](suite-workspace.md) and [runtime boundaries](runtime-adapters.md).

Live v0.10.2 create and bundle lift/status/evaluation/replay/readers/receipts are
removed outright. Old receipts are unreadable; no conversion/migration or deletion
of saved files/resources occurs. Reference extraction/export remains independent,
without native RecordedExecution or a manifest consumer. Author native cases.
Partial placement may leave resources; no retry/adoption/rollback is implied.

## Model and tool troubleshooting

### Empty replies and `input-required`

This was the former Kagent chat path's human-in-the-loop state. The current
packaged native evaluation accepts only completed answers and has no continuation
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
model pull. Pod restarts are not cluster deletion. `kmx local down` removes the whole
named kind cluster and its persistent data. Export needed resources, Secrets and
volumes before replacing it; see [Orka's data-retention limits](orka.md#limits-stated).
