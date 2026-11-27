# Resident command tools: setup and boundaries

Unreleased Unix/WSL support. A resident fake/Copilot agent sends typed tool calls
to a sibling broker; the broker launches the configured command. Model credentials
stay in the model worker, and the broker/commands receive no inherited credential
environment. Other model endpoint routes remain tool-free in this slice.

The implemented backend is **bounded host-user execution**, not a sandbox.
Commands retain the user's filesystem and inbound/outbound network authority.
Review `kmx.yaml`, including activation probes, as executable host configuration.
An empty environment does not prevent a program from reading host credential files.

For a fuller workflow, try the [project-maintenance demo](project-maintenance-demo.md):
monitor mocked PRs/issues, build a report and rebase disposable local worktrees,
including dirty-worktree and conflict outcomes.

## Run a small offline demo

This example uses `/usr/bin/touch` and a deterministic fake model. It verifies real
command/probe effects without a hosted model, cluster, container runtime or key.
Build the PR's CLI first and put that binary on PATH as `kmx`.

```bash
mkdir broker-demo
cd broker-demo
kmx create broker-demo
```

Replace `broker-demo.agent.md` with:

```markdown
---
name: broker-demo
model: fake
tools: [mark_run]
---
Call the tool `mark_run`. Reply with exactly `Marker created.`
```

Create `kmx.yaml` beside it:

```yaml
tools:
  mark_run: Enabled
commandTools:
  mark_run:
    command: /usr/bin/touch
    args: [agent-ran.txt]
    workingDirectory: .
    checks:
      readiness:
        args: [agent-probe.txt]
        timeout: 2s
        expect:
          exitCode: 0
```

`mark_run` has empty-object input and fixed arguments. The readiness check
creates `agent-probe.txt`; the ordinary call creates `agent-ran.txt`. There is
no shell redirection or model-selected executable.

```bash
kmx run broker-demo.agent.md --check-tools
# mark_run  readiness  passed

kmx run broker-demo.agent.md
# Starts the worker/broker, repeats the probe, and sends no model turn.
kmx list -o json

kmx chat broker-demo --runtime local "Run the tool."
# Marker created.
ls agent-ran.txt
```

Inventory JSON reports the source/instance IDs, both backends, and separate
config, contract, binding and complete plan digests. The fake answer is scripted;
the file check confirms the real command's effect.

Replace `broker-demo.agent.test.md` with:

````markdown
---
schemaVersion: 1
---

```yaml agent-test
id: marker
input: Run the tool.
assertions:
  - id: called
    type: toolCalled
    tool: mark_run
  - id: answered
    type: contains
    value: Marker created.
```
````

```bash
kmx test broker-demo.agent.md --model fake
kmx stop broker-demo
kmx list --all
```

Test uses the current-source, one-shot preview path. Its assertions are not proof
of broker isolation, durable Sessions recovery, or deployability. Broker-specific
process and security regressions live in `internal/kmx/localagent` and
`internal/kmx/residenttools`.

For real inference, choose an exact tool-capable model from
`kmx model list --provider copilot`, then start with
`kmx run broker-demo.agent.md --provider copilot --model MODEL_ID`. If a generation
is already active, use an explicit upgrade or a new runtime name. Copilot retains
its catalog tool-support checks. No implicit model or backend fallback occurs.

## What is enforced

- Agent-declared tool names must exactly match enabled command bindings. Missing,
  disabled, MCP-backed or environment-referencing resident bindings refuse.
- The model sees checked Sessions tool definitions, not resolved commands,
  argument templates, probe definitions or host paths from the binding.
- Startup transfers a bounded frozen plan through private descriptors. Its
  contract must match its actual schemas, and its digest binds the complete
  resolved inputs. Unsupported isolation backends and credential resolvers refuse.
- Command executable content and file/directory identity are rechecked. This is
  cooperative drift detection, not atomic executable attestation against a hostile
  concurrent filesystem writer.
- Reviewed dynamic arguments become individual argv elements: enums/full-value
  regex strings, lexical local paths or bounded integers. Programs may interpret
  arguments themselves; there is no implicit shell and no OS containment.
- Commands get an explicit empty environment, 5 KiB combined stdout/stderr,
  terminal normalization and credential-shape checks. The provider's exact-key
  content checker also fences model arguments and results. These checks are not a
  guarantee that arbitrary secrets can be recognized.
- Default budgets are one concurrent call, five per turn, 64 per session, 1,024
  per broker instance and 30 seconds per call. Admitted failed/cancelled calls
  consume budget. Repeated session/key pairs refuse without executing again.
- Startup accounts for all declared check timeouts plus cleanup/headroom. EOF or
  cancellation cancels probes and owned process groups. Broker descriptors are
  close-on-exec before command launch. Stop requires acknowledged broker cleanup;
  uncertain cleanup stays uncertain on repeated Close calls.

Processes that deliberately escape a process group are outside group cleanup's
guarantee. Tool effects can survive lost answer delivery; inspect external state
before explicitly trying again. `--check-tools` executes probes, rather than being
a syntax-only validator. Local effects/results grant no package or Deploy authority.

## Mapping to Agent Primitives

The mapping follows the pinned **Sessions v0.2.0** and **AgentSuite v0.7.0** concepts:

| Concept | Implementation here | Boundary |
|---|---|---|
| Agent instructions and tool requirements | MD source plus `tools: [...]` | Parsing a name does not resolve or authorize an executable. |
| ToolProvider and callable Tools | Private `ToolContract`: `local-command@1.0.0`, sorted names and JSON input schemas | Logical counterpart of Suite's provider/tool split; not a public ToolProvider manifest. |
| Effects and approval | Conservative `mutating`, `policy-controlled` | Commands make no read-only guarantee. Policy is the operator-owned binding and dispatch checks; no interactive approval resolver is implemented. |
| Runtime/environment binding | Private `LocalBinding`, separate agent/tool runtime selections | Host execution paths, binaries and actual exposure belong here, outside model definitions and portable content. |
| Model-facing definition | `sessions/api.ToolDefinition` | One checked definition per callable name, from the frozen host plan. |
| Host-executed call/result | `api.ToolCall`, `api.ToolResult`, `controller.ToolCallContext`, `CONTROLLER_MEDIATED` | Broker enforces schema, limits, scope and execution. A nonzero process exit is a completed observation with `succeeded: false`; incomplete execution is an error. |
| Idempotency scope | `(SessionUID, IdempotencyKey)` | In-memory duplicate refusal for the broker lifetime. No automatic replay or cross-restart exactly-once claim. |
| Evidence and packaging | Separate existing AgentSuite pipeline | No public artifact, deployment binding, evaluation receipt or durable Sessions event journal is produced here. |

The private contract/binding versions use `kmx.*` namespaces and private JSON
digests. They must not be relabeled as AgentSuite manifests or JCS artifact
identities. Export would require proper content-addressed schemas/implementation
descriptors, runtime qualification and the existing evaluation/publication gates.
Likewise, reusing Sessions API types is not a claim that this resident loop hosts
the full Sessions service, its durable intent/result journal, replay or approvals.

References: [Sessions tool mediation and recovery](https://github.com/agent-primitives/sessions/blob/v0.2.0/spec.md),
[AgentSuite ToolProvider and runtime concepts](https://github.com/agent-primitives/suite/blob/v0.7.0/spec.md).
